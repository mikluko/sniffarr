package version

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestResolve(t *testing.T) {
	linked := resolve("0.44.0", "1a2b3c4d5e6f", "2026-11-07T16:02:00+02:00", "feedfacefeedface", "2020-01-01T00:00:00Z")
	require.Equal(t, Info{Version: "0.44.0", Commit: "1a2b3c4", Built: time.Date(2026, 11, 7, 14, 2, 0, 0, time.UTC)}, linked,
		"linked values win; the commit is cut to 7 and the time is UTC")
	require.Equal(t, "sniffarr/0.44.0", linked.Server())

	stamped := resolve("", "", "", "feedfacefeedface", "2026-11-07T14:02:00Z")
	require.Equal(t, Info{Version: "dev", Commit: "feedfac", Built: time.Date(2026, 11, 7, 14, 2, 0, 0, time.UTC)}, stamped,
		"unlinked values come from the VCS stamp, and no release reads dev")

	require.Equal(t, Info{Version: "dev"}, resolve("", "", "not a time", "", ""))
}

func TestMiddlewareSetsServerOnEveryAnswer(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ok", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("GET /boom", func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "boom", http.StatusInternalServerError) })
	h := Middleware(mux)
	want := Get().Server()
	for path, code := range map[string]int{"/ok": 200, "/boom": 500, "/missing": 404} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		require.Equal(t, code, w.Code, path)
		require.Equal(t, want, w.Header().Get("Server"), path)
	}
}

// TestLinkerFlags builds a program with the flags the Dockerfile and the
// release workflow pass and checks that Get reads them.
func TestLinkerFlags(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a binary")
	}
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	bin := filepath.Join(t.TempDir(), "print")
	pkg := "github.com/mikluko/sniffarr/internal/version"
	flags := "-X " + pkg + ".version=0.44.0-alpha.2 -X " + pkg + ".commit=1a2b3c4d5e6f7890 -X " + pkg + ".built=2026-11-07T14:02:00Z"
	build := exec.Command("go", "build", "-ldflags", flags, "-o", bin, "./internal/version/testdata/print")
	build.Dir = root
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	out, err := build.CombinedOutput()
	require.NoError(t, err, string(out))
	got, err := exec.Command(bin).Output()
	require.NoError(t, err)
	require.Equal(t, "0.44.0-alpha.2 1a2b3c4 2026-11-07T14:02:00Z sniffarr/0.44.0-alpha.2", strings.TrimSpace(string(got)))
}
