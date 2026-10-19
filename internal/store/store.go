// Package store keeps torrents, rejected infohashes and classifications,
// with full-text search over names and file paths.
package store

import (
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

	"github.com/mikluko/sniffarr/internal/database"
	"github.com/mikluko/sniffarr/internal/rules"
)

// ErrNotFound is returned for an infohash that is not stored.
var ErrNotFound = errors.New("not found")

// Hash is a BitTorrent v1 infohash.
type Hash [20]byte

// Torrent is a torrent to store.
type Torrent struct {
	Hash          Hash
	Name          string
	Size          int64
	Files         []rules.File
	Discovery     string
	SeenAt        time.Time
	RussianMarker bool
}

// Release is a stored torrent with its classification.
type Release struct {
	Hash             Hash
	Name             string
	Size             int64
	FileCount        int64
	Kind             string
	KindConfidence   float64
	Source           string
	SourceConfidence float64
	// RussianAudio is 1 for a torrent with an explicit Russian marker, else
	// the classifier's probability, else nil.
	RussianAudio *float64
	FirstSeen    time.Time
	LastSeen     time.Time
}

// Pending is a torrent awaiting classification.
type Pending struct {
	ID    int64
	Name  string
	Size  int64
	Files []rules.File
}

// Classification is the classifier's verdict on a torrent.
type Classification struct {
	Kind             string
	KindConfidence   float64
	Source           string
	SourceConfidence float64
	RussianAudio     float64
}

// Store keeps its tables in a shared database. It is safe for concurrent
// use.
type Store struct {
	db   *database.DB
	w, r *sql.DB
}

//go:embed migrations/*.sql
var migrations embed.FS

// New returns the Store in db, applying any pending migrations.
func New(ctx context.Context, db *database.DB) (*Store, error) {
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return nil, err
	}
	if err := db.Migrate(ctx, "store", sub); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return &Store{db: db, w: db.W, r: db.R}, nil
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

// Insert stores a torrent for classification. It reports false, and stores
// nothing, when the hash is already stored or rejected.
func (s *Store) Insert(ctx context.Context, t Torrent) (bool, error) {
	var inserted bool
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var rejected bool
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM rejected WHERE infohash = ?)", t.Hash[:]).Scan(&rejected); err != nil || rejected {
			return err
		}
		var id int64
		err := tx.QueryRowContext(ctx, `INSERT INTO torrents (infohash, name, size, file_count, discovery, first_seen, last_seen, russian_marker)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT (infohash) DO NOTHING RETURNING id`,
			t.Hash[:], t.Name, t.Size, len(t.Files), t.Discovery, t.SeenAt.Unix(), t.SeenAt.Unix(), t.RussianMarker).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		inserted = true
		paths := make([]string, len(t.Files))
		for i, f := range t.Files {
			paths[i] = f.Path
			if _, err := tx.ExecContext(ctx, "INSERT INTO files (torrent_id, idx, path, size) VALUES (?, ?, ?, ?)", id, i, f.Path, f.Size); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO torrents_fts (rowid, name, paths) VALUES (?, ?, ?)", id, t.Name, strings.Join(paths, "\n"))
		return err
	})
	return inserted, err
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
	Limit           int
}

const releaseColumns = `t.infohash, t.name, t.size, t.file_count, coalesce(t.kind, ''), coalesce(t.kind_confidence, 0),
	coalesce(t.source, ''), coalesce(t.source_confidence, 0),
	CASE WHEN t.russian_marker THEN 1.0 ELSE t.russian_audio END, t.first_seen, t.last_seen`

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
		where = append(where, "("+strings.Join(or, " OR ")+")")
	}
	if len(q.Exclude) > 0 {
		where = append(where, "coalesce(t.kind, '') NOT IN ("+placeholders(len(q.Exclude))+")")
		for _, k := range q.Exclude {
			args = append(args, k)
		}
	}
	if q.MinRussianAudio != nil {
		where = append(where, "CASE WHEN t.russian_marker THEN 1.0 ELSE t.russian_audio END >= ?")
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
	err := row.Scan(&hash, &r.Name, &r.Size, &r.FileCount, &r.Kind, &r.KindConfidence, &r.Source, &r.SourceConfidence, &russian, &first, &last)
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

// Unclassified returns up to limit unclassified torrents with an ID above
// after, in ID order, each with at most maxFiles files.
func (s *Store) Unclassified(ctx context.Context, after int64, limit, maxFiles int) ([]Pending, error) {
	rows, err := s.r.QueryContext(ctx, "SELECT id, name, size FROM torrents WHERE classified_at IS NULL AND id > ? ORDER BY id LIMIT ?", after, limit)
	if err != nil {
		return nil, err
	}
	var out []Pending
	for rows.Next() {
		var p Pending
		if err := rows.Scan(&p.ID, &p.Name, &p.Size); err != nil {
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
func (s *Store) Classify(ctx context.Context, id int64, c Classification, at time.Time) error {
	_, err := s.w.ExecContext(ctx, `UPDATE torrents SET classified_at = ?, classify_error = NULL, kind = ?, kind_confidence = ?,
		source = ?, source_confidence = ?, russian_audio = ? WHERE id = ?`,
		at.Unix(), c.Kind, c.KindConfidence, c.Source, c.SourceConfidence, c.RussianAudio, id)
	return err
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
