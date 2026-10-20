package store

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/mikluko/sniffarr/internal/rules"
)

// Browse selects releases for Page.
type Browse struct {
	// Text matches releases whose name or file paths contain all its words.
	Text string
	// Kind selects releases of the kind; UnknownKind selects those whose kind
	// is unknown instead.
	Kind        string
	UnknownKind bool
	// Exclude drops releases of these kinds.
	Exclude []string
	// AudioMarker selects releases with a marker of AudioLanguage, and
	// MinAudioProbability those whose probability of an audio track in it is
	// at least that.
	AudioLanguage       string
	AudioMarker         bool
	MinAudioProbability *float64
	Source              string
	KindLayer           string
	// MinSize drops releases smaller than it, in bytes.
	MinSize       int64
	Offset, Limit int
	// Sort is SortPeers, SortLastSeen or SortSize; "" is best text match
	// first with Text and most recently seen first without.
	Sort string
}

// Sorts of a page.
const (
	SortPeers    = "peers"
	SortLastSeen = "last_seen"
	SortSize     = "size"
)

// Page returns the releases b selects, in b's sort, and how many it selects
// in all.
func (s *Store) Page(ctx context.Context, b Browse) ([]Release, int64, error) {
	var where []string
	var args []any
	from := "torrents t"
	order := "t.last_seen DESC, t.id DESC"
	if m := matchExpr(b.Text); m != "" {
		from = "torrents_fts f JOIN torrents t ON t.id = f.rowid"
		where = append(where, "torrents_fts MATCH ?")
		args = append(args, m)
		order = "f.rank, t.id"
	}
	switch b.Sort {
	case SortPeers:
		order = "t.peers_week DESC, t.id DESC"
	case SortLastSeen:
		order = "t.last_seen DESC, t.id DESC"
	case SortSize:
		order = "t.size DESC, t.id DESC"
	}
	switch {
	case b.UnknownKind:
		where = append(where, "t.kind IS NULL")
	case b.Kind != "":
		where = append(where, "t.kind = ?")
		args = append(args, b.Kind)
	}
	if len(b.Exclude) > 0 {
		where = append(where, "coalesce(t.kind, '') NOT IN ("+placeholders(len(b.Exclude))+")")
		for _, k := range b.Exclude {
			args = append(args, k)
		}
	}
	if b.AudioMarker {
		where = append(where, "EXISTS (SELECT 1 FROM audio a WHERE a.torrent_id = t.id AND a.language = ? AND a.marker)")
		args = append(args, b.AudioLanguage)
	}
	if b.MinAudioProbability != nil {
		where = append(where, "EXISTS (SELECT 1 FROM audio a WHERE a.torrent_id = t.id AND a.language = ? AND a.probability >= ?)")
		args = append(args, b.AudioLanguage, *b.MinAudioProbability)
	}
	if b.Source != "" {
		where = append(where, "t.source = ?")
		args = append(args, b.Source)
	}
	if b.KindLayer != "" {
		where = append(where, "t.kind_layer = ?")
		args = append(args, b.KindLayer)
	}
	if b.MinSize > 0 {
		where = append(where, "t.size >= ?")
		args = append(args, b.MinSize)
	}
	cond := ""
	if len(where) > 0 {
		cond = " WHERE " + strings.Join(where, " AND ")
	}
	var total int64
	if err := s.r.QueryRowContext(ctx, "SELECT count(*) FROM "+from+cond, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.r.QueryContext(ctx, "SELECT "+releaseColumns+" FROM "+from+cond+" ORDER BY "+order+" LIMIT ? OFFSET ?",
		append(args, b.Limit, b.Offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []Release
	for rows.Next() {
		r, err := scanRelease(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, r)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, 0, err
	}
	return out, total, s.audio(ctx, out)
}

// Card is a stored release with what its page shows besides.
type Card struct {
	Release
	Discovery string
	// Files are the release's first stored files, in stored order; Stored
	// counts them all.
	Files  []rules.File
	Stored int
	// Peers are the release's distinct peers over each window.
	Peers PeerCounts
	// Work is the release's work, zero without a work key; Releases counts
	// its releases of any kind and ReleaseRank is the release's place among
	// them by PeersWeek, 1 first.
	Work        Work
	Releases    int64
	ReleaseRank int
}

// Card returns the stored release with the hash, its first files, at most
// files of them, and its peers and work at now, or ErrNotFound.
func (s *Store) Card(ctx context.Context, h Hash, files int, now time.Time) (Card, error) {
	r, err := s.Get(ctx, h)
	if err != nil {
		return Card{}, err
	}
	c := Card{Release: r}
	if err := s.r.QueryRowContext(ctx, "SELECT discovery, (SELECT count(*) FROM files WHERE torrent_id = t.id) FROM torrents t WHERE t.id = ?", r.ID).
		Scan(&c.Discovery, &c.Stored); err != nil {
		return Card{}, err
	}
	if c.Files, err = s.files(ctx, r.ID, files); err != nil {
		return Card{}, err
	}
	if c.Peers, err = s.Peers(ctx, r.ID, now); err != nil {
		return Card{}, err
	}
	if r.WorkKey == "" {
		return c, nil
	}
	kind := ""
	if slices.Contains(TopKinds, r.Kind) {
		kind = r.Kind
	}
	works, err := s.works(ctx, kind, []string{r.WorkKey}, VoiceAny)
	if err != nil {
		return Card{}, err
	}
	c.Work = works[r.WorkKey]
	if err := s.r.QueryRowContext(ctx, `SELECT count(*), 1 + count(*) FILTER (WHERE peers_week > ? OR peers_week = ? AND id > ?)
		FROM torrents WHERE work_key = ?`, r.PeersWeek, r.PeersWeek, r.ID, r.WorkKey).Scan(&c.Releases, &c.ReleaseRank); err != nil {
		return Card{}, err
	}
	return c, nil
}

// Stats is what the store holds, in counts of torrents.
type Stats struct {
	Total int64
	// Kinds counts stored torrents by kind, "" for unknown; Layers by the
	// layer that decided the kind, "" for none; Sources by source, "" for
	// none.
	Kinds, Layers, Sources map[string]int64
	// Audio counts, per language of Options.Languages, the stored torrents
	// with a marker, and those without one whose probability is at least 0.5
	// or below it.
	Audio map[string]AudioCounts
	// Rejected counts rejected infohashes by reason.
	Rejected map[string]int64
	// Pending counts the torrents Unclassified selects.
	Pending int64
	// Days counts the torrents first seen in each of the 24-hour periods
	// before now, the latest first.
	Days []int64
}

// AudioCounts are the audio counts of one language.
type AudioCounts struct {
	Marker, Likely, Unlikely int64
}

// Stats returns the store's counts at now, with days periods in Days.
func (s *Store) Stats(ctx context.Context, now time.Time, days int) (Stats, error) {
	st := Stats{Kinds: map[string]int64{}, Layers: map[string]int64{}, Sources: map[string]int64{},
		Audio: map[string]AudioCounts{}, Rejected: map[string]int64{}, Days: make([]int64, days)}
	if err := s.each(ctx, func(rows *sql.Rows) error {
		var kind, layer, source string
		var n int64
		if err := rows.Scan(&kind, &layer, &source, &n); err != nil {
			return err
		}
		st.Total += n
		st.Kinds[kind] += n
		st.Layers[layer] += n
		st.Sources[source] += n
		return nil
	}, "SELECT coalesce(kind, ''), coalesce(kind_layer, ''), coalesce(source, ''), count(*) FROM torrents GROUP BY 1, 2, 3"); err != nil {
		return Stats{}, err
	}
	if len(s.opts.Languages) > 0 {
		args := make([]any, len(s.opts.Languages))
		for i, l := range s.opts.Languages {
			args[i] = l
		}
		if err := s.each(ctx, func(rows *sql.Rows) error {
			var lang string
			var c AudioCounts
			if err := rows.Scan(&lang, &c.Marker, &c.Likely, &c.Unlikely); err != nil {
				return err
			}
			st.Audio[lang] = c
			return nil
		}, `SELECT language, coalesce(sum(marker), 0), coalesce(sum(NOT marker AND probability >= 0.5), 0), coalesce(sum(NOT marker AND probability < 0.5), 0)
			FROM audio WHERE language IN (`+placeholders(len(args))+`) GROUP BY language`, args...); err != nil {
			return Stats{}, err
		}
		var err error
		if st.Pending, err = s.pendingCount(ctx); err != nil {
			return Stats{}, err
		}
	}
	if err := s.each(ctx, func(rows *sql.Rows) error {
		var reason string
		var n int64
		if err := rows.Scan(&reason, &n); err != nil {
			return err
		}
		st.Rejected[reason] = n
		return nil
	}, "SELECT reason, count(*) FROM rejected GROUP BY reason"); err != nil {
		return Stats{}, err
	}
	const day = 24 * 60 * 60
	at := now.Unix()
	if err := s.each(ctx, func(rows *sql.Rows) error {
		var d int
		var n int64
		if err := rows.Scan(&d, &n); err != nil {
			return err
		}
		st.Days[d] += n
		return nil
	}, "SELECT max(0, (? - first_seen) / ?), count(*) FROM torrents WHERE first_seen > ? GROUP BY 1", at, day, at-int64(days)*day); err != nil {
		return Stats{}, err
	}
	return st, nil
}

// each calls f on every row of the query.
func (s *Store) each(ctx context.Context, f func(*sql.Rows) error, query string, args ...any) error {
	rows, err := s.r.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := f(rows); err != nil {
			return err
		}
	}
	return errors.Join(rows.Err(), rows.Close())
}
