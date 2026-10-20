// Package userinfo looks up a signed-in person's locale at an OpenID Connect
// provider's userinfo endpoint, by the access token an authenticating proxy
// passes on.
package userinfo

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Source looks locales up at an OpenID Connect provider. It is safe for
// concurrent use.
type Source struct {
	issuer string
	client *http.Client
	ttl    time.Duration
	now    func() time.Time

	mu       sync.Mutex
	userinfo string
	cache    map[[sha256.Size]byte]entry
}

type entry struct {
	locale  string
	expires time.Time
}

// New returns a Source for the provider at issuer. A locale is kept for ttl
// after it was fetched, under a hash of the token.
func New(issuer string, client *http.Client, ttl time.Duration) *Source {
	return &Source{issuer: strings.TrimRight(issuer, "/"), client: client, ttl: ttl, now: time.Now, cache: map[[sha256.Size]byte]entry{}}
}

// Locale returns the locale claim of the person the access token was issued
// to, "" when the provider gives none.
func (s *Source) Locale(ctx context.Context, token string) (string, error) {
	if token == "" {
		return "", errors.New("userinfo: no access token")
	}
	key := sha256.Sum256([]byte(token))
	now := s.now()
	s.mu.Lock()
	e, ok := s.cache[key]
	s.mu.Unlock()
	if ok && now.Before(e.expires) {
		return e.locale, nil
	}
	endpoint, err := s.endpoint(ctx)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	var claims struct {
		Locale string `json:"locale"`
	}
	if err := s.getJSON(req, &claims); err != nil {
		return "", fmt.Errorf("userinfo: %w", err)
	}
	s.mu.Lock()
	for k, e := range s.cache {
		if now.After(e.expires) {
			delete(s.cache, k)
		}
	}
	s.cache[key] = entry{claims.Locale, now.Add(s.ttl)}
	s.mu.Unlock()
	return claims.Locale, nil
}

// endpoint is the provider's userinfo endpoint, from its discovery document.
func (s *Source) endpoint(ctx context.Context) (string, error) {
	s.mu.Lock()
	known := s.userinfo
	s.mu.Unlock()
	if known != "" {
		return known, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.issuer+"/.well-known/openid-configuration", nil)
	if err != nil {
		return "", err
	}
	var doc struct {
		Userinfo string `json:"userinfo_endpoint"`
	}
	if err := s.getJSON(req, &doc); err != nil {
		return "", fmt.Errorf("userinfo: discovery: %w", err)
	}
	if doc.Userinfo == "" {
		return "", errors.New("userinfo: discovery names no userinfo endpoint")
	}
	s.mu.Lock()
	s.userinfo = doc.Userinfo
	s.mu.Unlock()
	return doc.Userinfo, nil
}

func (s *Source) getJSON(req *http.Request, v any) error {
	res, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return errors.New(res.Status)
	}
	return json.NewDecoder(res.Body).Decode(v)
}
