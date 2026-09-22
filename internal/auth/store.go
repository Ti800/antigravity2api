// Package auth stores Antigravity account credentials and refreshes access
// tokens. Credentials are the user's own OAuth grant; this package never logs
// token material.
package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// OAuth client used to refresh Antigravity tokens. Both values come from the
// environment so the public desktop-client credentials are not stored in the
// source tree, where GitHub secret scanning rejects the push.
func clientID() string { return os.Getenv("ANTIGRAVITY_CLIENT_ID") }

func clientSecret() string { return os.Getenv("ANTIGRAVITY_CLIENT_SECRET") }

// Account is one authorized Antigravity account.
type Account struct {
	Email        string    `json:"email"`
	RefreshToken string    `json:"refresh_token"`
	AccessToken  string    `json:"access_token,omitempty"`
	ExpiresAt    time.Time `json:"expires_at,omitempty"`
	ProjectID    string    `json:"project_id,omitempty"`
}

// Store loads accounts from a directory of JSON files and refreshes tokens.
type Store struct {
	dir   string
	http  *http.Client
	mu    sync.Mutex
	accts []*Account
	next  int
}

// Load reads every *.json file in dir. Files that are not accounts are skipped.
func Load(dir string, client *http.Client) (*Store, error) {
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	s := &Store{dir: dir, http: client}
	if dir == "" {
		return s, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var a Account
		if json.Unmarshal(data, &a) != nil || a.RefreshToken == "" {
			continue
		}
		s.accts = append(s.accts, &a)
	}
	return s, nil
}

// Count reports how many accounts are loaded.
func (s *Store) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.accts)
}

// Emails reports account emails for the startup banner. Tokens are not included.
func (s *Store) Emails() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.accts))
	for _, a := range s.accts {
		if a.Email != "" {
			out = append(out, a.Email)
		}
	}
	return out
}

// Pick returns the next account, round-robin. nil means none are configured.
func (s *Store) Pick() *Account {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.accts) == 0 {
		return nil
	}
	a := s.accts[s.next%len(s.accts)]
	s.next++
	return a
}

// Token returns a valid access token, refreshing it when it expires within skew.
func (s *Store) Token(ctx context.Context, a *Account) (string, error) {
	if a == nil {
		return "", errors.New("no account")
	}
	s.mu.Lock()
	fresh := a.AccessToken != "" && time.Until(a.ExpiresAt) > 60*time.Second
	token := a.AccessToken
	refresh := a.RefreshToken
	s.mu.Unlock()
	if fresh {
		return token, nil
	}
	return s.refresh(ctx, a, refresh)
}

func (s *Store) refresh(ctx context.Context, a *Account, refreshToken string) (string, error) {
	id, secret := clientID(), clientSecret()
	if id == "" || secret == "" {
		return "", errors.New("refresh token: ANTIGRAVITY_CLIENT_ID and ANTIGRAVITY_CLIENT_SECRET must be set")
	}
	form := url.Values{}
	form.Set("client_id", id)
	form.Set("client_secret", secret)
	form.Set("refresh_token", refreshToken)
	form.Set("grant_type", "refresh_token")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://oauth2.googleapis.com/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := s.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("refresh token: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("refresh token: status %d", resp.StatusCode)
	}
	var tr struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tr); err != nil {
		return "", fmt.Errorf("refresh token: decode: %w", err)
	}
	if tr.AccessToken == "" {
		return "", errors.New("refresh token: empty access token")
	}
	s.mu.Lock()
	a.AccessToken = tr.AccessToken
	if tr.ExpiresIn <= 0 {
		tr.ExpiresIn = 3600
	}
	a.ExpiresAt = time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	s.mu.Unlock()
	return tr.AccessToken, nil
}

// SetProject records the Cloud Code project id discovered for an account.
func (s *Store) SetProject(a *Account, project string) {
	if a == nil || project == "" {
		return
	}
	s.mu.Lock()
	a.ProjectID = project
	s.mu.Unlock()
}
