package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestChatStoreCRUD(t *testing.T) {
	s := NewChatStore(t.TempDir() + "/chats")

	list, err := s.List()
	if err != nil || len(list) != 0 {
		t.Fatalf("empty list: %v %v", list, err)
	}

	chat, err := s.Create("", "model-a")
	if err != nil {
		t.Fatal(err)
	}
	if chat.Title != "New chat" || chat.ID == "" {
		t.Fatalf("chat = %+v", chat)
	}

	msg := json.RawMessage(`{"role":"user","content":"hi"}`)
	updated, err := s.Put(chat.ID, "Greeting", "", []json.RawMessage{msg})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Title != "Greeting" || updated.Model != "model-a" || len(updated.Messages) != 1 {
		t.Fatalf("updated = %+v", updated)
	}

	got, err := s.Get(chat.ID)
	if err != nil || len(got.Messages) != 1 {
		t.Fatalf("get: %+v %v", got, err)
	}

	list, err = s.List()
	if err != nil || len(list) != 1 || list[0].Messages != 1 {
		t.Fatalf("list: %+v %v", list, err)
	}

	if err := s.Delete(chat.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(chat.ID); err != errChatNotFound {
		t.Fatalf("expected not found, got %v", err)
	}
}

func TestChatStoreRejectsBadIDs(t *testing.T) {
	s := NewChatStore(t.TempDir())
	for _, id := range []string{"", "../../etc/passwd", "ABC", "short", strings.Repeat("a", 80)} {
		if _, err := s.Get(id); err != errChatNotFound {
			t.Errorf("Get(%q) err = %v, want errChatNotFound", id, err)
		}
	}
}

func TestChatsHTTPRoundtrip(t *testing.T) {
	a := newTestAppAPI(t)
	mux := serveAppAPI(a)

	// Create.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/chats", strings.NewReader(`{"title":"First"}`)))
	if rec.Code != 200 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	var chat Chat
	if err := json.Unmarshal(rec.Body.Bytes(), &chat); err != nil {
		t.Fatal(err)
	}

	// Update with messages.
	body := `{"messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"hi"}]}`
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/chats/"+chat.ID, strings.NewReader(body)))
	if rec.Code != 200 {
		t.Fatalf("put: %d %s", rec.Code, rec.Body)
	}

	// List shows one chat with two messages.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/chats", nil))
	var list struct {
		Chats []ChatSummary `json:"chats"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Chats) != 1 || list.Chats[0].Messages != 2 {
		t.Fatalf("list = %+v", list)
	}

	// Delete.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/chats/"+chat.ID, nil))
	if rec.Code != 200 {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/chats/"+chat.ID, nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("get deleted: %d", rec.Code)
	}
}
