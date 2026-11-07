package ui

import (
	"github.com/mikluko/sniffarr/internal/version"
)

// ProjectURL is the project page the footer's version links to.
func (Base) ProjectURL() string { return "https://sarak.sh/mikluko/sniffarr" }

// VersionTip is the tooltip of the footer's version: "Project page." and,
// when the build's time and commit are known, BuildTip.
func (b Base) VersionTip() string {
	if tip := b.BuildTip(); tip != "" {
		return b.T("foot.project") + " " + tip
	}
	return b.T("foot.project")
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
