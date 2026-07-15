# Hardening Plan

This file defines the execution plan to harden this codebase for reliability, maintainability, and production readiness.

## Goals

1. Standardize on Buf/Connect end-to-end.
2. Remove duplicated API paths and logic drift.
3. Build a text-first editor around IIIF Presentation 3 annotations and the IIIF Text Granularity Extension.
4. Add meaningful automated test coverage for critical flows.
5. Improve operational safety (error handling, observability, persistence correctness).
6. Keep the backend usable both as a standalone web application and as an API that external IIIF editors and plugins can build on.

## Non-Goals

1. Broad compatibility requirements unrelated to the current product direction.
2. Large feature expansion unrelated to OCR/editor reliability.
3. Replatforming to a different backend language or framework.

## Phase 0: Stabilization Guardrails ✓ COMPLETE

1. ✓ Freeze API surface: no new REST endpoints; all new operations go through Connect.
2. ✓ CI gates: `fmt`, `lint`, `generate`, `test` required on PR (`.github/workflows/lint-test.yaml`).
3. ✓ PR template: `.github/pull_request_template.md` includes a definition-of-done checklist.

Exit criteria:
1. ✓ CI fails on formatting/lint/test regressions.
2. ✓ PR template includes test evidence for OCR editor flows.

## Phase 1: API Standardization (Connect Only) ✓ COMPLETE

All operations are now served through generated Connect handlers. No REST-only routes remain
for business logic. The following were migrated from ad-hoc REST handlers:

- `SplitLineIntoWords` — split a line annotation into proportionally-spaced word annotations
- `SplitLineIntoTwoLines` — split a line annotation at a word boundary into two lines
- `JoinLines` — merge multiple line annotations into one (union bbox, joined text)
- `JoinWordsIntoLine` — merge word annotations into a line annotation
- `CrosswalkToPlainText` — export AnnotationPage as plain text
- `CrosswalkToHOCR` — export AnnotationPage as hOCR XML
- `CrosswalkToPageXML` — export AnnotationPage as PAGE XML
- `CrosswalkToALTOXML` — export AnnotationPage as ALTO XML
- `ReprocessItemImage` — reprocess an item image with a given context (moved to `ImageProcessingService`)

Naming conventions:
- Split/join operations use `SplitLine*` / `JoinLines` / `JoinWordsIntoLine` in proto and
  `splitLine*` / `joinLines` / `joinWordsIntoLine` in the TypeScript adapter.
- Crosswalk operations use `Crosswalk*` in proto.

Exit criteria:
1. ✓ Web app uses generated Connect client for all core OCR/editor operations.
2. ✓ No duplicated business logic between REST and Connect handlers.
3. ❌ OpenAPI generation from proto in `make generate` (buf.gen.yaml not yet configured).

## Canonical Model Decision

Persist OCR correction state as IIIF Presentation 3 `AnnotationPage` JSON using
the IIIF Text Granularity Extension.

Import should preserve the finest source granularity available, such as hOCR
word boxes, rather than collapsing everything to line-level annotations.

The app may store adjacent workflow metadata such as:
1. `revision`
2. `updated_by`
3. `updated_at`
4. `context_id`
5. transcription provider/model
6. source-system sync metadata

Non-goals of persistence:
1. editor-specific UI state as canonical storage
2. custom OCR document schema when IIIF AnnotationPage is sufficient
3. per-line or per-word revert history inside the editor model

## API and Product Decision

Scribe is both:
1. a standalone web app for ingesting, managing, processing, and QA-editing items
2. an API surface that editor plugins can call to load, save, transform, enrich,
   export, and publish canonical IIIF annotations

Architectural preference:
1. keep canonical annotation mutations on the backend when they are structural or
   model-driven
2. let clients call API operations for split/join/transcribe/crosswalk behavior
   instead of reimplementing those transforms locally
3. keep browser-only state limited to transient editor concerns such as
   selection, viewport, and undo/redo

## Auth and Access TODO

This is intentionally deferred so product work can continue, but the direction
should be treated as settled unless requirements change.

### Access architecture decision

1. Authorization should be enforced at the route/middleware layer, not scattered
   through individual handlers.
