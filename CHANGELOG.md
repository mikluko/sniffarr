# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/2.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.46.0]

### Added

- The metric `sniffarr.dht.sample_queries` counts the `sample_infohashes` queries the sniff sends, by `sniffarr.dht.outcome`: `answered`, `timeout` or `error`.
- The classifier reads the name of a torrent of unknown kind with Jev in two steps, the prefix to drop and then the title and kind, and a release's card shows the prefix and title it read among the rule hits as `jev.prefix` and `jev.title`.
- The metric `sniffarr.typesafe.requests` counts the requests to the classifier endpoint, retries included, by `sniffarr.typesafe.status`.
- The `-pprof` flag serves the Go runtime's profiles on the address it names, under `/debug/pprof/`.
- `SNIFFARR_UI_LANGUAGES` lists the web UI's languages, `en` and `ru`, in the order of the language switch, the first the default.
- With `SNIFFARR_NATS_URL`, the NATS server `-api` embeds connects to that upstream server as a leaf node, with `SNIFFARR_NATS_PASSWORD` or the `.creds` file `SNIFFARR_NATS_CREDS`, its streams kept in its own JetStream domain, `sniffarr`.
- The API polls the Torznab sources `SNIFFARR_SOURCES` names, in order, each with `SNIFFARR_SOURCE_<NAME>_URL`, an API key or a bearer token file, and a mode, `passive` or `active`, for their latest listings every `SNIFFARR_SOURCES_INTERVAL`, 15 minutes by default, and records each listing.
- The infohash of a Torznab source's listing the store lacks is queued at P2 under the discovery `torznab`, which the torrent's card shows as Torznab, and the source's first listing of it is journaled as `listed`.
- The metrics `sniffarr.source.requests` and `sniffarr.source.listings` count the requests to each Torznab source by `sniffarr.source.result` and its listings by `sniffarr.source.outcome`.
- The chart's `api.serviceAccountTokens` projects service account tokens with the audiences it names into the api pod, each at `/var/run/secrets/tokens/<name>`.
- The `-seed-idle` flag, 15 minutes by default and rendered from the chart's `probe.idle`, caps how long a probed torrent no connected peer needs is seeded without uploading.

### Changed

- **Breaking:** the web UI is in English alone, with no language switch, unless `SNIFFARR_UI_LANGUAGES` lists more languages; `en,ru` restores both.
- **Breaking:** `SNIFFARR_NATS_PASSWORD` is the password of the leaf node's user at `SNIFFARR_NATS_URL`, refused without it.
- `IngestService.Sight` publishes the sightings to the API's sightings stream, from which the API queues them in batches, announced ones first, and `SightResponse.known` counts the repeats the stream drops within 5 minutes, not the queued infohashes.
- **Breaking:** `IngestService.Sight` refuses `SOURCE_ARR`.
- `sniffarr.classify.tokens` counts the tokens by `sniffarr.classify.direction`, `input` or `output`.
- Requests to the classifier endpoint are paced at 10 a second.
- The start of `-api` allows up to 5 minutes for the NATS server to recover its store, on top of the 15 seconds the start of every process has.
- Fetches and peer lookups are orders on the NATS stream `FETCH_ORDERS`, which the API hands out through `IngestService.Fetch`, in place of the SQLite queue, whose fetches and lookups move into the stream at the first start.
- A timed-out fetch is retried by a message scheduled on `sniffarr.orders.fetch.retry.<infohash>`.
- An announce of an infohash whose sample waits orders its fetch again at the announce's priority, journaled as `sighted/queued`.
- `RefreshPeers` journals every request for a peer lookup.
- The metric `sniffarr.queue.depth` counts orders by `sniffarr.task.kind` and `sniffarr.priority`, and `sniffarr.queue.lease_expiries` counts by `sniffarr.task.kind`.
- Probes are orders on the NATS stream `PROBE_ORDERS`, which the API hands out through `IngestService.ProbeTask`, in place of the SQLite queue, whose probes move into the stream at the first start.
- A failed probe that a request or a search ordered is retried by a message scheduled on `sniffarr.orders.probe.retry.<infohash>`.
- `RequestProbe` journals every request, and orders the probe of a release without peers at once.
- A Torznab search orders no probe of a release without peers.
- The projection of the current hour, day and week on the data page weighs the period's pace so far against the count over the same length of time ending now, the period's weight rising from none at its start to three quarters halfway and all at its end, from the period's first moment.
- A probed torrent no connected peer needs at the first check after the probe is removed, counted in `sniffarr.seed.removals` as `useless`, and one no peer has needed and that has uploaded nothing for `-seed-idle` is removed as `idle`.

### Fixed

- On a phone, a torrent's meta line in the list leaves out the fields it has no value for and shows a voice-over only with a marker or a probability of at least 0.5.
- On a phone, the copy button of a torrent's infohash stays beside the hash instead of dropping under it.
- A name with a year and a resolution but no source or encoding tag, `Movie.2026.720p`, is no longer taken for a movie by the rules; `WEB-DLRip` counts as a source tag.
- The rules read a release's video source from its name, `UHD.Bluray.REMUX` as remux and `WEB-DLRip` as web, so the card and the source filters no longer show none for it, and stored releases get theirs at the first start.
- A work's TMDB vote count agrees with its number in the web UI, as in "3 192 голоса" and "1 vote".

### Removed

- **Breaking:** the NATS server's listener for workers, with `SNIFFARR_NATS_ADDR`, the user `worker`, and the chart's `api.nats.enabled` and port `nats`.
- **Breaking:** the eviction of waiting fetches by age and by size, with `SNIFFARR_QUEUE_MAX_AGE`, `SNIFFARR_QUEUE_MAX_SIZE` and the `age` and `size` evictions of `sniffarr.queue.evictions`.
- `SNIFFARR_PROBE_BATCH`, with the batching of probe tasks asked for at once.

## [0.45.0] - 2026-11-08

### Added

- `-api` embeds a NATS server with JetStream, its store in `SNIFFARR_NATS_DIR`, `nats` beside `SNIFFARR_DB` by default, with the streams of sightings, fetch and probe orders by priority, and fetch and probe results.
- With `SNIFFARR_NATS_PASSWORD`, the NATS server listens at `SNIFFARR_NATS_ADDR`, `:4222` by default, for the user `worker`, limited to publishing sightings and results and taking orders; the chart's `api.nats.enabled` opens it on the port and Service port `nats`, the password from `api.existingSecret`.

### Changed

- The queue has four priorities, P0 asked for in the web UI, P1 Radarr and Sonarr, P2 announced, P3 sampled; the data page and the levels of `BrowseService.Stats` show four, and queued tasks keep their order.
- The sniff gets a new DHT node ID when its port changes, and once on the first start after the upgrade.

### Fixed

- The chart's Deployments switch to the Recreate strategy, by turning on `hostNetwork` or `hostPort`, without being deleted by hand.

### Removed

- **Breaking:** the tracker feeds, rutracker, nnmclub and rutor, with `SNIFFARR_FEEDS` and their settings, `Source.SOURCE_FEED` and `Feature.FEATURE_FEEDS`; a migration drops their tables and journal entries.
- **Breaking:** `TYPESAFE_API_KEY`: set `SNIFFARR_CLASSIFY_API_KEY`.

## [0.44.3] - 2026-11-08

### Fixed

- Fetchers and probers no longer crash on the metadata of a v2 torrent whose large files have no piece hashes yet.

## [0.44.2] - 2026-11-08

### Fixed

- Fetchers and probers no longer crash on the metadata of a v2 torrent with empty files.

## [0.44.1] - 2026-11-08

### Added

- The chart's `sniff.hostPort` and `fetch.hostPort` publish the DHT port on the node's address from the pod network.

## [0.44.0] - 2026-11-08

The web UI's new design, a journal of what happens to each infohash, and the chart's API as a StatefulSet.

### Added

- What happens to an infohash is recorded in the journal, `torrent_journal`: its sighting and queueing, leases, fetches, classifications, title matches, Radarr and Sonarr grabs and imports, feed listings, probes, requests for peers and its deletion, kept for `SNIFFARR_RETENTION_JOURNAL`, `720h` by default.
- The release card shows the journal in rows by day, each opening on its task's steps and particulars, with failures marked; the card of an infohash with no stored torrent shows its journal and what ended it.
- `BrowseService.Journal` returns the journal of one infohash or of all, narrowed by `kind` and `outcome` and paged by `before`; `Card` returns its newest 200 entries.
- Every response of the API and the web UI carries `Server: sniffarr/<version>`, and the web UI's footer shows the version, linking its release notes.
- The API checks the releases feed for a newer stable release once a day, reports it as `BrowseService.Settings.latest_release`, and the web UI's footer announces it; `SNIFFARR_RELEASE_CHECK` is `lazy` by default, `eager`, or `off`.
- The top reads as a chart: the change against the previous window, time in the top, peak and debut, which `BrowseService.Top` and `Title` return too.
- "Match now" on a release card, and `BrowseService.RequestMatch`, look a title matched to no work up on TMDB at once.
- With `SNIFFARR_RADARR_URL` or `SNIFFARR_SONARR_URL` set for the UI, the pages link a matched work in Radarr or Sonarr; `SNIFFARR_RADARR_API_URL` and `SNIFFARR_SONARR_API_URL` name the API where the API reaches it at another address.
- `SNIFFARR_CLASSIFY_URL` and `SNIFFARR_CLASSIFY_API_KEY` set the classifier's endpoint and key; an endpoint other than TypeSafe's turns the classifier on with or without a key.
- `BrowseService.Settings` reports the optional features, whether each is on and works, and what turns it on; the data page lists them.
- `-wait-api` waits until the API serves the binary's own version, and the chart runs it as an init container of the ui, sniff, fetch and probe pods.
- `-port-offset` adds a number, such as a StatefulSet pod's index, to `-port`.
- `SNIFFARR_PROBE_BATCH` sets how many probe tasks are handed out together, 64 by default.

