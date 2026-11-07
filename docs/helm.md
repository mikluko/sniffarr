# Helm

Each `vX.Y.Z` tag publishes the image as `sarak.sh/mikluko/sniffarr:X.Y.Z`
and the chart in `charts/sniffarr` at the same version, without the `v`.

```shell
helm install sniffarr oci://sarak.sh/mikluko/charts/sniffarr --version X.Y.Z \
  --set api.existingSecret=sniffarr
```

`api.existingSecret` names a Secret the api pod takes as its environment,
such as `TYPESAFE_API_KEY`, `SNIFFARR_TORZNAB_TOKEN` and
`SNIFFARR_WEBHOOK_TOKEN`; without it the pod takes none. With
`api.litestream.enabled`, `api.litestream.existingConfigMap` is required.
Every value is described in [`values.yaml`](../charts/sniffarr/values.yaml).

With `api.networkPolicy.enabled`, true by default, a NetworkPolicy
`<release>-api` admits to the API port, 8080, only the release's own pods and
the peers listed in `api.networkPolicy.from`, which are NetworkPolicyPeers:
Prowlarr, Radarr and Sonarr reach Torznab and the webhook on that port, so
their namespaces or pods belong there. The api pod's other ports stay open.
Kubernetes leaves it to the network plugin whether a pod in the host network,
as `sniff` is by default, matches a pod selector; where it does not, list its
nodes in `api.networkPolicy.from` or turn the policy off.

```yaml
api:
  networkPolicy:
    from:
      - namespaceSelector:
          matchLabels:
            kubernetes.io/metadata.name: media
```

The chart installs in one of two modes, both of the one image. In either, the
container that runs `-api` has a startup probe giving it an hour to first
answer `/healthz`, and its liveness probe runs only after that: the API
migrates the database before it listens, and a migration over a large
database takes minutes.

## A Deployment per component

By default each component is a Deployment of its own, reaching the API at the
Service `<release>-api`. `<release>-api` runs `sniffarr -api` behind that
Service. `<release>-sniff` runs `sniffarr -sniff`, `sniff.discover` and
`sniff.fetch` rendering `-sniff` and `-fetch`, in the host's network by default
so that `announce_peer` queries reach its DHT port. `<release>-fetch` runs
`sniffarr -fetch`, in the pod network by default, with `fetch.replicas`
replicas and the URL of `sniff` unless its own is set. A
fetcher's DHT node is passive and the fetcher dials the peers it fetches from,
so it needs no open port.

With `probe.enabled`, `<release>-probe` is a StatefulSet of `probe.replicas`
fetchers that probe too, rendering `-probe-dir=/seeds` and `probe`'s
`probers`, `timeout`, `maxSize` and `maxAge` as `-probers`, `-probe-timeout`,
`-seed-max-size` and `-seed-max-age`; its pod settings are `fetch`'s, and its
`url` falls back to `fetch.url`, then `sniff.url`. `/seeds` is
`probe.persistence`: an `emptyDir`, a claim per replica,
`seeds-<release>-probe-<n>`, or a `hostPath` with a directory per pod under
it, `<path>/<pod>`. The kubelet makes a missing one root's, and the pod runs as
65532; `probe.extraInitContainers` is where a deployment makes the directory
the pod's, with the whole `hostPath` as the volume `seeds`. A prober reaches
the peers it seeds to and is reached by
them on its DHT port, so it belongs in the host's network, `probe.hostNetwork`,
or behind `probe.service`.

With `ui.enabled`, `<release>-ui` runs `sniffarr -ui`, with `ui.replicas`
replicas, behind the Service `<release>-ui` on `ui.service.port` (8081).
`ui.addr` is `SNIFFARR_UI_ADDR`, and `ui.env` carries the rest of the UI's
variables, `SNIFFARR_OIDC_ISSUER`, `SNIFFARR_SEERR_URL`,
`SNIFFARR_RADARR_URL` and `SNIFFARR_SONARR_URL`. `ui.extraContainers` and `ui.extraVolumes` add an
authenticating proxy to the pod, and `ui.service.targetPort` names its port.
With `ui.addr` on a loopback address, such as `127.0.0.1:8081`, the UI is
reachable only through that proxy, and `ui.service.targetPort` must name it.

Either DHT Deployment reaches the DHT from the pod network through
`<component>.service`: a Service of the component's name exposing its port,
TCP and UDP, `NodePort` by default. With `service.nodePort` the pod listens on
that port, rendered as `-port`, so the port peers reach the node on is the
port it uses; `flags.port` must then be unset or equal. `<component>.upnp`
renders `-upnp`.

## One process

With `single.enabled`, the chart renders one Deployment, `<release>`, in place
of the component Deployments. Its container runs `sniffarr -api`, and `-ui`,
`-sniff` and `-fetch` for each of `ui`, `sniff` and `fetch` with `enabled`
true, `sniff.discover` and `sniff.fetch` still deciding the sniff component's
flags. The Services `<release>-api` and, with `ui.enabled`, `<release>-ui`
select that pod. The components keep their values and the pod takes the union
of them: `api.persistence`, `api.litestream`, `api.existingSecret`, the `env`
of every enabled component, `ui.addr`, the `flags` and `upnp` of `sniff` and
`fetch`, `probe` with its claim `<release>-seeds`, their `hostNetwork`, and the `extraContainers` and `extraVolumes` of
`api` and `ui`. The pod's `resources`, `podAnnotations`, `podLabels`,
`nodeSelector`, `tolerations` and `affinity` come from the `single` block; the
components' own are not read. With `sniff.hostNetwork`, every listener binds on
the host, the API's and the UI's too.

One process has one DHT node, so `sniff.service` or `fetch.service`, not both,
exposes it as the Service `<release>-dht`. The chart refuses values the one
process cannot honour: `single.replicas` other than 1, since the process owns
the SQLite database; `ui.url`, `sniff.url` or `fetch.url`, since the clients
reach the API in-process and the binary refuses `SNIFFARR_URL` with `-api`;
and one `env` or `flags` key set to different values by two components.
