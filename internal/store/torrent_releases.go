package store

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"maps"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/mikluko/sniffarr/internal/names"
	"github.com/mikluko/sniffarr/internal/rules"
)

// Layers name what decided a torrent's kind or audio probability.
const (
	LayerRules = "rules"
	LayerReuse = "reuse"
	LayerJev   = "jev"
)

// Torrent is a torrent to store.
type Torrent struct {
	Hash      Hash
	Name      string
	Size      int64
	Files     []rules.File
	Discovery string
	SeenAt    time.Time
	// Kind, when set, was decided by KindLayer.
	Kind      string
	KindLayer string
	TitleKey  string
	// WorkKey, Year and Season are what names.Parse gives; a torrent without
	// a work key is in no top.
	WorkKey string
	Year    int
	Season  int
	// Audio holds one entry per language, at most.
	Audio []Audio
}

// Release is a stored torrent with its classification.
type Release struct {
	ID               int64
	Hash             Hash
	Name             string
	Size             int64
	FileCount        int64
	Kind             string
	KindLayer        string
	KindConfidence   float64
	Source           string
	SourceConfidence float64
	// Audio holds the entries of Options.Languages, in tag order.
	Audio    []Audio
	TitleKey string
	// Title is the match of TitleKey when Options.TMDB is set.
	Title   Title
	WorkKey string
	Year    int
	Season  int
	// PeersWeek is the torrent's distinct peers over the last 7 days, as
	// ComputeTop last counted them.
	PeersWeek int64
	// Classified reports whether the classifier has examined the torrent.
	Classified bool
	FirstSeen  time.Time
	LastSeen   time.Time
}

// Pending is a torrent awaiting classification.
type Pending struct {
	ID    int64
	Name  string
	Size  int64
	Files []rules.File
	// KindKnown reports whether the torrent's kind is decided already.
	KindKnown bool
}

// Classification is the classifier's verdict on a torrent. An empty Kind
// leaves the torrent's kind as it is.
type Classification struct {
	Kind             string
	KindConfidence   float64
	Source           string
	SourceConfidence float64
	// Audio maps languages to the probability of an audio track in each; a
	// probability decided by rules stays.
	Audio map[string]float64
}

