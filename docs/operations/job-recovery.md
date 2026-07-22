# Job recovery

Workers use leases, an immutable context snapshot, and a non-null canonical
input page revision. Every claim inserts a `transcription_job_attempts` row with
the attempt number, snapshot, input revision, owner, opaque token, and start
time in the same transaction that marks the queue row running. If a worker
exits, reclaim first closes the prior row as `lease_expired`, then creates the
next attempt and token. The old token can no longer update progress, fail the
job, renew its lease, or commit a canonical page.

Attempt outcomes are `completed`, `retryable_failed`, `failed`, `canceled`,
`superseded`, and `lease_expired`. Only the transition from `running` to one of
those outcomes is permitted. Completion records its result revision in the same
transaction as the AnnotationPage compare-and-swap, derived index, completion
event, and webhook outbox. Retryable provider failures use bounded exponential
backoff. Non-retryable failures and exhausted attempts move the job to `failed`
with a categorical, redacted reason; a replacement request uses the distinct
job status `superseded`.

Canceling a multi-file ingest batch atomically marks every pending job canceled
and closes each currently running attempt as `canceled`. This invalidates the
worker token before incomplete images are removed, so a late worker cannot
restore progress or commit output after the batch cancellation returns.

Operator sequence:

1. confirm database and provider readiness;
2. inspect queue age, job status, input revision, and ordered attempt history;
3. correct the external cause;
4. create a new transcription job after correcting the cause; creation
   supersedes any stale active job while preserving the previous audit row;
5. verify the result committed against the expected page revision;
6. leave an audit record of the intervention.

Never repair a job by editing queue or attempt rows, changing a lease timestamp,
or publishing the same message manually. Attempt rows are audit evidence, not a
retry control surface. Correct the dependency and let normal recovery reclaim
an expired lease, or submit a new request so the old job and attempt are marked
`superseded`. Lease tokens are stored for fencing but are never returned by the
Connect API or written to logs.

The transcription dead-letter topic has a persistent monitor subscription. A
non-empty DLQ means a job exceeded Pub/Sub delivery attempts. Inspect the
corresponding application job, canonical input revision, attempt history, and
outbox events before acknowledging the monitor message; never republish the
same payload as an operator retry. Restore the dependency and submit a new
application request when replacement work is required.

The same rule applies to annotation-mirror and resource-cleanup outboxes.
Inspect their attempt, lease, next-attempt, and terminal error fields; restore
the external dependency and allow the dispatcher to reclaim the row. Never
delete an outbox row to hide a failed Triplet or upload-blob operation.

Uploaded images use a server-generated immutable identity consisting of their
SHA-256 digest plus a random UUID. An ingest of identical bytes therefore never
reuses an object that an older cleanup worker may be deleting. Failed writes and
post-write processing failures are compensated immediately and also recorded in
the durable cleanup outbox. Upload-blob deletion keeps its monotonic attempt
count and retries beyond the normal threshold instead of becoming terminal,
with exponential backoff capped at one hour; Triplet cleanup remains bounded
and terminal failures require operator attention. Alert on failed cleanup rows,
oldest pending age, and upload-bucket bytes so a storage dependency outage is
visible before capacity is exhausted.

Immediately before physical deletion, the worker takes the global/workspace
quota guards, locks current image references, and commits `delete_fenced_at`.
Every local-upload reference creator takes those same guards and rejects a
fenced identity. The tombstone remains set across lease recovery and delete
retries, so a newly committed reference can never race a worker that is still
allowed to delete the blob; successful deletion removes the outbox row.

Before its first blob write, an ingest transfers its reserved bytes to a staged
cleanup row containing the generated object key. A clean commit retires that
row only after an `item_images` reference exists. If the process is killed at
any point between staging, local write, shared-store write, processing, and the
canonical insert, the row becomes eligible after the reservation deadline and
the normal idempotent cleanup worker removes the orphan. On dispatcher startup,
Scribe also removes only `.scribe-upload-*` atomic-write temporaries older than
one hour; recent temporaries, symlinks, directories, and canonical upload names
are preserved.

Pending upload cleanup retains the originating workspace and exact object size.
Those bytes continue to consume both workspace and global quota through retries,
including expired cleanup leases. Do not remove or zero an upload cleanup row to
free capacity: restore the blob backend and let an idempotent delete complete.
Items must be deleted through the application path, which removes every
relational child in one child-first transaction before committing the durable
external cleanup handoff. Scribe intentionally uses no database foreign keys;
direct parent-row deletion is unsupported because it bypasses repository
ownership validation, quota accounting, and outbox creation.
