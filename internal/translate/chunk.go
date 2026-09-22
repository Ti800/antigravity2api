package translate

import (
	"encoding/json"
	"strings"
)

// Chunk is one SSE event from Cloud Code, reduced to the fields the proxy emits.
type Chunk struct {
	Response struct {
		ResponseID   string `json:"responseId"`
		ModelVersion string `json:"modelVersion"`
		Candidates   []struct {
			FinishReason string `json:"finishReason"`
			Content      struct {
				Parts []struct {
					Text         string `json:"text"`
					Thought      bool   `json:"thought"`
					FunctionCall *struct {
						Name string          `json:"name"`
						Args json.RawMessage `json:"args"`
					} `json:"functionCall"`
					InlineData *struct {
						MIMEType string `json:"mimeType"`
						Data     string `json:"data"`
					} `json:"inlineData"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
		UsageMetadata *struct {
			PromptTokenCount     int `json:"promptTokenCount"`
			CandidatesTokenCount int `json:"candidatesTokenCount"`
			TotalTokenCount      int `json:"totalTokenCount"`
		} `json:"usageMetadata"`
	} `json:"response"`
}

// ParseChunk decodes one SSE data payload. Malformed payloads return ok=false
// so the caller can skip them instead of failing the stream.
func ParseChunk(data []byte) (Chunk, bool) {
	var c Chunk
	if json.Unmarshal(data, &c) != nil {
		return c, false
	}
	return c, true
}

// Delta is the piece of a chunk that should be forwarded to the client.
type Delta struct {
	Text      string
	Thought   string
	ToolCalls []ToolDelta
	Images    []ImageDelta
	Finish    string
	Usage     *Usage
	Model     string
	ID        string
}

// ToolDelta is one function call.
type ToolDelta struct {
	Name string
	Args string
}

// ImageDelta is an inline image, already base64.
type ImageDelta struct {
	MIME string
	Data string
}

// Usage is token accounting.
type Usage struct {
	Prompt     int
	Completion int
	Total      int
}

// Extract pulls forwardable content out of a chunk.
func Extract(c Chunk) Delta {
	d := Delta{Model: c.Response.ModelVersion, ID: c.Response.ResponseID}
	if len(c.Response.Candidates) > 0 {
		cand := c.Response.Candidates[0]
		d.Finish = mapFinish(cand.FinishReason)
		for _, p := range cand.Content.Parts {
			if p.FunctionCall != nil {
				args := string(p.FunctionCall.Args)
				if args == "" || args == "null" {
					args = "{}"
				}
				d.ToolCalls = append(d.ToolCalls, ToolDelta{Name: p.FunctionCall.Name, Args: args})
				continue
			}
			if p.InlineData != nil && p.InlineData.Data != "" {
				d.Images = append(d.Images, ImageDelta{MIME: p.InlineData.MIMEType, Data: p.InlineData.Data})
				continue
			}
			if p.Text == "" {
				continue
			}
			if p.Thought {
				d.Thought += p.Text
			} else {
				d.Text += p.Text
			}
		}
	}
	if u := c.Response.UsageMetadata; u != nil && (u.PromptTokenCount > 0 || u.CandidatesTokenCount > 0) {
		total := u.TotalTokenCount
		if total == 0 {
			total = u.PromptTokenCount + u.CandidatesTokenCount
		}
		d.Usage = &Usage{Prompt: u.PromptTokenCount, Completion: u.CandidatesTokenCount, Total: total}
	}
	return d
}

func mapFinish(reason string) string {
	switch strings.ToUpper(reason) {
	case "", "FINISH_REASON_UNSPECIFIED":
		return ""
	case "STOP":
		return "stop"
	case "MAX_TOKENS":
		return "length"
	case "SAFETY", "RECITATION", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII":
		return "content_filter"
	default:
		if strings.Contains(strings.ToUpper(reason), "TOOL") || strings.Contains(strings.ToUpper(reason), "FUNCTION") {
			return "tool_calls"
		}
		return "stop"
	}
}
