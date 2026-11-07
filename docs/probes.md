# Probes

A probe reads the track list of a stored release's main video file from its
container header, which lies in the file's first pieces. Only `-fetch` with
`-probe-dir` probes, and the API hands out the tasks.

The fetcher finds the torrent's peers with `get_peers`, fetches its
metadata by BEP 9, and picks the largest file ending in `.mkv`, `.webm`,
`.mp4`, `.m4v` or `.mov`; for a season pack that is one episode. It reads
the file through the pieces each read covers, downloading them from the
peers and verifying each against its hash before reading it. In Matroska
and WebM it reads the EBML header and the Segment's top-level element
headers up to Tracks, reading no body but those of SeekHead and Tracks, and
follows SeekHead when Tracks comes after the first Cluster; in MP4 and MOV it reads the
top-level box headers up to `moov`. A probe that would download more than
the task's `max_bytes` stops there, and the tracks stay unknown. Of each
track it records the kind, the language as ISO 639-2 and as BCP-47, the
name, the codec, an audio track's channels, and a video track's size and
dynamic range: `DV` for a Dolby Vision configuration (Matroska's
BlockAdditionMapping, MP4's `dvcC`, `dvvC` or `dvwC`), else `HLG` or
`HDR10` by the transfer characteristics, or `HDR10` for mastering metadata.

Candidates are stored movies and series with a stored file of those
formats and at least one distinct peer over the last 7 days. Probes wait in
the API's task queue beside fetches and peer lookups, by priority and then
the most recently queued first. Releases asked for with `RequestProbe`, the
web UI's probe button, go first, whatever their last probe. Releases a
Torznab search with a query or an ID returned go next, unless their probe is
done or its failures ran out without the peers growing. With the queue
empty, the release never probed with the most peers over 7 days goes, then
the one due a probe again with the most. A release without peers waits
until it has some. Each release is probed once, and
the outcome is stored, the tracks in their own table: read, unknown
(no file of those formats, a track list beyond `max_bytes`, or a header the
probe cannot read), or failed (fewer than `-min-peers` peers within
`-peer-wait`, the metadata not within `-fetch-timeout`, or the pieces not
before `-probe-timeout`). The release's journal records a request, and once
a day a search that queued or moved its probe, with the query, the first
hand-out of each attempt and of each move ahead with the piece data it may
download, and each outcome with the piece data downloaded, the leases the
attempt took when it took more than one and, for a failure, whether the attempts have run out and, when it waits out a backoff, when it
is due again. A failed probe is repeated after
`SNIFFARR_PROBE_BACKOFF`, doubled for each failure before; a requested or
searched one stays queued as searched until then, unless it was requested
while the failed attempt was in flight, which keeps the request. Once
`SNIFFARR_PROBE_ATTEMPTS` have failed since the release was last read, the
backoff stays at its last doubling and the release is probed again only once
it has twice the peers over 7 days it had at its last probe, and at least two
more. A task is leased for
`SNIFFARR_PROBE_LEASE`; leases are kept in the queue, and a restart of the
API frees them.

The API meters the piece data probes download per UTC day, as it meters
the classifier's tokens: `SNIFFARR_PROBE_DAILY_BYTES`, 100 GiB by default.
A lease counts as `SNIFFARR_PROBE_MAX_BYTES` until its result reports what
it downloaded. Releases no search returned stop at 75% of the day's bytes
by default, `SNIFFARR_PROBE_SEARCH_RESERVE` leaving the rest for searched
ones.

With the audio extension on, a probe that read the tracks sets the
probability of an audio track in each configured language, under layer
`probe`: 1 when an audio track is in it; 0 when every audio track gives
another language, over a marker in the name too, unless the release has an
audio file of its own (`.mka`, `.ac3`, `.eac3`, `.dts`, `.aac`, `.flac`,
`.m4a`, `.mp3`, `.opus`, `.wav`), which may carry that audio. The Torznab
title follows the probability: `[NORUS …]` once the probe set 0, `[RUS]`
while the marker stands. A track that gives no language
leaves the probability as it was. Jev does not override a probe. The
language's studios named in its tracks, as `MVO LostFilm`, join the
release's studios for the language.

What a probe downloaded is seeded back. The pieces stay in `-probe-dir` and
the torrent in the client, which uploads them to its peers and announces
itself on the DHT for the torrent every 30 minutes. The fetcher removes a
torrent and deletes its pieces once it has uploaded as much as it
downloaded; before that, once it has been seeded for `-seed-max-age`, or,
oldest first, while the torrents kept hold more than `-seed-max-size` of
piece data. It looks every minute, and tells the API by `ProbeUpload` what
the torrents uploaded since the last look; a report the API refuses goes
with the next. The seeded torrents, what each downloaded and uploaded, and
which pieces are complete survive a restart. Metadata fetches upload
nothing.
