package rocketchat

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestListRoomsUsesAuthHeadersAndNormalizedBaseURL(t *testing.T) {
	var seenPath string
	var authToken string
	var userID string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPath = r.URL.Path
		authToken = r.Header.Get("X-Auth-Token")
		userID = r.Header.Get("X-User-Id")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"update":[{"_id":"r1","name":"general"}]}`))
	}))
	defer server.Close()

	client := New(ClientOptions{
		BaseURL:   server.URL + "/",
		UserID:    "user-123",
		AuthToken: "token-abc",
		Timeout:   30 * time.Second,
	})

	rooms, err := client.ListRooms(context.Background())
	if err != nil {
		t.Fatalf("ListRooms returned error: %v", err)
	}
	if seenPath != "/api/v1/rooms.get" {
		t.Fatalf("path = %q", seenPath)
	}
	if authToken != "token-abc" || userID != "user-123" {
		t.Fatalf("auth headers = %q/%q", authToken, userID)
	}
	if len(rooms) != 1 || rooms[0].ID != "r1" || rooms[0].Name != "general" {
		t.Fatalf("rooms = %#v", rooms)
	}
}

func TestCleanRoomHistoryUsesExpectedBody(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/rooms.cleanHistory" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Fatalf("method = %q", r.Method)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("Decode body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"message":{"_id":"message-1","rid":"room-1"}}`))
	}))
	defer server.Close()

	client := New(ClientOptions{
		BaseURL:   server.URL,
		UserID:    "user-123",
		AuthToken: "token-abc",
		Timeout:   30 * time.Second,
	})

	err := client.CleanRoomHistory(context.Background(), "room-1", CleanRoomHistoryOptions{
		Oldest:           "1970-01-01T00:00:00.000Z",
		Latest:           "2026-06-07T12:00:00.000Z",
		IgnoreDiscussion: false,
		IgnoreThreads:    true,
		ExcludePinned:    true,
	})
	if err != nil {
		t.Fatalf("CleanRoomHistory returned error: %v", err)
	}

	assertBody(t, body, "roomId", "room-1")
	assertBody(t, body, "oldest", "1970-01-01T00:00:00.000Z")
	assertBody(t, body, "latest", "2026-06-07T12:00:00.000Z")
	assertBody(t, body, "inclusive", true)
	assertBody(t, body, "limit", float64(0))
	assertBody(t, body, "ignoreDiscussion", false)
	assertBody(t, body, "ignoreThreads", true)
	assertBody(t, body, "excludePinned", true)
}

