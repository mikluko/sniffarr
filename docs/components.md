# Components

sniffarr is one binary whose components are switched on by flags, any number
of them in one process; with none on, it exits with a usage error. It runs
until SIGINT or SIGTERM.

- `-api` is the centre of a work queue. It queues the infohashes sniffers
  sight and [Torznab sources](sources.md) list, hands them out as fetch tasks, stores the metadata that comes back
  in SQLite, classifies stored torrents with the classifier, and serves the
  search API and the Torznab indexer. It is the only
  component that touches the database.
- `-ui` serves the web UI, a client of the browse API.
- `-sniff` walks the DHT for infohashes, by BEP 51 `sample_infohashes` and by
  the `announce_peer` queries its own DHT node receives, and reports them to
  the ingest API, with the announcing peers counted in sketches. Its node
  answers the queries of other nodes in turn.
- `-fetch` takes tasks from the ingest API, finds each torrent's peers with
  `get_peers` on its own DHT node, fetches the metadata from them by BEP 9,
  and returns the name, size and file list, with the peers counted in
  sketches. With `-probe` it also takes probe tasks, reading the tracks
  of stored releases.

Probes are off unless `-probe` is given, and the flag means the same to
every component in the process: `-api` orders probes and hands them out,
and `-fetch` takes them. The web UI follows the API's `Settings`, not the
flag. With probes off, results stored by earlier probes still show.

`-ui`, `-sniff` and `-fetch` are clients of the API. In a process with `-api`
they reach it over an in-process connection; without `-api` they dial
`SNIFFARR_URL`, and fail at startup when it is unset.
Any number of sniffing and fetching processes can run against one API.
`-sniff` and `-fetch` in one process share one DHT node.

The queue holds three kinds of task: the fetch of an infohash's metadata,
the lookup of a stored release's peers and the probe of its tracks. It
serves them by priority: the lookups and probes asked for in the web UI
first, then the infohashes Radarr or Sonarr grabbed and the probes of
releases a search returned, then announced infohashes and those Torznab
sources listed before sampled ones, then the oldest first. Fetchers take fetches and lookups,
and probers take probes. Fetches and lookups are orders on the NATS stream
`FETCH_ORDERS` and probes on `PROBE_ORDERS`, described below, and survive
restarts with them. The API pulls the orders 32 at a time from each
priority's consumer, highest priority first, and hands them out against the
fetchers' credit, or one to each prober's `ProbeTask`; fetchers never connect
to NATS. [Probes](probes.md) tells how the probes are ordered and handed out. An order whose fetcher
goes away goes back to the stream at once, as does one held longer than
`SNIFFARR_FETCH_LEASE`; the orders the API holds are marked in progress every
30 seconds, so those an API that stopped without handing them back held go
out again within 2 minutes. A fetch whose fetcher finds no peers, or fewer
than its `-min-peers`, is dropped at once. A fetch whose metadata times out
is retried after a backoff, by a message scheduled on
`sniffarr.orders.fetch.retry.<infohash>` that the server publishes as the
order at its time, and dropped after a few attempts. A dropped infohash is
kept out of the queue for a while, a shorter one when it had no peers, then
forgotten. It is not rejected: an announce, a Radarr or Sonarr event or a
Torznab source's first listing queues it again at once, and a sample once it
is forgotten. An announce of an
infohash whose sample waits orders it again at the announce's priority; the
first of the two orders a fetcher takes is fetched, and the other is dropped
when it comes out while the first is held, and otherwise fetched again to no
effect: its metadata is stored already, and its failure retries nothing.
Nothing bounds the orders waiting.

What happens to an infohash is recorded in the store, `torrent_journal`, one
row per event, most in the transaction of the change it records: ordered,
as queued or, for a dead infohash, revived, handed out once per attempt and
per order, failed for no peers or a timeout, with the times the attempt was
handed out when that was more than once, and dropped, for an infohash
sighted by announce, Radarr or Sonarr, or a Torznab source; metadata fetched and stored, with
when its order was queued and the times its attempt was handed out when that
was more than once, rejected or already stored;
a classification by the classifier, with who asked for it, a kind copied from a release
of the same title key, and a classification that failed; a title lookup's
match, no match or error, with who asked for it, and the earlier match of its
title key a release takes as it is stored; the grabs and imports Radarr and
Sonarr report; its first listing by each Torznab source, journaled after it
is recorded, in a transaction of its own; the probes wanted, started, with when the
lease ends, and done; and its deletion. The queue's events of an infohash
only ever sampled are left out: the journal of a sampled infohash begins with
its metadata. What repeats without changing is left out too: a lease of the
same attempt after one expired, a title lookup that finds nothing or fails as
the last one did, unless `RequestMatch` asked for it, a search that returns the release within a day of the last,
and a Radarr or Sonarr event
that names the work its title key matched already, which raises the match's
layer to theirs and writes no row. Peer lookups are not journaled; a request
for one through `RefreshPeers`, which the web UI's button calls, is, at P0. A kind
copied to more than 50 releases of a title key, a match of more than 500, or
no match for a key of more than 50, is journaled on none of them. The journal
is keyed by infohash and outlives the torrent's row, so a rejected, dropped
or deleted infohash keeps its trail; the release card shows the newest 200
entries, as does the card of an infohash whose torrent is not stored, and
entries are kept for `SNIFFARR_RETENTION_JOURNAL`. Entries read in the order
of their times, those of one time in the order they were written. An entry's time is when it was recorded, except for a grab
or an import Radarr or Sonarr reports: its entry is at the time the event
happened, with the time it was recorded as `recorded_at` when that is more
than a minute apart, and the matches it applies carry the time it happened
as `event_at`.

