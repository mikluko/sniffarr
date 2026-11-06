# Development

## Container image

The image's entrypoint is `/sniffarr`, so the container's arguments are the
component flags.

```shell
docker build -t sniffarr .
docker run -e SNIFFARR_DB=/data/sniffarr.db sniffarr -api -ui
```

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

The repository lives at `https://forge.sarak.sh/mikluko/sniffarr`, and its
workflows in `.forgejo/workflows` run on Forgejo Actions. On a push to `main`,
`release-cut.yaml` runs CI, then tags the commit `vX.Y.Z` when the newest entry
names a version no tag carries. The tag starts `release-roll.yaml`, which fails
unless the tag is the newest entry of `CHANGELOG.md` at that commit, then
builds and pushes the image `sarak.sh/mikluko/sniffarr:X.Y.Z` and the chart
`oci://sarak.sh/mikluko/charts/sniffarr` at `X.Y.Z`, and creates the Forgejo
release from the entry last. A tag whose `release-roll.yaml` run failed is not
cut again: re-run that run. Run by hand, `release-roll.yaml` builds the newest
version, a prerelease included, and publishes nothing. Nothing else sets a
version.

### Setting up

The repository holds one Actions secret, `RELEASE_TOKEN`, a token of the
owner with `write:repository`: `release-cut.yaml` pushes the tag with it,
since a tag pushed with the automatic token starts no workflow. The image,
the chart and the packages are pushed with an OIDC token traded for the
owner's Authorized Integration named by `REGISTRY_AUDIENCE` in
`release-roll.yaml`, scoped `write:package`, since the automatic token
cannot write packages a user owns.
