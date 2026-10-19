# Node-managed password rotation

Approved scope: node master switch, per-server ENABLE_ROTATING_PASSWORD opt-in,
shared or unique mode, three random words, daily/weekly/bi-weekly/monthly intervals
(monthly default), and Discord notification on first generation and regeneration.

The node owns private persisted assignments, generated using crypto/rand.Int.
The separate configurable dictionary defaults to a downloaded, checksum-pinned
EFF long word list. Custom lists require 7,776 distinct ASCII lowercase words,
2–16 letters each. No public seed, date-based selection, or online random service.

Intervals begin at generation in UTC. Daily means 24 hours, weekly seven days,
bi-weekly fourteen days, monthly the same time next month clamped to its last day.
Persist the due date; restart never resets it. Catch up once after downtime.
Interval changes take effect at the next generation. Shared means only opted-in
servers on this node; unique mode rejects collisions with saved assignments.

Persist before sending, using private permissions and atomic replacement. Keep
known credentials on errors. An opted-in egg with no cached assignment must fetch
one before starting, to avoid accidentally starting public. Disabling node rotation
retains cached passwords. Opting a server out removes only its managed cfg include
on restart, returning control to its original configuration.

Amended 2026-10-19: the egg caches its assignment and sets it once through
`+sv_password` at startup. Remove the legacy managed server.cfg include and all
live/map reapplication. Operators may change or clear sv_password for playtests.
After durably saving a changed assignment, the egg requests an immediate Wings
restart of its own opted-in server. Retry failures and deduplicate successful
requests. Startup arguments contain the generated password at the operator's
request; normal logs, crash reports and status remain masked. Explicit root-only
CLI reveal is supported.

Discord uses the existing enabled alerts webhook independently of ordinary event
filters. Shared generation creates one notification. A persisted pending flag
retries after errors or daemon restart; when multiple rotations occurred during
an outage, only the current credential is delivered. Delivery is at-least-once:
a crash after posting and before acknowledging may duplicate a notification.
Do not log webhook response bodies or credential payloads.

Verify state recovery, disk failures, calendar boundaries, dictionary validation,
both gates, socket identity, concurrent shared requests, unique collisions,
config preservation, startup-only application, rotation restarts, masking and Discord retries.
No deployment or live Discord posts as part of development.

Review refinements: use one authoritative generated cfg as the egg cache, with
metadata in a comment, rather than two independently committed secret files.
Prepare the server.cfg include first. Sync file and containing directory. If a
rename is visible but directory sync fails, retain matching in-memory state and
retry durability before the node distributes it. Serialize each server's retrieval
and delivery. Revalidate pending notifications and defer regeneration of that
assignment during a bounded delivery; never start posting an expired password.
Fresh installation leaves the passwords module off, including --yes.
