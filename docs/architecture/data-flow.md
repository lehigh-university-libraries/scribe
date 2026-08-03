# Data flow

```text
browser or external IIIF editor
          |
          | Connect: load/save/transform/process
          v
application use case + route authorization
          |
          | atomic expected-revision transaction
          v
canonical AnnotationPage -----> derived annotation index / exports / metrics
          |
          | explicit expected-revision publish
          v
published AnnotationPage snapshot ----> anonymous IIIF dereference
          |
          +---- image publication grant ----> Triplet Image API
          |
          +---- durable outboxes ----> Triplet / events / webhooks
```

The browser keeps transient selection, viewport, history, and a local draft.
Its state is `base page + base revision + local changes + pending remote
changes`. Background results are rebased onto the draft; they are never
presented as a successful save.

Every external request has a bounded timeout and response size. Workspace input
cannot choose a credential audience or authenticated origin directly.

The browser event stream does not depend on process-local broadcast state.
Each API replica polls the shared, workspace-scoped event outbox after the
client's numeric cursor. An SSE reconnect can therefore land on a different
replica and resume with `Last-Event-ID`; ready/checkpoint events advance the
cursor across filtered records, and the client reconciles the durable job and
canonical-page snapshots after connecting.

Transcription provider and remote segmentation protocols are supplied by the
general-purpose `github.com/lehigh-university-libraries/htr` packages. Scribe's
provider registry selects installed models, exact origins/audiences, Vault
credentials, application retry policy, quotas, and audit metadata; it does not
encode vendor request or response bodies. The same HTR metrics package backs
offline model evaluation and persisted correction distances.

Multi-file ingest first commits an immutable batch declaration and context
snapshot. Per-file processing is leased and content-bound; only a fenced commit
can attach its image and transcription job. The batch row is therefore the
progress source of truth across browser reloads, retries, and lost responses.

Item deletion commits relational removal and cleanup intents in one database
transaction. Leased dispatchers then remove unreferenced upload blobs and the
Triplet AnnotationPage. Cleanup uses bounded retries, generation fences, and a
durable deletion tombstone serialized with every local-upload reference
creator, so an external outage cannot roll back the canonical delete, silently
orphan the work, or let a late reference race physical blob deletion.

Draft saves and worker transcription commits never change the public snapshot.
Publication locks the tenant-scoped canonical row, verifies the requested
revision, validates its IIIF bytes, and commits the snapshot and delivery
intents together. This keeps public reads available during downstream outages
without exposing a partially saved or newer private draft.

Triplet is the only Image API server. Scribe exposes only the immutable source
bytes under `/static/uploads/<sha256>-<uuid>.<ext>` that Triplet needs to derive
Image API resources. That source route accepts `GET` and `HEAD`; it is not a
second Image API implementation. It deliberately answers a range request with
the complete authorized object, `200`, and `Accept-Ranges: none`; Triplet owns
Image API range and derivative behavior. This whole-object contract prevents a
per-chunk range fan-out from amplifying into many complete object-store reads.
Triplet forwards the browser's `Cookie` or `Authorization` header when it
fetches a private source, but it does not forward
`X-Scribe-Workspace-ID`. A session with `annotations:read` is therefore
authorized against every workspace membership of its user. API keys, external
JWTs, and other delegated principals remain restricted to the single workspace
encoded in the principal and their annotation-read scope.

An unauthenticated source read succeeds only when
`ImageURLIsPublished` finds an explicit published canonical page referencing
the exact URL. Private responses are `private, no-store` with same-origin CORP;
published responses use public caching, wildcard CORS, and cross-origin CORP.
Both vary on every accepted credential form so an intermediary cannot reuse an
owner response as a public one. Authorization joins require matching
item-image, item, workspace, and membership tuples; the no-foreign-key schema
therefore fails closed if application-maintained relationships drift.

OCR baselines are immutable rows. `current_ocr_runs` is the explicit one-row
pointer per item image used by reads and correction metrics; creation time is
never used as a concurrency or current-state signal. Reprocessing completes its
operation reservation in the same transaction that advances this pointer, so
HTTP retries can replay the committed IDs without repeating model work.