### Changed

- **Breaking:** probes turn on with `-probe` alone, on the fetchers that probe and on the process running `-api`; the other probe flags without it stop sniffarr at start, and `-probe-dir` is optional. With the Helm chart, `probe.enabled` renders it.
- **Breaking:** TMDB matching cannot be turned off: an empty `TMDB_API_KEY` uses the built-in token.
- **Breaking:** the chart's probe StatefulSet is governed by the headless Service `<release>-probe`, not `<release>-probe-pods`, so delete it once with `kubectl delete statefulset <release>-probe --cascade=orphan` before the upgrade; with a fixed port, each replica listens on the port plus its index behind a Service of its own, `<release>-probe-<index>`.
- The chart runs the API as a StatefulSet of one, `<release>-api`, keeping the claim `<release>-api` of an earlier install; a new install's claim is `data-<release>-api-0`.
- After a restart, the API serves its last counts and answers `/readyz` at once, until it computes new ones.
- The table `settings` is named `state`.
- The classifier's layer `jev` is `classifier` in the API, the metrics and the stored data, and the web UI and the docs name it without Jev.
- Metrics carry the instrumentation scope `sarak.sh/mikluko/sniffarr/...`, not `github.com/mikluko/sniffarr/...`.
- The web UI has a new design: a new look, explanations in tooltips, Torrents and Top in the header, back links, a 404 page, and dates, numbers and wording consistent across pages and languages.
- The data page leads with its figures and meters, shows the task queue per kind and the store as three tables, refreshes on request and marks stale counts.
- The torrents page fits its table at 1280 px and folds its filters on a phone; its search, and `BrowseService.Torrents`, find a release by the titles of its matched work too.
- A movie or series page shows the TMDB details beside the poster, the work's peak in the top, and its releases filtered by season, quality, size and episode.

### Deprecated

- `TYPESAFE_API_KEY`: use `SNIFFARR_CLASSIFY_API_KEY`.

### Removed

- The top's audio and sources columns, and the source and dub filters of a movie's or series' releases.

### Fixed

- Behind a NodePort, the DHT names a probe's seeds at the node port.
- Fetches no longer slow down while many fetchers ask for probe tasks.
- A web UI started before a new API no longer shows probes, audio languages and features as off until restarted.
- The magnet link on the release card opens the torrent in a client.
- The probe button says when a release has no file a probe can read.
- Releases that had failed probes before a successful one get their full number of probe attempts.
- A probe that reads more than 128 tracks is recorded as unknown, so a crafted file header cannot stall the database.

## [0.44.0-rc.2] - 2026-11-08

No changes since rc.1.

## [0.44.0-rc.1] - 2026-11-08

Changes since 0.43.0, the pre-releases taken together: the web UI's new look, a journal of what happens to each infohash, and the chart's API as a StatefulSet.

### Added

- What happens to an infohash is recorded in the journal, the table `torrent_journal`, one entry per event in the transaction of the change: its sighting and queueing, leases, fetches, classifications, title matches, Radarr and Sonarr grabs and imports, feed listings, probes, the web UI's requests for peers, and its deletion. Entries are kept for `SNIFFARR_RETENTION_JOURNAL`, `720h` by default, and `sniffarr.store.journal.deletions` counts those deleted.
- The release card shows the journal in rows by day: a fetch or a probe with all its attempts, or a title's feed listings, form one row that opens on its steps, particulars and tracks. Failures are marked amber or red, in words for screen readers too, and the heading counts those no later entry settled.
- The release card shows the journal's newest 5 rows and loads older entries in place, 200 at a time. The card of an infohash with no stored torrent shows its journal, says what ended it, and notes when retention may have cut its start.
- `BrowseService.Journal` returns the newest journal entries, up to 1000, of one infohash or of all, narrowed by `kind` and `outcome` and paged by `before`; for one infohash it returns `total` and `older`. Each entry carries its `id` and `infohash`.
- `BrowseService.Card` returns the newest 200 journal entries as `journal`, with `journal_total`; `Settings` reports `journal_retention`.
- Every response of the API and the web UI carries the header `Server: sniffarr/<version>`, and the process logs its version, commit and build time at start.
- The image takes the build arguments `VERSION`, `COMMIT` and `BUILD_TIME`; a build without them reads `dev`.
- The web UI's footer shows the version, linking its release notes, or the project page for a build without a release's version, with the build time and commit in a tooltip.
- The API reads the latest stable release from the releases feed, `https://sarak.sh/mikluko/sniffarr/releases.rss`, once a day and reports it as `BrowseService.Settings.latest_release`, with `release_notice`; the web UI's footer announces it beside the version.
- `SNIFFARR_RELEASE_CHECK` is `lazy` by default, announcing a release when the build is behind it by a major or by more than one minor version, or is a pre-release of it or of an earlier one; `eager` announces any newer stable release, and `no` or `off` turns the check off.
- `-wait-api` polls the API at `SNIFFARR_URL` every 2 seconds and exits once its `Server` header names the binary's own version. The chart runs it as the init container `wait-api` of the ui, sniff, fetch and probe pods, turned off with `waitForAPI.enabled: false`.
- `-port-offset` adds a number, such as a StatefulSet pod's index, to `-port`.
- `SNIFFARR_PROBE_BATCH` sets how many probe tasks asked for at once are handed out together, 64 by default.
- `BrowseService.Settings` reports `probes`, and `features`: for the classifier, probes, audio and tracker feeds, whether each is on, whether it works now, and the flag or variable that turns it on. The data page lists the features likewise.
- `BrowseService.Stats` reports `probers`, the fetchers that asked for a probe task in the last 15 minutes, and `counts_interval`, how often the counts are recomputed.
- `SNIFFARR_CLASSIFY_URL` sets the classifier's System One endpoint; an endpoint other than TypeSafe's turns the classifier on with or without a key. `SNIFFARR_CLASSIFY_API_KEY` sets the key sent to it.
- A release card offers "Match now" for a movie or series matched to no work: it looks the title up on TMDB at once and answers with the work found or that TMDB still finds nothing. `BrowseService.RequestMatch` does the same.
- With `SNIFFARR_RADARR_URL` or `SNIFFARR_SONARR_URL` set for the UI, the title page and the release card link a matched movie in that Radarr and a series in that Sonarr.
- `SNIFFARR_RADARR_API_URL` and `SNIFFARR_SONARR_API_URL` name the root of the app's v3 API where the API reaches it by another address than the root URL, such as a cluster service.
- The top reads as a chart: the change against the previous window ("=", "new", "re-entry"), time in the top, peak and debut, from a history of places kept from this version on; a work already in the top when the history began shows its debut as "≤ date".
- `BrowseService.Top` returns each entry's `debut`, `on_chart`, `peak` and `reentry`, and `history_since`; `Title` returns `peaks` per window, which a movie or series page shows as a "Peak" row.

### Changed