The server passes fetched metadata through a first filter by name and file
extensions, rejecting what is never worth classifying: adult content,
software and games, books and comics, courses, torrents under 50 MiB,
torrents with neither video nor audio files, and torrents whose names have a
letter outside the allowed scripts, Latin and Cyrillic by default. Only the
name is judged by script, not file paths; digits, punctuation, symbols, and
letters of the Common and Inherited scripts always pass. A rejected infohash is
remembered with its reason, so it is never queued again, and its journal entry
keeps the torrent's name and size.

Accepted torrents are classified in layers, cheapest first, and every release
records which layer decided its kind:

1. Rules read the name: episode and season markers, in Latin and Cyrillic
   (`S01E02`, `1x05`, `Season 3`, `Сезон: 1`, `Серии: 1-8 из 8`), make a
   series; a title with a year and a source or encoding tag (`WEB-DL`,
   `BluRay`, `x264`), but not a resolution alone, makes a movie. A name that
   says neither leaves the kind unknown.
2. Reuse: the rules also reduce the name to a title key, the series title or
   the title and year. A torrent of unknown kind takes the kind of a stored
   torrent with the same key, now or once one is classified. Only the kind is
   shared; audio and the video source belong to each release.
3. The classifier, a model behind a System One endpoint, examines a torrent
   for audio in each audio language, video source, and kind when it is still
   unknown. A torrent of unknown kind has its name read with libarr's Jev
   reader, in two requests or one when the name opens with nothing that could
   be a prefix: which opening part of the name, such as a site address or a
   tracker tag, is not the title, then the title and the kind, a movie, a
   series, or else music, a book, software, adult or other, with the audio and
   source questions riding the second request. The release keeps the
   reading's spans of the prefix and the title, which its card shows among
   the rule hits as `jev.prefix` and `jev.title`, and the title itself when a
   prefix was found. It does so lazily, for the candidates of a search before the answer, and in the
   background only for torrents of unknown kind whose names carry a marker or
   a script hint of an audio language. Both share a daily token budget,
   2,000,000 tokens by default, some 1,500 torrents a day; the background stops
   at 75% of it by default, leaving the rest for searches. A search waits for
   the classifier for a short while at most; answers that come later are
   recorded for the next search, and a slow or failing classifier never fails
   a search. It is off without an endpoint or a key; see
   [the configuration](configuration.md).

Adult and software kinds never show in search results. Music, books and
software are classified like any other kind, and the web UI shows them only
as "other" among the torrents: they have no title page and no top.

The rules also read a release's features from its name: the year, the
season, the episode (`S02E05`, `1x05`, `E05`, `Серия 5`, `5 серия`; none for
a whole season or a range such as `S1E1-8` or `1-8 серии`) and the quality, by the
resolution the name gives: `2160p` for `2160p`, `4K` or `UHD`, `1080p` for
`1080p` or `1080i`, `720p`, and `sd` for `576p`, `480p`, `SD` or an SD
source such as `DVDRip`; none when the name gives no resolution. A zero in
a year or a resolution may be written as the letter O, Latin or Cyrillic, in
either case, and `p` as the Cyrillic `р`: `2O17` reads as `2017`, and
`1O8Op`, `108Op` and `72Ор` as `1080p`, `1080p` and `720p`. A release
stored without a year or a quality gets its features read again once, at
the first start that reads them this way.

The rules read the video source from the name too: `remux` for `Remux` or
`BDRemux`; `disc_image` for `BDMV`, `VIDEO_TS`, `BD25`, `BD50`, `BD66`, `BD100`,
`DVD5` or `DVD9`; `web` for `WEB-DL`, `WEB-DLRip` or `WEBRip`; `encode` for
`BDRip`, `BRRip`, `DVDRip`, `HDRip`, `HDTV`, `TVRip` or `SATRip`, the first
of these in that order. A name with none of them, `BluRay` alone among them,
and a name of music have no source until the classifier examines the
release; its answer replaces the rules'. A release stored without a source
gets the one its name gives once, at the first start that reads it so.

