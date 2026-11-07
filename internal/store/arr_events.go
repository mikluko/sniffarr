package store

import (
	"context"
	"database/sql"
	"errors"
	"maps"
	"time"
)

// Apps of ArrEvent.
const (
	AppRadarr = "radarr"
	AppSonarr = "sonarr"
)

// Events of ArrEvent.
const (
	EventGrab   = "grab"
	EventImport = "import"
)

// ArrEvent is a grab or an import that Radarr or Sonarr reported.
type ArrEvent struct {
	App   string
	Event string
	// Source names how the event came: "webhook" or "history"; the journal
	// records it.
	Source string
	// Key tells the app's downloads apart: events of one app, event and key
	// are one event.
	Key string
	// Hash is the torrent's infohash; nil when the download tells none.
	Hash *Hash
	// Type and TMDBID name the event's work; a zero TMDBID names none.
	Type   string
	TMDBID int
	// IDs are the work's IDs with providers other than TMDB; an ID the work
	// has with the provider already stays.
	IDs     []ExternalID
	Title   string
	Indexer string
	Size    int64
	// Season and Episode are a series', 0 when the event names none.
	Season  int
	Episode int
	At      time.Time
}

// ArrRecorded is what RecordArr did with an event.
type ArrRecorded struct {
	// New reports whether the event was not recorded before.
	New bool
	// Work is the store's ID of the event's work, 0 for none.
	Work int64
	// Stored reports whether the event's torrent is stored.
	Stored bool
}

