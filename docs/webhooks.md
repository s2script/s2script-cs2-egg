# Standalone Discord lifecycle delivery

Set `DISCORD_WEBHOOK_URL` privately in the panel or runtime environment, or use private `<root>/egg/server.json` field `webhook.url`. Empty means no requests. Generated egg/README examples contain an empty value; they never embed an operator URL. Configuration dumps and dispatcher formatting redact it. The configured URL must be HTTPS on `discord.com`, `discordapp.com`, `canary.discord.com` or `ptb.discord.com`, with `/api[/vN]/webhooks/<numeric-id>/<token>`. Query strings and fragments are rejected, including `thread_id`; forum/media thread routing is currently unsupported and is never silently discarded. Redirects are refused.

A private configuration can select delivery behavior without a URL in the public example:

```json
{
  "webhook": {
    "url": "",
    "events": ["server.up", "server.down", "server.start_failed", "server.crash"],
    "queue_capacity": 64,
    "timeout_seconds": 10,
    "max_retries": 2
  }
}
```

Default event selection is up/down/start_failed/crash. `startup.failure` is a legacy selector normalized to `server.start_failed`; selecting both sends once. `server.restart`, `server.update`, `backup`, and `guard` are selectable for future producers; this milestone does not fabricate those transitions. An empty events list selects none. Console/status/plugin-check events are filtered before HTTP work.

`server.up` follows the first successful A2S query. Readiness or preparation failure sends only `server.start_failed`, without up/down. A ready process's unexpected exit sends distinct `server.down` and `server.crash` transitions, each with its own event ID and the same RunID. A normal ready exit sends one down. ServerID persists in `egg/server-id` unless explicitly configured; RunID/CorrelationID change each run. Payloads include event/run/server IDs, UTC time, reason and bounded redacted details. Content is limited to 1900 Unicode characters, `allowed_mentions.parse` is empty, and `wait=true` requires a returned message ID for acknowledgement.

The runtime first hands facts to its separate best-effort queues (128 raw plus 16 reserved lifecycle entries, serial context-aware publication). The webhook dispatcher uses one network worker and a bounded queue (default 64; configurable 1–1024). Overflow returns an error immediately, increments dropped count and never blocks game output or stop. Pending event maps are copied before delivery. `Publish` returning nil reports queue admission, filtering or duplicate suppression, not remote acknowledgement; `Stats` is aggregate reporting, not a per-event delivery receipt. A durable node outbox must not delete records on this return value or treat republishing a failed, retained ID as another attempt. No goroutine is created per event. IDs are deduplicated while pending and for the last 1024 completed deliveries; process restart loses that memory. A duplicate accepted ID is ignored; a queue-overflowed event was not accepted and can be retried by a caller.

Each HTTP attempt has a configured 1–60 second timeout; `max_retries` (0–10, default 2) counts additional attempts. Network errors, HTTP 429 and 5xx can retry. Discord fractional `retry_after` and `Retry-After` seconds/date are honored using the larger valid delay. Malformed 429 timing or a requested wait exceeding 60 seconds fails delivery rather than retrying early. Other HTTP errors/redirects fail without retries; 404 disables the endpoint for that dispatcher lifetime. Responses are size-bounded, and URL/response/error content is never printed. Shutdown ends raw dispatch first and then allows one absolute two-second webhook flush, canceling active HTTP on deadline. Generic failed/dropped counters and deadline diagnostics report loss without exposing credentials.

This is best-effort delivery, not a durable outbox or exactly-once guarantee. Even acknowledged-ID dedup cannot prevent a duplicate Discord message after an accepted request loses its response and is retried. Queue overflow, outages, deadlines, SIGKILL, host failure or power loss can lose alerts. A standalone process cannot report its own disappearance after it dies. Local HTTP fixtures cover behavior; no live webhook was contacted.

## Future node ownership contract

Each run negotiates one delivery owner through the optional node connection. With no initial node connection/capability it uses the standalone dispatcher; a receipt-capable enabled node dispatcher may durably claim the run. The node records that ownership before acknowledgement and retains it across reconnect/restart. An ambiguous claim or a later disconnect never activates a competing local sender. There is no manual per-run owner flag, and protocol observation alone does not authorize delivery. See [node operations](node.md) for quotas, durable receipts and remote-delivery uncertainty.

When the node owns delivery, it retains the container's canonical event ID, ServerID, RunID and CorrelationID for forwarded facts and deduplicates by event ID. Node-observed host/container termination must reconcile with any container down for the same run so there is one logical down transition, while preserving crash as a distinct diagnostic. The node must separately identify its own observations and monitoring authority; it must not invent readiness from process existence or reuse a stale run's facts. Host/power-failure reporting needs an external observer outside that failed host. No container Docker socket or Wings administrator credential is introduced to support this boundary. Node tasks are separate work.

## Optional node delivery

The [node core](node.md#durable-ownership-and-delivery) negotiates and durably retains one webhook owner per run. `alerts.Dispatcher.Deliver` supplies explicit synchronous transport outcomes for the durable worker; `Publish` retains its standalone asynchronous semantics. Lost node contact never activates a second sender after ownership has been claimed.

## Host operational events

Optional selectors now include `crash.bundle`, `cleanup`, `backup`, `restore`, `guard` and `node.update`. Task12 implements the first two plus the shared producer route; later modules implement their own outcomes. No selector enables an absent module. Host schema-2 envelopes identify node, bound producer and stable action; they never impersonate a runtime run. Terminal inputs and timestamps are persisted before publish, and exact retry conflicts are rejected. The separate host quota is 256 pending/2048 hot records with the same bounded retry worker and exact cold archives. Filtered traffic consumes no durable webhook record. Content is redacted and bounded; no binary dump is attached or uploaded. See [event provenance](logging.md) and [retention](cleanup.md).
