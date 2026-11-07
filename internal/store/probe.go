package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"golang.org/x/text/language"

	"github.com/mikluko/sniffarr/internal/audio"
)

// LayerProbe decides an audio probability by the tracks a probe read in the
// release's main video file.
const LayerProbe = "probe"

// Probe outcomes.
const (
	// ProbeTracks is a probe that read the file's track list.
	ProbeTracks = "tracks"
	// ProbeUnknown is a probe that cannot learn the tracks: no file of a
	// known format, or a track list out of reach.
	ProbeUnknown = "unknown"
	// ProbeFailed is a probe whose peers or pieces did not come.
	ProbeFailed = "failed"
)

// probeExtensions are the file extensions a probe can read the tracks of,
// as SQL LIKE patterns.
var probeExtensions = []string{"%.mkv", "%.webm", "%.mp4", "%.m4v", "%.mov"}

// Track is one track of a release's main video file.
type Track struct {
	// Kind is "video", "audio" or "subtitle".
	Kind string
	// Language is the ISO 639-2 code the container gives, LanguageBCP47 the
	// BCP-47 tag; either may be empty.
	Language      string
	LanguageBCP47 string
	Name          string
	Codec         string
	Channels      int
	Width, Height int
	// HDR is "DV", "HDR10", "HLG" or "".
	HDR string
}

// base is the track's language subtag, by LanguageBCP47 when it gives one;
// "" when neither gives one, as "und" does not.
func (t Track) base() string {
	for _, s := range []string{t.LanguageBCP47, t.Language} {
		if s == "" {
			continue
		}
		tag, err := language.Parse(s)
		if err != nil {
			continue
		}
		if b, conf := tag.Base(); conf == language.Exact {
			return b.String()
		}
	}
	return ""
}

// In reports whether the track is in the language of the BCP-47 tag.
func (t Track) In(tag string) bool {
	want, err := language.Parse(tag)
	if err != nil {
		return false
	}
	b, _ := want.Base()
	got := t.base()
	return got != "" && got == b.String()
}

// Undetermined reports whether the track gives no language.
func (t Track) Undetermined() bool {
	return t.base() == ""
}

// Probe is the outcome of a probe that is not repeated.
type Probe struct {
	// Outcome is ProbeTracks or ProbeUnknown.
	Outcome string
	// Path and Format are the file the probe read and its container,
	// "matroska" or "mp4"; empty when it read none.
	Path   string
	Format string
	Tracks []Track
	// Downloaded is the piece data the probe downloaded, in bytes.
	Downloaded int64
}

// ProbeCandidate is a stored release leased to a probe.
type ProbeCandidate struct {
	Hash Hash
	// Wanted reports whether its probe was asked for or a search of Radarr
	// or Sonarr returned it: queued at PriorityArr or ahead.
	Wanted bool
}

// probeGrown selects a release whose failed probes have not run out, or
// which has twice the peers over 7 days it had at the last probe, and at
// least two more.
const probeGrown = `(p.peers_week IS NULL OR t.peers_week >= 2 * p.peers_week AND t.peers_week >= p.peers_week + 2)`

// probeDue selects a failed probe due again: its backoff out, and
// probeGrown. Its argument is the time in seconds.
const probeDue = `p.outcome = '` + ProbeFailed + `' AND p.retry_at <= ? AND ` + probeGrown

// WantProbe queues a probe at PriorityArr, at the time, for each stored
// release of the hashes a search returned that a probe can read, unless its
// probe is done or its failures ran out without its peers growing; a failed
// one waits out its backoff. A release already queued moves ahead to
// PriorityArr and to the time.
func (s *Store) WantProbe(ctx context.Context, hashes []Hash, at time.Time) error {
	if len(hashes) == 0 {
		return nil
	}
	args := make([]any, 0, len(hashes)+2+len(probeExtensions))
	args = append(args, PriorityArr, at.UnixMilli())
	for _, h := range hashes {
		args = append(args, h[:])
	}
	args = append(args, probeArgs()...)
	_, err := s.w.ExecContext(ctx, `INSERT INTO task_queue (kind, infohash, priority, queued_at, not_before)
		SELECT 'probe', t.infohash, ?1, ?2, coalesce(p.retry_at * 1000, 0) FROM torrent_releases t LEFT JOIN probe_releases p ON p.torrent_id = t.id
		WHERE t.infohash IN (`+placeholders(len(hashes))+`) AND (p.outcome IS NULL OR p.outcome = '`+ProbeFailed+`' AND `+probeGrown+`) AND `+probeReadable+`
		ON CONFLICT (kind, infohash) DO UPDATE SET priority = min(task_queue.priority, excluded.priority), queued_at = excluded.queued_at`, args...)
	return err
}

