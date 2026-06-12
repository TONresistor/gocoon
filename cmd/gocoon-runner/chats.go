package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ChatStore persists conversations as JSON files under <dataDir>/chats/.
// One file per conversation, atomic writes (tmp + rename). Local-only data:
// conversations never leave the machine.
type ChatStore struct {
	dir string
	mu  sync.Mutex
}

// Chat is a stored conversation. The UI owns the message shape; the store
// keeps it opaque enough to survive schema evolution (messages are raw JSON).
type Chat struct {
	ID        string            `json:"id"`
	Title     string            `json:"title"`
	Model     string            `json:"model,omitempty"`
	CreatedAt int64             `json:"created_at"`
	UpdatedAt int64             `json:"updated_at"`
	Messages  []json.RawMessage `json:"messages"`
}

// ChatSummary is the list-view projection.
type ChatSummary struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Model     string `json:"model,omitempty"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
	Messages  int    `json:"messages"`
}

var errChatNotFound = errors.New("chat not found")

// NewChatStore roots the store at dir (created lazily on first write).
func NewChatStore(dir string) *ChatStore {
	return &ChatStore{dir: dir}
}

func (s *ChatStore) path(id string) string {
	return filepath.Join(s.dir, id+".json")
}

// validChatID guards against path traversal: ids are hex strings we mint.
func validChatID(id string) bool {
	if len(id) < 8 || len(id) > 64 {
		return false
	}
	for _, c := range id {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func newChatID() (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// List returns chat summaries, most recently updated first.
func (s *ChatStore) List() ([]ChatSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(s.dir)
	if errors.Is(err, os.ErrNotExist) {
		return []ChatSummary{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := make([]ChatSummary, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		chat, err := s.readLocked(strings.TrimSuffix(name, ".json"))
		if err != nil {
			continue // skip unreadable entries rather than failing the list
		}
		out = append(out, ChatSummary{
			ID:        chat.ID,
			Title:     chat.Title,
			Model:     chat.Model,
			CreatedAt: chat.CreatedAt,
			UpdatedAt: chat.UpdatedAt,
			Messages:  len(chat.Messages),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt > out[j].UpdatedAt })
	return out, nil
}

// Create mints a new conversation.
func (s *ChatStore) Create(title, model string) (*Chat, error) {
	id, err := newChatID()
	if err != nil {
		return nil, err
	}
	now := time.Now().UnixMilli()
	chat := &Chat{
		ID:        id,
		Title:     strings.TrimSpace(title),
		Model:     model,
		CreatedAt: now,
		UpdatedAt: now,
		Messages:  []json.RawMessage{},
	}
	if chat.Title == "" {
		chat.Title = "New chat"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.writeLocked(chat); err != nil {
		return nil, err
	}
	return chat, nil
}

// Get loads one conversation.
func (s *ChatStore) Get(id string) (*Chat, error) {
	if !validChatID(id) {
		return nil, errChatNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readLocked(id)
}

// Put replaces title/model/messages of an existing conversation.
func (s *ChatStore) Put(id string, title, model string, messages []json.RawMessage) (*Chat, error) {
	if !validChatID(id) {
		return nil, errChatNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	chat, err := s.readLocked(id)
	if err != nil {
		return nil, err
	}
	if t := strings.TrimSpace(title); t != "" {
		chat.Title = t
	}
	if model != "" {
		chat.Model = model
	}
	if messages != nil {
		chat.Messages = messages
	}
	chat.UpdatedAt = time.Now().UnixMilli()
	if err := s.writeLocked(chat); err != nil {
		return nil, err
	}
	return chat, nil
}

// Delete removes a conversation.
func (s *ChatStore) Delete(id string) error {
	if !validChatID(id) {
		return errChatNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	err := os.Remove(s.path(id))
	if errors.Is(err, os.ErrNotExist) {
		return errChatNotFound
	}
	return err
}

func (s *ChatStore) readLocked(id string) (*Chat, error) {
	raw, err := os.ReadFile(s.path(id))
	if errors.Is(err, os.ErrNotExist) {
		return nil, errChatNotFound
	}
	if err != nil {
		return nil, err
	}
	var chat Chat
	if err := json.Unmarshal(raw, &chat); err != nil {
		return nil, fmt.Errorf("chat %s: %w", id, err)
	}
	if chat.Messages == nil {
		chat.Messages = []json.RawMessage{}
	}
	return &chat, nil
}

func (s *ChatStore) writeLocked(chat *Chat) error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(chat, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path(chat.ID) + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path(chat.ID))
}

/*
 * HTTP handlers (mounted from AppAPI.routes)
 */

func (a *AppAPI) handleChats(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		list, err := a.chats.List()
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSONValue(w, map[string]any{"chats": list})
	case http.MethodPost:
		var req struct {
			Title string `json:"title"`
			Model string `json:"model"`
		}
		if r.Body != nil {
			_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req)
		}
		chat, err := a.chats.Create(req.Title, req.Model)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSONValue(w, chat)
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *AppAPI) handleChatByID(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	switch r.Method {
	case http.MethodGet:
		chat, err := a.chats.Get(id)
		if err != nil {
			writeChatError(w, err)
			return
		}
		writeJSONValue(w, chat)
	case http.MethodPut:
		var req struct {
			Title    string            `json:"title"`
			Model    string            `json:"model"`
			Messages []json.RawMessage `json:"messages"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<20)).Decode(&req); err != nil {
			writeJSONError(w, http.StatusBadRequest, "decode request: "+err.Error())
			return
		}
		chat, err := a.chats.Put(id, req.Title, req.Model, req.Messages)
		if err != nil {
			writeChatError(w, err)
			return
		}
		writeJSONValue(w, chat)
	case http.MethodDelete:
		if err := a.chats.Delete(id); err != nil {
			writeChatError(w, err)
			return
		}
		writeJSONValue(w, map[string]any{"deleted": true})
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func writeChatError(w http.ResponseWriter, err error) {
	if errors.Is(err, errChatNotFound) {
		writeJSONError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSONError(w, http.StatusInternalServerError, err.Error())
}
