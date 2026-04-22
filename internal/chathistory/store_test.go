package chathistory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestStoreCreatesAndPersistsEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chat_history.json")
	store := New(path)

	started, err := store.Start(StartParams{
		CallerID:  "caller:abc",
		AccountID: "user@example.com",
		Model:     "deepseek-chat",
		Stream:    true,
		UserInput: "hello",
	})
	if err != nil {
		t.Fatalf("start entry failed: %v", err)
	}

	updated, err := store.Update(started.ID, UpdateParams{
		Status:           "success",
		ReasoningContent: "thinking",
		Content:          "answer",
		StatusCode:       200,
		ElapsedMs:        321,
		FinishReason:     "stop",
		Usage:            map[string]any{"total_tokens": 9},
		Completed:        true,
	})
	if err != nil {
		t.Fatalf("update entry failed: %v", err)
	}
	if updated.Status != "success" || updated.Content != "answer" {
		t.Fatalf("unexpected updated entry: %#v", updated)
	}

	snapshot, err := store.Snapshot()
	if err != nil {
		t.Fatalf("snapshot failed: %v", err)
	}
	if snapshot.Limit != DefaultLimit {
		t.Fatalf("unexpected default limit: %d", snapshot.Limit)
	}
	if len(snapshot.Items) != 1 {
		t.Fatalf("expected one item, got %d", len(snapshot.Items))
	}
	if snapshot.Items[0].CompletedAt == 0 {
		t.Fatalf("expected completed_at to be populated")
	}
	if snapshot.Items[0].Preview != "answer" {
		t.Fatalf("expected summary preview=answer, got %#v", snapshot.Items[0])
	}

	reloaded := New(path)
	reloadedSnapshot, err := reloaded.Snapshot()
	if err != nil {
		t.Fatalf("reload snapshot failed: %v", err)
	}
	if len(reloadedSnapshot.Items) != 1 {
		t.Fatalf("unexpected reloaded summaries: %#v", reloadedSnapshot.Items)
	}
	full, err := reloaded.Get(started.ID)
	if err != nil {
		t.Fatalf("get detail failed: %v", err)
	}
	if full.Content != "answer" {
		t.Fatalf("expected detail content=answer, got %#v", full)
	}
}

func TestStoreTrimsToConfiguredLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chat_history.json")
	store := New(path)
	if _, err := store.SetLimit(10); err != nil {
		t.Fatalf("set limit failed: %v", err)
	}

	for i := 0; i < 12; i++ {
		entry, err := store.Start(StartParams{Model: "deepseek-chat", UserInput: "msg"})
		if err != nil {
			t.Fatalf("start %d failed: %v", i, err)
		}
		if _, err := store.Update(entry.ID, UpdateParams{Status: "success", Content: "ok", Completed: true}); err != nil {
			t.Fatalf("update %d failed: %v", i, err)
		}
	}

	snapshot, err := store.Snapshot()
	if err != nil {
		t.Fatalf("snapshot failed: %v", err)
	}
	if len(snapshot.Items) != 10 {
		t.Fatalf("expected 10 items, got %d", len(snapshot.Items))
	}
}

func TestStoreDeleteClearAndLimitValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chat_history.json")
	store := New(path)
	entry, err := store.Start(StartParams{UserInput: "hello"})
	if err != nil {
		t.Fatalf("start failed: %v", err)
	}
	if err := store.Delete(entry.ID); err != nil {
		t.Fatalf("delete failed: %v", err)
	}
	snapshot, err := store.Snapshot()
	if err != nil {
		t.Fatalf("snapshot failed: %v", err)
	}
	if len(snapshot.Items) != 0 {
		t.Fatalf("expected empty items after delete, got %d", len(snapshot.Items))
	}
	if _, err := store.SetLimit(999); err == nil {
		t.Fatalf("expected invalid limit error")
	}
	if err := store.Clear(); err != nil {
		t.Fatalf("clear failed: %v", err)
	}
}

func TestStoreDisablePreservesHistoryAndBlocksNewEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chat_history.json")
	store := New(path)

	entry, err := store.Start(StartParams{UserInput: "hello"})
	if err != nil {
		t.Fatalf("start failed: %v", err)
	}
	if _, err := store.Update(entry.ID, UpdateParams{Status: "success", Content: "world", Completed: true}); err != nil {
		t.Fatalf("update failed: %v", err)
	}

	snapshot, err := store.SetLimit(DisabledLimit)
	if err != nil {
		t.Fatalf("disable failed: %v", err)
	}
	if snapshot.Limit != DisabledLimit {
		t.Fatalf("expected disabled limit, got %d", snapshot.Limit)
	}
	if len(snapshot.Items) != 1 {
		t.Fatalf("expected disabled mode to preserve summaries, got %d", len(snapshot.Items))
	}
	if store.Enabled() {
		t.Fatalf("expected store to report disabled")
	}
	if _, err := store.Start(StartParams{UserInput: "later"}); err != ErrDisabled {
		t.Fatalf("expected ErrDisabled, got %v", err)
	}
}

func TestStoreConcurrentUpdatesKeepSplitFilesValid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chat_history.json")
	store := New(path)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			entry, err := store.Start(StartParams{
				CallerID:  "caller:test",
				Model:     "deepseek-chat",
				UserInput: "hello",
			})
			if err != nil {
				t.Errorf("start failed: %v", err)
				return
			}
			_, err = store.Update(entry.ID, UpdateParams{
				Status:    "success",
				Content:   "answer",
				ElapsedMs: int64(idx),
				Completed: true,
			})
			if err != nil {
				t.Errorf("update failed: %v", err)
			}
		}(i)
	}
	wg.Wait()

	snapshot, err := store.Snapshot()
	if err != nil {
		t.Fatalf("snapshot failed: %v", err)
	}
	if len(snapshot.Items) != 8 {
		t.Fatalf("expected 8 items, got %d", len(snapshot.Items))
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read index failed: %v", err)
	}
	var persisted File
	if err := json.Unmarshal(raw, &persisted); err != nil {
		t.Fatalf("persisted index is invalid json: %v", err)
	}
	if len(persisted.Items) != 8 {
		t.Fatalf("expected persisted items=8, got %d", len(persisted.Items))
	}

	detailFiles, err := os.ReadDir(path + ".d")
	if err != nil {
		t.Fatalf("read detail dir failed: %v", err)
	}
	if len(detailFiles) != 8 {
		t.Fatalf("expected 8 detail files, got %d", len(detailFiles))
	}
}

func TestStoreAutoMigratesLegacyMonolith(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chat_history.json")
	legacy := legacyFile{
		Version: 1,
		Limit:   20,
		Items: []Entry{{
			ID:               "chat_legacy",
			CreatedAt:        1,
			UpdatedAt:        2,
			Status:           "success",
			UserInput:        "hello",
			Content:          "world",
			ReasoningContent: "thinking",
		}},
	}
	body, _ := json.MarshalIndent(legacy, "", "  ")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatalf("write legacy file failed: %v", err)
	}

	store := New(path)
	if err := store.Err(); err != nil {
		t.Fatalf("expected legacy migration success, got %v", err)
	}
	snapshot, err := store.Snapshot()
	if err != nil {
		t.Fatalf("snapshot failed: %v", err)
	}
	if len(snapshot.Items) != 1 {
		t.Fatalf("expected one migrated summary, got %#v", snapshot.Items)
	}
	full, err := store.Get("chat_legacy")
	if err != nil {
		t.Fatalf("get migrated detail failed: %v", err)
	}
	if full.Content != "world" {
		t.Fatalf("expected migrated detail content preserved, got %#v", full)
	}
}
