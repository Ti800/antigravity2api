package translate

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestFromChatEnvelope(t *testing.T) {
	temp := 0.5
	max := 128
	req := ChatRequest{
		Model: "gemini-3.5-flash",
		Messages: []ChatMessage{
			{Role: "system", Content: json.RawMessage(`"be brief"`)},
			{Role: "user", Content: json.RawMessage(`"hello"`)},
		},
		Temperature: &temp,
		MaxTokens:   &max,
		Tools: []Tool{{
			Type: "function",
			Function: struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				Parameters  json.RawMessage `json:"parameters"`
			}{Name: "lookup", Description: "find", Parameters: json.RawMessage(`{"type":"object"}`)},
		}},
	}
	env := FromChat(req, "proj-1", req.Model)
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, want := range []string{`"project":"proj-1"`, `"requestType":"agent"`, `"userAgent":"antigravity"`, `"model":"gemini-3.5-flash"`, `"sessionId":"-`, `"name":"lookup"`} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s in %s", want, s)
		}
	}
	if env.Request.SystemInstruction == nil || env.Request.SystemInstruction.Parts[0].Text != "be brief" {
		t.Errorf("system instruction not carried: %+v", env.Request.SystemInstruction)
	}
	if len(env.Request.Contents) != 1 || env.Request.Contents[0].Role != "user" {
		t.Errorf("contents: %+v", env.Request.Contents)
	}
}

func TestExtractThoughtAndTool(t *testing.T) {
	raw := []byte(`{"response":{"responseId":"r1","modelVersion":"m1","candidates":[{"finishReason":"STOP","content":{"parts":[{"text":"think","thought":true},{"text":"answer"},{"functionCall":{"name":"lookup","args":{"q":"x"}}}]}}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":4,"totalTokenCount":7}}}`)
	c, ok := ParseChunk(raw)
	if !ok {
		t.Fatal("parse")
	}
	d := Extract(c)
	if d.Thought != "think" || d.Text != "answer" {
		t.Errorf("text split: %+v", d)
	}
	if d.Finish != "stop" || len(d.ToolCalls) != 1 || d.ToolCalls[0].Name != "lookup" {
		t.Errorf("finish/tool: %+v", d)
	}
	if d.Usage == nil || d.Usage.Total != 7 {
		t.Errorf("usage: %+v", d.Usage)
	}
}

func TestReplayedToolCallGetsSignatureSentinel(t *testing.T) {
	var tc ToolCall
	tc.ID = "call_1"
	tc.Type = "function"
	tc.Function.Name = "get_weather"
	tc.Function.Arguments = `{"location":"Beijing"}`

	req := ChatRequest{
		Model: "gemini-3.8-flash-medium",
		Messages: []ChatMessage{
			{Role: "user", Content: json.RawMessage(`"weather?"`)},
			{Role: "assistant", Content: json.RawMessage(`""`), ToolCalls: []ToolCall{tc}},
			{Role: "tool", Name: "get_weather", Content: json.RawMessage(`"sunny"`)},
		},
	}
	env := FromChat(req, "p", req.Model)
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"thoughtSignature":"skip_thought_signature_validator"`) {
		t.Fatalf("sentinel missing: %s", raw)
	}
	var modelTurn *content
	for i := range env.Request.Contents {
		if env.Request.Contents[i].Role == "model" {
			modelTurn = &env.Request.Contents[i]
			break
		}
	}
	if modelTurn == nil || len(modelTurn.Parts) == 0 ||
		modelTurn.Parts[0].FunctionCall == nil || modelTurn.Parts[0].ThoughtSignature == "" {
		t.Fatalf("sentinel not on the first functionCall: %+v", modelTurn)
	}

	// The sentinel is Gemini-only; it must not leak into other models.
	req.Model = "claude-sonnet-4-6"
	env2 := FromChat(req, "p", req.Model)
	raw2, err := json.Marshal(env2)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw2), "skip_thought_signature_validator") {
		t.Fatalf("sentinel leaked into non-Gemini model: %s", raw2)
	}
}
