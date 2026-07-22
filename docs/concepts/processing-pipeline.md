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
per-provider concurrency gate before decoding images or invoking a model. The
gate is cancellable and acquired atomically across all three quotas. Reprocess
idempotency records are bound to the item image from the moment they are
reserved, are deleted explicitly with that resource by the application
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
