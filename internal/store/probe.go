package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"golang.org/x/text/language"

	"sarak.sh/mikluko/sniffarr/internal/audio"
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
	// Priority is the queue priority its task was leased at.
	Priority int
	// Attempts is how many probes of it have failed since the last one that
	// did not.
	Attempts int
}

// ProbeWant is the search that returned the releases WantProbe records.
type ProbeWant struct {
	// T is the Torznab function: "search", "movie" or "tvsearch".
	T     string
	Query string
	// ID is the ID searched by, as its parameter and value: "tmdbid:348",
	// "imdbid:tt0078748", "tvdbid:371980"; "" for none.
	ID string
	// Season and Episode are a TV search's, 0 when it names none.
	Season  int
	Episode int
}

// probeGrown selects a release whose failed probes have not run out, or
// which has twice the peers over 7 days it had at the last probe, and at
// least two more.
const probeGrown = `(p.peers_week IS NULL OR t.peers_week >= 2 * p.peers_week AND t.peers_week >= p.peers_week + 2)`

// probeDue selects a failed probe due again: its backoff out, and
// probeGrown. Its argument is the time in seconds.
const probeDue = `p.outcome = '` + ProbeFailed + `' AND p.retry_at <= ? AND ` + probeGrown

// leasesAfterMove is the leases of a probe an upsert finds queued: none when
// the upsert moves it ahead, as many as before otherwise.
const leasesAfterMove = `CASE WHEN excluded.priority < task_queue.priority THEN 0 ELSE task_queue.leases END`

// WantProbe queues a probe at PriorityArr, at the time, for each stored
// release of the hashes the search returned that a probe can read, unless
// its probe is done or its failures ran out without its peers growing; a
// failed one waits out its backoff. A release already queued at PriorityArr
// or behind moves to PriorityArr and to the time; a requested one stays as
// it is. Each release it queues or moves is journaled as wanted by a
// search, unless a search was journaled on it within the last day.
func (s *Store) WantProbe(ctx context.Context, hashes []Hash, w ProbeWant, at time.Time) error {
	if len(hashes) == 0 {
		return nil
	}
	args := make([]any, 0, len(hashes)+2+len(probeExtensions))
	args = append(args, PriorityArr, at.UnixMilli())
	for _, h := range hashes {
		args = append(args, h[:])
	}
	args = append(args, probeArgs()...)
	return s.tx(ctx, func(tx *sql.Tx) error {
		queued, err := hashesOf(tx.QueryContext(ctx, `INSERT INTO task_queue (kind, infohash, priority, queued_at, not_before)
			SELECT 'probe', t.infohash, ?1, ?2, coalesce(p.retry_at * 1000, 0) FROM torrent_releases t LEFT JOIN probe_releases p ON p.torrent_id = t.id
			WHERE t.infohash IN (`+placeholders(len(hashes))+`) AND (p.outcome IS NULL OR p.outcome = '`+ProbeFailed+`' AND `+probeGrown+`) AND `+probeReadable+`
			ON CONFLICT (kind, infohash) DO UPDATE SET priority = excluded.priority, queued_at = excluded.queued_at,
				leases = `+leasesAfterMove+`
			WHERE task_queue.priority >= excluded.priority
			RETURNING infohash`, args...))
		if err != nil {
			return err
		}
		d := Detail("t", w.T, "query", w.Query, "id", w.ID, "season", w.Season, "episode", w.Episode)
		for _, h := range queued {
			recent, err := wantedSince(ctx, tx, h, at)
			if err != nil || recent {
				if err != nil {
					return err
				}
				continue
			}
			if err := journal(ctx, tx, h, at, JournalProbeWanted, "search", d); err != nil {
				return err
			}
		}
		return nil
	})
}

