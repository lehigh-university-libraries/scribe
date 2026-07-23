# Scribe

Scribe ingests handwritten documents, couples segmentation and transcription
models through reusable contexts, and stores correctable OCR as IIIF
Presentation 3 AnnotationPages with the IIIF Text Granularity Extension.

Use this documentation according to the task in front of you:

- [Run Scribe locally](getting-started/index.md).
- [Choose and configure a processing context](getting-started/using-contexts.md).
- [Understand the canonical IIIF model](concepts/canonical-iiif.md).
- [Add a provider, segmentor, or RPC](development/index.md).
- [Integrate an external editor](api/index.md).
- [Deploy and operate Scribe](operations/index.md).
- [Review release criteria and engineering invariants](reference/release-criteria.md).

The repository favors a modular monolith plus a worker. Connect schemas are the
application API contract; IIIF Presentation 3 is the document interchange and
persistence contract. Generated files and documentation are checked in and CI
verifies that they match their sources.

## Project status

Scribe is greenfield software under active hardening. Do not infer production
readiness from the presence of a UI or a checked box. The current acceptance
criteria live in [release criteria](reference/release-criteria.md), the durable
rules live in the [engineering contract](reference/engineering-contract.md),
and executable checks live behind `make ci`. `AGENTS.md` is a concise
repository entry point to those published sources.
