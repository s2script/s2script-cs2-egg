# Contributing

Use Linux amd64 and Go 1.27.2. Production code uses the Go standard library.
Create a branch and keep changes focused; include a reproducer for behavioral
fixes and update operator documentation when settings or recovery behavior change.

Run `make test vet build egg-check release-check`, `go test -race ./...` and
the isolated schema check in the README. CI also builds both images and runs a
separate-UID helper fixture on a disposable runner. Do not run that privileged
fixture on a production host.

Keep generated egg/catalog files synchronized with `make egg`. Do not edit
generated output alone. Release manifests use `internal/releasecontract` as the
shared producer/consumer authority. Preserve source identity, fail-closed
validation, lease ordering, disabled placement, config/data ownership and
idempotent action receipts.

Use fake HTTP/Steam/game/helper boundaries in automated tests. Never contact a
real Discord webhook, modify host services/firewall, or download a full game as
an implicit test. Record live acceptance separately.

Include SPDX GPL-3.0-or-later headers in new project source and preserve upstream
notices. Do not commit secrets, private configuration, game binaries or generated
release binaries. A PR does not publish artifacts; maintainers use the explicit
[release procedure](../docs/releases.md).
