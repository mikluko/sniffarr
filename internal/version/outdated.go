package version

import (
	"strconv"
	"strings"
)

// Outdated reports whether a build of version running is far enough behind
// the stable release latest to announce it. A stable build is when latest
// is a later major, or a minor more than one ahead in the same major; a
// pre-release build is as soon as latest is its base version or later.
// Dev, a version that is not MAJOR.MINOR.PATCH with an optional
// pre-release, and a pre-release latest are never outdated. Either may lead
// with "v".
func Outdated(running, latest string) bool {
	run, ok := parseSemver(running)
	if !ok {
		return false
	}
	rel, ok := parseSemver(latest)
	if !ok || rel.pre {
		return false
	}
	if run.pre {
		return rel.compare(run) >= 0
	}
	if rel.major != run.major {
		return rel.major > run.major
	}
	return rel.minor > run.minor+1
}

// Prerelease reports whether v is a MAJOR.MINOR.PATCH version with a
// pre-release, such as "0.44.0-alpha.4".
func Prerelease(v string) bool {
	s, ok := parseSemver(v)
	return ok && s.pre
}

// semver is a version's numbers and whether it is a pre-release.
type semver struct {
	major, minor, patch int
	pre                 bool
}

// parseSemver reads "MAJOR.MINOR.PATCH" with an optional "-pre-release"
// and "+build", and a leading "v".
func parseSemver(s string) (semver, bool) {
	s = strings.TrimPrefix(s, "v")
	s, _, _ = strings.Cut(s, "+")
	core, pre, hasPre := strings.Cut(s, "-")
	if hasPre && pre == "" {
		return semver{}, false
	}
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return semver{}, false
	}
	var n [3]int
	for i, p := range parts {
		v, err := strconv.Atoi(p)
		if err != nil || v < 0 || p != strconv.Itoa(v) {
			return semver{}, false
		}
		n[i] = v
	}
	return semver{major: n[0], minor: n[1], patch: n[2], pre: hasPre}, true
}

// compare orders v and w by their numbers alone.
func (v semver) compare(w semver) int {
	for _, d := range [...]int{v.major - w.major, v.minor - w.minor, v.patch - w.patch} {
		if d != 0 {
			return d
		}
	}
	return 0
}
