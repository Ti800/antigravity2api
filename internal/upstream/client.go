// Package upstream talks to the Antigravity Cloud Code endpoint. It owns
// authentication headers, the request envelope, and streaming reads. It does
// not know about OpenAI or Claude request shapes.
package upstream

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	// BaseURL is the Cloud Code endpoint. The production host returns 429 for
	// accounts whose quota is only honored on the daily host, which is also where
	// onboarding happens. Both hosts speak the same protocol.
	BaseURL = "https://daily-cloudcode-pa.googleapis.com"
	// DailyBaseURL is used only to onboard an account that has no project yet.
	DailyBaseURL = "https://daily-cloudcode-pa.googleapis.com"
	version      = "v1internal"

	loadPath    = "/" + version + ":loadCodeAssist"
	onboardPath = "/" + version + ":onboardUser"
	modelsPath  = "/" + version + ":fetchAvailableModels"
	streamPath  = "/" + version + ":streamGenerateContent?alt=sse"
	genPath     = "/" + version + ":generateContent"
)

// Client is safe for concurrent use.
type Client struct {
	HTTP    *http.Client
	BaseURL string
	// UserAgent is sent on every upstream call. Empty selects a conservative default.
	UserAgent string
}

// IDEUserAgent is the client identity Cloud Code requires. A generic UA is
// accepted for model listing but rejected for project discovery and generation.
const IDEUserAgent = "antigravity/ide/2.5.5 (os_type=windows; arch=amd64; aidev_client; auth_method=oauth)"

// New builds a client with a small, explicit connection pool. iOS reclaims
// background processes, so there is no value in holding many idle connections.
func New(timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 180 * time.Second
	}
	tr := &http.Transport{
		Proxy:                  http.ProxyFromEnvironment,
		DialContext:            (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:      true,
		MaxIdleConns:           4,
		MaxIdleConnsPerHost:    2,
		IdleConnTimeout:        30 * time.Second,
		TLSHandshakeTimeout:    10 * time.Second,
		ExpectContinueTimeout:  time.Second,
		DisableCompression:     false,
		MaxResponseHeaderBytes: 32 << 10,
	}
	c := &Client{
		HTTP:      &http.Client{Transport: tr, Timeout: timeout},
		BaseURL:   BaseURL,
		UserAgent: IDEUserAgent,
	}
	// Google gates newer models behind a later IDE version occasionally; allow an
	// override without a rebuild.
	if ua := strings.TrimSpace(os.Getenv("ANTIGRAVITY_USER_AGENT")); ua != "" {
		c.UserAgent = ua
	}
	return c
}

// StatusError is a non-2xx upstream response. Body is capped and never logged
// with credentials; callers decide whether to surface it.
type StatusError struct {
	Status int
	Body   string
}

func (e *StatusError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("upstream status %d", e.Status)
	}
	return fmt.Sprintf("upstream status %d: %s", e.Status, e.Body)
}

func (c *Client) post(ctx context.Context, path, token string, payload any) (*http.Response, error) {
	var body io.Reader
	switch p := payload.(type) {
	case []byte:
		body = bytes.NewReader(p)
	case json.RawMessage:
		body = bytes.NewReader(p)
	default:
		raw, err := json.Marshal(p)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	return c.HTTP.Do(req)
}

func readErr(resp *http.Response) error {
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	return &StatusError{Status: resp.StatusCode, Body: strings.TrimSpace(string(b))}
}

// LoadProject asks loadCodeAssist for the account's Cloud Code project id.
// An empty result means the account still needs onboarding.
func (c *Client) LoadProject(ctx context.Context, token string) (string, error) {
	resp, err := c.post(ctx, loadPath, token, map[string]any{
		"metadata": map[string]string{"ideType": "ANTIGRAVITY"},
	})
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", readErr(resp)
	}
	defer resp.Body.Close()
	var data map[string]any
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&data); err != nil {
		return "", err
	}
	return projectOf(data), nil
}