2. Handlers should assume access has already been granted and focus on product
   behavior, validation, and persistence.
3. Route-level middleware should resolve the authenticated principal, load the
   relevant resource ownership/sharing context, and grant or deny before handler
   execution.
4. Shared authorization helpers may exist behind middleware/policy packages, but
   handler methods should not grow ad-hoc ownership checks.

### Planned identity and RBAC model

1. Users authenticate with Google OAuth.
2. Use `goth` as the Go OAuth library.
3. App admins control who can log in.
4. Admission policy should support:
   - allow-listing domains such as `foo.edu`
   - wildcard allow rules such as `*.edu`
   - deny-listing domains such as `gmail.com` or `bar.edu`
5. Users can create organizations.
6. Organizations have members with roles:
   - `admin`: manage org members and org-level administration
   - `write`: edit everything in the org
   - `create`: create new items
   - `read`: view-only access
7. Items belong to a user or organization and may later support explicit
   per-item grants.
8. Individual items can be shared globally as read-only.

### Future TODOs

1. Add auth middleware that injects principal and access grants into request
   context before Connect/HTTP handlers run.
2. Add org/user schema and policy evaluation layer.
3. Add item visibility rules including org ownership and public read-only
   sharing.
4. Add admin UI for login domain policy.
5. Add API key support later, after OAuth and RBAC are stable.

## Contexts and Metrics Decision

Contexts are first-class backend resources. A context bundles:
1. a segmentation model
2. a transcription provider/model
3. context-selection metadata used to infer or enrich which context should be
   applied to a supplied image

The current metrics model is intentionally simple:
1. compare the app's original plain-text output to the final corrected
   plain-text output
2. measure document-level Levenshtein distance between those two texts
3. use that as the primary correction-effort signal for context quality

Segmentation-quality measurement is still TBD and should be designed separately
from the document-text correction metric.

## Phase 2: Frontend Editor Decomposition ✓ COMPLETE

Split `web/src/main.ts` (314 LOC monolith) into focused modules. Landing page
redesigned to show a table of the current user's items and support four item
creation flows.

### Completed modules

| File | Purpose |
|------|---------|
| `web/src/lib/util.ts` | Pure utilities: `uint64ToString`, `readFileBytes`, `escHtml` |
| `web/src/api/transport.ts` | Singleton Connect transport |
| `web/src/api/annotations.ts` | AnnotationService wrappers: all CRUD + split/join/crosswalk |
| `web/src/api/items.ts` | ItemService wrappers: list, get, create (manifest), upload (multi), delete |
| `web/src/api/processing.ts` | ImageProcessingService wrappers: processImageURL, processImageUpload, processHOCR, getOCRRun, saveOCREdits, reprocessItemImage |
| `web/src/api/context.ts` | ContextService wrappers: full CRUD + selection rules + resolve |
| `web/src/pages/editor.ts` | Mirador viewer + annotation editor |
| `web/src/pages/home.ts` | Landing page: items table + 4-tab creation UI |
| `web/src/main.ts` | Router only (~10 LOC) |

### Home page creation flows

1. **Image URL** — calls `processImageURL` → navigates to `/editor?itemImageId=X`
2. **Single upload** — calls `processImageUpload` → navigates to editor; auto-submits on file select
3. **Multi-upload** — calls `uploadItemImages` sequentially (chains `itemId`) → refreshes table
4. **IIIF Manifest** — calls `createItem(sourceType="manifest")` → refreshes table

### Backend crosswalk tests (also completed this phase)

Golden-file tests added for all four crosswalk format routes in
`internal/server/annotation_crosswalk_test.go` with fixtures under
`internal/server/testdata/crosswalk/`. Run with:

```bash
go test ./internal/server/ -run TestCrosswalk
# regenerate golden files:
go test ./internal/server/ -run TestCrosswalk -update
```

Exit criteria:
1. ✓ No single frontend file > 500 LOC in editor core path.
2. Existing editor workflow works unchanged by manual QA script.

## Phase 3: Editor Replacement (New Current Focus)

Build a custom text-first editor that uses IIIF manifests/canvases for viewport
geometry but edits canonical IIIF AnnotationPage state directly.

