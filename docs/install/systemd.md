# Install as a systemd service

This guide installs one sniffarr process on a Linux host and runs it as a
systemd service. On Debian or Ubuntu, [install the package](debian-ubuntu.md)
instead: it carries the same service. The units are shown under
[The unit](#the-unit) and [A service per component](#a-service-per-component).

## Before you begin

- A Linux host on amd64 or arm64 with systemd 250 or later, and root or
  `sudo`.
- The host must reach the DHT: UDP and TCP on port 42069, inbound. The
  service asks the local gateway to forward the port by UPnP; where there is
  no UPnP, forward it by hand.

## Install with the script

The script picks the archive of the newest release for the host's
architecture, verifies its checksum, and installs the binary, the unit and
the settings file:

```shell
curl -fsSL https://sarak.sh/mikluko/sniffarr/raw/branch/main/install.sh | sh
```

To install a release other than the newest, set `SNIFFARR_VERSION`:

```shell
curl -fsSL https://sarak.sh/mikluko/sniffarr/raw/branch/main/install.sh | SNIFFARR_VERSION=0.38.0 sh
```

## Install by hand

1. Download the release archive and its checksums, and verify the archive.
   Replace `0.38.0` with the release and `amd64` with the architecture:

   ```shell
   curl -fsSLO https://sarak.sh/mikluko/sniffarr/releases/download/v0.38.0/sniffarr_0.38.0_linux_amd64.tar.gz
   curl -fsSLO https://sarak.sh/mikluko/sniffarr/releases/download/v0.38.0/checksums.txt
   sha256sum -c --ignore-missing checksums.txt
   ```

1. Unpack it and install the three files:

   ```shell
   tar -xzf sniffarr_0.38.0_linux_amd64.tar.gz
   sudo install -m 0755 sniffarr /usr/local/bin/sniffarr
   sudo install -m 0644 sniffarr.service /etc/systemd/system/sniffarr.service
   sudo install -m 0644 sniffarr.default /etc/default/sniffarr
   ```

1. Start the service:

   ```shell
   sudo systemctl daemon-reload
   sudo systemctl enable --now sniffarr.service
   ```

## Configure

The service runs `sniffarr $SNIFFARR_FLAGS` with the environment of
`/etc/default/sniffarr`, which starts as:

```shell
SNIFFARR_FLAGS=-api -ui -sniff -fetch -upnp
```

Set the keys there, uncommenting the lines: `TYPESAFE_API_KEY` for the
classifier, `SNIFFARR_TORZNAB_TOKEN` for Prowlarr, `SNIFFARR_WEBHOOK_TOKEN`
for Radarr and Sonarr. Every variable and flag is listed under
[Configuration](../../README.md#configuration). After a change:

```shell
sudo systemctl restart sniffarr.service
```

The database is `/var/lib/sniffarr/sniffarr.db`, owned by the service's
dynamic user. The web UI listens on `:8081` and authenticates nobody: bind it
to loopback with `SNIFFARR_UI_ADDR=127.0.0.1:8081`, or put an authenticating
proxy in front of it.

## Check

```shell
systemctl status sniffarr.service
journalctl -u sniffarr.service -f
```

## Upgrade

Run the script again, or repeat the download and `install` steps and restart
the service. The settings file is left as it is.

## Uninstall

```shell
sudo systemctl disable --now sniffarr.service
sudo rm /etc/systemd/system/sniffarr.service /usr/local/bin/sniffarr
sudo systemctl daemon-reload
```

`/etc/default/sniffarr` and `/var/lib/sniffarr` stay; remove them to drop the
settings and the database.

## The unit

What the archive and the package carry. The unit runs the binary found in
`/usr/local/bin` or `/usr/bin` as a dynamic user, with the database under
`/var/lib/sniffarr`.

`/etc/systemd/system/sniffarr.service`:

```ini
[Unit]
Description=sniffarr, a private classification database of DHT torrents
Documentation=https://sarak.sh/mikluko/sniffarr
After=network-online.target
Wants=network-online.target

[Service]
EnvironmentFile=/etc/default/sniffarr
Environment=SNIFFARR_DB=/var/lib/sniffarr/sniffarr.db
ExecSearchPath=/usr/local/bin:/usr/bin
ExecStart=sniffarr $SNIFFARR_FLAGS
DynamicUser=yes
StateDirectory=sniffarr
Restart=on-failure
RestartSec=5s
TimeoutStopSec=30s
ProtectHome=yes
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX AF_NETLINK

[Install]
WantedBy=multi-user.target
```

`/etc/default/sniffarr`:

```shell
SNIFFARR_FLAGS=-api -ui -sniff -fetch -upnp
#TYPESAFE_API_KEY=
#SNIFFARR_UI_ADDR=127.0.0.1:8081
#SNIFFARR_TORZNAB_TOKEN=
#SNIFFARR_WEBHOOK_TOKEN=
#SNIFFARR_AUDIO_LANGUAGES=ru
```

## A service per component

Four services in place of the one, sharing `/etc/default/sniffarr`, whose
`SNIFFARR_FLAGS` they do not read: the clients reach the API on loopback,
the UI binds to loopback for an authenticating proxy, and the fetcher's DHT
node picks a free port, since the sniffer holds `42069`. Install the binary
as above, save the four units in `/etc/systemd/system`, then:

```shell
sudo systemctl daemon-reload
sudo systemctl enable --now sniffarr-api.service sniffarr-ui.service sniffarr-sniff.service sniffarr-fetch.service
```

`sniffarr-api.service`:

```ini
[Unit]
Description=sniffarr API, the database and the work queue
After=network-online.target
Wants=network-online.target

[Service]
EnvironmentFile=/etc/default/sniffarr
Environment=SNIFFARR_DB=/var/lib/sniffarr/sniffarr.db
ExecSearchPath=/usr/local/bin:/usr/bin
ExecStart=sniffarr -api
DynamicUser=yes
StateDirectory=sniffarr
Restart=on-failure
RestartSec=5s
TimeoutStopSec=30s
ProtectHome=yes
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX

[Install]
WantedBy=multi-user.target
```

`sniffarr-ui.service`:

```ini
[Unit]
Description=sniffarr web UI
After=sniffarr-api.service
Wants=sniffarr-api.service

[Service]
EnvironmentFile=/etc/default/sniffarr
Environment=SNIFFARR_URL=http://127.0.0.1:8080
Environment=SNIFFARR_UI_ADDR=127.0.0.1:8081
ExecSearchPath=/usr/local/bin:/usr/bin
ExecStart=sniffarr -ui
DynamicUser=yes
Restart=on-failure
RestartSec=5s
ProtectHome=yes
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX

[Install]
WantedBy=multi-user.target
```

`sniffarr-sniff.service`:

```ini
[Unit]
Description=sniffarr DHT sniffer
After=sniffarr-api.service
Wants=sniffarr-api.service

[Service]
EnvironmentFile=/etc/default/sniffarr
Environment=SNIFFARR_URL=http://127.0.0.1:8080
ExecSearchPath=/usr/local/bin:/usr/bin
ExecStart=sniffarr -sniff -upnp
DynamicUser=yes
Restart=on-failure
RestartSec=5s
ProtectHome=yes
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX AF_NETLINK

[Install]
WantedBy=multi-user.target
```

`sniffarr-fetch.service`:

```ini
[Unit]
Description=sniffarr metadata fetcher
After=sniffarr-api.service
Wants=sniffarr-api.service

[Service]
EnvironmentFile=/etc/default/sniffarr
Environment=SNIFFARR_URL=http://127.0.0.1:8080
ExecSearchPath=/usr/local/bin:/usr/bin
ExecStart=sniffarr -fetch -port 0
DynamicUser=yes
Restart=on-failure
RestartSec=5s
ProtectHome=yes
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX

[Install]
WantedBy=multi-user.target
```
