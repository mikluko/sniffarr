# Install on Kubernetes

This guide installs sniffarr with its Helm chart, one workload per
component. Every value is described in the chart's
[`values.yaml`](../../charts/sniffarr/values.yaml), and the chart's behaviour
under [Helm](../helm.md) in the README.

## Before you begin

- Helm 3.8 or later and a cluster to install into.
- A node with a public address for the sniffer: its DHT node must receive
  UDP and TCP on port 42069. The chart runs the sniffer in the host network
  by default.
- A PersistentVolumeClaim, or a StorageClass, for the API's SQLite database.

## Install

1. Create a Secret with the keys; each is a variable of
   [Configuration](../configuration.md):

   ```shell
   kubectl create secret generic sniffarr \
     --from-literal=SNIFFARR_CLASSIFY_API_KEY=... \
     --from-literal=SNIFFARR_TORZNAB_TOKEN=...
   ```

1. Write the values: the node of the sniffer, the database's storage, the
   Secret, and the UI if you want it:

   ```yaml
   # values.yaml
   api:
     existingSecret: sniffarr
     persistence:
       type: pvc
       storageClass: standard
   sniff:
     nodeSelector:
       kubernetes.io/hostname: edge
   ui:
     enabled: true
   ```

1. Install the chart at a release's version:

   ```shell
   helm install sniffarr oci://sarak.sh/mikluko/charts/sniffarr --version 0.38.0 -f values.yaml
   ```

The web UI is the Service `sniffarr-ui` on port 8081 and authenticates
nobody: put an authenticating proxy in front of it before exposing it, as
`ui.extraContainers` and `ui.service.targetPort` allow.

## Check

```shell
kubectl get pods -l app.kubernetes.io/instance=sniffarr
kubectl logs statefulset/sniffarr-api --since=5m
```

## Upgrade

```shell
helm upgrade sniffarr oci://sarak.sh/mikluko/charts/sniffarr --version 0.39.0 -f values.yaml
```

Migrations run at start and are not undone. A release older than the database's
schema refuses to start with `database schema is at version N, newer than the M
this binary knows`; roll back by restoring a database backup taken before the
upgrade.

## Uninstall

```shell
helm uninstall sniffarr
```

The database's PersistentVolumeClaim stays; delete it to drop the database.
