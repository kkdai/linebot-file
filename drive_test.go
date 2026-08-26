package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"google.golang.org/api/drive/v3"
	"google.golang.org/api/option"
)

// newMockDrive returns a Drive service pointed at handler.
func newMockDrive(t *testing.T, handler http.HandlerFunc) *drive.Service {
	t.Helper()

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	srv, err := drive.NewService(t.Context(),
		option.WithEndpoint(server.URL), option.WithoutAuthentication())
	if err != nil {
		t.Fatalf("Failed to create mock drive service: %v", err)
	}
	return srv
}

func writeFiles(t *testing.T, w http.ResponseWriter, files ...*drive.File) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(&drive.FileList{Files: files}); err != nil {
		t.Errorf("Failed to encode response: %v", err)
	}
}

func TestEscapeDriveQuery(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "photo", "photo"},
		{"apostrophe", "it's", `it\'s`},
		{"backslash", `a\b`, `a\\b`},
		// The backslash must be escaped first, otherwise the escape character
		// introduced for the quote gets escaped again.
		{"backslash then quote", `a\'b`, `a\\\'b`},
		{"injection attempt", "x' or name contains 'y", `x\' or name contains \'y`},
		{"cjk", "照片", "照片"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := escapeDriveQuery(tc.in); got != tc.want {
				t.Errorf("escapeDriveQuery(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestFindOrCreateFolderExisting(t *testing.T) {
	srv := newMockDrive(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/files" {
			t.Errorf("Unexpected request: %s %s", r.Method, r.URL.Path)
			return
		}
		writeFiles(t, w, &drive.File{Id: "existing_folder_id", Name: "Test Folder"})
	})

	id, err := findOrCreateFolder(t.Context(), srv, "Test Folder", "root")
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}
	if id != "existing_folder_id" {
		t.Errorf("Expected folder ID 'existing_folder_id', got %q", id)
	}
}

func TestFindOrCreateFolderCreates(t *testing.T) {
	createCalled := false
	srv := newMockDrive(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/files":
			writeFiles(t, w)
		case r.Method == http.MethodPost && r.URL.Path == "/files":
			createCalled = true
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(&drive.File{Id: "new_folder_id", Name: "New Folder"})
		default:
			t.Errorf("Unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})

	id, err := findOrCreateFolder(t.Context(), srv, "New Folder", "root")
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}
	if !createCalled {
		t.Error("Expected the create folder API to be called, but it was not.")
	}
	if id != "new_folder_id" {
		t.Errorf("Expected folder ID 'new_folder_id', got %q", id)
	}
}

// findFolder is used on read paths, which must never create folders in the
// user's Drive as a side effect.
func TestFindFolderDoesNotCreate(t *testing.T) {
	srv := newMockDrive(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			t.Error("findFolder must not create anything")
		}
		writeFiles(t, w)
	})

	id, err := findFolder(t.Context(), srv, "Missing", "root")
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}
	if id != "" {
		t.Errorf("Expected an empty ID for a missing folder, got %q", id)
	}
}

func TestSortAndTrimFiles(t *testing.T) {
	files := []*drive.File{
		{Id: "b", CreatedTime: "2025-02-01T00:00:00.000Z"},
		{Id: "a", CreatedTime: "2025-03-01T00:00:00.000Z"},
		{Id: "c", CreatedTime: "2025-01-01T00:00:00.000Z"},
	}

	got := sortAndTrimFiles(files, 2)
	if len(got) != 2 {
		t.Fatalf("Expected 2 files, got %d", len(got))
	}
	if got[0].Id != "a" || got[1].Id != "b" {
		t.Errorf("Expected newest first [a b], got [%s %s]", got[0].Id, got[1].Id)
	}
}

func TestSortAndTrimFilesShorterThanLimit(t *testing.T) {
	got := sortAndTrimFiles([]*drive.File{{Id: "a"}}, 10)
	if len(got) != 1 {
		t.Errorf("Expected 1 file, got %d", len(got))
	}
	if got := sortAndTrimFiles(nil, 10); len(got) != 0 {
		t.Errorf("Expected no files, got %d", len(got))
	}
}

// driveSearchMock serves the three-request shape both read paths use: look up
// the root folder, list its month subfolders, then one combined query. It
// records every q parameter it saw.
type driveSearchMock struct {
	t        *testing.T
	queries  []string
	rootID   string
	monthIDs []string
	result   []*drive.File
}

func (m *driveSearchMock) handler(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	m.queries = append(m.queries, q)

	switch {
	case r.Method == http.MethodPost:
		m.t.Errorf("Read paths must not create anything (POST %s)", r.URL.Path)

	case strings.Contains(q, "name='"+rootFolderName+"'"):
		if m.rootID == "" {
			writeFiles(m.t, w)
			return
		}
		writeFiles(m.t, w, &drive.File{Id: m.rootID, Name: rootFolderName})

	case strings.Contains(q, "mimeType='"+folderMimeType+"'"):
		months := make([]*drive.File, 0, len(m.monthIDs))
		for _, id := range m.monthIDs {
			months = append(months, &drive.File{Id: id})
		}
		writeFiles(m.t, w, months...)

	default:
		writeFiles(m.t, w, m.result...)
	}
}

