package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestTitles(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	for _, tr := range []struct {
		b         byte
		name, key string
		kind      string
	}{
		{1, "Alien.1979.1080p.BluRay", "y:alien 1979", "movie"},
		{2, "Alien.1979.Directors.Cut.2160p", "y:alien 1979", "movie"},
		{3, "Severance.S02E01", "s:severance", "series"},
		{4, "Moon.Safari.1998.FLAC", "y:moon safari 1998", "music"},
		{5, "Heat.1995.Rus", "y:heat 1995", ""},
	} {
		x := torrent(tr.b, tr.name)
		x.TitleKey, x.Kind = tr.key, tr.kind
		if tr.kind != "" {
			x.KindLayer = LayerRules
		}
		_, _, err := s.Insert(ctx, x)
		require.NoError(t, err)
	}

	keys, err := s.UnmatchedTitles(ctx, nil, t0, 10)
	require.NoError(t, err)
	require.Equal(t, []TitleKey{
		{Key: "s:severance", Kind: "series", Name: "Severance.S02E01"},
		{Key: "y:alien 1979", Kind: "movie", Name: "Alien.1979.Directors.Cut.2160p"},
	}, keys, "movies and series only, newest first, with the newest name")
	keys, err = s.UnmatchedTitles(ctx, []string{"y:alien 1979", "y:moon safari 1998"}, t0, 10)
	require.NoError(t, err)
	require.Equal(t, []TitleKey{{Key: "y:alien 1979", Kind: "movie", Name: "Alien.1979.Directors.Cut.2160p"}}, keys)
	keys, err = s.UnmatchedTitles(ctx, []string{}, t0, 10)
	require.NoError(t, err)
	require.Empty(t, keys)

	alien := Title{Year: 1979, Confidence: 1, Layer: LayerTMDB, IDs: []ExternalID{{ProviderTMDB, "348"}, {ProviderIMDb, "tt0078748"}}}
	require.NoError(t, s.RecordTitle(ctx, "y:alien 1979", alien, t0))
	require.NoError(t, s.RecordTitle(ctx, "s:severance", Title{}, t0))
	keys, err = s.UnmatchedTitles(ctx, nil, t0, 10)
	require.NoError(t, err)
	require.Empty(t, keys, "a failed match is not retried before its time")
	keys, err = s.UnmatchedTitles(ctx, nil, t0.Add(time.Second), 10)
	require.NoError(t, err)
	require.Equal(t, []string{"s:severance"}, titleKeys(keys), "then it is, and a match never is")

	r, err := s.Get(ctx, hash(1))
	require.NoError(t, err)
	require.Equal(t, Title{}, r.Title, "without Options.Titles")
	s.opts.Titles = true
	r, err = s.Get(ctx, hash(1))
	require.NoError(t, err)
	require.Equal(t, "y:alien 1979", r.TitleKey)
	require.Equal(t, Title{Year: 1979, Confidence: 1, Layer: LayerTMDB, IDs: []ExternalID{{ProviderIMDb, "tt0078748"}, {ProviderTMDB, "348"}}}, r.Title)

	rs, err := s.Search(ctx, Query{ID: &ExternalID{ProviderIMDb, "tt0078748"}, Limit: 10})
	require.NoError(t, err)
	require.Len(t, rs, 2, "every release of the key")
	for _, r := range rs {
		require.Equal(t, "y:alien 1979", r.TitleKey)
		require.Len(t, r.Title.IDs, 2)
	}
	rs, err = s.Search(ctx, Query{ID: &ExternalID{ProviderTMDB, "tt0078748"}, Limit: 10})
	require.NoError(t, err)
	require.Empty(t, rs, "the ID of another provider")

	require.NoError(t, s.RecordTitle(ctx, "y:alien 1979", Title{Year: 1979, Confidence: 0.6, Layer: LayerJev, IDs: []ExternalID{{ProviderTMDB, "8077"}}}, t0))
	r, err = s.Get(ctx, hash(2))
	require.NoError(t, err)
	require.Equal(t, []ExternalID{{ProviderTMDB, "8077"}}, r.Title.IDs, "a match replaces the one before")

	_, err = s.w.ExecContext(ctx, "DELETE FROM titles WHERE title_key = 'y:alien 1979'")
	require.NoError(t, err)
	var ids int
	require.NoError(t, s.r.QueryRowContext(ctx, "SELECT count(*) FROM title_ids").Scan(&ids))
	require.Zero(t, ids, "IDs go with their key")
}

func titleKeys(ks []TitleKey) []string {
	var out []string
	for _, k := range ks {
		out = append(out, k.Key)
	}
	return out
}
