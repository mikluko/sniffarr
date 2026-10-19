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
task that fails is retried after a backoff and forgotten after a few attempts;
a forgotten infohash is not rejected, and a later sighting queues it again.

The server passes fetched metadata through a first filter by name and file
extensions, rejecting what is never worth classifying: adult content,
software and games, books and comics, courses, torrents under 50 MiB, and
torrents with neither video nor audio files. A rejected infohash is
remembered with its reason only, so it is never queued again. Accepted
torrents are classified for kind, Russian audio and video source; adult and
software kinds never show in search results.

## API

Both API groups are [ConnectRPC](https://connectrpc.com) services, defined in
[`proto/sniffarr/v1`](proto/sniffarr/v1). The server speaks HTTP/1.1 and HTTP/2
without TLS, so Connect, gRPC and gRPC-Web clients all work; `Fetch` streams
both ways and needs HTTP/2 end to end. Each group needs its own bearer token
in the `Authorization` header; `/healthz` needs none.

| Service                     | RPC      | Token                   |
|-----------------------------|----------|-------------------------|
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
- `Search` matches every word of the query against names and file paths, and
  filters by kind and by the probability of Russian audio.
- `Get` returns one release by infohash.

## Configuration

### sniffapi

| Variable                         | Default | Meaning                                                  |
|----------------------------------|---------|----------------------------------------------------------|
| `SNIFFARR_DB`                    |         | Path of the SQLite database; required                    |
| `SNIFFARR_INGEST_TOKEN`          |         | Bearer token of `IngestService`; required                |
| `SNIFFARR_SEARCH_TOKEN`          |         | Bearer token of `SearchService`; required                |
| `SNIFFARR_ADDR`                  | `:8080` | Listen address                                           |
| `TYPESAFE_API_KEY`               |         | TypeSafe API key; when unset, torrents stay unclassified |
| `SNIFFARR_CLASSIFY_CONCURRENCY`  | `4`     | Classification requests at once                          |
| `SNIFFARR_CLASSIFY_DAILY_TOKENS` | `0`     | Classifier tokens per UTC day at most; `0` is no limit   |
| `SNIFFARR_FETCH_LEASE`           | `1m`    | How long a fetcher may hold a task                       |
| `SNIFFARR_FETCH_ATTEMPTS`        | `3`     | Failed fetches after which an infohash is forgotten      |
| `SNIFFARR_FETCH_BACKOFF`         | `10m`   | Wait before retrying a failed task, doubled per failure  |

The database runs in WAL mode and its schema migrates on start; leases left
over from a previous run are freed on start.

### sniffdht

| Variable                | Meaning                                                          |
|-------------------------|------------------------------------------------------------------|
| `SNIFFARR_URL`          | Base URL of the server, such as `http://sniffapi:8080`; required |
| `SNIFFARR_INGEST_TOKEN` | Bearer token of `IngestService`; required                        |

| Flag             | Default | Meaning                                                   |
|------------------|---------|-----------------------------------------------------------|
| `-discover`      | `true`  | Walk the DHT and report the infohashes it reveals         |
| `-fetch`         | `true`  | Take fetch tasks from the server and return metadata      |
| `-port`          | `42069` | TCP and UDP port for peers and the DHT, forwarded by UPnP |
| `-fetchers`      | `2000`  | Metadata fetches at once                                  |
| `-rate`          | `2000`  | DHT queries, and new peer connections, a second           |
| `-fetch-timeout` | `15s`   | How long to wait for metadata once a torrent has peers    |
| `-peer-wait`     | `8s`    | How long to look for a torrent's peers on the DHT         |
| `-walkers`       | `16`    | `sample_infohashes` queries at once                       |

Discovery rests on `sample_infohashes`: in a measured run on a server with a
public address, no `announce_peer` query reached the node, and the sniffer
still fetched metadata for about 22 torrents a second. Behind NAT it fetches
far fewer, a few a second at most.

## Container image

The image carries both binaries and starts `sniffapi`; run the sniffer by
overriding the entrypoint with `/sniffdht`.

```shell
docker build -t sniffarr .
```

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
