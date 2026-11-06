# API

Both API groups are [ConnectRPC](https://connectrpc.com) services, defined in
[`proto/sniffarr/v1`](../proto/sniffarr/v1). The server speaks HTTP/1.1 and HTTP/2
without TLS, so Connect, gRPC and gRPC-Web clients all work; `Fetch` streams
both ways and needs HTTP/2 end to end. The services authenticate no caller:
whoever reaches the port can call every RPC, so the network has to keep
others out, as the chart's `api.networkPolicy` does.

| Service                     | RPC             |
| --------------------------- | --------------- |
| `sniffarr.v1.IngestService` | `Sight`         |
| `sniffarr.v1.IngestService` | `Fetch`         |
| `sniffarr.v1.IngestService` | `HashKey`       |
| `sniffarr.v1.IngestService` | `Peers`         |
| `sniffarr.v1.IngestService` | `ProbeTask`     |
| `sniffarr.v1.IngestService` | `ProbeResult`   |
| `sniffarr.v1.IngestService` | `ProbeUpload`   |
| `sniffarr.v1.SearchService` | `Search`        |
| `sniffarr.v1.SearchService` | `Get`           |
| `sniffarr.v1.BrowseService` | `SearchTitles`  |
| `sniffarr.v1.BrowseService` | `Title`         |
| `sniffarr.v1.BrowseService` | `TitleReleases` |
| `sniffarr.v1.BrowseService` | `Torrents`      |
| `sniffarr.v1.BrowseService` | `Card`          |
| `sniffarr.v1.BrowseService` | `Stats`         |
| `sniffarr.v1.BrowseService` | `Home`          |
| `sniffarr.v1.BrowseService` | `Top`           |
| `sniffarr.v1.BrowseService` | `Settings`      |

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
- `ProbeTask` leases the next release to probe and the most piece data its
  probe may download, or nothing while there is none or the day's budget is
  spent.
- `ProbeResult` records a probe's outcome: the file's path, format and
  tracks, or why there are none, and the piece data it downloaded.
- `ProbeUpload` records the piece data a fetcher uploaded to the swarms of
  the releases it probed since its last report.
- `Search` matches every word of the query against names and stored file
  paths, and filters by kind and by the probability of an audio track in one
  language. It has Jev examine a bounded number of its candidates first, so
  the filters apply to what is known once that is done. Each release carries
  the layer that decided its kind, whether Jev has examined it, its audio
  entries with their marks, its quality, season and episode, its work as
  `work`, `m348` or `tv1399`, the work's IDs, the probed `tracks` of its
  main video file, and `tmdb`, a `TMDBWork`: what TMDB knows of the
  work its title is matched to, with the title, overview and genres in the
  request's `lang`, `ru` or `en` (`en` when empty), falling back to the other
  language and the title to the original; absent while the title was never
  looked up, with `id` 0 when nothing matched. Given one ID, `Search` returns
  the releases of that work.
- `Get` returns one release by infohash, with `tmdb` in `lang` likewise.
- `BrowseService` reads the store as the web UI shows it and classifies
  nothing first. A work in its answers is a `WorkSummary`: its ID, kind,
  `tmdb`, stored releases, distinct peers over the last 7 days, place in
  the 7-day top of its kind, and `grabs_week`, the grabs Radarr and Sonarr
  reported over the last 7 days. A voice narrows releases by their audio in one
  language: `VOICE_MARKER` to those with a marker of it, `VOICE_LIKELY` to
  those with a marker or a probability of at least 0.5.

| RPC             | Request                                                                                                                                                                          | Response                                                                                                                                   |
| --------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------ |
| `SearchTitles`  | `query`, `kind` (movie, series or both), `lang`                                                                                                                                  | `works`: those whose titles, or whose releases' names and paths, have every word, and those TMDB finds, with releases first by peers, then the rest by TMDB popularity; `ours`, `tmdb_only` |
| `Title`         | `id`, `lang`                                                                                                                                                                     | `work`; `ranks` over 24 hours, 7 days and 30 days; `peers` of all its releases over the three; `audio` per configured language: releases marked, their marks, releases likely |
| `TitleReleases` | `id`; `quality`, `source`, `min_size`, `max_size`, `audio_language` and `voice`, `season`, `episode`; `sort` by peers, first sighting or size; `offset`, `limit`                  | `releases` with their peers over 7 days, quality, source, season, episode and audio; `total`, `matched`, `seasons`                          |
| `Torrents`      | `query`, words or an infohash; `kind` (`KIND_OTHER` takes music, books and software in) or `unknown_kind`; `matched`; `quality`; `audio_language` and `voice`; `source`, `layer`, `min_size`; `sort`; `offset`, `limit`; `lang` | `rows`, each a release with its `work` when matched, adult releases left out; `total`                                                      |
| `Card`          | `infohash`, `lang`                                                                                                                                                               | the release, with `tmdb` the state of its title's match; discovery, every stored file, peers over the day, week and month; its `work` and `rank_in_work`, its place among the work's releases by peers; its `rules`, those that recognised features in the name (kind, year, season, episode, quality) or audio markers in the name or file paths, each with the matched fragment, recomputed when the card is opened |
| `Top`           | `kind` (movie or series), `window`, `voice` and `voice_language` (the first audio language by default), `offset`, `limit`, `lang`                                                | `entries`: rank, `work`, peers, `prev_rank` in the window before, `audio`, sources and first sighting of the releases the voice admits; `total`, `computed_at` |
| `Stats`         | `hours`                                                                                                                                                                          | counts by kind (movie, series, other, unknown; music, books and software are other), layer, source and audio language, rejections by reason, pending, tokens spent today and the day's limit, piece data probes downloaded today, the day's limit on it, and what the fetchers uploaded back today, new torrents per clock hour over the `hours` asked for, the current hour first, `computed_at` |
| `Home`          |                                                                                                                                                                                  | stored, new in 24 hours and pending, `computed_at`                                                                                         |
| `Settings`      |                                                                                                                                                                                  | the API's audio languages, BCP-47 tags in its order, empty while the audio extension is off; the web UI asks it at start                  |

`Stats` and `Home` serve the counts the API computed last, at start and then
every `SNIFFARR_STATS_INTERVAL`, as of `computed_at`; only `tokens_used_day`,
`probe_bytes_rx_day` and `probe_bytes_tx_day` are read at the request.

`lang` is `ru` or `en`, `en` when empty: the language of `tmdb` texts, and
of TMDB's search for `SearchTitles`. A work's `id` that is not one fails
with `INVALID_ARGUMENT`, one of no stored work with `NOT_FOUND`.

## Calling the API from a shell

`hack/rpc` sends one unary request to the API over ConnectRPC and prints the
response as indented protojson. It is a developer tool, not part of the image.

```shell
go run ./hack/rpc [flags] <Service>/<Method> [json|-]
go run ./hack/rpc -l
```

The method is `Service/Method` of `sniffarr.v1` or its full name, such as
`sniffarr.v1.SearchService/Search`; `-l` lists them, and streaming methods are
refused. The request is protojson, from the argument, or from stdin when the
argument is `-` or absent and stdin is not a terminal; empty is the zero
message. `--url` names the API, such as `http://127.0.0.1:8080`. Without it,
`rpc` runs `kubectl port-forward svc/<release>-api` on a free local port, in
the kube context `--context`, the current one when empty, and the namespace
`-n` or `--namespace`, `sniffarr` by default, with `--release` `sniffarr` by
default, and stops it on exit. `-v` prints the method, the URL and the time
taken to stderr; `--timeout`, 30 seconds by default, bounds the call.

```shell
go run ./hack/rpc --context saraksh SearchService/Search '{"query":"House of the Dragon","limit":5}'
go run ./hack/rpc BrowseService/Home
```
