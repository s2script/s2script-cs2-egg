# SteamRT4 v2.0.0

The user selected a complete SteamRT4 migration for v2.0.0 after reviewing the runtime assessment. SteamRT4 is the only v2 runtime; stable and beta remain release channels, not runtime choices.

## Approaches

- An optional RT4 image beside RT3 would allow a gradual trial, but the user explicitly chose a complete migration.
- Replace the default platform with RT4 and migrate native addons on first startup. This is the selected approach.

## Implementation decisions

- Verify Valve's actual amd64+i386 RT4 platform image and use it in the existing flattened-rootfs Docker build. Keep the static Go entrypoint and node protocol.
- Select only explicit `-steamrt4.tar.gz` MultiAddonManager assets. A pin without an RT4 asset fails with an actionable error; do not silently install RT3 or legacy Linux artifacts.
- Record the target runtime in native installation evidence. A legacy or different-runtime receipt requires reinstalling the selected version, even when its release tag is unchanged. Apply this to both native addons while preserving private configuration and data.
- A channel behind an installed version may retain it only when its files and RT4 evidence verify. An unverified newer install must request an explicit version pin for repair rather than silently downgrade or retain unknown binaries. Transient outages must never retain RT3-only evidence.
- Set the local build fallback to `2.0.0`; published release metadata still derives from tag `v2.0.0`. Document the breaking runtime migration and image/addon rollback requirements. Preserve channel-based egg image URLs.
- Add runtime checks to image CI/release verification for glibc, i386 loader, and startup tools. Verify real binaries as far as the available environment allows, and report any unperformed CS2 gameplay checks accurately.
- Runtime testing found the stock S2Script v0.12.2 shim requests an executable stack and fails `dlopen` under RT4 glibc 2.41. Preserve publisher bytes and add glibc's documented `glibc.rtld.execstack=2` only to the game child's environment when the installed shim requests it (including a missing GNU_STACK header on x86-64). Preserve other tunables and reject explicit conflicting execstack values. This permits an executable stack in that game process; a future non-executable-stack shim automatically avoids the compatibility setting.

## Acceptance

Regression tests cover RT4 selection with RT3 assets present, missing RT4 assets, same-version legacy/runtime migration, preservation of operator files, verified RT4 offline retention, rejection of legacy evidence during outages, and no unintended downgrade. Run Go tests, race tests, vet, and static builds; build and inspect the RT4 image and bootstrap SteamCMD if available. No automatic live-server deployment is part of this source migration.
