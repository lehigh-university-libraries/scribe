# Canonical IIIF annotations

The correction source of truth is a complete IIIF Presentation 3
`AnnotationPage`. OCR annotations use the IIIF Text Granularity Extension and
the `supplementing` motivation.

The persisted page has a workspace-scoped Scribe identity and a monotonic
revision. External Canvas identifiers are targets and provenance; they are not
tenant keys. hOCR, PAGE XML, ALTO XML, plain text, search rows, metrics, and
published representations are derived from a committed page revision.

## Invariants

- Every Scribe-minted Manifest, Canvas, AnnotationPage, and Annotation is
  published to Triplet at a matching `GET`/`HEAD` URL. Imported Canvas HTTP(S)
  identifiers remain external targets/provenance and are not reminted or
  served under a different Scribe identity.
- A page belongs to one workspace item image even when two workspaces import
  the same external manifest.
- Unknown canonical page/body/target/selector properties survive an edit round
  trip. A bounded raw source Manifest is retained as provenance; emitted
  Manifests merge libops-supported descriptive and nested extension properties
  while unsupported top-level extension terms remain internal.
- Source Manifest provenance is decoded and indexed once per response. Canvas
  emission performs exact-ID lookups rather than repeatedly scanning the raw
  Manifest, so aggregate emission remains linear in the Canvas count.
- Extension contexts, including Text Granularity, precede the Presentation 3
  context, which occurs exactly once as the final `@context` entry.
- Non-page OCR annotations use non-negative integer pixel origins and positive
  integer `xywh` dimensions within the owning image's authoritative width and
  height; percentage selectors are not canonical OCR geometry. A page with
  fragment geometry fails closed when those dimensions are unknown.
- Page-granularity text may target the complete Canvas without a fragment;
  derived hOCR, PAGE XML, ALTO XML, and text use the Canvas dimensions.
- When a page contains both line and word annotations, they are synchronized
  at save: a changed line updates or retires its contained words, while changed
  word CRUD updates an otherwise unchanged line. Geometry, identifiers, and
  unknown IIIF/extension properties are preserved whenever the word count
  remains compatible.
- Structural changes are performed by backend operations shared by all editors.
- Save is an atomic compare-and-swap of the complete page.
- Publication is an explicit, revision-checked snapshot; draft saves cannot
  mutate the anonymously dereferenceable representation.
- The same snapshot is the anonymous access grant for the page's Image API
  derivatives; owning workspaces retain private draft access.
- A derived representation never becomes an independent correction store.
- Generic Web Annotations without a supported `textGranularity` remain
  canonical but never enter OCR exports or correction metrics.
- OCR baseline/provenance created by a worker commits with the canonical
  revision, terminal job fence, and durable quota accounting; none can advance
  independently if capacity is exhausted.

Scribe uses `github.com/libops/iiif-spec` v0.3 validators at import, mutation,
save, publication, Image API `info.json` generation, and conformance-test
boundaries. Image API `info.json` is built with the library's generated Image 3
wire types and then schema-validated. Presentation resources use the library's
extension-aware Manifest, Canvas, Range, AnnotationCollection, AnnotationPage,
Annotation, and Text Granularity validators directly against the complete JSON
document.

Canonical Presentation JSON remains a bounded raw map so edits can preserve
unknown context-defined properties and exact JSON number tokens. The upstream
schemas establish the IIIF contract; Scribe's local checks add only its OCR
profile semantics, such as `supplementing` motivation, TextualBody content,
pixel geometry, owning-Canvas bounds, and the requirement that unknown terms
declare an extension context.

The web client parses canonical resources with JSON source-text access. Numeric
extension values that a JavaScript `number` cannot reproduce are carried as
clone-safe internal wrappers, and the Mirador adapter restores their original
unquoted number tokens with `JSON.rawJSON` when it calls a mutation or save
RPC. This applies to arbitrary-size integers and high-precision decimals; the
client fails closed instead of silently rounding when those platform APIs are
unavailable. The pinned Node and Chromium quality gates exercise this boundary.

The same direct validators cover standalone resources and resources nested in
an imported Manifest, so valid extensions are neither projected away nor
silently accepted outside their declared context.

## Draft and published revisions

The canonical page and its revision remain the correction source of truth. A
published row stores the complete validated bytes of one canonical revision,
along with its publisher and publication time. It is a durable access grant,
not a second editable page: editors never save into it, and publishing a newer
revision atomically replaces it.

This separation makes cache and privacy behavior explicit. Owners can continue
editing revision `N+1` while anonymous IIIF clients safely dereference
published revision `N`. The next publish compares against `N+1`, so a
concurrent correction cannot be mislabeled or accidentally exposed.
