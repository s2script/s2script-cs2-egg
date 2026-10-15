# Rotating join passwords

The node generates three random words, such as `lantern-meadow-copper`, and
manages the CS2 join password (`sv_password`) for opted-in servers.

## Enable

1. Update the node binary and server image, then import the updated egg export
   so the panel has the **Rotating Join Password** variable.
2. Run `sudo cs2node install` and enable **passwords**. Choose `shared` or `unique`
   and a regeneration interval. The module is off until explicitly enabled.
3. Configure the existing **alerts** module with a webhook for a private Discord
   channel. Password notifications include the actual password.
4. Set `ENABLE_ROTATING_PASSWORD=1` on each participating server and restart it.

Both the node module and the server opt-in are required for generation. An
opted-in server with no saved assignment refuses to start until the node provides
one. This avoids accidentally starting without its requested join password.

## Modes and intervals

| Setting | Options | Default |
|---|---|---|
| `mode` | `shared`: participating servers on this node share one password; `unique`: each gets a different one | `unique` |
| `interval` | `daily`, `weekly`, `bi-weekly`, `monthly` | `monthly` |

Daily means 24 hours, weekly seven days and bi-weekly fourteen days. Monthly
means the same UTC time next calendar month, clamped to the last day if needed
(January 31 becomes February 28). Each period starts when the password is
generated. The due date is saved, so restarts do not reset it. Interval changes
apply to the next generation. Node configuration changes need a daemon restart.

Connected opted-in servers are checked every 30 seconds. Offline servers catch
up on their next start, generating once rather than replaying missed periods.
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
server (or shared scope), interval and next regeneration time. Shared mode posts
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
assignment metadata in a comment and the engine command, so they commit together. A marked include at the end of
`server.cfg` loads it on startup and map changes; the egg also reapplies it through
the console after map activation and when receiving updates. Avoid other plugins
or configs that independently manage `sv_password`.

The current and previously received passwords are masked from normal egg logs and
crash tails. Node status exposes only mode, interval and pending notification
count. Administrators can explicitly reveal a saved assignment:

```bash
sudo cs2node passwords show <full-server-uuid>
# Shared mode also accepts this convenient label:
sudo cs2node passwords show shared
```

Disabling the node module stops regeneration; enrolled servers retain their last
cached password, including during a node outage. Set a server's variable to `0`
and restart it to remove the managed include and return to its ordinary config.
Its original config contents are preserved. Root and that server's owner can read
the assigned password; shared mode intentionally gives participants the same one.

## Config

`modules.passwords` in `/etc/cs2node/config.json`:

```json
{
  "enabled": true,
  "mode": "unique",
  "interval": "monthly",
  "dir": "/var/lib/cs2node/passwords",
  "wordlist": "/var/lib/cs2node/passwords/words.txt"
}
```
