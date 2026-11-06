# Feeds

With `SNIFFARR_FEEDS` naming them, the API reads tracker feeds, which list
releases with their infohash and the tracker's full title. Each named feed
is read at start, and then at every `SNIFFARR_FEED_INTERVAL` of wall-clock
time plus a moment fixed by the feed's name, so the rounds fall at the same
moments across restarts, never on the hour, and feeds read at different
moments; a read that fails is retried after 10 minutes, or at the next
round if that comes first. Every release listed is recorded once per feed
and infohash, with its title, page, category, size and date; the infohash
of one the store lacks is queued under discovery `feed`, after `arr` and
ahead of announced infohashes, unless it is rejected. A feed is a window,
so a release is listed again until it leaves it and recorded once.

| Feed        | Reads                                                                                                                                                                                                                                     |
| ----------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `rutracker` | `SNIFFARR_RUTRACKER_FEED_URL` at `/atom/f/<forum>.atom` for each forum of `SNIFFARR_RUTRACKER_FORUMS`, anonymously; forum `0` is the whole tracker, some 150 releases over a few hours, and a forum's feed its latest, about a month of them. A forum those feeds name in a category is read too, once per process, a second apart, so a fresh store fills with a month of every forum the tracker is active in; a forum's feed that fails is logged and not tried again |
