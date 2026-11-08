# Development

## Container image

The image's entrypoint is `/sniffarr`, so the container's arguments are the
component flags.

```shell
docker build -t sniffarr .
docker run -e SNIFFARR_DB=/data/sniffarr.db sniffarr -api -ui
```

## Version

A binary knows its version, its commit (7 characters) and its build time
(UTC) from three variables of `internal/version`, set at link time with
`-ldflags -X`. The image takes them as the build arguments `VERSION`,
`COMMIT` and `BUILD_TIME`, and the release workflow passes the version it
cuts, the commit it builds and the time of the build to the image, and as
`-X` flags to the archives and packages. Without them a build reads version
`dev`, and takes the commit and the time from the VCS stamp `go build`
leaves in a checkout, when there is one.

```shell
docker build --build-arg VERSION=0.44.0 --build-arg COMMIT=$(git rev-parse HEAD) \
  --build-arg BUILD_TIME=$(date -u +%Y-%m-%dT%H:%M:%SZ) -t sniffarr .
```

The process logs them once at start, and the API and the UI answer every
request with the `Server` header `sniffarr/<version>`; the UI shows them
in the footer of every page.

## Generating code

The generated code under `internal/apis` is committed. After changing a `.proto` file,
regenerate it with [buf](https://buf.build). buf and the protoc plugins are
tool dependencies of the module in `tools`, apart from the build's, so no
separate install is needed.

```shell
go tool -modfile=tools/go.mod buf lint
go generate ./...
```

## Checking the external ports

`hack/ports` checks, from a machine outside the cluster, a release's sniff
and probes at the node ports of the chart's Services, or at their pods'
hostPorts where no such Service selects them. It reads the ports and
the nodes' addresses with kubectl, and the sniff's node ID and the torrents
probes seed from the API: `--url`, or else `kubectl port-forward` as for
`hack/rpc`. It prints one line per check and exits 1 when any fails.

```shell
go run ./hack/ports --context saraksh
go run ./hack/ports --context saraksh -n sniffarr -addr 95.168.161.76 -seeds 16
```

- The sniff answers a DHT ping at its UDP port with the stored node ID under
  the BEP 42 prefix of the node's address, and accepts TCP.
- Other DHT nodes list the sniff at its port, the port its queries leave
  from as they see it.
- Each probe accepts TCP on the same port number as UDP, refuses a torrent it
  does not hold, and serves some of the `-seeds` newest seeded torrents.
- The DHT holds each of those torrents' peers at the probes' addresses only
  at a probe's port, the port it announces. The torrents are
  those probed at least `-min-age` ago, 35 minutes by default, since a probe
  announces its seeds every 30 minutes.

`-addr` stands in for the nodes' addresses, which are otherwise each node's
`ExternalIP`, or else its public `InternalIP`. A check that finds no evidence
either way, such as a torrent nobody announced, prints `skip`.

## Releases

`CHANGELOG.md` decides the version. To release, rename `## [Unreleased]` to
`## [X.Y.Z] - YYYY-MM-DD`, open a fresh `## [Unreleased]` above it, and merge
on the date you wrote. `mikluko/action-changelog` validates the file on every
pull request.

The repository lives at `https://sarak.sh/mikluko/sniffarr`, and its
workflows in `.forgejo/workflows` run on Forgejo Actions. On a push to `main`,
`release.yaml` runs CI, and when it passes and the newest entry names a
version no tag carries, drafts the Forgejo release from the entry. It then
calls `build.yaml`, which builds in turn the archives and the `.deb` packages,
attaching the archives and their checksums to the draft and uploading the
packages to the Debian registry, then the image
`sarak.sh/mikluko/sniffarr:X.Y.Z`, then the chart
`oci://sarak.sh/mikluko/charts/sniffarr` at `X.Y.Z`. Last it publishes the
draft, which creates the tag `vX.Y.Z` on that commit: the tag is born with the
release, nothing pushes it. A run that failed leaves a draft and no tag; the
next run replaces the draft, so re-run it. Nothing else sets a version.

On a pull request `build.yaml` builds the same as `0.0.0-pr.N`, beside CI, and
publishes nothing.

### Setting up

The repository holds no secret. The image, the chart and the packages are
pushed with an OIDC token traded for an Authorized Integration of the owner's
of the kind Forgejo Actions (Local), whose source repository is this one and
whose scope is `write:package`, since the automatic token cannot write
packages a user owns; the repository variable `REGISTRY_AUDIENCE` holds that
integration's audience. The release, its draft and its archives are made with
the automatic token.
