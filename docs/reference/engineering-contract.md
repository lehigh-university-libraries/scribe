# Engineering contract

These are Scribe's durable architecture and engineering invariants. A change
that intentionally replaces one must update this page, the narrower concept or
operations page, and the executable acceptance contract in the same review.

## Settled architecture

1. Connect and protobuf are the application API contract. Do not add REST-only
   business operations.
2. A complete IIIF Presentation 3 AnnotationPage using the IIIF Text
   Granularity Extension is canonical OCR correction state.
3. Canonical pages have Scribe HTTP(S) IDs, workspace/item-image ownership, and
   monotonic revisions. Imported Canvas IDs are targets and provenance, not
   tenant keys.
4. hOCR, PAGE XML, ALTO XML, plain text, metrics, search rows, and public
   resources are derived from a committed canonical revision.
5. Structural annotation mutations and model-driven processing live behind
   backend operations reusable by the bundled app and external editors.
6. Browser-only state is limited to the local draft, selection, viewport,
   history, and conflict/rebase presentation.
7. Authorization is enforced at route middleware and policy boundaries and is
   side-effect free. Import and processing are explicit authorized operations.
8. Providers and segmentors are registered capabilities. Workspaces cannot
   choose an authenticated URL or audience pair.
9. Keep a modular monolith plus worker until an independently scalable or
   isolated service boundary is demonstrated.
10. Pull-request code never executes Terraform or repository scripts with cloud
    credentials. Preview and production applies use protected environments and
    immutable reviewed inputs.

## Canonical IIIF and persistence

- One `internal/iiif` boundary owns IDs, parsing/building, extension semantics,
  raw-property preservation, and `iiif-spec` validation.
- Complete workspace-scoped pages and revisions are stored transactionally.
- Annotation/search rows and legacy export formats are rebuildable views.
- Versioned database migrations own schema changes once the greenfield schema
  stabilizes; processes do not replay a monolithic schema at startup.
- Item deletion is transactional and cleans relational, blob, Triplet, job,
  audit, and outbox state through explicit child-first application deletes.
  The schema has no foreign keys, so repository transactions own consistency
  and cleanup acceptance tests.
- Webhook subscription creation, deletion, and event delivery expansion
  serialize on the owning workspace row. Subscription and event deletion are
  child-first, and the recovery audit independently detects missing delivery
  parents or a parent workspace mismatch. Do not add a one-off database
  relationship mechanism that bypasses this repository-wide lifecycle model.

## Editor

- A typed editor-session reducer owns the base page/revision, local draft,
  history, pending remote changes, conflict, and save states.
- OpenSeadragon coordinate conversion lives in one tested geometry module.
- Edits preserve unknown IIIF page, body, target, selector, service, and
  extension properties.
- One documented event bridge connects Mirador and the application shell.

## Processing and extensibility

- Ingest, reprocess, delete, and publish orchestration lives in application use
  cases with transactions, idempotency, and outbox events.
- Provider and segmentor descriptors contain models, defaults, capabilities,
  limits, secret schema, and endpoint policy.
- Configuration fields are consumed by runtime behavior; unused fields are
  removed.
- Unicode is preserved and language/model filtering is explicit and testable.

## Security and operations

- API keys stay hashed, sessions stay secure, body capture stays disabled, and
  audit retention stays bounded.
- Request, upload-byte, decoded-pixel, rate, workspace, provider, and stream
  limits apply before expensive work.
- `/livez` reports process liveness; `/readyz` reports persistence readiness.
- Long-running Compose services are restartable with readiness-aware ordering
  and graceful termination.
- Container-Optimized OS (COS) is the only supported host for every
  Scribe-managed GCP VM, including previews and production. Host-executed
  lifecycle scripts use its shipped shell and jq feature set; portability
  layers for other VM operating systems are out of scope.
- Actions, module commits, tools, downloads, and container digests are pinned;
  Renovate updates related references together.
- Protected `preview` and `production` environments are required. GCP Workload
  Identity Federation claims are restricted to the repository, workflow, ref,
  and environment.
- Preview teardown evidence succeeds only after both Terraform and its exact
  Vault namespace are absent. Ordinary destroy requires readable current state;
  protected recovery may accept an already absent exact preview workspace only
  after authoritative workspace inventory and remains fail closed otherwise.

## Developer experience and documentation

- `.go-version`, `.nvmrc`, `.tool-versions`, Make targets, Docker images, and CI
  remain aligned.
- Missing Buf, sqlc, or security tools fail with an installation command;
  generation and lint never silently skip.
- Shell automation may invoke reviewed packaged Python tools required by Kraken
  or Zensical, but does not embed Python with `-c`, standard input, or a
  heredoc. Repository helpers are Go or Bash; Node is limited to frontend
  packages.
- Do not add or expand lifecycle state machines in Bash. New stateful
  deployment orchestration belongs in typed Go; shell entrypoints for those
  components remain thin binary launchers without lifecycle or policy logic.
- `make generate-check` covers Go, Connect, TypeScript, sqlc, and OpenAPI
  output.
- Zensical source under `docs/` owns quickstart, architecture, extension, API,
  configuration, deployment, observability, backup, and recovery guidance.
- The README and `AGENTS.md` remain concise entry points. Detailed and durable
  claims belong in tested, published documentation.

Use the [release criteria](release-criteria.md) to decide whether implementation
evidence is complete.
