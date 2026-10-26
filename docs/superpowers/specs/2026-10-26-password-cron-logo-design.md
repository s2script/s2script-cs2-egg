# Cron password decay and egg artwork

The requested change is configurable cron-style password decay and the supplied
Source2Script PNG as the egg list artwork, based on the latest upstream main.

## Approach

Use five-field cron syntax in the existing `modules.passwords.interval` setting,
with a separate `timezone` setting (UTC by default). The daemon computes the next
matching date with robfig/cron v3; it does not install system cron jobs. Existing
daily/weekly/bi-weekly/monthly intervals retain their elapsed-period semantics.
The installer offers free text with examples. New configurations default to
`0 0 1 * *` (midnight on the first day of each month, UTC).

An external cron job would duplicate the daemon's assignment, persistence,
notification, and restart coordination. A custom cron parser would add unnecessary
calendar logic. The existing daemon with a standard parser is the selected
implementation approach; these defaults are implementation choices.

## Behavior and constraints

- Standard minute/hour/day/month/weekday fields, wildcards, ranges, lists, steps,
  and month/weekday names. No seconds, command field, inline timezone, or macros.
- Named IANA timezones; an empty timezone means UTC. Reject invalid zones,
  malformed expressions, and schedules without a match in the parser's five-year
  search horizon. Bundle timezone data for static deployments.
- Persist UTC due timestamps. Keep already saved deadlines through config edits
  and daemon restarts; the new schedule applies at the next generation.
- Retain the 30-second sweep, shared/unique modes, one catch-up rotation after
  downtime, secret masking, Discord retries, and existing Wings restart behavior.
  Cron controls the deadline, not a guarantee of an exact-second restart.
- Show the active schedule and timezone in status, doctor, and Discord.
- Copy the supplied PNG unchanged to `pterodactyl/s2script-cs2-egg.png` and embed
  its data URI in both exports' `icon` fields. Preserve the field when generating
  standalone exports. Pelican imports it; stock Pterodactyl's format has no icon
  support. Keep the compatible PTDL_v2 export format.

## Validation

Test cron boundaries, DST, invalid schedules, legacy intervals, persistence and
catch-up, operator-facing schedule reporting, and embedded artwork preservation.
Run the full Go suite, vet, race checks and a static Linux build.
