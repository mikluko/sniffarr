# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/2.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.14.1] - 2026-10-21

### Fixed

- `-fetch` no longer crashes a few times an hour: the BitTorrent and DHT libraries are back at their released versions, v1.61.0 and v2.24.0, whose `sample_infohashes` reply the node frames itself.

## [0.14.0] - 2026-10-20

### Changed

- Among several TMDB candidates that fit a title, the one with a popularity of at least 20 and ten times every other's is the match, under layer `tmdb` with confidence 0.8; the classifier is asked only when none stands out, and it is shown each candidate's language, countries, popularity and votes.
- A title key that matched nothing is looked up again after 7 days by default, `SNIFFARR_TMDB_RETRY` `168h`, in place of 30.

## [0.13.0] - 2026-10-20

### Added

- A matched title's TMDB details: type, original title, poster path, rating and votes, runtime, a series' status and seasons, countries and language in `tmdb_works`, and its title, overview and genres in Russian and English in the table `tmdb_texts`; a migration adds them. `Release`, `Work` and `WorkMatch` carry `tmdb`, a `TMDBWork`, with the texts in the request's `lang`, `ru` or `en`, which `SearchRequest`, `GetRequest`, `PageRequest`, `CardRequest`, `TopRequest` and `WorkRequest` take; a title looked up and not found is a `TMDBWork` with `id` 0.
- The TMDB worker backfills the details of matches that lack them, oldest match first, under `SNIFFARR_TMDB_RATE` and `SNIFFARR_TMDB_CONCURRENCY`.
- The web UI shows the match on a torrent's card, in the rows of the top and in the block over search results, with posters from `image.tmdb.org`, links to TMDB and IMDb, and the method, confidence and date of the match; an unmatched title gets TMDB and IMDb searches.
- The TMDB logo and the notice "This product uses the TMDB API but is not endorsed or certified by TMDB." at the foot of every page of the web UI.

### Changed

- The block over search results is "Titles" (Russian «Названия»); the top's caption no longer names works; the card's "work" row and "look up" links are the TMDB block.
- A match's IMDb, TVDB and Wikidata IDs come with the work's page, so a match costs two requests past its searches instead of one, and a backfilled match two; a page TMDB refuses to serve leaves the match with empty details instead of recording a failed match.

## [0.12.0] - 2026-10-20

### Changed

- The tables are named after their module: `torrents` → `torrent_releases`, `torrents_fts` → `torrent_releases_fts`, `files` → `torrent_files`, `audio` → `torrent_audio`, `queue` → `ingest_queue`, `dead` → `ingest_dead`, `rejected` → `ingest_rejected`, `token_usage` → `ingest_token_usage`, `peers_hour` → `stat_peers_by_hour`, `peers_day` → `stat_peers_by_day`, `top` → `stat_top`; `tmdb_works`, `tmdb_ids`, `settings` and `schema_migrations` keep their names. A migration renames them with their indexes, and the index `title_ids_id` becomes `tmdb_ids_id`. The queue metrics report the instrumentation scope `github.com/mikluko/sniffarr/internal/store`.

## [0.11.0] - 2026-10-20

### Changed

- **Breaking:** The titles extension is the TMDB extension. `SNIFFARR_TITLES_CONCURRENCY`, `SNIFFARR_TITLES_RATE`, `SNIFFARR_TITLES_RETRY` and `SNIFFARR_SEARCH_TITLES_MAX` are `SNIFFARR_TMDB_CONCURRENCY`, `SNIFFARR_TMDB_RATE`, `SNIFFARR_TMDB_RETRY` and `SNIFFARR_SEARCH_TMDB_MAX`. A release carries `tmdb_layer` and `tmdb_confidence` in place of `title_layer` and `title_confidence`, and the enum is `TMDBLayer` with `TMDB_LAYER_UNSPECIFIED`, `TMDB_LAYER_SEARCH` and `TMDB_LAYER_CLASSIFIER`. The metrics `sniffarr.titles.matches` and `sniffarr.titles.requests` are `sniffarr.tmdb.matches` and `sniffarr.tmdb.requests`, with the attributes `sniffarr.tmdb.layer` and `sniffarr.tmdb.status`. The tables `titles` and `title_ids` are `tmdb_works` and `tmdb_ids`; a migration renames them.

