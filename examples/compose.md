# Docker Compose examples

Each example is a complete `compose.yaml`. Beside it go a `data` directory
the image's user can write, and a `.env` with the keys, each line a
variable of [Configuration](../README.md#configuration):

```shell
mkdir -p data && sudo chown 65532:65532 data
printf 'TYPESAFE_API_KEY=...\n' >.env
SNIFFARR_VERSION=0.38.0 docker compose up -d
```

The [Compose install guide](../docs/install/compose.md) walks through the
first one.

## One process

One container with every component. The host's network carries the DHT
port and the UPnP discovery; the API and the web UI then listen on the
host's `:8080` and `:8081`, and the UI authenticates nobody.

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

## A container per component

The API and the UI sit on the compose network, the UI published on
loopback for an authenticating proxy. The sniffer and the fetcher need the
DHT port and the UPnP discovery, so they use the host's network and reach
the API through the port it publishes on loopback. The fetcher's DHT node
picks a free port, since the sniffer holds `42069`.

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
