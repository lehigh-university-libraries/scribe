# IIIF resources

Scribe owns ingest, workspace authorization, canonical OCR correction state,
and publication orchestration. [Triplet](https://github.com/libops/triplet) is
the sole HTTP server for IIIF Image and Presentation resources. Scribe consumes
and builds Presentation 3 Manifests, Canvases, AnnotationPages, and Annotations,
then publishes immutable resource representations to Triplet. OCR annotations
use the Text Granularity Extension values `page`, `block`, `paragraph`, `line`,
`word`, and `glyph`.

The supported projection of incoming Presentation 3 resources and every
Presentation resource sent to Triplet are validated with `libops/iiif-spec`.
Triplet validates and serves the Image API response contract. Scribe
additionally requires extension motivation, the Text
Granularity JSON-LD context, an inline `TextualBody`, positive pixel-based
selector geometry, resource ownership, and canonical HTTP(S) IDs. The
Annotation carries the `supplementing` motivation; a redundant `purpose` on
the body is accepted but is not required by the Text Granularity Extension.

An imported manifest can use an array-valued `@context`, `Choice` image bodies,
and extension properties. Scribe retains the exact validated, bounded
Presentation 3 source (at most 20 MiB and 128 JSON nesting levels) as internal
provenance. Emission merges
supported descriptive properties, multilingual language maps, Ranges, Canvas
properties, and selected Image properties with current Scribe state. A
validated source Image `format` and Image API `service` descriptor take
precedence over conservative URL inference; current selected identity and
dimensions still come from Scribe.
Scribe-owned identity, item lists, dimensions, publication state, and canonical
AnnotationPage references always win. Arbitrary top-level extension keys remain
in the raw provenance and are preserved when the applicable IIIF schema permits
extensions. Unknown properties in canonical AnnotationPages remain lossless
through edits, including integer and decimal extension values outside
JavaScript's exactly round-trippable numeric range. A partial or single-Canvas
projection omits a source `start` Canvas when that Canvas is not present in the
emitted `items` array.

The production browser policy permits OpenSeadragon to fetch `info.json` and
tiles from HTTPS Image API services advertised by an imported manifest. Those
cross-origin requests are anonymous and the remote service must return suitable
CORS headers. Plain-HTTP external Image API services are intentionally not
supported from an HTTPS deployment: outbound network connections remain
limited to same-origin and HTTPS destinations, and browsers also reject mixed
content. The `connect-src` policy additionally permits browser-local `blob:`
URLs so Mirador can fetch the authorized private Manifest that the editor
constructs in memory; it does not authorize another network origin.
Scribe-owned images use Triplet's same-origin Image API. Their Image API
identifier is the escaped absolute URL of Scribe's immutable raw source at
`/static/uploads/{content-hash}-{uuid}.{extension}`. Triplet authorizes every
source read against Scribe and never exposes the internal source origin. The
raw source is a whole-object boundary: a byte-range request receives the
complete authorized object with `200` and `Accept-Ranges: none`. Triplet owns
Image API ranges and derivatives. Declining ranges at the raw source prevents
Triplet's per-chunk range fan-out from becoming many complete object-store
reads.

## Presentation resource identities

The configured Triplet Presentation base is the identity prefix. For a base of
`https://scribe.example/presentation/v3`, Scribe publishes:

- `.../item-{item_id}/manifest` for the aggregate published-item Manifest;
- `.../item-image-{item_image_id}/manifest` for a one-Canvas Manifest;
- `.../item-image-{item_image_id}/canvas/page-1` for a Scribe-owned upload
  Canvas;
- `.../item-image-{item_image_id}/canvas/page-1/painting` and
  `.../painting/items/image` for the painting AnnotationPage and Annotation;
- `.../item-image-{item_image_id}/canvas/page-1/annotations` for the canonical
  published AnnotationPage; and
- `.../item-image-{item_image_id}/canvas/page-1/annotations/items/{id}` for a
  standalone canonical child Annotation.

Triplet supports public `GET`, `HEAD`, and `OPTIONS` for those resources. A
returned document's `id` is exactly the requested resource ID. Imported Canvas
IDs remain targets and provenance; Scribe embeds them in emitted Manifests but
does not publish a conflicting local Canvas representation.

The Scribe API intentionally does not register `/iiif/*`,
`/v1/items/{id}/manifest`, `/v1/item-images/{id}/manifest`, Canvas/painting
children, canonical AnnotationPage, or child Annotation HTTP routes. Reverse
proxy rules send `/iiif` and `/presentation` directly to Triplet. Draft editors
load a private Manifest with `ItemService.GetEditorManifest`. Its Canvases omit
public `annotations` references; the editor adapter injects and saves the
complete canonical draft page through authorized `AnnotationService` Connect
RPCs instead.

Anonymous Manifests contain only Canvases with an explicit publication
snapshot, and every AnnotationPage reference resolves to the most recently
published canonical revision. A later draft save does not change Triplet until
that exact new revision is explicitly published. Triplet provides wildcard,
credential-free CORS for public Presentation resources. Draft representations
never enter the public HTTP resource store.

## Canonical AnnotationPage API

`AnnotationService` owns the page-level correction contract. Use generated
Connect clients when possible; the same operations are described in the
[generated OpenAPI document](scribe.openapi.yaml).

| RPC | Required input | Result and concurrency behavior |
| --- | --- | --- |
| `ItemService.GetEditorManifest` | `item_image_id` | Returns an authorized private Presentation 3 Manifest and selected Canvas for the bundled editor. It is a draft bootstrap document, not a public dereference route. |
| `GetAnnotationPage` | `item_image_id` | Returns the complete `annotation_page_json`, its source `canvas_uri`, monotonic `revision`, and `updated_at`. |
| `SaveAnnotationPage` | `item_image_id`, complete `annotation_page_json`, `expected_revision` | Atomically validates and replaces the page and its search index. Use `0` only to create; use the exact loaded revision for every update. A stale save returns Connect `aborted`. |
| `SearchAnnotations` | `item_image_id`; optional matching `canvas_uri` | Returns a derived projection, optionally filtered by text granularity. It is not a second correction store. The optional Canvas is an opaque consistency check, never a lookup key. |
| `GetAnnotation` | `item_image_id`, canonical annotation `id` | Returns one derived index entry only when it belongs to the exact authorized image. Load the complete page for editing. |
| `PublishItemImageEdits` | `item_image_id`, the exact positive `expected_revision` returned by save | Atomically validates and snapshots that canonical revision, queues its durable mirror/event work, and returns `published_revision`, `published_at`, and `public_url`. A stale publish returns Connect `aborted`; repeating the already-published revision is idempotent. |
| `EnrichAnnotation` | `item_image_id`, `scope`, canonical line/page JSON, optional `context_id` | Re-transcribes one line or every line in a page from the exact authorized item image, and returns updated IIIF JSON without saving it. Word and other granularities are preserved without duplicate model calls. The Canvas target must match that image but is never used as its lookup or authorization key. |
| `SplitLineIntoWords`, `SplitLineIntoTwoLines` | `item_image_id`, complete draft `annotation_page_json`, `selected_annotation_id`, and any split parameters | Returns the complete transformed `annotation_page_json`. The operation is pure: it does not commit the result or return replacement fragments. |
| `JoinLines`, `JoinWordsIntoLine` | `item_image_id`, complete draft `annotation_page_json`, and at least two distinct `selected_annotation_ids` | Returns the complete transformed `annotation_page_json`. The server owns selection validation, ordering, geometry, IDs, and property-preserving replacement semantics. |

The `item_image_id` is the workspace-scoped application identity. A Canvas URI
can appear in multiple workspaces and is never sufficient for authorization or
lookup; Scribe deliberately exposes no Canvas-to-image repository lookup.
When supplied as a consistency check, Scribe trims surrounding
whitespace but otherwise treats the identifier as opaque: its query parameters
remain part of the Canvas ID. Select a projection with the typed `granularity`
field; do not append filtering parameters to the Canvas URI.
On an `aborted` save, reload the page, reapply the user's edit to the new
revision, and save again. Clients must not silently overwrite or auto-merge
unknown changes.

Structural split/join RPCs are pure complete-page transforms. A client sends
its current full draft, including unsaved annotations and unknown IIIF or
extension properties, and replaces its draft directly with the complete page
returned by the backend. Clients must not reconstruct a result by removing or
merging annotation fragments locally. Enrichment RPCs likewise accept and
return IIIF JSON so Mirador and other editors reuse the server's semantics.
Export and publish operations must read the committed page revision rather than browser state.
`SaveAnnotationPage` is the only correction save path; hOCR is an import/export
projection, not a second editable persistence API.

`ImageProcessingService.ReprocessItemImage` is an intentionally destructive
structural operation. Clients must send `item_image_id`, the selected
`context_id`, and the exact positive `expected_revision` returned by
`GetAnnotationPage` or `SaveAnnotationPage`. Scribe deduplicates the operation
by image and expected revision before contacting a segmentation provider. A
stale revision returns Connect `aborted`; an identical committed retry returns
the original OCR session, canonical revision, and successor transcription job.
When `context_id` is zero, Scribe resolves and authorizes the workspace/default
context before reserving provider work. Clients should follow the returned job
rather than implementing transcription locally.

## Public dereference and draft privacy

There is one public representation: the exact committed publication snapshot
stored by Triplet. Triplet returns strong ETags and uses conditional
`If-Match`/`If-None-Match` writes internally. Image graphs are fenced by image
revision, while the shared aggregate Manifest is delivered only by a
generation-fenced item-scoped outbox. A superseding generation waits beyond the
bounded prior operation lease, so retrying a stale image payload can never
regress the item Manifest. Public readers never select a draft or
text-granularity projection by query parameter.

The current draft is available only through authorized Connect RPCs. An
unpublished page therefore has no public Presentation representation at all,
and callers cannot use the Scribe API to probe its existence. Publishing
materializes the complete graph from one committed canonical revision:
standalone child Annotations, painting resources, the canonical AnnotationPage,
the hosted Canvas when Scribe owns it, and single/aggregate Manifests.

Triplet serves `/iiif/3/{identifier}/...`. For a Scribe-owned upload, it probes
the exact immutable Scribe source URL on every authorized derivative request.
Authenticated editors can read a source referenced by a workspace they belong
to; anonymous source access is granted only when the referenced canonical page
has an explicit publication snapshot. Provider or application credentials are
not exposed to browsers or forwarded to arbitrary origins.

Publication is a database transaction over the tenant-scoped canonical page,
the public snapshot, the coalescing image and item Triplet graph intents,
standalone-Annotation deletion tombstones, the CloudEvent, and configured
webhook deliveries. Network dispatch happens after commit. The dispatcher
replaces a parent page before deleting removed standalone children and
acknowledges a tombstone only after Triplet confirms that child is absent, so a
crash cannot leave a removed transcription publicly dereferenceable. Deleting
the image explicitly removes the snapshot and pending mirror intent in the same
child-first application transaction; durable external cleanup then removes an
already-delivered Triplet resource. No database cascade is involved.

## Derived OCR views

Only annotations carrying a supported `textGranularity` value are OCR input.
Ordinary Web Annotations—comments, tags, links, or other Mirador additions—are
preserved in the canonical page and publication but excluded from hOCR, PAGE
XML, ALTO XML, plain text, correction metrics, and other OCR-derived views.
Geometry alone never turns a generic annotation into an OCR line.

The `internal/iiif` boundary validates each standalone Manifest, Canvas,
AnnotationPage, and Annotation with `libops/iiif-spec`, including Text
Granularity semantics, before the publication dispatcher writes it to Triplet.