- **Breaking:** probes no longer turn on with `-probe-dir` alone, and `-probe-dir`, `-probers`, `-probe-timeout`, `-seed-max-size` or `-seed-max-age` without `-probe` stops sniffarr at start: add `-probe` to the fetchers that probe and to the process running `-api`. With the Helm chart, `probe.enabled` renders it. While the API does not probe, `RequestProbe` fails with `FAILED_PRECONDITION` and the web UI hides the probe button, meter and queue card.
- `-probe-dir` is optional: without it, `-fetch -probe` keeps the pieces in a temporary directory removed at stop.
- **Breaking:** TMDB matching is part of the core and cannot be turned off: an empty `TMDB_API_KEY` uses the built-in token instead of turning matching off.
- **Breaking:** the chart's probe StatefulSet is governed by the headless Service `<release>-probe`, not `<release>-probe-pods`; delete the StatefulSet once with `kubectl delete statefulset <release>-probe --cascade=orphan` before the upgrade.
- **Breaking:** with a fixed port, each probe replica listens on the port plus its index, and `probe.service` is a Service per replica, `<release>-probe-<index>`, on that port and on `probe.service.nodePort` plus the index.
- The chart runs the API as a StatefulSet of one, `<release>-api`, in place of a Deployment, so a replacement starts only once the old pod is gone; a command naming `deployment/<release>-api` names `statefulset/<release>-api`. With `api.persistence.type: pvc` and no `existingClaim`, the claim `<release>-api` of an earlier install stays `/data`, kept on uninstall, and a new install's is `data-<release>-api-0`.
- The API keeps its last counts in the database and, after a restart, serves them and answers `/readyz` at once, until its first computation replaces them; the data page marks them stale by their age.
- The table `settings` is named `state`; a migration renames it.
- The classifier's layer and journal outcome `jev` are `classifier`, in the API, the metrics' `sniffarr.classify.layer` and `sniffarr.tmdb.layer`, and the stored data.
- `BrowseService.Settings` names `SNIFFARR_CLASSIFY_API_KEY` as the classifier's setting, `SNIFFARR_CLASSIFY_URL` with another endpoint, and `TYPESAFE_API_KEY` only when that key turned it on. The web UI shows the classifier turned off as "off", and names it without Jev, as the docs do.
- Metrics carry the instrumentation scope `sarak.sh/mikluko/sniffarr/...`, not `github.com/mikluko/sniffarr/...`.
- The web UI has a new look: JetBrains Mono served by sniffarr itself, neutral colours with blue for data and links, amber for a state or a failure that is retried, red for a final failure, and borders only on blocks with a state of their own and on controls. The dark theme keeps 0.43.0's background, surfaces and lines.
- Explanations open as a tooltip on a dotted-underlined label, on hover or focus; the notes under the data page's task queue and the top's controls are such tooltips. The data page's chart bars show their value in the web UI's own tooltip, opened by hover or a tap.
- Labels and headings are in sentence case, and controls are at least 40 px tall on a phone.
- The header's tabs are Torrents and Top; the logo leads to the data page. A movie or series page links back to the top, the search results or the release it was reached from.
- The web UI answers a path it does not serve, and an unknown movie, series or infohash, with a 404 page naming the path and linking home.
- The data page leads with its figures and the classifier and probe meters, then the task queue as one card per kind with its priority bar; the current hour's and day's projection is a dashed outline. The store is a grid of three tables, each row with its share as a bar. A refresh button sits at the top, and counts older than two of their recomputation intervals show "updated N min ago" beside it in amber.
- On the data page, the classifier's meter is left out while it is off and turns amber with its daily limit spent, saying how many torrents wait until 00:00 UTC. With probes on and no prober connected, the probe queue card says "No probers", and "Probe tracks" answers that the probe starts once a prober connects.
- The priority badges in the data page's tables have no border: a square of the priority's colour and the label, with the meaning in the tooltip.
- The release card follows the new design: how the title was matched is in a tooltip, size and file count share a line, an action's answer shows under the buttons, and the infohash and magnet copy buttons are icons.
- The torrents page fits its table at 1280 px, cuts names at two lines with the movie or series linked under them, and shows a torrent's kind alone, with the deciding layer and the classifier's confidence in its tooltip. A set filter is bordered, "Reset filters" shows only then, and on a phone the filters sit behind a "Filters · N" button.
- A search on the torrents page, and `BrowseService.Torrents`, finds a release by the titles of its matched movie or series too, original or translated.
- A movie or series page sets a 185 px poster, 96 px on a phone, beside the TMDB details, lists the place in the top, peers, releases and dub as a table, and answers "Refresh peers" and "Probe tracks" beside the buttons. Its releases are filtered by season and quality, with size and episode as fields, the episode as `E05` or `5`, and sort by "peers, 7 days", "first seen" and "size".
- The top filters by segmented switches and frames a work with no poster dashed; on a phone it shows a title's first-entry date in its meta line while the history is short.
- The web UI writes dates one way per language ("Nov 7, 16:10" in English, "07.11 16:10" in Russian), groups the thousands of every count, and writes a place in the top as "#2" or "№2".
- In English, the web UI calls dubbing "Voice-over", with the configured language, the title-reuse layer "same title", and the title page's facts "Chart position"; the voice-over filter reads "any / ≥ 0.5 / explicit" on the torrents and top pages alike. TMDB genres are lower-cased.

### Deprecated

- `TYPESAFE_API_KEY`: use `SNIFFARR_CLASSIFY_API_KEY`; it is still read when that is unset, and sent to TypeSafe's endpoint only.

### Removed

- The top's audio and sources columns.
- The source and dub filters and columns of a movie's or series' releases; the torrents page and a release's card keep them.

### Fixed

- Behind a NodePort, the DHT names a probe's seeds at the node port, not at a port the masquerade picked.
- Fetches no longer slow down while many fetchers ask for probe tasks.
- The web UI asks the API for its settings every minute, so a UI started before a new API no longer shows probes, audio languages and features as off until restarted.
- The torrents page's layer filter no longer offers the classifier while it is off.
- The task queue's tables on the data page are no longer cut off on the right at 1280 px and on a phone.
- The magnet link on the release card opens the torrent in a client and carries its name.
- The probe button says when a release has no file a probe can read, rather than reporting a probe that never runs.
- Releases that had failed probes before a successful one, probed before 0.43.0, get their full number of probe attempts.
- A probe that reads more than 128 tracks is recorded as unknown, so a crafted file header cannot stall the database.

## [0.44.0-beta.8] - 2026-11-08

### Added

- `-port-offset` adds a number, such as a StatefulSet pod's index, to `-port`.

### Changed

- **Breaking:** the chart's probe StatefulSet is governed by the headless Service `<release>-probe`, not `<release>-probe-pods`; a StatefulSet installed by an earlier version is deleted once with `kubectl delete statefulset <release>-probe --cascade=orphan` before the upgrade.
- **Breaking:** with a fixed port, each probe replica listens on the port plus its index, and `probe.service` is a Service per replica, `<release>-probe-<index>`, on that port and on `probe.service.nodePort` plus the index.
- In an open journal row, the particulars and the steps' text start under the outcome column, their keys and times under the event column.
- The journal's heading counts a failure only while no later entry of its task has settled it: a fetch or probe that ended a later chain, or a later entry of the same kind.
- A time in a journal row's particulars leaves out the year when it is the current one.

## [0.44.0-beta.7] - 2026-11-08

### Fixed

- The chart's API StatefulSet upgrades: its claim template carries no chart or app version label. A StatefulSet installed by 0.44.0-beta.5 or beta.6 is deleted once with `kubectl delete statefulset <release>-api --cascade=orphan` before the upgrade; its pod and claim stay.

## [0.44.0-beta.6] - 2026-11-07

### Added

- `Journal` returns every infohash's events when `infohash` is empty, narrows them by `kind` and by `outcome`, and names each event's `infohash`.

### Changed

- The table `settings` is named `state`; a migration renames it.

## [0.44.0-beta.5] - 2026-11-07

### Changed

- The API keeps its last counts in the database and, after a restart, serves them and answers `/readyz` at once, until its first computation replaces them; the data page marks them stale by their age.

## [0.44.0-beta.4] - 2026-11-07

### Changed

- With `api.persistence.type: pvc` and no `existingClaim`, a new install's `/data` is the StatefulSet's claim `data-<release>-api-0`; a claim `<release>-api` an earlier chart made stays `/data`, and the chart keeps it on uninstall.
- **Breaking:** a release on 0.44.0-beta.3 that moves to the StatefulSet's claim deletes the StatefulSet with `--cascade=orphan` before upgrading, its claim template being immutable.

## [0.44.0-beta.3] - 2026-11-07

### Changed

- The chart runs the API as a StatefulSet of one, `<release>-api`, in place of a Deployment, with the same volume, so a replacement starts only once the old pod is gone; a command naming `deployment/<release>-api` names `statefulset/<release>-api`.

## [0.44.0-beta.2] - 2026-11-07

### Changed

- The release check reads the latest stable release from the releases feed, `https://sarak.sh/mikluko/sniffarr/releases.rss`, the highest stable version among its items; an RSS or Atom feed of releases reads the same.
- The data page's refresh tooltip says how often the server recomputes the data.
- In English, the title page's facts say "Chart position".
- A search on the torrents page, and `BrowseService.Torrents`, finds a release by the titles of its matched movie or series too, original or translated, whatever its own name.

### Fixed

- The data page's chart bars stand 2 px apart, evenly, at every width.

## [0.44.0-beta.1] - 2026-11-07

### Added

- `SNIFFARR_CLASSIFY_URL` sets the classifier's System One endpoint; an endpoint other than TypeSafe's turns the classifier on with or without a key.
- `SNIFFARR_CLASSIFY_API_KEY` sets the key sent to the classifier's endpoint.
- `-wait-api` polls the API at `SNIFFARR_URL` every 2 seconds and exits once its `Server` header names the binary's own version.
- The chart gives the ui, sniff, fetch and probe pods an init container `wait-api` that holds each pod until its API serves the same version, on by default and turned off with `waitForAPI.enabled: false`.
- The web UI's footer version links to its release notes, or to the project page for a build without a release's version, its tooltip saying which beside the build time and commit; the footer's tooltips open upwards.
- The API checks the latest stable release on forge.sarak.sh once a day and reports it as `BrowseService.Settings.latest_release`, with `release_notice` saying when to announce it.
- The web UI's footer announces a newer stable release left of the version, linking its release notes, when the build is behind it by more than one minor version or by a major one, or is a pre-release of that version or an earlier one.
- `SNIFFARR_RELEASE_CHECK` is `lazy` by default; `eager` announces any newer stable release, and `no` or `off` turns the check off.

### Changed

