package server

import (
	"context"
	"net/http"
	"sort"
	"sync"
	"time"
)

// Model list is cached because Minis polls /v1/models often and the upstream
// list changes rarely.
var (
	modelMu    sync.Mutex
	modelCache []string
	modelAt    time.Time
)

const modelTTL = 10 * time.Minute

func (a *App) handleModels(w http.ResponseWriter, r *http.Request) {
	ids := a.models(r)
	data := make([]any, len(ids))
	now := time.Now().Unix()
	for i, id := range ids {
		data[i] = map[string]any{
			"id": id, "object": "model", "created": now, "owned_by": "antigravity",
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}

func (a *App) models(r *http.Request) []string {
	modelMu.Lock()
	if time.Since(modelAt) < modelTTL && len(modelCache) > 0 {
		out := append([]string(nil), modelCache...)
		modelMu.Unlock()
		return out
	}
	modelMu.Unlock()

	acct := a.Auth.Pick()
	if acct == nil {
		return fallbackModels()
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	token, err := a.Auth.Token(ctx, acct)
	if err != nil {
		return fallbackModels()
	}
	list, err := a.Up.FetchModels(ctx, token)
	if err != nil || len(list) == 0 {
		return fallbackModels()
	}
	ids := make([]string, 0, len(list))
	seen := map[string]bool{}
	for _, m := range list {
		if m.ID == "" || seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		ids = append(ids, m.ID)
	}
	sort.Strings(ids)

	modelMu.Lock()
	modelCache = ids
	modelAt = time.Now()
	modelMu.Unlock()
	return ids
}

func fallbackModels() []string {
	return []string{"gemini-3.8-flash-medium", "gemini-3.8-flash-high", "gemini-pro-agent"}
}

// modelAliases maps loose or legacy names onto the concrete wire ids the
// upstream accepts. A bare "gemini-3.8-flash" is a 404: flash tiers are
// carried as suffixes, and the pro tiers have their own agent ids.
var modelAliases = map[string]string{
	"gemini-3.8-flash":          "gemini-3.8-flash-medium",
	"gemini-3.8-flash-thinking": "gemini-3.8-flash-high",
	"gemini-3.7-flash":          "gemini-3.7-flash-medium",
	"gemini-3.7-flash-thinking": "gemini-3.7-flash-high",
	"gemini-3.6-flash":          "gemini-3.6-flash-medium",
	"gemini-3.1-pro":            "gemini-pro-agent",
}

// normalizeModel resolves aliases; unknown names pass through untouched.
func normalizeModel(name string) string {
	if mapped, ok := modelAliases[name]; ok {
		return mapped
	}
	return name
}
