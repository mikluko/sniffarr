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
	"sarak.sh/mikluko/sniffarr/internal/container"
	"sarak.sh/mikluko/sniffarr/internal/names"
)

// LayerProbe decides an audio probability or a quality by the tracks a probe
// read in the release's main video file.
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
	// Default is the track's default flag in the container.
	Default bool
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
	// Leases is how many times the attempt was handed out.
	Leases int
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

// ProbeOrder is a probe to order of a stored release, due at At, or at once
// when At is zero.
type ProbeOrder struct {
	Hash Hash
	At   time.Time
}

// probeGrown selects a release whose failed probes have not run out, or
// which has twice the peers over 7 days it had at the last probe, and at
// least two more.
const probeGrown = `(p.peers_week IS NULL OR t.peers_week >= 2 * p.peers_week AND t.peers_week >= p.peers_week + 2)`

// probeDue selects a failed probe due again: its backoff out, and
// probeGrown. Its argument is the time in seconds.
const probeDue = `p.outcome = '` + ProbeFailed + `' AND p.retry_at <= ? AND ` + probeGrown

// WantProbe returns the probe orders of the stored releases of the hashes
// with peers that a probe can read whose probe is neither done nor out of
// failures without its peers growing, a failed one due once its backoff is
// out, and journals each at the time as wanted by the search w, unless a
// search was journaled on it within the last day.
func (s *Store) WantProbe(ctx context.Context, hashes []Hash, w ProbeWant, at time.Time) ([]ProbeOrder, error) {
	if len(hashes) == 0 {
		return nil, nil
	}
	args := make([]any, 0, len(hashes)+len(probeExtensions))
	for _, h := range hashes {
		args = append(args, h[:])
	}
	args = append(args, probeArgs()...)
	var out []ProbeOrder
	err := s.tx(ctx, func(tx *sql.Tx) error {
		out = nil
		err := eachOn(ctx, tx, func(rows *sql.Rows) error {
			var b []byte
			var retry int64
			if err := rows.Scan(&b, &retry); err != nil {
				return err
			}
			o := ProbeOrder{Hash: Hash(b)}
			if retry > at.Unix() {
				o.At = time.Unix(retry, 0).UTC()
			}
			out = append(out, o)
			return nil
		}, `SELECT t.infohash, coalesce(p.retry_at, 0) FROM torrent_releases t LEFT JOIN probe_releases p ON p.torrent_id = t.id
			WHERE t.infohash IN (`+placeholders(len(hashes))+`) AND `+probeCond(`p.outcome IS NULL OR p.outcome = '`+ProbeFailed+`' AND `+probeGrown), args...)
		if err != nil {
			return err
		}
		return journalWanted(ctx, tx, out, w, at)
	})
	return out, err
}

// journalWanted journals each of the orders at the time as wanted by the
// search w, unless a search was journaled on it within the last day.
func journalWanted(ctx context.Context, tx *sql.Tx, orders []ProbeOrder, w ProbeWant, at time.Time) error {
	d := Detail("t", w.T, "query", w.Query, "id", w.ID, "season", w.Season, "episode", w.Episode)
	for _, o := range orders {
		recent, err := wantedSince(ctx, tx, o.Hash, at)
		if err != nil {
			return err
		}
		if recent {
			continue
		}
		if err := journal(ctx, tx, o.Hash, at, JournalProbeWanted, "search", d); err != nil {
			return err
		}
	}
	return nil
}

// RequestProbe journals, at the time, a probe requested on each stored
// release of the hashes a probe can read, whatever its last probe and
// backoff, and returns those releases and how many of the hashes are stored.
func (s *Store) RequestProbe(ctx context.Context, hashes []Hash, at time.Time) (readable []Hash, stored int, err error) {
	if len(hashes) == 0 {
		return nil, 0, nil
	}
	args := make([]any, 0, len(hashes)+len(probeExtensions))
	for _, h := range hashes {
		args = append(args, h[:])
	}
	err = s.tx(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM torrent_releases WHERE infohash IN ("+placeholders(len(hashes))+")", args...).Scan(&stored); err != nil {
			return err
		}
		if readable, err = hashesOf(tx.QueryContext(ctx, `SELECT t.infohash FROM torrent_releases t WHERE t.infohash IN (`+placeholders(len(hashes))+`) AND `+probeReadable,
			append(args, probeArgs()...)...)); err != nil {
			return err
		}
		return journalEach(ctx, tx, readable, len(readable), at, JournalProbeWanted, "request", nil)
	})
	return readable, stored, err
}

