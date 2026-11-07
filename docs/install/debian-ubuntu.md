# Install on Debian or Ubuntu

This guide installs sniffarr from the sarak.sh Debian registry on Debian or
Ubuntu, amd64 or arm64. The package carries the same systemd service as the
[systemd install](systemd.md).

## Before you begin

- Root or `sudo`, and `curl`.
- The host must reach the DHT: UDP and TCP on port 42069, inbound. The
  service asks the local gateway to forward the port by UPnP; where there is
  no UPnP, forward it by hand.

## Install with the script

The script adds the registry and installs the package:

```shell
curl -fsSL https://sarak.sh/mikluko/sniffarr/raw/branch/main/install.sh | sh
```

## Install by hand

1. Add the registry's signing key and the registry:

   ```shell
   sudo install -d -m 0755 /etc/apt/keyrings
   curl -fsSL https://sarak.sh/api/packages/mikluko/debian/repository.key | sudo tee /etc/apt/keyrings/sniffarr.asc >/dev/null
   echo "deb [signed-by=/etc/apt/keyrings/sniffarr.asc] https://sarak.sh/api/packages/mikluko/debian trixie main" | sudo tee /etc/apt/sources.list.d/sniffarr.list
   ```

   `trixie` names the registry's distribution, not the host's: the package
   is a static binary and installs on any Debian or Ubuntu with systemd 250
   or later.

1. Install the package:

   ```shell
   sudo apt-get update
   sudo apt-get install sniffarr
   ```

   The package enables and starts `sniffarr.service`.

## Configure

Settings are in `/etc/default/sniffarr`, kept across upgrades; the database
is `/var/lib/sniffarr/sniffarr.db`. See [Configure](systemd.md#configure)
in the systemd guide: the service is the same.

## Upgrade

```shell
sudo apt-get update
sudo apt-get install sniffarr
```

The package restarts the service.

Migrations run at start and are not undone. A release older than the database's
schema refuses to start with `database schema is at version N, newer than the M
this binary knows`; roll back by restoring a database backup taken before the
upgrade.

## Uninstall

```shell
sudo apt-get remove sniffarr
```

`/etc/default/sniffarr` stays until `apt-get purge`; `/var/lib/sniffarr`
stays either way.
