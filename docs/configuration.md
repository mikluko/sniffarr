# Configuration

Each component reads its own variables, listed below, and only when it is on.
A variable several components read, such as `SNIFFARR_URL`, is read once and means
the same to each.

## Every component

| Flag or variable     | Default | Meaning                                                                                                             |
| -------------------- | ------- | ------------------------------------------------------------------------------------------------------------------- |
| `-api`               | off     | Serve the API                                                                                                       |
| `-ui`                | off     | Serve the web UI                                                                                                    |
| `-sniff`             | off     | Walk the DHT and report infohashes                                                                                  |
| `-fetch`             | off     | Take fetch tasks and return metadata                                                                                |
| `-probe`             | off     | Probe stored releases for their tracks: `-api` queues probes, `-fetch` takes them; see [Probes](probes.md)          |
| `-wait-api`          | off     | Poll `SNIFFARR_URL` every 2s and exit once its `Server` header names this binary's version; runs alone, without the flags above |
| `-log-level`         | `info`  | Level to log at: `debug`, `info`, `warn` or `error`                                                                 |
| `SNIFFARR_LOG_LEVEL` |         | The level when `-log-level` is not given; the flag wins when both are set                                           |
| `SNIFFARR_URL`       |         | Base URL of the API, such as `http://sniffarr-api:8080`; required by `-ui`, `-sniff`, `-fetch` and `-wait-api` without `-api`, and must be unset with it |

## `-api`

