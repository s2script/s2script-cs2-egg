# S2Script CS2 Egg Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver the approved GPL-3.0-or-later S2Script-only CS2 egg, optional node service, plugin management, lifecycle webhooks, public documentation, and the complete operational parity scope.

**Architecture:** A Go entrypoint (`s2egg`) owns each server and an optional Go node service (`s2node`) coordinates host-wide resources. Immutable shared generations, recoverable file transactions, and a versioned local protocol separate game availability from optional services. Three sequential plans below implement independently usable milestones without dropping later requirements.

**Tech Stack:** Go 1.27.2, Linux amd64, standard-library HTTP/JSON/archive/testing packages, SteamCMD, Docker, Wings, systemd; nftables for the optional node guard. Add narrowly scoped dependencies only where maintained parsing/client code is preferable to inventing protocols; lock all versions and record licenses.

**Spec:** [Approved design](../specs/2026-10-11-s2script-cs2-egg-design.md).

## Global Constraints

- License project code under GPL-3.0-or-later, matching CS2-Egg-Go.
- Linux x86-64 is the supported platform; both Pterodactyl and Pelican are targets; the node daemon is optional.
- App ID is 730.
- No CounterStrikeSharp, Swiftly, or ModSharp installer or selector is included.
- Maintain a list of installed plugins and check every plugin on that list at startup.
- Entries can pin a version and can start disabled; a disabled plugin is placed in `addons/s2script/plugins/disabled/` and still receives updates unless pinned.
- Bundled plugins inherit S2Script's standard enablement unless the operator overrides it.
- Deliver webhook notifications in the first runnable server milestone.
- No Docker socket or Wings administrator credential is mounted into a game container.
- The whole task is not complete merely because the first increment runs.

## Review Focus

- A disabled entry with a newer registry version must update without becoming briefly loadable: Plan 1, Tasks 4–6.
- An exact bundled-plugin pin can conflict with a newer runtime; no update may overwrite the pin or mix a native generation: Plan 1, Tasks 3–5.
- SIGKILL/power loss cannot be reported reliably by a dead standalone process; distinguish node-observed disconnection from a confirmed exit: Plan 1 Task 7, Plan 2 Task 8.
- A stale or disconnected shared mount must never become the target of standalone SteamCMD: Plan 2 Task 9.
- A public release must include actual working source/artifact links and must not imply live verification when only fixture tests ran: Plan 3 Task 16.

---

## Read this before execution

The user approved the design with three amendments, all incorporated into the spec: GPL-3.0-or-later instead of MIT; server lifecycle webhooks now; explicit plugin pins/initial disabled placement plus updates for disabled entries. Do not ask for design approval again.

Product implementation has not started. These plans are pending user review and execution-method selection as required by the configured writing-plans workflow. Native execution is recommended: the startup, plugin, shared-file, and lifecycle tasks depend closely on each other's contracts. A final independent review remains part of that execution method. Subagent-driven execution is also available if the user prefers per-task independent implementation/review.

## Plan sequence

| Order | Plan | Working milestone | Tasks |
| --- | --- | --- | --- |
| 1 | [Server and plugins](2026-10-11-s2script-server.md) | Standalone egg/image with CS2, Metamod/S2Script, inventory, pins/toggles, startup updates, and lifecycle webhooks | 1–7 |
| 2 | [Shared node services](2026-10-11-s2script-node.md) | Optional node daemon, shared game generations, coordinated updates/restarts, artifact cache, workshop sharing | 8–11 |
| 3 | [Operations and release](2026-10-11-s2script-operations.md) | Logs/crashes/cleanup, metrics, backups, guard, extended alerts, self-update, release assets, public operating docs | 12–16 |

Read the approved spec before each plan. Complete tasks in dependency order. Independent operations modules may be delegated only under the selected execution workflow. Commit each reviewed deliverable and update the feature-status table in `docs/parity.md`; a missing capability is not a finished feature.

## Environment and validation

The current repository has only the design commit. `go` is not installed. Docker client 29.7.2 exists, but connecting to `/var/run/docker.sock` is denied even outside the filesystem sandbox. No daemon access, privileged network namespace, CS2 installation, or Wings test node has been verified.

Task 1 can download the official Go archive into a project-specific directory under `/tmp` and verify its digest, without changing system packages. At planning time the official download metadata selected:

```text
https://go.dev/dl/go1.27.2.linux-amd64.tar.gz
sha256 ecbadb99091a3f46e31f5f934b068b1864eafa7995211b39eaddf76996045fe5
```

Use Go 1.27.2 consistently in `go.mod`, CI, and the Docker build stage. A newer toolchain needs an explicit recorded reason and updated verification, not an unreviewed `latest` image. Bind Go build/module caches to task-owned writable directories when needed. Follow the environment's network and filesystem approval rules.

Automated gates: focused tests per task, `go test -race ./...`, `go vet ./...`, builds of both Linux binaries, egg/schema generation checks, and shell/static checks for any packaging scripts actually introduced. Run broader gates once each dependency milestone stabilizes, or after changes that justify rerunning them.

Separate release acceptance from these tests: image build; real CS2 readiness and `meta list`; plugin update/disabled/pin behavior; two-container shared files; Wings restart policy; backup restoration; HUD with a client; firewall enforcement in an isolated host. Record unrun checks and their concrete environmental reason. Do not change this machine's Docker permissions, system services, mounts, or firewall just to make tests pass.

## Completion record

- [ ] Plan 1 implemented and its fixture/build checks pass.
- [ ] Plan 2 implemented and its fixture/build checks pass.
- [ ] Plan 3 implemented and its fixture/build checks pass.
- [ ] Whole-branch review findings resolved and affected checks rerun.
- [ ] Public README and `docs/parity.md` accurately distinguish tested, implemented-but-not-live-verified, and incomplete capabilities.
- [ ] Live acceptance outcomes recorded; release status remains provisional if required live checks could not run.
- [ ] Source, licenses, egg, image build files, service assets, and installation instructions are deliverable without unpublished download promises.
