package openai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ds2api/internal/chathistory"
)

func newTestChatHistoryStore(t *testing.T) *chathistory.Store {
	t.Helper()
	store := chathistory.New(filepath.Join(t.TempDir(), "chat_history.json"))
	if err := store.Err(); err != nil {
		t.Fatalf("chat history store unavailable: %v", err)
	}
	return store
}

func TestChatCompletionsNonStreamPersistsHistory(t *testing.T) {
	historyStore := newTestChatHistoryStore(t)
	h := &Handler{
		Store:       mockOpenAIConfig{wideInput: true},
		Auth:        streamStatusAuthStub{},
		DS:          streamStatusDSStub{resp: makeOpenAISSEHTTPResponse(`data: {"p":"response/content","v":"hello world"}`, `data: [DONE]`)},
		ChatHistory: historyStore,
	}

	reqBody := `{"model":"deepseek-chat","messages":[{"role":"system","content":"be precise"},{"role":"user","content":"hi there"},{"role":"assistant","content":"previous answer"}],"stream":false}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Authorization", "Bearer direct-token")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ChatCompletions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	snapshot, err := historyStore.Snapshot()
	if err != nil {
		t.Fatalf("snapshot failed: %v", err)
	}
	if len(snapshot.Items) != 1 {
		t.Fatalf("expected one history item, got %d", len(snapshot.Items))
	}
	item := snapshot.Items[0]
	if item.Status != "success" || item.UserInput != "hi there" {
		t.Fatalf("unexpected persisted history summary: %#v", item)
	}
	full, err := historyStore.Get(item.ID)
	if err != nil {
		t.Fatalf("expected detail item, got %v", err)
	}
	if full.Content != "hello world" {
		t.Fatalf("expected detail content persisted, got %#v", full)
	}
	if len(full.Messages) != 3 {
		t.Fatalf("expected all request messages persisted, got %#v", full.Messages)
	}
	if full.FinalPrompt == "" {
		t.Fatalf("expected final prompt to be persisted")
	}
	if item.CallerID != "caller:test" {
		t.Fatalf("expected caller hash persisted in summary, got %#v", item.CallerID)
	}
}

func TestHandleStreamContextCancelledMarksHistoryStopped(t *testing.T) {
	historyStore := newTestChatHistoryStore(t)
	entry, err := historyStore.Start(chathistory.StartParams{
		CallerID:  "caller:test",
		Model:     "deepseek-chat",
		Stream:    true,
		UserInput: "hello",
	})
	if err != nil {
		t.Fatalf("start history failed: %v", err)
	}
	session := &chatHistorySession{
		store:       historyStore,
		entryID:     entry.ID,
		startedAt:   time.Now(),
		lastPersist: time.Now(),
		finalPrompt: "hello",
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	h := &Handler{}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	resp := makeOpenAISSEHTTPResponse(`data: {"p":"response/content","v":"hello"}`, `data: [DONE]`)

	h.handleStream(rec, req, resp, "cid-stop", "deepseek-chat", "prompt", false, false, nil, session)

	snapshot, err := historyStore.Snapshot()
	if err != nil {
		t.Fatalf("snapshot failed: %v", err)
	}
	if len(snapshot.Items) != 1 {
		t.Fatalf("expected one history item, got %d", len(snapshot.Items))
	}
	full, err := historyStore.Get(snapshot.Items[0].ID)
	if err != nil {
		t.Fatalf("get detail failed: %v", err)
	}
	if full.Status != "stopped" {
		t.Fatalf("expected stopped status, got %#v", full)
	}
}

func TestChatCompletionsSkipsAdminWebUISource(t *testing.T) {
	historyStore := newTestChatHistoryStore(t)
	h := &Handler{
		Store:       mockOpenAIConfig{wideInput: true},
		Auth:        streamStatusAuthStub{},
		DS:          streamStatusDSStub{resp: makeOpenAISSEHTTPResponse(`data: {"p":"response/content","v":"hello world"}`, `data: [DONE]`)},
		ChatHistory: historyStore,
	}

	reqBody := `{"model":"deepseek-chat","messages":[{"role":"user","content":"hi there"}],"stream":false}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Authorization", "Bearer direct-token")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(adminWebUISourceHeader, adminWebUISourceValue)
	rec := httptest.NewRecorder()
	h.ChatCompletions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	snapshot, err := historyStore.Snapshot()
	if err != nil {
		t.Fatalf("snapshot failed: %v", err)
	}
	if len(snapshot.Items) != 0 {
		t.Fatalf("expected admin webui source to be skipped, got %#v", snapshot.Items)
	}
}

func TestChatCompletionsSkipsHistoryWhenDisabled(t *testing.T) {
	historyStore := newTestChatHistoryStore(t)
	if _, err := historyStore.SetLimit(chathistory.DisabledLimit); err != nil {
		t.Fatalf("disable history store failed: %v", err)
	}
	h := &Handler{
		Store:       mockOpenAIConfig{wideInput: true},
		Auth:        streamStatusAuthStub{},
		DS:          streamStatusDSStub{resp: makeOpenAISSEHTTPResponse(`data: {"p":"response/content","v":"hello world"}`, `data: [DONE]`)},
		ChatHistory: historyStore,
	}

	reqBody := `{"model":"deepseek-chat","messages":[{"role":"user","content":"hi there"}],"stream":false}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Authorization", "Bearer direct-token")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ChatCompletions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	snapshot, err := historyStore.Snapshot()
	if err != nil {
		t.Fatalf("snapshot failed: %v", err)
	}
	if len(snapshot.Items) != 0 {
		t.Fatalf("expected disabled history to stay empty, got %#v", snapshot.Items)
	}
}
