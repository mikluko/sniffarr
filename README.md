# sniffarr

sniffarr is a private classification database of BitTorrent torrents
discovered on the DHT, with an API to search it.

The database and the API are meant to stay private: run them for yourself, not
as a public index. Peer IP addresses are never sent to the server or stored.
The sniffer fetches metadata only: it never downloads pieces, never uploads,
and never announces itself into a swarm.

## Binaries

- `sniffapi`, the server, is the centre of a work queue. It queues the
  infohashes sniffers sight, hands them out as fetch tasks, stores the
  metadata that comes back in SQLite, classifies stored torrents with
  [TypeSafe](https://typesafe.ai)'s Jev model, and serves the search API.
- `sniffdht`, the sniffer, is a replica that plays one role or both:
  - as a discoverer it walks the DHT for infohashes, by BEP 51
    `sample_infohashes` and by the `announce_peer` queries its own DHT node
    receives, and reports them to the server;
  - as a fetcher it takes tasks from the server, finds each torrent's peers
    with `get_peers` on its own DHT node, fetches the metadata from them by
    BEP 9, and returns the name, size and file list.

  Any number of sniffers can run against one server. It runs until SIGINT or
  SIGTERM.

The queue serves announced infohashes before sampled ones, then the oldest
first. It lives in the same SQLite database and survives restarts; a task
whose fetcher goes away, or whose lease expires, goes back to the queue. A
task whose fetcher finds no peers, or fewer than its `-min-peers`, is dropped
at once. A task whose metadata times out is retried after a backoff and
dropped after a few attempts.
The queue is bounded: once a minute the server evicts the infohashes that have
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
   series; a title with a year and a quality tag makes a movie. An explicit
   Russian marker (`Rus`, a dubbing term or studio) sets Russian audio. A name
   that says neither leaves the kind unknown.
2. Reuse: the rules also reduce the name to a title key, the series title or
   the title and year. A torrent of unknown kind takes the kind of a stored
   torrent with the same key, now or once one is classified. Only the kind is
   shared; Russian audio and the video source belong to each release.
3. [TypeSafe](https://typesafe.ai)'s Jev examines a torrent for Russian audio,
   video source, and kind when it is still unknown. It does so lazily, for
   the candidates of a search before the answer, and in the background only
   for torrents of unknown kind whose names are Cyrillic or carry a Russian
   marker. Both share a daily token budget, 2,000,000 tokens by default, some
   1,500 torrents a day; the background stops at 75% of it by default, leaving
   the rest for searches. A search waits for Jev for a short while at most;
   answers that come later are recorded for the next search, and a slow or
   failing Jev never fails a search.

Adult and software kinds never show in search results.

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
| `sniffarr.v1.SearchService` | `Search` | `SNIFFARR_SEARCH_TOKEN` |
| `sniffarr.v1.SearchService` | `Get`    | `SNIFFARR_SEARCH_TOKEN` |

- `Sight` is client-streaming: it queues every infohash the server has neither
  stored, rejected nor queued, and returns how many were new and how many
  known.
- `Fetch` is bidirectional: the client grants credit, one unit per task it can
  take, the server answers with a task per unit, and the client returns each
  task's result, metadata or a failure, on the same stream.
- `Search` matches every word of the query against names and stored file
  paths, and filters by kind and by the probability of Russian audio. It has
  Jev examine a bounded number of its candidates first, so the filters apply
  to what is known once that is done. Each release carries the layer that
  decided its kind and whether Jev has examined it.
- `Get` returns one release by infohash.

## Configuration

### sniffapi

| Variable                           | Default          | Meaning                                                                                                                |
| ---------------------------------- | ---------------- | ---------------------------------------------------------------------------------------------------------------------- |
| `SNIFFARR_DB`                      |                  | Path of the SQLite database; required                                                                                  |
| `SNIFFARR_INGEST_TOKEN`            |                  | Bearer token of `IngestService`; required                                                                              |
| `SNIFFARR_SEARCH_TOKEN`            |                  | Bearer token of `SearchService`; required                                                                              |
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
| `SNIFFARR_LOG_LEVEL`               | `info`           | Level to log at: `debug`, `info`, `warn` or `error`                                                                    |

The database runs in WAL mode and its schema migrates on start; leases left
over from a previous run are freed on start. A torrent keeps its true file
count, but only its largest files are stored and searched. Once a minute the
server deletes, in small batches, the torrents nobody has sighted within the
retention period; a later sighting queues them again.

### sniffdht

| Variable                | Meaning                                                          |
| ----------------------- | ---------------------------------------------------------------- |
| `SNIFFARR_URL`          | Base URL of the server, such as `http://sniffapi:8080`; required |
| `SNIFFARR_INGEST_TOKEN` | Bearer token of `IngestService`; required                        |

| Flag             | Default | Meaning                                                   |
| ---------------- | ------- | --------------------------------------------------------- |
| `-discover`      | `true`  | Walk the DHT and report the infohashes it reveals         |
| `-fetch`         | `true`  | Take fetch tasks from the server and return metadata      |
| `-port`          | `42069` | Port for peers and the DHT, TCP and UDP; `0` picks one    |
| `-fetchers`      | `2000`  | Metadata fetches at once                                  |
| `-rate`          | `2000`  | DHT queries, and new peer connections, a second           |
| `-fetch-timeout` | `15s`   | How long to wait for metadata once a torrent has peers    |
| `-peer-wait`     | `8s`    | How long to look for a torrent's peers on the DHT         |
| `-min-peers`     | `1`     | Peers needed within `-peer-wait` to fetch the metadata    |
| `-walkers`       | `16`    | `sample_infohashes` queries at once                       |
| `-log-level`     | `info`  | Level to log at: `debug`, `info`, `warn` or `error`       |

A discovering sniffer forwards its port by UPnP. With `-discover=false` the
node is passive by BEP 43: it forwards no port, answers no DHT query, and asks
other nodes to keep it out of their routing tables.

Discovery rests on `sample_infohashes`: in a measured run on a server with a
public address, no `announce_peer` query reached the node, and the sniffer
still fetched metadata for about 22 torrents a second. Behind NAT it fetches
far fewer, a few a second at most.

## Telemetry

Both binaries export traces and metrics with the OpenTelemetry SDK over OTLP,
configured only by the standard variables: `OTEL_EXPORTER_OTLP_ENDPOINT` and
the rest of `OTEL_EXPORTER_OTLP_*`, `OTEL_TRACES_EXPORTER`,
`OTEL_METRICS_EXPORTER`, `OTEL_SERVICE_NAME` (`sniffapi` or `sniffdht` when
unset), `OTEL_RESOURCE_ATTRIBUTES`, `OTEL_TRACES_SAMPLER` and
`OTEL_TRACES_SAMPLER_ARG`. Without an endpoint the exporters go to the
specification's default on localhost. `OTEL_SDK_DISABLED=true` turns
telemetry off: no exporter is created and nothing connects anywhere.

Every RPC is traced and measured on both ends by
[otelconnect](https://github.com/connectrpc/otelconnect-go). Logs stay JSON on
stderr and carry `trace_id` and `span_id` when written inside a span. No
BitTorrent peer address appears in spans, attributes or logs.

| Binary     | Metric                                 | Unit         | Attributes                                 |
| ---------- | -------------------------------------- | ------------ | ------------------------------------------ |
| `sniffapi` | `sniffarr.ingest.sightings`            | `{sighting}` | `sniffarr.sighting.result`                 |
| `sniffapi` | `sniffarr.queue.depth`                 | `{infohash}` | `sniffarr.discovery`                       |
| `sniffapi` | `sniffarr.queue.leases`                | `{lease}`    |                                            |
| `sniffapi` | `sniffarr.queue.lease_expiries`        | `{lease}`    |                                            |
| `sniffapi` | `sniffarr.queue.evictions`             | `{infohash}` | `sniffarr.queue.eviction`                  |
| `sniffapi` | `sniffarr.queue.dead`                  | `{infohash}` |                                            |
| `sniffapi` | `sniffarr.store.retention.deletions`   | `{torrent}`  |                                            |
| `sniffapi` | `sniffarr.ingest.results`              | `{result}`   | `sniffarr.fetch.outcome`                   |
| `sniffapi` | `sniffarr.ingest.rejections`           | `{torrent}`  | `sniffarr.rules.reason`                    |
| `sniffapi` | `sniffarr.ingest.stored`               | `{torrent}`  |                                            |
| `sniffapi` | `sniffarr.classify.classifications`    | `{torrent}`  | `sniffarr.kind`, `sniffarr.classify.layer` |
| `sniffapi` | `sniffarr.search.lazy.duration`        | `s`          |                                            |
| `sniffapi` | `sniffarr.search.lazy.classifications` | `{torrent}`  | `sniffarr.search.lazy.result`              |
| `sniffapi` | `sniffarr.classify.tokens`             | `{token}`    |                                            |
| `sniffapi` | `sniffarr.classify.errors`             | `{error}`    | `sniffarr.classify.error`                  |
| `sniffdht` | `sniffarr.dht.nodes`                   | `{node}`     |                                            |
| `sniffdht` | `sniffarr.dht.sample_answers`          | `{answer}`   |                                            |
| `sniffdht` | `sniffarr.sniff.sightings`             | `{infohash}` | `sniffarr.discovery`                       |
| `sniffdht` | `sniffarr.sniff.fetch.in_flight`       | `{task}`     |                                            |
| `sniffdht` | `sniffarr.sniff.fetch.outcomes`        | `{task}`     | `sniffarr.fetch.outcome`                   |
| `sniffdht` | `sniffarr.sniff.fetch.duration`        | `s`          | `sniffarr.fetch.outcome`                   |

`sniffarr.fetch.outcome` is `torrent`, `no_peers` or `timeout`;
`sniffarr.discovery` is `announce` or `sample`; `sniffarr.sighting.result` is
`new` or `known`; `sniffarr.classify.error` is `permanent` or `transient`;
`sniffarr.queue.eviction` is `age`, `size` or `no_peers`;
`sniffarr.classify.layer` is `rules`, `reuse` or `jev`;
`sniffarr.search.lazy.result` is `in_time`, `late` or `failed`.

## Container image

The image carries both binaries and starts `sniffapi`; run the sniffer by
overriding the entrypoint with `/sniffdht`.

```shell
docker build -t sniffarr .
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
`TYPESAFE_API_KEY`, which sniffapi takes as its environment. The sniffers read
their ingest token from the same Secret unless `sniff.existingSecret` or
`fetch.existingSecret` names another. With `api.litestream.enabled`,
`api.litestream.existingConfigMap` is required too. Every value is described
in [`values.yaml`](charts/sniffarr/values.yaml).

The chart runs sniffapi as `<release>-api`, behind a Service of the same name,
and sniffdht as two Deployments. `<release>-sniff` discovers, in the host's
network by default so that `announce_peer` queries reach its DHT port;
`<release>-fetch` fetches, in the pod network by default, with
`fetch.replicas` replicas and the URL and token Secret of `sniff` unless its
own are set. A fetcher's DHT node is passive and the fetcher dials the peers
it fetches from, so it needs no open port.

## Generating code

The generated code under `internal/apis` is committed. After changing a `.proto` file,
regenerate it with [buf](https://buf.build); the protoc plugins come from the
Go tool dependencies in `go.mod`.

```shell
buf lint
go generate ./...
```

## License

MIT; see [LICENSE](LICENSE).
