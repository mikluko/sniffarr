// Package store keeps torrents, rejected infohashes and classifications,
// with full-text search over names and file paths.
package store

import (
	"cmp"
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"
	"time"
	"unicode"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"

	"github.com/mikluko/sniffarr/internal/database"
	"github.com/mikluko/sniffarr/internal/rules"
)

// ErrNotFound is returned for an infohash that is not stored.
var ErrNotFound = errors.New("not found")

// Hash is a BitTorrent v1 infohash.
type Hash [20]byte

// Layers name what decided a torrent's kind.
const (
	LayerRules = "rules"
	LayerReuse = "reuse"
	LayerJev   = "jev"
)

// Torrent is a torrent to store.
type Torrent struct {
	Hash          Hash
	Name          string
	Size          int64
	Files         []rules.File
	Discovery     string
	SeenAt        time.Time
	RussianMarker bool
	// RussianHint marks a torrent whose names are Cyrillic or carry a
	// Russian marker.
	RussianHint bool
	// Kind, when set, was decided by KindLayer.
	Kind      string
	KindLayer string
	TitleKey  string
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
	// RussianAudio is 1 for a torrent with an explicit Russian marker, else
	// the classifier's probability, else nil.
	RussianAudio *float64
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
	RussianAudio     float64
}

// DefaultMaxFiles is the default of Options.MaxFiles.
const DefaultMaxFiles = 20

// Options tune a Store. Zero fields take the default named in their doc.
type Options struct {
	// MaxFiles is how many files of a torrent are stored, the largest ones;
	// DefaultMaxFiles. The file count stays true.
	MaxFiles int
	// Meter records the store's metrics; the global MeterProvider when nil.
	Meter metric.MeterProvider
}

// Store keeps its tables in a shared database. It is safe for concurrent
// use.
type Store struct {
	db      *database.DB
	w, r    *sql.DB
	opts    Options
	deleted metric.Int64Counter
}

//go:embed migrations/*.sql
var migrations embed.FS

// New returns the Store in db, applying any pending migrations.
func New(ctx context.Context, db *database.DB, opts Options) (*Store, error) {
	if opts.MaxFiles <= 0 {
		opts.MaxFiles = DefaultMaxFiles
	}
	if opts.Meter == nil {
		opts.Meter = otel.GetMeterProvider()
	}
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return nil, err
	}
	if err := db.Migrate(ctx, "store", sub); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	deleted, err := opts.Meter.Meter("github.com/mikluko/sniffarr/internal/store").Int64Counter("sniffarr.store.retention.deletions",
		metric.WithUnit("{torrent}"), metric.WithDescription("Stored torrents deleted for not being seen within the retention period."))
	if err != nil {
		return nil, err
	}
	return &Store{db: db, w: db.W, r: db.R, opts: opts, deleted: deleted}, nil
}

// Sweep deletes, with their files and search entries, the torrents last seen
// before before, in transactions of at most batch torrents each, and returns
// how many it deleted.
func (s *Store) Sweep(ctx context.Context, before time.Time, batch int) (int, error) {
	total := 0
	for {
		var n int
		err := s.tx(ctx, func(tx *sql.Tx) error {
			const ids = "SELECT id FROM torrents WHERE last_seen < ? ORDER BY last_seen LIMIT ?"
			if _, err := tx.ExecContext(ctx, "DELETE FROM torrents_fts WHERE rowid IN ("+ids+")", before.Unix(), batch); err != nil {
				return err
			}
			res, err := tx.ExecContext(ctx, "DELETE FROM torrents WHERE id IN ("+ids+")", before.Unix(), batch)
			if err != nil {
				return err
			}
			d, err := res.RowsAffected()
			n = int(d)
			return err
		})
		if err != nil {
			return total, err
		}
		total += n
		s.deleted.Add(ctx, int64(n))
		if n < batch {
			return total, nil
		}
	}
}

// largest returns the at most n largest files, largest first.
func largest(files []rules.File, n int) []rules.File {
	files = slices.Clone(files)
	slices.SortStableFunc(files, func(a, b rules.File) int { return cmp.Compare(b.Size, a.Size) })
	return files[:min(n, len(files))]
}

func (s *Store) tx(ctx context.Context, f func(*sql.Tx) error) error {
	return s.db.Tx(ctx, f)
}

