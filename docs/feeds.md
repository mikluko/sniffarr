# Feeds

With `SNIFFARR_FEEDS` naming them, the API reads tracker feeds, which list
releases with their infohash and the tracker's full title. Each named feed
is read at start and every `SNIFFARR_FEED_INTERVAL`; a read that fails is
retried after 10 minutes. Every release listed is recorded once per feed
and infohash, with its title, page, category, size and date; the infohash
of one the store lacks is queued under discovery `feed`, after `arr` and
ahead of announced infohashes, unless it is rejected. A feed is a window,
so a release is listed again until it leaves it and recorded once.

| Feed        | Reads                                                                                                                                                                                                                                     |
| ----------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `rutracker` | `SNIFFARR_RUTRACKER_FEED_URL` at `/atom/f/<forum>.atom` for each forum of `SNIFFARR_RUTRACKER_FORUMS`, anonymously; forum `0` is the whole tracker, some 150 releases over a few hours, and a forum's feed its latest, about a month of them |