## [0.10.0] - 2026-10-20

### Changed

- **Breaking:** `-public-ip` is gone. `-sniff` asks a STUN server for its public address at start, `-stun`, default `stun.l.google.com:19302`; the node ID takes the BEP 42 prefix of the answer, and is used as stored when the server does not answer within 5 seconds or `-stun` is empty.

### Removed

- The line under the charts of the data page that pointed at Grafana for the rest of the telemetry.

## [0.9.0] - 2026-10-20

### Added

- The DHT node of `-sniff` serves the peers announced to it: `get_peers` replies carry up to 50 peers announced within the last 30 minutes, held in memory only, up to 100 per infohash and 200,000 infohashes. Metrics `sniffarr.dht.infohashes` and `sniffarr.dht.peers`.
- The node answers BEP 51 `sample_infohashes` with up to 20 of the infohashes it holds, their count, the nodes nearest the target and an interval of 1800 seconds, one reply per source every 10 seconds. Metric `sniffarr.dht.replies`.
- The node answers a `get_peers` with `scrape` by BEP 33: the reply carries the `BFsd` and `BFpe` bloom filters of the seeds and the other peers held for the infohash, the `seed` flag of `announce_peer` telling them apart.
- The node's ID is kept across restarts: `IngestService.HashKey` returns `node_id`, 20 bytes the API generates once into its store, and `-sniff` waits for it before the node starts. `-public-ip` gives the ID the BEP 42 prefix of the node's public address.

## [0.8.0] - 2026-10-20

### Changed

- A torrent's card links to TMDB and IMDb: the work's pages when its IDs are known, else searches for its title.
- The work named on a torrent's card and in search results links to that work in the top with its releases open, in place of the top of its kind.

## [0.7.1] - 2026-10-20

### Fixed

- A torrent stored by `-api` gets its `work_key`, `year` and `season` at insertion; a stored torrent that has none takes them at the next start.
- The DHT node of `-sniff` puts a token in its `get_peers` replies, so it receives `announce_peer`: torrents are discovered with `SOURCE_ANNOUNCE` and their announcing peers are counted. It still serves no peers.

## [0.7.0] - 2026-10-20

### Added

- Peers per torrent, as HyperLogLog sketches and nothing else: `-sniff` counts the senders of `announce_peer` and `-fetch` the peers it meets, for stored torrents only, and both send their sketches to the API with `IngestService.Peers` every 3 minutes, hashing each `IP:port` with the key `IngestService.HashKey` gives. The store keeps hourly sketches for 8 days and daily ones for 65.
- Works: `work_key`, `year` and `season` on every torrent; a movie or an album is its title and year, a series its title and season.
- The top of works, `BrowseService.Top` and `Work`, recomputed every 10 minutes for `movie`, `series` and `music` over the windows `24h`, `7d` and `30d` and the voice filters `any`, `p50` and `explicit`, each entry with its peers, releases and change against the previous window.
- `BrowseService.Home`; `Page` takes `sort` (`peers`, `last_seen`, `size`) and lists matching `works`; `Card` carries `peers` over three windows, the torrent's `work`, its `releases` and `release_rank`; `Release` carries `peers_week`.
- `SightResponse.stored` says, per infohash sent, whether the torrent is stored.
- Web UI: a home page at `/` with a summary, the top at `/top` and `/top/w/{id}`, a works block and a 7-day peers column in search results sorted by `sort`, peers over three windows and the work on a torrent's card, and `?lang=ru|en` remembered in a cookie for a year.
- Metrics `sniffarr.ingest.sketches`, `sniffarr.sniff.peers` and `sniffarr.sniff.sketches`.

### Changed

- The search page does not poll for new torrents; the data page still does.
- The web UI's English locale is `en`; `C` and `POSIX` resolve to it.