// RequestProbe queues a probe at PriorityRequest, at the time, for each
// stored release of the hashes a probe can read, whatever its last probe and
// backoff, and returns how many it queued and how many of the hashes are
// stored; each queued is journaled as requested, unless it was queued at
// PriorityRequest already and no lease began since.
func (s *Store) RequestProbe(ctx context.Context, hashes []Hash, at time.Time) (queued, stored int, err error) {
	if len(hashes) == 0 {
		return 0, 0, nil
	}
	args := make([]any, 0, len(hashes)+2+len(probeExtensions))
	args = append(args, PriorityRequest, at.UnixMilli())
	for _, h := range hashes {
		args = append(args, h[:])
	}
	err = s.tx(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM torrent_releases WHERE infohash IN ("+placeholders(len(hashes))+")", args[2:]...).Scan(&stored); err != nil {
			return err
		}
		waiting, err := hashesOf(tx.QueryContext(ctx, `SELECT infohash FROM task_queue WHERE kind = 'probe' AND priority = ?1 AND infohash IN (`+placeholders(len(hashes))+`)
			AND (leased_at IS NULL OR queued_at > leased_at)`, append([]any{PriorityRequest}, args[2:]...)...))
		if err != nil {
			return err
		}
		hs, err := hashesOf(tx.QueryContext(ctx, `INSERT INTO task_queue (kind, infohash, priority, queued_at)
			SELECT 'probe', t.infohash, ?1, ?2 FROM torrent_releases t WHERE t.infohash IN (`+placeholders(len(hashes))+`) AND `+probeReadable+`
			ON CONFLICT (kind, infohash) DO UPDATE SET priority = excluded.priority, queued_at = excluded.queued_at, not_before = 0,
				leases = `+leasesAfterMove+`
			RETURNING infohash`, append(args, probeArgs()...)...))
		if err != nil {
			return err
		}
		queued = len(hs)
		return journalEach(ctx, tx, slices.DeleteFunc(hs, func(h Hash) bool { return slices.Contains(waiting, h) }), queued, at, JournalProbeWanted, "request", nil)
	})
	return queued, stored, err
}

// ProbeLeases returns how many probes are leased and unexpired at now.
func (s *Store) ProbeLeases(ctx context.Context, now time.Time) (int, error) {
	var n int
	err := s.r.QueryRowContext(ctx, probeLeasesQuery, now.UnixMilli()).Scan(&n)
	return n, err
}

// probeLeasesQuery counts the probes leased and unexpired at its argument.
const probeLeasesQuery = "SELECT count(*) FROM task_queue WHERE kind = 'probe' AND lease_until > ?"

// probeLeaseOutcome is the journal outcome of the lease of c, the first that
// holds: request for a probe asked for with RequestProbe, retry for a
// release whose probe failed before, search for one a search returned,
// background for the rest.
func probeLeaseOutcome(c ProbeCandidate) string {
	switch {
	case c.Priority <= PriorityRequest:
		return "request"
	case c.Attempts > 0:
		return "retry"
	case c.Wanted:
		return "search"
	}
	return "background"
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

// ProbeFailed records a failed probe of the stored release at the time, for
// reason ProbeNoPeers or ProbeTimeout, having downloaded the bytes. The
// release is due again after backoff, doubled for each failure before up to
// attempts; once attempts probes have failed it is due again only with more
// peers over 7 days than it had at the last, and ProbeFailed reports whether
// the attempts have run out. A probe requested while the attempt was in
// flight stays requested and due at once; else one queued at PriorityArr or
// ahead stays queued at PriorityArr until the backoff is out, while attempts
// remain, and any other leaves the queue.
func (s *Store) ProbeFailed(ctx context.Context, h Hash, reason string, downloaded int64, at time.Time, attempts int, backoff time.Duration) (final bool, err error) {
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
		leases, err := probeLeases(ctx, tx, h)
		if err != nil {
			return err
		}
		failed++
		final = failed >= attempts
		retry := at.Add(backoff << min(failed-1, attempts-1))
		if _, err := tx.ExecContext(ctx, `INSERT INTO probe_releases (torrent_id, outcome, attempts, retry_at, probed_at, peers_week) VALUES (?1, ?2, ?3, ?4, ?5, ?6)
			ON CONFLICT (torrent_id) DO UPDATE SET outcome = ?2, attempts = ?3, retry_at = ?4, probed_at = ?5, peers_week = ?6`,
			id, ProbeFailed, failed, retry.Unix(), at.Unix(), sql.NullInt64{Int64: peers, Valid: final}); err != nil {
			return err
		}
		requested, err := requeueProbe(ctx, tx, h, final, retry.Unix())
		if err != nil {
			return err
		}
		d := Detail("attempt", failed, "downloaded", downloaded, "peers_week", peers, "leases", manyLeases(leases))
		if !final && !requested {
			d["retry_at"] = retry.Unix()
		}
		d["final"] = final
		return journal(ctx, tx, h, at, JournalProbed, reason, d)
	})
	return final, err
}

