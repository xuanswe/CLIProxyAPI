#!/usr/bin/env bash
# Sync the fork with upstream CLIProxyAPI and tag fork releases.
#
#   akc/scripts/sync-upstream.sh [--dry-run] [--until vX.Y.Z]
#
# 1. Preconditions: on main-fork, clean tree, GitHub Actions off for the fork,
#    git/gh/go present. Adds the "upstream" remote if missing.
# 2. Mirror: every upstream branch is pushed to the fork under the same name
#    (forced; never main-fork); upstream tags missing in the fork are pushed.
# 3. Releases: upstream tags vX.Y.Z from the base release on, without a
#    vX.Y.Z-fork tag, in version order (up to --until when given).
# 4. Merge: git merge --no-ff vX.Y.Z into main-fork, unless already contained.
# 5. Check and tag: gofmt on fork Go files, server build, go test ./...; then
#    the annotated tag vX.Y.Z-fork on main-fork's head; push branch, then tag.
#
# Exit codes: 1 precondition failed, 2 merge conflict, 3 check failed,
# 4 an upstream tag changed. Nothing is pushed for the release that stopped;
# after fixing and committing, a rerun resumes.
#
# Environment overrides:
#   AKC_UPSTREAM_URL  upstream repository URL used when adding the remote
#   AKC_FORK_REPO     owner/name of the fork on GitHub (default: from origin)
#   AKC_SYNC_BASE     first release that gets a fork tag

set -Eeuo pipefail

