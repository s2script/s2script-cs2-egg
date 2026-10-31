# Translation Merge Implementation Plan

> **For agentic workers:** Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Keep custom translation wording while adding newly shipped phrases.

**Architecture:** Prepare per-file JSON merges in the existing release staging
directory before live copies. Existing keys win; unchanged catalogs are skipped.

**Tech Stack:** Go standard library, existing verified ZIP updater and tests.

**Spec:** ../specs/2026-10-31-translation-merge-design.md

## Global Constraints

Only merge corresponding `addons/s2script/translations/**/*.phrases.json` files.
Retain existing wording and custom/obsolete keys. No locale cross-filling, new
dependency, panel variable, or plugin registry change. Preflight before copying.

## Review Focus

Empty strings count as existing keys. Invalid JSON and non-string values must
not erase custom content. No-op merges preserve bytes. Destination symlinks and
directories must not be read or replaced. Custom-only catalogs stay untouched.

## Task 1: Merge release phrase catalogs

**Files:** modify `internal/egg/addons/s2script.go`; create
`internal/egg/addons/s2script_translations.go` and
`tests/unit/egg/addons/s2script_translations_test.go`; update `docs/addons.md`.

**Interfaces:** consume `Env.UpdateS2Script` and existing release fixtures;
produce internal `s2scriptMergeTranslations(root *os.Root, target, source string)
(skip bool, err error)` for staging merged content and indicating no-op copies.

- [x] Write public updater tests for the merge and review-focus behaviors.
- [x] Run the new tests and confirm they expose overwrites before implementation.
- [x] Implement validation and staged merges; call them after destination parent
  checks and before the live copy loop. Document upgrade behavior.
- [x] Run addon tests, full suite, vet, static Linux build, and diff checks.
- [x] Review the final changes and record verification; leave the changes in the
  user's current checkout for review.

## Execution record

Ruling: work in the clean current `codex/password-cron-logo` checkout and execute
inline, consistent with the user's instruction to merge new phrases in.
Pre-flight: one task; no shared interfaces between tasks.

Review findings: generic copying applies umask and removes the live file before
writing. Add a `0664` regression and a Linux filesystem-observer regression that
rejects deletion/in-place writes of the live catalog. Both failed before the
fix. Publish translation catalogs through a confined temporary sibling, set
permissions explicitly, sync/close successfully, then rename into place.

Task 1: complete. The addon suite, full `go test -p 1 ./...`, `go vet -p 1 ./...`,
static Linux/amd64 `cs2egg` build, gofmt and diff checks pass using the official
checksum-verified Go 1.27.3 toolchain in `/tmp`. Build parallelism was limited
after the first unrestricted compiler process was killed; the baseline addon
suite then passed before the failing regression tests were added.

Final review: the two publication findings were fixed and their regressions
failed before the fix and passed afterward. Runtime reload/placeholder behavior
and plugin registry behavior stay with S2Script. Transactional rollback of
unrelated framework files remains outside this translation change; each changed
translation has atomic publication. No deployment was requested.

PR follow-up: the user requested a push and pull request. The prior password
schedule PR is already merged, and its tree matches current `origin/main`.
Create `codex/preserve-translation-phrases` from that base for this change.
