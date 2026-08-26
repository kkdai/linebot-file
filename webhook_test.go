package main

import (
	"testing"
	"time"

	"github.com/line/line-bot-sdk-go/v8/linebot/webhook"
)

func TestParseCommand(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		wantName string
		wantArg  string
		wantOK   bool
	}{
		{"connect", "/connect_drive", cmdConnect, "", true},
		{"reconnect", "/reconnect", cmdReconnect, "", true},
		{"disconnect", "/disconnect_drive", cmdDisconnect, "", true},
		{"recent files", "/recent_files", cmdRecent, "", true},

		{"search with argument", "/search_files 照片", cmdSearch, "照片", true},
		{"short search with argument", "/q 照片", cmdSearchShort, "照片", true},
		{"search with multi word argument", "/q holiday photos", cmdSearchShort, "holiday photos", true},
		{"search with no argument", "/search_files", cmdSearch, "", true},
		{"short search with no argument", "/q", cmdSearchShort, "", true},
		{"search with trailing space only", "/q   ", cmdSearchShort, "", true},

		// Regression: prefix matching used to turn "/quit" into a search for
		// "it", and "/search_filesXYZ" into a command.
		{"lookalike quit", "/quit", "", "", false},
		{"lookalike qq", "/qq test", "", "", false},
		{"lookalike search prefix", "/search_filesXYZ", "", "", false},

		{"plain text", "hello", "", "", false},
		{"unknown command", "/unknown", "", "", false},
		{"empty", "", "", "", false},
		{"leading whitespace is trimmed", "  /recent_files  ", cmdRecent, "", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			name, arg, ok := parseCommand(tc.text)
			if ok != tc.wantOK || name != tc.wantName || arg != tc.wantArg {
				t.Errorf("parseCommand(%q) = (%q, %q, %v), want (%q, %q, %v)",
					tc.text, name, arg, ok, tc.wantName, tc.wantArg, tc.wantOK)
			}
		})
	}
}

func TestUserIDFromSource(t *testing.T) {
	tests := []struct {
		name   string
		source webhook.SourceInterface
		wantID string
		wantOK bool
	}{
		// The SDK unmarshals sources as values, not pointers.
		{"user value", webhook.UserSource{UserId: "U123"}, "U123", true},
		{"user pointer", &webhook.UserSource{UserId: "U123"}, "U123", true},
		{"user with empty id", webhook.UserSource{}, "", false},

		// Regression: these used to be unchecked assertions that panicked.
		{"group", webhook.GroupSource{GroupId: "G123"}, "", false},
		{"room", webhook.RoomSource{RoomId: "R123"}, "", false},
		{"nil", nil, "", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			id, ok := userIDFromSource(tc.source)
			if id != tc.wantID || ok != tc.wantOK {
				t.Errorf("userIDFromSource(%T) = (%q, %v), want (%q, %v)",
					tc.source, id, ok, tc.wantID, tc.wantOK)
			}
		})
	}
}

func TestHandledEventsDeduplicates(t *testing.T) {
	h := &handledEvents{seen: make(map[string]time.Time)}

	if !h.markHandled("event-1") {
		t.Error("first delivery of event-1 should be handled")
	}
	if h.markHandled("event-1") {
		t.Error("redelivery of event-1 should be skipped")
	}
	if !h.markHandled("event-2") {
		t.Error("a different event should be handled")
	}

	// Events with no ID cannot be deduplicated, so they always run.
	if !h.markHandled("") || !h.markHandled("") {
		t.Error("events without an ID should always be handled")
	}
}

func TestHandledEventsExpires(t *testing.T) {
	h := &handledEvents{seen: map[string]time.Time{
		"old": time.Now().Add(-2 * eventTTL),
	}}

	if !h.markHandled("old") {
		t.Error("an entry older than eventTTL should have been forgotten")
	}
}
