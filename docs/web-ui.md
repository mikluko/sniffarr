# Web UI

With `-ui`, sniffarr serves a web UI on `SNIFFARR_UI_ADDR`, in English or
Russian. The UI touches no database: it reads `BrowseService`, in-process
when `-api` is on and over the network at
`SNIFFARR_URL` otherwise. At start it asks `Settings` for the API's audio
languages and whether it probes, retrying every 5 seconds until the API
answers, and listens only then; both are kept for the life of the process,
so a change of the API's needs the UI restarted. While the API does not
probe, the UI leaves out the probe button, the probe meter and the probe
queue card, and answers the probe routes with 404.

| Route               | Page                                                                                                                                                                                                                                                                                                                                                                                                                                 |
| ------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `GET /`             | The store's counts: kinds, layers, sources, audio per language, rejections by reason, the classifier's pending torrents and tokens used, the piece data probes downloaded against the day's limit and what the fetchers uploaded back, both with what they come to by the end of the UTC day at their pace so far, the task queue as a card per kind (metadata fetches, peer lookups, probes) with a bar split by priority from P0 to P4, the tasks at each and how long until each priority is done at that kind's pace over the last 15 minutes, a kind with nothing queued and nothing done in that time left out, and new torrents per hour over 48 hours, per calendar day over 30 and per calendar week over 26 in the browser's time zone, which the page's script puts in the `tz` cookie, the process's (`TZ`) until then, the current hour, day and week unfinished, outlined up to their pace so far. The page fetches itself every 30 seconds while visible and on its refresh button, whose tooltip says when the API computed the counts; counts older than two of their intervals, the longer of those 30 seconds and the API's `SNIFFARR_STATS_INTERVAL`, show how many minutes ago beside the button                                                                                                                                           |
| `GET /movie/{id}`, `GET /tv/{id}` | A movie or series by its TMDB id, the paths Seerr gives the same titles: what TMDB knows of it, its places in the tops over 24 h, 7 days and 30 days, its releases and peers, the audio of its releases, and its releases under `quality` (`2160p`, `1080p`, `720p`, `sd`), `source` (`web`, `rip`, `remux`, `disc`), `min` and `max` in GB, `vl` and `voice`, a series' `season` and `episode` (`5` or `E05`), sorted by `sort` (`peers`, `first_seen`, `size`), 50 a page from `o` |
| `GET /torrents`     | Every release, matched or not, by name or infohash in `q`, under `kind` (`movie`, `series`, `other`, `unknown`), `matched` (`yes`, `no`), `quality`, `vl` and `voice`, `source`, `layer`, `min` in GB, sorted by `sort` (`peers`, `last_seen`, `size`), 50 a page from `o`; a matched release links to its movie or series. The layer that decided a release's kind, with the classifier's confidence, and the day it was first seen are in tooltips. A filter that narrows the list is bordered, and a link, shown while one does, drops them all, keeping `q` and `sort`; on a phone the filters fold behind a button that counts those set |
| `GET /top`          | The 100 movies or series with the most peers over a window: `kind` is `movie` or `series`, `win` is `24h`, `7d` or `30d`, `voice` is `any`, `p50` or `explicit` in the audio language `vl`, narrowing each to its releases with a marker or a probability of at least 0.5, or to those with a marker; the first 25 until `all=1`. A row shows the places a work moved since the window before, or "new" where its place then was below 100th or it had none, and leads to its movie's or series' page                                                       |
| `GET /w/{id}`, `GET /top/w/{id}` | A permanent redirect of the title pages of earlier versions, `id` `m348` or `tv1399`, to `/movie/348` or `/tv/1399`                                                                                                                                                                                                                                                                                                       |
| `GET /t/{infohash}` | A release: its magnet link, peers over the last day, 7 days and 30 days, its movie or series with its place in the top and the release's place among its releases, or the state of its title's lookup with TMDB and IMDb searches while it is unmatched, classification, audio signs per language with their marks, its journal, the newest 200 events of its life with when, what, how it ended and the particulars, and the first 8 stored files, every one with `files=all`. For an infohash whose torrent is not stored, rejected, dropped, evicted or deleted among them, the infohash and its journal alone; not found when it has no journal either |
| `POST /t/{infohash}/peers`, `POST /movie/{id}/peers`, `POST /tv/{id}/peers` | Asks `RefreshPeers` to look up the peers of the release, or of every release of the movie or series; the cards' button posts it |
| `POST /t/{infohash}/probe`, `POST /movie/{id}/probe`, `POST /tv/{id}/probe` | Asks `RequestProbe` to probe the release, or every release of the movie or series; the cards' button posts it, and is disabled without peers over 7 days, the line under the buttons saying so; its answer says when no release has a file a probe can read |
| `POST /t/{infohash}/match` | Asks `RequestMatch` to look the release's title up on TMDB now; the card's button posts it, shown for a movie or series matched to no work. Its answer is the work it is matched to, with a link, that TMDB still finds nothing, or why it could not look: the classifier's daily budget spent, TMDB off, or TMDB not answering |
| `GET /static/…`     | Stylesheet, scripts, the TMDB logo, and the JetBrains Mono font with its licence, `fonts/OFL.txt`. Each is cached for a year at its URL with a version in `v`, a hash of its content, where the pages link it; at any other URL the browser revalidates it by ETag                                                                                                                                                                 |
| Any other route     | 404, with a page naming the path asked for and a link home; a `GET` of a movie, series or release the API does not know answers the same |

A parameter value outside those listed is the default: every kind, quality,
source and layer, the audio language of the page's language when it is
configured and else the first, `voice=any`, `sort=peers`, `kind=movie` for
the top, `win=7d`. A fragment of a page is served alone when htmx asks for it
with `HX-Request: true` and `HX-Target` naming it: `results`, `releases`,
`torrents`, `top`, `files` or `data`. A `POST` answers htmx with
`HX-Target` `act-answer` with the line under the card's buttons, which says
what was asked for and at which priority and links the queue on the data
page, and with the pressed button, which stays disabled; it answers any
other request with a redirect to the card. A `POST` from
another origin, by `Sec-Fetch-Site` or `Origin`, is refused with 403.

The UI authenticates nobody: an authenticating proxy, such as oauth2-proxy,
goes in front of it. Its language is the one picked with `?lang=ru` or
`?lang=en`, on any page, remembered in a cookie for a year, else, with
`SNIFFARR_OIDC_ISSUER` set, the `locale` claim of the provider's userinfo for
the access token in the `X-Forwarded-Access-Token` header, which oauth2-proxy
can pass on; a locale is kept for 10 minutes per token. Adult releases do not
show in it. With `SNIFFARR_SEERR_URL` set, a matched title's card and release
cards link it in that Seerr, at `/movie/<tmdb id>` or `/tv/<tmdb id>` under
it. With `SNIFFARR_RADARR_URL` or `SNIFFARR_SONARR_URL` set, they link a
matched movie in that Radarr and a series in that Sonarr, at
`/add/new?term=tmdb:<tmdb id>` under it: the app's search for the work,
which leads on to the work's page when the work is in its library.

The chart serves the UI with `ui.enabled`, behind the Service `<release>-ui`;
see [Helm](helm.md).
