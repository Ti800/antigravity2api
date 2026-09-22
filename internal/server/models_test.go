package server

import "testing"

func TestNormalizeModelAliases(t *testing.T) {
	cases := map[string]string{
		// Current-generation loose names.
		"gemini-3.8-flash":          "gemini-3.8-flash-medium",
		"gemini-3.8-flash-thinking": "gemini-3.8-flash-high",
		"gemini-3.7-flash":          "gemini-3.7-flash-medium",
		"gemini-3.6-flash":          "gemini-3.6-flash-medium",
		"gemini-3.1-pro":            "gemini-pro-agent",
		// Retired names from the pre-rewrite model list.
		"gemini-3.5-flash":               "gemini-3.8-flash-medium",
		"gemini-3.5-flash-thinking":      "gemini-3.8-flash-high",
		"gemini-3.5-flash-thinking-lite": "gemini-3.5-flash-lite",
		"gemini-flash-lite":              "gemini-3.5-flash-lite",
		"gemini-auto":                    "gemini-3.8-flash-medium",
		// Concrete wire ids and non-Gemini models pass through untouched.
		"gemini-3.8-flash-medium": "gemini-3.8-flash-medium",
		"gemini-pro-agent":        "gemini-pro-agent",
		"claude-sonnet-4-6":       "claude-sonnet-4-6",
		"gpt-oss-120b-medium":     "gpt-oss-120b-medium",
	}
	for in, want := range cases {
		if got := normalizeModel(in); got != want {
			t.Errorf("normalizeModel(%q) = %q, want %q", in, got, want)
		}
	}
}