- The data page's chart bars show their value in the web UI's own tooltip above the bar, opened by hover or a tap, in place of the browser's.
- The web UI shows the classifier turned off as "off" rather than "no key".
- The classifier's layer and journal outcome `jev` are `classifier`, in the API, the metrics' `sniffarr.classify.layer` and `sniffarr.tmdb.layer`, and the stored data.
- `BrowseService.Settings` names `SNIFFARR_CLASSIFY_API_KEY` as the classifier's setting, `SNIFFARR_CLASSIFY_URL` with another endpoint, and `TYPESAFE_API_KEY` only when that key turned it on.
- The web UI and the docs name the classifier without Jev.
- The priority badges in the data page's tables have no border: a square of the priority's colour and the label, with the meaning in the tooltip.
- The web UI writes dates one way per language, on the charts' axes and tooltips as on the cards: "Nov 7" and "Nov 7, 16:10" in English, "07.11" and "07.11 16:10" in Russian, with the year in full timestamps.
- The web UI groups the thousands of every count, the release card's releases and files and the pagers included.
- The web UI writes a place in the top as "#2" in English and "№2" in Russian everywhere.
- The voice-over filter reads "any / ≥ 0.5 / explicit" ("любая / ≥ 0,5 / явно") on the torrents and top pages alike.
- A title's releases sort by "peers, 7 days", "first seen" and "size", worded as on the torrents page.
- In English, the web UI calls dubbing "Voice-over", with the configured language ("Voice-over: ru"), and the title-reuse layer "same title".
- In English, the data page reads "last 7 days:" under today's count and "books" among dropped torrents.
- TMDB genres are lower-cased.
- The top's "Change" tooltip explains "—" as a history not yet built up.
- On a phone, the top shows a title's first-entry date in its meta line while the history is short.

### Deprecated

- `TYPESAFE_API_KEY`: use `SNIFFARR_CLASSIFY_API_KEY`; it is still read when that is unset, and sent to TypeSafe's endpoint only.

### Fixed

- With audio off, the release card's journal no longer shows past audio probabilities.

## [0.44.0-alpha.4] - 2026-11-07

### Added

- The release card's journal reads in rows by day: a fetch or a probe with all its attempts, or a title's feed listings, form one row that opens on its steps, particulars and tracks; failures are marked amber or red, in words for screen readers too, and the heading's tooltip gives the retention.
- The card shows the journal's newest 5 rows and loads the rest in place, older entries 200 at a time.
- The card of an infohash with no torrent says what ended it, and notes when retention may have cut the start of its journal.
- A request for peers from the web UI is recorded in the journal.
- `BrowseService.Journal` takes `before` and returns `total` and `older`; `Card` returns `journal_total`; journal entries carry `id`; `Settings` reports `journal_retention`.
- `SNIFFARR_PROBE_BATCH` sets how many probe tasks asked for at once are handed out together, 64 by default.

### Fixed

- Fetches no longer slow down while many fetchers ask for probe tasks.

## [0.44.0-alpha.3] - 2026-11-07

### Added

- Every response of the API and the web UI carries the header `Server: sniffarr/<version>`, and the process logs its version, commit and build time at start.
- The footer of every page of the web UI shows its version, with the build time and commit in a tooltip.
- The image takes the build arguments `VERSION`, `COMMIT` and `BUILD_TIME`, which the release sets, as it sets them for the archives and packages; a build without them reads `dev`.
- A movie or series page links back to the top, the search results or the release it was reached from.
- The top reads as a chart: the change against the previous window ("=", "new", "re-entry"), time in the top, peak and debut, from a history of places kept from this version on; a work already in the top when the history began shows its debut as "≤ date".
- `BrowseService.Top` returns each entry's `debut`, `on_chart`, `peak` and `reentry`, and `history_since`; `Title` returns `peaks` per window.

### Changed

- The header's tabs are Torrents and Top; the logo leads to the data page, which selects no tab, and a movie or series page selects none.
- The torrents page shows a torrent's kind alone; which layer decided it, with the classifier's confidence, is in the value's tooltip.
- The data page shows the store as a grid of three tables, each row with its share as a bar; a table of a feature turned off leaves its cell empty, and the dropped table has no total row.
- The tooltip of the current hour on the data page says its pace is over the last hour and so far this hour.
- sniffarr started on a database whose schema is newer than its migrations stops with an error naming both versions instead of failing later on a missing table; to roll back, restore a backup taken before the upgrade.
- A movie or series page has a 185 px poster, 96 px on a phone, and a "Peak" row, its best place in each top since the history began.

### Removed

- The top's audio and sources columns.
- The source and dub filters and columns of a movie's or series' releases; the torrents page and a release's card keep them.

### Fixed

- The web UI asks the API for its settings every minute, so a UI started before a new API no longer shows probes, audio languages and features as off until restarted.
- The language switch no longer wraps the header onto a second line at 375 px.
- The torrents page's layer filter no longer offers the classifier while it is off.

## [0.44.0-alpha.2] - 2026-11-07

### Added

- `BrowseService.Settings` reports `features`: for the Jev classifier, probes, audio and tracker feeds, whether each is on, whether it works now and the flag or variable that turns it on.
- `BrowseService.Stats` reports `probers`, the fetchers that asked for a probe task in the last 15 minutes.
- The data page lists the optional features with their state and how to turn each on.
- With the classifier's daily limit spent, its meter is full and amber and says how many torrents wait until 00:00 UTC.
- With probes on and no prober connected, the probe queue card says "No probers" with no ETAs, and "Probe tracks" answers that the probe starts once a prober connects.

### Changed

- With the classifier off, the data page leaves out its meter and its row among the layers; with no feeds, P2 reads "feeds (off)".

- The dark theme keeps 0.43.0's background, surfaces and lines, and buttons, selects, inputs and switches have borders of their own shade.
- The torrents page fits its table at 1280 px, cuts names at two lines with the movie or series linked under them, and shows the first-seen date in the tooltip on "Last seen".
- The torrents page explains in tooltips what "?" and "—" in place of a poster mean and which layer decided a torrent's kind, with the classifier's confidence.
- The torrents page borders a filter that is set and offers "Reset filters" only then, keeping the search and the sort; on a phone the filters sit behind a "Filters · N" button.
- The movie and series page sets the poster beside the TMDB details, lists the place in the top, peers, releases and dub as a table, and answers "Refresh peers" and "Probe tracks" beside the buttons.
- A movie's or series' releases are filtered by segments for season, quality, source and dub, with size and episode as fields; "Reset filters" shows only while a filter is set, and on a phone the filters fold behind "Filters · N".
- The episode filter takes `E05` as well as `5`.
- The top's "±" reads "new" in bold for a work below 100th in the previous window or absent from it, with its previous place in a tooltip.
- The top filters by segmented switches, shows audio on one line, "ru explicit 2 of 95 · likely 7 more", with the marks in a tooltip, and a dashed frame for a work with no poster.
- The web UI answers a path it does not serve, and an unknown movie, series or infohash, with a 404 page naming the path and linking home.

## [0.44.0-alpha.1] - 2026-11-07

### Added

- `-probe` turns probing on, probe tasks and seeding; it is off by default, `-api` and `-fetch` both read it, and the web UI hides the probe button, meter and queue card while the API does not probe. Probe results already stored still show.
- `BrowseService.Settings` reports `probes`; `RequestProbe` fails with `FAILED_PRECONDITION` while probes are off.
- A release card offers "Match now" for a movie or series matched to no work: it looks the title up on TMDB at once and answers with the work found or that TMDB still finds nothing; `BrowseService.RequestMatch` does the same.
- What happens to an infohash is recorded in the new table `torrent_journal`, one row per event in the transaction of the change: queued, promoted, leased, failed, dropped and evicted for one sighted by announce, a feed or Radarr and Sonarr, never for the queue's events of one only sampled; metadata stored, rejected or already stored; classifications by Jev with who asked, kinds copied from a release of the same title and failed classifications; title lookups' matches, no matches and errors with who asked, and the earlier match a release takes from its title as it is stored; grabs and imports; the first listing in each feed, journaled after it is recorded, in a transaction of its own; probes wanted, started and done; deletion. `Card` carries the newest 200 entries as `journal`, the `Journal` RPC of `BrowseService` returns up to 1000 for any infohash, stored or not, and the release card shows them under the classification; an infohash whose torrent is not stored has a card of its journal alone. Entries are kept for `SNIFFARR_RETENTION_JOURNAL`, `720h` by default, and `sniffarr.store.journal.deletions` counts those deleted.
- With `SNIFFARR_RADARR_URL` or `SNIFFARR_SONARR_URL` set for the UI, the title page and the release card link a matched movie in that Radarr and a series in that Sonarr.
- `BrowseService.Stats` returns `counts_interval`, how often the counts are recomputed.
- `SNIFFARR_RADARR_API_URL` and `SNIFFARR_SONARR_API_URL` name the root of the app's v3 API where the API reaches it by another address than the root URL, such as a cluster service; unset, the API uses `/api/v3` under `SNIFFARR_RADARR_URL` or `SNIFFARR_SONARR_URL`, as before.

### Changed

