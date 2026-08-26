package main

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"google.golang.org/api/googleapi"
)

func TestIsGoogleAuthError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"unauthorized", &googleapi.Error{Code: http.StatusUnauthorized}, true},
		{"forbidden", &googleapi.Error{Code: http.StatusForbidden}, true},
		{"not found", &googleapi.Error{Code: http.StatusNotFound}, false},
		{"server error", &googleapi.Error{Code: http.StatusInternalServerError}, false},
		{
			"wrapped unauthorized",
			fmt.Errorf("failed to list files: %w", &googleapi.Error{Code: http.StatusUnauthorized}),
			true,
		},
		{"revoked refresh token", errors.New(`oauth2: "invalid_grant" token expired`), true},
		{
			"wrapped invalid_grant",
			fmt.Errorf("failed to get token: %w", errors.New("oauth2: invalid_grant")),
			true,
		},
		{"unrelated error", errors.New("connection refused"), false},
		// Short strings used to be walked by a hand-rolled substring loop.
		{"short error", errors.New("nope"), false},
		{"empty error", errors.New(""), false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isGoogleAuthError(tc.err); got != tc.want {
				t.Errorf("isGoogleAuthError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestGenerateStateIsUniqueAndURLSafe(t *testing.T) {
	seen := make(map[string]bool)
	for range 100 {
		state := generateState()
		if state == "" {
			t.Fatal("generateState returned an empty string")
		}
		if seen[state] {
			t.Fatalf("generateState repeated a value: %q", state)
		}
		seen[state] = true

		for _, r := range state {
			isUpper := r >= 'A' && r <= 'Z'
			isDigit := r >= '0' && r <= '9'
			if !isUpper && !isDigit {
				t.Fatalf("state %q contains a character that needs URL escaping: %q", state, r)
			}
		}
	}
}