UPSTREAM_URL=${AKC_UPSTREAM_URL:-https://github.com/router-for-me/CLIProxyAPI.git}
SYNC_BASE=${AKC_SYNC_BASE:-v8.0.47}
FORK_BRANCH=main-fork

EXIT_PRECONDITION=1
EXIT_CONFLICT=2
EXIT_CHECK=3
EXIT_TAG_CHANGED=4

trap 'echo "error: unexpected failure at line $LINENO" >&2; exit $EXIT_PRECONDITION' ERR

die() {
	local code=$1
	shift
	echo "error: $*" >&2
	exit "$code"
}

usage() {
	echo "usage: $0 [--dry-run] [--until vX.Y.Z]" >&2
	exit $EXIT_PRECONDITION
}

is_release() { [[ $1 =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; }

# version_le A B: A <= B in version order.
version_le() { [[ $(printf '%s\n%s\n' "$1" "$2" | sort -V | head -n1) == "$1" ]]; }

dry_run=false
until_tag=""
while (($#)); do
	case $1 in
	--dry-run) dry_run=true ;;
	--until)
		(($# >= 2)) || usage
		until_tag=$2
		shift
		;;
	-h | --help) usage ;;
	*) usage ;;
	esac
	shift
done
if [[ -n $until_tag ]] && ! is_release "$until_tag"; then
	die $EXIT_PRECONDITION "--until needs a release tag vX.Y.Z, got '$until_tag'"
fi
is_release "$SYNC_BASE" || die $EXIT_PRECONDITION "base release '$SYNC_BASE' is not vX.Y.Z"

# --- 1. Preconditions -------------------------------------------------------

for tool in git gh go gofmt; do
	command -v "$tool" >/dev/null || die $EXIT_PRECONDITION "$tool not found"
done
cd "$(git rev-parse --show-toplevel)"

branch=$(git symbolic-ref --quiet --short HEAD || true)
[[ $branch == "$FORK_BRANCH" ]] || die $EXIT_PRECONDITION "check out $FORK_BRANCH first (on '${branch:-detached HEAD}')"
[[ -z $(git status --porcelain --untracked-files=all) ]] || die $EXIT_PRECONDITION "working tree is not clean"

fork_repo=${AKC_FORK_REPO:-}
if [[ -z $fork_repo ]]; then
	origin_url=$(git remote get-url origin)
	if [[ $origin_url =~ github\.com[:/]([^/]+/[^/]+)$ ]]; then
		fork_repo=${BASH_REMATCH[1]%.git}
	else
		die $EXIT_PRECONDITION "cannot derive the GitHub fork from origin '$origin_url'; set AKC_FORK_REPO"
	fi
fi
actions=$(gh api "repos/$fork_repo/actions/permissions" --jq .enabled) ||
	die $EXIT_PRECONDITION "cannot read the Actions setting of $fork_repo"
[[ $actions == false ]] || die $EXIT_PRECONDITION "GitHub Actions are enabled for $fork_repo; disable them first"

if ! git remote get-url upstream >/dev/null 2>&1; then
	echo "adding remote upstream: $UPSTREAM_URL"
	git remote add upstream "$UPSTREAM_URL"
	git remote set-url --push upstream DISABLED
fi

# remote_refs REMOTE: "<ref> <object>" for its branches and tags, sorted by ref.
remote_refs() {
	git ls-remote --heads --tags "$1" | awk '$2 !~ /\^\{\}$/ { print $2, $1 }' | LC_ALL=C sort
}
upstream_refs=$(remote_refs upstream) || die $EXIT_PRECONDITION "cannot list upstream refs"
fork_refs=$(remote_refs origin) || die $EXIT_PRECONDITION "cannot list fork refs"

declare -A up_obj fork_obj
while read -r ref obj; do [[ -n $ref ]] && up_obj[$ref]=$obj; done <<<"$upstream_refs"
while read -r ref obj; do [[ -n $ref ]] && fork_obj[$ref]=$obj; done <<<"$fork_refs"

if [[ -n $until_tag ]]; then
	[[ -n ${up_obj[refs/tags/$until_tag]:-} ]] || die $EXIT_PRECONDITION "--until $until_tag is not an upstream tag"
	version_le "$SYNC_BASE" "$until_tag" || die $EXIT_PRECONDITION "--until $until_tag is before the base $SYNC_BASE"
fi

# The fork's main-fork must not hold commits missing locally.
if [[ -n ${fork_obj[refs/heads/$FORK_BRANCH]:-} ]]; then
	git fetch --quiet --no-tags origin "refs/heads/$FORK_BRANCH"
	git merge-base --is-ancestor FETCH_HEAD HEAD ||
		die $EXIT_PRECONDITION "the fork's $FORK_BRANCH has commits missing locally; update $FORK_BRANCH first"
fi

# --- 2. Mirror ---------------------------------------------------------------

# An upstream tag whose fork or local copy points elsewhere stops the run.
for ref in "${!up_obj[@]}"; do
	[[ $ref == refs/tags/* ]] || continue
	if [[ -n ${fork_obj[$ref]:-} && ${fork_obj[$ref]} != "${up_obj[$ref]}" ]]; then
		die $EXIT_TAG_CHANGED "upstream tag ${ref#refs/tags/} changed: fork has ${fork_obj[$ref]}, upstream ${up_obj[$ref]}"
	fi
	local_obj=$(git rev-parse --verify --quiet "$ref" || true)
	if [[ -n $local_obj && $local_obj != "${up_obj[$ref]}" ]]; then
		die $EXIT_TAG_CHANGED "upstream tag ${ref#refs/tags/} changed: local has $local_obj, upstream ${up_obj[$ref]}"
	fi
done

git fetch --quiet --no-tags --prune upstream '+refs/heads/*:refs/remotes/upstream/*' 'refs/tags/*:refs/tags/*'

branch_specs=()
tag_specs=()
for ref in $(printf '%s\n' "${!up_obj[@]}" | LC_ALL=C sort); do
	[[ ${fork_obj[$ref]:-} == "${up_obj[$ref]}" ]] && continue
	case $ref in
	refs/heads/"$FORK_BRANCH")
		echo "warning: upstream has a branch named $FORK_BRANCH; not mirrored" >&2
		;;
	refs/heads/*)
		branch_specs+=("+refs/remotes/upstream/${ref#refs/heads/}:$ref")
		echo "mirror branch ${ref#refs/heads/}"
		;;
	refs/tags/*)
		tag_specs+=("$ref:$ref")
		echo "mirror tag ${ref#refs/tags/}"
		;;
	esac
done
for ref in "${!fork_obj[@]}"; do
	if [[ $ref == refs/heads/* && $ref != "refs/heads/$FORK_BRANCH" && -z ${up_obj[$ref]:-} ]]; then
		echo "fork-only branch, not deleted: ${ref#refs/heads/}"
	fi
done
echo "mirror: ${#branch_specs[@]} branches, ${#tag_specs[@]} new tags"
if ! $dry_run; then
	((${#branch_specs[@]} == 0)) || git push --quiet origin "${branch_specs[@]}"
	((${#tag_specs[@]} == 0)) || git push --quiet origin "${tag_specs[@]}"
fi

# --- 3. Releases -------------------------------------------------------------

releases=()
while read -r tag; do
	[[ -n $tag ]] || continue
	version_le "$SYNC_BASE" "$tag" || continue
	if [[ -n $until_tag ]] && ! version_le "$tag" "$until_tag"; then
		continue
	fi
	[[ -n ${fork_obj[refs/tags/$tag-fork]:-} ]] && continue
	releases+=("$tag")
done < <(for ref in "${!up_obj[@]}"; do
	tag=${ref#refs/tags/}
	[[ $ref == refs/tags/* ]] && is_release "$tag" && echo "$tag"
done | sort -V)

if ((${#releases[@]} == 0)); then
	echo "releases: nothing to do"
	exit 0
fi

# fork_go_files RELEASE: Go files that main-fork adds or changes over RELEASE.
fork_go_files() {
	git diff --name-only --diff-filter=d "$1" HEAD -- '*.go'
}

run_checks() {
	local release=$1 unformatted
	mapfile -t files < <(fork_go_files "$release")
	if ((${#files[@]})); then
		unformatted=$(gofmt -l "${files[@]}")
		if [[ -n $unformatted ]]; then
			echo "$release: gofmt needed:" >&2
			echo "$unformatted" >&2
			return 1
		fi
	fi
	echo "$release: gofmt ok"
	go build -o /dev/null ./cmd/server || {
		echo "$release: server build failed" >&2
		return 1
	}
	echo "$release: build ok"
	go test ./... || {
		echo "$release: go test failed" >&2
		return 1
	}
	echo "$release: go test ok"
}

for release in "${releases[@]}"; do
	fork_tag=$release-fork
	contained=false
	git merge-base --is-ancestor "$release" HEAD && contained=true

	if $dry_run; then
		if $contained; then
			echo "$release: in $FORK_BRANCH; would check and tag $fork_tag"
		else
			echo "$release: would merge into $FORK_BRANCH, check and tag $fork_tag"
		fi
		continue
	fi

	# --- 4. Merge ---
	if $contained; then
		echo "$release: already in $FORK_BRANCH"
	else
		if ! git merge --quiet --no-ff --no-edit -m "Merge upstream release $release into $FORK_BRANCH" "$release"; then
			conflicts=$(git diff --name-only --diff-filter=U)
			git merge --abort
			echo "$release: conflict in:" >&2
			echo "$conflicts" >&2
			die $EXIT_CONFLICT "merge aborted; merge $release by hand, commit, then rerun"
		fi
		echo "$release: merged into $FORK_BRANCH"
	fi

	# --- 5. Check and tag ---
	run_checks "$release" || die $EXIT_CHECK "$release: checks failed; fix on $FORK_BRANCH, commit, then rerun"

	existing=$(git rev-parse --verify --quiet "refs/tags/$fork_tag^{commit}" || true)
	if [[ -z $existing ]]; then
		git tag -a "$fork_tag" -m "$fork_tag: upstream $release with fork changes"
	elif [[ $existing != "$(git rev-parse HEAD)" ]]; then
		die $EXIT_PRECONDITION "local tag $fork_tag points to $existing, not $FORK_BRANCH's head; remove it and rerun"
	fi
	git push --quiet origin "refs/heads/$FORK_BRANCH:refs/heads/$FORK_BRANCH"
	git push --quiet origin "refs/tags/$fork_tag:refs/tags/$fork_tag"
	echo "$release: tagged $fork_tag, pushed"
done
