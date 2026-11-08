package ui

import (
	"sarak.sh/mikluko/sniffarr/internal/version"
)

// projectURL is the project page.
const projectURL = "https://sarak.sh/mikluko/sniffarr"

// VersionURL is where the footer's version links: the release notes of the
// build's version, or the project page for a build without a release's
// version, such as dev.
func (b Base) VersionURL() string {
	if version.Tagged(b.Build.Version) {
		return projectURL + "/releases/tag/v" + b.Build.Version
	}
	return projectURL
}

// VersionTip is the tooltip of the footer's version: what VersionURL opens
// and, when the build's time and commit are known, BuildTip.
func (b Base) VersionTip() string {
	what := b.T("foot.project")
	if version.Tagged(b.Build.Version) {
		what = b.T("foot.release")
	}
	if tip := b.BuildTip(); tip != "" {
		return what + " " + tip
	}
	return what
}

// Update is the footer's notice of a newer stable release: its Label, the
// Tip naming this build and the release, and the URL of its notes.
type Update struct {
	Label, Tip, URL string
}

// Update returns the notice of Latest, nil unless version.Outdated holds
// for the build and Latest under Notice.
func (b Base) Update() *Update {
	if b.Latest.Version == "" || !version.Outdated(b.Build.Version, b.Latest.Version, b.Notice) {
		return nil
	}
	key := "foot.update_tip"
	switch {
	case version.Prerelease(b.Build.Version):
		key = "foot.update_tip_pre"
	case b.Notice == version.Eager:
		key = "foot.update_tip_eager"
	}
	return &Update{
		Label: b.Fmt("foot.update", b.Latest.Version),
		Tip:   b.Fmt(key, b.Build.Version, b.Latest.Version, b.FullDate(b.Latest.Published)),
		URL:   b.Latest.URL,
	}
}
