package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/Ti800/antigravity2api/internal/auth"
	"github.com/Ti800/antigravity2api/internal/translate"
	"github.com/Ti800/antigravity2api/internal/upstream"
)

func (a *App) handleChat(w http.ResponseWriter, r *http.Request) {
	var req translate.ChatRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxRequestBody)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("invalid json"))
		return
	}
	model := req.Model
	if model == "" {
		model = "gemini-3.8-flash-medium"
	}
	model = normalizeModel(model)
	acct := a.Auth.Pick()
	if acct == nil {
		writeJSON(w, http.StatusServiceUnavailable, errBody("no account configured"))
		return
	}
	token, err := a.Auth.Token(r.Context(), acct)
	if err != nil {
		a.logFailure(r, "token refresh failed: %v", err)
		writeJSON(w, http.StatusBadGateway, errBody("token refresh failed"))
		return
	}
	project, err := a.ensureProject(r.Context(), acct, token)
	if err != nil {
		a.logFailure(r, "project lookup failed: %v", err)
		writeJSON(w, http.StatusBadGateway, errBody("project lookup failed"))
		return
	}

	env := translate.FromChat(req, project, model)
	raw, err := json.Marshal(env)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errBody("encode failed"))
		return
	}

	if req.Stream {
		a.streamChat(w, r, token, raw, model)
		return
	}
	a.onceChat(w, r, token, raw, model)
}

func (a *App) ensureProject(ctx context.Context, acct *auth.Account, token string) (string, error) {
	if acct.ProjectID != "" {
		return acct.ProjectID, nil
	}
	id, err := a.Up.LoadProject(ctx, token)
	if err != nil {
		return "", err
	}
	if id == "" {
		id, err = a.Up.OnboardUser(ctx, token, "")
		if err != nil {
			return "", err
		}
	}
	a.Auth.SetProject(acct, id)
	return id, nil
}

func (a *App) streamChat(w http.ResponseWriter, r *http.Request, token string, raw []byte, model string) {
	resp, err := a.Up.Stream(r.Context(), token, raw)
	if err != nil {
		a.logFailure(r, "stream failed: %v", err)
		writeUpstreamError(w, err)
		return
	}
	defer resp.Body.Close()
	if !startSSE(w) {
		return
	}

	id := "chatcmpl-" + uuid.NewString()
	created := time.Now().Unix()
	roleSent := false
	toolIdx := 0
	var finish string

	err = upstream.ReadSSE(resp.Body, func(data []byte) error {
		if string(data) == "[DONE]" {
			return nil
		}
		chunk, ok := translate.ParseChunk(data)
		if !ok {
			return nil
		}
		d := translate.Extract(chunk)
		if d.ID != "" {
			id = d.ID
		}
		if d.Model != "" {
			model = d.Model
		}
		if !roleSent && (d.Text != "" || d.Thought != "" || len(d.ToolCalls) > 0) {
			roleSent = true
			if err := writeSSE(w, chatChunk(id, created, model, map[string]any{"role": "assistant"}, "")); err != nil {
				return err
			}
		}
		if d.Text != "" || d.Thought != "" {
			delta := map[string]any{}
			if d.Text != "" {
				delta["content"] = d.Text
			}
			if d.Thought != "" {
				delta["reasoning_content"] = d.Thought
			}
			if err := writeSSE(w, chatChunk(id, created, model, delta, "")); err != nil {
				return err
			}
		}
		for _, tc := range d.ToolCalls {
			delta := map[string]any{"tool_calls": []any{map[string]any{
				"index": toolIdx,
				"id":    "call_" + uuid.NewString(),
				"type":  "function",
				"function": map[string]any{
					"name":      tc.Name,
					"arguments": tc.Args,
				},
			}}}
			toolIdx++
			if err := writeSSE(w, chatChunk(id, created, model, delta, "")); err != nil {
				return err
			}
		}
		if d.Finish != "" {
			finish = d.Finish
		}
		return nil
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		return
	}
	if finish == "" {
		if toolIdx > 0 {
			finish = "tool_calls"
		} else {
			finish = "stop"
		}
	}
	_ = writeSSE(w, chatChunk(id, created, model, map[string]any{}, finish))
	writeSSEDone(w)
}

func (a *App) onceChat(w http.ResponseWriter, r *http.Request, token string, raw []byte, model string) {
	body, err := a.Up.Generate(r.Context(), token, raw)
	if err != nil {
		a.logFailure(r, "generate failed: %v", err)
		writeUpstreamError(w, err)
		return
	}
	chunk, ok := translate.ParseChunk(body)
	if !ok {
		a.logFailure(r, "unreadable upstream response")
		writeJSON(w, http.StatusBadGateway, errBody("upstream returned an unreadable response"))
		return
	}
	d := translate.Extract(chunk)
	if d.Model != "" {
		model = d.Model
	}
	msg := map[string]any{"role": "assistant", "content": d.Text}
	if d.Thought != "" {
		msg["reasoning_content"] = d.Thought
	}
	finish := d.Finish
	if len(d.ToolCalls) > 0 {
		calls := make([]any, len(d.ToolCalls))
		for i, tc := range d.ToolCalls {
			calls[i] = map[string]any{
				"id":   "call_" + uuid.NewString(),
				"type": "function",
				"function": map[string]any{
					"name":      tc.Name,
					"arguments": tc.Args,
				},
			}
		}
		msg["tool_calls"] = calls
		if finish == "" || finish == "stop" {
			finish = "tool_calls"
		}
	}
	if finish == "" {
		finish = "stop"
	}
	out := map[string]any{
		"id":      firstNonEmpty(d.ID, "chatcmpl-"+uuid.NewString()),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []any{map[string]any{"index": 0, "message": msg, "finish_reason": finish}},
	}
	if d.Usage != nil {
		out["usage"] = map[string]any{
			"prompt_tokens":     d.Usage.Prompt,
			"completion_tokens": d.Usage.Completion,
			"total_tokens":      d.Usage.Total,
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func chatChunk(id string, created int64, model string, delta map[string]any, finish string) map[string]any {
	choice := map[string]any{"index": 0, "delta": delta}
	if finish != "" {
		choice["finish_reason"] = finish
	} else {
		choice["finish_reason"] = nil
	}
	return map[string]any{
		"id":      id,
		"object":  "chat.completion.chunk",
		"created": created,
		"model":   model,
		"choices": []any{choice},
	}
}

func writeUpstreamError(w http.ResponseWriter, err error) {
	var se *upstream.StatusError
	if errors.As(err, &se) {
		status := http.StatusBadGateway
		if se.Status == http.StatusTooManyRequests || se.Status == http.StatusUnauthorized || se.Status == http.StatusForbidden {
			status = se.Status
		}
		writeJSON(w, status, errBody(se.Error()))
		return
	}
	writeJSON(w, http.StatusBadGateway, errBody("upstream request failed"))
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}