Audio is an extension over the layers, off until `SNIFFARR_AUDIO_LANGUAGES`
names languages; `ru` is the only language built in. For each configured
language every stored torrent records a marker, a word or studio of the
language in its name or the paths of files other than subtitles (`.srt`,
`.ass`, `.ssa`, `.sub`, `.idx`, `.sup`), as `Rus`, `Дубляж`, `LostFilm` for
`ru`, or a translation code of the language, and a script hint, letters of the
language's script there (Cyrillic for `ru`). The codes of `ru` are those of
Russian release names: `D` dub, `P` professional many-voice, `P2`
two-voice, `P1` one-voice, `L`, `L2` and `L1` the same amateur, `A` an
author's voice-over, and `Pm`, a Russian voice of no known kind. A code
counts only in its case and where release names put codes: after the year,
its zeros perhaps written as O, and before the source
(`Bad.Apples.2026.P.WEB-DL`, `Movie.2O17.D.P2.BDRip`), or in a
comma-separated list between bars (`Movie (1984) BDRip | D, P2, A |`); a
lone `P` or `A` elsewhere, as in `Plan.B`, is none. A
marker sets the probability of an audio track in the language to 1; otherwise
the classifier answers it, one question per language. A marker records its mark as
written in the name, or else in a stored file path: a studio when there is
one, wherever it stands, else a word, else a code, so
`Silo.S03E10.rus.LostFilm` marks `LostFilm`; stored markers get their marks
recomputed once, at the first start of a version whose marking changed, and
torrents stored before codes were read get a marker from a code in their
name once, at the first start that reads them. A marker the rules stored
from a subtitle's path alone is cleared once, with its mark and
probability; a probe's finding stays. While the extension is off, nothing
about audio is recorded or returned, the classifier is asked about kind and
source only,
the background classifier has nothing to select, `BrowseService.Settings`
returns no languages, and the web UI shows no voice filter, no per-language
columns in search results and tops, and no audio rows on a release's card.