// RequestProbe queues a probe at PriorityRequest, at the time, for each
// stored release of the hashes, whatever its last probe and backoff, and
// returns how many of them are stored.
func (s *Store) RequestProbe(ctx context.Context, hashes []Hash, at time.Time) (int, error) {
	if len(hashes) == 0 {
		return 0, nil
	}
	args := make([]any, 0, len(hashes)+2)
	args = append(args, PriorityRequest, at.UnixMilli())
	for _, h := range hashes {
		args = append(args, h[:])
	}
	res, err := s.w.ExecContext(ctx, `INSERT INTO task_queue (kind, infohash, priority, queued_at)
		SELECT 'probe', infohash, ?1, ?2 FROM torrent_releases WHERE infohash IN (`+placeholders(len(hashes))+`)
		ON CONFLICT (kind, infohash) DO UPDATE SET priority = excluded.priority, queued_at = excluded.queued_at, not_before = 0`, args...)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// ProbeLeases returns how many probes are leased and unexpired at now.
func (s *Store) ProbeLeases(ctx context.Context, now time.Time) (int, error) {
	var n int
	err := s.r.QueryRowContext(ctx, "SELECT count(*) FROM task_queue WHERE kind = 'probe' AND lease_until > ?", now.UnixMilli()).Scan(&n)
	return n, err
}

// LeaseProbe frees the expired probe leases, then leases to owner until
// now+d the release to probe next and returns it, or false when there is
// none. It takes the queued probe with the lowest priority, the newest
// first, among those at PriorityArr or ahead unless background; then, with
// background, the release never probed with the most peers over 7 days,
// else the one due a probe again with the most, which it queues at
// PrioritySample. Each is a movie or a series with peers and a stored file
// of a format a probe reads.
func (s *Store) LeaseProbe(ctx context.Context, owner int64, now time.Time, d time.Duration, background bool) (c ProbeCandidate, ok bool, err error) {
	err = s.tx(ctx, func(tx *sql.Tx) error {
		c, ok, err = leaseProbe(ctx, tx, owner, now, d, background)
		return err
	})
	return c, ok, err
}

func leaseProbe(ctx context.Context, tx *sql.Tx, owner int64, now time.Time, d time.Duration, background bool) (ProbeCandidate, bool, error) {
	if _, err := freeExpired(ctx, tx, "kind = 'probe'", now); err != nil {
		return ProbeCandidate{}, false, err
	}
	until := now.Add(d).UnixMilli()
	c, id, err := queuedProbe(ctx, tx, now, background)
	if err == nil {
		_, err = tx.ExecContext(ctx, "UPDATE task_queue SET lease_owner = ?, lease_until = ? WHERE id = ?", owner, until, id)
		return c, err == nil, err
	}
	if !errors.Is(err, sql.ErrNoRows) || !background {
		return ProbeCandidate{}, false, ignoreNoRows(err)
	}
	h, err := unqueuedProbe(ctx, tx, now)
	if err != nil || h == nil {
		return ProbeCandidate{}, false, err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO task_queue (kind, infohash, priority, queued_at, lease_owner, lease_until) VALUES ('probe', ?, ?, ?, ?, ?)",
		h, PrioritySample, now.UnixMilli(), owner, until)
	return ProbeCandidate{Hash: Hash(h)}, err == nil, err
}

// queuedProbeQuery selects the free queued probe due at its first argument,
// at the priority of its second or ahead unless its third. CROSS JOIN keeps
// SQLite walking the queued probes rather than every release.
var queuedProbeQuery = `SELECT q.id, q.infohash, q.priority FROM task_queue q CROSS JOIN torrent_releases t ON t.infohash = q.infohash
	WHERE q.kind = 'probe' AND q.lease_owner IS NULL AND q.not_before <= ? AND (q.priority <= ? OR ?) AND ` + probeCond("1") + `
	ORDER BY q.priority, q.queued_at DESC, q.id DESC LIMIT 1`

// unqueued selects releases t not queued for a probe.
const unqueued = ` AND NOT EXISTS (SELECT 1 FROM task_queue q WHERE q.kind = 'probe' AND q.infohash = t.infohash)`

// backgroundProbeQuery selects the release not queued for a probe never
// probed with the most peers over 7 days.
var backgroundProbeQuery = `SELECT t.infohash FROM torrent_releases t LEFT JOIN probe_releases p ON p.torrent_id = t.id
	WHERE ` + probeCond("p.outcome IS NULL") + unqueued + ` ORDER BY t.peers_week DESC, t.id DESC LIMIT 1`

// retryProbeQuery selects the release not queued for a probe due a probe
// again at its first argument with the most peers over 7 days.
var retryProbeQuery = `SELECT t.infohash FROM probe_releases p CROSS JOIN torrent_releases t ON t.id = p.torrent_id
	WHERE ` + probeCond(probeDue) + unqueued + ` ORDER BY t.peers_week DESC, t.id DESC LIMIT 1`

// queuedProbe returns the free queued probe due at now that LeaseProbe takes
// first, and the ID of its task, or sql.ErrNoRows.
func queuedProbe(ctx context.Context, tx *sql.Tx, now time.Time, background bool) (ProbeCandidate, int64, error) {
	var id int64
	var prio int
	var h []byte
	if err := tx.QueryRowContext(ctx, queuedProbeQuery, append([]any{now.UnixMilli(), PriorityArr, background}, probeArgs()...)...).Scan(&id, &h, &prio); err != nil {
		return ProbeCandidate{}, 0, err
	}
	return ProbeCandidate{Hash: Hash(h), Wanted: prio <= PriorityArr}, id, nil
}

// unqueuedProbe returns the infohash of the release not queued for a probe
// never probed with the most peers over 7 days, else of the one due a probe
// again at now with the most, or nil.
func unqueuedProbe(ctx context.Context, tx *sql.Tx, now time.Time) ([]byte, error) {
	for _, q := range []struct {
		query string
		args  []any
	}{
		{backgroundProbeQuery, probeArgs()},
		{retryProbeQuery, append([]any{now.Unix()}, probeArgs()...)},
	} {
		var h []byte
		err := tx.QueryRowContext(ctx, q.query, q.args...).Scan(&h)
		if err == nil {
			return h, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	}
	return nil, nil
}

// probeCond selects releases t with peers a probe can read whose probe row
// p, when state names it, passes state; its arguments are those of state,
// then probeArgs.
func probeCond(state string) string {
	return `t.peers_week > 0 AND (` + state + `) AND ` + probeReadable
}

// probeReadable selects releases t a probe can read: a movie or a series
// with a stored file of a format a probe reads. Its arguments are probeArgs.
const probeReadable = `t.kind IN ('movie', 'series')
	AND EXISTS (SELECT 1 FROM torrent_files f WHERE f.torrent_id = t.id AND (f.path LIKE ? OR f.path LIKE ? OR f.path LIKE ? OR f.path LIKE ? OR f.path LIKE ?))`

// probeArgs are the arguments probeCond takes after those of its state.
func probeArgs() []any {
	args := make([]any, len(probeExtensions))
	for i, e := range probeExtensions {
		args[i] = e
	}
	return args
}

func ignoreNoRows(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	return err
}

// ProbeFailed records a failed probe of the stored release at the time. The
// release is due again after backoff, doubled for each failure before up to
// attempts; once attempts probes have failed it is due again only with more
// peers over 7 days than it had at the last, and ProbeFailed reports whether
// the attempts have run out. A probe queued at PriorityArr or ahead stays
// queued at PriorityArr until the backoff is out, while attempts remain;
// any other leaves the queue.
func (s *Store) ProbeFailed(ctx context.Context, h Hash, at time.Time, attempts int, backoff time.Duration) (final bool, err error) {
	err = s.tx(ctx, func(tx *sql.Tx) error {
		var id, peers int64
		var failed int
		err := tx.QueryRowContext(ctx, `SELECT t.id, t.peers_week, coalesce(p.attempts, 0) FROM torrent_releases t LEFT JOIN probe_releases p ON p.torrent_id = t.id
			WHERE t.infohash = ?`, h[:]).Scan(&id, &peers, &failed)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		failed++
		final = failed >= attempts
		retry := at.Add(backoff << min(failed-1, attempts-1)).Unix()
		_, err = tx.ExecContext(ctx, `INSERT INTO probe_releases (torrent_id, outcome, attempts, retry_at, probed_at, peers_week) VALUES (?1, ?2, ?3, ?4, ?5, ?6)
			ON CONFLICT (torrent_id) DO UPDATE SET outcome = ?2, attempts = ?3, retry_at = ?4, probed_at = ?5, peers_week = ?6`,
			id, ProbeFailed, failed, retry, at.Unix(), sql.NullInt64{Int64: peers, Valid: final})
		if err != nil {
			return err
		}
		return requeueProbe(ctx, tx, h, final, retry)
	})
	return final, err
}

// requeueProbe keeps the probe of h queued at PriorityArr, due at retry in
// seconds, when it was queued at PriorityArr or ahead and is not final, and
// drops it from the queue otherwise.
func requeueProbe(ctx context.Context, tx *sql.Tx, h Hash, final bool, retry int64) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM task_queue WHERE kind = 'probe' AND infohash = ? AND (? OR priority > ?)", h[:], final, PriorityArr); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, "UPDATE task_queue SET priority = ?, not_before = ?, lease_owner = NULL, lease_until = NULL WHERE kind = 'probe' AND infohash = ?",
		PriorityArr, retry*1000, h[:])
	return err
}

