// Package server exposes the proxy over HTTP. It authenticates the local
// client, picks an account, and translates one request at a time. It does not
// own token refresh or the upstream protocol.
package server

import (
	"net/http"
	"time"

	"github.com/Ti800/antigravity2api/internal/auth"
	"github.com/Ti800/antigravity2api/internal/config"
	"github.com/Ti800/antigravity2api/internal/upstream"
)

// App is the process-wide state. It is safe for concurrent requests.
type App struct {
	Cfg     config.Config
	Auth    *auth.Store
	Up      *upstream.Client
	Started time.Time
	Version string
}

// New wires the dependencies. Neither store nor client may be nil.
func New(cfg config.Config, store *auth.Store, up *upstream.Client, version string) *App {
	return &App{Cfg: cfg, Auth: store, Up: up, Started: time.Now(), Version: version}
}

// Handler returns the root handler. Routes match the OpenAI and Claude shapes
// Minis already speaks.
func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", a.handleHealth)
	mux.HandleFunc("GET /{$}", a.handleHealth)
	mux.HandleFunc("GET /v1/models", a.handleModels)
	mux.HandleFunc("POST /v1/chat/completions", a.handleChat)
	mux.HandleFunc("POST /v1/messages", a.handleMessages)
	return a.withAuthAndLogging(a.withCORS(mux))
}
