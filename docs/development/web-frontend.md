# Change the web application

The browser shell lives in `web/`; the reusable Mirador editor integration
lives in `mirador-scribe/`. Keep application API calls in the typed Connect
clients under `web/src/api`, page orchestration in `web/src/pages`, and shared
state/formatting helpers in `web/src/lib`. Do not duplicate backend business
operations or canonical IIIF mutations in the shell.

The existing Tailwind 4 PostCSS plugin handles vendor prefixing; no separate
Autoprefixer pass is needed.

The frontend routes both `/iiif/` image requests and `/presentation/` requests
to the configured Triplet origin. Image requests preserve the encoded source
identifier and reader credentials so Triplet can fetch authorized uploads from
the API. Public Presentation requests strip browser credentials.

Opening an existing item loads its saved annotations without replaying a
completed transcription job. Completed line animations apply only to a job
explicitly linked by `jobId` or observed pending/running in this editor session.
New completion events also refresh the page; reconciling a historical
completion never starts an animation.

Install exactly the reviewed lockfiles and run each package independently:

```bash
npm --prefix web ci
npm --prefix web test
npm --prefix web run build

npm --prefix mirador-scribe ci
npm --prefix mirador-scribe test
npm --prefix mirador-scribe run build
```

Use generated protobuf/Connect types rather than handwritten request or
response interfaces. When an RPC changes, run `make generate` and review both
the generated TypeScript and OpenAPI output. Keep workspace headers inside the
shared transport, preserve `AbortSignal` cancellation for replaceable reads,
and fence async responses before updating current-page state.

For UI behavior, add the smallest Vitest interaction test that proves the
rendered control and outgoing typed request. Use `make test-browser` whenever a
change touches Mirador focus, keyboard routing, OpenSeadragon geometry,
background rebase, or save/revision-conflict behavior. Finish with
`make test-frontend`; it runs both package suites, type checks, package-consumer
checks, and production bundle builds in the pinned Node container.
