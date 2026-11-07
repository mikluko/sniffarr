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
request with the `Server` header `sniffarr/<version>`.

## Generating code

The generated code under `internal/apis` is committed. After changing a `.proto` file,
regenerate it with [buf](https://buf.build); the protoc plugins come from the
Go tool dependencies in `go.mod`.

```shell
buf lint
go generate ./...
```

## Releases

`CHANGELOG.md` decides the version. To release, rename `## [Unreleased]` to
`## [X.Y.Z] - YYYY-MM-DD`, open a fresh `## [Unreleased]` above it, and merge
on the date you wrote. `mikluko/action-changelog` validates the file on every
pull request.

The repository lives at `https://sarak.sh/mikluko/sniffarr`, and its
workflows in `.forgejo/workflows` run on Forgejo Actions. On a push to `main`,
`release.yaml` runs CI, and when the newest entry names a version no tag
carries, builds and pushes the image `sarak.sh/mikluko/sniffarr:X.Y.Z`, the
chart `oci://sarak.sh/mikluko/charts/sniffarr` at `X.Y.Z`, the archives and
the `.deb` packages, and last creates the Forgejo release from the entry,
with the tag `vX.Y.Z` on that commit: the tag is born with the release,
nothing pushes it. A run that failed leaves no tag; re-run it. Run by hand,
`release.yaml` builds the newest version, a prerelease included, and
publishes nothing. Nothing else sets a version.

### Setting up

The repository holds no secret. The image, the chart and the packages are
pushed with an OIDC token traded for an Authorized Integration of the owner's
of the kind Forgejo Actions (Local), whose source repository is this one and
whose scope is `write:package`, since the automatic token cannot write
packages a user owns; the repository variable `REGISTRY_AUDIENCE` holds that
integration's audience. The release and its tag are made with the automatic
token.