- **Breaking:** probes no longer turn on with `-probe-dir` alone, and `-probe-dir`, `-probers`, `-probe-timeout`, `-seed-max-size` or `-seed-max-age` without `-probe` stops sniffarr at start: add `-probe` to the fetchers that probe and to the process running `-api`. With the Helm chart, `probe.enabled` renders it.
- `-probe-dir` is optional: without it, `-fetch -probe` keeps the pieces in a temporary directory removed at stop.
- **Breaking:** TMDB matching is part of the core and cannot be turned off: an empty `TMDB_API_KEY` uses the built-in token instead of turning matching off.
- The release card follows the new design: how the title was matched is in a tooltip, size and file count share a line, and an action's answer shows on a line under the buttons with the pressed button disabled; the peer lookup's answer gives its priority and links the queue on the data page.
- The web UI has a new look: JetBrains Mono served by sniffarr itself, neutral colours with blue for data and links, amber for a state or a failure that is retried, red for a final failure, and borders only on blocks with a state of their own.
- Explanations open as a tooltip on a dotted-underlined label, on hover or focus; the notes under the task queue on the data page and under the top's controls are such tooltips.
- The infohash and magnet copy buttons on a release card are icons; the tooltip confirms the copy.
- Labels and headings are in sentence case, and controls are at least 40 px tall on a phone.
- The data page leads with its figures and the classifier and probe meters, then the task queue as one card per kind with its priority bar and each priority's meaning in a tooltip on its badge; the current hour's and day's projection is a dashed outline.
- The data page has a refresh button; counts older than two of their recomputation intervals show "updated N min ago" beside it in amber.

### Fixed

- The task queue's tables on the data page are no longer cut off on the right at 1280 px and on a phone.
- The magnet link on the release card opens the torrent in a client and carries its name; it led nowhere.
- The probe button says when a release has no file a probe can read, rather than reporting a probe that never runs.
- Releases that had failed probes before a successful one, probed before 0.43.0, get their full number of probe attempts.
- A probe that reads more than 128 tracks is recorded as unknown, so a crafted file header cannot stall the database.

## [0.43.0] - 2026-11-07

### Added

- The API logs each migration as it begins and as it ends, with how long it took.

### Changed

- **Breaking:** `BrowseService.Stats` replaces `queue` with `queue_fetches`, `queue_lookups` and `queue_probes`, one `TaskQueue` per kind with its pace and its levels.
- The data page shows the task queue per kind: metadata fetches, peer lookups and probes each get their own bar split by priority, with how long until each priority is done at that kind's own pace.
- Torznab titles carry every voice label of the Russian track, even one the release name already has, as in `HIN.DUB [Dub, RUS]`.

### Fixed

- Torznab titles write `[RUS, -]` instead of a bare `[RUS]`, which Radarr read as the release group.
- A Russian word in a subtitle file's path no longer marks a release as having Russian audio; stored markers from subtitle paths alone are cleared.
- A release asked for again after a successful probe gets its full number of probe attempts.
- Searches no longer queue probes of releases a probe cannot read (AVI only, BDMV, ISO, not a movie or series), and such probes already queued leave the queue.
- A probe requested from the web UI while an attempt is running is no longer lost when that attempt fails.
- A release whose probe found audio tracks, all in other languages, is titled `[NORUS …]` rather than `[RUS, -]` from a word in its name, unless it has an audio file of its own.
- The top opens in a fraction of a second instead of about four seconds.
- Probe results, completions and other writes no longer time out while the tops are recomputed.
- A slow or locked database no longer fails the API's start: leftover leases are freed before it.
- The Helm chart gives the API an hour to start before its liveness probe runs, so a long migration is no longer killed.

## [0.42.0] - 2026-11-07

### Added

- Release cards and movie and series pages have a button that asks a fetcher to look up the peers of the release, or of every release of the title, on the DHT now: `BrowseService.RefreshPeers`.
- Release cards and movie and series pages have a button that asks for a probe of the release, or of every release of the title, ahead of the others and whatever its last probe: `BrowseService.RequestProbe`. The button is disabled without peers over 7 days.
- The data page shows the task queue as a bar split by priority from P0 to P4, with the fetches, peer lookups and probes at each priority and how long until each is done at the pace of the last 15 minutes. `BrowseService.Stats` returns the same as `queue`.

### Changed

- `sniffarr.queue.leases` counts the leases of probes too, with `sniffarr.task.kind` telling fetches, peer lookups and probes apart.

## [0.41.0] - 2026-11-06

### Changed

- The moment of a feed's rounds within `SNIFFARR_FEED_INTERVAL` is fixed by the feed's name and the database's own key, so two instances read one feed at different moments; it may fall on the hour.

### Fixed

- A feed's rounds fell within seconds of the hour, every feed's together; the moment is now spread over the whole `SNIFFARR_FEED_INTERVAL`.

## [0.40.0] - 2026-11-06

### Added

- `SNIFFARR_FEEDS` takes `nnmclub`, the RSS of the forums of `SNIFFARR_NNMCLUB_FORUMS` at `SNIFFARR_NNMCLUB_URL` with each topic's page read once for its magnet link, and `rutor`, the listing pages of the categories of `SNIFFARR_RUTOR_CATEGORIES` at `SNIFFARR_RUTOR_URL`, `SNIFFARR_RUTOR_DEPTH` pages deep on the first pass.
- The Russian translation codes of release names, `D`, `P`, `P1`, `P2`, `L`, `L1`, `L2`, `A` and `Pm`, after the year and before the source or listed between bars, are markers of Russian audio, and the Torznab title labels them as `Dub`, `MVO`, `DVO`, `AVO` or `VO`: `Movie.2026.P.WEB-DL.1080p [MVO, RUS]`. Stored releases with a code in the name get the marker once at start. Jev's question about Russian audio names the codes too, for the names where they stand elsewhere.

### Changed

- A feed is read at start and then at every `SNIFFARR_FEED_INTERVAL` of wall-clock time plus a moment fixed by the feed's name, the same across restarts and never on the hour.
- The `rutracker` feed reads, once per database, the feed of every forum the forums of `SNIFFARR_RUTRACKER_FORUMS` name in their categories, a month of each.
- Every page a feed fetches is recorded in the database by URL, with its `ETag` and `Last-Modified`: a page read every round is skipped when unchanged, a page read once is not read again, and requests of one feed are a second apart.
- A year or a resolution with a zero written as the letter O, `2O17`, `1O8Op` or `72Ор`, gives the release its year and quality, and the Torznab title and category give them in digits, `2017`, `1080p`, for Radarr and Sonarr. Stored releases without a year or a quality are read again once at start.

## [0.39.0] - 2026-11-06

### Changed

- **Breaking:** the repository is `sarak.sh/mikluko/sniffarr`, the image `sarak.sh/mikluko/sniffarr:X.Y.Z` and the chart `oci://sarak.sh/mikluko/charts/sniffarr`; the `arr` paths serve the versions before this one.

### Added

- `SNIFFARR_FEEDS` names tracker feeds the API reads every `SNIFFARR_FEED_INTERVAL`: `rutracker`, the forums of `SNIFFARR_RUTRACKER_FORUMS` at `SNIFFARR_RUTRACKER_FEED_URL`. A release listed is kept with the feed's title and page, and queued under the discovery `feed`, after `arr`, when the store lacks it.
- A release carries static Linux binaries for amd64 and arm64, each in an archive with a systemd unit and a settings file, and a `.deb` of the same in the sarak.sh Debian registry; `install.sh` installs one or the other as the service `sniffarr`. Guides under `docs/install` cover Debian/Ubuntu, systemd, Docker Compose and Kubernetes, one process or one container or service per component.

## [0.38.0] - 2026-11-06

### Added

- With `SNIFFARR_SEERR_URL` set, the title page and the release card link a matched title in that Seerr.

### Changed

- A title's page is `/movie/<tmdb id>` or `/tv/<tmdb id>`, the paths Seerr gives the same titles; `/w/<id>` and `/top/w/<id>` redirect there.

## [0.37.0] - 2026-11-05

### Changed

- A `hostPath` of `probe.persistence` takes any number of `probe.replicas`: each pod's `/seeds` is `<path>/<pod>` under it, and `probe.extraInitContainers` run before the prober, for a deployment to make that directory the pod's.

## [0.36.0] - 2026-11-05

### Added

- The classifier's tokens and the probes' bytes on the data page show what they come to by the end of the UTC day at their pace so far, as a dashed outline on the meter and a share of the limit, once a tenth of the day has passed.

### Changed

- **Breaking:** the chart probes from `<release>-probe`, a StatefulSet of `probe.replicas` fetchers that probe too, each with a `/seeds` of its own, so `fetch.replicas` is free again; `fetch.probe` is `probe`, with the pod settings of `fetch`. A claim is `seeds-<release>-probe-<n>` per replica, or `<release>-seeds` in single mode; `persistence.existingClaim` is gone.

### Fixed

- The dates under the charts on the data page sit under their bars; they were spread evenly along the axis, a day or more off their bars.

## [0.35.2] - 2026-11-05

### Added

- The current hour, day and week on the data page are outlined up to a projection, the mean of the count over the same length of time ending now and of the period's pace so far, the pace counted once a tenth of the period has passed; the tooltip names the figure.