| Variable                           | Default          | Meaning                                                                                                                |
| ---------------------------------- | ---------------- | ---------------------------------------------------------------------------------------------------------------------- |
| `SNIFFARR_DB`                      |                  | Path of the SQLite database; required                                                                                  |
| `SNIFFARR_ADDR`                    | `:8080`          | Listen address                                                                                                         |
| `SNIFFARR_CLASSIFY_URL`            | `https://api.typesafe.ai/v1/systemone` | Classifier endpoint serving the System One protocol, `POST /v1/systemone`; another endpoint than the default turns the classifier on, with or without a key, and has no daily token limit |
| `SNIFFARR_CLASSIFY_API_KEY`        |                  | Key sent to `SNIFFARR_CLASSIFY_URL` as a bearer token; with the default endpoint and no key the classifier is off, and only rules and reuse classify |
| `TYPESAFE_API_KEY`                 |                  | Deprecated: use `SNIFFARR_CLASSIFY_API_KEY`. Read when that is unset, and sent to the default endpoint only            |
| `SNIFFARR_CLASSIFY_CONCURRENCY`    | `4`              | Background classification requests at once                                                                             |
| `SNIFFARR_CLASSIFY_DAILY_TOKENS`   | `2000000`        | Classifier tokens per UTC day at most with the default endpoint; `0` is no limit                                       |
| `SNIFFARR_CLASSIFY_SEARCH_RESERVE` | `25`             | Percentage of the daily tokens background classification leaves for searches, `0` to `100`                             |
| `SNIFFARR_SEARCH_CLASSIFY_MAX`     | `20`             | Candidates one search has classified at most                                                                           |
| `SNIFFARR_SEARCH_CLASSIFY_WAIT`    | `2s`             | How long a search waits for them                                                                                       |
| `SNIFFARR_FETCH_LEASE`             | `1m`             | How long a fetcher may hold a task                                                                                     |
| `SNIFFARR_FETCH_ATTEMPTS`          | `3`              | Timed-out fetches after which an infohash is dropped                                                                   |
| `SNIFFARR_FETCH_BACKOFF`           | `10m`            | Wait before retrying a timed-out task, doubled per timeout                                                             |
| `SNIFFARR_QUEUE_MAX_SIZE`          | `1000000`        | Fetches the queue keeps                                                                                                |
| `SNIFFARR_QUEUE_MAX_AGE`           | `24h`            | How long a fetch may wait in the queue                                                                                 |
| `SNIFFARR_QUEUE_DEAD_FOR`          | `168h`           | How long an evicted infohash, or one dropped after its last attempt, stays out                                         |
| `SNIFFARR_QUEUE_NO_PEERS_FOR`      | `24h`            | How long an infohash dropped for having no peers stays out                                                             |
| `SNIFFARR_STORE_MAX_FILES`         | `20`             | Files stored per torrent, the largest ones                                                                             |
| `SNIFFARR_RETENTION`               | `2160h`          | How long a stored torrent is kept without being seen                                                                   |
| `SNIFFARR_RETENTION_IDLE`          | `168h`           | How long a stored torrent with no TMDB work is kept without being seen                                                 |
| `SNIFFARR_RETENTION_JOURNAL`       | `720h`           | How long a torrent's journal entries are kept                                                                          |
| `SNIFFARR_STATS_INTERVAL`          | `5m`             | How often the counts of `Stats` and `Home` are recomputed                                                              |
| `SNIFFARR_SCRIPTS`                 | `Latin,Cyrillic` | Scripts, by Go `unicode.Scripts` name and comma-separated, whose letters a torrent name may have; empty is the default |
| `SNIFFARR_AUDIO_LANGUAGES`         |                  | Audio languages, BCP-47 tags and comma-separated; unset or empty leaves the extension off. An unknown tag fails startup |
| `TMDB_API_KEY`                     | built-in token   | TMDB API key or read access token in place of the built-in one; unset or empty uses the built-in one                   |
| `SNIFFARR_TMDB_CONCURRENCY`        | `2`              | Background title matches at once                                                                                       |
| `SNIFFARR_TMDB_RATE`               | `20`             | TMDB requests a second at most                                                                                         |
| `SNIFFARR_TMDB_RETRY`              | `168h`           | How long a title key that matched nothing waits before it is looked up again                                           |
| `SNIFFARR_SEARCH_TMDB_MAX`         | `5`              | Title keys one search has matched at most                                                                              |
| `SNIFFARR_TORZNAB_TOKEN`           |                  | API key of the Torznab endpoint; when unset, there is none                                                             |
| `SNIFFARR_WEBHOOK_TOKEN`           |                  | Basic password of the Radarr and Sonarr webhook; when unset, there is none                                             |
| `SNIFFARR_RADARR_URL`              |                  | Root URL of the Radarr whose grab history is imported; set with `SNIFFARR_RADARR_API_KEY` or not at all                |
| `SNIFFARR_RADARR_API_URL`          | `/api/v3` under `SNIFFARR_RADARR_URL` | Root of that Radarr's v3 API, such as `http://radarr:7878/api/v3`, where the API reaches it by another address |
| `SNIFFARR_RADARR_API_KEY`          |                  | That Radarr's API key                                                                                                  |
| `SNIFFARR_SONARR_URL`              |                  | Root URL of the Sonarr whose grab history is imported; set with `SNIFFARR_SONARR_API_KEY` or not at all                |
| `SNIFFARR_SONARR_API_URL`          | `/api/v3` under `SNIFFARR_SONARR_URL` | Root of that Sonarr's v3 API, such as `http://sonarr:8989/api/v3`, where the API reaches it by another address |
| `SNIFFARR_SONARR_API_KEY`          |                  | That Sonarr's API key                                                                                                  |
| `SNIFFARR_FEEDS`                   |                  | Comma-separated tracker feeds read: `rutracker`; empty reads none                                                      |
| `SNIFFARR_FEED_INTERVAL`           | `1h`             | How often each feed is read                                                                                            |
| `SNIFFARR_RUTRACKER_FEED_URL`      | `https://feed.rutracker.cc` | Where rutracker's feeds are served                                                                          |
| `SNIFFARR_RUTRACKER_FORUMS`        | `0`              | Comma-separated rutracker forums whose feeds are read; `0` is the whole tracker                                        |
| `SNIFFARR_NNMCLUB_URL`             | `https://nnmclub.to/forum` | The NNM-Club forum whose RSS and topics are read                                                             |
| `SNIFFARR_NNMCLUB_FORUMS`          |                  | Comma-separated NNM-Club forums whose RSS is read; empty reads the RSS of every public forum                           |
| `SNIFFARR_RUTOR_URL`               | `https://rutor.info` | The rutor site whose listing pages are read                                                                        |
| `SNIFFARR_RUTOR_CATEGORIES`        | `0`              | Comma-separated rutor categories whose listings are read; `0` is every one                                             |
| `SNIFFARR_RUTOR_DEPTH`             | `10`             | Listing pages of each rutor category the first pass reads                                                              |
| `SNIFFARR_PROBE_DAILY_BYTES`       | `100GiB`         | Piece data probes download per UTC day at most, in bytes or with a unit of `B`, `KiB`, `MiB`, `GiB` or `TiB`; `0` is no limit |
| `SNIFFARR_PROBE_SEARCH_RESERVE`    | `25`             | Percentage of the daily bytes left to releases a Torznab search returned, `0` to `100`                                 |
| `SNIFFARR_PROBE_MAX_BYTES`         | `64MiB`          | Piece data one probe downloads at most, units as above                                                                 |
| `SNIFFARR_PROBE_LEASE`             | `15m`            | How long a fetcher may hold a probe task                                                                               |
| `SNIFFARR_PROBE_ATTEMPTS`          | `3`              | Failed probes after which a release is probed again only with more peers than at its last probe                       |
| `SNIFFARR_PROBE_BACKOFF`           | `6h`             | Wait before repeating a failed probe, doubled per failure                                                              |
| `SNIFFARR_PROBE_BATCH`             | `64`             | Probe tasks one search and one write hand out at most; set it to the probers that may ask at once                     |
| `SNIFFARR_RELEASE_CHECK`           | `lazy`           | Whether the API reads the latest stable release from the releases feed, `https://sarak.sh/mikluko/sniffarr/releases.rss`, once a day, and when the UI's footer announces it: `lazy` when this build is a later major or more than one minor behind, `eager` when it is behind at all, `no` or `off` asks nothing; a pre-release build is announced under either as soon as its stable release is out |

