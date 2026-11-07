# akc: fork changes

This directory holds every change this fork makes to CLIProxyAPI. Each module
lives in `akc/ak-<module>/`. Outside `akc/`, the fork changes upstream files
only through the hooks listed below, plus two test files at fixed paths.

## ak-router

`routing.strategy: ak-router` selects the ak-router strategy. The value is
matched like upstream values: surrounding spaces are trimmed and letter case is
ignored. There are no aliases; any other value behaves as upstream (unknown
values run `round-robin`).

```yaml
routing:
  strategy: ak-router
  session-affinity: true   # optional; all session-affinity settings apply unchanged
```

ak-router currently selects exactly like `fill-first`. For the same
credentials, credential state, request and settings, it picks the same
credential or returns the same error, with session affinity on or off. The
manager and scheduler treat it as fill-first, so every request path behaves
the same. Package `akrouter` (`akc/ak-router/`) delegates `Pick` to
`FillFirstSelector`. Its `FillFirstEquivalent()` marker method tells the hooks
to apply fill-first semantics.

Where the value can be set or read:

| Surface | Behavior |
| --- | --- |
| `config.yaml` and hot reload | Accepted; switching to or from `ak-router` replaces the selector like any strategy change |
| `PUT /v0/management/config.yaml` | Accepted |
| `GET /v0/management/routing/strategy` | Reports `{"strategy":"ak-router"}` |
| `PUT /v0/management/routing/strategy` | Rejects `ak-router` with 400 `invalid strategy` (upstream behavior; this fork does not change management endpoints). Set the value through the config file or the full-config endpoint instead. |

## Hooks in upstream files

Each hook only adds lines, so upstream edits around it merge cleanly unless
they change the same lines. Every hook line carries the comment `akc hook H-n`.

| ID | File | Function | Adds |
| --- | --- | --- | --- |
| H-1 | `sdk/cliproxy/service_config.go` | `normalizedRoutingRuntimeState` | `case akrouter.Strategy:` sets the routing state; plus one import |
| H-2 | `sdk/cliproxy/service_config.go` | `newRoutingSelector` | `case akrouter.Strategy:` builds `akrouter.NewSelector()` |
| H-3 | `sdk/cliproxy/auth/conductor_selection.go` | `isBuiltInSelector` | `case interface{ FillFirstEquivalent() }:` returns true |
| H-4 | `sdk/cliproxy/auth/scheduler.go` | `selectorStrategy` | the same case returns fill-first scheduling |

Test files at fixed paths. Their hooked functions are unexported, so the tests
must sit in the same packages:

| ID | File | Covers |
| --- | --- | --- |
| T-1 | `sdk/cliproxy/akc_hooks_test.go` | H-1, H-2, hot reload |
| T-2 | `sdk/cliproxy/auth/akc_hooks_test.go` | H-3, H-4, using a local type with the marker (importing `akrouter` there would be an import cycle) |

The tests in `akc/ak-router/` compare ak-router with fill-first, both as
selectors and through two real managers with session affinity off and on.

## Branches and tags

| Ref | Content |
| --- | --- |
| `main`, every other upstream branch, every upstream tag | Exact mirrors of upstream |
| `main-fork` | Fork branch: an upstream release plus the fork changes |
| `vX.Y.Z-fork` | `main-fork` after merging upstream release `vX.Y.Z`, checked |

Only upstream release tags (`vX.Y.Z`) are merged into `main-fork`, never
upstream `main`. So each fork tag is exactly one upstream release plus the
fork changes.

### One-time setup

1. Turn GitHub Actions off for the fork (Settings → Actions → General →
   "Disable actions"). Upstream workflows publish releases and images on tag
   pushes. The sync script refuses to run while Actions are enabled.
2. Create `main-fork` from the base release and commit the fork changes on it.
   The default branch stays `main`, a mirror.
3. Use git credentials that can push to the fork. Mirroring upstream commits
   that change `.github/workflows` needs a token with the `workflow` scope.

### Sync

```sh
git switch main-fork
akc/scripts/sync-upstream.sh --dry-run          # print the plan, push nothing
akc/scripts/sync-upstream.sh --until v8.0.48    # example: handle releases up to v8.0.48
akc/scripts/sync-upstream.sh                    # handle every pending release
```

The script needs `git`, `gh` (logged in) and `go`. It runs these steps:

1. **Preconditions.** Checks that `main-fork` is checked out with a clean
   tree, that Actions are off, and that the fork's `main-fork` has no commits
   missing locally. Adds the `upstream` remote if it is missing.
2. **Mirror.** Pushes every upstream branch to the fork under the same name
   (forced; never `main-fork`) and pushes upstream tags missing in the fork.
   Branches that exist only in the fork are reported, not deleted.
3. **Releases.** Upstream tags `vX.Y.Z` from the base release on that have no
   `vX.Y.Z-fork` tag, in version order, up to `--until` when given.
4. **Merge.** Runs `git merge --no-ff vX.Y.Z` into `main-fork`, or skips it
   when the release is already contained.
5. **Check and tag.** Runs `gofmt -l` on the Go files `main-fork` changes,
   `go build ./cmd/server` and `go test ./...`. Then it creates the annotated
   tag `vX.Y.Z-fork` and pushes `main-fork`, then the tag.

| Exit | Meaning | What to do |
| --- | --- | --- |
| 1 | A precondition failed | Fix what the message names and rerun |
| 2 | Merge conflict; the merge was aborted | Merge the release by hand, resolve (usually hook lines), commit, rerun |
| 3 | A check failed | Fix on `main-fork`, commit, rerun |
| 4 | An upstream tag now points elsewhere | Investigate upstream before syncing again |

Nothing is pushed for the release that stopped. A rerun skips releases that
already have their fork tag and skips merges already done.

Environment overrides: `AKC_UPSTREAM_URL` (upstream URL used when adding the
remote), `AKC_FORK_REPO` (`owner/name` of the fork on GitHub; default: derived
from `origin`), and `AKC_SYNC_BASE` (first release that gets a fork tag;
default `v8.0.47`).