// Known returns which of the hashes are stored or rejected, and moves the
// last sighting of the stored ones to seen.
func (s *Store) Known(ctx context.Context, hashes []Hash, seen time.Time) (map[Hash]bool, error) {
	known := make(map[Hash]bool, len(hashes))
	err := s.tx(ctx, func(tx *sql.Tx) error {
		for c := range slices.Chunk(hashes, 500) {
			args := make([]any, len(c))
			for i, h := range c {
				args[i] = h[:]
			}
			in := placeholders(len(c))
			if _, err := tx.ExecContext(ctx, "UPDATE torrents SET last_seen = max(last_seen, ?) WHERE infohash IN ("+in+")",
				append([]any{seen.Unix()}, args...)...); err != nil {
				return err
			}
			rows, err := tx.QueryContext(ctx, "SELECT infohash FROM torrents WHERE infohash IN ("+in+") UNION ALL SELECT infohash FROM rejected WHERE infohash IN ("+in+")",
				append(args, args...)...)
			if err != nil {
				return err
			}
			for rows.Next() {
				var b []byte
				if err := rows.Scan(&b); err != nil {
					rows.Close()
					return err
				}
				known[Hash(b)] = true
			}
			if err := rows.Close(); err != nil {
				return err
			}
		}
		return nil
	})
	return known, err
}

// Insert stores a torrent for classification, with its MaxFiles largest
// files, and copies its kind, if it has one, to the stored torrents of the
// same title key whose kind is unknown; it returns whether it stored the
// torrent and how many it copied the kind to. It stores nothing when the hash
// is already stored or rejected.
func (s *Store) Insert(ctx context.Context, t Torrent) (inserted bool, copied int, err error) {
	err = s.tx(ctx, func(tx *sql.Tx) error {
		var rejected bool
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM rejected WHERE infohash = ?)", t.Hash[:]).Scan(&rejected); err != nil || rejected {
			return err
		}
		var id int64
		err := tx.QueryRowContext(ctx, `INSERT INTO torrents (infohash, name, size, file_count, discovery, first_seen, last_seen,
				russian_marker, russian_hint, kind, kind_layer, title_key)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT (infohash) DO NOTHING RETURNING id`,
			t.Hash[:], t.Name, t.Size, len(t.Files), t.Discovery, t.SeenAt.Unix(), t.SeenAt.Unix(),
			t.RussianMarker, t.RussianHint, null(t.Kind), null(t.KindLayer), null(t.TitleKey)).Scan(&id)
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
			if _, err := tx.ExecContext(ctx, "INSERT INTO files (torrent_id, idx, path, size) VALUES (?, ?, ?, ?)", id, i, f.Path, f.Size); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO torrents_fts (rowid, name, paths) VALUES (?, ?, ?)", id, t.Name, strings.Join(paths, "\n")); err != nil {
			return err
		}
		if t.Kind == "" || t.TitleKey == "" {
			return nil
		}
		res, err := tx.ExecContext(ctx, "UPDATE torrents SET kind = ?, kind_layer = ? WHERE title_key = ? AND kind IS NULL", t.Kind, LayerReuse, t.TitleKey)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		copied = int(n)
		return err
	})
	return inserted, copied, err
}

// Reject remembers a hash as rejected for reason. It reports false when the
// hash is already stored or rejected.
func (s *Store) Reject(ctx context.Context, h Hash, reason rules.Reason, at time.Time) (bool, error) {
	var rejected bool
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var stored bool
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM torrents WHERE infohash = ?)", h[:]).Scan(&stored); err != nil || stored {
			return err
		}
		res, err := tx.ExecContext(ctx, "INSERT INTO rejected (infohash, reason, rejected_at) VALUES (?, ?, ?) ON CONFLICT DO NOTHING", h[:], string(reason), at.Unix())
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		rejected = n > 0
		return err
	})
	return rejected, err
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
	// MinRussianAudio, when set, drops releases with a lower or absent
	// RussianAudio.
	MinRussianAudio *float64
	// Unexamined admits, past the Kinds and MinRussianAudio filters, the
	// releases the classifier has not examined whose kind or RussianAudio is
	// unknown.
	Unexamined bool
	Limit      int
}

const releaseColumns = `t.id, t.infohash, t.name, t.size, t.file_count, coalesce(t.kind, ''), coalesce(t.kind_layer, ''),
	coalesce(t.kind_confidence, 0), coalesce(t.source, ''), coalesce(t.source_confidence, 0),
	CASE WHEN t.russian_marker THEN 1.0 ELSE t.russian_audio END, t.classified_at IS NOT NULL, t.first_seen, t.last_seen`

