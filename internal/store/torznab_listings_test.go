package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func listing(b byte, source string) Listing {
	return Listing{Source: source, Hash: hash(b), Title: "Alien.1979.1080p.BluRay", Comments: "https://tracker.example/t/1",
		Categories: []int{2040, 2000}, Size: 8 << 30, PublishedAt: t0.Add(-time.Hour)}
}

func TestRecordListing(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	_, _, err := s.Insert(ctx, torrent(1, "Alien.1979.1080p.BluRay"))
	require.NoError(t, err)

	rec, err := s.RecordListing(ctx, listing(1, "spidarr"), t0)
	require.NoError(t, err)
	require.Equal(t, ListingRecorded{New: true, Stored: true}, rec)
	rec, err = s.RecordListing(ctx, listing(2, "spidarr"), t0)
	require.NoError(t, err)
	require.Equal(t, ListingRecorded{New: true}, rec)

	again := listing(2, "spidarr")
	again.IMDb, again.TMDBID, again.Categories = "tt0078748", 348, nil
	rec, err = s.RecordListing(ctx, again, t0.Add(time.Hour))
	require.NoError(t, err)
	require.False(t, rec.New, "a listing again is not new")
	rec, err = s.RecordListing(ctx, listing(2, "jackett"), t0)
	require.NoError(t, err)
	require.True(t, rec.New, "another source's listing of the hash is")

	got, err := s.Listings(ctx, hash(2))
	require.NoError(t, err)
	want := listing(2, "jackett")
	want.PublishedAt = want.PublishedAt.UTC()
	require.Equal(t, []Listing{want, {Source: "spidarr", Hash: hash(2), Title: again.Title, Comments: again.Comments, Size: again.Size,
		IMDb: "tt0078748", TMDBID: 348, PublishedAt: want.PublishedAt}}, got, "a listing again replaces what the source tells")
}

func TestSweepListings(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	for b := byte(1); b <= 3; b++ {
		_, err := s.RecordListing(ctx, listing(b, "spidarr"), t0.Add(time.Duration(b)*time.Hour))
		require.NoError(t, err)
	}
	_, err := s.RecordListing(ctx, listing(1, "spidarr"), t0.Add(5*time.Hour))
	require.NoError(t, err)

	n, err := s.SweepListings(ctx, t0.Add(4*time.Hour), 1)
	require.NoError(t, err)
	require.Equal(t, 2, n, "in batches, those listed last before the time")
	for b, left := range map[byte]int{1: 1, 2: 0, 3: 0} {
		got, err := s.Listings(ctx, hash(b))
		require.NoError(t, err)
		require.Len(t, got, left, "hash %d", b)
	}
}

func TestJournalListed(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	require.NoError(t, s.JournalListed(ctx, hash(1), t0, "queued", Detail("source", "spidarr", "title", "Alien", "categories", []int{2040, 2000})))
	require.NoError(t, s.JournalListed(ctx, hash(2), t0, "stored", Detail("source", "spidarr", "categories", []int(nil))))
	got, err := s.Journal(ctx, hash(1), 10)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, JournalListed, got[0].Kind)
	require.Equal(t, "queued", got[0].Outcome)
	require.JSONEq(t, `{"source":"spidarr","title":"Alien","categories":[2040,2000]}`, got[0].Detail)
	got, err = s.Journal(ctx, hash(2), 10)
	require.NoError(t, err)
	require.JSONEq(t, `{"source":"spidarr"}`, got[0].Detail, "no categories are left out")
}
