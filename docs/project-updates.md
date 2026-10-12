# Project releases and node updates

CS2, Metamod, S2Script and registry plugin updates are separate from updates to this project's `s2egg` and `s2node` binaries. See [game updates](updates.md) and [plugins](plugins.md) for those settings. Project self-update is **disabled by default** and makes no metadata requests or downloads while disabled.

The default `/usr/local/bin/s2node` installation is operator-managed. An unprivileged node cannot replace that root-owned binary. Update it manually from a verified release, retaining its predecessor, and restart the service during an operator-selected maintenance window. The server image/egg is also updated by the panel operator; node self-update does not replace containers, pinned launchers or independently provisioned helpers.

## Release verification

A release contains `s2script-release-v1.json`, two Linux amd64 executables, the generated `egg-s2script-cs2.json`, matching source and deployment archives, and `SHA256SUMS`. Tag-bearing filenames include the full version, including any prerelease suffix. The manifest records the exact source commit, GPL license and protocol/helper/state compatibility. [The public schema](../schemas/release-manifest.schema.json) describes the structure; the shared Go validator also enforces exact asset identities and cross-field relationships.

The updater resolves only `s2script/s2script-cs2-egg` releases through GitHub's API. It checks the manifest bytes against GitHub's publisher SHA-256 and size before parsing, compares every binary/source/distribution descriptor to the release asset metadata, and dereferences the tag to the declared source commit. It verifies the selected node binary's exact length and digest before staging. Missing hashes, draft/incomplete releases, foreign redirects, mismatched source or assets, oversized responses and incompatible versions are refused. These checks rely on the publisher and HTTPS/GitHub trust; the manifest is not an independent cryptographic signature. See [GitHub asset metadata](https://docs.github.com/en/rest/releases/assets).

`channel: stable` selects stable versions only. `prerelease` permits both stable and preview releases and selects the highest SemVer. `version: latest` retrieves the complete bounded release listing; more than 1,000 records without reaching the end fails selection. An exact canonical version selects only that tag and never falls back. Build metadata and a leading `v` are not accepted in the configuration's `version` field. Downgrades require `allow_downgrade: true`. A previously staged version with a different binary or manifest digest is an integrity conflict.

## Optional managed installation

Use this only after release assets are published and the node/helper compatibility checks pass. No command here is run automatically by the installer. Back up node state before deployment changes. Provision and test on a disposable node first.

1. Stop the existing node service. Keep the operator-managed binary for the fixed-volume and guard helpers.
2. Install a verified release's `s2node` binary as `/usr/local/libexec/s2node-launcher`, root-owned mode `0755`, with root-owned nonwritable ancestors. This pinned launcher runs as the ordinary `s2node` account; it receives no extra capabilities. Upgrade the launcher separately when a release requires a newer launcher contract.
3. Create `/var/lib/s2node/node-releases`, owned exclusively by `s2node`, mode `0700`. The path must be `node-releases` directly under the configured node `state_dir`. Keep node policy root-owned, readable by its service account, and not group/world writable; its ancestors must have the same ownership restrictions.
4. Add the following object to the node configuration. Replace `0.1.0-alpha.1` with an **actually published verified release**; the example is not a promise that the tag exists. Select all optional modules whose initialization you require before accepting a trial.

```json
"project_updates": {
  "enabled": true,
  "schedule": false,
  "activate": false,
  "channel": "prerelease",
  "version": "0.1.0-alpha.1",
  "allow_downgrade": false,
  "interval_seconds": 86400,
  "startup_seconds": 60,
  "root": "/var/lib/s2node/node-releases",
  "required_modules": ["selfupdate"]
}
```

5. As the `s2node` account, run `/usr/local/libexec/s2node-launcher project-bootstrap --config /etc/s2node/node.json`. It verifies and stages the exact configured release, then initializes its selection. It refuses to overwrite an initialized installation. Bootstrap takes an exact version, not `latest`; development builds cannot serve as an automatic-update baseline.
6. Review [the launcher drop-in](../deploy/s2node-launcher.conf) and install it under `s2node.service.d`. Retain the node unit's existing supplementary groups and narrowly scoped writable paths. Run `systemctl daemon-reload`, then start the node service. Helper units continue executing the independently root-owned `/usr/local/bin/s2node`.
7. Once the managed baseline starts successfully, set `activate: true` to permit activation through the launcher. Optionally change `version` to `latest`, choose the desired channel and enable `schedule`. Restart the node after editing policy. `enabled: true, schedule: false` permits explicit checks/staging without scheduled network access.

