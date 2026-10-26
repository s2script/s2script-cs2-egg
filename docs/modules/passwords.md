# Rotating join passwords

The node generates three random words, such as `lantern-meadow-copper`, and
manages the CS2 join password (`sv_password`) for opted-in servers.

## Enable

1. Update the node binary and server image, then import the updated egg export
   so the panel has the **Rotating Join Password** variable.
2. Run `sudo cs2node install` and enable **passwords**. Choose `shared` or `unique`
   and a cron decay schedule and timezone. The module is off until explicitly enabled.
3. Configure the existing **alerts** module with a webhook for a private Discord
   channel. Password notifications include the actual password.
4. Set `ENABLE_ROTATING_PASSWORD=1` on each participating server and restart it.

Both the node module and the server opt-in are required for generation. An
opted-in server with no saved assignment refuses to start until the node provides
one. This avoids accidentally starting without its requested join password.

## Modes and decay schedules

| Setting | Options | Default |
|---|---|---|
| `mode` | `shared`: participating servers on this node share one password; `unique`: each gets a different one | `unique` |
| `interval` | Five-field cron expression, or legacy `daily`, `weekly`, `bi-weekly`, `monthly` | `0 0 1 * *` |
| `timezone` | IANA timezone such as `America/Chicago`; empty means UTC | `UTC` |

Cron sets calendar times using `minute hour day-of-month month day-of-week`:

| Expression | Decay time in the configured timezone |
|---|---|
| `0 4 * * *` | Every day at 04:00 |
| `30 3 * * MON` | Every Monday at 03:30 |
| `0 4,16 * * *` | Every day at 04:00 and 16:00 |
| `*/30 * * * *` | Every half hour |
| `0 4 * * MON-FRI` | Weekdays at 04:00 |
| `0 0 1 * *` | First day of each month at midnight |

Wildcards (`*`), lists (`,`), ranges (`-`), steps (`/`), and month/weekday names
are supported. Numeric weekdays are `0` (Sunday) through `6` (Saturday).
Use exactly five fields: no seconds, year, command, `@daily`/`@every` shorthand,
or inline `TZ=`/`CRON_TZ=` prefix. Set the separate `timezone` instead. When both
day-of-month and day-of-week are restricted, either can match, as in standard
cron. A date absent from a month is skipped: day 31 does not clamp to February's
last day. Impossible schedules, invalid timezones, and schedules with no match
within the parser's five-year search horizon are rejected.

Named zones follow daylight saving time. A skipped local time does not run; a
repeated local time can run twice. Use UTC if you want to avoid those changes.
The daemon evaluates the schedule itself; no system cron job needs installing.
The next match is strictly after generation, so a late sweep does not shift the
next deadline away from the configured clock time.

### Existing configurations

Legacy `daily` means 24 hours, `weekly` seven days and `bi-weekly` fourteen days. `monthly`
means the same UTC time next calendar month, clamped to the last day if needed
(January 31 becomes February 28). Each period starts when the password is
generated; `timezone` does not change these legacy intervals. Existing configs
keep these values when upgraded. For example, replace `"interval": "monthly"`
with `"interval": "0 4 * * MON"` to choose Monday mornings.

The due date is saved in UTC, so restarts do not reset it. Schedule and timezone
changes apply to the next generation; the already saved deadline is retained.
Node configuration changes need a daemon restart (`sudo systemctl restart cs2node`).

Connected opted-in servers are checked every 30 seconds, so a rotation can begin
up to roughly 30 seconds after its scheduled time, plus delivery/restart time.
Offline servers catch
up on their next start, generating once rather than replaying missed periods.
When a running server receives a different generated password, it saves the
assignment and requests a restart through Wings. Repeated delivery of the same
assignment does not reapply `sv_password` or restart the server again. Failed
restart requests are retried on later deliveries. Restarts are immediate,
including when players are connected. Update both the node binary and server
image for this behavior.
A server newly joining shared mode receives the existing shared assignment.
Changing modes selects the saved assignment for the new mode, or generates it
if needed. An overdue saved assignment rotates before being returned.

