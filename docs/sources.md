# Torznab sources

With `SNIFFARR_SOURCES` naming them, the API polls Torznab sources for their
latest listings: a `t=search` without `q`. Each source is set by variables
under `SNIFFARR_SOURCE_<NAME>_`, listed in [the configuration](configuration.md#torznab-sources).

```shell
SNIFFARR_SOURCES=spidarr
SNIFFARR_SOURCE_SPIDARR_URL=http://spidarr.spidarr:8080/api
SNIFFARR_SOURCE_SPIDARR_TOKEN_FILE=/var/run/secrets/tokens/spidarr
```

## Polling

Each source is polled at start, and then at every `SNIFFARR_SOURCES_INTERVAL`
of wall-clock time plus a moment fixed by the source's name and the
database's own key, so the polls fall at the same moments across restarts,
sources are polled at different moments, and so is one source by two
instances. A poll asks for 100 listings at a time, newest first, at least a
second apart, until an answer holds a listing recorded before, holds fewer
than 100, or 500 were asked for. A poll that fails is retried after 5
minutes, or at the next poll if that comes first. A source is never
searched, whatever its mode: `passive` and `active` are polled alike.

A request carries the source's API key as the `apikey` parameter, or the
token in its token file as `Authorization: Bearer <token>`, the file read
again for every request, so a projected service account token that rotates
is taken up. A source that answers with a Newznab error, such as error 100
for credentials it does not take, fails the poll.

## Listings

Every listing with a BitTorrent v1 infohash, from its `infohash` attribute or
else from the magnet link of its `magneturl` attribute, GUID, link or
enclosure, is recorded once per source and infohash, in the store's
`torznab_listings`: its title, page (`comments`), Newznab categories, size,
publication date, and the IMDb, TMDB and TVDB IDs of its work where the
source gives them (`imdb` or `imdbid`, `tmdbid`, `tvdbid`). A listing again
replaces what the source tells of the release and when it last listed it. A
listing without an infohash is skipped. A listing no source has listed for
`SNIFFARR_RETENTION` is deleted.

The infohash of a new listing whose torrent the store lacks is queued under
discovery `torznab`, at P2 with the announced infohashes, unless it is
rejected; a dead infohash is queued again. The torrent fetched from that
order is stored with discovery `torznab`, which its card shows as Torznab.
The first listing of an infohash by each source is journaled on it as
`listed`, with the source's name and the listing's particulars, and what it
came to: `stored` already, `queued`, or `rejected` already; a listing again
is not.
