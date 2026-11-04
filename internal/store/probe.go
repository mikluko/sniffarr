package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"golang.org/x/text/language"
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

// ProbeCandidate is a stored release to probe.
type ProbeCandidate struct {
	ID   int64
	Hash Hash
	// Wanted reports whether a search of Radarr or Sonarr returned it.
	Wanted bool
}

// WantProbe records that a search returned the releases of the hashes at the
// time; hashes not stored are ignored.
func (s *Store) WantProbe(ctx context.Context, hashes []Hash, at time.Time) error {
	if len(hashes) == 0 {
		return nil
	}
	args := make([]any, 0, len(hashes)+1)
	args = append(args, at.Unix())
	for _, h := range hashes {
		args = append(args, h[:])
	}
	_, err := s.w.ExecContext(ctx, `INSERT INTO probe_releases (torrent_id, wanted_at)
		SELECT id, ?1 FROM torrent_releases WHERE infohash IN (`+placeholders(len(hashes))+`)
		ON CONFLICT (torrent_id) DO UPDATE SET wanted_at = excluded.wanted_at`, args...)
	return err
}

// NextProbe returns the release to probe next at now, other than those of
// exclude: the most recently wanted one, else, with background, the one
// with the most peers over 7 days. Either is a movie or a series with
// peers, a stored file of a format a probe reads, and no probe done, or a
// failed one due again. It returns false when there is none.
func (s *Store) NextProbe(ctx context.Context, now time.Time, background bool, exclude []int64) (ProbeCandidate, bool, error) {
	args := []any{now.Unix()}
	for _, e := range probeExtensions {
		args = append(args, e)
	}
	args = append(args, int64Args(exclude)...)
	c, err := s.nextProbe(ctx, wantedProbe(len(exclude)), args...)
	if err == nil || !errors.Is(err, sql.ErrNoRows) || !background {
		return c, err == nil, ignoreNoRows(err)
	}
	c, err = s.nextProbe(ctx, backgroundProbe(len(exclude)), args...)
	return c, err == nil, ignoreNoRows(err)
}

// probeCond selects releases due a probe, other than excluded ones; its
// arguments are the time, probeExtensions and the excluded IDs.
func probeCond(excluded int) string {
	cond := `t.kind IN ('movie', 'series') AND t.peers_week > 0
		AND (p.outcome IS NULL OR p.outcome = '` + ProbeFailed + `' AND p.retry_at <= ? AND (p.peers_week IS NULL OR t.peers_week > p.peers_week))
		AND EXISTS (SELECT 1 FROM torrent_files f WHERE f.torrent_id = t.id AND (f.path LIKE ? OR f.path LIKE ? OR f.path LIKE ? OR f.path LIKE ? OR f.path LIKE ?))`
	if excluded > 0 {
		cond += " AND t.id NOT IN (" + placeholders(excluded) + ")"
	}
	return cond
}

// wantedProbe is the query of the most recently wanted release due a probe.
// CROSS JOIN keeps SQLite walking the wanted releases rather than every
// release.
func wantedProbe(excluded int) string {
	return `SELECT t.id, t.infohash, 1 FROM probe_releases p CROSS JOIN torrent_releases t ON t.id = p.torrent_id
		WHERE p.wanted_at IS NOT NULL AND ` + probeCond(excluded) + ` ORDER BY p.wanted_at DESC, t.peers_week DESC LIMIT 1`
}

// backgroundProbe is the query of the release due a probe with the most
// peers over 7 days.
func backgroundProbe(excluded int) string {
	return `SELECT t.id, t.infohash, 0 FROM torrent_releases t LEFT JOIN probe_releases p ON p.torrent_id = t.id
		WHERE ` + probeCond(excluded) + ` ORDER BY t.peers_week DESC, t.id DESC LIMIT 1`
}

func (s *Store) nextProbe(ctx context.Context, query string, args ...any) (ProbeCandidate, error) {
	var c ProbeCandidate
	var h []byte
	if err := s.r.QueryRowContext(ctx, query, args...).Scan(&c.ID, &h, &c.Wanted); err != nil {
		return ProbeCandidate{}, err
	}
	c.Hash = Hash(h)
	return c, nil
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
// the attempts have run out.
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
		return err
	})
	return final, err
}

// ProbeDone records the probe of the stored release at the time, replacing
// its tracks. With the tracks read, each audio language of Options gets
// probability 1 when an audio track is in it, and 0 when every audio track
// gives another language and no marker decided it.
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
		if _, err := tx.ExecContext(ctx, `INSERT INTO probe_releases (torrent_id, outcome, attempts, retry_at, probed_at, path, format, downloaded)
			VALUES (?1, ?2, 0, NULL, ?3, ?4, ?5, ?6) ON CONFLICT (torrent_id) DO UPDATE SET
			outcome = ?2, retry_at = NULL, probed_at = ?3, path = ?4, format = ?5, downloaded = downloaded + ?6`,
			id, p.Outcome, at.Unix(), nullString(p.Path), nullString(p.Format), p.Downloaded); err != nil {
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
	var audio, in, undetermined int
	for _, t := range tracks {
		if t.Kind != "audio" {
			continue
		}
		audio++
		switch {
		case t.In(lang):
			in++
		case t.Undetermined():
			undetermined++
		}
	}
	switch {
	case in > 0:
		_, err := tx.ExecContext(ctx, `INSERT INTO torrent_audio (torrent_id, language, marker, script_hint, probability, layer)
			VALUES (?1, ?2, 0, 0, 1, ?3) ON CONFLICT (torrent_id, language) DO UPDATE SET probability = 1, layer = ?3`, id, lang, LayerProbe)
		return err
	case audio > 0 && undetermined == 0:
		_, err := tx.ExecContext(ctx, `INSERT INTO torrent_audio (torrent_id, language, marker, script_hint, probability, layer)
			VALUES (?1, ?2, 0, 0, 0, ?3) ON CONFLICT (torrent_id, language) DO UPDATE SET probability = 0, layer = ?3
			WHERE torrent_audio.layer IS NOT ?4`, id, lang, LayerProbe, LayerRules)
		return err
	}
	return nil
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