## Dictionary and randomness

The default dictionary is [EFF's long word list](https://www.eff.org/dice),
downloaded once from its HTTPS URL and checked against a pinned SHA-256. It lives
at `/var/lib/cs2node/passwords/words.txt`, independently of the Go source. Normal
rotation needs no internet access.

Set `wordlist` to use your own absolute path. Use one lowercase ASCII word per
line, or EFF's dice-number and word format. At least 7,772 distinct words of
2–16 letters are required; duplicates do not increase the count. Hyphenated
compounds in dice-number rows are skipped (four in the pinned EFF list), so
passwords always contain three simple words. Restart the
daemon after fixing an initially missing default list, or install it manually
from the documented source. Invalid custom lists prevent regeneration and retain
the last assignment.

Each of the three words is selected using `crypto/rand.Int` with fresh operating
system randomness. Knowing the code, dictionary, time, or server identifier does
not reveal the selection. The dictionary is public; the random selection is not.

## Discord

Initial generation and every regeneration queue a message containing the password,
server (or shared scope), schedule with timezone and next regeneration time. Shared mode posts
once per generated shared password. Delivery uses the enabled alerts module's
webhook and identity settings, independently of its ordinary `events` filter.

Pending delivery is stored with the assignment and retried every sweep and after
daemon restart. A failed webhook does not change the password again or clear it.
If multiple periods pass during an outage, the retry delivers the current
password. A crash after Discord accepts a message but before the node records
success can produce a duplicate. No notification is sent when alerts is disabled;
the current pending assignment is delivered when it becomes available again.

## Storage, recovery and disabling

Node state lives in `/var/lib/cs2node/passwords/state.json` (directory `0700`, file
`0600`). Back it up privately. A corrupt state file stops the module; restore the
state instead of deleting it and unintentionally issuing new passwords.

Each egg caches only its own assignment in
`game/csgo/cfg/cs2node-password.cfg` (`0600`). This single file contains both
assignment metadata in a comment and the engine command, so they commit together.
The egg uses this saved assignment only when building the startup command, as
`+sv_password "..."`. It does not execute the cache as a cfg, modify `server.cfg`,
or reapply the password on map activation, hibernation, or unchanged node replies.
On upgrade it removes the old marked include from `server.cfg`, preserving the
rest of that file. Other configs or plugins can still change the live password.

For a playtest, use `sv_password "your-test-password"` or `sv_password ""` in the
server console. The password manager leaves this override alone until the next
server start. Your own map configs can still override it. To opt out across
restarts, set `ENABLE_ROTATING_PASSWORD=0` in the panel and restart. A scheduled
rotation still restarts an opted-in server, restoring the generated password.

The current and previously received passwords are masked from normal egg logs and
crash tails. Node status exposes only mode, schedule, timezone and pending notification
count. Administrators can explicitly reveal a saved assignment:

```bash
sudo cs2node passwords show <full-server-uuid>
# Shared mode also accepts this convenient label:
sudo cs2node passwords show shared
```

Disabling the node module stops regeneration; enrolled servers retain their last
cached password, including during a node outage. Set a server's variable to `0`
and restart it to omit the generated startup argument and use its ordinary config.
Its original config contents are preserved. Root and that server's owner can read
the assigned password; shared mode intentionally gives participants the same one.

## Config

`modules.passwords` in `/etc/cs2node/config.json`:

```json
{
  "enabled": true,
  "mode": "unique",
  "interval": "0 4 * * *",
  "timezone": "America/Chicago",
  "dir": "/var/lib/cs2node/passwords",
  "wordlist": "/var/lib/cs2node/passwords/words.txt"
}
```

This example decays daily at 04:00 Chicago time. The new-install default is
midnight UTC on the first of each month (`0 0 1 * *`, `UTC`).