// ProbeDue reports whether the stored release of h with peers that a probe
// can read is due a probe at now: never probed, or failed and due again.
func (s *Store) ProbeDue(ctx context.Context, h Hash, now time.Time) (bool, error) {
	var due bool
	err := s.r.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM torrent_releases t LEFT JOIN probe_releases p ON p.torrent_id = t.id
		WHERE t.infohash = ? AND `+probeCond("p.outcome IS NULL OR "+probeDue)+`)`, append([]any{h[:], now.Unix()}, probeArgs()...)...).Scan(&due)
	return due, err
}

// ProbeLease is a probe handed to a prober.
type ProbeLease struct {
	Hash     Hash
	Priority int
	// MaxBytes is the most piece data the probe may download.
	MaxBytes int64
	Until    time.Time
}

// RecordProbeLease journals, at the time, the first hand-out of an attempt at
// the probe of l's stored release, with the attempt and as probeLeaseOutcome
// names it.
func (s *Store) RecordProbeLease(ctx context.Context, l ProbeLease, at time.Time) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		var attempts int
		err := tx.QueryRowContext(ctx, `SELECT coalesce(p.attempts, 0) FROM torrent_releases t LEFT JOIN probe_releases p ON p.torrent_id = t.id
			WHERE t.infohash = ?`, l.Hash[:]).Scan(&attempts)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		return journal(ctx, tx, l.Hash, at, JournalProbeLeased, probeLeaseOutcome(l.Priority, attempts),
			Detail("attempt", attempts+1, "max_bytes", l.MaxBytes, "lease_until", l.Until))
	})
}

// probeLeaseOutcome is the journal outcome of a hand-out at the priority of
// the probe of a release whose probes failed attempts times since the last
// that did not, the first that holds: request for a probe asked for with
// RequestProbe, retry for a release whose probe failed before, search for
// one a search returned, background for the rest.
func probeLeaseOutcome(priority, attempts int) string {
	switch {
	case priority <= PriorityRequest:
		return "request"
	case attempts > 0:
		return "retry"
	case priority <= PriorityArr:
		return "search"
	}
	return "background"
}

// BackgroundProbes returns up to n stored releases with peers that a probe
// can read, due a probe at now: those never probed, the most peers over 7
// days first, then those due again, likewise.
func (s *Store) BackgroundProbes(ctx context.Context, n int, now time.Time) ([]Hash, error) {
	out, err := hashesOf(s.r.QueryContext(ctx, backgroundProbeQuery, append(probeArgs(), n)...))
	if err != nil || len(out) >= n {
		return out, err
	}
	more, err := hashesOf(s.r.QueryContext(ctx, retryProbeQuery, append(append([]any{now.Unix()}, probeArgs()...), n-len(out))...))
	return append(out, more...), err
}

// backgroundProbeQuery selects, given probeArgs and a limit, the releases
// never probed, the most peers over 7 days first.
var backgroundProbeQuery = `SELECT t.infohash FROM torrent_releases t LEFT JOIN probe_releases p ON p.torrent_id = t.id
	WHERE ` + probeCond("p.outcome IS NULL") + ` ORDER BY t.peers_week DESC, t.id DESC LIMIT ?`

// retryProbeQuery selects, given the time in seconds, probeArgs and a limit,
// the releases due a probe again then, the most peers over 7 days first.
var retryProbeQuery = `SELECT t.infohash FROM probe_releases p CROSS JOIN torrent_releases t ON t.id = p.torrent_id
	WHERE ` + probeCond(probeDue) + ` ORDER BY t.peers_week DESC, t.id DESC LIMIT ?`

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
// reason ProbeNoPeers or ProbeTimeout, having downloaded the bytes in an
// attempt handed out leases times. The release is due again after backoff,
// doubled for each failure before up to attempts, which ProbeFailed returns
// the time of; once attempts probes have failed it is due again only with
// more peers over 7 days than it had at the last, and ProbeFailed reports
// whether the attempts have run out.
func (s *Store) ProbeFailed(ctx context.Context, h Hash, reason string, downloaded int64, leases int, at time.Time, attempts int, backoff time.Duration) (final bool, retry time.Time, err error) {
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
		retry = at.Add(backoff << min(failed-1, attempts-1)).Truncate(time.Second)
		if _, err := tx.ExecContext(ctx, `INSERT INTO probe_releases (torrent_id, outcome, attempts, retry_at, probed_at, peers_week) VALUES (?1, ?2, ?3, ?4, ?5, ?6)
			ON CONFLICT (torrent_id) DO UPDATE SET outcome = ?2, attempts = ?3, retry_at = ?4, probed_at = ?5, peers_week = ?6`,
			id, ProbeFailed, failed, retry.Unix(), at.Unix(), sql.NullInt64{Int64: peers, Valid: final}); err != nil {
			return err
		}
		d := Detail("attempt", failed, "downloaded", downloaded, "peers_week", peers, "leases", manyLeases(leases))
		if !final {
			d["retry_at"] = retry.Unix()
		}
		d["final"] = final
		return journal(ctx, tx, h, at, JournalProbed, reason, d)
	})
	return final, retry, err
}

// probed records the outcome of the probe of release id at the time, and
// clears its failures.
func probed(ctx context.Context, tx *sql.Tx, id int64, p Probe, at time.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO probe_releases (torrent_id, outcome, attempts, retry_at, probed_at, path, format, downloaded)
		VALUES (?1, ?2, 0, NULL, ?3, ?4, ?5, ?6) ON CONFLICT (torrent_id) DO UPDATE SET
		outcome = ?2, attempts = 0, retry_at = NULL, probed_at = ?3, path = ?4, format = ?5, downloaded = downloaded + ?6`,
		id, p.Outcome, at.Unix(), nullString(p.Path), nullString(p.Format), p.Downloaded)
	return err
}