## [0.6.0] - 2026-10-20

### Added

- `BrowseService`, behind the search token: `Page` lists releases with the UI's filters, `Card` gives one release with its files, `Stats` gives the counts of the data page.
- The chart's `ui` component: Deployment and Service `<release>-ui` running `sniffarr -ui`, with `ui.service.targetPort` naming a port of a proxy in `ui.extraContainers` and `SNIFFARR_OIDC_ISSUER` set through `ui.env`.
- The chart's `single.enabled`: one Deployment `<release>` running `-api` with every enabled component of `ui`, `sniff` and `fetch`, with `single.replicas` fixed at 1.

### Changed

- **Breaking:** one binary, `sniffarr`, whose components are the flags `-api`, `-ui`, `-sniff` and `-fetch`; `sniffapi` and `sniffdht` are gone, and the image's entrypoint is `sniffarr`. Every environment variable and DHT flag keeps its name.
- **Breaking:** `-discover` is `-sniff`; the web UI is served by `-ui`, and `SNIFFARR_UI_ADDR` defaults to `:8081`.
- The web UI reads through `BrowseService`: in a process with `-api` over an in-memory connection, else at `SNIFFARR_URL` with `SNIFFARR_SEARCH_TOKEN`. `SNIFFARR_URL` set in a process with `-api` is refused.
- `-log-level` given wins over `SNIFFARR_LOG_LEVEL`.
- Every component reports telemetry as the service `sniffarr` unless `OTEL_SERVICE_NAME` says otherwise.
- The chart's containers are named `api`, `sniff` and `fetch`.

### Removed

- **Breaking:** the chart's `api.ui`; `ui` replaces it, and a values file still setting `api.ui` fails to render.

## [0.5.0] - 2026-10-20

### Added

- `sniffdht -upnp` forwards the listening port, TCP and UDP, by UPnP.
- The chart's `sniff.service` and `fetch.service` expose the DHT port, TCP and UDP, as a Service of type `ClusterIP`, `NodePort` or `LoadBalancer`; with `nodePort` set, the pod listens on that port.
- The chart's `sniff.upnp` and `fetch.upnp` pass `-upnp`.

### Changed

- **Breaking:** a discovering node forwards its port only with `-upnp`; `-discover` alone forwards none.

## [0.4.0] - 2026-10-20

### Added

- The Torznab endpoint `/torznab/api`, with `SNIFFARR_TORZNAB_TOKEN` as its API key: `caps`, `search`, `movie` by `tmdbid` or `imdbid`, and `tvsearch` by `tvdbid` or `tmdbid`; each release carries `tmdbid`, `tvdbid` and `imdbid` attributes and a magnet link.

## [0.3.0] - 2026-10-20

### Added

- Title keys are matched to TMDB works, in the background and on demand; a release carries `ids` by provider (`tmdb`, `imdb`, `tvdb`, `wikidata`) and `title_confidence`, and a search filters by one `id`.
- `TMDB_API_KEY` replaces the built-in TMDB token; set empty, no titles are matched. `SNIFFARR_TITLES_CONCURRENCY`, `SNIFFARR_TITLES_RATE`, `SNIFFARR_TITLES_RETRY` and `SNIFFARR_SEARCH_TITLES_MAX` tune the matching.
- The web UI on `SNIFFARR_UI_ADDR`: search, release cards and a data page, in English and Russian; `SNIFFARR_OIDC_ISSUER` gives it a person's locale from the provider's userinfo.
- The chart's `api.ui` serves the UI, with `api.extraContainers` and `api.extraVolumes` for a proxy in front.

## [0.2.0] - 2026-10-20

### Added

- Audio languages as an extension: `SNIFFARR_AUDIO_LANGUAGES`, `ru` by default, names the languages judged; a release carries `audio` per language with `probability`, `marker` and `script_hint`, and a search filters by `audio_language` and `min_audio_probability`.

### Removed

