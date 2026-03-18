# Scribe

![Scribe example workflow](docs/assets/example.gif)

Scribe is a web-based OCR correction tool. Upload images or point it at a IIIF manifest, run OCR, then fix the results visually in an image-aligned text editor. All data is stored per-user and the API is defined end-to-end in protobuf with Connect RPC.

The application now runs as a single Go API server on port `8080`. That server
hosts Connect RPC, annotation and IIIF HTTP routes, and the static web app.

## Direction

Scribe is standardizing on IIIF Presentation 3 `AnnotationPage` JSON as the
canonical persisted OCR correction model, using the IIIF Text Granularity
Extension for page/block/paragraph/line/word/glyph structure:
https://iiif.io/api/extension/text-granularity/

That means:

- IIIF is the canonical saved correction state
- hOCR, PageXML, ALTO, and plain text are export/import formats
- editor-specific UI state is transient and not the canonical storage model
- revision metadata such as `updated_by`, `updated_at`, and `revision` is stored
  adjacent to the canonical IIIF payload
- the API exposes annotation and text-editing actions that editor plugins can
  call directly rather than reimplementing split/join/transcription logic in
  the browser
- the same backend also serves a standalone web app for item ingestion,
  management, OCR generation, and QA editing

The editor is designed as a custom text-first OCR correction workflow built on
top of canonical IIIF annotation state.

## Quick start

```bash
cp sample.env .env
bash generate-secrets.sh
docker compose up --build
```

| Service | URL |
|---------|-----|
| Web app | http://localhost |
| API + Annotation API | http://localhost |
| IIIF image server (Cantaloupe) | http://localhost/cantaloupe |

## Creating items

The landing page offers four ways to create an item:

| Tab | What happens |
|-----|-------------|
| **Image URL** | OCR runs immediately; editor opens automatically |
| **Single upload** | Upload one image; OCR runs; editor opens automatically |
| **Multi-upload** | Upload several images into one item; appears in the table for editing |
| **IIIF Manifest** | Fetches all canvases from the manifest; appears in the table |

After OCR, click **Edit** on any item to open the page editor where you can
correct line and word text against the image.

## Architecture

```
cmd/api/            Single Go binary (Connect RPC + annotation/IIIF REST + web)
internal/
  server/           Connect handlers, canonical AnnotationPage routes, crosswalk routes
  store/            MariaDB access via sqlc
proto/              Protobuf definitions (Buf managed)
web/src/
  main.ts           Router (~10 LOC)
  api/              Connect client wrappers (items, processing, transport)
  pages/            home.ts (landing page), editor.ts (editor shell)
  lib/              Pure utilities
mirador-scribe/
  src/              Repo-owned Mirador v4 OCR editor plugin + annotation adapter
sqlc/               SQL queries + generated Go code
```

Canonical data model:

- Persist one IIIF Presentation 3 `AnnotationPage` per page/canvas
- Use IIIF Text Granularity Extension semantics for line/word/glyph annotations
- Preserve the finest source granularity available during import, such as word
  boxes from hOCR `ocrx_word`
- Store revision and workflow metadata adjacent to the canonical annotation JSON
- Export repository-facing formats such as hOCR/PageXML/ALTO from that canonical state

API/editor contract:

- the backend is the canonical source for annotation mutations such as line
  splitting, line joining, word splitting, word joining, and retranscription
- editor plugins should call those API operations and then reload or reconcile
  the returned IIIF annotations
- this keeps plugin implementations thinner and makes the same API usable from
  Mirador or other IIIF-capable editors

## Build and test

```bash
# Backend
make lint
make test

# Regenerate proto stubs and SQL
make proto
make sqlc
make generate

# Frontend (from web/)
npm install
npm run build
```

## Key environment variables

| Variable | Default | Description |
|----------|---------|-------------|
| `ANNOTATION_API_BASE` | `/` | Public base URL used when generating annotation item/page IDs; path values are expanded against the incoming request host |
| `CANTALOUPE_IIIF_BASE` | `/cantaloupe/iiif/2` | IIIF image base URL used in manifests; path values are expanded against the incoming request host |
| `SCRIBE_WEBHOOK_URLS` | empty | Comma-separated webhook endpoints that receive all emitted Scribe CloudEvents |
| `VITE_ANNOTATION_API_BASE` | `/` | Annotation API base for viewer/editor integration; runtime HTML injection takes precedence when available |

## IIIF endpoints

