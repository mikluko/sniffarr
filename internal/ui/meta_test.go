package ui

import (
	"reflect"
	"testing"

	libui "sarak.sh/mikluko/libarr/ui"
)

func TestMeta(t *testing.T) {
	b := Base{Locale: libui.Locale{Lang: "en", Messages: map[string]string{
		"quality.1080p": "1080p", "kind.movie": "movie", "source.web": "web",
	}}, VoiceLangs: []string{"ru"}}
	ru := func(a Audio) []Audio { a.Lang = "ru"; return []Audio{a} }
	pr := func(p float64) *float64 { return &p }
	row := Row{Kind: "movie", Quality: "1080p", Source: "web"}
	size, q, k, s := MetaPart{Text: b.Size(0)}, MetaPart{Text: "1080p"}, MetaPart{Text: "movie"}, MetaPart{Text: "web"}
	cases := []struct {
		name string
		row  func(Row) Row
		want []MetaPart
	}{
		{"all fields, no dub", func(r Row) Row { return r }, []MetaPart{size, q, k, s}},
		{"no quality", func(r Row) Row { r.Quality = ""; return r }, []MetaPart{size, k, s}},
		{"source none", func(r Row) Row { r.Source = "none"; return r }, []MetaPart{size, q, k}},
		{"source empty", func(r Row) Row { r.Source = ""; return r }, []MetaPart{size, q, k}},
		{"probability zero", func(r Row) Row { r.Audio = ru(Audio{Probability: pr(0)}); return r }, []MetaPart{size, q, k, s}},
		{"probability below half", func(r Row) Row { r.Audio = ru(Audio{Probability: pr(.49)}); return r }, []MetaPart{size, q, k, s}},
		{"probability half", func(r Row) Row { r.Audio = ru(Audio{Probability: pr(.5)}); return r }, []MetaPart{size, q, k, s, {Label: "ru", Text: "0.50"}}},
		{"marker", func(r Row) Row { r.Audio = ru(Audio{Marker: true, Mark: "LostFilm", Probability: pr(0)}); return r }, []MetaPart{size, q, k, s, {Label: "ru", Text: "LostFilm", Strong: true}}},
	}
	for _, c := range cases {
		if got := b.Meta(c.row(row)); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
}
