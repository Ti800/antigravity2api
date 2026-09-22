// Package translate converts between client protocols and the Antigravity
// request envelope. The envelope is a Gemini request plus project, requestType,
// requestId and sessionId.
package translate

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

// ChatRequest is the subset of an OpenAI chat completion request this proxy
// forwards. Unknown fields are ignored.
type ChatRequest struct {
	Model       string        `json:"model"`
	Messages    []ChatMessage `json:"messages"`
	Stream      bool          `json:"stream"`
	Temperature *float64      `json:"temperature,omitempty"`
	TopP        *float64      `json:"top_p,omitempty"`
	MaxTokens   *int          `json:"max_tokens,omitempty"`
	Tools       []Tool        `json:"tools,omitempty"`
}

// ChatMessage is one turn. Content is either a string or an array of parts.
type ChatMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	ToolCalls  []ToolCall      `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	Name       string          `json:"name,omitempty"`
}

// Tool is an OpenAI function tool declaration.
type Tool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

// ToolCall is an assistant function call replayed from history.
type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type part struct {
	Text         string        `json:"text,omitempty"`
	Thought      bool          `json:"thought,omitempty"`
	FunctionCall *functionCall `json:"functionCall,omitempty"`
	FunctionResp *functionResp `json:"functionResponse,omitempty"`
	InlineData   *inlineData   `json:"inlineData,omitempty"`
}

type functionCall struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args,omitempty"`
}

type functionResp struct {
	Name     string         `json:"name"`
	Response map[string]any `json:"response"`
}

type inlineData struct {
	MIMEType string `json:"mimeType"`
	Data     string `json:"data"`
}

type content struct {
	Role  string `json:"role"`
	Parts []part `json:"parts"`
}

// Envelope is what Cloud Code expects.
type Envelope struct {
	Project     string   `json:"project,omitempty"`
	RequestType string   `json:"requestType"`
	RequestID   string   `json:"requestId"`
	UserAgent   string   `json:"userAgent"`
	Model       string   `json:"model"`
	Request     *Request `json:"request"`
}

// Request is the inner Gemini request.
type Request struct {
	Contents          []content      `json:"contents"`
	SystemInstruction *content       `json:"systemInstruction,omitempty"`
	GenerationConfig  map[string]any `json:"generationConfig,omitempty"`
	Tools             []any          `json:"tools,omitempty"`
	SessionID         string         `json:"sessionId,omitempty"`
}

