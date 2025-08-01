package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"google.golang.org/api/drive/v3"
	"google.golang.org/api/option"
)

// TestFindOrCreateFolder tests the findOrCreateFolder function.
func TestFindOrCreateFolder(t *testing.T) {
	// --- Test Case 1: Folder already exists ---
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// This handler simulates the Google Drive API.
		// For the "list" request, it returns a pre-defined folder.
		if r.URL.Path == "/files" {
			w.Header().Set("Content-Type", "application/json")
			response := &drive.FileList{
				Files: []*drive.File{
					{Id: "existing_folder_id", Name: "Test Folder"},
				},
			}
			json.NewEncoder(w).Encode(response)
			return
		}
		// Any other request in this test case is unexpected.
		t.Errorf("Unexpected request to %s", r.URL.Path)
	}))
	defer server.Close()

	// Create a drive service that points to our mock server
	ctx := context.Background()
	driveService, err := drive.NewService(ctx, option.WithEndpoint(server.URL), option.WithoutAuthentication())
	if err != nil {
		t.Fatalf("Failed to create mock drive service: %v", err)
	}

	// Run the function
	folderID, err := findOrCreateFolder(driveService, "Test Folder", "root")
	if err != nil {
		t.Errorf("Expected no error, but got: %v", err)
	}
	if folderID != "existing_folder_id" {
		t.Errorf("Expected folder ID 'existing_folder_id', but got: '%s'", folderID)
	}

	// --- Test Case 2: Folder does not exist and needs to be created ---
	createCalled := false
	server2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// First, the function will try to list files. Return an empty list.
		if r.Method == "GET" && r.URL.Path == "/files" {
			response := &drive.FileList{Files: []*drive.File{}}
			json.NewEncoder(w).Encode(response)
			return
		}
		// Next, the function should try to create the folder.
		if r.Method == "POST" && r.URL.Path == "/files" {
			createCalled = true
			response := &drive.File{Id: "new_folder_id", Name: "New Folder"}
			json.NewEncoder(w).Encode(response)
			return
		}
		t.Errorf("Unexpected request: %s %s", r.Method, r.URL.Path)
	}))
	defer server2.Close()

	driveService2, err := drive.NewService(ctx, option.WithEndpoint(server2.URL), option.WithoutAuthentication())
	if err != nil {
		t.Fatalf("Failed to create mock drive service: %v", err)
	}

	// Run the function
	folderID2, err := findOrCreateFolder(driveService2, "New Folder", "root")
	if err != nil {
		t.Errorf("Expected no error, but got: %v", err)
	}
	if !createCalled {
		t.Error("Expected the create folder API to be called, but it was not.")
	}
	if folderID2 != "new_folder_id" {
		t.Errorf("Expected folder ID 'new_folder_id', but got: '%s'", folderID2)
	}
}
