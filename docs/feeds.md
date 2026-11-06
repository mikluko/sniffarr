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
| `nnmclub`   | The RSS at `rss.php` under `SNIFFARR_NNMCLUB_URL`, of each forum of `SNIFFARR_NNMCLUB_FORUMS` or of every public forum for none, 25 topics each, anonymously; the RSS carries no magnet link, so the page of each topic not read before is read for it, a second apart, and the topic marked read whether it had one or not; a page that fails is logged and left for the next round |
| `rutor`     | The listing pages `browse/<page>/<category>/0/0` under `SNIFFARR_RUTOR_URL`, 100 releases a page with a magnet link each, anonymously, for each category of `SNIFFARR_RUTOR_CATEGORIES`, `0` every one: the first page each round, and `SNIFFARR_RUTOR_DEPTH` pages, a second apart, on the first pass, which is marked done |

What a feed read once and need not again, a forum, a topic or the first
pass, is kept in the store, so a restart does not read it again.