- **Breaking:** `russian_audio` on a release and the `min_russian_audio` search filter; `audio` with language `ru` replaces them.

## [0.1.4] - 2026-10-20

### Changed

- **Breaking:** the chart's components are `api`, `sniff` and `fetch`, in values and in Deployment names, in place of `sniffapi` and `sniffdht`.

## [0.1.3] - 2026-10-20

### Added

- `sniffdht -discover` and `-fetch` choose what a node does; a node that does not discover is passive on the DHT, and `-port 0` picks a free port.
- The chart runs discovery and fetching as two Deployments, fetching in the pod network.

## [0.1.2] - 2026-10-20

### Added

- `SNIFFARR_LOG_LEVEL` and `sniffdht -log-level`; at `debug`, a line is logged per fetched torrent.

### Fixed

- `sniffdht` writes no torrent data to its working directory and needs no data directory.

## [0.1.1] - 2026-10-20

### Added

- `sniffdht -min-peers`, the peers a torrent needs within `-peer-wait` before its metadata is fetched.
- `SNIFFARR_QUEUE_NO_PEERS_FOR`, how long an infohash dropped for having no peers stays out.

### Changed

- An infohash whose torrent has no peers is dropped at once and revived by an announce; `SNIFFARR_FETCH_ATTEMPTS` and `SNIFFARR_FETCH_BACKOFF` count timeouts only.

### Fixed

- The chart installs when its version carries build metadata after a `+`.

## [0.1.0] - 2026-10-20

### Added

- `sniffapi`: Connect `IngestService` and `SearchService` over h2c with bearer tokens, on an SQLite database with full-text search.
- `sniffdht`: a DHT crawler that fetches the metadata of unknown torrents and submits it.
- Layered classification: rules, reuse by title key, then the Jev classifier within a daily token budget; a torrent whose name has letters outside `SNIFFARR_SCRIPTS` is rejected.
- OpenTelemetry traces and metrics over OTLP.
- A container image with both binaries, and a Helm chart at `oci://ghcr.io/mikluko/charts/sniffarr`.

[Unreleased]: https://github.com/mikluko/sniffarr/compare/v0.14.1...HEAD
[0.14.1]: https://github.com/mikluko/sniffarr/compare/v0.14.0...v0.14.1
[0.14.0]: https://github.com/mikluko/sniffarr/compare/v0.13.0...v0.14.0
[0.13.0]: https://github.com/mikluko/sniffarr/compare/v0.12.0...v0.13.0
[0.12.0]: https://github.com/mikluko/sniffarr/compare/v0.11.0...v0.12.0
[0.11.0]: https://github.com/mikluko/sniffarr/compare/v0.10.0...v0.11.0
[0.10.0]: https://github.com/mikluko/sniffarr/compare/v0.9.0...v0.10.0
[0.9.0]: https://github.com/mikluko/sniffarr/compare/v0.8.0...v0.9.0
[0.8.0]: https://github.com/mikluko/sniffarr/compare/v0.7.1...v0.8.0
[0.7.1]: https://github.com/mikluko/sniffarr/compare/v0.7.0...v0.7.1
[0.7.0]: https://github.com/mikluko/sniffarr/compare/v0.6.0...v0.7.0
[0.6.0]: https://github.com/mikluko/sniffarr/compare/v0.5.0...v0.6.0
[0.5.0]: https://github.com/mikluko/sniffarr/compare/v0.4.0...v0.5.0
[0.4.0]: https://github.com/mikluko/sniffarr/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/mikluko/sniffarr/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/mikluko/sniffarr/compare/v0.1.4...v0.2.0
[0.1.4]: https://github.com/mikluko/sniffarr/compare/v0.1.3...v0.1.4
[0.1.3]: https://github.com/mikluko/sniffarr/compare/v0.1.2...v0.1.3
[0.1.2]: https://github.com/mikluko/sniffarr/compare/v0.1.1...v0.1.2
[0.1.1]: https://github.com/mikluko/sniffarr/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/mikluko/sniffarr/releases/tag/v0.1.0
