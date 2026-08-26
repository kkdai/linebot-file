// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"google.golang.org/api/drive/v3"
)

// escapeDriveQuery escapes a value for interpolation inside the single quotes
// of a Drive API query. Backslashes must be escaped before quotes, otherwise
// the escape character itself gets escaped twice.
//
// Without this a search term containing an apostrophe breaks the query, and a
// crafted term could inject extra query clauses.
func escapeDriveQuery(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return strings.ReplaceAll(s, `'`, `\'`)
}

// findFolder returns the ID of the named folder under parentID, or an empty
// string when it does not exist.
func findFolder(ctx context.Context, srv *drive.Service, name, parentID string) (string, error) {
	query := fmt.Sprintf("mimeType='%s' and trashed=false and name='%s' and '%s' in parents",
		folderMimeType, escapeDriveQuery(name), escapeDriveQuery(parentID))

	r, err := srv.Files.List().Q(query).PageSize(1).Fields("files(id)").Context(ctx).Do()
	if err != nil {
		return "", fmt.Errorf("failed to search for folder %q: %w", name, err)
	}
	if len(r.Files) == 0 {
		return "", nil
	}
	return r.Files[0].Id, nil
}

// findOrCreateFolder returns the ID of the named folder under parentID,
// creating it when it does not exist yet.
func findOrCreateFolder(ctx context.Context, srv *drive.Service, name, parentID string) (string, error) {
	id, err := findFolder(ctx, srv, name, parentID)
	if err != nil {
		return "", err
	}
	if id != "" {
		return id, nil
	}

	created, err := srv.Files.Create(&drive.File{
		Name:     name,
		MimeType: folderMimeType,
		Parents:  []string{parentID},
	}).Fields("id").Context(ctx).Do()
	if err != nil {
		return "", fmt.Errorf("failed to create folder %q: %w", name, err)
	}
	return created.Id, nil
}

// uploadParents returns every folder an upload can live in: the root folder
// plus its YYYY-MM subfolders. It returns nil when the root folder does not
// exist, i.e. the user has never uploaded anything. Read paths must not
// create folders in the user's Drive as a side effect.
func uploadParents(ctx context.Context, srv *drive.Service) ([]string, error) {
	rootID, err := findFolder(ctx, srv, rootFolderName, "root")
	if err != nil || rootID == "" {
		return nil, err
	}

	query := fmt.Sprintf("'%s' in parents and trashed=false and mimeType='%s'",
		escapeDriveQuery(rootID), folderMimeType)
	r, err := srv.Files.List().Q(query).Fields("files(id)").Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("failed to list upload subfolders: %w", err)
	}

	parents := make([]string, 0, len(r.Files)+1)
	parents = append(parents, rootID)
	for _, f := range r.Files {
		parents = append(parents, f.Id)
	}
	return parents, nil
}

// listFiles searches every parent in one request rather than one request per
// folder. extraClause, when non-empty, is ANDed into the query.
func listFiles(ctx context.Context, srv *drive.Service, parents []string, extraClause string, limit int) ([]*drive.File, error) {
	if len(parents) == 0 {
		return nil, nil
	}

	clauses := make([]string, 0, len(parents))
	for _, p := range parents {
		clauses = append(clauses, fmt.Sprintf("'%s' in parents", escapeDriveQuery(p)))
	}
	// Exclude folders: the YYYY-MM subfolders are themselves children of the
	// root folder and would otherwise show up as results.
	query := fmt.Sprintf("(%s) and trashed=false and mimeType != '%s'",
		strings.Join(clauses, " or "), folderMimeType)
	if extraClause != "" {
		query += " and " + extraClause
	}

	r, err := srv.Files.List().
		Q(query).
		PageSize(int64(limit)).
		OrderBy("createdTime desc").
		Fields("files(id, name, webViewLink, createdTime)").
		Context(ctx).
		Do()
	if err != nil {
		return nil, fmt.Errorf("failed to list files: %w", err)
	}

	return sortAndTrimFiles(r.Files, limit), nil
}

// sortAndTrimFiles orders files newest first and keeps at most limit of them.
// Drive reports createdTime as an RFC 3339 UTC timestamp, which orders
// correctly as a plain string comparison.
func sortAndTrimFiles(files []*drive.File, limit int) []*drive.File {
	slices.SortStableFunc(files, func(a, b *drive.File) int {
		return cmp.Compare(b.CreatedTime, a.CreatedTime)
	})
	return files[:min(len(files), limit)]
}

// getRecentFiles returns the most recently uploaded files across the root
// folder and all of its month subfolders.
func getRecentFiles(ctx context.Context, srv *drive.Service, count int) ([]*drive.File, error) {
	parents, err := uploadParents(ctx, srv)
	if err != nil {
		return nil, err
	}
	return listFiles(ctx, srv, parents, "", count)
}

// searchFilesInDrive returns uploads whose name contains searchQuery.
func searchFilesInDrive(ctx context.Context, srv *drive.Service, searchQuery string) ([]*drive.File, error) {
	parents, err := uploadParents(ctx, srv)
	if err != nil {
		return nil, err
	}
	clause := fmt.Sprintf("name contains '%s'", escapeDriveQuery(searchQuery))
	return listFiles(ctx, srv, parents, clause, maxSearchResults)
}

// uploadToDrive stores content as filename under "LINE Bot Uploads/YYYY-MM".
func uploadToDrive(ctx context.Context, content io.Reader, filename, userID string) (*drive.File, error) {
	srv, err := getGoogleDriveService(ctx, userID)
	if err != nil {
		return nil, err
	}

	rootID, err := findOrCreateFolder(ctx, srv, rootFolderName, "root")
	if err != nil {
		return nil, fmt.Errorf("failed to find or create main folder: %w", err)
	}

	monthID, err := findOrCreateFolder(ctx, srv, time.Now().Format("2006-01"), rootID)
	if err != nil {
		return nil, fmt.Errorf("failed to find or create month subfolder: %w", err)
	}

	file := &drive.File{Name: filename, Parents: []string{monthID}}
	return srv.Files.Create(file).Media(content).Fields("id, name, webViewLink").Context(ctx).Do()
}
