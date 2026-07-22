# Frontend

Vite + TypeScript + Tailwind frontend. The shell is intentionally framework-light
TypeScript and the OCR editor is a lazy-loaded Mirador island. React, MUI,
Emotion, i18next, and OpenSeadragon stay because Mirador requires them; shadcn
components and their Radix/CVA/tailwind-merge helpers are not part of the
runtime architecture.

## Run

```bash
npm ci
npm run dev
```

With the Compose stack running, the dev proxy targets its edge at
`http://localhost`. To run the Go API directly on another origin (for example,
`http://localhost:8080`), set `SCRIBE_DEV_BACKEND_ORIGIN`. The proxy covers
`/v1`, `/scribe.v1.*`, `/auth`, `/logout`, `/static/uploads`, `/iiif`,
`/presentation`, and `/healthz`.

To serve the production build locally:

```bash
npm run build
npm run serve
```

Run fast DOM/unit checks with `npm test`. From the repository root, use
`make test-browser` for the real Chromium suite. It uses a digest-pinned
Playwright container and exercises the production editor shell, OpenSeadragon
geometry, session reducer, and annotation adapter without requiring a host
browser installation.

The build script runs Vite with `CI=true` so it works in non-interactive CI
shells. Route modules are loaded dynamically, so the OCR editor stack is kept
out of the initial shell bundle until an editor route is opened. The editor
route has an explicit bundle budget because Mirador is intentionally lazy
loaded.

`npm run serve` uses `SCRIBE_FRONTEND_BACKEND_ORIGIN` for backend, API, and
Image API proxying. Image requests always pass through the backend publication
gate; the frontend cannot be configured to bypass it for a private image
service. The production server rejects malformed URL paths, sets baseline
security headers, adds immutable cache headers for built assets, and keeps
request/header timeouts bounded.
