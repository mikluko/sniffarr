# systemd examples

Each unit runs the binary found in `/usr/local/bin` or `/usr/bin` as a
dynamic user, with the database under `/var/lib/sniffarr` and the keys from
`/etc/default/sniffarr`, each line a variable of
[Configuration](../README.md#configuration). Units go in
`/etc/systemd/system`, then:

```shell
sudo systemctl daemon-reload
sudo systemctl enable --now sniffarr.service
```

The [systemd install guide](../docs/install/systemd.md) walks through the
first one, which the release archive and the `.deb` carry.

## One process

One service with every component, the DHT port forwarded by UPnP.

`/etc/systemd/system/sniffarr.service`:

```ini
[Unit]
Description=sniffarr, a private classification database of DHT torrents
Documentation=https://sarak.sh/arr/sniffarr
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

Four services sharing `/etc/default/sniffarr`, whose `SNIFFARR_FLAGS` they
do not read: the clients reach the API on loopback, the UI binds to loopback
for an authenticating proxy, and the fetcher's DHT node picks a free port,
since the sniffer holds `42069`. Enable all four; the clients wait for the
API.

`/etc/systemd/system/sniffarr-api.service`:

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

`/etc/systemd/system/sniffarr-ui.service`:

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

`/etc/systemd/system/sniffarr-sniff.service`:

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

`/etc/systemd/system/sniffarr-fetch.service`:

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