TMDB is part of the core and cannot be turned off. It runs on the
[TMDB](https://www.themoviedb.org) read access token sniffarr ships with;
`TMDB_API_KEY`, when set and not empty, replaces it with a v3 API key or a
read access token of your own. It matches title keys,
not torrents, to works, so every release under a key shares its match. A
background matcher walks the keys of movies and series without a match, newest
torrents first, and a search matches the unmatched keys of its candidates
within the same wait as its classification. Each title of the newest torrent's
name, both in a `Русское / Original` name, is searched on TMDB as a movie or a
series after the torrent's kind, in English and, when it has Cyrillic letters,
in Russian too. A candidate fits when one of its titles equals one of the
name's, ignoring case and punctuation, and, when the key has a year, its year
is the key's or one off. One fit is the match; among several, the one with a
TMDB popularity of at least 20 and ten times every other's is; else the
classifier picks
one or none, shown each fit's titles, year, language, countries, popularity,
votes and the first sentence of its overview, within the classifier's daily
budget and its search reserve. With the classifier off several fits with no
such favourite match nothing. A key that matched nothing is looked up again
after `SNIFFARR_TMDB_RETRY`, and one that matched never is; `RequestMatch`,
the release card's button, looks a key that matched nothing up at once.

A work is a movie or a series on TMDB, stored once per TMDB type and ID,
with or without releases. Title keys are matched to works many to one: the
keys of `Чужой (1979)` and `Alien.1979` both match the movie 348, and every
release of either key belongs to it. A release belongs to the work its key
is matched to, at its storing when the key is matched already and at the
match otherwise; a release whose key is matched to nothing belongs to no
work. The API names a work `m` and its TMDB ID for a movie and `tv` and its
TMDB ID for a series, `m348` or `tv1399`, as TMDB numbers the two apart.

A search of movies and series asks TMDB as well, through `/3/search/movie`
and `/3/search/tv` in the request's language, both when the search names
neither kind: two requests, under `SNIFFARR_TMDB_RATE`. An answer is kept 6
hours per kind and query, its words lowercase, and a search within them
asks TMDB nothing. Every work an answer has is stored with the answer's
title, original title, year, poster and popularity, its details left to the
backfill.

A match recorded, the work's page is fetched twice, as `en-US` and `ru-RU`,
each with its external IDs appended. The first answer gives the work's
details: its type, original title, poster path, rating and votes, runtime (a
typical episode's for a series), a series' status and seasons, its origin
countries and original language, and its IMDb, TVDB (series) and Wikidata
IDs; each answer gives its title, overview, genres and poster in that
language, stored as `en` and `ru` texts as TMDB returns them, which is the
original where TMDB has no translation. The API serves the texts and the
poster of the request's `lang`; a language without a poster takes the
other's, then the work's. A work's page is fetched once, for the first key
matched to it. A work whose page could not be fetched, or that a search
stored, keeps what it has, and the TMDB worker's backfill fetches the
details of every work without them, those with releases first, then the
oldest, under `SNIFFARR_TMDB_RATE` and `SNIFFARR_TMDB_CONCURRENCY`, until
none is left; it looks again every 10 seconds. A page TMDB refuses to serve
marks the work fetched with what a search told of it, so it is not asked
about again. The web UI shows the details on a movie's or series' page, on
the card, in the top and in search results, with posters from
`image.tmdb.org`; every page carries the TMDB logo and the notice "This
product uses the TMDB API but is not endorsed or certified by TMDB."

Peers are a second extension, always on. A peer is an `IP:port`; `-sniff` sees
one in every `announce_peer` its node receives, `-fetch` in every `get_peers`
answer and every connection of a fetch. Each client hashes a peer with
SipHash-2-4 under a 128-bit key the API generated once and keeps in the store,
and adds the hash to a HyperLogLog sketch of the infohash and the current UTC
hour; every few minutes it sends the sketches to the API, which drops those of
infohashes it has not stored and unions the rest into the stored sketch of the
torrent and hour. Nothing else about a peer is kept, and a sketch gives
nothing back but an estimate of how many distinct peers it saw, within a few
percent. Hour sketches are kept 8 days, then unioned into day sketches kept 65
days; every count is over a sliding window of 24 hours, 7 days or 30 days
ending now, the 30 days by the day beyond the hours kept.

Every 10 minutes the API ranks, for movies and for series and for each
window, the 100 works whose releases the most distinct peers share, with the
place each held in the window before, and again, for each configured audio
language, narrowed to the releases with a marker of the language or a
probability of at least 0.5 of an audio track in it, and to those with a
marker. A release that belongs to no work is in no top; the episodes and
seasons of a series all count towards it. It refreshes the distinct peers
over the last 7 days of each torrent and each work at the same time, so a
page can sort by them.

`-api` embeds a NATS server with JetStream, started before the API serves
and shut down cleanly after it stops; no other component runs one or
connects to it. Its
store is the directory `SNIFFARR_NATS_DIR`, by default `nats` beside the
SQLite database, so it shares the API's volume. The start of `-api` allows
up to 5 minutes for the server to recover its store, on top of the 15
seconds the start of every process has. At start the API connects
to it in process and creates or updates the pipeline's streams: sightings,
on a subject per discovery, `sniffarr.sightings.announce` and
`sniffarr.sightings.sample`, which drop a sighting repeated within 5 minutes
under the same `Nats-Msg-Id` header, the discovery and the infohash in hex,
so an announce after a sample of the same infohash is kept, and keep one
nobody consumed for a day, fetch and probe orders on a subject per
priority, P0 to P3, the fetch orders with `sniffarr.orders.fetch.peers` for
the peer lookups besides, and fetch and probe results. The order and result
streams are work queues without limits: a message stays until a consumer
acknowledges it. The order streams drop an order repeated within 5 minutes
under the same `Nats-Msg-Id`, and take scheduled messages on
`sniffarr.orders.fetch.retry.<infohash>` and
`sniffarr.orders.probe.retry.<infohash>`, a later one on the same subject
replacing the one pending; a consumer of each order subject delivers at most
1024 orders not acknowledged, and one neither acknowledged nor marked in
progress again after 2 minutes. A fetch order names the discovery it was
ordered for in the header `Sniffarr-Discovery`, which its retries keep, and
the torrent its fetch stores takes that discovery; an order without the
header serves its priority's. Only the API publishes and consumes the
fetch and probe orders; no component publishes the results yet.
The server listens on no port and has no monitoring port, and the
API's user connects in process only. Its JetStream domain is `sniffarr`.
With `SNIFFARR_NATS_URL` set, the server connects to that upstream server as
a leaf node, with `SNIFFARR_NATS_PASSWORD` or the `.creds` file
`SNIFFARR_NATS_CREDS`. The streams stay on the embedded server, which passes
no JetStream request upstream, and the API runs the same while the upstream
is unreachable.

`-sniff` reports the infohashes it sights to the ingest API's `Sight`, which
publishes them to the sightings stream and answers which of them are stored
torrents from one read of the store, writing nothing to it. The API's intake
takes the sightings from the stream in batches of 500, every announced one
before any sampled one, and checks each batch against the store in one
read: an infohash stored, rejected, or kept out of the queue and sampled is
dropped, and any other is ordered at its discovery's priority, under the
`Nats-Msg-Id` of its sighting, so a batch taken again after a failure orders
nothing twice. The intake moves the last sighting of a stored infohash.
