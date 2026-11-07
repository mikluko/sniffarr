# sniffarr

sniffarr is a private classification database of BitTorrent torrents
discovered on the DHT, with an API to search it. Its web UI is a catalogue
of movies and series as [TMDB](https://www.themoviedb.org) knows them, each with the releases of it the
DHT carries; a search looks in the database and on TMDB together, so it
also finds what has no release yet.

The database and the API are meant to stay private: run them for yourself, not
as a public index. Peer IP addresses are never sent to the server or stored:
peers are counted in HyperLogLog sketches of keyed hashes, from which no
address comes back. The sniffer fetches metadata only: it never downloads
pieces, never uploads, and never announces itself into a swarm. The fetcher
does so only with `-probe-dir`: it downloads the first pieces of a stored
release's main video file to read its tracks, and seeds them back; see
[Probes](docs/probes.md).

The repository is [sarak.sh/mikluko/sniffarr](https://sarak.sh/mikluko/sniffarr),
mirrored hourly to [github.com/mikluko/sniffarr](https://github.com/mikluko/sniffarr).

## Install

One process on a Linux host, as a systemd service:

```shell
curl -fsSL https://sarak.sh/mikluko/sniffarr/raw/branch/main/install.sh | sh
```

On Debian/Ubuntu the script installs the package from the sarak.sh Debian
registry; elsewhere, the release archive. Each way has its guide:

- [Debian/Ubuntu](docs/install/debian-ubuntu.md), the `.deb` package
- [systemd](docs/install/systemd.md), the release archive on any Linux
- [Docker Compose](docs/install/compose.md), the image in one container
- [Kubernetes](docs/install/kubernetes.md), the Helm chart

The Compose and systemd guides also give a container or a service per
component.

## Documentation

- [Components](docs/components.md): the `-api`, `-ui`, `-sniff` and `-fetch` components, what each does and how they fit together
- [Probes](docs/probes.md): reading the audio tracks of releases from their first pieces, and seeding them back
- [API](docs/api.md): the ingest, browse and search services, and calling them from a shell
- [Torznab](docs/torznab.md): the indexer [Prowlarr](https://prowlarr.com), [Radarr](https://radarr.video) and [Sonarr](https://sonarr.tv) search
- [Radarr and Sonarr](docs/radarr-sonarr.md): their webhook and grab history as demand and as matches
- [Feeds](docs/feeds.md): tracker feeds as a source of releases
- [Web UI](docs/web-ui.md): the pages, their parameters and the language
- [Configuration](docs/configuration.md): every flag and variable of every component
- [Telemetry](docs/telemetry.md): metrics, traces and logs
- [Helm](docs/helm.md): the chart, a Deployment per component or one process
- [Development](docs/development.md): building the image, generating code and cutting a release

## Contributing

Proposals, not code; see [CONTRIBUTING](CONTRIBUTING.md).

## License

MIT; see [LICENSE](LICENSE). The web UI ships JetBrains Mono under the SIL
Open Font License 1.1, in
[internal/ui/static/fonts/OFL.txt](internal/ui/static/fonts/OFL.txt), and
Lucide icons under the ISC License, in
[internal/ui/icons/LICENSE](internal/ui/icons/LICENSE).