```
GET  /v1/item-images/{id}/manifest        IIIF Presentation v3 manifest
GET  /v1/item-images/{id}/hocr            Current persisted hOCR document
GET  /v1/item-images/{id}/annotations     IIIF annotation page bootstrap/export
GET  /v1/events                           Server-sent event stream for job + annotation lifecycle events
```

The application API is proto-first. New API operations should be defined in
protobuf and consumed through generated Connect clients.

Annotation and OCR operations are exposed on these Connect services:

```
POST /scribe.v1.ItemService/*
POST /scribe.v1.ImageProcessingService/*
POST /scribe.v1.ContextService/*
POST /scribe.v1.AnnotationService/*
```

Plain HTTP routes should exist only when there is a concrete resource-URL
reason not to use RPC. The `GET /v1/item-images/{id}/manifest`,
`GET /v1/item-images/{id}/annotations`, and `GET /v1/item-images/{id}/hocr`
routes are examples of that exception: they expose dereferenceable IIIF/OCR
documents that external viewers and IIIF clients fetch directly.

## Events and webhooks

Scribe emits a small CloudEvents-style event set from the backend. Clients can
consume those events either through `GET /v1/events` over SSE or by configuring
`SCRIBE_WEBHOOK_URLS` to fan out each event as `application/cloudevents+json`.

Current event types:

- `dev.scribe.transcription.task.started`
- `dev.scribe.transcription.task.completed`
- `dev.scribe.transcription.completed`
- `dev.scribe.transcription.failed`
- `dev.scribe.annotations.created`
- `dev.scribe.annotations.published`

Use `transcription.task.completed` to drive per-line progress in the UI. Use
`annotations.created` and `annotations.published` for external integrations such
as Islandora. Save does not publish: `annotations.published` is emitted only
after the explicit `POST /scribe.v1.AnnotationService/PublishItemImageEdits`
action.

Editor-oriented annotation operations are exposed on `AnnotationService` so
plugins can delegate structural OCR edits to the backend:

```
POST /scribe.v1.AnnotationService/SplitAnnotationIntoWords
POST /scribe.v1.AnnotationService/SplitAnnotationIntoTwoLines
POST /scribe.v1.AnnotationService/MergeAnnotationsIntoLine
POST /scribe.v1.AnnotationService/MergeWordsIntoLineAnnotation
POST /scribe.v1.AnnotationService/TranscribeAnnotation
POST /scribe.v1.AnnotationService/TranscribeAnnotationPage
```

## Contexts and metrics

Contexts bundle the OCR/transcription settings used to process or enrich an
image. A context can include:

- a segmentation model
- a transcription provider/model
- additional context-selection metadata used to infer the best context from the
  supplied image or related metadata

Scribe seeds these system contexts on startup:

- `Default`
  Runs both `tesseract` segmentation and the in-repo `scribe` custom segmentor,
  then keeps whichever finds more words.
- `Tesseract OCR`
  Uses Tesseract segmentation and Tesseract transcription directly.
- `Scribe Custom`
  Uses the custom segmentor, crops by detected line, sends each line to the
  configured LLM provider, and assembles the result back into line-level OCR.

For images uploaded or supplied without existing hOCR, the default system flow
is:

1. Run the Tesseract segmentor and the Scribe custom segmentor.
2. Compare the number of detected words.
3. Use the winning segmentation path for OCR generation.
4. If Tesseract wins, keep Tesseract's text directly.
5. If the Scribe segmentor wins, run the line-crop LLM transcription path.

The backend exposes context resolution so ingestion and editor operations can
choose a context explicitly or let the server pick one when enough information
can be inferred. OCR runs are stored with the resolved context so context-level
metrics aggregate against the context that was actually used.

Scribe records edit metrics to evaluate context quality. The primary metric
today is document-level Levenshtein distance between:

- the plain-text document produced by the app originally
- the plain-text document represented by the user-corrected final result

This gives a simple measure of how much correction a context required.
Segmentation quality metrics are planned but still TBD.

## Product Model

Scribe supports two primary workflows:

1. Low/no-touch OCR generation
   - ingest images or manifests
   - generate canonical IIIF annotation pages
   - export hOCR/PageXML/ALTO/plain text
   - optionally publish results back to a parent repository system

2. Human QA correction
   - load canonical IIIF annotation pages in the editor
   - edit text and geometry with a text-first workflow
   - save new revisions
   - export or publish corrected results
