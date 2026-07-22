# Scribe

Scribe ingests handwritten documents, couples segmentation and transcription
models through reusable contexts, and stores correctable OCR as IIIF
Presentation 3 AnnotationPages with the IIIF Text Granularity Extension.

Use this documentation according to the task in front of you:

- [Run Scribe locally](getting-started/index.md).
- [Understand the canonical IIIF model](concepts/canonical-iiif.md).
- [Add a provider, segmentor, or RPC](development/index.md).
- [Integrate an external editor](api/index.md).
- [Deploy and operate Scribe](operations/index.md).

The repository favors a modular monolith plus a worker. Connect schemas are the
application API contract; IIIF Presentation 3 is the document interchange and
persistence contract. Generated files and documentation are checked in and CI
verifies that they match their sources.

## Project status

Scribe is greenfield software under active hardening. Do not infer production
readiness from the presence of a UI or a checked box. The current acceptance
criteria and remaining work are maintained in `AGENTS.md`, while executable
checks live behind `make ci`.
