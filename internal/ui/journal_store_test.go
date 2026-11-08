package ui_test

import (
	"slices"
	"testing"

	"sarak.sh/mikluko/sniffarr/internal/store"
	"sarak.sh/mikluko/sniffarr/internal/ui"
)

// journalSpec marks up exactly the kinds and outcomes the store journals.
func TestJournalSpecMatchesStore(t *testing.T) {
	spec := ui.JournalPairs()
	for kind, outcomes := range store.JournalOutcomes {
		want := slices.Sorted(slices.Values(outcomes))
		if got := spec[kind]; !slices.Equal(got, want) {
			t.Errorf("journalSpec[%q] has %v, the store writes %v", kind, got, want)
		}
	}
	for kind := range spec {
		if _, ok := store.JournalOutcomes[kind]; !ok {
			t.Errorf("journalSpec has %q, which the store does not write", kind)
		}
	}
}
