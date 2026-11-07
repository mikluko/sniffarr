# Web UI

With `-ui`, sniffarr serves a web UI on `SNIFFARR_UI_ADDR`, in English or
Russian. The UI touches no database: it reads `BrowseService`, in-process
when `-api` is on and over the network at
`SNIFFARR_URL` otherwise. At start it asks `Settings` for the API's audio
languages and which optional features are on, retrying every 5 seconds until
the API answers, and listens only then. It asks again every minute and a
change shows on the next page, so the order the UI and the API start in does
not matter; an answer that fails or names an audio language the UI does not
know leaves what it has. What is off leaves
no trace: without audio languages the voice filters, columns and audio rows
go; while the API does not probe, the probe button, the probe meter and the
probe queue card go, and the probe routes answer 404; without the classifier
its meter and its row among the layers on the data page go; without feeds P2
is labelled as feeds turned off.

The footer of every page, the 404 page included, shows "sniffarr" and the
UI process's own version, whichever the API's is; a build with no version
reads `dev`. Its tooltip gives the build time in UTC and the commit, and is
left out while either is unknown. Every response of the UI carries the
`Server` header `sniffarr/<version>`. See [Development](development.md#version)
for how a build gets its version.

| Route               | Page                                                                                                                                                                                                                                                                                                                                                                                                                                 |
| ------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `GET /`             | The store's counts in a grid of three tables, each row with its count, share and a share bar: kinds, layers, sources, audio per language (a cell left empty with audio off), rejections by reason, the classifier's pending torrents and tokens used, the piece data probes downloaded against the day's limit and what the fetchers uploaded back, both with what they come to by the end of the UTC day at their pace so far, the task queue as a card per kind (metadata fetches, peer lookups, probes) with a bar split by priority from P0 to P4, the tasks at each and how long until each priority is done at that kind's pace over the last 15 minutes, a kind with nothing queued and nothing done in that time left out, and new torrents per hour over 48 hours, per calendar day over 30 and per calendar week over 26 in the browser's time zone, which the page's script puts in the `tz` cookie, the process's (`TZ`) until then, the current hour, day and week unfinished, outlined up to their pace so far. The page fetches itself every 30 seconds while visible and on its refresh button, whose tooltip says when the API computed the counts; counts older than two of their intervals, the longer of those 30 seconds and the API's `SNIFFARR_STATS_INTERVAL`, show how many minutes ago beside the button. At the bottom, a row per optional feature (the classifier, probes, audio, feeds) as `Settings` tells it at the request: off with the flag or variable that turns it on, working, or on and not working in amber: the classifier with its daily limit spent, its meter then full and amber with the torrents waiting for it, or probes with no prober connected, the probe card then saying so with no ETAs                                                                                                                                           |
| `GET /movie/{id}`, `GET /tv/{id}` | A movie or series by its TMDB id, the paths Seerr gives the same titles: what TMDB knows of it, its places in the tops over 24 h, 7 days and 30 days, its releases and peers, the audio of its releases, and its releases under `quality` (`2160p`, `1080p`, `720p`, `sd`), `source` (`web`, `rip`, `remux`, `disc`), `min` and `max` in GB, `vl` and `voice`, a series' `season` and `episode` (`5` or `E05`), sorted by `sort` (`peers`, `first_seen`, `size`), 50 a page from `o`; a back link by where the visitor came from, see below |
| `GET /torrents`     | Every release, matched or not, by name or infohash in `q`, under `kind` (`movie`, `series`, `other`, `unknown`), `matched` (`yes`, `no`), `quality`, `vl` and `voice`, `source`, `layer`, `min` in GB, sorted by `sort` (`peers`, `last_seen`, `size`), 50 a page from `o`; a matched release links to its movie or series. The kind column shows the value only; who decided it, with the classifier's confidence, is in the value's tooltip ("not determined yet" when no layer did), and the day a release was first seen is in the last-seen tooltip. The layer filter offers the classifier only while it runs, or while it is the selected one. A filter that narrows the list is bordered, and a link, shown while one does, drops them all, keeping `q` and `sort`; on a phone the filters fold behind a button that counts those set |
| `GET /top`          | The 100 movies or series with the most peers over a window: `kind` is `movie` or `series`, `win` is `24h`, `7d` or `30d`, `voice` is `any`, `p50` or `explicit` in the audio language `vl`, narrowing each to its releases with a marker or a probability of at least 0.5, or to those with a marker; the first 25 until `all=1`. A row shows the places a work moved since the window before, or, where its place then was below 100th or it had none, "new" for its first entry into the top and "back" for a return, its time in the top over every stay, its best place and its first entry, all counted from the top's first computation by a version that keeps the history, and leads to its movie's or series' page                                                       |
| `GET /w/{id}`, `GET /top/w/{id}` | A permanent redirect of the title pages of earlier versions, `id` `m348` or `tv1399`, to `/movie/348` or `/tv/1399`                                                                                                                                                                                                                                                                                                       |
| `GET /t/{infohash}` | A release: its magnet link, peers over the last day, 7 days and 30 days, its movie or series with its place in the top and the release's place among its releases, or the state of its title's lookup with TMDB and IMDb searches while it is unmatched, classification, audio signs per language with their marks, its journal, the newest 200 events of its life with when, what, how it ended and the particulars, and the first 8 stored files, every one with `files=all`. For an infohash whose torrent is not stored, rejected, dropped, evicted or deleted among them, the infohash and its journal alone; not found when it has no journal either |
| `POST /t/{infohash}/peers`, `POST /movie/{id}/peers`, `POST /tv/{id}/peers` | Asks `RefreshPeers` to look up the peers of the release, or of every release of the movie or series; the cards' button posts it |
| `POST /t/{infohash}/probe`, `POST /movie/{id}/probe`, `POST /tv/{id}/probe` | Asks `RequestProbe` to probe the release, or every release of the movie or series; the cards' button posts it, and is disabled without peers over 7 days, the line under the buttons saying so; its answer says when no release has a file a probe can read, and, in amber, when no prober is connected to take it |
| `POST /t/{infohash}/match` | Asks `RequestMatch` to look the release's title up on TMDB now; the card's button posts it, shown for a movie or series matched to no work. Its answer is the work it is matched to, with a link, that TMDB still finds nothing, or why it could not look: the classifier's daily budget spent, TMDB off, or TMDB not answering |
| `GET /static/…`     | Stylesheet, scripts, the TMDB logo, and the JetBrains Mono font with its licence, `fonts/OFL.txt`. Each is cached for a year at its URL with a version in `v`, a hash of its content, where the pages link it; at any other URL the browser revalidates it by ETag                                                                                                                                                                 |
| Any other route     | 404, with a page naming the path asked for and a link home; a `GET` of a movie, series or release the API does not know answers the same |

The header has the logo, which leads to the data page, the tabs Torrents and
Top, and the language switch, on one line down to 320 px. The data page and a
movie or series select no tab; a release's card selects Torrents. A movie or
series has a back link to the page the visitor came from, "Top movies" or "Top
series" from the top, "Back to results" from the torrents list, "Back to the
release" from a card, to the same URL it left, filters included. The page's
links to a title carry that URL, less its language, in `from`, and the title's
own filters, pager and reset carry it on; without `from` the back link follows
the `Referer` when it is of this host. `from` and `Referer` name only the top,
the torrents list or a release's card, anything else is ignored, and a title
reached from none of them has no back link.

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
