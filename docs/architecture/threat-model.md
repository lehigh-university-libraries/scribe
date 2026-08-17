# Threat model

This model covers the bundled browser application, Mirador plugin, Connect API,
worker, MariaDB, Vault, Triplet publication mirror, private image/OCR helpers,
external IIIF resources, model providers, and GitHub deployment workflows.
Revisit it whenever a feature moves data or authority across one of those
boundaries.

## Assets and actors

Protected assets are workspace membership, canonical AnnotationPages and their
revision integrity, unpublished documents and uploads, provider credentials,
OAuth and API credentials, one-time production browser sessions, audit records,
deployment identities, backups, and the integrity of derived exports and public
resources.

Expected actors are interactive workspace members, scoped API clients,
registered external JWT issuers, operators, and anonymous readers of explicitly
published IIIF. Treat uploaded bytes, IIIF/hOCR/JSON/XML, browser input,
provider output, webhook destinations, forwarded headers, and pull-request
content as hostile. A provider is trusted to process only the document data an
authorized operation sends to it; it is not trusted with another workspace's
credential or content.

## Trust boundaries and controls

| Boundary | Principal threats | Enforced controls |
| --- | --- | --- |
| Browser or external editor → API | IDOR, CSRF, credential theft, oversized or compressed requests | Route-level policy, workspace/resource lookup, secure same-site session cookies, fail-closed origin checks, scoped API keys/JWTs, per-procedure uncompressed body limits, rate and concurrent-read admission |
| Workspace → workspace | Shared Canvas/provider identifiers becoming tenant keys; cross-tenant jobs, exports, search, metrics, or publication | Scribe-owned workspace/item-image identity, transactionally validated application ownership, tenant-scoped queries and cursors, canonical repository acceptance tests |
| Untrusted document → parser/model | SSRF, decompression/XML/JSON amplification, hostile geometry, excessive fan-out or paid work | Bounded fetches with special-network denial and redirect checks, manifest/hOCR/page byte and structure limits, decoded-pixel and coordinate validation, annotation/Canvas caps, storage quotas, hierarchical processing admission |
| API/worker → provider or helper | Endpoint substitution, credential forwarding, response/log disclosure, retry storms | Administrator-registered exact origins and audiences, fixed vendor adapters, redirect denial, per-workspace Vault credentials, response/deadline/retry limits, categorical redacted errors and metadata-only audits |
| Concurrent editor/worker → canonical page | Lost corrections, stale model overwrite, false-clean drafts | Atomic `expected_revision` saves, typed browser session state, explicit rebase/conflict handling, immutable attempts fenced by lease token and input revision |
| Canonical state → derived/public data | Divergent exports or stale/accidental publication | Revision-bound derivation, one canonical repository, explicit publication snapshots, libops IIIF validation, public-image gate, rebuildable indexes/mirrors |
| Process → MariaDB/Vault/blob/Triplet | Partial cross-system writes, leaked or orphaned secrets/blobs | Child-first application transactions without foreign keys, idempotency keys, durable outboxes/cleanup states, provider-secret reconciliation, bounded retry and retention |
| GitHub PR → cloud deployment | Unreviewed code using cloud credentials, mutable artifacts, identity confusion | Uncredentialed PR checks, protected preview/production environments, exact-SHA inputs and digest promotion, claim-restricted WIF, pinned actions/tools/images/modules, deployment attestation |
| Protected deploy/VM → isolated production browser job | Session leakage or reuse, wrong VM/job/secret binding, orphaned credentials | Exact-instance port-22 IAP, reserved identity validation, 50-minute session bound, mode-`0600` one-time materialization, exact secret-version and digest attestation, no-data runner IAM, logout plus protected-API 401 proof, inert-version restoration and exact-version destruction, next-apply execution fence |
| Failure or operator error → durable data | Undetected loss, unrecoverable jobs, backup compromise | Versioned migrations, isolated restore drills, immutable job attempts and recovery, independent least-privilege backup verification, documented recovery procedures |

Public IIIF endpoints intentionally allow cross-origin anonymous reads only
after publication. Authenticated application endpoints do not inherit that
policy. Provider calls intentionally disclose the selected image region and
prompt to the selected provider; the UI and API must make that choice explicit.

## Availability and abuse assumptions

Limits reduce amplification but do not make an authenticated tenant free to
operate. Workspace storage/job limits, global and per-tenant work pools,
provider concurrency, SSE deadlines, bounded lists, and platform instance
limits must be sized together. Health probes bypass expensive-work queues, and
readiness fails when required persistence is unavailable.

Scribe assumes the host/container platform, MariaDB administrator, Vault
operator, and protected-environment reviewers are trusted operators. A host
administrator can read process memory and mounted data. A malicious approved
model provider can retain content it legitimately receives. These risks require
operator governance and vendor agreements rather than an application-layer
compatibility mechanism.

## Feature review questions

Before merging a new feature, answer these in its tests and documentation:

1. Which workspace-owned resource authorizes the operation, and can a shared
   external identifier bypass that lookup?
2. What byte, count, geometry, duration, concurrency, and durable-storage limits
   apply before expensive work?
3. Can a retry, cancellation, stale lease, or revision race duplicate work or
   overwrite a newer correction?
4. Which secrets or document values can reach logs, errors, audits, browser
   state, provider requests, or public resources?
5. How is partial failure reconciled across MariaDB and external systems?
6. What acceptance test crosses the boundary with two tenants or concurrent
   actors, and what recovery evidence is required?
