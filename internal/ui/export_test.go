package ui

import (
	"maps"
	"slices"
)

// JournalPairs is every kind journalSpec marks up with its outcomes, sorted.
func JournalPairs() map[string][]string {
	out := make(map[string][]string, len(journalSpec))
	for kind, outcomes := range journalSpec {
		out[kind] = slices.Sorted(maps.Keys(outcomes))
	}
	return out
}