## [0.35.1] - 2026-11-05

### Changed

- The torrents page without a filter takes its total from the counts snapshot, up to the stats interval old, instead of counting the table on every view.
- The API answers `/readyz` with 503 until its first counts snapshot exists; the chart's readiness probe asks it, liveness stays on `/healthz`. The first top recount starts two minutes after start.

### Fixed

- The first counts snapshot after a start took minutes: counting first sightings per hour sorted every release through a temp B-tree. It reads the index in order.

## [0.35.0] - 2026-11-05

### Added

- The data page charts new torrents per hour over the last 48 hours, with the last 24 hours' total.

### Changed

- The data page's daily and weekly charts count calendar days and weeks, Monday first, in the browser's time zone, which the page's script puts in the `tz` cookie; the current hour, day and week are drawn lighter and marked in their tooltips. "New today" and "7 days" count calendar days. They counted 24-hour periods back from the computation.
- **Breaking:** the `Stats` RPC takes `hours` and returns `hours`, counts per UTC clock hour, the current hour first; `days` is gone.

## [0.34.1] - 2026-11-04

### Changed

- A work with no peers in the previous window shows "—" in the top's places column, not "new".

### Fixed

- The ten-minute top recount in 0.34.0 took over ten minutes, ranking the previous window once per kind; the tops showed every work as new until its first run was through. It unions peers once per voice and ranks both kinds from that.

## [0.34.0] - 2026-11-04

### Changed

- The top's column against the previous window shows the places a work moved, "+3" or "−1", and "new" for a work with no peers then; it showed the change of peers as a percentage. The `Top` RPC's entries carry `prev_rank` instead of `delta`.

## [0.32.2] - 2026-11-04

### Fixed

- The ten-minute recount of peers over 7 days writes only the rows that change, 500 a transaction; it held the database's one writer for some 20 seconds, and every probe or fetch result arriving meanwhile was lost to its 5-second deadline.

## [0.32.1] - 2026-11-04

### Fixed

- Releases never probed go before those due a probe again, and a release out of attempts is due again only with twice the peers over 7 days it had at its last probe and at least two more; in 0.31.0 the releases let back in, mostly without live peers, filled the queue ahead of new ones.

## [0.32.0] - 2026-11-04

### Added

- A probe adds the studios named in the audio tracks of a configured language, as `MVO LostFilm`, to the release's studios for the language, so Torznab titles carry them without a mention in the release's name. A one-time backfill does the same for the releases probed before.

## [0.31.0] - 2026-11-04

### Changed

- A release whose `SNIFFARR_PROBE_ATTEMPTS` probes have failed is probed again once it has more peers over 7 days than at its last probe, after the last backoff, rather than never; releases written off before are due again on the same terms.

## [0.30.0] - 2026-11-02

### Changed

- The web UI is set in the system's monospace face, SF Mono on macOS and Consolas on Windows; it no longer looks for MonoLisa, JetBrains Mono, IBM Plex Mono or Source Code Pro.

## [0.29.0] - 2026-11-02

### Added

- The data page shows the piece data probes downloaded on the UTC day against `SNIFFARR_PROBE_DAILY_BYTES`, and what the fetchers uploaded back.
- `ProbeUpload`: a fetcher with `-probe-dir` reports once a minute the piece data it uploaded to the swarms of probed releases.
- `Stats` carries `probe_bytes_rx_day`, `probe_bytes_tx_day` and `probe_bytes_limit_day`.
- `sniffarr.probe.uploaded`, the piece data the fetchers reported uploading.

### Changed

- The pages link the stylesheet, the scripts and the TMDB logo with a version in `v` that changes with the file; a static file at its version is cached for a year, at any other URL the browser revalidates it by ETag.

### Fixed

- `sniffarr.seed.uploaded` no longer counts the upload before a restart again at the first check after it.

## [0.28.0] - 2026-11-01

### Changed

- The data page is the web UI's first page, at `/`, and leads the navigation: data, torrents, top. The search box lives on the torrents page only.

### Removed

- The web UI's home page and its search of movies and series, with the titles found only on TMDB. `GET /data` answers 404, and `q` on `/` is ignored.

## [0.27.3] - 2026-11-01

### Fixed

- The API picks the next release a search wants probed from the wanted releases alone; it took some 30 seconds a task, walking every release.

## [0.27.2] - 2026-11-01

### Changed

- Torznab titles of probed releases with no Russian track open their bracket with `NORUS`, as `[NORUS FRA, ENG]`, so a custom format refuses them whatever languages they have.

## [0.27.1] - 2026-11-01

### Fixed

- The chart replaces the fetch pod with probes on by stopping the old one first, so one process at a time holds the pieces in `/seeds`.

## [0.27.0] - 2026-11-01

### Added

- `-fetch -probe-dir` probes stored releases for their tracks: it downloads, hash-verified, the pieces at the start of a release's largest Matroska or MP4 file and reads its track list, each audio track's language, name, codec and channels and the video's size and HDR or Dolby Vision. Releases a Radarr or Sonarr search returned go first, then the rest with the most peers over 7 days; releases without peers wait. Only movies and series are probed, each once; a probe whose peers or pieces do not come is repeated after 6 hours, doubled each time, until 3 have failed.
- The API meters probes at 100 GiB of piece data a UTC day, `SNIFFARR_PROBE_DAILY_BYTES`, of which 25 % is kept for releases a search returned, `SNIFFARR_PROBE_SEARCH_RESERVE`; one probe downloads 64 MiB at most, `SNIFFARR_PROBE_MAX_BYTES`. `SNIFFARR_PROBE_LEASE`, `SNIFFARR_PROBE_ATTEMPTS` and `SNIFFARR_PROBE_BACKOFF` tune the tasks.
- The fetcher seeds what it probed until each torrent uploaded as much as it downloaded, announcing it on the DHT, and removes a torrent earlier after `-seed-max-age`, 7 days by default, or oldest first beyond `-seed-max-size`, 200 GiB by default.
- `IngestService.ProbeTask` and `ProbeResult`, and `tracks` on `Release`.
- The metrics `sniffarr.probe.tasks`, `sniffarr.probe.results` and `sniffarr.probe.downloaded` of the API, and `sniffarr.sniff.probe.outcomes`, `sniffarr.seed.downloaded`, `sniffarr.seed.uploaded`, `sniffarr.seed.torrents`, `sniffarr.seed.stored` and `sniffarr.seed.removals` of the fetcher.
- The chart's `fetch.probe`, with a volume for the seeded pieces at `/seeds`.

### Changed

- A probe sets the probability of an audio track in a configured language to 1 when it found one, and to 0 when every audio track it found is in another language, unless a marker in the name says otherwise; the classifier no longer overrides either.
- Torznab titles of probed releases name what the Russian tracks' names say, as `[Dub, MVO, RUS]`, leaving out a label the name already has, and name the languages of releases with no Russian track, as `[ENG, UKR]`.
- The web UI is set in monospace — MonoLisa where installed, otherwise JetBrains Mono, IBM Plex Mono, Source Code Pro or the system monospace — with square corners, hairline table rules and an inverted search button. Titles of films and series are bold text instead of link-coloured, and charts and meters use the accent colour.

## [0.26.0] - 2026-10-27

### Added

- `sniffarr.db.wal.size`, the size of the write-ahead log in bytes.
- Once a minute the API truncates the WAL when it has reached 1 GiB; a reader that holds it past the 10-second wait is logged with the frames it pins.

### Changed

- The WAL file is truncated to 64 MiB after a checkpoint resets it.

### Fixed

- A search no longer classifies a release a second time when another search recorded its classification while the first was reading it.

## [0.25.2] - 2026-10-23

### Fixed

- A Torznab search without a query, which Radarr and Sonarr send for their RSS sync, lists releases newest found first rather than most recently seen, so a newly found release reaches the feed.

## [0.25.1] - 2026-10-23

### Fixed

- The API starts serving before the one-time backfills of stored releases finish.
- A backfill resumes where it stopped after a restart.

## [0.25.0] - 2026-10-23

### Changed

- Torznab titles end with `[RUS]` and the Russian dubbing studios found in the release's name or files, such as `[RUS LostFilm HDRezka]`, when the release has a Russian audio marker or the classifier gives Russian audio a probability of at least 0.9, so Radarr and Sonarr release-title custom formats can score it.

## [0.24.0] - 2026-10-22

### Added

- Calls to TypeSafe, TMDB and the Radarr and Sonarr APIs record OpenTelemetry client spans and carry the trace context.

### Changed

- Torznab answers with the classifications and TMDB matches already stored instead of waiting up to `SNIFFARR_SEARCH_CLASSIFY_WAIT` for them; the search still starts them in the background, and later searches return their results. Searches from the web UI wait as before.
- Searches running at the same time classify a release once between them.

## [0.23.1] - 2026-10-22

### Added

- Torznab and the Radarr/Sonarr webhook record OpenTelemetry server spans and continue the caller's trace.

## [0.23.0] - 2026-10-22

### Added

- The chart's NetworkPolicy `<release>-api`, `api.networkPolicy.enabled`, true by default: only the release's own pods and the NetworkPolicyPeers in `api.networkPolicy.from` reach the API port, 8080; the api pod's other ports stay open.

