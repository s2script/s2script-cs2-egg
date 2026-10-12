# Feature and evidence matrix

The supported stack is Metamod + S2Script. CS2-Egg-Go is the operational
reference; licensing/adaptation credits are in NOTICE. “Implemented” means the
code and automated fixtures exist. Live game, panel, HUD and firewall acceptance
is tracked separately in [acceptance.md](acceptance.md).

| Capability | Implementation and evidence | Operator guide |
| --- | --- | --- |
| Configuration, precedence, secret redaction | `internal/config`, CLI tests | [Configuration](configuration.md) |
| Generated panel egg and 18 default controls | `internal/panel`, `internal/catalog`, generated-output tests | [Install](install.md), [catalog](bundled-plugins.md) |
| SteamCMD bootstrap/install/update/validation | `internal/steam`, preparation/process fixtures; immutable image seed | [Runtime](runtime.md) |
| Compatible Metamod/S2Script versions and pins | `internal/framework`, publisher/archive/pair tests | [Frameworks](frameworks.md) |
| Installed inventory including disabled/local entries | `internal/plugins`, discovery and CLI tests | [Plugins](plugins.md) |
| Registry resolution, dependencies, exact pins, outage fallback | Plugin TLS/ZIP/reconciliation fixtures | [Plugins](plugins.md) |
| Disabled first install/update; config/data preservation | Transaction, placement, ownership and pin tests | [Plugins](plugins.md) |
| Verified downloads and recoverable file application | `internal/artifact`, hash/archive/symlink/recovery tests | [Runtime](runtime.md) |
| Supervised console, A2S readiness and graceful stop | `internal/server`, real process-group fixtures | [Runtime](runtime.md) |
| Automatic CS2 checks and player/window-aware restart | `internal/scheduler`, `internal/wings`, durable job and race tests | [Updates](updates.md) |
| Optional node, private peer identity and delivery ownership | `internal/node`, `internal/protocol`, peer/outbox fixtures | [Node](node.md) |
| Immutable VPK/game sharing, symlink/copy/hardlink, safe fallback | `internal/shared`, lease/recovery tests and separate-UID CI | [Shared files](shared-files.md) |
| Artifact cache and versioned/chunked workshop sharing | `internal/cache`, `internal/workshop`, metadata/quota/helper tests | [Workshop](workshop.md) |
| Optional HUD/MultiAddonManager preparation | Framework game-build/dependency/config-merge fixtures; native rendering unrun | [Frameworks](frameworks.md) |
| Discord lifecycle and operational events | `internal/alerts`, HTTP/schema/ownership tests; no real webhook posted | [Webhooks](webhooks.md) |
| Bounded logs, text crash bundles, private metrics and cleanup | `internal/observability`, `internal/privateops`, `internal/crashes`, `internal/cleanup` | [Logging](logging.md), [cleanup](cleanup.md) |
| Consistent backup, stopped restore, retention/recovery | `internal/backup`, archive/hash/preview/recovery tests and separate-UID CI | [Backups](backups.md) |
| Conservative console guard and optional scoped nftables | `internal/guard`, parser/replay/ambiguity/policy/rule/helper tests | [Guard](guard.md) |
| Verified node updates, private jobs, readiness and rollback | `internal/selfupdate`, release/launcher/real-child tests | [Project updates](project-updates.md) |
| Future companion integration contract | Actual event/action schema tests; bot application/gateway intentionally separate | [Integrations](integrations.md) |
| Runtime/debug images | Nonpublishing CI builds, non-root/seed/config smoke checks; native game unrun | [Install](install.md) |
| Reproducible binaries/source and explicit release workflow | `internal/releasepack`, offline Git fixtures, no-clobber publication and anonymous verification | [Releases](releases.md) |

Shared storage, host operations, guard and node self-updates require explicit
operator configuration. Installing the standalone egg does not grant host
privileges or automatically activate these modules.