// probed records the outcome of the probe of release id, h, at the time,
// clears its failures, and drops it from the queue.
func probed(ctx context.Context, tx *sql.Tx, id int64, h Hash, p Probe, at time.Time) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO probe_releases (torrent_id, outcome, attempts, retry_at, probed_at, path, format, downloaded)
		VALUES (?1, ?2, 0, NULL, ?3, ?4, ?5, ?6) ON CONFLICT (torrent_id) DO UPDATE SET
		outcome = ?2, attempts = 0, retry_at = NULL, probed_at = ?3, path = ?4, format = ?5, downloaded = downloaded + ?6`,
		id, p.Outcome, at.Unix(), nullString(p.Path), nullString(p.Format), p.Downloaded); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, "DELETE FROM task_queue WHERE kind = 'probe' AND infohash = ?", h[:])
	return err
}

// ProbeDone records the probe of the stored release at the time, drops it
// from the queue, and replaces its tracks. With the tracks read, each audio
// language of Options gets probability 1 when an audio track is in it, and 0
// when every audio track gives another language and no marker decided it.
func (s *Store) ProbeDone(ctx context.Context, h Hash, p Probe, at time.Time) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		var id int64
		err := tx.QueryRowContext(ctx, "SELECT id FROM torrent_releases WHERE infohash = ?", h[:]).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := probed(ctx, tx, id, h, p, at); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM probe_tracks WHERE torrent_id = ?", id); err != nil {
			return err
		}
		for i, t := range p.Tracks {
			if _, err := tx.ExecContext(ctx, `INSERT INTO probe_tracks (torrent_id, idx, kind, language, language_bcp47, name, codec, channels, width, height, hdr)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				id, i, t.Kind, t.Language, t.LanguageBCP47, t.Name, t.Codec, t.Channels, t.Width, t.Height, t.HDR); err != nil {
				return err
			}
		}
		if p.Outcome != ProbeTracks {
			return nil
		}
		for _, lang := range s.opts.Languages {
			if err := probeAudio(ctx, tx, id, lang, p.Tracks); err != nil {
				return err
			}
		}
		return nil
	})
}

