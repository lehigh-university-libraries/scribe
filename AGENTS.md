# Scribe hardening and release criteria

This file is the evidence-based engineering tracker for Scribe. A capability is
complete only when the implementation, automated acceptance test, generated
contracts, and relevant documentation are committed and the required CI jobs
pass. UI text, emitted browser events, unit mocks, or a manually checked box are
not sufficient evidence by themselves.

## Current status

**Release status: not yet approved for production.**

The codebase is greenfield. Backward compatibility and data migrations are not
constraints for the current hardening pass; prefer a clean invariant over a
compatibility layer. Do not preserve duplicate persistence or API paths.

Run the repository contract with:

```bash
make ci
```

The required GitHub jobs independently verify contracts/lint, tests, security,
and infrastructure/documentation.

## Settled architecture decisions

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
7. Authorization is enforced at route middleware/policy boundaries and is
   side-effect free. Import and processing are explicit authorized operations.
8. Providers and segmentors are registered capabilities. Workspaces cannot
   choose an authenticated URL/audience pair.
9. Keep a modular monolith plus worker until an independently scalable or
   isolated service boundary is demonstrated.
10. Pull-request code never executes Terraform or repository scripts with cloud
    credentials. Preview and production applies use protected environments and
    immutable reviewed inputs.

## Release blockers

Every item below requires a committed automated acceptance test.

- [ ] Two workspaces can import the same external manifest and cannot read,
      mutate, index, export, or publish each other's annotations.
- [ ] Every editor, worker, public manifest, export, metric, and publication
      reads the same canonical page repository and revision.
- [ ] `GetAnnotationPage` and `SaveAnnotationPage(expected_revision)` provide
      one atomic page save and explicit revision conflicts.
- [ ] Line-to-word edits, word CRUD, split/join operations, save, reload, public
      dereference, and exports preserve the same word annotations.
- [ ] A dirty editor can receive background transcription without becoming
      falsely clean or losing local changes.
- [ ] Multi-file ingest applies the selected context and exposes idempotent
      progress, retry, resume, partial failure, and cancellation.
- [ ] Job attempts are immutable and fenced by lease token plus input revision;
      stale attempts cannot overwrite newer corrections.
- [ ] Valid IIIF v3 manifests, including array `@context` and image `Choice`,
      import successfully and emitted resources pass `libops/iiif-spec`.
- [ ] Provider endpoints are administrator-registered exact origins/audiences;
      credentials and response content are redacted from errors and logs.
- [ ] Real-browser tests cover focus/keyboard routing, geometry at nonzero
      offsets and zoom, background rebase, save/reload, and revision conflict.
- [ ] Backup/restore and job-recovery procedures have been exercised in an
      isolated environment.
- [ ] All jobs invoked by `make ci` pass from a clean checkout.

## Engineering invariants

### Canonical IIIF and persistence

- One `internal/iiif` boundary owns IDs, parse/build, extension semantics,
  raw-property preservation, and `iiif-spec` validation.
- Complete workspace-scoped pages and revisions are stored transactionally.
- Annotation/search rows and legacy export formats are rebuildable views.
- Versioned database migrations own schema changes once the greenfield schema
  stabilizes; do not replay a monolithic schema on every process startup.
- Item deletion is transactional and cleans relational, blob, Triplet, job,
  and audit state through explicit child-first application deletes and outbox
  cleanup. The schema has no foreign keys; repository transactions own
  consistency and cleanup acceptance tests.

### Editor

- A typed editor-session reducer owns base page/revision, local draft,
  history, pending remote changes, conflict, and save states.
- OpenSeadragon coordinate conversion lives in one tested geometry module.
- Edits preserve unknown IIIF page, body, target, selector, service, and extension
  properties across edits.
- One documented event bridge connects Mirador and the application shell.

### Processing and extensibility

- Ingest/reprocess/delete/publish orchestration lives in application use cases
  with transactions, idempotency, and outbox events.
- Provider and segmentor descriptors contain models, defaults,
  capabilities, limits, secret schema, and endpoint policy.
- Configuration fields are consumed by runtime behavior; unused fields are
  removed.
- Unicode is preserved and language/model filtering is explicit and testable.

### Security and operations

- API keys stay hashed, sessions stay secure, body capture stays disabled, and
  audit retention stays bounded.
- Request, upload-byte, decoded-pixel, rate, workspace, provider, and
  stream limits before expensive work.
- `/livez` reports process liveness; `/readyz` reports persistence readiness.
- Long-running Compose services are restartable with readiness-aware ordering
  and graceful termination.
- Actions, module commits, tools, downloads, and container digests are pinned.
  Renovate updates all references together.
- Protected `preview` and `production` environments are required, and GCP
  Workload Identity Federation claims are restricted to the repository,
  workflow, ref, and environment.

### Developer experience and documentation

- `.go-version`, `.nvmrc`, `.tool-versions`, Make targets, Docker images,
  and CI aligned.
- Missing Buf, sqlc, or security tools must fail with an installation command;
  generation and lint may never silently skip.
- Shell automation may invoke reviewed packaged Python tools needed by Kraken or
  Zensical, but must never embed Python with `-c`, stdin, or a heredoc. Write
  repository helpers in Go or Bash; use Node only for the frontend packages.
- `make generate-check` covers Go, Connect, TypeScript, sqlc, and OpenAPI output.
- Zensical documentation under `docs/` owns quickstart, architecture,
  extension guides, API integration, configuration, deployment, observability,
  backup, and recovery procedures.
- The README remains a short truthful entry point; detailed operational claims
  belong in tested documentation.

## Definition of done for a change

- [ ] Source and generated files are formatted and current.
- [ ] The smallest meaningful unit/contract tests pass.
- [ ] Cross-boundary behavior has an integration or browser test where drift or
      concurrency is possible.
- [ ] Authorization, tenant isolation, input cost, secret/log exposure,
      idempotency, and failure recovery were considered.
- [ ] API/configuration/architecture behavior is documented.
- [ ] `make ci` passes from a clean checkout.
- [ ] The pull request includes concrete test evidence and any residual risk.

## Documentation

Start at `docs/index.md`. In particular:

- `docs/concepts/canonical-iiif.md`
- `docs/architecture/data-flow.md`
- `docs/development/adding-provider.md`
- `docs/development/adding-rpc.md`
- `docs/operations/deployment.md`
- `docs/reference/quality-gates.md`