The database runs in WAL mode. Before the API listens, its schema migrates,
each migration logged as it begins and as it ends with how long it took, and
leases left over from a previous run are freed; however long that takes, it
leaves the API unready rather than failing its start. Once a minute the API
truncates the WAL when it has reached 1 GiB, waiting up to 10 seconds for readers; a
reader that outlasts the wait is logged with the frames it pins. The WAL's
size is the metric `sniffarr.db.wal.size`. A torrent keeps its true file
count, but only its largest files are stored and searched. Once a minute the
API deletes, in small batches, the torrents nobody has sighted within the
retention period, or within the idle retention period for those matched to no
TMDB work, with their peer sketches; a later sighting queues them again.
Then, in batches of its own, it deletes the journal entries older than the
journal retention period, counted from each entry's time, so the entry of a
grab or an import Radarr or Sonarr reports later than that after it
happened goes at the next sweep. At the defaults the journal outlasts the
idle retention and not the retention: a torrent deleted as idle keeps its
whole trail unless it began more than 30 days before, and one deleted as
expired, unseen for 90 days, has lost the start of its trail. Once an hour it
unions the hour sketches older than 8 days into day sketches and deletes the
day sketches older than 65 days, and every 10 minutes, and once at start, it
recomputes the tops of works and every torrent's peers over the last 7 days.

## `-ui`

| Variable                   | Default | Meaning                                                                              |
| -------------------------- | ------- | ------------------------------------------------------------------------------------ |
| `SNIFFARR_UI_ADDR`         | `:8081` | Listen address                                                                       |
| `SNIFFARR_OIDC_ISSUER`     |         | OpenID Connect issuer whose userinfo gives the UI a person's locale; empty asks none |
| `SNIFFARR_SEERR_URL`       |         | Base URL of a Seerr the title and release cards link a matched title to; empty links none |
| `SNIFFARR_RADARR_URL`      |         | Root URL of a Radarr the title and release cards link a matched movie to; empty links none |
| `SNIFFARR_SONARR_URL`      |         | Root URL of a Sonarr the title and release cards link a matched series to; empty links none |

## `-sniff` and `-fetch`

| Flag             | Default | Meaning                                                |
| ---------------- | ------- | ------------------------------------------------------ |
| `-port`          | `42069` | Port for peers and the DHT, TCP and UDP; `0` picks one |
| `-port-offset`   | `0`     | Added to `-port`, such as a StatefulSet pod's index, so replicas run from one set of flags listen apart; `0` with `-port 0` |
| `-upnp`          | `false` | Forward the port, TCP and UDP, by UPnP                 |
| `-stun`          | `stun.l.google.com:19302` | STUN server asked for the node's public address, for BEP 42; empty asks none |
| `-fetchers`      | `2000`  | Metadata fetches at once                               |
| `-rate`          | `2000`  | DHT queries, and new peer connections, a second        |
| `-fetch-timeout` | `15s`   | How long to wait for metadata once a torrent has peers |
| `-peer-wait`     | `8s`    | How long to look for a torrent's peers on the DHT      |
| `-min-peers`     | `1`     | Peers needed within `-peer-wait` to fetch the metadata |
| `-walkers`       | `16`    | `sample_infohashes` queries at once                    |
| `-probe-dir`     |         | Directory of the pieces `-fetch` probes and seeds; empty is a fresh temporary directory, removed at stop; it needs `-fetch` |
| `-probers`       | `4`     | Probes at once                                         |
| `-probe-timeout` | `10m`   | How long one probe may take, from looking for peers to the tracks |
| `-seed-max-size` | `200GiB` | Piece data kept for seeding, units as `SNIFFARR_PROBE_MAX_BYTES`; the oldest torrents go first beyond it |
| `-seed-max-age`  | `168h`  | How long a probed torrent is seeded at most            |

`-probe-dir`, `-probers`, `-probe-timeout`, `-seed-max-size` and
`-seed-max-age` need `-probe`; given without it, sniffarr refuses to start.

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
