# Events and jobs

Successful transcription commits write their completion event and webhook
deliveries in the same transaction as the canonical page. They do not publish
the resulting draft. An explicit, revision-checked publication transaction
updates the public snapshot and a coalescing, revision-fenced Triplet mirror
outbox row, and writes its publication event and webhook deliveries.
Dispatchers retry network delivery; consumers must be idempotent.

Long-running transcription APIs return a job identity. Job status distinguishes
pending, running, completed, failed, and canceled work. Retry and supersession
details are represented by attempt counters and the redacted failure reason.
Per-segment payloads are transient progress previews, not durable canonical
saves.

`ListTranscriptionJobs` returns scalar summaries through signed, opaque keyset
cursors. Pages default to 50 jobs and are capped at 100; a continuation token
is bound to the selected workspace and optional item-image filter. Summary rows
exclude context snapshots, per-segment annotation JSON, and attempt history so
the page count is also a meaningful response-cost bound. Use
`GetTranscriptionJob` for one job's full progress payload and immutable attempt
audit.

Server-sent event streams are bounded per principal/workspace and send only
authorized job updates. Every delivered event has a numeric outbox `id`; a
reconnecting `EventSource` sends it as `Last-Event-ID` and the server resumes
strictly after that cursor. The named `dev.scribe.stream.ready` event records
the initial/reconnected high-water mark. Clients subscribe first, then reconcile
their durable job snapshot after each ready event so completion cannot fall
between a snapshot and subscription. They still load the canonical page
revision rather than treating an event payload as a save.
