package tmdb

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"
)

func TestFindTVDB(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("external_source") != "tvdb_id" || r.URL.Query().Get("api_key") != "secret" {
			http.Error(w, "{}", http.StatusBadRequest)
			return
		}
		switch r.URL.Path {
		case "/3/find/81189":
			_, _ = w.Write([]byte(`{"movie_results":[],"tv_results":[{"id":1396,"name":"Breaking Bad"}]}`))
		default:
			_, _ = w.Write([]byte(`{"movie_results":[],"tv_results":[]}`))
		}
	}))
	t.Cleanup(srv.Close)
	c := NewClient("secret", 1000)
	c.URL, c.Backoff, c.Limiter = srv.URL, time.Millisecond, rate.NewLimiter(rate.Inf, 1)

	id, err := c.FindTVDB(context.Background(), 81189)
	require.NoError(t, err)
	require.Equal(t, 1396, id)
	id, err = c.FindTVDB(context.Background(), 1)
	require.NoError(t, err)
	require.Zero(t, id, "TMDB knows no series of the ID")
}
