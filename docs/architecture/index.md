# Architecture

Scribe is a modular Go application with an asynchronous worker and two browser
packages:

- `cmd/api`: Connect API, auth routes, and public IIIF representations
- `cmd/worker`: leased background transcription and publication work
- `cmd/ocr-matrix`: OCR image build matrix from `config/ocr.yaml`
- `cmd/secret-manager-secrets`: verified Vault-to-Secret-Manager bootstrap copy
- `internal/iiif`: IIIF IDs, parsing, validation, builders, and extensions
- `internal/store`: transactional canonical pages, revisions, jobs, and outboxes
- `internal/providerregistry`: provider and segmentor capability policy
- `internal/server`: generated Connect implementations and public IIIF gates
- `web`: application shell and routing
- `mirador-scribe`: reusable Mirador 4 OCR editor plugin

Cloud SQL for MySQL stores cloud application state; MariaDB remains the local
Compose database. Triplet mirrors explicitly published Presentation resources in
its own SQL database. The API gates source upload and Image API reads. Secret
Manager owns cloud OAuth/provider credentials; local development may use Vault.
Cloud Run hosts the API, request-driven worker, and private helpers. All durable cloud
state is outside container filesystems, and no cloud VM is deployed.

See [architecture decisions](decisions.md#production-topology-and-availability)
for the managed topology and [threat model](threat-model.md) for trust boundaries.

Prefer a new module and a narrow interface over another service. Split a
service only when independent scaling, failure isolation, or ownership has been
demonstrated and the operational cost is justified.

Use the [threat model](threat-model.md) when changing a trust boundary,
credential flow, document parser, provider integration, or deployment path.
