# sniffarr

sniffarr is a private classification database of BitTorrent torrents
discovered on the DHT, with an API to search it.

The database and the API are meant to stay private: run them for yourself, not
as a public index. Peer IP addresses are never sent to the server or stored.
The sniffer fetches metadata only: it never downloads pieces, never uploads,
and never announces itself into a swarm.

## Binaries

- `sniffdht`, the sniffer, walks the DHT for infohashes, by BEP 51
  `sample_infohashes` and by the `announce_peer` queries its own DHT node
  receives. It asks the server which of them it has never seen, fetches the
  metadata of those from their peers by BEP 9, and streams the name, size and
  file list to the server. It runs until SIGINT or SIGTERM.
- `sniffapi`, the server, stores what the sniffer submits in SQLite, serves the
  API, and classifies stored torrents with
  [TypeSafe](https://typesafe.ai)'s Jev model.

On submit, the server first rejects by name and file extensions what is never
worth classifying: adult content, software and games, books and comics,
courses, torrents under 50 MiB, and torrents with neither video nor audio
files. A rejected infohash is remembered with its reason only, so the sniffer
is never asked to fetch it again. Accepted torrents are classified for kind,
Russian audio and video source; adult and software kinds never show in search
results.

## API

Both API groups are [ConnectRPC](https://connectrpc.com) services, defined in
[`proto/sniffarr/v1`](proto/sniffarr/v1). The server speaks HTTP/1.1 and HTTP/2
without TLS, so Connect, gRPC and gRPC-Web clients all work. Each group needs
its own bearer token in the `Authorization` header; `/healthz` needs none.

| Service                     | RPC       | Token                   |
|-----------------------------|-----------|-------------------------|
| `sniffarr.v1.IngestService` | `Unknown` | `SNIFFARR_INGEST_TOKEN` |
| `sniffarr.v1.IngestService` | `Submit`  | `SNIFFARR_INGEST_TOKEN` |
| `sniffarr.v1.SearchService` | `Search`  | `SNIFFARR_SEARCH_TOKEN` |
| `sniffarr.v1.SearchService` | `Get`     | `SNIFFARR_SEARCH_TOKEN` |

- `Unknown` returns the infohashes the server has neither stored nor rejected.
- `Submit` is client-streaming and returns how many torrents were accepted,
  rejected and duplicate.
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

The database runs in WAL mode and its schema migrates on start.

### sniffdht

| Variable                | Meaning                                                          |
|-------------------------|------------------------------------------------------------------|
| `SNIFFARR_URL`          | Base URL of the server, such as `http://sniffapi:8080`; required |
| `SNIFFARR_INGEST_TOKEN` | Bearer token of `IngestService`; required                        |

| Flag             | Default | Meaning                                                   |
|------------------|---------|-----------------------------------------------------------|
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
