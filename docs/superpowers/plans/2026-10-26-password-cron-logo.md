# Cron password decay and egg artwork implementation plan

> **For agentic workers:** Use superpowers:executing-plans to implement this plan.

**Goal:** Configure password decay with cron timing and provide the supplied PNG
as the egg list icon.

**Architecture:** Extend the existing interval setting with cron syntax and a
timezone. Continue using the store's saved deadline and existing rotation flow.
Embed the original PNG in the canonical egg and preserve it in the generator.

**Tech Stack:** Go, robfig/cron v3.0.1, JSON, original PNG.

**Spec:** ../specs/2026-10-26-password-cron-logo-design.md

## Global constraints

Legacy intervals and saved deadlines remain valid. No system crontab changes.
No changes to password generation, secret handling, or restart policy. Preserve
the supplied image bytes and the PTDL_v2 export format.

## Tasks

- [x] Add failing schedule/store tests for UTC boundaries, timezone/DST, invalid
  input, old intervals, restart persistence and catch-up. Add `Config.Next` in
  `internal/node/passwords/config.go`, keeping `Next` for UTC compatibility;
  route store generation through it. Use a strict five-field cron parser.
- [x] Update installer questions, status, doctor and Discord schedule context in
  `internal/node/passwords/module.go`; verify reporting without leaking passwords.
- [x] Test icon preservation through `eggvariants.Standalone`, add its optional
  icon field, copy the original PNG, embed it in the canonical egg, regenerate
  standalone, and verify both decoded icons equal the supplied PNG.
- [x] Document cron syntax, timezone/DST, migration behavior, sweep latency and
  Pelican artwork import; update panel variable help and changelog.
- [x] Run full tests, vet, race checks, static Linux build and a final review.

## Review focus

Malformed timezone prefixes must not reach parser panic paths. Impossible calendar
dates must not save a zero deadline. Schedule edits must not rotate prematurely.
Timezone display must match actual scheduling. Standalone generation must retain
artwork while preserving the existing variable selection.

## Execution record

- Based on `origin/main` at `8857a55`, on `codex/password-cron-logo`. Local `main`
  has unpublished commits and was preserved. Working in the clean current checkout.
- Runtime absent from PATH; use a checksum-verified temporary Go toolchain.

- Cron, timezone/DST and restart/catch-up regression tests failed before the
  implementation and pass afterward. New tests use the public API under
  `tests/unit/node/passwords/`, following CONTRIBUTING. The existing Discord
  integration test now verifies the timezone in delivered messages.
- Original PNG SHA-256: `5cfc0f9861566bbf5712644a7f4126e1c433bcb034abc515b9350b0503d6579d`.
  Both embedded icons decode to exactly those original bytes.
- Full tests, race suite, vet and static Linux/amd64 build passed. Socket-based
  tests required execution outside the restricted sandbox. Independent review found no critical or important defects.
- Ruling: preserve the standalone export's existing variable selection; removing
  its existing rotating-password opt-in is unrelated to this change.

- Follow-up: added both supplied marks to the installer's opening banner as
  compiled Unicode half-block artwork, with S2Script lime and a monochrome
  fallback using the existing UI color policy. The 72-column banner needs no
  image renderer or external assets at runtime; the EdgeGamers source is saved
  under `assets/branding/` for future updates.
- Rendered the actual banner output in color and monochrome, checked identical
  layout and absence of ANSI controls in plain output, and visually inspected
  the preview. Full Go tests, vet, static Linux build and diff checks passed
  again after this follow-up. No new unit tests for this cosmetic change.