// RecordArr records the event once per app, event and key, filling in the
// work and the infohash of an event recorded before without them. With a
// TMDB ID it stores the work, without details when it is new, and the IDs
// it lacks; with an infohash too, it points the stored torrent of the hash
// at the work, and matches the torrent's title key to the work under
// LayerArr. Insert does the same for a torrent stored later. The event is
// journaled on its infohash once, when it first has one, at the event's own
// time, with now as recorded_at when the two are more than a minute apart;
// the matches it applies are journaled at now, with the event's time as
// event_at.
func (s *Store) RecordArr(ctx context.Context, e ArrEvent, now time.Time) (ArrRecorded, error) {
	var r ArrRecorded
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var work any
		if e.TMDBID != 0 {
			var err error
			if r.Work, err = ensureWork(ctx, tx, e.Type, e.TMDBID, e.IDs, e.At); err != nil {
				return err
			}
			work = r.Work
		}
		var hash any
		if e.Hash != nil {
			hash = e.Hash[:]
		}
		var hashed bool
		err := tx.QueryRowContext(ctx, "SELECT infohash IS NOT NULL FROM arr_events WHERE app = ? AND event = ? AND key = ?", e.App, e.Event, e.Key).Scan(&hashed)
		r.New = errors.Is(err, sql.ErrNoRows)
		if err != nil && !r.New {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO arr_events (app, event, key, infohash, work, title, indexer, size, season, episode, at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (app, event, key) DO UPDATE SET work = coalesce(arr_events.work, excluded.work), infohash = coalesce(arr_events.infohash, excluded.infohash)`,
			e.App, e.Event, e.Key, hash, work, null(e.Title), null(e.Indexer), nullInt64(e.Size), nullInt(e.Season), nullInt(e.Episode), e.At.Unix()); err != nil {
			return err
		}
		if e.Hash == nil {
			return nil
		}
		if !hashed {
			var recorded time.Time
			if d := now.Sub(e.At); d > time.Minute || d < -time.Minute {
				recorded = now
			}
			if err := journal(ctx, tx, *e.Hash, e.At, JournalArr, e.Event, Detail("app", e.App, "source", e.Source, "indexer", e.Indexer, "title", e.Title,
				"size", e.Size, "season", e.Season, "episode", e.Episode, "work", r.Work, "recorded_at", recorded)); err != nil {
				return err
			}
		}
		if r.Work == 0 {
			return tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM torrent_releases WHERE infohash = ?)", e.Hash[:]).Scan(&r.Stored)
		}
		r.Stored, err = applyArr(ctx, tx, *e.Hash, r.Work, e.At, now, e.At, false)
		return err
	})
	return r, err
}

// ensureWork stores the work of the type and TMDB ID unless it is stored,
// and its IDs with providers it has none with, and returns its ID.
func ensureWork(ctx context.Context, tx *sql.Tx, typ string, tmdbID int, ids []ExternalID, at time.Time) (int64, error) {
	var id int64
	if err := tx.QueryRowContext(ctx, `INSERT INTO tmdb_works (type, tmdb_id, created_at) VALUES (?, ?, ?)
		ON CONFLICT (type, tmdb_id) DO UPDATE SET tmdb_id = excluded.tmdb_id RETURNING id`, typ, tmdbID, at.Unix()).Scan(&id); err != nil {
		return 0, err
	}
	for _, x := range ids {
		if x.Provider == ProviderTMDB || x.ID == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO tmdb_ids (work, provider, id) VALUES (?, ?, ?) ON CONFLICT (work, provider) DO NOTHING", id, x.Provider, x.ID); err != nil {
			return 0, err
		}
	}
	return id, nil
}

// applyArr points the stored torrent of the hash at the work and matches
// its title key, if it has one, to the work under LayerArr at the time,
// which points every torrent of the key at it; it reports whether the
// torrent is stored. Every torrent it points elsewhere is journaled at now
// with eventAt, the time of the event the work comes from, and the torrent
// of the hash as pending when that event was recorded before it.
func applyArr(ctx context.Context, tx *sql.Tx, h Hash, work int64, at, now, eventAt time.Time, pending bool) (bool, error) {
	var key sql.NullString
	var before sql.NullInt64
	err := tx.QueryRowContext(ctx, "SELECT title_key, work FROM torrent_releases WHERE infohash = ?", h[:]).Scan(&key, &before)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var typ string
	var tmdbID int
	if err := tx.QueryRowContext(ctx, "SELECT type, tmdb_id FROM tmdb_works WHERE id = ?", work).Scan(&typ, &tmdbID); err != nil {
		return false, err
	}
	d := Detail("work", work, "tmdb_id", tmdbID, "type", typ, "trigger", TriggerArr, "event_at", eventAt)
	own := d
	if pending {
		own = maps.Clone(d)
		own["pending"] = true
	}
	if before.Int64 != work {
		if _, err := tx.ExecContext(ctx, "UPDATE torrent_releases SET work = ? WHERE infohash = ?", work, h[:]); err != nil {
			return false, err
		}
	}
	if pending || before.Int64 != work {
		if err := journal(ctx, tx, h, now, JournalMatched, LayerArr, own); err != nil {
			return false, err
		}
	}
	if !key.Valid {
		return true, nil
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO tmdb_matches (title_key, work, layer, confidence, matched_at, error) VALUES (?, ?, ?, 1, ?, NULL)
		ON CONFLICT (title_key) DO UPDATE SET work = excluded.work, layer = excluded.layer, confidence = excluded.confidence,
		matched_at = excluded.matched_at, error = NULL`, key.String, work, LayerArr, at.Unix()); err != nil {
		return false, err
	}
	siblings, err := hashesOf(tx.QueryContext(ctx, "UPDATE torrent_releases SET work = ? WHERE title_key = ? AND work IS NOT ? RETURNING infohash", work, key.String, work))
	if err != nil {
		return false, err
	}
	return true, journalEach(ctx, tx, siblings, journalFanOut, now, JournalMatched, LayerArr, d)
}

// applyPendingArr applies, as RecordArr does, the work of the latest event
// with a work that has the hash, if there is one, and reports whether there
// is.
func applyPendingArr(ctx context.Context, tx *sql.Tx, h Hash, at time.Time) (bool, error) {
	var work, happened int64
	err := tx.QueryRowContext(ctx, "SELECT work, at FROM arr_events WHERE infohash = ? AND work IS NOT NULL ORDER BY at DESC, id DESC LIMIT 1", h[:]).Scan(&work, &happened)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	_, err = applyArr(ctx, tx, h, work, at, at, time.Unix(happened, 0), true)
	return true, err
}

// LatestArr returns when the latest event of the app and event happened,
// and false when none is recorded.
func (s *Store) LatestArr(ctx context.Context, app, event string) (time.Time, bool, error) {
	var at sql.NullInt64
	if err := s.r.QueryRowContext(ctx, "SELECT max(at) FROM arr_events WHERE app = ? AND event = ?", app, event).Scan(&at); err != nil {
		return time.Time{}, false, err
	}
	if !at.Valid {
		return time.Time{}, false, nil
	}
	return time.Unix(at.Int64, 0).UTC(), true, nil
}

// arrImported is the settings key prefix that records, per app, that its
// history was imported.
const arrImported = "arr_history_imported:"

// ArrImported reports whether SetArrImported was called for the app.
func (s *Store) ArrImported(ctx context.Context, app string) (bool, error) {
	return s.flag(ctx, arrImported+app)
}

// SetArrImported records that the app's history was imported.
func (s *Store) SetArrImported(ctx context.Context, app string) error {
	return s.setFlag(ctx, arrImported+app)
}

// TMDBByExternal returns the TMDB ID of the stored work of the type with
// the ID with the provider, or ErrNotFound.
func (s *Store) TMDBByExternal(ctx context.Context, typ, provider, id string) (int, error) {
	var tmdbID int
	err := s.r.QueryRowContext(ctx, `SELECT w.tmdb_id FROM tmdb_ids i JOIN tmdb_works w ON w.id = i.work
		WHERE i.provider = ? AND i.id = ? AND w.type = ? ORDER BY w.id LIMIT 1`, provider, id, typ).Scan(&tmdbID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return tmdbID, err
}

func nullInt64(n int64) any {
	if n == 0 {
		return nil
	}
	return n
}
