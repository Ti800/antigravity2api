package server

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/Ti800/gemini-web2api-ios/internal/translate"
)

// messageRequest is the subset of the Anthropic Messages API this proxy accepts.
type messageRequest struct {
	Model    string          `json:"model"`
	System   json.RawMessage `json:"system"`
	Messages []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"messages"`
	Stream    bool `json:"stream"`
	MaxTokens *int `json:"max_tokens"`
}

// handleMessages accepts Claude-shaped requests and serves them through the
// same chat path. Translation stays in this file so the chat handler does not
// grow a second protocol.
func (a *App) handleMessages(w http.ResponseWriter, r *http.Request) {
	var in messageRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxRequestBody)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("invalid json"))
		return
	}
	chat := translate.ChatRequest{Model: in.Model, Stream: in.Stream, MaxTokens: in.MaxTokens}
	if sys := strings.TrimSpace(string(in.System)); sys != "" && sys != "null" {
		chat.Messages = append(chat.Messages, translate.ChatMessage{Role: "system", Content: in.System})
	}
	for _, m := range in.Messages {
		chat.Messages = append(chat.Messages, translate.ChatMessage{Role: m.Role, Content: m.Content})
	}
	raw, err := json.Marshal(chat)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errBody("encode failed"))
		return
	}
	r.Body = io.NopCloser(strings.NewReader(string(raw)))
	a.handleChat(w, r)
}
