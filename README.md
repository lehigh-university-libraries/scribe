# hOCRedit

hOCRedit is a web-based OCR correction tool. Upload images or point it at a IIIF manifest, run OCR, then fix the results visually in a Mirador-powered annotation editor. All data is stored per-user and the API is defined end-to-end in protobuf with Connect RPC.

The application now runs as a single Go API server on port `8080`. That server
hosts Connect RPC, the legacy annotation REST compatibility routes used by
Mirador, and the static web app.

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

After OCR, click **Edit** on any item to open the Mirador annotation editor where you can correct word and line bounding boxes.

## Architecture

```
cmd/api/            Single Go binary (Connect RPC + annotation/IIIF REST + web)
internal/
  server/           Connect handlers, annotation routes, crosswalk routes
  store/            MariaDB access via sqlc
proto/              Protobuf definitions (Buf managed)
web/src/
  main.ts           Router (~10 LOC)
  api/              Connect client wrappers (items, processing, transport)
  pages/            home.ts (landing page), editor.ts (Mirador editor)
  lib/              Pure utilities
sqlc/               SQL queries + generated Go code
```

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
| `VITE_ANNOTATION_API_BASE` | `http://localhost:8080` | Annotation API base for Mirador adapter |

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
