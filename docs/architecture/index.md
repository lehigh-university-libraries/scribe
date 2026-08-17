# Architecture

Scribe is a modular Go application with an asynchronous worker and two browser
packages:

- `cmd/api`: Connect API, auth routes, and public IIIF representations
- `cmd/worker`: leased background transcription and publication work
- `cmd/browser-session`: trusted-host, fixed 50-minute browser-readiness fallback
  credentials
- `cmd/cloud-run-readiness`: validated Cloud Run readiness command boundary
- `cmd/production-browser-readiness`: validated production session-transport
  command and restricted remote-session boundary
- `internal/cloudrunreadiness`: typed execution fencing, launch recovery,
  terminal settlement, and bounded diagnostics lifecycle
- `internal/productionbrowserreadiness`: typed Secret Manager, Cloud Run job,
  IAP transfer, and cleanup lifecycle for production browser readiness
- `internal/iiif`: IIIF IDs, parsing, validation, builders, and extensions
- `internal/store`: transactional canonical pages, revisions, jobs, and outboxes
- `internal/providerregistry`: provider and segmentor capability policy
- `internal/server`: generated Connect implementations and public IIIF gates
- `web`: application shell and routing
- `mirador-scribe`: reusable Mirador 4 OCR editor plugin

MariaDB stores application state. Triplet mirrors explicitly published
Presentation resources but does not compete with the canonical page repository.
Image API traffic passes through the application publication gate before the
private image helper is invoked. Vault owns provider and OAuth secrets. Cloud
Run hosts private OCR/image helpers; authenticated server-side callers use
registered exact audiences.

Protected preview and production delivery share one deployed Playwright product
scenario. Preview uses its isolated anonymous principal; production reaches the
exact Compose VM over port-22-only IAP, mints a reserved-user session without
Google OAuth or an operator cookie, and transfers it through one temporary
Secret Manager version to an isolated Cloud Run job. The runner validates and
consumes the state before its first request, retains a revocation-only cookie
independently of Chromium, and does not accept logout until the original cookie
is rejected by a protected API. Terraform pins the idle job to inert secret
version 1; transport temporarily attests an exact numeric credential reference,
restores version 1, and destroys and verifies the known credential version on
every handled exit. The controller and its copied remote-session helper are two
modes of one statically built Go binary; shell entrypoints do not own session
or cleanup state.
A failed reconciliation remains a failed deployment.
Browser executions reach their natural terminal state so application cleanup
can run; a later protected apply fences an execution left by hard termination
before rollout. The fixed 50-minute lifetime is the final revocation fallback.

The current production backend is explicitly a non-HA, single-zone Cloud
Compose deployment. Its per-layer recovery-artifact evidence, lack of bounded
coordinated RPO or service RTO, and the gates that must precede a Cloud
SQL/Cloud Run migration are recorded in
[architecture decisions](decisions.md#production-topology-and-availability).

Prefer a new module and a narrow interface over another service. Split a
service only when independent scaling, failure isolation, or ownership has been
demonstrated and the operational cost is justified.

Use the [threat model](threat-model.md) when changing a trust boundary,
credential flow, document parser, provider integration, or deployment path.
