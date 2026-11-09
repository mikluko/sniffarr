package app

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProfiles(t *testing.T) {
	srv := httptest.NewServer(profiles())
	defer srv.Close()
	for _, path := range []string{"/debug/pprof/", "/debug/pprof/heap?debug=1", "/debug/pprof/goroutine?debug=1", "/debug/pprof/cmdline"} {
		resp, err := http.Get(srv.URL + path)
		require.NoError(t, err)
		resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode, path)
	}
	resp, err := http.Get(srv.URL + "/")
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusNotFound, resp.StatusCode, "nothing outside the profiles")
}
