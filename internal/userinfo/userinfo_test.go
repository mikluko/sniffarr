package userinfo

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// provider serves discovery and userinfo, answering the token "good" with the
// locale ru and counting userinfo calls.
func provider(t *testing.T, calls *atomic.Int32) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"userinfo_endpoint":"` + srv.URL + `/userinfo"}`))
	})
	mux.HandleFunc("GET /userinfo", func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		switch r.Header.Get("Authorization") {
		case "Bearer good":
			_, _ = w.Write([]byte(`{"sub":"1","locale":"ru"}`))
		case "Bearer plain":
			_, _ = w.Write([]byte(`{"sub":"2"}`))
		default:
			http.Error(w, "no", http.StatusUnauthorized)
		}
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestLocale(t *testing.T) {
	ctx := context.Background()
	var calls atomic.Int32
	srv := provider(t, &calls)
	s := New(srv.URL+"/", srv.Client(), time.Minute)
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }

	l, err := s.Locale(ctx, "good")
	require.NoError(t, err)
	require.Equal(t, "ru", l)
	l, err = s.Locale(ctx, "good")
	require.NoError(t, err)
	require.Equal(t, "ru", l)
	require.EqualValues(t, 1, calls.Load(), "cached under the token")

	now = now.Add(time.Minute)
	_, err = s.Locale(ctx, "good")
	require.NoError(t, err)
	require.EqualValues(t, 2, calls.Load(), "fetched again after ttl")

	l, err = s.Locale(ctx, "plain")
	require.NoError(t, err)
	require.Empty(t, l, "no locale claim")

	_, err = s.Locale(ctx, "bad")
	require.ErrorContains(t, err, "401")
	_, err = s.Locale(ctx, "")
	require.Error(t, err)
}

func TestNoUserinfoEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{}`)) }))
	t.Cleanup(srv.Close)
	_, err := New(srv.URL, srv.Client(), time.Minute).Locale(context.Background(), "good")
	require.ErrorContains(t, err, "names no userinfo endpoint")
}
