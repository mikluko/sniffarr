# Radarr and Sonarr

With `SNIFFARR_WEBHOOK_TOKEN` set, the API port also serves a webhook for
Radarr and Sonarr at `POST /webhooks/arr`; without it the path answers 404.
In each app, add a connection of type Webhook under Settings, Connect:

| Field    | Value                                                  |
| -------- | ------------------------------------------------------ |
| URL      | `https://<api>/webhooks/arr`                           |
| Method   | `POST`                                                 |
| Username | anything                                               |
| Password | `SNIFFARR_WEBHOOK_TOKEN`                               |
| Triggers | On Grab, On File Import; On File Upgrade if you like   |

A request without the token as its Basic password answers 401. The app is
the query's `app`, `radarr` or `sonarr`, or else the payload's: a movie is
Radarr's, a series Sonarr's. A Grab is recorded as a grab, a Download, which
Radarr and Sonarr send on import and upgrade, as an import; Test and every
other event answer 200 and record nothing.

Each event is recorded once per app, event and download ID, so the same
download reported twice counts once. A `downloadId` of 40 hex digits, as
torrent clients such as qBittorrent report it, is the torrent's infohash;
any other, such as a Usenet client's, leaves the event without one. The
event's work is Radarr's TMDB ID, or Sonarr's TMDB ID or else its TVDB ID,
which TMDB's `/3/find` turns into a TMDB ID once, the mapping kept with the
work's IDs; a work new to the store is stored without details, for the
backfill to fetch.

With a work and an infohash, the stored release of the hash belongs to the
work, and its title key is matched to the work under layer `arr` with
confidence 1, `TMDB_LAYER_ARR` in the API, which every other release of the
key follows. Neither TMDB's search nor Jev replaces an `arr` match; a later
event does. A release not stored is queued, ahead of announced infohashes,
and gets the work and the match when its metadata is stored; one the DHT
never yields, such as a private tracker's, stays unfetched. A rejected
infohash is not queued.

Every grab is demand, with or without an infohash: `grabs_week` in a
`WorkSummary`, in `Title`, `SearchTitles`, `Top` and `Card`, counts the
work's grabs over the last 7 days. Imports are recorded and not counted.

With `SNIFFARR_RADARR_URL` and `SNIFFARR_RADARR_API_KEY`, or the
`SNIFFARR_SONARR_` pair, the API imports the app's grabs through its
`/api/v3/history`: the whole history once per database, oldest first, and
then at start and every 24 hours the grabs since the latest recorded one
through `/api/v3/history/since`; a round that fails is retried after an
hour. The URL is the app's base, with its URL base if it has one, such as
`http://radarr:7878` or `http://nas/sonarr`. A history record and a webhook
event of one download are one event, and the records of a season pack's
episodes one grab. An event without a download ID is never taken for
another.
