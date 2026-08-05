# Architecture

Scribe is a modular Go application with an asynchronous worker and two browser
packages:

- `cmd/api`: Connect API, auth routes, and public IIIF representations
- `cmd/worker`: leased background transcription and publication work
- `cmd/browser-session`: trusted-host, five-minute browser smoke credentials
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