// FromChat builds an envelope from an OpenAI chat request.
func FromChat(req ChatRequest, project, model string) Envelope {
	env := Envelope{
		Project:     project,
		UserAgent:   "antigravity",
		Model:       model,
		RequestType: "agent",
		RequestID:   "agent-" + uuid.NewString(),
		Request:     &Request{},
	}
	if strings.Contains(model, "image") {
		env.RequestType = "image_gen"
	}

	var system []part
	for _, m := range req.Messages {
		switch m.Role {
		case "system", "developer":
			if text := textOf(m.Content); text != "" {
				system = append(system, part{Text: text})
			}
		case "tool":
			env.Request.Contents = append(env.Request.Contents, content{
				Role: "user",
				Parts: []part{{FunctionResp: &functionResp{
					Name:     m.Name,
					Response: map[string]any{"result": textOf(m.Content)},
				}}},
			})
		default:
			role := "user"
			if m.Role == "assistant" {
				role = "model"
			}
			c := content{Role: role}
			c.Parts = append(c.Parts, partsOf(m.Content)...)
			for _, tc := range m.ToolCalls {
				args := json.RawMessage(tc.Function.Arguments)
				if !json.Valid(args) {
					args = json.RawMessage("{}")
				}
				c.Parts = append(c.Parts, part{FunctionCall: &functionCall{Name: tc.Function.Name, Args: args}})
			}
			if len(c.Parts) == 0 {
				c.Parts = []part{{Text: ""}}
			}
			env.Request.Contents = append(env.Request.Contents, c)
		}
	}
	if len(system) > 0 {
		env.Request.SystemInstruction = &content{Role: "user", Parts: system}
	}
	if len(env.Request.Contents) == 0 {
		env.Request.Contents = []content{{Role: "user", Parts: []part{{Text: ""}}}}
	}
	env.Request.SessionID = sessionID(env.Request.Contents)

	gen := map[string]any{}
	if req.Temperature != nil {
		gen["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		gen["topP"] = *req.TopP
	}
	if req.MaxTokens != nil && *req.MaxTokens > 0 {
		gen["maxOutputTokens"] = *req.MaxTokens
	}
	if len(gen) > 0 {
		env.Request.GenerationConfig = gen
	}
	if decls := toolDecls(req.Tools); len(decls) > 0 {
		env.Request.Tools = []any{map[string]any{"functionDeclarations": decls}}
	}
	return env
}

func toolDecls(tools []Tool) []map[string]any {
	var out []map[string]any
	for _, t := range tools {
		if t.Type != "" && t.Type != "function" {
			continue
		}
		if t.Function.Name == "" {
			continue
		}
		d := map[string]any{"name": t.Function.Name}
		if t.Function.Description != "" {
			d["description"] = t.Function.Description
		}
		if len(t.Function.Parameters) > 0 && string(t.Function.Parameters) != "null" {
			d["parameters"] = json.RawMessage(t.Function.Parameters)
		}
		out = append(out, d)
	}
	return out
}

func textOf(raw json.RawMessage) string {
	raw = trim(raw)
	if len(raw) == 0 {
		return ""
	}
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return s
		}
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) == nil {
		var b strings.Builder
		for _, p := range parts {
			if p.Text != "" {
				if b.Len() > 0 {
					b.WriteByte('\n')
				}
				b.WriteString(p.Text)
			}
		}
		return b.String()
	}
	return ""
}

func partsOf(raw json.RawMessage) []part {
	raw = trim(raw)
	if len(raw) == 0 {
		return nil
	}
	if raw[0] == '"' {
		if s := textOf(raw); s != "" {
			return []part{{Text: s}}
		}
		return nil
	}
	var blocks []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		ImageURL struct {
			URL string `json:"url"`
		} `json:"image_url"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return nil
	}
	var out []part
	for _, b := range blocks {
		switch b.Type {
		case "text", "":
			if b.Text != "" {
				out = append(out, part{Text: b.Text})
			}
		case "image_url":
			if p, ok := dataURLPart(b.ImageURL.URL); ok {
				out = append(out, p)
			}
		}
	}
	return out
}

// dataURLPart accepts data URLs only. Remote image URLs are not fetched: that
// would add a network hop and an SSRF surface the proxy does not need.
func dataURLPart(u string) (part, bool) {
	const prefix = "data:"
	if !strings.HasPrefix(u, prefix) {
		return part{}, false
	}
	rest := u[len(prefix):]
	semi := strings.IndexByte(rest, ';')
	comma := strings.IndexByte(rest, ',')
	if semi < 0 || comma < 0 || comma < semi {
		return part{}, false
	}
	mime := rest[:semi]
	if !strings.HasPrefix(rest[semi:], ";base64,") {
		return part{}, false
	}
	data := rest[comma+1:]
	if mime == "" || data == "" {
		return part{}, false
	}
	return part{InlineData: &inlineData{MIMEType: mime, Data: data}}, true
}

func sessionID(contents []content) string {
	for _, c := range contents {
		if c.Role != "user" {
			continue
		}
		for _, p := range c.Parts {
			if p.Text == "" {
				continue
			}
			sum := sha256.Sum256([]byte(p.Text))
			n := int64(binary.BigEndian.Uint64(sum[:8])) & 0x7FFFFFFFFFFFFFFF
			return "-" + strconv.FormatInt(n, 10)
		}
	}
	return "-" + uuid.NewString()
}

func trim(b []byte) []byte {
	return []byte(strings.TrimSpace(string(b)))
}
