# Events and jobs

Successful transcription commits write their completion event and webhook
deliveries in the same transaction as the canonical page. They do not publish
the resulting draft. An explicit, revision-checked publication transaction
updates the public snapshot and a coalescing, revision-fenced Triplet mirror
outbox row, and writes its publication event and webhook deliveries.
Dispatchers retry network delivery; consumers must be idempotent.

## Workspace webhook subscriptions

Webhook targets are workspace-owned API resources, not a process-global URL
list. A workspace administrator uses `WebhookService.CreateWebhook`,
`ListWebhooks`, and `DeleteWebhook`. Creation accepts a positive
`workspace_id`, one absolute public HTTPS target without credentials, query, or
fragment, and a caller-generated signing secret containing 32–1024 bytes
without surrounding whitespace. Store the secret in the receiver's secret
manager before creation: it is write-only and never appears in create or list
responses. A workspace may register at most 100 unique targets.
Scribe does not follow delivery redirects; the registered target must accept
the POST directly.

Each event transaction expands deliveries only for subscriptions belonging to
the event's workspace. Expansion and subscription creation or deletion
serialize on that workspace, so a concurrent delete cannot leave a delivery
without its subscription. Deletion removes every delivery for the subscription
before removing the subscription itself, including terminal delivery history;
the parent event remains until normal bounded event retention removes it. It
never exposes or affects another workspace's target.

## Verify a delivery

Scribe sends the structured CloudEvent JSON as the exact POST body with:

- `Content-Type: application/cloudevents+json`
- `X-Scribe-Timestamp: <Unix seconds>`
- `X-Scribe-Signature: v1=<lowercase hexadecimal HMAC-SHA256>`

The signed bytes are:

```text
X-Scribe-Timestamp + "." + exact raw request body
```

Verify before JSON decoding or any repository side effect:

1. Read the bounded raw body without reserializing it. Require a decimal Unix
   timestamp and reject a timestamp outside the receiver's small replay
   window, normally five minutes.
2. Require exactly the supported `v1=` signature form. Compute HMAC-SHA256
   with the subscription secret over the timestamp bytes, one ASCII period,
   and the unmodified body; encode the digest as lowercase hexadecimal.
3. Compare the complete expected and supplied signature in constant time.
   Missing, malformed, stale, or mismatched values are unauthenticated and
   must not reach event handling.
4. After verification, decode the CloudEvent and atomically record its `id`
   with the intended repository action. Return 2xx only after that durable
   idempotency boundary commits.

Every retry has a freshly generated timestamp and signature. Network failures
and non-2xx responses are retried, so the same CloudEvent `id` can arrive more
than once. Do not deduplicate by timestamp or assume delivery order; use the
event ID for attempt deduplication and the resource's monotonic revision for
state ordering.

## Event correlation data

Item-scoped completion, terminal failure, and publication events carry the
same correlation spine in `data`:

- numeric `workspaceId` and `itemImageId`;
- string `itemId` and `canvasUri`;
- numeric canonical `revision`; and
- caller values `externalReferenceId`, `idempotencyKey`, and `metadata` when
  the ingest supplied them.

`dev.scribe.transcription.completed` also includes `jobId`,
`completedSegments`, `failedSegments`, and `totalSegments`.
`dev.scribe.transcription.failed` includes the job ID and a bounded, redacted
`error`. `dev.scribe.annotations.published` adds `annotationCount`,
`annotationPageId`, `publishedRevision`, `publicUrl`, and `publishedAt`.
Treat the IDs as one workspace-scoped tuple and validate them against the
integration's persisted ingest record. Never perform a lookup by Canvas URI
alone.

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

The authenticated server-sent event stream and outbound webhooks are separate
delivery contracts. SSE uses the stream cursor and the caller's existing Scribe
credential; the HMAC headers above apply only to outbound webhook POSTs.
