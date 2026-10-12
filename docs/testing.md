# Verification and native acceptance

Use Go 1.27.2 on Linux amd64. All current dependencies are standard library; no `go.sum` is required.

```sh
go test ./internal/alerts ./tests/integration
go test ./...
go vet ./...
go test -race ./...
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o bin/s2egg ./cmd/s2egg
make egg-check
```

Tests use local HTTP fixtures and harmless subprocess/UDP fixtures. They verify payload redaction/mention suppression, acknowledged delivery, bounded queue overflow, alias selection, event-ID dedup, fractional/body/header retry timing, permanent failures, disabled endpoints, shutdown deadline and CLI startup-failure flushing. Runtime integration checks the entire lifecycle sequence for normal stop, crash, readiness timeout and early exit; no earlier forbidden event is discarded by the startup-failure assertions. Debug tests use an inert executable fixture, validate enabled/default-off environment and fail when the tool is absent; they never start a remote debugger.

The `CI` workflow runs tests, vet, race, Linux builds, formatting and generated-egg checks. Each image matrix job builds a source-based `runtime` or `debug` target, loads it only into the runner, and checks non-root UID, writable HOME, immutable seed files, loader/bash, notices, configuration and debug tools. It does not execute SteamCMD/CS2, publish images, upload build records/release artifacts, use registry credentials or send webhooks. Actions are pinned to reviewed official commit refs. A successful remote job supplies build/prerequisite evidence, not native-game acceptance.

Local Docker daemon access was unavailable (permission denied at `/var/run/docker.sock`). No permission/service change was made. Local image build, bootstrap execution, debug attach and live game/panel/HUD acceptance remain **unrun** until infrastructure is available. Remote CI outcomes must be checked on the actual commit. Use [install.md](install.md) for real local build commands.

## Native smoke checklist (requires a prepared test host)

1. Build both targets and run the non-game prerequisite checks. Confirm runtime/debug are non-root; default debug port is 0.
2. Start from an empty persistent root with a real GSLT and the assigned reachable UDP/TCP game allocation. Confirm image-owned bootstrap safely seeds writable `steamcmd`, uses HOME=root, initializes SDK64 and installs app 730. No Docker socket or Wings administrator credential may enter the game container.
3. Inspect first-run logs, preparation/transaction state, the installed-plugin inventory and every plugin's startup check outcome, including disabled entries. Confirm inherited release defaults and explicit enablement overrides survive restart.
4. Confirm exactly one readiness marker after a valid A2S response, live `s2egg status`, actual client connection/gameplay, `meta list`, S2Script load/version, representative plugin behavior and optional HUD rendering from a client. Query readiness alone cannot prove native loading.
5. With a separately authorized private test webhook, verify one up, one down after graceful stop, and distinct crash/down after an unexpected ready-process exit. Failed readiness must send only start_failed. Do not conduct live message delivery without that authorization.
6. Exercise `quit`, SIGTERM and direct Docker stop with the documented 30-second grace. Check child/group cleanup, released leases, no stale status and bounded HTTP outage shutdown. Repeat through each target panel's actual import/start/stop path.
7. Select the debug image, restrict an allocated TCP port, attach/continue within readiness timeout, and confirm no up while waiting. Turn debug off and verify ordinary startup. Assess any scoped ptrace permission separately on the test host.
8. Record a stopped persistent backup, source commit and image IDs. Recreate with retained state, then rehearse rollback into a separate volume and verify framework/plugin versions and policy. Image replacement alone does not reverse downloaded game state.

Record date, source/image IDs, panel/Wings revisions and actual outputs for every exercised item. Task16 tracks final acceptance; later node tasks do not silently inherit standalone native acceptance. No public playable-release claim should be based only on unit tests or image construction.
