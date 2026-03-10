# Scribe

Scribe is a web-based OCR correction tool. Upload images or point it at a IIIF manifest, run OCR, then fix the results visually in an image-aligned text editor. All data is stored per-user and the API is defined end-to-end in protobuf with Connect RPC.

The application now runs as a single Go API server on port `8080`. That server
hosts Connect RPC, annotation and IIIF HTTP routes, and the static web app.

## Direction

Scribe is standardizing on IIIF Presentation 3 `AnnotationPage` JSON as the
canonical persisted OCR correction model, using the IIIF Text Granularity
Extension for line/word/glyph structure.

That means:

- IIIF is the canonical saved correction state
- hOCR, PageXML, ALTO, and plain text are export/import formats
- editor-specific UI state is transient and not the canonical storage model
- revision metadata such as `updated_by`, `updated_at`, and `revision` is stored
  adjacent to the canonical IIIF payload

The editor is designed as a custom text-first OCR correction workflow built on
top of canonical IIIF annotation state.

## Quick start

```bash
docker compose up --build
```

| Service | URL |
|---------|-----|
| Web app | http://localhost:8080 |
| API + Annotation API | http://localhost:8080 |
| IIIF image server (Cantaloupe) | http://localhost:8182 |

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
- Store revision and workflow metadata adjacent to the canonical annotation JSON
- Export repository-facing formats such as hOCR/PageXML/ALTO from that canonical state

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
| `ANNOTATION_API_BASE` | `http://localhost:8080` | Public base URL used when generating annotation item/page IDs |
| `CANTALOUPE_IIIF_BASE` | `http://localhost:8182/iiif/2` | IIIF image base URL used in manifests |
| `VITE_ANNOTATION_API_BASE` | `http://localhost:8080` | Annotation API base for viewer/editor integration |

## IIIF endpoints

```
GET  /v1/item-images/{id}/manifest        IIIF Presentation v3 manifest
GET  /v1/ocr/runs/{session_id}/hocr       Raw hOCR for a run
GET  /v1/ocr/runs/{session_id}/annotations  IIIF annotation page
```

Crosswalk routes convert stored IIIF annotations to other OCR formats:

```
POST /v1/crosswalk/plain-text
POST /v1/crosswalk/hocr
POST /v1/crosswalk/page-xml
POST /v1/crosswalk/alto-xml
```

Annotation persistence (used by Mirador):

```
GET    /v1/annotations/3/search?canvasUri=<id>
POST   /v1/annotations/3/create
POST   /v1/annotations/3/update
DELETE /v1/annotations/3/delete?uri=<id>
GET    /v1/annotations/3/item/{id}
POST   /v1/annotations/3/enrich
```

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
