# Security

This initial alpha has not completed production CS2/panel/firewall acceptance.
Use the latest reviewed prerelease and inspect its acceptance record. No separate
long-term support branch is currently maintained.

Do not post exploit details or credentials in public issues. If GitHub private
vulnerability reporting is enabled, use the repository's Security → Report a
vulnerability flow. Otherwise contact an organization maintainer privately
through their published contact method and request a secure reporting channel;
this project does not invent a monitored security mailbox.

Include affected commit/version, prerequisites, scope and a minimal reproducer.
Remove webhook/GSLT/Wings credentials, personal data and dump contents.

Trust boundaries include archive/registry data, writable game volumes, console
output, peer-credential Unix sockets and privileged guard policy. Game containers
must never receive a Docker socket, Wings administrator credentials, helper
policy files or the node administrator socket. Keep helpers narrowly scoped and
host guard opt-in. See [node permissions](../docs/node.md),
[guard](../docs/guard.md) and [project updates](../docs/project-updates.md).

Release hashes establish integrity against publisher metadata; they are not an
independent signing system. Corresponding source, strict compatibility checks
and retained rollback state accompany node updates.
