# Processing pipeline

An ingest request creates an item and one or more item images. Each image moves
through explicit, observable stages:

```text
ingest -> validate -> segment -> transcribe -> build AnnotationPage -> review
       -> revisioned save -> derive exports/metrics -> publish
```

Long-running work is represented by idempotent jobs. A job records an immutable
snapshot of its selected processing context, the canonical input revision,
transient per-segment progress, its attempt count, and terminal status. Each
claim also creates an immutable attempt-history row containing the frozen
context and canonical input revision. Workers fence every write with the job,
attempt number, lease token, and input revision so a delayed attempt cannot
overwrite newer human corrections or silently pick up an edited model
configuration.

Cloud workers receive authenticated Pub/Sub pushes and can scale to zero when
idle. Each delivery finishes within its HTTP request and uses the same durable
claim and revision fence as local polling workers. Committed events push outbox
maintenance wake-ups; scheduled passes recover missed wake-ups, expired leases,
and delayed retries every thirty minutes. A transcription push attempt is
bounded to nine minutes and cancellation follows the existing bounded retry
policy. Private checkpoint rows persist the segmented page once and each
successful line separately, so later attempts resume unfinished lines without
rewriting the whole page or repeating checkpointed provider calls. Checkpoint
reads and writes enforce the current attempt, input revision, lease token, and
unexpired lease. Checkpoints are deleted transactionally on completion,
cancellation, permanent failure, supersession, exhausted retries, and job/item
deletion. Invalid checkpoints are
discarded and rebuilt from the original page. They never bypass the canonical
revision fence. If every allowed attempt hits its deadline, the job fails and
its checkpoints are removed; no partial canonical page is committed in that
case. Resuming reduces repeated work but does not guarantee completion within
the bounded attempt budget.

Queued line transcription uses `llm.line_transcribe_concurrency` (default five),
with each call also acquiring the existing global, workspace, and provider
processing quotas inside its running goroutine. These in-memory quotas are
per process/Cloud Run instance, not service-wide; multiple instances can each
use their own allowance. For queued transcription, quotas count individual
line calls; the previous queued path counted whole jobs. Operators should size
both the per-instance limits and instance count for their provider quota. The
singular current-line indicator identifies one active line and stays populated until
all active calls finish. Cancellation stops scheduling useful work and joins
every started call before returning; no cloud work continues outside the
request.

Line transcription attempts every segment before deciding whether a
retryable or unclassified line failure should retry the page. If at least one
line succeeds, the worker commits the partial page once, records the failed
line count, and leaves each failed line's canonical subtree unchanged. A
retry after an interrupted attempt reuses checkpointed successes. An attempt
with no successful results follows the bounded retry policy; cancellation and
permanent provider failures still stop immediately. A call interrupted before
its result is checkpointed may run again; provider execution is not exactly-once.

Batch cancellation uses that same boundary: the batch, its active jobs, and
their current attempt outcomes change in one transaction. A worker holding a
pre-cancellation token is fenced from progress and canonical-page writes.

The successful worker transaction commits the canonical page, derived search
index, job completion, completion event, and webhooks together. That result is
still a draft. A later explicit publish verifies the expected canonical
revision and atomically commits the public snapshot, publication event,
webhooks, and coalescing Triplet mirror record. Triplet delivery happens after
commit and is revision-fenced, idempotent, and safe to retry.

Reprocessing requires the exact positive page revision the caller reviewed.
The workspace, image, and revision form an operation-level idempotency key, so
concurrent calls and retries after a lost response do not invoke segmentation
twice. The item image is read transiently from its canonical painting resource;
reprocessing never creates a second durable upload.

Every expensive foreground operation enters a global, per-workspace, and
per-capability concurrency gate before decoding images or invoking a model.
Segmentation-only work is keyed to its installed segmentor; transcription work
is keyed to its installed provider, even when the selected context names both.
The gate is cancellable and acquired atomically across all three quotas.
Reprocess idempotency records are bound to the item image from the moment they
are reserved, are deleted explicitly with that resource by the application
transaction, and are retained only for the configured audit/retry window after
they become terminal or abandoned.

Its post-provider transaction follows the global job-before-page lock order,
replaces only the expected page revision and derived index, inserts a new
append-only OCR baseline, advances the explicit `current_ocr_runs` pointer,
supersedes the old active transcription job and attempt, creates the successor
job, completes the operation reservation, and records the event/webhooks. Any
revision conflict or late database failure rolls back all of those effects. A
retry of a committed operation replays its persisted run, revision, and job
without provider work. The RPC returns the committed canonical revision and
successor job ID so clients can immediately follow the durable work.

## Supported OCR recipes

Segmentation and transcription are independent capabilities. Letters and
medieval manuscripts start with Kraken BLLA lines; newspapers additionally use
PP-DocLayoutV3 region reading order. A line crop is never expanded to page width
or joined with a neighboring column. Gemini, GLM-OCR, or OpenAI transcribes each
crop. Canonical line annotations and their ordered revisions remain the input
to editing, export, and retry recovery.

The initial collection assumptions are Latin-script handwriting and historical
English newspapers. They determine the built-in presets, not the data model.
Additional scripts and collections use the existing trusted model registry,
workspace contexts, and selection rules. Evaluate segmentation completeness and
reading order on representative pages before adopting a collection default.