### Removed

- **Breaking:** `SNIFFARR_INGEST_TOKEN` and `SNIFFARR_SEARCH_TOKEN` are gone: `IngestService`, `SearchService` and `BrowseService` take no bearer token and authenticate no caller.
- **Breaking:** the chart values `ui.existingSecret`, `ui.secretKey`, `sniff.existingSecret`, `sniff.secretKey`, `fetch.existingSecret` and `fetch.secretKey`; set, they fail the render. `api.existingSecret` is optional.

## [0.22.1] - 2026-10-22

### Added

- Torznab items carry `seeders` and `peers`: the release's distinct peers over the last 7 days.
- `season` and `episode` in `SearchRequest`: only releases whose name gives the season, and with `episode` that episode and the season's whole packs.

### Fixed

- Torznab `tvsearch` honours `season` and `ep`: `season` selects the releases of that season, `ep` with it that episode and the season's whole packs, before `offset` and `limit` are applied; a name that gives no season matches none, and an invalid value answers error 201.

## [0.22.0] - 2026-10-22

### Added

- The Radarr and Sonarr webhook at `POST /webhooks/arr`, behind HTTP Basic with `SNIFFARR_WEBHOOK_TOKEN` as the password: Grab and Download events are recorded as grabs and imports in the new table `arr_events`, once per app, event and download ID; Test and other events are ignored.
- `SNIFFARR_RADARR_URL` and `SNIFFARR_RADARR_API_KEY`, `SNIFFARR_SONARR_URL` and `SNIFFARR_SONARR_API_KEY`: the API imports the app's grab history once, then every 24 hours the grabs since the latest recorded one.
- A grab or import with a TMDB ID, or Sonarr's TVDB ID looked up on TMDB, stores the work and matches the release of its infohash to it under the new layer `arr`, `TMDB_LAYER_ARR`, which no TMDB search or Jev choice replaces; an infohash not stored is queued with `SOURCE_ARR`, ahead of announced ones, and matched when stored.
- `grabs_week` in `WorkSummary`: the work's grabs over the last 7 days.
- Metrics `sniffarr.arr.events` and `sniffarr.arr.outcomes`.

## [0.21.0] - 2026-10-22

### Added

- `SNIFFARR_STATS_INTERVAL`, default `5m`: how often the API recomputes the counts of `BrowseService.Stats` and `Home`.
- `computed_at` in `StatsResponse` and `HomeResponse`; the data page shows how many minutes ago its counts were computed.

### Changed

- The data page and the home page are served from a snapshot of the store's counts that the API computes at start and then every 5 minutes, instead of counting at each request; only the tokens spent today are read at the request.
- Indexes `torrent_releases (source, kind_layer, kind)`, `torrent_releases (first_seen)` and `ingest_rejected (reason)` cover the counts by kind, layer and source, of new torrents per day, and of rejections by reason; a migration builds them at start.

## [0.20.0] - 2026-10-22

### Added

- SQLite statements are traced as child spans of the request that runs them, with the statement text and without its arguments, and measured in `db.client.operation.duration` by `db.pool`, `db.operation.name` and `code.function.name`; `db.sql.connection.*` reports both pools.

## [0.19.0] - 2026-10-22

### Fixed

- The chart's pods take `fsGroup` 65532, so `api.persistence.type: pvc` on a fresh volume owned by root is writable; litestream's restore failed on it with `permission denied`.

### Added

- `SNIFFARR_RETENTION_IDLE`, default `168h`: a stored torrent matched to no TMDB work is deleted once it has not been seen for that long; the others keep `SNIFFARR_RETENTION`. `sniffarr.store.retention.deletions` carries `sniffarr.store.retention.class`, `idle` or `expired`.

### Changed

- The dead set keys an infohash by its first 8 bytes, about 40 bytes an entry with its index instead of 68; a migration converts the stored entries, keeping the latest expiry of the hashes that share a key. Two infohashes that share the first 8 bytes are kept out of the queue together.

## [0.18.0] - 2026-10-21

### Changed

- The repository moves to `https://forge.sarak.sh/arr/sniffarr`, the image to `sarak.sh/arr/sniffarr` and the chart to `oci://sarak.sh/arr/charts/sniffarr`; the chart's default `image.repository` follows. Releases from 0.18.0 on are published there and no longer to `ghcr.io/mikluko`.

## [0.17.1] - 2026-10-21

### Fixed

- An audio mark names the studio when the name or a file has one, even after a word of the language: `Silo.S03E10.1080p.rus.LostFilm.TV.mkv` marks `LostFilm`, not `rus`; stored marks are recomputed once at the first start.

## [0.17.0] - 2026-10-21

### Added

- The release card lists the rules that recognised features in its name, with the matched fragments, and the audio markers found in the name or file paths; `CardResponse` carries them as `rules`, recomputed when the card is opened.

## [0.16.0] - 2026-10-21

### Added

- The web UI searches movies and series in the store and on TMDB together; what TMDB finds is listed after the movies and series with releases, and stored even without releases.
- A page per movie or series at `/w/{id}`, `m348` or `tv1399`: its TMDB details, its places in the tops over 24 hours, 7 days and 30 days, its peers, the audio of its releases, and its releases filtered by quality, source, size, audio, season and episode.
- The torrents tab at `/torrents`: every release, matched or not, filtered by kind, match, quality, audio, source, layer and size, each matched one linked to its movie or series.
- `BrowseService.SearchTitles`, `Title`, `TitleReleases` and `Torrents`.
- A release's quality, `2160p`, `1080p`, `720p` or `sd`, and its episode, read from its name; `Release` carries `quality`, `season` and `episode`, and stored releases get them at the first start.
- An audio marker records its mark, the word or studio as written; `Audio` carries `mark`, and stored markers get theirs at the first start.
- `TopRequest.voice_language` picks the audio language a voice applies to among the configured ones.

### Changed

- **Breaking:** `BrowseService` is reshaped for the movies and series of TMDB: `Card` returns the release's `work` and `rank_in_work` in place of `work`, `releases` and `release_rank`, `TopEntry` carries a `WorkSummary` with `audio`, `sources` and `first_seen` in place of `Work`, and `Stats` counts music, books and software as `KIND_OTHER`.
- **Breaking:** the tops rank TMDB movies and series: releases of every title key matched to one TMDB work count together, and a release matched to none is in no top.
- **Breaking:** music, books and software have no top and no page in the web UI; they show as "other" among the torrents.
- **Breaking:** `VOICE_LIKELY` and `voice=p50` admit releases with a marker as well as those with a probability of at least 0.5.
- **Breaking:** a movie or series is named by its ID, `m348` or `tv1399`, in place of the work key `WorkRef.key` gave; `Release.work` carries it.
- A movie's or series' TMDB details are fetched once for all its title keys.

### Removed

- `BrowseService.Page` and `BrowseService.Work`, with `PageRequest`, `WorkMatch`, `WorkRef` and `Work`.
- The web UI's `/top/w/{id}`: it redirects to `/w/{id}`.

## [0.15.1] - 2026-10-21

### Changed

- The source labels of the web UI are lower case in both languages: `web`, `rip`, `remux`.

## [0.15.0] - 2026-10-21

### Added

- `BrowseService.Settings` returns the API's audio languages, BCP-47 tags in its order; the web UI asks it at start and listens only once it has answered.
- `TMDBWork.poster_path` is the poster TMDB serves for the request's `lang`, `ru` or `en`, stored per language in `tmdb_texts`; a language without one takes the other's, then the work's `en-US` poster.

### Changed

- **Breaking:** `SNIFFARR_AUDIO_LANGUAGES` has no default: unset or empty, the audio extension is off, and no audio is recorded, asked about or shown. Installs that relied on the default set `SNIFFARR_AUDIO_LANGUAGES=ru`.
- The migration marks every matched work's details unfetched, so the TMDB worker's backfill fetches each once more, two requests a work, to record both posters; until it has, the work shows its match without details.

### Removed

- `SNIFFARR_AUDIO_LANGUAGES` of `-ui`: the UI takes the languages from the API.

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