// ProbeDone records the probe of the stored release at the time, and
// replaces its tracks and, as probeVideo does, its quality, HDR and codec.
// With the tracks read, each audio
// language of Options gets probability 1 when an audio track is in it, and 0
// when every audio track gives another language, over a marker unless the
// release stores an audio file of its own. Its journal entry lists the first
// journalTracks tracks, with tracks_total when there were more, and the
// quality the video track set.
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
		if err := probed(ctx, tx, id, p, at); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM probe_tracks WHERE torrent_id = ?", id); err != nil {
			return err
		}
		for i, t := range p.Tracks {
			if _, err := tx.ExecContext(ctx, `INSERT INTO probe_tracks (torrent_id, idx, kind, language, language_bcp47, name, codec, channels, width, height, hdr, is_default)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				id, i, t.Kind, t.Language, t.LanguageBCP47, t.Name, t.Codec, t.Channels, t.Width, t.Height, t.HDR, t.Default); err != nil {
				return err
			}
		}
		quality, err := probeVideo(ctx, tx, id, p.Tracks)
		if err != nil {
			return err
		}
		d := Detail("path", p.Path, "format", p.Format, "downloaded", p.Downloaded, "leases", manyLeases(p.Leases), "quality", quality)
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

// probeVideo sets the HDR and codec of release id to those of the video
// track of the tracks, and its quality to the track's by widthQuality under
// layer LayerProbe, returning that quality; a release whose tracks give no
// width gets back the quality of its name.
func probeVideo(ctx context.Context, tx *sql.Tx, id int64, tracks []Track) (string, error) {
	v, _ := videoTrack(tracks)
	if q := widthQuality(v.Width); q != "" {
		_, err := tx.ExecContext(ctx, "UPDATE torrent_releases SET quality = ?, quality_layer = ?, hdr = ?, codec = ? WHERE id = ?",
			q, LayerProbe, null(v.HDR), null(v.Codec), id)
		return q, err
	}
	var name string
	var layer sql.NullString
	if err := tx.QueryRowContext(ctx, "SELECT name, quality_layer FROM torrent_releases WHERE id = ?", id).Scan(&name, &layer); err != nil {
		return "", err
	}
	if layer.Valid {
		if _, err := tx.ExecContext(ctx, "UPDATE torrent_releases SET quality = ?, quality_layer = NULL WHERE id = ?", null(names.Parse(name).Quality), id); err != nil {
			return "", err
		}
	}
	_, err := tx.ExecContext(ctx, "UPDATE torrent_releases SET hdr = ?, codec = ? WHERE id = ?", null(v.HDR), null(v.Codec), id)
	return "", err
}

// videoTrack returns the video track of the tracks that carries the film:
// the first default one of a codec that is not container.Picture, else the
// first of such a codec; ok is false for none.
func videoTrack(tracks []Track) (v Track, ok bool) {
	for _, t := range tracks {
		if t.Kind != "video" || container.Picture(t.Codec) {
			continue
		}
		if t.Default {
			return t, true
		}
		if !ok {
			v, ok = t, true
		}
	}
	return v, ok
}

// widthQuality is the quality of a video frame the width wide, "" for an
// unknown width; by width alone, a frame cropped to a wider aspect keeps its
// class.
func widthQuality(width int) string {
	switch {
	case width <= 0:
		return ""
	case width >= 3200:
		return names.Quality2160p
	case width >= 1800:
		return names.Quality1080p
	case width >= 1200:
		return names.Quality720p
	}
	return names.QualitySD
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
		if err := rows.Scan(&id, &t.Kind, &t.Language, &t.LanguageBCP47, &t.Name, &t.Codec, &t.Channels, &t.Width, &t.Height, &t.HDR, &t.Default); err != nil {
			return err
		}
		i := index[id]
		rs[i].Tracks = append(rs[i].Tracks, t)
		return nil
	}, `SELECT torrent_id, kind, language, language_bcp47, name, codec, channels, width, height, hdr, is_default FROM probe_tracks
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
