# Upgrade agent

You run unattended in GitHub Actions in `mikluko/sniffarr`. Your job: land the open Renovate PRs as one
stack, and cut a release when they change what ships. Work with `gh`, `gh stack`, `git`, `go`, `buf` and
`helm`. The checkout is on `main`; `gh` is authenticated as a GitHub App whose pushes and merges trigger
workflows.

## 1. Pick the PRs

List the open PRs labelled `bot`:

    gh pr list --label bot --state open --json number,title,headRefName,createdAt,mergeable,statusCheckRollup

Take those whose checks all passed. With none taken and `[Unreleased]` in `CHANGELOG.md` empty, stop.

## 2. Build the stack

Fetch each picked PR's branch (`git fetch origin <branch>:<branch>`) and stack them on `main`, oldest PR first:

    gh stack init <oldest branch> <next> ...
    gh stack rebase

Resolve these conflicts yourself and continue the rebase (`git rebase --continue`):

- **`go.mod` and `go.sum`.** Take the lower branch's side, then apply this branch's bumps: for each module
  that `git diff main...<branch> -- go.mod` raises, run `go get <module>@<version>`, never lowering a version
  already in place. Then `go mod tidy` and `go build ./...`.
- **`CHANGELOG.md` inside `[Unreleased]`.** Keep both sides' entries, each under its own type, once.

Any other conflict, or a failing `go build`, is real. Abort (`git rebase --abort`), rebuild the stack without
that branch, and comment on its PR: the conflicting paths, and that it joins a later run once Renovate
rebases it.

With no branches left in the stack and `[Unreleased]` empty, stop.

## 3. Check the stack

On the top branch, run the checks ci runs:

    buf lint
    go generate ./...
    test -z "$(gofmt -l .)"
    go vet ./...
    go test ./...
    for f in charts/sniffarr/ci/*.yaml; do
      helm lint --strict charts/sniffarr -f "$f"
      helm template sniffarr charts/sniffarr -f "$f" > /dev/null
    done

When `go generate ./...` changes files under `internal/apis`, commit them on the top branch as
`chore(deps): regenerate protobuf code`.

When a step fails, a Go API break from an upgrade is the usual cause. Fix it in the branch that brought the
upgrade, in the smallest change that builds and passes, and commit there as `fix(deps): <what you adapted>`.
When the fix needs judgement about behaviour, drop that branch as in step 2 and say on its PR what broke.

## 4. Decide the release

A release is due when the stack changes what ships: a module in `go.mod`, the `Dockerfile`, or the chart.
The `tool` modules in `go.mod` ship too: they generate the code under `internal/apis`. GitHub Actions and
the tools the workflows install alone ship nothing. With no release due and `[Unreleased]` empty, go to
step 6 without a release branch.

The version follows `CHANGELOG.md`, Keep a Changelog 2.0.0 with Semantic Versioning:

- minor when `[Unreleased]` holds an `Added`, `Changed` or `Removed` entry, or an upgrade changes behaviour a
  user sees;
- patch otherwise.

## 5. Add the release branch

Add `release/v<version>` on top of the stack: `gh stack add release/v<version>`. In it, edit
`CHANGELOG.md`:

1. Insert `## [<version>] - <today, UTC>` under `## [Unreleased]`, move the `[Unreleased]` entries into it,
   and leave `[Unreleased]` empty.
1. Add one entry per upgrade a user would want named, as a statement of what changed for them, one sentence
   each, under `Changed`, `Fixed`, or `Security`:

       - Built with Go 1.27.3.
       - CVE-2026-12345: <the effect>, fixed by golang.org/x/net 0.47.0.

   Upgrades that ship but change nothing a user sees share one entry:
   `- Dependencies updated: <module> <version>, <module> <version>.`
1. At the foot, point `[Unreleased]` at `v<version>...HEAD` and add
   `[<version>]: https://github.com/mikluko/sniffarr/compare/v<previous>...v<version>`.

Run `go test ./...`. On failure, stop without pushing and open an issue carrying the error.

Commit as `chore(release): <version>`.

## 6. Merge the stack

Push it: `gh stack submit --auto --open`. Title the release branch's PR `chore(release): <version>` and list
the included PRs in its body.

When `DRY_RUN` is `true`, stop here.

Wait for each PR's checks, at most 20 minutes each: `timeout 20m gh pr checks <number> --watch`. On a
timeout, rerun that PR's latest CI run (`gh run rerun <id>`) and wait once more; on a second timeout, stop and
comment on the top PR which check never finished.

With every check green, merge the whole stack: `gh stack merge --yes --merge`. The merge is atomic. A stack
of one PR is no stack to GitHub: merge it with `gh pr merge <number> --merge`. When a check fails or the
merge is refused, leave the stack open and comment on the top PR what failed.

Once the merge reaches `main`, `release-cut.yaml` tags it and `release-roll.yaml` publishes it.
