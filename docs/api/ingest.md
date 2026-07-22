# Ingest images and manifests

Scribe exposes four explicit ingest shapes through generated Connect clients:

- `ImageProcessingService.ProcessImageURL` for one remote image;
- `ImageProcessingService.ProcessHOCR` for precomputed hOCR paired with image
  bytes or an image URL;
- `ItemService.StartUploadBatch` plus `UploadItemImage` for one or more ordered
  uploaded files; and
- `ItemService.ImportManifest` for a IIIF Presentation manifest.

There is no generic item-creation operation. Each ingest contract supplies the
data needed to create a complete item, image ownership row, canonical
AnnotationPage, OCR provenance, quota accounting, and any durable job. A
single uploaded file uses an upload batch containing one file, so upload retry,
resume, cancellation, and progress have exactly one implementation.

`ProcessImageURL`, `ProcessHOCR`, and `ImportManifest` require a stable
`idempotency_key`. Reusing a key with the same request replays the committed
result; reusing it with different content returns `already_exists`. Generated
browser clients derive keys from normalized request content. External clients
should persist a random operation identifier until they receive a committed
response.

All processing resolves a context before expensive work. The durable job stores
an immutable context snapshot, so later context edits cannot change an ingest
already in progress.

`ProcessHOCR` requires exactly one image source: either at most 100 MiB of
`image_data` or an absolute public HTTP(S) external image URL. Credentials,
local/private-network targets, other schemes, and application-relative
`/static/uploads/...` aliases are rejected. Send `image_data` to create a fresh
immutable workspace-owned upload. Local aliases are deliberately never reused:
that rule prevents a concurrent deletion of the previous last reference from
leaving a new item pointed at an already-deleted object.

hOCR is parsed as bounded XML at the shared import/provider boundary, not with
a lenient HTML or regular-expression fallback. The document is limited to 10
MiB, 128 levels, 80,000 elements, 64 attributes per element, 4 KiB of text per
word, and the canonical 10,000-annotation fan-out. Word and line CSS classes
are exact tokens. Nested word/line structures and malformed or amplified input
are rejected before storage or model work. When a use case needs page bounds,
lines, words, and plain text, it carries one parsed projection rather than
decoding the same document repeatedly.

## Manifest imports

Manifest import fetches at most the configured response-byte limit, decodes the
document, counts every declared Presentation 2 or Presentation 3 Canvas, and
then runs the IIIF validation and extraction boundary. The default maximum is
500 Canvases. Scribe authorizes a caller-selected context and reserves the
workspace-scoped idempotency key before contacting the source. A completed
request therefore replays without depending on the source still being
available. Invalid or unavailable sources leave only a bounded, retryable
failed reservation; they never create an item, image, OCR state, canonical
page, or job.
Every painting body must use an absolute public HTTP(S) URL; private targets,
credentials, relative URLs, and application-local `/static/uploads/...` aliases
are rejected. Imported resources never acquire ownership of an existing Scribe
upload.
Operators can change the bounded limit with `iiif.max_manifest_canvases`; see
[configuration](../operations/configuration.md).

For Presentation 3, the raw source provenance is capped at 20 MiB and the raw
bytes plus imported hOCR must also fit `iiif.max_manifest_import_bytes`. Both
limits are enforced before any tenant content write. hOCR requests share one
aggregate byte budget, run with at most four concurrent requests, and share the
manifest import's 90-second deadline. The raw projection is quota-accounted
exactly once per item. Presentation 2 is parsed and normalized but is not stored
as a Presentation 3 raw projection.

Presentation 3 manifests may use an array `@context` and an image `Choice`.
Scribe selects the first supported public image resource while preserving the
imported Canvas as target/provenance. `seeAlso` hOCR is optional: when it is
absent, import commits a valid empty canonical AnnotationPage and a fenced
transcription job for the selected context.

Every declared Canvas must have a unique absolute ID and a supported painting
image; Scribe rejects the whole manifest instead of silently dropping an
incomplete Canvas. Presentation 3 painting annotations must explicitly declare
the `painting` motivation and target the enclosing Canvas ID exactly. This
keeps imported order, structures, and the persisted image set from diverging.

Choice traversal is ordered and accepts `Image` resources (including an Image
inside `SpecificResource`) with JPEG, PNG, GIF, WebP, TIFF, or JPEG 2000 media
types; a missing format is allowed. Non-image bodies, unsupported formats, and
invalid or credential-bearing URLs are skipped while later candidates are
considered. Import fails when a Canvas has no supported image. Imported Canvas
URIs are bounded to the 1024-byte persistence contract before any write.

The item, every Canvas/image, optional OCR provenance, complete canonical
pages, derived indexes, jobs, quota accounting, and idempotency result are one
database transaction. A process termination cannot expose a partial manifest
item. After an expired operation lease is reclaimed, the same idempotency key
either replays the committed item or performs one clean retry; it never creates
an orphan sibling.

## Resumable upload batches

The client chooses a stable `batch_id` and calls `StartUploadBatch` once with
the ordered filename, byte size, and lowercase SHA-256 digest of every file.
This declaration, selected context, and item name are immutable. Repeating the
same request returns the existing batch; reusing the ID for different content
returns `already_exists`.

Upload each declared file with its one-based `sequence` and bytes. The server
verifies the declared size and digest before claiming a processing lease. A
successful response contains the item image, transcription job, and current
batch state. If the response is lost after commit, sending the same file again
replays the committed response without creating another image or job.

Use `GetUploadBatch` after reconnecting. Skip files in `COMPLETED`, retry files
in `FAILED` while `attempt_count < max_attempts`, and do not submit a file that
another request has in `PROCESSING`. A batch becomes `COMPLETED` only after all
declared files and jobs have been durably accepted. Partial progress remains
queryable throughout the ingest.

`CancelUploadBatch` is idempotent while the batch is active. It fences active
file requests and cancels pending or running transcription jobs already linked
to the batch. A completed batch is immutable and cannot be canceled.

Keep file bytes outside application storage. A browser can persist only the
batch ID and declaration, then ask the user to reselect matching local files
after a reload. Generated clients should treat the server batch record—not an
in-memory progress counter—as the source of truth.

## Failure handling

- `invalid_argument`: malformed declarations, unsupported image bytes, or a
  size/digest mismatch;
- `already_exists`: the batch/file is actively owned by another request, or a
  batch ID was reused with a different declaration;
- `failed_precondition`: canceled/completed batch, exhausted attempts, or a
  processing lease that lost its fence;
- `not_found`: no batch exists in the selected workspace;
- `resource_exhausted`: the durable workspace or deployment-wide byte,
  item, or image quota has no capacity; delete unused items or ask an operator
  to adjust the configured ceiling;
- `internal`: retry after obtaining current state with `GetUploadBatch`. The
  response and structured log record only a categorical failure class and safe
  operation metadata; raw provider, request, document, and subprocess
  diagnostics are deliberately not retained.

Every batch operation is workspace-scoped. External integrations should send
the workspace header through the generated transport and use an API key whose
role and scopes permit the requested item operation.
