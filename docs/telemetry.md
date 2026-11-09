# Telemetry

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

The API's SQLite statements are instrumented by
[otelsql](https://github.com/XSAM/otelsql): inside a traced request each
statement is a child span carrying its text with placeholders, never its
arguments, and every statement is measured in `db.client.operation.duration`
(`s`), by `db.pool` (`write` or `read`), `db.operation.name` and
`code.function.name`, the Go function that issued it. The pools' connection
counts are the `db.sql.connection.*` metrics.

| Component  | Metric                                 | Unit         | Attributes                                 |
| ---------- | -------------------------------------- | ------------ | ------------------------------------------ |
| `-api`     | `sniffarr.db.wal.size`                 | `By`         |                                            |
| `-api`     | `sniffarr.ingest.sightings`            | `{sighting}` | `sniffarr.sighting.result`                 |
| `-api`     | `sniffarr.queue.depth`                 | `{order}`    | `sniffarr.task.kind`, `sniffarr.priority`  |
| `-api`     | `sniffarr.queue.leases`                | `{lease}`    | `sniffarr.task.kind`                       |
| `-api`     | `sniffarr.queue.lease_expiries`        | `{lease}`    | `sniffarr.task.kind`                       |
| `-api`     | `sniffarr.queue.evictions`             | `{infohash}` | `sniffarr.queue.eviction`                  |
| `-api`     | `sniffarr.queue.dead`                  | `{infohash}` |                                            |
| `-api`     | `sniffarr.store.retention.deletions`   | `{torrent}`  | `sniffarr.store.retention.class`           |
| `-api`     | `sniffarr.store.journal.deletions`     | `{entry}`    |                                            |
| `-api`     | `sniffarr.ingest.results`              | `{result}`   | `sniffarr.fetch.outcome`                   |
| `-api`     | `sniffarr.ingest.rejections`           | `{torrent}`  | `sniffarr.rules.reason`                    |
| `-api`     | `sniffarr.ingest.stored`               | `{torrent}`  |                                            |
| `-api`     | `sniffarr.classify.classifications`    | `{torrent}`  | `sniffarr.kind`, `sniffarr.classify.layer` |
| `-api`     | `sniffarr.search.lazy.duration`        | `s`          |                                            |
| `-api`     | `sniffarr.search.lazy.classifications` | `{torrent}`  | `sniffarr.search.lazy.result`              |
| `-api`     | `sniffarr.classify.tokens`             | `{token}`    | `sniffarr.classify.direction`              |
| `-api`     | `sniffarr.typesafe.requests`           | `{request}`  | `sniffarr.typesafe.status`                 |
| `-api`     | `sniffarr.classify.errors`             | `{error}`    | `sniffarr.classify.error`                  |
| `-api`     | `sniffarr.tmdb.matches`                | `{title}`    | `sniffarr.tmdb.layer`                      |
| `-api`     | `sniffarr.tmdb.requests`               | `{request}`  | `sniffarr.tmdb.status`                     |
| `-api`     | `sniffarr.search.lazy.titles`          | `{title}`    | `sniffarr.search.lazy.result`              |
| `-api`     | `sniffarr.arr.events`                  | `{event}`    | `sniffarr.arr.app`, `sniffarr.arr.event`, `sniffarr.arr.source` |
| `-api`     | `sniffarr.arr.outcomes`                | `{event}`    | `sniffarr.arr.app`, `sniffarr.arr.outcome` |
| `-api`     | `sniffarr.source.requests`             | `{request}`  | `sniffarr.source.name`, `sniffarr.source.result` |
| `-api`     | `sniffarr.source.listings`             | `{listing}`  | `sniffarr.source.name`, `sniffarr.source.outcome` |
| `-api`     | `sniffarr.probe.tasks`                 | `{task}`     | `sniffarr.probe.priority`                  |
| `-api`     | `sniffarr.probe.results`               | `{result}`   | `sniffarr.probe.outcome`                   |
| `-api`     | `sniffarr.probe.downloaded`            | `By`         |                                            |
| `-api`     | `sniffarr.probe.uploaded`              | `By`         |                                            |
| `-sniff`, `-fetch` | `sniffarr.dht.nodes`                   | `{node}`     |                                            |
| `-sniff`, `-fetch` | `sniffarr.dht.sample_answers`          | `{answer}`   |                                            |
| `-sniff`, `-fetch` | `sniffarr.dht.sample_queries`          | `{query}`    | `sniffarr.dht.outcome`                     |
| `-sniff`   | `sniffarr.dht.replies`                 | `{reply}`    | `sniffarr.dht.query`                       |
| `-sniff`   | `sniffarr.dht.infohashes`              | `{infohash}` |                                            |
| `-sniff`   | `sniffarr.dht.peers`                   | `{peer}`     |                                            |
| `-sniff`   | `sniffarr.sniff.sightings`             | `{infohash}` | `sniffarr.discovery`                       |
| `-fetch`   | `sniffarr.sniff.fetch.in_flight`       | `{task}`     |                                            |
| `-fetch`   | `sniffarr.sniff.fetch.outcomes`        | `{task}`     | `sniffarr.fetch.outcome`                   |
| `-fetch`   | `sniffarr.sniff.fetch.duration`        | `s`          | `sniffarr.fetch.outcome`                   |
| `-fetch`   | `sniffarr.sniff.probe.outcomes`        | `{task}`     | `sniffarr.probe.outcome`                   |
| `-fetch`   | `sniffarr.seed.downloaded`             | `By`         |                                            |
| `-fetch`   | `sniffarr.seed.uploaded`               | `By`         |                                            |
| `-fetch`   | `sniffarr.seed.torrents`               | `{torrent}`  |                                            |
| `-fetch`   | `sniffarr.seed.stored`                 | `By`         |                                            |
| `-fetch`   | `sniffarr.seed.removals`               | `{torrent}`  | `sniffarr.seed.reason`                     |

`sniffarr.fetch.outcome` is `torrent`, `no_peers` or `timeout`, and for a peer lookup
`lookup` or `lookup_no_peers`;
`sniffarr.discovery` is `announce` or `sample`;
`sniffarr.dht.query` is `sample_infohashes` or `scrape`; `sniffarr.dht.outcome` is
`answered`, `timeout` (no answer within 5 seconds) or `error` (an error reply or a send
failure), and the sum over it is the `sample_infohashes` queries sent, less those cut
short when the node stops; `sniffarr.sighting.result` is
`new` or `known`; `sniffarr.classify.error` is `permanent` or `transient`;
`sniffarr.classify.direction` is `input` or `output`;
`sniffarr.typesafe.status` is the HTTP status of the classifier endpoint's
answer to each attempt, retries included, or `error` without one;
`sniffarr.queue.eviction` is `no_peers`; `sniffarr.priority` is `0` to `3`;
`sniffarr.task.kind` is `fetch`, `peers` or `probe`;
`sniffarr.store.retention.class` is `idle` or `expired`;
`sniffarr.classify.layer` is `rules`, `reuse` or `classifier`;
`sniffarr.search.lazy.result` is `in_time`, `late` or `failed`;
`sniffarr.tmdb.layer` is `tmdb`, `classifier` or `none`; `sniffarr.tmdb.status` is
the HTTP status of TMDB's answer, or `error` without one;
`sniffarr.arr.app` is `radarr` or `sonarr`; `sniffarr.arr.event` is `grab`
or `import`; `sniffarr.arr.source` is `webhook` or `history`;
`sniffarr.arr.outcome` is `matched` (the stored release got the work),
`queued` (the infohash was queued), `unknown` (no infohash, no work, or a
rejected torrent) or `duplicate` (recorded before);
`sniffarr.source.name` is a Torznab source's name in `SNIFFARR_SOURCES`;
`sniffarr.source.result` is `ok` (a feed), `refused` (a Newznab error, as
for credentials the source does not take) or `failed` (no answer, a status
other than 2xx, or an answer that is neither); `sniffarr.source.outcome` is
`stored`, `queued`, `rejected` or `duplicate` (recorded before), a listing
without an infohash counted in none;
`sniffarr.probe.priority` is `search` or `background`;
`sniffarr.probe.outcome` is `tracks`, `unknown`, `no_peers` or `timeout`;
`sniffarr.seed.reason` is `ratio` (uploaded as much as downloaded), `age`
(seeded for `-seed-max-age`), `useless` (no connected peer needed it at the
first check after the probe), `idle` (no peer needed it and it uploaded
nothing for `-seed-idle`) or `size` (oldest beyond `-seed-max-size`), all but
`ratio` removals before the ratio.