// Search returns releases matching q, best text match first, or most
// recently seen first when q has no text.
func (s *Store) Search(ctx context.Context, q Query) ([]Release, error) {
	var where []string
	var args []any
	from := "torrents t"
	order := "t.last_seen DESC"
	if m := matchExpr(q.Text); m != "" {
		from = "torrents_fts f JOIN torrents t ON t.id = f.rowid"
		where = append(where, "torrents_fts MATCH ?")
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
	if q.MinRussianAudio != nil {
		cond := "CASE WHEN t.russian_marker THEN 1.0 ELSE t.russian_audio END >= ?"
		if q.Unexamined {
			cond = "(" + cond + " OR t.classified_at IS NULL AND NOT t.russian_marker)"
		}
		where = append(where, cond)
		args = append(args, *q.MinRussianAudio)
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
	return out, rows.Err()
}

// Get returns the stored release with the hash, or ErrNotFound.
func (s *Store) Get(ctx context.Context, h Hash) (Release, error) {
	r, err := scanRelease(s.r.QueryRowContext(ctx, "SELECT "+releaseColumns+" FROM torrents t WHERE t.infohash = ?", h[:]))
	if errors.Is(err, sql.ErrNoRows) {
		return Release{}, ErrNotFound
	}
	return r, err
}

func scanRelease(row interface{ Scan(...any) error }) (Release, error) {
	var r Release
	var hash []byte
	var russian sql.NullFloat64
	var first, last int64
	err := row.Scan(&r.ID, &hash, &r.Name, &r.Size, &r.FileCount, &r.Kind, &r.KindLayer, &r.KindConfidence, &r.Source, &r.SourceConfidence,
		&russian, &r.Classified, &first, &last)
	if err != nil {
		return Release{}, err
	}
	r.Hash = Hash(hash)
	if russian.Valid {
		r.RussianAudio = &russian.Float64
	}
	r.FirstSeen = time.Unix(first, 0).UTC()
	r.LastSeen = time.Unix(last, 0).UTC()
	return r, nil
}

// Unclassified returns up to limit torrents with an ID above after, in ID
// order, that the classifier has not examined, whose kind is unknown, and
// whose names hint at Russian audio; each with at most maxFiles files.
func (s *Store) Unclassified(ctx context.Context, after int64, limit, maxFiles int) ([]Pending, error) {
	return s.pending(ctx, maxFiles, `SELECT id, name, size, kind IS NOT NULL FROM torrents
		WHERE classified_at IS NULL AND kind IS NULL AND russian_hint = 1 AND id > ? ORDER BY id LIMIT ?`, after, limit)
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
	return s.pending(ctx, maxFiles, "SELECT id, name, size, kind IS NOT NULL FROM torrents WHERE classified_at IS NULL AND id IN ("+placeholders(len(ids))+")", args...)
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
	err := s.r.QueryRowContext(ctx, "SELECT kind FROM torrents WHERE title_key = ? AND kind IS NOT NULL LIMIT 1", key).Scan(&kind)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return kind, err
}

func null(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (s *Store) files(ctx context.Context, id int64, limit int) ([]rules.File, error) {
	rows, err := s.r.QueryContext(ctx, "SELECT path, size FROM files WHERE torrent_id = ? ORDER BY idx LIMIT ?", id, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []rules.File
	for rows.Next() {
		var f rules.File
		if err := rows.Scan(&f.Path, &f.Size); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// Classify records the classification of the torrent with the ID.
// A Kind is recorded only for a torrent whose kind is unknown, and is copied
// to the torrents of the same title key whose kind is unknown; Classify
// returns how many it copied it to.
func (s *Store) Classify(ctx context.Context, id int64, c Classification, at time.Time) (int, error) {
	var copied int
	err := s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE torrents SET classified_at = ?, classify_error = NULL,
			source = ?, source_confidence = ?, russian_audio = ? WHERE id = ?`,
			at.Unix(), c.Source, c.SourceConfidence, c.RussianAudio, id); err != nil {
			return err
		}
		if c.Kind == "" {
			return nil
		}
		var key sql.NullString
		err := tx.QueryRowContext(ctx, `UPDATE torrents SET kind = ?, kind_layer = ?, kind_confidence = ? WHERE id = ? AND kind IS NULL
			RETURNING title_key`, c.Kind, LayerJev, c.KindConfidence, id).Scan(&key)
		if errors.Is(err, sql.ErrNoRows) || err == nil && !key.Valid {
			return nil
		}
		if err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, "UPDATE torrents SET kind = ?, kind_layer = ? WHERE title_key = ? AND kind IS NULL", c.Kind, LayerReuse, key.String)
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
	_, err := s.w.ExecContext(ctx, "UPDATE torrents SET classified_at = ?, classify_error = ? WHERE id = ?", at.Unix(), reason, id)
	return err
}

// TokensUsed returns the classifier tokens recorded for the day.
func (s *Store) TokensUsed(ctx context.Context, day string) (int64, error) {
	var n int64
	err := s.r.QueryRowContext(ctx, "SELECT tokens FROM token_usage WHERE day = ?", day).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return n, err
}

// AddTokens adds n to the classifier tokens recorded for the day.
func (s *Store) AddTokens(ctx context.Context, day string, n int64) error {
	_, err := s.w.ExecContext(ctx, "INSERT INTO token_usage (day, tokens) VALUES (?, ?) ON CONFLICT (day) DO UPDATE SET tokens = tokens + excluded.tokens", day, n)
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

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}
