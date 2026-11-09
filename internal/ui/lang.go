package ui

import (
	"fmt"
	"net/http"
	"slices"
	"strings"

	libui "sarak.sh/mikluko/libarr/ui"
)

// Languages are the languages the UI has messages in.
var Languages = []string{"en", "ru"}

// DefaultLanguages are the UI's languages when none are configured.
var DefaultLanguages = []string{"en"}

// ParseLanguages reads a comma-separated list of UI languages in the order
// the switcher shows them, the first the default; "C" and "POSIX" are "en".
// An empty spec is DefaultLanguages; an empty entry or one not in Languages
// is an error.
func ParseLanguages(spec string) ([]string, error) {
	if strings.TrimSpace(spec) == "" {
		return slices.Clone(DefaultLanguages), nil
	}
	var out []string
	for _, l := range strings.Split(spec, ",") {
		l = strings.TrimSpace(l)
		if l == "C" || l == "POSIX" {
			l = "en"
		}
		l = strings.ToLower(l)
		if !slices.Contains(Languages, l) {
			return nil, fmt.Errorf("UI language %q: not one of %s", l, strings.Join(Languages, ", "))
		}
		if !slices.Contains(out, l) {
			out = append(out, l)
		}
	}
	return out, nil
}

// Lang resolves the UI language among supported as libarr's ui.Lang does,
// falling back on userLocale, the signed-in person's locale: one starting
// with "ru" is "ru"; "C", "POSIX" and one starting with "en" are "en".
func Lang(w http.ResponseWriter, req *http.Request, supported []string, userLocale string) string {
	return libui.Lang(w, req, supported, func(*http.Request) string {
		switch l := strings.ToLower(userLocale); {
		case strings.HasPrefix(l, "ru"):
			return "ru"
		case userLocale == "C", userLocale == "POSIX", strings.HasPrefix(l, "en"):
			return "en"
		}
		return ""
	})
}
