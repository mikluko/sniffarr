# sniffarr

sniffarr is a private classification database of BitTorrent torrents
discovered on the DHT, with an API to search it. Its web UI is a catalogue
of movies and series as TMDB knows them, each with the releases of it the
DHT carries; a search looks in the database and on TMDB together, so it
also finds what has no release yet.

The database and the API are meant to stay private: run them for yourself, not
as a public index. Peer IP addresses are never sent to the server or stored:
peers are counted in HyperLogLog sketches of keyed hashes, from which no
address comes back. The sniffer fetches metadata only: it never downloads
pieces, never uploads, and never announces itself into a swarm.

## Components

sniffarr is one binary whose components are switched on by flags, any number
of them in one process; with none on, it exits with a usage error. It runs
until SIGINT or SIGTERM.

- `-api` is the centre of a work queue. It queues the infohashes sniffers
  sight, hands them out as fetch tasks, stores the metadata that comes back
  in SQLite, classifies stored torrents with [TypeSafe](https://typesafe.ai)'s
  Jev model, and serves the search API and the Torznab indexer. It is the only
  component that touches the database.
- `-ui` serves the web UI, a client of the browse API.
- `-sniff` walks the DHT for infohashes, by BEP 51 `sample_infohashes` and by
  the `announce_peer` queries its own DHT node receives, and reports them to
  the ingest API, with the announcing peers counted in sketches. Its node
  answers the queries of other nodes in turn.
- `-fetch` takes tasks from the ingest API, finds each torrent's peers with
  `get_peers` on its own DHT node, fetches the metadata from them by BEP 9,
  and returns the name, size and file list, with the peers counted in
  sketches.

`-ui`, `-sniff` and `-fetch` are clients of the API. In a process with `-api`
they reach it over an in-process connection, with the process's own
`SNIFFARR_INGEST_TOKEN` and `SNIFFARR_SEARCH_TOKEN`; without `-api` they dial
`SNIFFARR_URL` with the same tokens, and fail at startup when it is unset.
Any number of sniffing and fetching processes can run against one API.
`-sniff` and `-fetch` in one process share one DHT node.

The queue serves announced infohashes before sampled ones, then the oldest
first. It lives in the same SQLite database and survives restarts; a task
whose fetcher goes away, or whose lease expires, goes back to the queue. A
task whose fetcher finds no peers, or fewer than its `-min-peers`, is dropped
at once. A task whose metadata times out is retried after a backoff and
dropped after a few attempts.
The queue is bounded: once a minute the API evicts the infohashes that have
waited too long, then the ones beyond the size limit, sampled ones before
announced ones and the oldest first. A dropped or evicted infohash is kept out
of the queue for a while, a shorter one when it had no peers, then forgotten.
It is not rejected: an announce queues it again at once, and a sample once it
is forgotten.

The server passes fetched metadata through a first filter by name and file
extensions, rejecting what is never worth classifying: adult content,
software and games, books and comics, courses, torrents under 50 MiB,
torrents with neither video nor audio files, and torrents whose names have a
letter outside the allowed scripts, Latin and Cyrillic by default. Only the
name is judged by script, not file paths; digits, punctuation, symbols, and
letters of the Common and Inherited scripts always pass. A rejected infohash is
remembered with its reason only, so it is never queued again.

Accepted torrents are classified in layers, cheapest first, and every release
records which layer decided its kind:

1. Rules read the name: episode and season markers, in Latin and Cyrillic
   (`S01E02`, `1x05`, `Season 3`, `Сезон: 1`, `Серии: 1-8 из 8`), make a
   series; a title with a year and a quality tag makes a movie. A name that
   says neither leaves the kind unknown.
2. Reuse: the rules also reduce the name to a title key, the series title or
   the title and year. A torrent of unknown kind takes the kind of a stored
   torrent with the same key, now or once one is classified. Only the kind is
   shared; audio and the video source belong to each release.
3. [TypeSafe](https://typesafe.ai)'s Jev examines a torrent for audio in each
   audio language, video source, and kind when it is still unknown. It does
   so lazily, for the candidates of a search before the answer, and in the
   background only for torrents of unknown kind whose names carry a marker or
   a script hint of an audio language. Both share a daily token budget,
   2,000,000 tokens by default, some 1,500 torrents a day; the background stops
   at 75% of it by default, leaving the rest for searches. A search waits for
   Jev for a short while at most; answers that come later are recorded for the
   next search, and a slow or failing Jev never fails a search.

Adult and software kinds never show in search results. Music, books and
software are classified like any other kind, and the web UI shows them only
as "other" among the torrents: they have no title page and no top.

The rules also read a release's features from its name: the year, the
season, the episode (`S02E05`, `1x05`, `E05`, `Серия 5`, `5 серия`; none for
a whole season or a range such as `S1E1-8` or `1-8 серии`) and the quality, by the
resolution the name gives: `2160p` for `2160p`, `4K` or `UHD`, `1080p` for
`1080p` or `1080i`, `720p`, and `sd` for `576p`, `480p`, `SD` or an SD
source such as `DVDRip`; none when the name gives no resolution. A release
stored before episodes and qualities were read gets them once, at the first
start that has them.

Audio is an extension over the layers, off until `SNIFFARR_AUDIO_LANGUAGES`
names languages; `ru` is the only language built in. For each configured
language every stored torrent records a marker, a word or studio of the
language in its name or file paths (`Rus`, `Дубляж`, `LostFilm` for `ru`), and
a script hint, letters of the language's script there (Cyrillic for `ru`). A
marker sets the probability of an audio track in the language to 1; otherwise
Jev answers it, one question per language. A marker records its mark, the
word or studio as written in the name, or else in a stored file path:
`Rus`, `LostFilm`; markers stored without one get theirs once, at the first
start that records marks. While the extension is off, nothing
about audio is recorded or returned, Jev is asked about kind and source only,
the background classifier has nothing to select, `BrowseService.Settings`
returns no languages, and the web UI shows no voice filter, no per-language
columns in search results and tops, and no audio rows on a release's card.

TMDB is a second extension, on by default with the
[TMDB](https://www.themoviedb.org) read access token sniffarr ships with;
`TMDB_API_KEY` replaces it with a v3 API key or a read access token of your
own, and set empty turns the extension off. It matches title keys,
not torrents, to works, so every release under a key shares its match. A
background matcher walks the keys of movies and series without a match, newest
torrents first, and a search matches the unmatched keys of its candidates
within the same wait as its classification. Each title of the newest torrent's
name, both in a `Русское / Original` name, is searched on TMDB as a movie or a
series after the torrent's kind, in English and, when it has Cyrillic letters,
in Russian too. A candidate fits when one of its titles equals one of the
name's, ignoring case and punctuation, and, when the key has a year, its year
is the key's or one off. One fit is the match; among several, the one with a
TMDB popularity of at least 20 and ten times every other's is; else Jev picks
one or none, shown each fit's titles, year, language, countries, popularity,
votes and the first sentence of its overview, within the classifier's daily
budget and its search reserve. Without `TYPESAFE_API_KEY` several fits with no
such favourite match nothing. A key that matched nothing is looked up again
after `SNIFFARR_TMDB_RETRY`, and one that matched never is. Without the key
nothing is matched and releases carry no IDs.

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

Peers are a third extension, always on. A peer is an `IP:port`; `-sniff` sees
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
change against the window before, and again, for each configured audio
language, narrowed to the releases with a marker of the language or a
probability of at least 0.5 of an audio track in it, and to those with a
marker. A release that belongs to no work is in no top; the episodes and
seasons of a series all count towards it. It refreshes the distinct peers
over the last 7 days of each torrent and each work at the same time, so a
page can sort by them.

## API

Both API groups are [ConnectRPC](https://connectrpc.com) services, defined in
[`proto/sniffarr/v1`](proto/sniffarr/v1). The server speaks HTTP/1.1 and HTTP/2
without TLS, so Connect, gRPC and gRPC-Web clients all work; `Fetch` streams
both ways and needs HTTP/2 end to end. Each group needs its own bearer token
in the `Authorization` header; `/healthz` needs none.

| Service                     | RPC      | Token                   |
| --------------------------- | -------- | ----------------------- |
| `sniffarr.v1.IngestService` | `Sight`  | `SNIFFARR_INGEST_TOKEN` |
| `sniffarr.v1.IngestService` | `Fetch`  | `SNIFFARR_INGEST_TOKEN` |
| `sniffarr.v1.IngestService` | `HashKey` | `SNIFFARR_INGEST_TOKEN` |
| `sniffarr.v1.IngestService` | `Peers`  | `SNIFFARR_INGEST_TOKEN` |
| `sniffarr.v1.SearchService` | `Search` | `SNIFFARR_SEARCH_TOKEN` |
| `sniffarr.v1.SearchService` | `Get`    | `SNIFFARR_SEARCH_TOKEN` |
| `sniffarr.v1.BrowseService` | `SearchTitles` | `SNIFFARR_SEARCH_TOKEN` |
| `sniffarr.v1.BrowseService` | `Title`  | `SNIFFARR_SEARCH_TOKEN` |
| `sniffarr.v1.BrowseService` | `TitleReleases` | `SNIFFARR_SEARCH_TOKEN` |
| `sniffarr.v1.BrowseService` | `Torrents` | `SNIFFARR_SEARCH_TOKEN` |
| `sniffarr.v1.BrowseService` | `Card`   | `SNIFFARR_SEARCH_TOKEN` |
| `sniffarr.v1.BrowseService` | `Stats`  | `SNIFFARR_SEARCH_TOKEN` |
| `sniffarr.v1.BrowseService` | `Home`   | `SNIFFARR_SEARCH_TOKEN` |
| `sniffarr.v1.BrowseService` | `Top`    | `SNIFFARR_SEARCH_TOKEN` |
| `sniffarr.v1.BrowseService` | `Settings` | `SNIFFARR_SEARCH_TOKEN` |

- `Sight` is client-streaming: it queues every infohash the server has neither
  stored, rejected nor queued, and returns how many were new and how many
  known, and, per infohash sent, whether it is a stored torrent.
- `Fetch` is bidirectional: the client grants credit, one unit per task it can
  take, the server answers with a task per unit, and the client returns each
  task's result, metadata or a failure, on the same stream.
- `HashKey` returns the 16-byte SipHash-2-4 key peers are hashed with.
- `Peers` is client-streaming: each message is an infohash, the start of a
  UTC hour and a sketch in the Aggregate Knowledge storage specification
  v1.0.0 (`log2m` 10, `regwidth` 5) of the peers seen sharing it in that
  hour, hashed with the key; the server returns how many sketches it merged
  and how many it dropped for an infohash it has not stored.
- `Search` matches every word of the query against names and stored file
  paths, and filters by kind and by the probability of an audio track in one
  language. It has Jev examine a bounded number of its candidates first, so
  the filters apply to what is known once that is done. Each release carries
  the layer that decided its kind, whether Jev has examined it, its audio
  entries with their marks, its quality, season and episode, its work as
  `work`, `m348` or `tv1399`, and the work's IDs, and `tmdb`, a `TMDBWork`: what TMDB knows of the
  work its title is matched to, with the title, overview and genres in the
  request's `lang`, `ru` or `en` (`en` when empty), falling back to the other
  language and the title to the original; absent while the title was never
  looked up, with `id` 0 when nothing matched. Given one ID, `Search` returns
  the releases of that work.
- `Get` returns one release by infohash, with `tmdb` in `lang` likewise.
- `BrowseService` reads the store as the web UI shows it and classifies
  nothing first. A work in its answers is a `WorkSummary`: its ID, kind,
  `tmdb`, stored releases, distinct peers over the last 7 days and place in
  the 7-day top of its kind. A voice narrows releases by their audio in one
  language: `VOICE_MARKER` to those with a marker of it, `VOICE_LIKELY` to
  those with a marker or a probability of at least 0.5.

| RPC             | Request                                                                                                                                                                          | Response                                                                                                                                   |
| --------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------ |
| `SearchTitles`  | `query`, `kind` (movie, series or both), `lang`                                                                                                                                  | `works`: those whose titles, or whose releases' names and paths, have every word, and those TMDB finds, with releases first by peers, then the rest by TMDB popularity; `ours`, `tmdb_only` |
| `Title`         | `id`, `lang`                                                                                                                                                                     | `work`; `ranks` over 24 hours, 7 days and 30 days; `peers` of all its releases over the three; `audio` per configured language: releases marked, their marks, releases likely |
| `TitleReleases` | `id`; `quality`, `source`, `min_size`, `max_size`, `audio_language` and `voice`, `season`, `episode`; `sort` by peers, first sighting or size; `offset`, `limit`                  | `releases` with their peers over 7 days, quality, source, season, episode and audio; `total`, `matched`, `seasons`                          |
| `Torrents`      | `query`, words or an infohash; `kind` (`KIND_OTHER` takes music, books and software in) or `unknown_kind`; `matched`; `quality`; `audio_language` and `voice`; `source`, `layer`, `min_size`; `sort`; `offset`, `limit`; `lang` | `rows`, each a release with its `work` when matched, adult releases left out; `total`                                                      |
| `Card`          | `infohash`, `lang`                                                                                                                                                               | the release, with `tmdb` the state of its title's match; discovery, every stored file, peers over the day, week and month; its `work` and `rank_in_work`, its place among the work's releases by peers; its `rules`, those that recognised features in the name (kind, year, season, episode, quality) or audio markers in the name or file paths, each with the matched fragment, recomputed when the card is opened |
| `Top`           | `kind` (movie or series), `window`, `voice` and `voice_language` (the first audio language by default), `offset`, `limit`, `lang`                                                | `entries`: rank, `work`, peers, change against the window before, `audio`, sources and first sighting of the releases the voice admits; `total`, `computed_at` |
| `Stats`         | `days`                                                                                                                                                                           | counts by kind (movie, series, other, unknown; music, books and software are other), layer, source and audio language, rejections by reason, pending, tokens spent today and the day's limit, new torrents per day |
| `Home`          |                                                                                                                                                                                  | stored, new in 24 hours and pending                                                                                                        |
| `Settings`      |                                                                                                                                                                                  | the API's audio languages, BCP-47 tags in its order, empty while the audio extension is off; the web UI asks it at start                  |

`lang` is `ru` or `en`, `en` when empty: the language of `tmdb` texts, and
of TMDB's search for `SearchTitles`. A work's `id` that is not one fails
with `INVALID_ARGUMENT`, one of no stored work with `NOT_FOUND`.

## Torznab

With `SNIFFARR_TORZNAB_TOKEN` set, the API port also serves a
[Torznab](https://torznab.github.io/spec-1.3-draft/) indexer at
`/torznab/api`, for Prowlarr, Radarr and Sonarr; without it the path answers 404. Each request carries the token as its `apikey` parameter, and no bearer
token.

| `t`        | Parameters                                                        |
| ---------- | ----------------------------------------------------------------- |
| `caps`     |                                                                   |
| `search`   | `q`, `cat`, `offset`, `limit`                                     |
| `movie`    | `q`, `tmdbid`, `imdbid`, `cat`, `offset`, `limit`                 |
| `tvsearch` | `q`, `tvdbid`, `tmdbid`, `season`, `ep`, `cat`, `offset`, `limit` |

It answers through `Search`, so its candidates are classified and matched as
a search's are, and returns movies and series only: `movie` searches movies,
`tvsearch` series, `search` both, and `cat` narrows them to Movies (2000s) or
TV (5000s). An ID parameter selects the releases matched to that work,
`tmdbid` among movies for `movie` and among series for `tvsearch`; `imdbid`
takes either `tt0078748` or `78748`. `season` and `ep` are accepted and
ignored. `limit` is 100 by default and `offset` and `limit` together 500 at
most.

Each item is a release: its name, its magnet as guid, link and enclosure, its
first sighting as `pubDate`, and the attributes `category`, `size`,
`infohash`, `magneturl`, and `tmdbid`, `imdbid` (without `tt`) and `tvdbid`
when its title is matched. The category is Movies or TV, `/UHD` for a name
with `2160p`, `4K` or `UHD`, `/HD` for `1080p`, `1080i` or `720p`, `/SD`
otherwise. Items carry no seeders or peers, which the DHT does not tell;
Radarr's minimum-seeders check passes a release without them.

In Prowlarr, add an indexer of type Generic Torznab with URL
`http://<host>:8080/torznab`, API path `/api`, and the token as API key.

## Web UI

With `-ui`, sniffarr serves a web UI on `SNIFFARR_UI_ADDR`, in English or
Russian. The UI touches no database: it reads `BrowseService` with
`SNIFFARR_SEARCH_TOKEN`, in-process when `-api` is on and over the network at
`SNIFFARR_URL` otherwise. At start it asks `Settings` for the API's audio
languages, retrying every 5 seconds until the API answers, and listens only
then; the languages are kept for the life of the process, so a change of the
API's needs the UI restarted.

| Route               | Page                                                                                                                                                                                                                                                                                                                                                                                                                                 |
| ------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `GET /`             | Without `q`, the home page: the search box, links to the tops, how many releases are stored, how many were first seen in the last 24 h, and how many await the classifier                                                                                                                                                                                                                                                             |
| `GET /?q=…`         | The movies and series whose titles or releases match `q`, in the store and on TMDB: those with releases first, most peers over 7 days first, then those found on TMDB only, most popular first; `kind` is `movie` or `series`, both without it                                                                                                                                                                                       |
| `GET /w/{id}`       | A movie or series, `id` `m348` or `tv1399`: what TMDB knows of it, its places in the tops over 24 h, 7 days and 30 days, its releases and peers, the audio of its releases, and its releases under `quality` (`2160p`, `1080p`, `720p`, `sd`), `source` (`web`, `rip`, `remux`, `disc`), `min` and `max` in GB, `vl` and `voice`, a series' `season` and `episode`, sorted by `sort` (`peers`, `first_seen`, `size`), 50 a page from `o` |
| `GET /torrents`     | Every release, matched or not, by name or infohash in `q`, under `kind` (`movie`, `series`, `other`, `unknown`), `matched` (`yes`, `no`), `quality`, `vl` and `voice`, `source`, `layer`, `min` in GB, sorted by `sort` (`peers`, `last_seen`, `size`), 50 a page from `o`; a matched release links to its movie or series                                                                                                             |
| `GET /top`          | The 100 movies or series with the most peers over a window: `kind` is `movie` or `series`, `win` is `24h`, `7d` or `30d`, `voice` is `any`, `p50` or `explicit` in the audio language `vl`, narrowing each to its releases with a marker or a probability of at least 0.5, or to those with a marker; the first 25 until `all=1`. A row leads to its movie's or series' page                                                       |
| `GET /top/w/{id}`   | A permanent redirect to `/w/{id}`                                                                                                                                                                                                                                                                                                                                                                                                    |
| `GET /t/{infohash}` | A release: its magnet link, peers over the last day, 7 days and 30 days, its movie or series with its place in the top and the release's place among its releases, or the state of its title's lookup with TMDB and IMDb searches while it is unmatched, classification, audio signs per language with their marks, and the first 8 stored files, every one with `files=all`                                                          |
| `GET /data`         | The store's counts: kinds, layers, sources, audio per language, rejections by reason, the classifier's pending torrents and tokens used, and new torrents per day and per week                                                                                                                                                                                                                                                       |
| `GET /static/…`     | Stylesheet, scripts and the TMDB logo                                                                                                                                                                                                                                                                                                                                                                                                |

A parameter value outside those listed is the default: every kind, quality,
source and layer, the audio language of the page's language when it is
configured and else the first, `voice=any`, `sort=peers`, `kind=movie` for
the top, `win=7d`. A fragment of a page is served alone when htmx asks for it
with `HX-Request: true` and `HX-Target` naming it: `results`, `releases`,
`torrents`, `top`, `files` or `data`.

The UI authenticates nobody: an authenticating proxy, such as oauth2-proxy,
goes in front of it. Its language is the one picked with `?lang=ru` or
`?lang=en`, on any page, remembered in a cookie for a year, else, with
`SNIFFARR_OIDC_ISSUER` set, the `locale` claim of the provider's userinfo for
the access token in the `X-Forwarded-Access-Token` header, which oauth2-proxy
can pass on; a locale is kept for 10 minutes per token. Adult releases do not
show in it.

The chart serves the UI with `ui.enabled`, behind the Service `<release>-ui`;
see [Helm](#helm).

## Configuration

Each component reads its own variables, listed below, and only when it is on.
A variable several components read, such as a token, is read once and means
the same to each.

### Every component

| Flag or variable     | Default | Meaning                                                                                                             |
| -------------------- | ------- | ------------------------------------------------------------------------------------------------------------------- |
| `-api`               | off     | Serve the API                                                                                                       |
| `-ui`                | off     | Serve the web UI                                                                                                    |
| `-sniff`             | off     | Walk the DHT and report infohashes                                                                                  |
| `-fetch`             | off     | Take fetch tasks and return metadata                                                                                |
| `-log-level`         | `info`  | Level to log at: `debug`, `info`, `warn` or `error`                                                                 |
| `SNIFFARR_LOG_LEVEL` |         | The level when `-log-level` is not given; the flag wins when both are set                                           |
| `SNIFFARR_URL`       |         | Base URL of the API, such as `http://sniffarr-api:8080`; required by `-ui`, `-sniff` and `-fetch` without `-api`, and must be unset with it |

### `-api`

| Variable                           | Default          | Meaning                                                                                                                |
| ---------------------------------- | ---------------- | ---------------------------------------------------------------------------------------------------------------------- |
| `SNIFFARR_DB`                      |                  | Path of the SQLite database; required                                                                                  |
| `SNIFFARR_INGEST_TOKEN`            |                  | Bearer token of `IngestService`; required                                                                              |
| `SNIFFARR_SEARCH_TOKEN`            |                  | Bearer token of `SearchService` and `BrowseService`; required                                                          |
| `SNIFFARR_ADDR`                    | `:8080`          | Listen address                                                                                                         |
| `TYPESAFE_API_KEY`                 |                  | TypeSafe API key; when unset, only rules and reuse classify                                                            |
| `SNIFFARR_CLASSIFY_CONCURRENCY`    | `4`              | Background classification requests at once                                                                             |
| `SNIFFARR_CLASSIFY_DAILY_TOKENS`   | `2000000`        | Classifier tokens per UTC day at most; `0` is no limit                                                                 |
| `SNIFFARR_CLASSIFY_SEARCH_RESERVE` | `25`             | Percentage of the daily tokens background classification leaves for searches, `0` to `100`                             |
| `SNIFFARR_SEARCH_CLASSIFY_MAX`     | `20`             | Candidates one search has classified at most                                                                           |
| `SNIFFARR_SEARCH_CLASSIFY_WAIT`    | `2s`             | How long a search waits for them                                                                                       |
| `SNIFFARR_FETCH_LEASE`             | `1m`             | How long a fetcher may hold a task                                                                                     |
| `SNIFFARR_FETCH_ATTEMPTS`          | `3`              | Timed-out fetches after which an infohash is dropped                                                                   |
| `SNIFFARR_FETCH_BACKOFF`           | `10m`            | Wait before retrying a timed-out task, doubled per timeout                                                             |
| `SNIFFARR_QUEUE_MAX_SIZE`          | `1000000`        | Infohashes the queue keeps                                                                                             |
| `SNIFFARR_QUEUE_MAX_AGE`           | `24h`            | How long an infohash may wait in the queue                                                                             |
| `SNIFFARR_QUEUE_DEAD_FOR`          | `168h`           | How long an evicted infohash, or one dropped after its last attempt, stays out                                         |
| `SNIFFARR_QUEUE_NO_PEERS_FOR`      | `24h`            | How long an infohash dropped for having no peers stays out                                                             |
| `SNIFFARR_STORE_MAX_FILES`         | `20`             | Files stored per torrent, the largest ones                                                                             |
| `SNIFFARR_RETENTION`               | `2160h`          | How long a stored torrent is kept without being seen                                                                   |
| `SNIFFARR_SCRIPTS`                 | `Latin,Cyrillic` | Scripts, by Go `unicode.Scripts` name and comma-separated, whose letters a torrent name may have; empty is the default |
| `SNIFFARR_AUDIO_LANGUAGES`         |                  | Audio languages, BCP-47 tags and comma-separated; unset or empty leaves the extension off. An unknown tag fails startup |
| `TMDB_API_KEY`                     | built-in token   | TMDB API key or read access token; set empty, no titles are matched                                                    |
| `SNIFFARR_TMDB_CONCURRENCY`        | `2`              | Background title matches at once                                                                                       |
| `SNIFFARR_TMDB_RATE`               | `20`             | TMDB requests a second at most                                                                                         |
| `SNIFFARR_TMDB_RETRY`              | `168h`           | How long a title key that matched nothing waits before it is looked up again                                           |
| `SNIFFARR_SEARCH_TMDB_MAX`         | `5`              | Title keys one search has matched at most                                                                              |
| `SNIFFARR_TORZNAB_TOKEN`           |                  | API key of the Torznab endpoint; when unset, there is none                                                             |

The database runs in WAL mode and its schema migrates on start; leases left
over from a previous run are freed on start. A torrent keeps its true file
count, but only its largest files are stored and searched. Once a minute the
API deletes, in small batches, the torrents nobody has sighted within the
retention period, with their peer sketches; a later sighting queues them
again. Once an hour it unions the hour sketches older than 8 days into day
sketches and deletes the day sketches older than 65 days, and every 10
minutes, and once at start, it recomputes the tops of works and every
torrent's peers over the last 7 days.

### `-ui`

| Variable                   | Default | Meaning                                                                              |
| -------------------------- | ------- | ------------------------------------------------------------------------------------ |
| `SNIFFARR_SEARCH_TOKEN`    |         | Bearer token of `BrowseService`; required                                            |
| `SNIFFARR_UI_ADDR`         | `:8081` | Listen address                                                                       |
| `SNIFFARR_OIDC_ISSUER`     |         | OpenID Connect issuer whose userinfo gives the UI a person's locale; empty asks none |

### `-sniff` and `-fetch`

| Variable                | Meaning                                   |
| ----------------------- | ----------------------------------------- |
| `SNIFFARR_INGEST_TOKEN` | Bearer token of `IngestService`; required |

| Flag             | Default | Meaning                                                |
| ---------------- | ------- | ------------------------------------------------------ |
| `-port`          | `42069` | Port for peers and the DHT, TCP and UDP; `0` picks one |
| `-upnp`          | `false` | Forward the port, TCP and UDP, by UPnP                 |
| `-stun`          | `stun.l.google.com:19302` | STUN server asked for the node's public address, for BEP 42; empty asks none |
| `-fetchers`      | `2000`  | Metadata fetches at once                               |
| `-rate`          | `2000`  | DHT queries, and new peer connections, a second        |
| `-fetch-timeout` | `15s`   | How long to wait for metadata once a torrent has peers |
| `-peer-wait`     | `8s`    | How long to look for a torrent's peers on the DHT      |
| `-min-peers`     | `1`     | Peers needed within `-peer-wait` to fetch the metadata |
| `-walkers`       | `16`    | `sample_infohashes` queries at once                    |

With `-upnp` the node asks the gateways it finds on the local network to
forward the port it listens on, TCP and UDP. Without `-sniff` the node is
passive by BEP 43: it answers no DHT query and asks other nodes to keep it out
of their routing tables.

With `-sniff` the node answers `ping`, `find_node`, `get_peers` and
`announce_peer`, and holds the peers announced to it in memory: one per IP
and infohash, served in `get_peers` for 30 minutes after the announce, at most
50 a reply, up to 100 peers of each of up to 200,000 infohashes, the oldest
evicted first. Nothing of it is persisted. It answers BEP 51
`sample_infohashes` with up to 20 of the infohashes it holds, their count,
the nodes nearest the target and an interval of 1800 seconds, one reply per
source every 10 seconds; and a `get_peers` with `scrape` set gets the BEP 33
`BFsd` and `BFpe` bloom filters of the seeds and the other peers it holds for
the infohash, a peer being a seed when its `announce_peer` said `seed`.

The node's ID comes from the API's `HashKey` with the hash key: the API
generates 20 random bytes once and keeps them in its store, so the node keeps
its place in other nodes' routing tables across restarts. The node waits for
the ID before it starts, retrying while the API is unreachable and warning
at every attempt after the first. Then it asks the `-stun` server for its
public address, with 5 seconds to answer, and the ID takes the BEP 42 prefix
of that address, so other nodes that check it accept the node; the rest of
the ID stays, so the same address gives the same ID, and a new address a new
one. When the server does not answer the ID is used as stored, with a warning,
and so it is when `-stun` is empty; nodes enforcing BEP 42 may not keep it. The
passive node of `-fetch` alone takes a random ID at each start: it answers
nothing and is in no routing table.

Both components count the peers they see in sketches of the current UTC hour
and send them to the API's `Peers` every 3 minutes, and at once when 100,000
sketches wait; a send that fails is retried, and while the API is unreachable
the oldest unsent batch makes room for new sketches. `-sniff` counts an
announcing peer only for an infohash `Sight` has reported as a stored torrent,
remembering up to 1,000,000 of them, so a torrent's first announce before that
answer is not counted; `-fetch` counts the peers of every torrent it fetched,
and the API drops the sketches of those it rejected. The key comes from the
API's `HashKey` at start, and peers seen before it arrives are not counted.

Discovery rests on `sample_infohashes`: in a measured run on a server with a
public address, no `announce_peer` query reached the node, and the sniffer
still fetched metadata for about 22 torrents a second. Behind NAT it fetches
far fewer, a few a second at most.

## Telemetry

sniffarr exports traces and metrics with the OpenTelemetry SDK over OTLP,
configured only by the standard variables: `OTEL_EXPORTER_OTLP_ENDPOINT` and
the rest of `OTEL_EXPORTER_OTLP_*`, `OTEL_TRACES_EXPORTER`,
`OTEL_METRICS_EXPORTER`, `OTEL_SERVICE_NAME` (`sniffarr` when unset),
`OTEL_RESOURCE_ATTRIBUTES`, `OTEL_TRACES_SAMPLER` and
`OTEL_TRACES_SAMPLER_ARG`. Without an endpoint the exporters go to the
specification's default on localhost. `OTEL_SDK_DISABLED=true` turns
telemetry off: no exporter is created and nothing connects anywhere.

Every RPC is traced and measured on both ends by
[otelconnect](https://github.com/connectrpc/otelconnect-go). Logs stay JSON on
stderr and carry `trace_id` and `span_id` when written inside a span. No
BitTorrent peer address appears in spans, attributes or logs.

| Component  | Metric                                 | Unit         | Attributes                                 |
| ---------- | -------------------------------------- | ------------ | ------------------------------------------ |
| `-api`     | `sniffarr.ingest.sightings`            | `{sighting}` | `sniffarr.sighting.result`                 |
| `-api`     | `sniffarr.queue.depth`                 | `{infohash}` | `sniffarr.discovery`                       |
| `-api`     | `sniffarr.queue.leases`                | `{lease}`    |                                            |
| `-api`     | `sniffarr.queue.lease_expiries`        | `{lease}`    |                                            |
| `-api`     | `sniffarr.queue.evictions`             | `{infohash}` | `sniffarr.queue.eviction`                  |
| `-api`     | `sniffarr.queue.dead`                  | `{infohash}` |                                            |
| `-api`     | `sniffarr.store.retention.deletions`   | `{torrent}`  |                                            |
| `-api`     | `sniffarr.ingest.results`              | `{result}`   | `sniffarr.fetch.outcome`                   |
| `-api`     | `sniffarr.ingest.rejections`           | `{torrent}`  | `sniffarr.rules.reason`                    |
| `-api`     | `sniffarr.ingest.stored`               | `{torrent}`  |                                            |
| `-api`     | `sniffarr.classify.classifications`    | `{torrent}`  | `sniffarr.kind`, `sniffarr.classify.layer` |
| `-api`     | `sniffarr.search.lazy.duration`        | `s`          |                                            |
| `-api`     | `sniffarr.search.lazy.classifications` | `{torrent}`  | `sniffarr.search.lazy.result`              |
| `-api`     | `sniffarr.classify.tokens`             | `{token}`    |                                            |
| `-api`     | `sniffarr.classify.errors`             | `{error}`    | `sniffarr.classify.error`                  |
| `-api`     | `sniffarr.tmdb.matches`                | `{title}`    | `sniffarr.tmdb.layer`                      |
| `-api`     | `sniffarr.tmdb.requests`               | `{request}`  | `sniffarr.tmdb.status`                     |
| `-api`     | `sniffarr.search.lazy.titles`          | `{title}`    | `sniffarr.search.lazy.result`              |
| `-sniff`, `-fetch` | `sniffarr.dht.nodes`                   | `{node}`     |                                            |
| `-sniff`, `-fetch` | `sniffarr.dht.sample_answers`          | `{answer}`   |                                            |
| `-sniff`   | `sniffarr.dht.replies`                 | `{reply}`    | `sniffarr.dht.query`                       |
| `-sniff`   | `sniffarr.dht.infohashes`              | `{infohash}` |                                            |
| `-sniff`   | `sniffarr.dht.peers`                   | `{peer}`     |                                            |
| `-sniff`   | `sniffarr.sniff.sightings`             | `{infohash}` | `sniffarr.discovery`                       |
| `-fetch`   | `sniffarr.sniff.fetch.in_flight`       | `{task}`     |                                            |
| `-fetch`   | `sniffarr.sniff.fetch.outcomes`        | `{task}`     | `sniffarr.fetch.outcome`                   |
| `-fetch`   | `sniffarr.sniff.fetch.duration`        | `s`          | `sniffarr.fetch.outcome`                   |

`sniffarr.fetch.outcome` is `torrent`, `no_peers` or `timeout`;
`sniffarr.discovery` is `announce` or `sample`; `sniffarr.dht.query` is
`sample_infohashes` or `scrape`; `sniffarr.sighting.result` is
`new` or `known`; `sniffarr.classify.error` is `permanent` or `transient`;
`sniffarr.queue.eviction` is `age`, `size` or `no_peers`;
`sniffarr.classify.layer` is `rules`, `reuse` or `jev`;
`sniffarr.search.lazy.result` is `in_time`, `late` or `failed`;
`sniffarr.tmdb.layer` is `tmdb`, `jev` or `none`; `sniffarr.tmdb.status` is
the HTTP status of TMDB's answer, or `error` without one.

## Container image

The image's entrypoint is `/sniffarr`, so the container's arguments are the
component flags.

```shell
docker build -t sniffarr .
docker run -e SNIFFARR_DB=/data/sniffarr.db -e SNIFFARR_INGEST_TOKEN=… -e SNIFFARR_SEARCH_TOKEN=… sniffarr -api -ui
```

## Helm

Each `vX.Y.Z` tag publishes the image as `ghcr.io/mikluko/sniffarr:X.Y.Z`
and the chart in `charts/sniffarr` at the same version, without the `v`.

```shell
helm install sniffarr oci://ghcr.io/mikluko/charts/sniffarr --version X.Y.Z \
  --set api.existingSecret=sniffarr
```

`api.existingSecret` is required: the name of a Secret holding
`SNIFFARR_INGEST_TOKEN` and `SNIFFARR_SEARCH_TOKEN`, and optionally
`TYPESAFE_API_KEY`, which the api pod takes as its environment. The ui, sniff
and fetch pods read their token from the same Secret unless
`ui.existingSecret`, `sniff.existingSecret` or `fetch.existingSecret` names
another. With `api.litestream.enabled`, `api.litestream.existingConfigMap` is
required too. Every value is described in
[`values.yaml`](charts/sniffarr/values.yaml).

The chart installs in one of two modes, both of the one image.

### A Deployment per component

By default each component is a Deployment of its own, reaching the API at the
Service `<release>-api`. `<release>-api` runs `sniffarr -api` behind that
Service. `<release>-sniff` runs `sniffarr -sniff`, `sniff.discover` and
`sniff.fetch` rendering `-sniff` and `-fetch`, in the host's network by default
so that `announce_peer` queries reach its DHT port. `<release>-fetch` runs
`sniffarr -fetch`, in the pod network by default, with `fetch.replicas`
replicas and the URL and token Secret of `sniff` unless its own are set. A
fetcher's DHT node is passive and the fetcher dials the peers it fetches from,
so it needs no open port.

With `ui.enabled`, `<release>-ui` runs `sniffarr -ui`, with `ui.replicas`
replicas, behind the Service `<release>-ui` on `ui.service.port` (8081).
`ui.addr` is `SNIFFARR_UI_ADDR`, and `ui.env` carries the rest of the UI's
variables, `SNIFFARR_OIDC_ISSUER`. `ui.extraContainers` and `ui.extraVolumes` add an
authenticating proxy to the pod, and `ui.service.targetPort` names its port.
With `ui.addr` on a loopback address, such as `127.0.0.1:8081`, the UI is
reachable only through that proxy, and `ui.service.targetPort` must name it.

Either DHT Deployment reaches the DHT from the pod network through
`<component>.service`: a Service of the component's name exposing its port,
TCP and UDP, `NodePort` by default. With `service.nodePort` the pod listens on
that port, rendered as `-port`, so the port peers reach the node on is the
port it uses; `flags.port` must then be unset or equal. `<component>.upnp`
renders `-upnp`.

### One process

With `single.enabled`, the chart renders one Deployment, `<release>`, in place
of the component Deployments. Its container runs `sniffarr -api`, and `-ui`,
`-sniff` and `-fetch` for each of `ui`, `sniff` and `fetch` with `enabled`
true, `sniff.discover` and `sniff.fetch` still deciding the sniff component's
flags. The Services `<release>-api` and, with `ui.enabled`, `<release>-ui`
select that pod. The components keep their values and the pod takes the union
of them: `api.persistence`, `api.litestream`, `api.existingSecret`, the `env`
of every enabled component, `ui.addr`, the `flags` and `upnp` of `sniff` and
`fetch`, their `hostNetwork`, and the `extraContainers` and `extraVolumes` of
`api` and `ui`. The pod's `resources`, `podAnnotations`, `podLabels`,
`nodeSelector`, `tolerations` and `affinity` come from the `single` block; the
components' own are not read. With `sniff.hostNetwork`, every listener binds on
the host, the API's and the UI's too.

One process has one DHT node, so `sniff.service` or `fetch.service`, not both,
exposes it as the Service `<release>-dht`. The chart refuses values the one
process cannot honour: `single.replicas` other than 1, since the process owns
the SQLite database; `ui.url`, `sniff.url` or `fetch.url`, since the clients
reach the API in-process and the binary refuses `SNIFFARR_URL` with `-api`;
`ui.existingSecret`, `sniff.existingSecret` or `fetch.existingSecret`, since
the process reads each token once, from `api.existingSecret`; and one `env` or
`flags` key set to different values by two components.

## Generating code

The generated code under `internal/apis` is committed. After changing a `.proto` file,
regenerate it with [buf](https://buf.build); the protoc plugins come from the
Go tool dependencies in `go.mod`.

```shell
buf lint
go generate ./...
```

## Releases

`CHANGELOG.md` decides the version. To release, rename `## [Unreleased]` to
`## [X.Y.Z] - YYYY-MM-DD`, open a fresh `## [Unreleased]` above it, and merge
on the date you wrote. `mikluko/action-changelog` validates the file on every
pull request.

On a push to `main`, `release-cut.yaml` runs CI, then tags the commit `vX.Y.Z`
as the GitHub App when the newest entry names a version no tag carries. The tag
starts `release-roll.yaml`, which fails unless the tag is the newest entry of
`CHANGELOG.md` at that commit, then builds and pushes the image
`ghcr.io/mikluko/sniffarr:X.Y.Z` and the chart
`oci://ghcr.io/mikluko/charts/sniffarr` at `X.Y.Z`, and creates the GitHub
release from the entry last. A tag whose `release-roll.yaml` run failed is not
cut again: re-run that run. Run by hand, `release-roll.yaml` builds the newest
version, a prerelease included, and publishes nothing. Nothing else sets a
version.

Renovate opens its pull requests labelled `bot`, three days after the upstream
release, per `.github/renovate.json`. Once a day, `upgrade-agent.yaml` runs
goose on Kimi K3 through `.github/upgrade-agent.md`: it stacks the open `bot`
pull requests whose checks passed, runs the CI checks on the stack, and merges
it. When the stack changes what ships, a module in `go.mod`, the `Dockerfile`
or the chart, it cuts the version in `CHANGELOG.md` on a `release/vX.Y.Z`
branch on top, so the merge releases. It merges no pull request without the
`bot` label, none whose checks failed, and none that conflicts outside `go.mod`,
`go.sum` and `[Unreleased]` or breaks the build in a way that needs a judgement
about behaviour; those it leaves open with a comment saying why. Run by hand
with `dry_run`, it opens the stack's pull requests and merges nothing.

### Setting up

Once, a human:

- installs a GitHub App on the repository, sets the repository variable
  `APP_CLIENT_ID` to its client id and the secret `APP_PRIVATE_KEY` to a
  private key of it. `release-cut.yaml` tags as the app so that the tag starts
  `release-roll.yaml`; `upgrade-agent.yaml` pushes, comments and merges as it.
- sets the secret `MOONSHOT_API_KEY` to a Kimi Code key.
- installs the Renovate GitHub App on the repository. It reads
  `.github/renovate.json`.

## License

MIT; see [LICENSE](LICENSE).