Scope:
1. ✓ Build the main editor flow around canonical IIIF AnnotationPage editing.
2. ✓ Build a custom editor panel optimized for OCR correction:
   - ✓ edit line text
   - ✓ edit word text
   - ✓ add/delete word
   - ✓ split line into words
   - ✓ merge words into line
   - ✓ split line into two lines
   - ✓ merge adjacent lines
   - ✓ adjust bounding boxes (corner handles on selected annotation; drag converts screen→image coords via OSD)
3. ✓ Keep undo/redo in frontend memory; save full page AnnotationPage snapshots.
4. ✓ Use backend API operations for structural OCR edits and retranscription so
   plugins can stay thin and other IIIF editors can reuse the same behavior.
5. Continue exporting hOCR/PageXML/ALTO/plain text from canonical IIIF annotations.
6. ✓ Keep the Mirador v4 plugin in-repo as a standalone package (`mirador-scribe/`).

Suggested backend/API work:
1. ❌ Add first-class save/load endpoints for page-level canonical AnnotationPage revisions.
2. ❌ Store revision metadata adjacent to canonical annotation JSON.
3. ✓ Helper endpoints for split/join/transcribe/crosswalk are editor-agnostic Connect RPCs.
4. ❌ Support publish/sync workflows back to source systems using exported formats or public URLs.

Exit criteria:
1. Editing a page does not depend on editor-specific canonical metadata.
2. Save/reload correctness is based on canonical IIIF AnnotationPage state.
3. Text editing workflows are optimized for OCR correction.

## Phase 4: Test Coverage for Critical Paths ✓ MOSTLY COMPLETE

1. Backend tests:
   - ✓ Crosswalk golden-file tests (`annotation_crosswalk_test.go`)
   - ✓ Manifest ingestion integration tests (`manifest_ingest_test.go`)
   - ✓ Levenshtein metrics unit test (`metrics/metrics_test.go`)
   - ✓ Connect handler tests for split/join/save/transcribe
   - ✓ AnnotationPage revision save semantics
   - ✓ hOCR parsing/building regressions, including word persistence and Kraken baseline fallback
   - ✓ Context resolution, context-driven enrichment behavior, and async transcription metrics
2. Frontend tests:
   - ✓ Vitest infrastructure exists in `web/`
   - ✓ AnnotationPage load/edit/save round-trip
   - ✓ Keyboard routing (`Tab`, `Shift+Tab`, `Cmd/Ctrl+Z`, `Cmd+Delete`)
   - ✓ Editor actions (`split`, `expand words`, `merge`, `save`, `reload`)
3. End-to-end smoke tests (containerized):
   - ✓ process image -> editor -> expand words -> edit -> save -> reload -> changes persist
   - ✓ import IIIF manifest with `seeAlso` hOCR -> edit line text -> save -> export hOCR persists
   - ✓ low-touch backend smoke flow for ingestion, processing, persistence, and export

Exit criteria:
1. Test suite covers the two highest-risk regressions:
   - ✓ word persistence after save/reload
   - ✓ editor focus/keyboard correctness.
2. ✓ CI executes tests in reproducible containerized environment.

## Phase 5: Operational Hardening

1. Structured logging consistency:
   - include `session_id`, `provider`, `model`, operation, latency.
2. Error taxonomy:
   - normalize invalid argument vs internal errors across handlers.
3. Persistence safety:
   - explicit source-of-truth rules for `original.hocr` and `corrected.hocr`.
   - startup integrity check for cache/session directories.
4. Config hardening:
   - document required env vars with defaults and validation behavior.
5. Provider call audit safety:
   - prompt/request/response body capture is disabled by default.
   - provider call audit rows are retained for 30 days by default and purged by API/worker loops.

Exit criteria:
1. Logs are queryable by session/model/provider for debugging.
2. Failures are diagnosable without code inspection.

## Work Order (Recommended)

1. ✓ Phase 1 API standardization.
2. ✓ Phase 2 frontend decomposition.
3. Phase 3 editor replacement (in progress).
4. Phase 4 test expansion.
5. Phase 5 operational hardening.

## Tracking

Create one checklist issue per phase. Do not start a next phase until current phase exit criteria are met.
