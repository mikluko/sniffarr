# Torznab

With `SNIFFARR_TORZNAB_TOKEN` set, the API port also serves a
[Torznab](https://torznab.github.io/spec-1.3-draft/) indexer at
`/torznab/api`, for Prowlarr, Radarr and Sonarr; without it the path answers 404. Each request carries the token as its `apikey` parameter.

| `t`        | Parameters                                                        |
| ---------- | ----------------------------------------------------------------- |
| `caps`     |                                                                   |
| `search`   | `q`, `cat`, `offset`, `limit`                                     |
| `movie`    | `q`, `tmdbid`, `imdbid`, `cat`, `offset`, `limit`                 |
| `tvsearch` | `q`, `tvdbid`, `tmdbid`, `season`, `ep`, `cat`, `offset`, `limit` |

It answers through `Search`, so its candidates are classified and matched as
a search's are, and returns movies and series only: `movie` searches movies,
`tvsearch` series, `search` both, and `cat` narrows them to Movies (2000s) or
TV (5000s). An ID parameter selects the releases matched to that work,
`tmdbid` among movies for `movie` and among series for `tvsearch`; `imdbid`
takes either `tt0078748` or `78748`. `season` selects the releases whose
name gives that season, and `ep` with it that episode and the season's whole
packs; a name that gives no season matches no `season`, and an `ep` in the
daily form `MM/DD` matches nothing. `limit` is 100 by default and `offset`
and `limit` together 500 at most, counted among the releases that match.

Each item is a release: its name, its magnet as guid, link and enclosure, its
first sighting as `pubDate`, and the attributes `category`, `size`,
`infohash`, `magneturl`, `seeders`, `peers`, and `tmdbid`, `imdbid` (without
`tt`) and `tvdbid` when its title is matched. The category is Movies or TV,
`/UHD` for a name with `2160p`, `4K` or `UHD`, `/HD` for `1080p`, `1080i` or
`720p`, `/SD` otherwise. `seeders` and `peers` are both the release's distinct
peers over the last 7 days, since the DHT does not tell seeders apart.

The title is the release's name with its year and resolution spelled in
digits and Latin letters, `2O17` as `2017`, `1O8Op` or `1080р` as `1080p`,
since Radarr and Sonarr read no other spelling; the category reads it so
too. Markers in brackets follow it, for the release-title custom formats of
Radarr and Sonarr:

- A probed Russian audio track: the labels the Russian tracks' names say,
  `Dub` for a dub (`Дубляж`), `MVO`, `DVO`, `AVO` and `VO` for
  many-voice, two-voice, author's and one-voice voice-over (`Многоголосый`,
  `Двухголосый`, `Авторский`, `Одноголосый`, or the abbreviation), each
  left out when the name already has it as a word, then `RUS`:
  `Coraline.2009.2160p.HDR.BDRip.Master5 [Dub, MVO, RUS]`.
- Probed audio tracks all in other languages, every one giving a language,
  and no Russian marker: `NORUS` and their ISO 639-2 codes in the order of
  the tracks, `[NORUS FRA, ENG]`. No release name has `NORUS`, and it does
  not read as Russian.
- Otherwise a Russian marker, or a probability of at least 0.9 of a Russian
  track: the labels the name's Russian translation codes say, `Dub` for
  `D`, `MVO` for `P` and `L`, `DVO` for `P2` and `L2`, `AVO` for `A`, `VO`
  for `P1` and `L1`, none for `Pm`, each left out as above, then `RUS`:
  `Bad.Apples.2026.P.WEB-DL.1080p.mkv [MVO, RUS]`. The codes are listed
  under [Components](components.md).

`RUS` is followed by the Russian studios in the name or stored file paths,
`[RUS LostFilm]`. A release that has none of these keeps its name.

In Prowlarr, add an indexer of type Generic Torznab with URL
`http://<host>:8080/torznab`, API path `/api`, and the token as API key.
