# Run with Docker Compose

This guide runs one sniffarr process from the published image with the
[`compose.yaml`](../../compose.yaml) of the repository.

## Before you begin

- Docker with the Compose plugin, on a Linux host. The container uses the
  host's network: the DHT port, 42069, and the UPnP discovery need it, so
  the API and the web UI listen on the host's `:8080` and `:8081` too.
- The host must reach the DHT: UDP and TCP on port 42069, inbound. The
  process asks the local gateway to forward the port by UPnP; where there is
  no UPnP, forward it by hand.

## Run

1. Get the file and make the data directory the image's user's:

   ```shell
   curl -fsSLO https://sarak.sh/arr/sniffarr/raw/branch/main/compose.yaml
   mkdir -p data && sudo chown 65532:65532 data
   ```

1. Put the keys in `.env` beside it; each line is a variable of
   [Configuration](../../README.md#configuration):

   ```shell
   TYPESAFE_API_KEY=...
   SNIFFARR_UI_ADDR=127.0.0.1:8081
   ```

1. Start it with the release to run:

   ```shell
   SNIFFARR_VERSION=0.38.0 docker compose up -d
   ```

The database is `data/sniffarr.db`. The web UI authenticates nobody: bind it
to loopback, as above, or put an authenticating proxy in front of it.

## Check

```shell
docker compose logs -f
```

## Upgrade

Start it again with the new release in `SNIFFARR_VERSION`.

## Uninstall

```shell
docker compose down
```

`data` stays; remove it to drop the database.
