package ui

import (
	"encoding/hex"
	"net/url"
	"strings"
)

// BackTarget is ref, a path with a query, when it is a page a title's page
// leads back to: the top, the torrents list or a release's card. Anything
// else, a URL with a host included, is "".
func BackTarget(ref string) string {
	if !strings.HasPrefix(ref, "/") || strings.HasPrefix(ref, "//") {
		return ""
	}
	u, err := url.Parse(ref)
	if err != nil || u.Scheme != "" || u.Host != "" {
		return ""
	}
	if u.Path == "/top" || u.Path == "/torrents" {
		return u.RequestURI()
	}
	if h, ok := strings.CutPrefix(u.Path, "/t/"); ok && len(h) == 40 {
		if _, err := hex.DecodeString(h); err == nil {
			return u.RequestURI()
		}
	}
	return ""
}

// WorkURL is path, a title's page, carrying the current page, without its
// language, in its from parameter for the title's back link.
func (b Base) WorkURL(path string) string {
	u, err := url.Parse(b.Path)
	if err != nil {
		return path
	}
	q := u.Query()
	q.Del("lang")
	u.RawQuery = q.Encode()
	return path + "?" + url.Values{"from": {u.RequestURI()}}.Encode()
}

// BackLabel is the text of the page's back link, following the page it leads
// to: the top of the title's kind, the torrents list or a release's card.
func (p WorkPage) BackLabel() string {
	switch {
	case strings.HasPrefix(p.Back, "/top"):
		return p.T("work.back." + p.Work.Kind)
	case strings.HasPrefix(p.Back, "/torrents"):
		return p.T("work.back.torrents")
	}
	return p.T("work.back.torrent")
}

// ResetURL is the title page without release filters, keeping its back link.
func (p WorkPage) ResetURL() string {
	if p.Back == "" {
		return p.Releases.Path
	}
	return p.Releases.Path + "?" + url.Values{"from": {p.Back}}.Encode()
}
