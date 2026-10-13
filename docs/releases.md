# Releases and corresponding source

The intended first release is **v0.0.1-alpha.1**. This document describes the
publication procedure; it does not claim that tag or images are already
available. Check the actual [release listing](https://github.com/s2script/s2script-cs2-egg/releases)
and its Release workflow, including **public-verification**. Native acceptance
remains separate in [acceptance.md](acceptance.md).

## Build a release

Use a clean checkout at a canonical SemVer tag, including annotated tags,
with Go 1.27.2, Git and Make. The tag must dereference to HEAD. Tracked
modifications and untracked source files are rejected. Output must not already
exist and must be outside the checkout.

```sh
make release RELEASE_TAG=v0.0.1-alpha.1 OUT=/tmp/s2script-release-v0.0.1-alpha.1
go run ./cmd/releasectl verify --output /tmp/s2script-release-v0.0.1-alpha.1
```

The command reads the exact committed Git tree, exports the corresponding
source, and builds from that export. Source/deployment tarballs have sorted
entries, normalized owners/modes, commit timestamps and deterministic gzip
headers. Linux amd64 binaries use CGO=0, trimpath, no VCS path data and the
explicit version/source revision. Reproducibility tests build twice in separate
directories and compare every output. The debug container's distribution
packages are not claimed byte-for-byte reproducible.

For a release vVERSION, the assets are:

| Asset | Purpose |
| --- | --- |
| `s2egg-vVERSION-linux-amd64` | Container supervisor |
| `s2node-vVERSION-linux-amd64` | Node, launcher and helper subcommands |
| `s2script-cs2-egg-vVERSION-source.tar.gz` | Exact corresponding project source |
| `s2script-cs2-egg-vVERSION-deployment.tar.gz` | Service units/drop-in, examples, schemas and notices |
| `egg-s2script-cs2.json` | Panel egg referencing the immutable runtime tag |
| `s2script-release-v1.json` | Strict updater manifest, hashes, sizes and compatibility |
| `SHA256SUMS` | Hashes of all preceding assets; excludes itself |
| `release-images-v1.json` | Publication receipt for runtime/debug image digests |

The image receipt is produced after image publication, so it is not in the
initial local package/checksum inventory. Its GitHub publisher digest is verified
separately. GitHub's HTTPS publisher digest is integrity evidence, not an
independent cryptographic publisher signature.

## Publish

1. Finish review, CI and the acceptance record. Use a prerelease while the live
   checks are unrun. Keep release notes explicit about known limitations.
2. Review source visibility. The public-distribution workflow refuses a private
   repository; it never changes repository or package visibility.
3. Create a canonical tag at the reviewed merged commit. Publish a GitHub release
   with a matching prerelease flag. For example, `v0.0.1-alpha.1` requires
   **Set as a pre-release**; `v1.0.0` requires that box unchecked. Write `v0.0.1`,
   with no dot between `v` and the first number. The workflow validates both
   fields before installing Go or running tests. Only `release:published` triggers publication;
   tag pushes, PRs and ordinary CI never publish.
4. The read-only job tests and packages the exact tag. The serialized publisher
   regenerates the package, uploads without clobbering, checks every API digest
   and size, and verifies anonymous access to the corresponding source archive.
5. It publishes `ghcr.io/s2script/s2script-cs2-egg:vVERSION` and
   `:vVERSION-debug`. Immutable version tags must be absent. OCI labels include
   version, revision, license, repository and the exact source archive URL.
6. Check **public-verification**. It uses a clean Docker configuration and no
   login/token in its requests to read source, assets and both immutable image
   identities. A successful authenticated push alone is insufficient.

New GHCR packages can remain private even when the repository is public.
An organization/package administrator must deliberately choose public package
visibility if public pulls are intended. GitHub documents that a public package
cannot subsequently be made private. This workflow never makes that decision.
After access is corrected, rerun **only the failed public-verification job**:
it reads the saved release receipt and performs no uploads or tag promotion.

### Channels and partial failures

`stable` / `stable-debug` advance only to newer stable versions.
`prerelease` / `prerelease-debug` advance to the greatest eligible SemVer,
including stable releases. There is no `latest` alias. Existing aliases must
have valid source/version/digest identity; equal-version conflicts fail.
Release jobs are serialized across the repository. Runtime/debug aliases cannot
switch atomically: select immutable tags or digests for coordinated deployments.

The checked-in [panel egg](../pterodactyl/s2script-cs2-egg.json) offers the
`prerelease` channel first and `stable` as an alternative, with a raw GitHub
update URL. Its installer uses the standard Pterodactyl Debian installer image;
CS2 installation runs on first server startup. Release asset eggs stay pinned
to their immutable image and do not inherit the moving channel's update URL.
The `stable` choice is usable only after a stable release has published it.

### Recovering the first failed alpha

The October 13, 2026 [release run](https://github.com/s2script/s2script-cs2-egg/actions/runs/38418225357)
used `v.0.0.1-alpha.1` and had `prerelease: false`. Packaging rejected the extra
dot; after fixing the tag, the incorrect prerelease flag would also block
publication. The run never entered the publisher and the release had no assets
when inspected.

After merging the fix and passing CI, create **`v0.0.1-alpha.1` at that merged
commit**, then publish its GitHub release with **Set as a pre-release** checked.
This is a new tag; retain the failed tag as evidence. Rerunning the old action
would still use its original tag, event metadata and source revision. Editing
release notes does not trigger publication. Check the new release's publisher
and anonymous verification jobs before using the panel image.

Publication can be partially complete. The updater fails closed on absent,
mismatched or incompatible assets. A failure never overwrites an asset or
version tag. Inspect the exact failed step, uploaded assets, receipt and
registry state. Preserve evidence and publish a new version for a corrected
build; do not reuse a version for different bytes. Retrying the publisher on a
partially populated release intentionally fails. Any cleanup/repair requires a
maintainer's explicit decision. If only anonymous visibility verification
failed, preserve successful publication and rerun that read-only job.

## Installing downloads

After verification, download the manifest, checksums and selected assets from
the same release. Check `sha256sum -c SHA256SUMS` in the complete asset directory
(or check only the entries you downloaded). Keep LICENSE, NOTICE and
third_party/Go-LICENSE when redistributing binaries; keep corresponding source
available. Use the deployment archive's examples as templates, replacing IDs,
UIDs, groups and paths before provisioning.

The version commands are `s2egg version` and `s2node version`. Unversioned
source builds say `dev (unknown)` and cannot be automatic-update baselines.
See [project-updates.md](project-updates.md) for the root-owned pinned launcher,
bootstrap and rollback procedure. Downloading a release does not itself enable
any service.

Maintainer references:
[GitHub asset integrity](https://docs.github.com/en/rest/releases/assets?apiVersion=2022-11-28),
[package visibility](https://docs.github.com/en/packages/learn-github-packages/configuring-a-packages-access-control-and-visibility),
[Docker image inspection](https://docs.docker.com/reference/cli/docker/buildx/imagetools/inspect/).
