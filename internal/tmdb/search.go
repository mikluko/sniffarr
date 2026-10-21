package tmdb

import (
	"context"
	"fmt"
	"strings"

	"github.com/mikluko/sniffarr/internal/store"
)

// NormalQuery is the query as SearchWorks asks and caches it: lowercase, its
// words joined by single spaces.
func NormalQuery(query string) string {
	return strings.Join(strings.Fields(strings.ToLower(query)), " ")
}

// SearchWorks returns, in TMDB's order, the works TMDB's search finds for
// the query among works of the type, store.TypeMovie or store.TypeTV,
// asked in lang, "en" or "ru". It asks TMDB at most once per type and
// NormalQuery within store.SearchTTL, storing every work the answer has
// with its texts in lang; concurrent calls for one search share a request.
func (m *Matcher) SearchWorks(ctx context.Context, typ, query, lang string) ([]int64, error) {
	q := NormalQuery(query)
	if q == "" {
		return nil, nil
	}
	ids, ok, err := m.Store.CachedSearch(ctx, typ, q, m.now().Add(-store.SearchTTL))
	if err != nil || ok {
		return ids, err
	}
	tag := languages[0].tag
	for _, l := range languages {
		if l.lang == lang {
			tag = l.tag
		}
	}
	v, err, _ := m.group.Do("search\x00"+typ+"\x00"+q, func() (any, error) {
		found, err := m.Client.Search(ctx, typ == store.TypeTV, q, 0, tag)
		if err != nil {
			return nil, err
		}
		hits := make([]store.Hit, len(found))
		for i, c := range found {
			hits[i] = hit(typ, tag, c)
		}
		ids, err := m.Store.RecordSearch(ctx, typ, q, hits, m.now())
		if err != nil {
			return nil, fmt.Errorf("record search: %w", err)
		}
		return ids, nil
	})
	if err != nil {
		return nil, err
	}
	return v.([]int64), nil
}
