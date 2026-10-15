# Password rotation implementation plan

> Use superpowers:executing-plans to implement this plan in this session.

**Goal:** Deliver node-controlled rotating join passwords and Discord delivery.

**Architecture:** Node module generates and persists assignments. Additive socket
messages deliver only the caller's assignment. Egg caches and applies its value.

**Tech Stack:** Go, existing core/supervisor/alerts and panel exports.

**Spec:** ../specs/2026-10-15-password-rotation.md

## Global constraints

Both gates required; default off, unique/monthly. Three cryptographically random
words from a separate list. Persist before use. Secrets only in private state,
explicit reveal and configured Discord. Preserve valid passwords on failure.

## Review focus

- Invalid dictionary/state cannot result in an empty live password.
- Concurrent shared requests must produce only one generation/notification.
- An unopted socket cannot retrieve another server's assignment.
- Map cfg execution must not permanently restore an older password.
- Discord errors must not leak secrets or regenerate passwords.

### Task 1: Generation and persistence

Files: internal/node/passwords/{config,words,store}.go and store_test.go.
Interfaces: Config, Defaults, Open, Store.Get(server, now), Pending, durable delivery,
Reveal. Record contains password and generated/due timestamps.

- [x] Write failing schedule, dictionary, state, shared/unique, disk-error tests.
- [x] Implement generation, pinned dictionary provisioning and atomic persistence.
- [x] Run focused tests.

### Task 2: Node, protocol, Discord

Files: node/passwords/module.go, proto/proto.go, core/core.go, alerts/discord.go,
cmd/cs2node/main.go. Interfaces: PasswordQuery, PasswordUpdate, Hello opt-in,
Core.PasswordOptIn. Use Store and existing alerts.Post.

- [x] Write failing protocol, gate, socket and webhook tests.
- [x] Implement registration, scheduling, reveal and persistent Discord retry.
- [x] Run focused integration tests against local sockets and a fake webhook.

### Task 3: Egg and delivery documentation

Files: egg/passwords, egg/boot, panel exports, module and reference docs.
Interfaces: Manager.Open/Accept/Ready/Apply/Mask, consuming PasswordUpdate.

- [x] Write failing config preservation, cache, opt-out, masking and apply tests.
- [x] Implement fetch/reconnect, cfg persistence, panel toggles and docs.
- [x] Run suite, race checks, vet, builds and export validation.
- [x] Obtain independent final review; fix material findings with regression tests.

## Execution record

- Baseline full suite passed. Managed worktree was blocked by filesystem policy;
  changes isolated on codex/rotating-passwords in the writable checkout instead.
- Generation/state, socket gates and Discord, egg/cache, installer default tests
  were exercised RED then GREEN. Full suite and race suite passed before review.
- Independent review identified incomplete-JSON acceptance, stale notifications,
  multi-file egg commits, reply ordering and directory durability. Regression
  tests cover the first four; the shared writer now fsyncs file and directory and
  reports whether rename committed. The node retries uncertain durability.
- Ruling: consolidate egg cache and generated cfg into one atomic file — removes
  cross-file inconsistency. This is a new unshipped format, so no migration cost.
- Ruling: administrator/plugin password overrides and raw engine memory dumps
  remain outside the feature's control; documented ownership and log masking
  address its intended threat model. Live CS2 map timing needs deployment testing.

Final validation: full race-enabled Go suite passes after review fixes. No live
CS2 instance or production webhook was used; validate map transitions on a canary
server before rollout.
