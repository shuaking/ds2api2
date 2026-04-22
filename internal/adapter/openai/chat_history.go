package openai

import (
	"net/http"
	"strings"
	"time"

	"ds2api/internal/auth"
	"ds2api/internal/chathistory"
	"ds2api/internal/config"
	openaifmt "ds2api/internal/format/openai"
	"ds2api/internal/prompt"
	"ds2api/internal/util"
)

const adminWebUISourceHeader = "X-Ds2-Source"
const adminWebUISourceValue = "admin-webui-api-tester"

type chatHistorySession struct {
	store       *chathistory.Store
	entryID     string
	startedAt   time.Time
	lastPersist time.Time
	finalPrompt string
	startParams chathistory.StartParams
	disabled    bool
}

func startChatHistory(store *chathistory.Store, r *http.Request, a *auth.RequestAuth, stdReq util.StandardRequest) *chatHistorySession {
	if store == nil || r == nil || a == nil {
		return nil
	}
	if !store.Enabled() {
		return nil
	}
	if !shouldCaptureChatHistory(r) {
		return nil
	}
	entry, err := store.Start(chathistory.StartParams{
		CallerID:    strings.TrimSpace(a.CallerID),
		AccountID:   strings.TrimSpace(a.AccountID),
		Model:       strings.TrimSpace(stdReq.ResponseModel),
		Stream:      stdReq.Stream,
		UserInput:   extractSingleUserInput(stdReq.Messages),
		Messages:    extractAllMessages(stdReq.Messages),
		FinalPrompt: stdReq.FinalPrompt,
	})
	if err != nil {
		config.Logger.Warn("[chat_history] start failed", "error", err)
		return nil
	}
	startParams := chathistory.StartParams{
		CallerID:    strings.TrimSpace(a.CallerID),
		AccountID:   strings.TrimSpace(a.AccountID),
		Model:       strings.TrimSpace(stdReq.ResponseModel),
		Stream:      stdReq.Stream,
		UserInput:   extractSingleUserInput(stdReq.Messages),
		Messages:    extractAllMessages(stdReq.Messages),
		FinalPrompt: stdReq.FinalPrompt,
	}
	return &chatHistorySession{
		store:       store,
		entryID:     entry.ID,
		startedAt:   time.Now(),
		lastPersist: time.Now(),
		finalPrompt: stdReq.FinalPrompt,
		startParams: startParams,
	}
}

func shouldCaptureChatHistory(r *http.Request) bool {
	if r == nil {
		return false
	}
	if isVercelStreamPrepareRequest(r) || isVercelStreamReleaseRequest(r) {
		return false
	}
	return strings.TrimSpace(r.Header.Get(adminWebUISourceHeader)) != adminWebUISourceValue
}

func extractSingleUserInput(messages []any) string {
	for i := len(messages) - 1; i >= 0; i-- {
		msg, ok := messages[i].(map[string]any)
		if !ok {
			continue
		}
		role := strings.ToLower(strings.TrimSpace(asString(msg["role"])))
		if role != "user" {
			continue
		}
		if normalized := strings.TrimSpace(prompt.NormalizeContent(msg["content"])); normalized != "" {
			return normalized
		}
	}
	return ""
}

func extractAllMessages(messages []any) []chathistory.Message {
	out := make([]chathistory.Message, 0, len(messages))
	for _, raw := range messages {
		msg, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		role := strings.ToLower(strings.TrimSpace(asString(msg["role"])))
		content := strings.TrimSpace(prompt.NormalizeContent(msg["content"]))
		if role == "" || content == "" {
			continue
		}
		out = append(out, chathistory.Message{
			Role:    role,
			Content: content,
		})
	}
	return out
}

func (s *chatHistorySession) progress(thinking, content string) {
	if s == nil || s.store == nil || s.disabled {
		return
	}
	now := time.Now()
	if now.Sub(s.lastPersist) < 250*time.Millisecond {
		return
	}
	s.lastPersist = now
	if _, err := s.store.Update(s.entryID, chathistory.UpdateParams{
		Status:           "streaming",
		ReasoningContent: thinking,
		Content:          content,
		StatusCode:       http.StatusOK,
		ElapsedMs:        time.Since(s.startedAt).Milliseconds(),
	}); err != nil {
		if !s.retryMissingEntry() {
			s.disableOnMissing(err)
			return
		}
		_, retryErr := s.store.Update(s.entryID, chathistory.UpdateParams{
			Status:           "streaming",
			ReasoningContent: thinking,
			Content:          content,
			StatusCode:       http.StatusOK,
			ElapsedMs:        time.Since(s.startedAt).Milliseconds(),
		})
		s.disableOnMissing(retryErr)
	}
}

