// Package config loads the proxy configuration. Missing fields keep their
// defaults, so a partial config.json is valid.
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Config is the on-disk configuration. It is a construction-time snapshot;
// callers must not mutate a live copy.
type Config struct {
	Port              int      `json:"port"`
	Host              string   `json:"host"`
	APIKeys           []string `json:"api_keys"`
	AuthDir           string   `json:"auth_dir"`
	RequestTimeoutSec int      `json:"request_timeout_sec"`
	LogRequests       bool     `json:"log_requests"`
	// GOMAXPROCS caps the Go scheduler. 0 keeps the runtime default (1 on iSH).
	GOMAXPROCS int `json:"gomaxprocs"`
	// GCPercent is the GC target percentage. 0 keeps the runtime default.
	GCPercent int `json:"gc_percent"`
	// MemoryLimitMB is a soft heap cap passed to debug.SetMemoryLimit. 0 disables it.
	MemoryLimitMB int `json:"memory_limit_mb"`
}

// Default returns the iOS-oriented defaults.
func Default() Config {
	return Config{
		Port:              8081,
		Host:              "127.0.0.1",
		RequestTimeoutSec: 180,
		LogRequests:       true,
		GOMAXPROCS:        1,
		GCPercent:         50,
		MemoryLimitMB:     48,
	}
}

// Load reads path and overlays it on Default. An empty path returns Default.
func Load(path string) (Config, error) {
	cfg := Default()
	if path == "" {
		return cfg, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	var aux struct {
		Port              *int      `json:"port"`
		Host              *string   `json:"host"`
		APIKeys           *[]string `json:"api_keys"`
		AuthDir           *string   `json:"auth_dir"`
		RequestTimeoutSec *int      `json:"request_timeout_sec"`
		LogRequests       *bool     `json:"log_requests"`
		GOMAXPROCS        *int      `json:"gomaxprocs"`
		GCPercent         *int      `json:"gc_percent"`
		MemoryLimitMB     *int      `json:"memory_limit_mb"`
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return cfg, err
	}
	if aux.Port != nil {
		cfg.Port = *aux.Port
	}
	if aux.Host != nil {
		cfg.Host = *aux.Host
	}
	if aux.APIKeys != nil {
		cfg.APIKeys = *aux.APIKeys
	}
	if aux.AuthDir != nil {
		cfg.AuthDir = *aux.AuthDir
	}
	if aux.RequestTimeoutSec != nil {
		cfg.RequestTimeoutSec = *aux.RequestTimeoutSec
	}
	if aux.LogRequests != nil {
		cfg.LogRequests = *aux.LogRequests
	}
	if aux.GOMAXPROCS != nil {
		cfg.GOMAXPROCS = *aux.GOMAXPROCS
	}
	if aux.GCPercent != nil {
		cfg.GCPercent = *aux.GCPercent
	}
	if aux.MemoryLimitMB != nil {
		cfg.MemoryLimitMB = *aux.MemoryLimitMB
	}
	return cfg, nil
}

// Find looks for config.json next to the working directory, then under
// ~/.config/antigravity2api/.
func Find() string {
	paths := []string{"./config.json"}
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, ".config", "antigravity2api", "config.json"))
	}
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}