func TestSearchFilesInDriveSearchesEveryFolderInOneQuery(t *testing.T) {
	mock := &driveSearchMock{
		t:        t,
		rootID:   "root_id",
		monthIDs: []string{"month_1", "month_2"},
		result: []*drive.File{
			{Id: "f1", Name: "holiday.jpg", CreatedTime: "2025-01-01T00:00:00.000Z"},
			{Id: "f2", Name: "holiday2.jpg", CreatedTime: "2025-02-01T00:00:00.000Z"},
		},
	}
	srv := newMockDrive(t, mock.handler)

	files, err := searchFilesInDrive(t.Context(), srv, "holiday")
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("Expected 2 files, got %d", len(files))
	}
	// Newest first, not whatever order the map happened to produce.
	if files[0].Id != "f2" {
		t.Errorf("Expected the newest file first, got %q", files[0].Id)
	}

	// Previously this was one request per subfolder; now it is a single
	// combined query regardless of how many months exist.
	if len(mock.queries) != 3 {
		t.Errorf("Expected 3 Drive requests, got %d: %v", len(mock.queries), mock.queries)
	}

	search := mock.queries[len(mock.queries)-1]
	for _, parent := range []string{"root_id", "month_1", "month_2"} {
		if !strings.Contains(search, "'"+parent+"' in parents") {
			t.Errorf("Search query does not cover parent %q: %s", parent, search)
		}
	}
	if !strings.Contains(search, "name contains 'holiday'") {
		t.Errorf("Search query is missing the search term: %s", search)
	}
	if !strings.Contains(search, "mimeType != '"+folderMimeType+"'") {
		t.Errorf("Search query does not exclude folders: %s", search)
	}
}

func TestSearchFilesInDriveEscapesUserInput(t *testing.T) {
	mock := &driveSearchMock{t: t, rootID: "root_id"}
	srv := newMockDrive(t, mock.handler)

	if _, err := searchFilesInDrive(t.Context(), srv, "it's"); err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	search := mock.queries[len(mock.queries)-1]
	if !strings.Contains(search, `name contains 'it\'s'`) {
		t.Errorf("Search term was not escaped: %s", search)
	}
}

func TestSearchFilesInDriveWithoutRootFolder(t *testing.T) {
	mock := &driveSearchMock{t: t}
	srv := newMockDrive(t, mock.handler)

	files, err := searchFilesInDrive(t.Context(), srv, "holiday")
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}
	if len(files) != 0 {
		t.Errorf("Expected no files, got %d", len(files))
	}
	if len(mock.queries) != 1 {
		t.Errorf("Expected to stop after the root lookup, got %d requests", len(mock.queries))
	}
}

// Uploads live in YYYY-MM subfolders, so listing only the root folder used to
// return the month folders themselves instead of any files.
func TestGetRecentFilesLooksInsideMonthFolders(t *testing.T) {
	mock := &driveSearchMock{
		t:        t,
		rootID:   "root_id",
		monthIDs: []string{"month_1"},
		result:   []*drive.File{{Id: "f1", Name: "a.jpg", CreatedTime: "2025-01-01T00:00:00.000Z"}},
	}
	srv := newMockDrive(t, mock.handler)

	files, err := getRecentFiles(t.Context(), srv, recentFileCount)
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}
	if len(files) != 1 || files[0].Id != "f1" {
		t.Fatalf("Expected the uploaded file, got %+v", files)
	}

	list := mock.queries[len(mock.queries)-1]
	if !strings.Contains(list, "'month_1' in parents") {
		t.Errorf("Recent files query does not look inside month folders: %s", list)
	}
	if !strings.Contains(list, "mimeType != '"+folderMimeType+"'") {
		t.Errorf("Recent files query does not exclude folders: %s", list)
	}
}

// A long-lived account accumulates one YYYY-MM folder per month and will
// eventually exceed a single page of results. Every page has to be collected,
// otherwise the oldest folders silently drop out of search and recent files.
func TestUploadParentsPagesThroughAllSubfolders(t *testing.T) {
	var subfolderRequests int

	srv := newMockDrive(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("q")

		if strings.Contains(q, "name='"+rootFolderName+"'") {
			writeFiles(t, w, &drive.File{Id: "root_id", Name: rootFolderName})
			return
		}

		subfolderRequests++
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("pageToken") == "" {
			json.NewEncoder(w).Encode(&drive.FileList{
				Files:         []*drive.File{{Id: "month_1"}, {Id: "month_2"}},
				NextPageToken: "page-2",
			})
			return
		}
		json.NewEncoder(w).Encode(&drive.FileList{
			Files: []*drive.File{{Id: "month_3"}},
		})
	})

	parents, err := uploadParents(t.Context(), srv)
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	want := []string{"root_id", "month_1", "month_2", "month_3"}
	if !slices.Equal(parents, want) {
		t.Errorf("uploadParents() = %v, want %v", parents, want)
	}
	if subfolderRequests != 2 {
		t.Errorf("Expected 2 subfolder requests (two pages), got %d", subfolderRequests)
	}
}