// probeLeases returns how many times the probe of h has been leased since
// its attempt began or it last moved ahead, 0 when it is not queued.
func probeLeases(ctx context.Context, tx *sql.Tx, h Hash) (int, error) {
	var n int
	err := tx.QueryRowContext(ctx, "SELECT leases FROM task_queue WHERE kind = 'probe' AND infohash = ?", h[:]).Scan(&n)
	return n, ignoreNoRows(err)
}

// requeueProbe frees the probe of h, keeping it requested and due at once
// when it was requested after its last lease began, which it reports; else
// it keeps it queued at PriorityArr, due at retry in seconds, when it was
// queued at PriorityArr or ahead and is not final, and drops it from the
// queue otherwise.
func requeueProbe(ctx context.Context, tx *sql.Tx, h Hash, final bool, retry int64) (bool, error) {
	res, err := tx.ExecContext(ctx, `UPDATE task_queue SET lease_owner = NULL, lease_until = NULL, leases = 0
		WHERE kind = 'probe' AND infohash = ? AND priority = ? AND queued_at > leased_at`, h[:], PriorityRequest)
	if err != nil {
		return false, err
	}
	if n, err := res.RowsAffected(); err != nil || n > 0 {
		return n > 0, err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM task_queue WHERE kind = 'probe' AND infohash = ? AND (? OR priority > ?)", h[:], final, PriorityArr); err != nil {
		return false, err
	}
	_, err = tx.ExecContext(ctx, "UPDATE task_queue SET priority = ?, not_before = ?, lease_owner = NULL, lease_until = NULL, leases = 0 WHERE kind = 'probe' AND infohash = ?",
		PriorityArr, retry*1000, h[:])
	return false, err
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
// when every audio track gives another language, over a marker unless the
// release stores an audio file of its own. Its journal entry lists the first
// journalTracks tracks, with tracks_total when there were more.
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
		leases, err := probeLeases(ctx, tx, h)
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
		d := Detail("path", p.Path, "format", p.Format, "downloaded", p.Downloaded, "leases", manyLeases(leases))
		if p.Outcome != ProbeTracks {
			return journal(ctx, tx, h, at, JournalProbed, p.Outcome, d)
		}
		for _, lang := range s.opts.Languages {
			if err := probeAudio(ctx, tx, id, lang, p.Tracks); err != nil {
				return err
			}
		}
		tracks := make([]string, min(len(p.Tracks), journalTracks))
		for i, t := range p.Tracks[:len(tracks)] {
			tracks[i] = t.Kind + ":" + t.Language + ":" + t.Codec
		}
		if len(tracks) < len(p.Tracks) {
			d["tracks_total"] = len(p.Tracks)
		}
		audio, err := audioProbabilities(ctx, tx, id)
		if err != nil {
			return err
		}
		d["tracks"] = tracks
		if len(audio) > 0 {
			d["audio"] = audio
		}
		return journal(ctx, tx, h, at, JournalProbed, p.Outcome, d)
	})
}

// audioProbabilities returns the release's audio probabilities by language,
// those decided.
func audioProbabilities(ctx context.Context, tx *sql.Tx, id int64) (map[string]float64, error) {
	rows, err := tx.QueryContext(ctx, "SELECT language, probability FROM torrent_audio WHERE torrent_id = ? AND probability IS NOT NULL", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]float64{}
	for rows.Next() {
		var lang string
		var p float64
		if err := rows.Scan(&lang, &p); err != nil {
			return nil, err
		}
		out[lang] = p
	}
	return out, rows.Err()
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
			WHERE torrent_audio.layer IS NOT ?4 OR NOT `+separateAudio, append([]any{id, lang, LayerProbe, LayerRules}, separateAudioArgs()...)...)
		return err
	}
	return nil
}

// audioExtensions are the file extensions of audio a release may carry
// beside its video file, as SQL LIKE patterns.
var audioExtensions = []string{"%.mka", "%.ac3", "%.eac3", "%.dts", "%.aac", "%.flac", "%.m4a", "%.mp3", "%.opus", "%.wav"}

// separateAudio selects the release of ID ?1 with a stored audio file of
// its own. Its arguments after ?1 are separateAudioArgs.
var separateAudio = `EXISTS (SELECT 1 FROM torrent_files f WHERE f.torrent_id = ?1 AND (f.path LIKE ?` +
	strings.Repeat(" OR f.path LIKE ?", len(audioExtensions)-1) + `))`

func separateAudioArgs() []any {
	args := make([]any, len(audioExtensions))
	for i, e := range audioExtensions {
		args[i] = e
	}
	return args
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