// fillWorkKeys derives work_key, year and season from the names of the
// torrents that have a title key and no work key, in batches; a name whose
// work key is empty takes its title key.
func (s *Store) fillWorkKeys(ctx context.Context) error {
	for {
		rows, err := s.r.QueryContext(ctx, "SELECT id, name, title_key FROM torrent_releases WHERE work_key IS NULL AND title_key IS NOT NULL LIMIT 1000")
		if err != nil {
			return err
		}
		type row struct {
			id   int64
			p    names.Parsed
			work string
		}
		var batch []row
		for rows.Next() {
			var r row
			var name string
			if err := rows.Scan(&r.id, &name, &r.work); err != nil {
				rows.Close()
				return err
			}
			r.p = names.Parse(name)
			if r.p.Work != "" {
				r.work = r.p.Work
			}
			batch = append(batch, r)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		if len(batch) == 0 {
			return nil
		}
		if err := s.tx(ctx, func(tx *sql.Tx) error {
			for _, r := range batch {
				if _, err := tx.ExecContext(ctx, "UPDATE torrent_releases SET work_key = ?, year = ?, season = ? WHERE id = ?", r.work, nullInt(r.p.Year), nullInt(r.p.Season), r.id); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return err
		}
	}
}

// largest returns the at most n largest files, largest first.
func largest(files []rules.File, n int) []rules.File {
	files = slices.Clone(files)
	slices.SortStableFunc(files, func(a, b rules.File) int { return cmp.Compare(b.Size, a.Size) })
	return files[:min(n, len(files))]
}

// Known returns which of the hashes are stored and which are rejected, and
// moves the last sighting of the stored ones to seen.
func (s *Store) Known(ctx context.Context, hashes []Hash, seen time.Time) (stored, rejected map[Hash]bool, err error) {
	stored, rejected = make(map[Hash]bool, len(hashes)), map[Hash]bool{}
	err = s.tx(ctx, func(tx *sql.Tx) error {
		for c := range slices.Chunk(hashes, 500) {
			args := make([]any, len(c))
			for i, h := range c {
				args[i] = h[:]
			}
			in := placeholders(len(c))
			if _, err := tx.ExecContext(ctx, "UPDATE torrent_releases SET last_seen = max(last_seen, ?) WHERE infohash IN ("+in+")",
				append([]any{seen.Unix()}, args...)...); err != nil {
				return err
			}
			rows, err := tx.QueryContext(ctx, "SELECT infohash, 1 FROM torrent_releases WHERE infohash IN ("+in+") UNION ALL SELECT infohash, 0 FROM ingest_rejected WHERE infohash IN ("+in+")",
				append(args, args...)...)
			if err != nil {
				return err
			}
			for rows.Next() {
				var b []byte
				var isStored bool
				if err := rows.Scan(&b, &isStored); err != nil {
					rows.Close()
					return err
				}
				if isStored {
					stored[Hash(b)] = true
				} else {
					rejected[Hash(b)] = true
				}
			}
			if err := rows.Close(); err != nil {
				return err
			}
		}
		return nil
	})
	return stored, rejected, err
}

// Insert stores a torrent for classification, with its MaxFiles largest
// files, and copies its kind, if it has one, to the stored torrents of the
// same title key whose kind is unknown; it returns whether it stored the
// torrent and how many it copied the kind to. It stores nothing when the hash
// is already stored or rejected.
func (s *Store) Insert(ctx context.Context, t Torrent) (inserted bool, copied int, err error) {
	err = s.tx(ctx, func(tx *sql.Tx) error {
		var rejected bool
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM ingest_rejected WHERE infohash = ?)", t.Hash[:]).Scan(&rejected); err != nil || rejected {
			return err
		}
		var id int64
		err := tx.QueryRowContext(ctx, `INSERT INTO torrent_releases (infohash, name, size, file_count, discovery, first_seen, last_seen,
				kind, kind_layer, title_key, work_key, year, season)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT (infohash) DO NOTHING RETURNING id`,
			t.Hash[:], t.Name, t.Size, len(t.Files), t.Discovery, t.SeenAt.Unix(), t.SeenAt.Unix(),
			null(t.Kind), null(t.KindLayer), null(t.TitleKey), null(t.WorkKey), nullInt(t.Year), nullInt(t.Season)).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		inserted = true
		files := largest(t.Files, s.opts.MaxFiles)
		paths := make([]string, len(files))
		for i, f := range files {
			paths[i] = f.Path
			if _, err := tx.ExecContext(ctx, "INSERT INTO torrent_files (torrent_id, idx, path, size) VALUES (?, ?, ?, ?)", id, i, f.Path, f.Size); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO torrent_releases_fts (rowid, name, paths) VALUES (?, ?, ?)", id, t.Name, strings.Join(paths, "\n")); err != nil {
			return err
		}
		for _, a := range t.Audio {
			if _, err := tx.ExecContext(ctx, "INSERT INTO torrent_audio (torrent_id, language, marker, script_hint, probability, layer) VALUES (?, ?, ?, ?, ?, ?)",
				id, a.Language, a.Marker, a.ScriptHint, a.Probability, null(a.Layer)); err != nil {
				return err
			}
		}
		if t.Kind == "" || t.TitleKey == "" {
			return nil
		}
		res, err := tx.ExecContext(ctx, "UPDATE torrent_releases SET kind = ?, kind_layer = ? WHERE title_key = ? AND kind IS NULL", t.Kind, LayerReuse, t.TitleKey)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		copied = int(n)
		return err
	})
	return inserted, copied, err
}

// Query selects releases for Search.
type Query struct {
	// Text matches releases whose name or file paths contain all its words.
	Text string
	// Kinds selects releases of these kinds; "" selects unclassified ones.
	// Empty selects any kind.
	Kinds []string
	// Exclude drops releases of these kinds.
	Exclude []string
	// MinAudioProbability, when set, drops releases whose probability of an
	// audio track in AudioLanguage is lower or unknown.
	MinAudioProbability *float64
	AudioLanguage       string
	// Unexamined admits, past the Kinds and MinAudioProbability filters, the
	// releases the classifier has not examined whose kind or audio
	// probability is unknown.
	Unexamined bool
	// ID, when set, selects releases whose title key is matched to it.
	ID    *ExternalID
	Limit int
}

const releaseColumns = `t.id, t.infohash, t.name, t.size, t.file_count, coalesce(t.kind, ''), coalesce(t.kind_layer, ''),
	coalesce(t.kind_confidence, 0), coalesce(t.source, ''), coalesce(t.source_confidence, 0),
	t.classified_at IS NOT NULL, t.first_seen, t.last_seen, coalesce(t.title_key, ''),
	coalesce(t.work_key, ''), coalesce(t.year, 0), coalesce(t.season, 0), t.peers_week`

// Search returns releases matching q, best text match first, or most
// recently seen first when q has no text.
func (s *Store) Search(ctx context.Context, q Query) ([]Release, error) {
	var where []string
	var args []any
	from := "torrent_releases t"
	order := "t.last_seen DESC"
	if m := matchExpr(q.Text); m != "" {
		from = "torrent_releases_fts f JOIN torrent_releases t ON t.id = f.rowid"
		where = append(where, "torrent_releases_fts MATCH ?")
		args = append(args, m)
		order = "f.rank"
	}
	if len(q.Kinds) > 0 {
		var or []string
		var kinds []any
		for _, k := range q.Kinds {
			if k == "" {
				or = append(or, "t.kind IS NULL")
			} else {
				kinds = append(kinds, k)
			}
		}
		if len(kinds) > 0 {
			or = append(or, "t.kind IN ("+placeholders(len(kinds))+")")
			args = append(args, kinds...)
		}
		if q.Unexamined {
			or = append(or, "(t.kind IS NULL AND t.classified_at IS NULL)")
		}
		where = append(where, "("+strings.Join(or, " OR ")+")")
	}
	if len(q.Exclude) > 0 {
		where = append(where, "coalesce(t.kind, '') NOT IN ("+placeholders(len(q.Exclude))+")")
		for _, k := range q.Exclude {
			args = append(args, k)
		}
	}
	if q.MinAudioProbability != nil {
		const probability = "(SELECT a.probability FROM torrent_audio a WHERE a.torrent_id = t.id AND a.language = ?)"
		cond := probability + " >= ?"
		args = append(args, q.AudioLanguage, *q.MinAudioProbability)
		if q.Unexamined {
			cond = "(" + cond + " OR t.classified_at IS NULL AND " + probability + " IS NULL)"
			args = append(args, q.AudioLanguage)
		}
		where = append(where, cond)
	}
	if q.ID != nil {
		where = append(where, "t.title_key IN (SELECT i.title_key FROM tmdb_ids i WHERE i.provider = ? AND i.id = ?)")
		args = append(args, q.ID.Provider, q.ID.ID)
	}
	stmt := "SELECT " + releaseColumns + " FROM " + from
	if len(where) > 0 {
		stmt += " WHERE " + strings.Join(where, " AND ")
	}
	stmt += " ORDER BY " + order + " LIMIT ?"
	args = append(args, q.Limit)
	rows, err := s.r.QueryContext(ctx, stmt, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Release
	for rows.Next() {
		r, err := scanRelease(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	if err := s.audio(ctx, out); err != nil {
		return nil, err
	}
	return out, s.tmdb(ctx, out)
}

// Get returns the stored release with the hash, or ErrNotFound.
func (s *Store) Get(ctx context.Context, h Hash) (Release, error) {
	r, err := scanRelease(s.r.QueryRowContext(ctx, "SELECT "+releaseColumns+" FROM torrent_releases t WHERE t.infohash = ?", h[:]))
	if errors.Is(err, sql.ErrNoRows) {
		return Release{}, ErrNotFound
	}
	if err != nil {
		return Release{}, err
	}
	rs := []Release{r}
	if err := errors.Join(s.audio(ctx, rs), s.tmdb(ctx, rs)); err != nil {
		return Release{}, err
	}
	return rs[0], nil
}

func scanRelease(row interface{ Scan(...any) error }) (Release, error) {
	var r Release
	var hash []byte
	var first, last int64
	err := row.Scan(&r.ID, &hash, &r.Name, &r.Size, &r.FileCount, &r.Kind, &r.KindLayer, &r.KindConfidence, &r.Source, &r.SourceConfidence,
		&r.Classified, &first, &last, &r.TitleKey, &r.WorkKey, &r.Year, &r.Season, &r.PeersWeek)
	if err != nil {
		return Release{}, err
	}
	r.Hash = Hash(hash)
	r.FirstSeen = time.Unix(first, 0).UTC()
	r.LastSeen = time.Unix(last, 0).UTC()
	return r, nil
}

// Unclassified returns up to limit torrents with an ID above after, in ID
// order, that the classifier has not examined, whose kind is unknown, and
// whose names have a marker or script hint of one of Options.Languages; each
// with at most maxFiles files.
func (s *Store) Unclassified(ctx context.Context, after int64, limit, maxFiles int) ([]Pending, error) {
	if len(s.opts.Languages) == 0 {
		return nil, nil
	}
	args := make([]any, 0, len(s.opts.Languages)+2)
	for _, l := range s.opts.Languages {
		args = append(args, l)
	}
	return s.pending(ctx, maxFiles, `SELECT t.id, t.name, t.size, t.kind IS NOT NULL FROM torrent_releases t
		WHERE t.classified_at IS NULL AND t.kind IS NULL AND EXISTS (SELECT 1 FROM torrent_audio a WHERE a.torrent_id = t.id
			AND a.language IN (`+placeholders(len(s.opts.Languages))+`) AND (a.marker OR a.script_hint))
		AND t.id > ? ORDER BY t.id LIMIT ?`, append(args, after, limit)...)
}

// Pending returns the torrents with the IDs that the classifier has not
// examined, each with at most maxFiles files.
func (s *Store) Pending(ctx context.Context, ids []int64, maxFiles int) ([]Pending, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	return s.pending(ctx, maxFiles, "SELECT id, name, size, kind IS NOT NULL FROM torrent_releases WHERE classified_at IS NULL AND id IN ("+placeholders(len(ids))+")", args...)
}

func (s *Store) pending(ctx context.Context, maxFiles int, query string, args ...any) ([]Pending, error) {
	rows, err := s.r.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	var out []Pending
	for rows.Next() {
		var p Pending
		if err := rows.Scan(&p.ID, &p.Name, &p.Size, &p.KindKnown); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, p)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].Files, err = s.files(ctx, out[i].ID, maxFiles); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// KindByKey returns the kind of a stored torrent with the title key whose
// kind is decided, or "" when there is none.
func (s *Store) KindByKey(ctx context.Context, key string) (string, error) {
	var kind string
	err := s.r.QueryRowContext(ctx, "SELECT kind FROM torrent_releases WHERE title_key = ? AND kind IS NOT NULL LIMIT 1", key).Scan(&kind)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return kind, err
}

// Classify records the classification of the torrent with the ID.
// A Kind is recorded only for a torrent whose kind is unknown, and is copied
// to the torrents of the same title key whose kind is unknown; Classify
// returns how many it copied it to.
func (s *Store) Classify(ctx context.Context, id int64, c Classification, at time.Time) (int, error) {
	var copied int
	err := s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE torrent_releases SET classified_at = ?, classify_error = NULL,
			source = ?, source_confidence = ? WHERE id = ?`,
			at.Unix(), c.Source, c.SourceConfidence, id); err != nil {
			return err
		}
		for _, lang := range slices.Sorted(maps.Keys(c.Audio)) {
			if _, err := tx.ExecContext(ctx, `INSERT INTO torrent_audio (torrent_id, language, marker, script_hint, probability, layer)
				VALUES (?, ?, 0, 0, ?, ?) ON CONFLICT (torrent_id, language) DO UPDATE SET
				probability = excluded.probability, layer = excluded.layer WHERE torrent_audio.layer IS NOT ?`,
				id, lang, c.Audio[lang], LayerJev, LayerRules); err != nil {
				return err
			}
		}
		if c.Kind == "" {
			return nil
		}
		var key sql.NullString
		err := tx.QueryRowContext(ctx, `UPDATE torrent_releases SET kind = ?, kind_layer = ?, kind_confidence = ? WHERE id = ? AND kind IS NULL
			RETURNING title_key`, c.Kind, LayerJev, c.KindConfidence, id).Scan(&key)
		if errors.Is(err, sql.ErrNoRows) || err == nil && !key.Valid {
			return nil
		}
		if err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, "UPDATE torrent_releases SET kind = ?, kind_layer = ? WHERE title_key = ? AND kind IS NULL", c.Kind, LayerReuse, key.String)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		copied = int(n)
		return err
	})
	return copied, err
}

// ClassifyFailed marks the torrent with the ID as one the classifier cannot
// handle, so Unclassified skips it.
func (s *Store) ClassifyFailed(ctx context.Context, id int64, reason string, at time.Time) error {
	_, err := s.w.ExecContext(ctx, "UPDATE torrent_releases SET classified_at = ?, classify_error = ? WHERE id = ?", at.Unix(), reason, id)
	return err
}

// matchExpr turns free text into an FTS5 query matching all its words, or ""
// when the text has none.
func matchExpr(text string) string {
	words := strings.FieldsFunc(text, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) })
	for i, w := range words {
		words[i] = `"` + w + `"`
	}
	return strings.Join(words, " ")
}

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
	from := "torrent_releases t"
	order := "t.last_seen DESC, t.id DESC"
	if m := matchExpr(b.Text); m != "" {
		from = "torrent_releases_fts f JOIN torrent_releases t ON t.id = f.rowid"
		where = append(where, "torrent_releases_fts MATCH ?")
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
		where = append(where, "EXISTS (SELECT 1 FROM torrent_audio a WHERE a.torrent_id = t.id AND a.language = ? AND a.marker)")
		args = append(args, b.AudioLanguage)
	}
	if b.MinAudioProbability != nil {
		where = append(where, "EXISTS (SELECT 1 FROM torrent_audio a WHERE a.torrent_id = t.id AND a.language = ? AND a.probability >= ?)")
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
	return out, total, errors.Join(s.audio(ctx, out), s.tmdb(ctx, out))
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
	if err := s.r.QueryRowContext(ctx, "SELECT discovery, (SELECT count(*) FROM torrent_files WHERE torrent_id = t.id) FROM torrent_releases t WHERE t.id = ?", r.ID).
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
		FROM torrent_releases WHERE work_key = ?`, r.PeersWeek, r.PeersWeek, r.ID, r.WorkKey).Scan(&c.Releases, &c.ReleaseRank); err != nil {
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
	}, "SELECT coalesce(kind, ''), coalesce(kind_layer, ''), coalesce(source, ''), count(*) FROM torrent_releases GROUP BY 1, 2, 3"); err != nil {
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
			FROM torrent_audio WHERE language IN (`+placeholders(len(args))+`) GROUP BY language`, args...); err != nil {
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
	}, "SELECT reason, count(*) FROM ingest_rejected GROUP BY reason"); err != nil {
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
	}, "SELECT max(0, (? - first_seen) / ?), count(*) FROM torrent_releases WHERE first_seen > ? GROUP BY 1", at, day, at-int64(days)*day); err != nil {
		return Stats{}, err
	}
	return st, nil
}

// scanReleasePeers scans releaseColumns followed by a peer count.
func scanReleasePeers(rows *sql.Rows) (Release, int64, error) {
	var r Release
	var hash []byte
	var first, last, peers int64
	err := rows.Scan(&r.ID, &hash, &r.Name, &r.Size, &r.FileCount, &r.Kind, &r.KindLayer, &r.KindConfidence, &r.Source, &r.SourceConfidence,
		&r.Classified, &first, &last, &r.TitleKey, &r.WorkKey, &r.Year, &r.Season, &r.PeersWeek, &peers)
	if err != nil {
		return Release{}, 0, err
	}
	r.Hash = Hash(hash)
	r.FirstSeen = time.Unix(first, 0).UTC()
	r.LastSeen = time.Unix(last, 0).UTC()
	return r, peers, nil
}

// pendingCount counts the torrents Unclassified selects.
func (s *Store) pendingCount(ctx context.Context) (int64, error) {
	if len(s.opts.Languages) == 0 {
		return 0, nil
	}
	args := make([]any, len(s.opts.Languages))
	for i, l := range s.opts.Languages {
		args[i] = l
	}
	var n int64
	err := s.r.QueryRowContext(ctx, `SELECT count(*) FROM torrent_releases t
		WHERE t.classified_at IS NULL AND t.kind IS NULL AND EXISTS (SELECT 1 FROM torrent_audio a WHERE a.torrent_id = t.id
			AND a.language IN (`+placeholders(len(args))+`) AND (a.marker OR a.script_hint))`, args...).Scan(&n)
	return n, err
}
