package ui

// MetaPart is one field of a release's meta line on a phone.
type MetaPart struct {
	Label  string // the dub language, "" for the other fields
	Text   string
	Strong bool
}

// Meta lists the fields of r that carry a value: size, quality, kind, source,
// then per configured dub language the mark or a probability of at least 0.5.
func (b Base) Meta(r Row) []MetaPart {
	parts := []MetaPart{{Text: b.Size(r.Size)}}
	if r.Quality != "" {
		parts = append(parts, MetaPart{Text: b.T("quality." + r.Quality)})
	}
	parts = append(parts, MetaPart{Text: b.T("kind." + r.Kind)})
	if r.Source != "" && r.Source != "none" {
		parts = append(parts, MetaPart{Text: b.T("source." + r.Source)})
	}
	for _, lang := range b.VoiceLangs {
		a, _ := AudioOf(r.Audio, lang)
		if a.Marker || a.Likely() {
			parts = append(parts, MetaPart{Label: lang, Text: b.AudioCell(a), Strong: a.Marker})
		}
	}
	return parts
}
