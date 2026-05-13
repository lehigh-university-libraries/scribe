# Frontend

Vite + TypeScript + Tailwind frontend. The shell is intentionally framework-light
TypeScript and the OCR editor is a lazy-loaded Mirador island. React, MUI,
Emotion, i18next, and OpenSeadragon stay because Mirador requires them; shadcn
components and their Radix/CVA/tailwind-merge helpers are not part of the
runtime architecture.

## Run

```bash
npm install
npm run dev
```

This expects the Go API on `http://localhost:8080` and proxies `/v1`,
`/scribe.v1.*`, `/auth`, `/logout`, `/static/uploads`, `/iiif`, and
`/healthz` during dev.

To serve the production build locally:

```bash
npm run build
npm run serve
```

The build script runs Vite with `CI=true` so it works in non-interactive CI
shells. Route modules are loaded dynamically, so the OCR editor stack is kept
out of the initial shell bundle until an editor route is opened. The editor
route has an explicit bundle budget because Mirador is intentionally lazy
loaded.

`npm run serve` uses `SCRIBE_FRONTEND_BACKEND_ORIGIN` for backend/API proxying
and `SCRIBE_FRONTEND_IIIF_ORIGIN` for `/iiif` proxying when needed. The
production server rejects malformed URL paths, sets baseline security headers,
adds immutable cache headers for built assets, and keeps request/header
timeouts bounded.