A scheduled check starts after one configured interval, then persists its next deadline before admitting a job. The interval is 300–604,800 seconds, default one day. Startup trials allow 5–300 seconds, default 60. The staged systemd startup budget accommodates one maximum-length trial and one fallback. The launcher reports `READY=1` itself only after accepting the child's private inherited-pipe readiness message.

A service-owned executable directory is a deployment choice, not an integrity boundary against compromise of that service UID. Root-owned policy, launcher and privileged helpers remain separate. Never point a privileged helper at the mutable managed node binary.

## Check, stage and activate

Use the private administrator socket; containers cannot negotiate `project.update`.

```sh
s2node project-check --request check-2026-10-12
s2node project-stage --request stage-2026-10-12
s2node project-status
s2node project-status --id <returned-job-ID>
s2node project-activate --request activate-2026-10-12
```

Every mutation uses a stable request ID; the CLI generates and prints one if omitted. Admission returns a job record, not successful completion. Reuse that request ID after uncertain responses; inspect the returned job ID for the outcome. Conflicting reuse is rejected. History pages contain at most eight records and return `Next`; pass `--offset` for subsequent pages. Jobs have finite limits of eight queued and 128 retained records; published completed records become eligible for pruning after 30 days. Interrupted jobs become `uncertain` and never automatically re-execute.

`stage` downloads and durably saves a versioned executable and manifest; it does not activate it. `activate` checks the configured release, stages it and verifies compatibility against the retained current release before writing an activation request. It checks independently installed required helper capabilities through authenticated read-only discovery. No stopped game lease is acquired by discovery. Old, missing, offline or incompatible required helpers block activation and need separate operator attention.

Candidate protocol support must preserve existing container compatibility. Both candidate and predecessor must read the state each may write; irreversible migrations are prohibited during automatic activation. Release state ABI 1 covers delivery format 2 and current module journals; it does not mean every internal file has schema number 1. Core configuration/state recovery and bound sockets must succeed. `selfupdate` initialization and configured `required_modules` must succeed; live Steam, Wings, Discord or game availability is not the startup readiness criterion.

## Activation and recovery

After saving the request, the daemon stops admitting work through its closing sockets and quiesces its modules within a bounded shutdown. It exits with code 75 only for an authorized managed activation. The pinned launcher propagates that code; the reviewed unit uses `RestartForceExitStatus=75`. No updater process executes `systemctl`. An explicit service stop remains a stop. See [systemd service restart semantics](https://github.com/systemd/systemd/blob/main/man/systemd.service.xml).

On the next start the launcher records `trial-started` before executing the candidate. Successful private readiness commits that version as known-good and retains its predecessor. Failure, timeout or interruption selects the retained previous release for one fallback attempt. If fallback also fails, exit 78 prevents an automatic restart loop and leaves an actionable failed journal. A previously failed candidate is not retried automatically. The updater records separate staging, activation request and final activation/rollback outcomes; optional `node.update` webhooks use persisted event identities.

If automatic recovery cannot start a compatible node: stop the service, preserve the whole node state and release directory, inspect `activation.json` and the service journal, disable project self-update, and restore the ordinary node unit using an operator-installed verified compatible binary. Do not erase delivery ownership, scheduler or backup state to force startup. Resolve incompatible helpers or policy before returning to the managed arrangement. An interrupted fallback is conservatively a manual-recovery condition.

At most 32 version directories, including interrupted staging directories, are admitted. Cleanup is manual and offline: preserve the current, previous and pending candidate referenced by `activation.json`, and preserve evidence before removing an unreferenced version or `.stage-*` directory. Do not modify a staged executable in place. Checksums are rechecked before selection. A failed fsync/rename or incomplete directory is never treated as a selected release.

## Verification limits

Tests use local publisher fixtures, temporary managed directories, fake child adapters and a real isolated readiness subprocess. They cover bad hashes, foreign redirects, missing assets, annotated tags, channel selection, protocol/state/helper incompatibility, readonly paths, symlinks, failed/interrupted trials and explicit stops. They do not replace real system binaries, restart services, publish a project release or prove production readiness. Live systemd upgrade/rollback acceptance remains required before production deployment.