// probeAudio sets the probability of an audio track in lang that the
// tracks decide, if they decide one.
func probeAudio(ctx context.Context, tx *sql.Tx, id int64, lang string, tracks []Track) error {
	var count, in, undetermined int
	var names []string
	for _, t := range tracks {
		if t.Kind != "audio" {
			continue
		}
		count++
		switch {
		case t.In(lang):
			in++
			names = append(names, t.Name)
		case t.Undetermined():
			undetermined++
		}
	}
	switch {
	case in > 0:
		studios, err := mergedStudios(ctx, tx, id, lang, names)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO torrent_audio (torrent_id, language, marker, script_hint, probability, layer, studios)
			VALUES (?1, ?2, 0, 0, 1, ?3, ?4) ON CONFLICT (torrent_id, language) DO UPDATE SET probability = 1, layer = ?3, studios = ?4`, id, lang, LayerProbe, studios)
		return err
	case count > 0 && undetermined == 0:
		_, err := tx.ExecContext(ctx, `INSERT INTO torrent_audio (torrent_id, language, marker, script_hint, probability, layer)
			VALUES (?1, ?2, 0, 0, 0, ?3) ON CONFLICT (torrent_id, language) DO UPDATE SET probability = 0, layer = ?3
			WHERE torrent_audio.layer IS NOT ?4`, id, lang, LayerProbe, LayerRules)
		return err
	}
	return nil
}

// mergedStudios is the studios column of the release's audio in lang with
// the language's studios named in the track names added, after those
// stored, each once.
func mergedStudios(ctx context.Context, tx *sql.Tx, id int64, lang string, names []string) (sql.NullString, error) {
	var stored sql.NullString
	err := tx.QueryRowContext(ctx, "SELECT studios FROM torrent_audio WHERE torrent_id = ? AND language = ?", id, lang).Scan(&stored)
	if err = ignoreNoRows(err); err != nil {
		return sql.NullString{}, err
	}
	var studios []string
	if stored.Valid {
		if err := json.Unmarshal([]byte(stored.String), &studios); err != nil {
			return sql.NullString{}, err
		}
	}
	if l, ok := audio.Lookup(lang); ok {
		for _, s := range l.StudiosInTexts(names) {
			if !slices.Contains(studios, s) {
				studios = append(studios, s)
			}
		}
	}
	return studiosColumn(studios)
}

// tracks fills in the Tracks of the releases.
func (s *Store) tracks(ctx context.Context, rs []Release) error {
	if len(rs) == 0 {
		return nil
	}
	index := make(map[int64]int, len(rs))
	ids := make([]int64, len(rs))
	for i, r := range rs {
		index[r.ID] = i
		ids[i] = r.ID
	}
	return s.each(ctx, func(rows *sql.Rows) error {
		var id int64
		var t Track
		if err := rows.Scan(&id, &t.Kind, &t.Language, &t.LanguageBCP47, &t.Name, &t.Codec, &t.Channels, &t.Width, &t.Height, &t.HDR); err != nil {
			return err
		}
		i := index[id]
		rs[i].Tracks = append(rs[i].Tracks, t)
		return nil
	}, `SELECT torrent_id, kind, language, language_bcp47, name, codec, channels, width, height, hdr FROM probe_tracks
		WHERE torrent_id IN (`+placeholders(len(ids))+`) ORDER BY torrent_id, idx`, int64Args(ids)...)
}

// ProbeTraffic is piece data of probes on a UTC day, in bytes: Rx what the
// probes downloaded, Tx what the fetchers uploaded to the swarms of probed
// releases.
type ProbeTraffic struct {
	Rx, Tx int64
}

// ProbeTraffic returns the probes' traffic on the day.
func (s *Store) ProbeTraffic(ctx context.Context, day string) (ProbeTraffic, error) {
	var t ProbeTraffic
	err := s.r.QueryRowContext(ctx, "SELECT rx, tx FROM probe_usage WHERE day = ?", day).Scan(&t.Rx, &t.Tx)
	return t, ignoreNoRows(err)
}

// AddProbeTraffic adds t to the probes' traffic on the day.
func (s *Store) AddProbeTraffic(ctx context.Context, day string, t ProbeTraffic) error {
	_, err := s.w.ExecContext(ctx, `INSERT INTO probe_usage (day, rx, tx) VALUES (?, ?, ?)
		ON CONFLICT (day) DO UPDATE SET rx = rx + excluded.rx, tx = tx + excluded.tx`, day, t.Rx, t.Tx)
	return err
}

func nullString(s string) sql.NullString {
	return sql.NullString{String: s, Valid: s != ""}
}
