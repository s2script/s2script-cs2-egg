#!/usr/bin/env bash
# SPDX-License-Identifier: GPL-3.0-or-later
# Optional CLI alternative to publishing a release in GitHub's UI.
# Actions owns asset/image builds; this script never edits version files.
set -euo pipefail

usage() {
    echo 'Usage: scripts/release.sh vX.Y.Z [--prerelease] [--dry-run]'
    echo 'Publishes the supplied tag at HEAD with automatically generated notes.'
}
tag=
prerelease=false
dry_run=false
for argument in "$@"; do
    case "$argument" in
        --prerelease) prerelease=true ;;
        --dry-run) dry_run=true ;;
        -h|--help) usage; exit 0 ;;
        v*) [[ -z "$tag" ]] || { usage >&2; exit 1; }; tag="$argument" ;;
        *) usage >&2; exit 1 ;;
    esac
done
[[ "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([.-][A-Za-z0-9.-]+)?$ ]] || { usage >&2; exit 1; }
if ! $prerelease && [[ "$tag" == *-* ]]; then
    echo 'A tag with a prerelease suffix requires --prerelease.' >&2
    exit 1
fi
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
commit=$(git -C "$repo_root" rev-parse HEAD)
version="${tag#v}"
channel=stable
$prerelease && channel=beta
args=(release create "$tag" --title "$tag" --target "$commit" --generate-notes)
$prerelease && args+=(--prerelease)
if $dry_run; then
    echo "Would build node VERSION=$version CHANNEL=$channel COMMIT=$commit"
    printf 'Would run: gh'
    printf ' %q' "${args[@]}"
    printf '\n'
    exit 0
fi
command -v gh >/dev/null || { echo 'Install the GitHub CLI, or publish the release in GitHub instead.' >&2; exit 1; }
cd "$repo_root"
if gh release view "$tag" >/dev/null 2>&1; then
    echo "Release $tag already exists." >&2
    exit 1
fi
make -C "$repo_root" node VERSION="$version" CHANNEL="$channel" COMMIT="$commit"
gh "${args[@]}"
echo "Release $tag published; Actions will build the node assets and Docker image."