[0.46.0]: https://forge.sarak.sh/mikluko/sniffarr/compare/v0.45.0...HEAD
[0.45.0]: https://forge.sarak.sh/mikluko/sniffarr/compare/v0.44.3...v0.45.0
[0.44.3]: https://forge.sarak.sh/mikluko/sniffarr/compare/v0.44.2...v0.44.3
[0.44.2]: https://forge.sarak.sh/mikluko/sniffarr/compare/v0.44.1...v0.44.2
[0.44.1]: https://forge.sarak.sh/mikluko/sniffarr/compare/v0.44.0...v0.44.1
[0.44.0]: https://forge.sarak.sh/mikluko/sniffarr/compare/v0.43.0...v0.44.0
[0.44.0-rc.2]: https://forge.sarak.sh/mikluko/sniffarr/compare/v0.44.0-rc.1...v0.44.0-rc.2
[0.44.0-rc.1]: https://forge.sarak.sh/mikluko/sniffarr/compare/v0.44.0-beta.8...v0.44.0-rc.1
[0.44.0-beta.8]: https://forge.sarak.sh/mikluko/sniffarr/compare/v0.44.0-beta.7...v0.44.0-beta.8
[0.44.0-beta.7]: https://forge.sarak.sh/mikluko/sniffarr/compare/v0.44.0-beta.6...v0.44.0-beta.7
[0.44.0-beta.6]: https://forge.sarak.sh/mikluko/sniffarr/compare/v0.44.0-beta.5...v0.44.0-beta.6
[0.44.0-beta.5]: https://forge.sarak.sh/mikluko/sniffarr/compare/v0.44.0-beta.4...v0.44.0-beta.5
[0.44.0-beta.4]: https://forge.sarak.sh/mikluko/sniffarr/compare/v0.44.0-beta.3...v0.44.0-beta.4
[0.44.0-beta.3]: https://forge.sarak.sh/mikluko/sniffarr/compare/v0.44.0-beta.2...v0.44.0-beta.3
[0.44.0-beta.2]: https://forge.sarak.sh/mikluko/sniffarr/compare/v0.44.0-beta.1...v0.44.0-beta.2
[0.44.0-beta.1]: https://forge.sarak.sh/mikluko/sniffarr/compare/v0.44.0-alpha.4...v0.44.0-beta.1
[0.44.0-alpha.4]: https://forge.sarak.sh/mikluko/sniffarr/compare/v0.44.0-alpha.3...v0.44.0-alpha.4
[0.44.0-alpha.3]: https://forge.sarak.sh/mikluko/sniffarr/compare/v0.44.0-alpha.2...v0.44.0-alpha.3
[0.44.0-alpha.2]: https://forge.sarak.sh/mikluko/sniffarr/compare/v0.44.0-alpha.1...v0.44.0-alpha.2
[0.44.0-alpha.1]: https://forge.sarak.sh/mikluko/sniffarr/compare/v0.43.0...v0.44.0-alpha.1
[0.43.0]: https://forge.sarak.sh/mikluko/sniffarr/compare/v0.42.0...v0.43.0
[0.42.0]: https://forge.sarak.sh/mikluko/sniffarr/compare/v0.41.0...v0.42.0
[0.41.0]: https://forge.sarak.sh/mikluko/sniffarr/compare/v0.40.0...v0.41.0
[0.40.0]: https://forge.sarak.sh/mikluko/sniffarr/compare/v0.39.0...v0.40.0
[0.39.0]: https://forge.sarak.sh/mikluko/sniffarr/compare/v0.38.0...v0.39.0
[0.38.0]: https://forge.sarak.sh/mikluko/sniffarr/compare/v0.37.0...v0.38.0
[0.37.0]: https://forge.sarak.sh/mikluko/sniffarr/compare/v0.36.0...v0.37.0
[0.36.0]: https://forge.sarak.sh/mikluko/sniffarr/compare/v0.35.2...v0.36.0
[0.35.2]: https://forge.sarak.sh/mikluko/sniffarr/compare/v0.35.1...v0.35.2
[0.35.1]: https://forge.sarak.sh/mikluko/sniffarr/compare/v0.35.0...v0.35.1
[0.35.0]: https://forge.sarak.sh/mikluko/sniffarr/compare/v0.34.1...v0.35.0
[0.34.1]: https://forge.sarak.sh/mikluko/sniffarr/compare/v0.34.0...v0.34.1
[0.34.0]: https://forge.sarak.sh/mikluko/sniffarr/compare/v0.32.2...v0.34.0
[0.32.2]: https://forge.sarak.sh/mikluko/sniffarr/compare/v0.32.1...v0.32.2
[0.32.1]: https://forge.sarak.sh/mikluko/sniffarr/compare/v0.32.0...v0.32.1
[0.32.0]: https://forge.sarak.sh/mikluko/sniffarr/compare/v0.31.0...v0.32.0
[0.31.0]: https://forge.sarak.sh/mikluko/sniffarr/compare/v0.30.0...v0.31.0
[0.30.0]: https://forge.sarak.sh/mikluko/sniffarr/compare/v0.29.0...v0.30.0
[0.29.0]: https://forge.sarak.sh/mikluko/sniffarr/compare/v0.28.0...v0.29.0
[0.28.0]: https://forge.sarak.sh/mikluko/sniffarr/compare/v0.27.3...v0.28.0
[0.27.3]: https://forge.sarak.sh/mikluko/sniffarr/compare/v0.27.2...v0.27.3
[0.27.2]: https://forge.sarak.sh/mikluko/sniffarr/compare/v0.27.1...v0.27.2
[0.27.1]: https://forge.sarak.sh/mikluko/sniffarr/compare/v0.27.0...v0.27.1
[0.27.0]: https://forge.sarak.sh/mikluko/sniffarr/compare/v0.26.0...v0.27.0
[0.26.0]: https://forge.sarak.sh/mikluko/sniffarr/compare/v0.25.2...v0.26.0
[0.25.2]: https://forge.sarak.sh/mikluko/sniffarr/compare/v0.25.1...v0.25.2
[0.25.1]: https://forge.sarak.sh/mikluko/sniffarr/compare/v0.25.0...v0.25.1
[0.25.0]: https://forge.sarak.sh/mikluko/sniffarr/compare/v0.24.0...v0.25.0
[0.24.0]: https://forge.sarak.sh/mikluko/sniffarr/compare/v0.23.1...v0.24.0
[0.23.1]: https://forge.sarak.sh/mikluko/sniffarr/compare/v0.23.0...v0.23.1
[0.23.0]: https://forge.sarak.sh/mikluko/sniffarr/compare/v0.22.1...v0.23.0
[0.22.1]: https://forge.sarak.sh/mikluko/sniffarr/compare/v0.22.0...v0.22.1
[0.22.0]: https://forge.sarak.sh/mikluko/sniffarr/compare/v0.21.0...v0.22.0
[0.21.0]: https://forge.sarak.sh/mikluko/sniffarr/compare/v0.20.0...v0.21.0
[0.20.0]: https://forge.sarak.sh/mikluko/sniffarr/compare/v0.19.0...v0.20.0
[0.19.0]: https://forge.sarak.sh/mikluko/sniffarr/compare/v0.18.0...v0.19.0
[0.18.0]: https://forge.sarak.sh/mikluko/sniffarr/compare/v0.17.1...v0.18.0
[0.17.1]: https://forge.sarak.sh/mikluko/sniffarr/compare/v0.17.0...v0.17.1
[0.17.0]: https://forge.sarak.sh/mikluko/sniffarr/compare/v0.16.0...v0.17.0
[0.16.0]: https://forge.sarak.sh/mikluko/sniffarr/compare/v0.15.1...v0.16.0
[0.15.1]: https://forge.sarak.sh/mikluko/sniffarr/compare/v0.15.0...v0.15.1
[0.15.0]: https://forge.sarak.sh/mikluko/sniffarr/compare/v0.14.1...v0.15.0
[0.14.1]: https://forge.sarak.sh/mikluko/sniffarr/compare/v0.14.0...v0.14.1
[0.14.0]: https://forge.sarak.sh/mikluko/sniffarr/compare/v0.13.0...v0.14.0
[0.13.0]: https://forge.sarak.sh/mikluko/sniffarr/compare/v0.12.0...v0.13.0
[0.12.0]: https://forge.sarak.sh/mikluko/sniffarr/compare/v0.11.0...v0.12.0
[0.11.0]: https://forge.sarak.sh/mikluko/sniffarr/compare/v0.10.0...v0.11.0
[0.10.0]: https://forge.sarak.sh/mikluko/sniffarr/compare/v0.9.0...v0.10.0
[0.9.0]: https://forge.sarak.sh/mikluko/sniffarr/compare/v0.8.0...v0.9.0
[0.8.0]: https://forge.sarak.sh/mikluko/sniffarr/compare/v0.7.1...v0.8.0
[0.7.1]: https://forge.sarak.sh/mikluko/sniffarr/compare/v0.7.0...v0.7.1
[0.7.0]: https://forge.sarak.sh/mikluko/sniffarr/compare/v0.6.0...v0.7.0
[0.6.0]: https://forge.sarak.sh/mikluko/sniffarr/compare/v0.5.0...v0.6.0
[0.5.0]: https://forge.sarak.sh/mikluko/sniffarr/compare/v0.4.0...v0.5.0
[0.4.0]: https://forge.sarak.sh/mikluko/sniffarr/compare/v0.3.0...v0.4.0
[0.3.0]: https://forge.sarak.sh/mikluko/sniffarr/compare/v0.2.0...v0.3.0
[0.2.0]: https://forge.sarak.sh/mikluko/sniffarr/compare/v0.1.4...v0.2.0
[0.1.4]: https://forge.sarak.sh/mikluko/sniffarr/compare/v0.1.3...v0.1.4
[0.1.3]: https://forge.sarak.sh/mikluko/sniffarr/compare/v0.1.2...v0.1.3
[0.1.2]: https://forge.sarak.sh/mikluko/sniffarr/compare/v0.1.1...v0.1.2
[0.1.1]: https://forge.sarak.sh/mikluko/sniffarr/compare/v0.1.0...v0.1.1
[0.1.0]: https://forge.sarak.sh/mikluko/sniffarr/releases/tag/v0.1.0
