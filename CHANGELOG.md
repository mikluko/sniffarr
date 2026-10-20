# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/2.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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

[Unreleased]: https://github.com/mikluko/sniffarr/compare/v0.5.0...HEAD
[0.5.0]: https://github.com/mikluko/sniffarr/compare/v0.4.0...v0.5.0
[0.4.0]: https://github.com/mikluko/sniffarr/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/mikluko/sniffarr/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/mikluko/sniffarr/compare/v0.1.4...v0.2.0
[0.1.4]: https://github.com/mikluko/sniffarr/compare/v0.1.3...v0.1.4
[0.1.3]: https://github.com/mikluko/sniffarr/compare/v0.1.2...v0.1.3
[0.1.2]: https://github.com/mikluko/sniffarr/compare/v0.1.1...v0.1.2
[0.1.1]: https://github.com/mikluko/sniffarr/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/mikluko/sniffarr/releases/tag/v0.1.0
