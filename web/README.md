# Frontend

Vite + TypeScript + Tailwind frontend.

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

`npm run serve` uses `SCRIBE_FRONTEND_BACKEND_ORIGIN` for backend/API proxying
and `SCRIBE_FRONTEND_IIIF_ORIGIN` for `/iiif` proxying when needed.