func TestListMessagesUsesHistoryEndpointForRoomType(t *testing.T) {
	var seenPaths []string
	var seenQueries []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPaths = append(seenPaths, r.URL.Path)
		seenQueries = append(seenQueries, r.URL.RawQuery)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"messages":[{"_id":"m1","rid":"r1"}],"count":1,"offset":0,"total":1}`))
	}))
	defer server.Close()

	client := New(ClientOptions{
		BaseURL:   server.URL,
		UserID:    "user-123",
		AuthToken: "token-abc",
		Timeout:   30 * time.Second,
	})

	cases := []struct {
		room rocketchatRoom
		path string
	}{
		{room: rocketchatRoom{ID: "c1", Type: "c"}, path: "/api/v1/channels.history"},
		{room: rocketchatRoom{ID: "p1", Type: "p"}, path: "/api/v1/groups.history"},
		{room: rocketchatRoom{ID: "d1", Type: "d"}, path: "/api/v1/im.history"},
	}
	for _, tc := range cases {
		messages, page, err := client.ListMessages(context.Background(), Room{ID: tc.room.ID, Type: tc.room.Type}, ListMessagesOptions{
			Offset:             5,
			Count:              25,
			IncludeThreads:     true,
			IncludeDiscussions: true,
		})
		if err != nil {
			t.Fatalf("ListMessages returned error: %v", err)
		}
		if len(messages) != 1 || messages[0].ID != "m1" || page.Total != 1 {
			t.Fatalf("messages/page = %#v/%#v", messages, page)
		}
	}

	for i, wantPath := range []string{"/api/v1/channels.history", "/api/v1/groups.history", "/api/v1/im.history"} {
		if seenPaths[i] != wantPath {
			t.Fatalf("path[%d] = %q, want %q", i, seenPaths[i], wantPath)
		}
		if !strings.Contains(seenQueries[i], "roomId=") || !strings.Contains(seenQueries[i], "count=25") || !strings.Contains(seenQueries[i], "offset=5") {
			t.Fatalf("query[%d] = %q", i, seenQueries[i])
		}
	}
}

func TestDeleteMessageUsesChatDelete(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/chat.delete" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Fatalf("method = %q", r.Method)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("Decode body: %v", err)
		}
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer server.Close()

	client := New(ClientOptions{
		BaseURL:   server.URL,
		UserID:    "user-123",
		AuthToken: "token-abc",
		Timeout:   30 * time.Second,
	})

	if err := client.DeleteMessage(context.Background(), "room-1", "message-1"); err != nil {
		t.Fatalf("DeleteMessage returned error: %v", err)
	}

	assertBody(t, body, "roomId", "room-1")
	assertBody(t, body, "msgId", "message-1")
	assertBody(t, body, "asUser", true)
}

func TestDebugLoggingShowsDeleteRequestAndResponseWithoutToken(t *testing.T) {
	var debug bytes.Buffer
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer server.Close()

	client := New(ClientOptions{
		BaseURL:     server.URL,
		UserID:      "user-123",
		AuthToken:   "secret-token",
		Timeout:     30 * time.Second,
		Debug:       true,
		DebugWriter: &debug,
	})

	if err := client.DeleteMessage(context.Background(), "room-1", "message-1"); err != nil {
		t.Fatalf("DeleteMessage returned error: %v", err)
	}

	output := debug.String()
	for _, want := range []string{
		"[debug] delete start roomId=room-1 msgId=message-1",
		"[debug] request POST /api/v1/chat.delete",
		"[debug] response POST /api/v1/chat.delete status=200 success=true",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("debug output missing %q: %s", want, output)
		}
	}
	if strings.Contains(output, "channels.history") || strings.Contains(output, "rooms.get") {
		t.Fatalf("debug output included non-delete requests: %s", output)
	}
	if strings.Contains(output, "secret-token") {
		t.Fatalf("debug output leaked token: %s", output)
	}
	if strings.Contains(output, "Unknown error") {
		t.Fatalf("debug output treated successful response as an error: %s", output)
	}
}

func TestAPIErrorForUnsuccessfulResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"success":false,"error":"Not allowed","errorType":"error-not-allowed"}`))
	}))
	defer server.Close()

	client := New(ClientOptions{
		BaseURL:   server.URL,
		UserID:    "user-123",
		AuthToken: "token-abc",
		Timeout:   30 * time.Second,
	})

	err := client.CleanRoomHistory(context.Background(), "room-1", CleanRoomHistoryOptions{
		Oldest:           "1970-01-01T00:00:00.000Z",
		Latest:           "2026-06-07T12:00:00.000Z",
		IgnoreDiscussion: true,
		IgnoreThreads:    true,
		ExcludePinned:    false,
	})
	if err == nil {
		t.Fatal("expected error")
	}
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("err type = %T", err)
	}
	if apiErr.Method != http.MethodPost || apiErr.Endpoint != "/api/v1/rooms.cleanHistory" || apiErr.StatusCode != 403 {
		t.Fatalf("apiErr = %#v", apiErr)
	}
	if !strings.Contains(apiErr.Error(), "Not allowed") {
		t.Fatalf("error = %q", apiErr.Error())
	}
}

func TestSuccessFalseOnOKResponseIsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":false,"error":"No permission"}`))
	}))
	defer server.Close()

	client := New(ClientOptions{
		BaseURL:   server.URL,
		UserID:    "user-123",
		AuthToken: "token-abc",
		Timeout:   30 * time.Second,
	})

	_, err := client.ListRooms(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "status 200: No permission") {
		t.Fatalf("err = %q", err.Error())
	}
}

func assertBody(t *testing.T, body map[string]any, key string, want any) {
	t.Helper()
	if body[key] != want {
		t.Fatalf("body[%s] = %#v, want %#v", key, body[key], want)
	}
}

type rocketchatRoom struct {
	ID   string
	Type string
}
