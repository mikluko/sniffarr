# Run with Docker Compose

This guide runs sniffarr from the published image: one process in one
container, or [a container per component](#a-container-per-component).

## Before you begin

- Docker with the Compose plugin, on a Linux host. The container uses the
  host's network: the DHT port, 42069, and the UPnP discovery need it, so
  the API and the web UI listen on the host's `:8080` and `:8081` too.
- The host must reach the DHT: UDP and TCP on port 42069, inbound. The
  process asks the local gateway to forward the port by UPnP; where there is
  no UPnP, forward it by hand.

## Run

1. Save this as `compose.yaml`:

   ```yaml
   services:
     sniffarr:
       image: sarak.sh/arr/sniffarr:${SNIFFARR_VERSION:?a release, such as 0.38.0}
       command: ["-api", "-ui", "-sniff", "-fetch", "-upnp"]
       network_mode: host
       environment:
         SNIFFARR_DB: /data/sniffarr.db
       env_file:
         - path: .env
           required: false
       volumes:
         - ./data:/data
       restart: unless-stopped
   ```

1. Make the data directory the image's user's:

   ```shell
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

## A container per component

The same steps with this `compose.yaml`. The API and the UI sit on the
compose network, the UI published on loopback for an authenticating proxy.
The sniffer and the fetcher need the DHT port and the UPnP discovery, so
they use the host's network and reach the API through the port it publishes
on loopback. The fetcher's DHT node picks a free port, since the sniffer
holds `42069`.

```yaml
x-image: &image sarak.sh/arr/sniffarr:${SNIFFARR_VERSION:?a release, such as 0.38.0}

services:
  api:
    image: *image
    command: ["-api"]
    environment:
      SNIFFARR_DB: /data/sniffarr.db
    env_file:
      - path: .env
        required: false
    ports:
      - "127.0.0.1:8080:8080"
    volumes:
      - ./data:/data
    restart: unless-stopped

  ui:
    image: *image
    command: ["-ui"]
    environment:
      SNIFFARR_URL: http://api:8080
    ports:
      - "127.0.0.1:8081:8081"
    depends_on: [api]
    restart: unless-stopped

  sniff:
    image: *image
    command: ["-sniff", "-upnp"]
    network_mode: host
    environment:
      SNIFFARR_URL: http://127.0.0.1:8080
    depends_on: [api]
    restart: unless-stopped

  fetch:
    image: *image
    command: ["-fetch", "-port", "0"]
    network_mode: host
    environment:
      SNIFFARR_URL: http://127.0.0.1:8080
    depends_on: [api]
    restart: unless-stopped
```
