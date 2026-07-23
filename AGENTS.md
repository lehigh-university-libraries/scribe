# Scribe repository instructions

This file is the concise entry point for agents and contributors. The
authoritative, published policies are:

- [release status, blockers, and definition of done](docs/reference/release-criteria.md)
- [architecture and engineering invariants](docs/reference/engineering-contract.md)
- [quality gates](docs/reference/quality-gates.md)
- [documentation workflow](docs/development/documentation.md)

Read the concept, architecture, and operations page for the area you change.
Start at [docs/index.md](docs/index.md).

## Required contract

Run the smallest focused test while iterating, then run the repository contract
before declaring a change complete:

```bash
make ci
```

Required GitHub jobs independently verify contracts/lint, tests, security, and
infrastructure/documentation. Generated output and relevant documentation must
be committed with the implementation.

## Essential invariants

- Scribe is greenfield during the current hardening pass. Prefer one clean
  invariant over a compatibility layer or duplicate persistence/API path.
- Connect/protobuf is the business API. Complete, workspace-scoped IIIF
  Presentation 3 AnnotationPages and monotonic revisions are canonical OCR
  state; exports, search, metrics, and publications are derived.
- Authorization is enforced at side-effect-free route policy boundaries.
  Tenant isolation, input cost, secret/log exposure, idempotency, and failure
  recovery are part of every change.
- Providers, models, and segmentors are registered server capabilities.
  Workspace input never controls authenticated endpoint URLs or audiences.
- Browser state is a draft over one canonical base revision. Background work
  and job attempts are revision-fenced and cannot overwrite newer corrections.
- Pull-request code receives no cloud credentials. Protected preview and
  production jobs consume immutable reviewed inputs.
- Tools, actions, modules, downloads, and runtime images remain pinned.
- Repository automation is Go or Bash. Do not embed Python through `-c`,
  standard input, or heredocs; use the reviewed packaged Python tools only for
  Kraken or Zensical.
- Durable guidance belongs under `docs/`; keep this file and the README as
  short indexes.

The current release remains unapproved until every blocker in the
[release criteria](docs/reference/release-criteria.md) has committed automated
evidence and all required jobs pass.
