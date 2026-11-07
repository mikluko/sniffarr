// Package version is the release a binary was built from: the version, the
// commit and the build time, and the Server header that names it.
package version

import (
	"net/http"
	"runtime/debug"
	"time"
)

// Set at link time with -ldflags "-X <this package>.version=0.44.0"; built
// is RFC 3339.
var (
	version string
	commit  string
	built   string
)

// Dev is the version of a build that was given none.
const Dev = "dev"

// shortCommit is how many characters of a commit are kept.
const shortCommit = 7

// Info describes one build. Commit and Built are zero when unknown.
type Info struct {
	Version string
	// Commit is at most 7 characters.
	Commit string
	// Built is in UTC.
	Built time.Time
}

// Get returns the build's Info: the link-time values, with Commit and Built
// from the build's VCS stamp where they were not linked in, and Version Dev
// where no release was linked in.
func Get() Info {
	var vcsRev, vcsTime string
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				vcsRev = s.Value
			case "vcs.time":
				vcsTime = s.Value
			}
		}
	}
	return resolve(version, commit, built, vcsRev, vcsTime)
}

func resolve(version, commit, built, vcsRev, vcsTime string) Info {
	if version == "" {
		version = Dev
	}
	if commit == "" {
		commit = vcsRev
	}
	if built == "" {
		built = vcsTime
	}
	if len(commit) > shortCommit {
		commit = commit[:shortCommit]
	}
	info := Info{Version: version, Commit: commit}
	if t, err := time.Parse(time.RFC3339, built); err == nil {
		info.Built = t.UTC()
	}
	return info
}

// Server is the value of the Server header: "sniffarr/<version>".
func (i Info) Server() string { return "sniffarr/" + i.Version }

// Middleware sets the Server header of every response of next to the
// running build's.
func Middleware(next http.Handler) http.Handler {
	server := Get().Server()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Server", server)
		next.ServeHTTP(w, r)
	})
}
