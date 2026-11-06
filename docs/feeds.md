# Feeds

With `SNIFFARR_FEEDS` naming them, the API reads tracker feeds, which list
releases with their infohash and the tracker's full title. Each named feed
is read at start, and then at every `SNIFFARR_FEED_INTERVAL` of wall-clock
time plus a moment fixed by the feed's name and the database's own key, so
the rounds fall at the same moments across restarts, feeds read at
different moments, and so do two instances reading one feed; a read that
fails is retried after 10 minutes, or at the next round if that comes
first. Every release listed is recorded once per feed
and infohash, with its title, page, category, size and date; the infohash
of one the store lacks is queued under discovery `feed`, after `arr` and
ahead of announced infohashes, unless it is rejected. A feed is a window,
so a release is listed again until it leaves it and recorded once.

| Feed        | Reads                                                                                                                                                                                                                                     |
| ----------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `rutracker` | `SNIFFARR_RUTRACKER_FEED_URL` at `/atom/f/<forum>.atom` for each forum of `SNIFFARR_RUTRACKER_FORUMS`, anonymously; forum `0` is the whole tracker, some 150 releases over a few hours, and a forum's feed its latest, about a month of them. A forum those feeds name in a category is read too, once, so a fresh store fills with a month of every forum the tracker is active in |
| `nnmclub`   | The RSS at `rss.php` under `SNIFFARR_NNMCLUB_URL`, of each forum of `SNIFFARR_NNMCLUB_FORUMS` or of every public forum for none, 25 topics each, anonymously; the RSS carries no magnet link, so the page of each topic is read for it, once, whether it has one or not |
| `rutor`     | The listing pages `browse/<page>/<category>/0/0` under `SNIFFARR_RUTOR_URL`, 100 releases a page with a magnet link each, anonymously, for each category of `SNIFFARR_RUTOR_CATEGORIES`, `0` every one: the first page each round, and the pages after it up to `SNIFFARR_RUTOR_DEPTH` once |

Every page a feed fetches is recorded in the store, `crawl_state`, by feed
and URL: when it was first and last fetched, the status it answered, its
`ETag` and `Last-Modified`, and the error when it failed. A page fetched
every round, a forum's feed or a listing's first page, is asked for with
those validators and skipped when unchanged; a page fetched once, a forum
found in a category, a topic or a deeper listing page, is skipped once a
fetch of it succeeded, so a restart repeats none of it, and a failed fetch
is tried again the next round. Requests of one feed are at least a second
apart.