// OnboardUser polls the daily endpoint until the account has a project.
func (c *Client) OnboardUser(ctx context.Context, token, tier string) (string, error) {
	if tier == "" {
		tier = "free-tier"
	}
	saved := c.BaseURL
	c.BaseURL = DailyBaseURL
	defer func() { c.BaseURL = saved }()

	payload := map[string]any{
		"tier_id": tier,
		"metadata": map[string]string{
			"ide_type":    "ANTIGRAVITY",
			"ide_name":    "antigravity",
			"ide_version": "2.5.5",
		},
	}
	for attempt := 0; attempt < 5; attempt++ {
		resp, err := c.post(ctx, onboardPath, token, payload)
		if err != nil {
			return "", err
		}
		// Transient upstream failures keep the poll going; hard 4xx gives up.
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(2 * time.Second):
			}
			continue
		}
		if resp.StatusCode != http.StatusOK {
			return "", readErr(resp)
		}
		var data struct {
			Done     bool           `json:"done"`
			Response map[string]any `json:"response"`
		}
		err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&data)
		resp.Body.Close()
		if err != nil {
			return "", err
		}
		if data.Done {
			if id := projectOf(data.Response); id != "" {
				return id, nil
			}
			return "", errors.New("onboard completed without a project id")
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return "", errors.New("onboard did not complete")
}

func projectOf(data map[string]any) string {
	if data == nil {
		return ""
	}
	for _, key := range []string{"cloudaicompanionProject", "projectId", "project"} {
		switch v := data[key].(type) {
		case string:
			if s := strings.TrimSpace(v); s != "" {
				return s
			}
		case map[string]any:
			if id, ok := v["id"].(string); ok && strings.TrimSpace(id) != "" {
				return strings.TrimSpace(id)
			}
		}
	}
	return ""
}

// Model is one model advertised by fetchAvailableModels.
type Model struct {
	ID   string
	Name string
}

// FetchModels returns the models the account can call. The response shape has
// drifted between releases, so several envelopes are accepted.
func (c *Client) FetchModels(ctx context.Context, token string) ([]Model, error) {
	resp, err := c.post(ctx, modelsPath, token, map[string]any{})
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, readErr(resp)
	}
	defer resp.Body.Close()
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&raw); err != nil {
		return nil, err
	}
	var list []Model
	for _, key := range []string{"models", "availableModels"} {
		if blob, ok := raw[key]; ok {
			list = append(list, parseModelList(blob)...)
		}
	}
	return list, nil
}

func parseModelList(blob json.RawMessage) []Model {
	var asMap map[string]json.RawMessage
	if json.Unmarshal(blob, &asMap) == nil && len(asMap) > 0 {
		out := make([]Model, 0, len(asMap))
		for id := range asMap {
			out = append(out, Model{ID: id, Name: id})
		}
		return out
	}
	var asList []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if json.Unmarshal(blob, &asList) == nil {
		out := make([]Model, 0, len(asList))
		for _, m := range asList {
			id := m.ID
			if id == "" {
				id = m.Name
			}
			if id != "" {
				out = append(out, Model{ID: id, Name: id})
			}
		}
		return out
	}
	return nil
}

// Stream sends a prepared envelope and returns the raw response for the caller
// to read as SSE. The caller must close the body.
func (c *Client) Stream(ctx context.Context, token string, envelope []byte) (*http.Response, error) {
	resp, err := c.post(ctx, streamPath, token, envelope)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, readErr(resp)
	}
	return resp, nil
}

// Generate sends a non-streaming request and returns the raw JSON body.
func (c *Client) Generate(ctx context.Context, token string, envelope []byte) ([]byte, error) {
	resp, err := c.post(ctx, genPath, token, envelope)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, readErr(resp)
	}
	defer resp.Body.Close()
	return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
}

// ReadSSE yields each `data:` payload from an upstream SSE body. Blank lines
// and comments are skipped. The function returns when the body ends.
func ReadSSE(r io.Reader, fn func(data []byte) error) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	var data []byte
	flush := func() error {
		if len(data) == 0 {
			return nil
		}
		err := fn(data)
		data = data[:0]
		return err
	}
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			if err := flush(); err != nil {
				return err
			}
			continue
		}
		if line[0] == ':' {
			continue
		}
		if bytes.HasPrefix(line, []byte("data:")) {
			payload := bytes.TrimPrefix(line, []byte("data:"))
			payload = bytes.TrimPrefix(payload, []byte(" "))
			if len(data) > 0 {
				data = append(data, '\n')
			}
			data = append(data, payload...)
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return flush()
}