func (s *chatHistorySession) success(statusCode int, thinking, content, finishReason string, usage map[string]any) {
	if s == nil || s.store == nil || s.disabled {
		return
	}
	if _, err := s.store.Update(s.entryID, chathistory.UpdateParams{
		Status:           "success",
		ReasoningContent: thinking,
		Content:          content,
		StatusCode:       statusCode,
		ElapsedMs:        time.Since(s.startedAt).Milliseconds(),
		FinishReason:     finishReason,
		Usage:            usage,
		Completed:        true,
	}); err != nil {
		if !s.retryMissingEntry() {
			s.disableOnMissing(err)
			return
		}
		_, retryErr := s.store.Update(s.entryID, chathistory.UpdateParams{
			Status:           "success",
			ReasoningContent: thinking,
			Content:          content,
			StatusCode:       statusCode,
			ElapsedMs:        time.Since(s.startedAt).Milliseconds(),
			FinishReason:     finishReason,
			Usage:            usage,
			Completed:        true,
		})
		s.disableOnMissing(retryErr)
	}
}

func (s *chatHistorySession) error(statusCode int, message, finishReason, thinking, content string) {
	if s == nil || s.store == nil || s.disabled {
		return
	}
	if _, err := s.store.Update(s.entryID, chathistory.UpdateParams{
		Status:           "error",
		ReasoningContent: thinking,
		Content:          content,
		Error:            message,
		StatusCode:       statusCode,
		ElapsedMs:        time.Since(s.startedAt).Milliseconds(),
		FinishReason:     finishReason,
		Completed:        true,
	}); err != nil {
		if !s.retryMissingEntry() {
			s.disableOnMissing(err)
			return
		}
		_, retryErr := s.store.Update(s.entryID, chathistory.UpdateParams{
			Status:           "error",
			ReasoningContent: thinking,
			Content:          content,
			Error:            message,
			StatusCode:       statusCode,
			ElapsedMs:        time.Since(s.startedAt).Milliseconds(),
			FinishReason:     finishReason,
			Completed:        true,
		})
		s.disableOnMissing(retryErr)
	}
}

func (s *chatHistorySession) stopped(thinking, content, finishReason string) {
	if s == nil || s.store == nil || s.disabled {
		return
	}
	if _, err := s.store.Update(s.entryID, chathistory.UpdateParams{
		Status:           "stopped",
		ReasoningContent: thinking,
		Content:          content,
		StatusCode:       http.StatusOK,
		ElapsedMs:        time.Since(s.startedAt).Milliseconds(),
		FinishReason:     finishReason,
		Usage:            openaifmt.BuildChatUsage(s.finalPrompt, thinking, content),
		Completed:        true,
	}); err != nil {
		if !s.retryMissingEntry() {
			s.disableOnMissing(err)
			return
		}
		_, retryErr := s.store.Update(s.entryID, chathistory.UpdateParams{
			Status:           "stopped",
			ReasoningContent: thinking,
			Content:          content,
			StatusCode:       http.StatusOK,
			ElapsedMs:        time.Since(s.startedAt).Milliseconds(),
			FinishReason:     finishReason,
			Usage:            openaifmt.BuildChatUsage(s.finalPrompt, thinking, content),
			Completed:        true,
		})
		s.disableOnMissing(retryErr)
	}
}

func (s *chatHistorySession) retryMissingEntry() bool {
	if s == nil || s.store == nil || s.disabled {
		return false
	}
	entry, err := s.store.Start(s.startParams)
	if err != nil {
		s.disableOnMissing(err)
		return false
	}
	s.entryID = entry.ID
	return true
}

func (s *chatHistorySession) disableOnMissing(err error) {
	if err == nil || s == nil {
		return
	}
	if strings.Contains(strings.ToLower(err.Error()), "not found") {
		s.disabled = true
		return
	}
	config.Logger.Warn("[chat_history] update disabled", "error", err)
	s.disabled = true
}
