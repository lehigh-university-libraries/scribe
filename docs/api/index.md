# API integration

Connect is Scribe's application API. Generated TypeScript and Go clients are the
preferred integration path; the generated OpenAPI 3.1 document supports tools
that do not speak Connect directly. Use the
[generated OpenAPI contract](scribe.openapi.yaml) for client generation and
interactive API tooling.

See [context catalogs and selection rules](contexts.md) for bounded context
resolution, [ingest images and manifests](ingest.md) for the durable multi-file
protocol, [list workspace items](items.md) for bounded library pagination, and
[IIIF resources](iiif.md) for canonical Presentation 3 behavior. Prepared
[exports](exports.md) and the workspace-scoped [event stream](events.md) are
also generated from committed canonical revisions.

External IIIF editors should:

1. authenticate and select a workspace;
2. load an AnnotationPage with its revision;
3. call backend split/join/transcribe operations for structural changes;
4. save the complete page with `expected_revision`;
5. handle revision conflicts by reloading and rebasing;
6. use export or publication operations derived from the committed revision.

Do not derive resource authorization from a caller-supplied Canvas URI. Use the
Scribe page or item-image identity returned by the API.

The OpenAPI document advertises three alternative authentication mechanisms:
the interactive session cookie (named `scribe_session` by default), the
`X-Scribe-API-Key` header, and an `Authorization: Bearer` token containing a
Scribe API key or a JWT from a registered issuer. Operations marked
session-only in protobuf advertise only the cookie; intentionally anonymous
operations explicitly override the document default.

RPC request bodies are sent uncompressed. Scribe rejects `Content-Encoding`,
`Connect-Content-Encoding`, or `Grpc-Encoding` request compression so the
per-procedure byte limit applies to the actual protobuf or JSON message rather
than its compressed representation. Response compression and
`Accept-Encoding` negotiation remain supported. The default request class is
4 MiB, complete AnnotationPage operations are limited to 32 MiB, and image
upload/processing operations are limited to 140 MiB.

Metrics and provider-call audits are also Connect-only contracts. Use
`ContextService.GetContextMetrics` and
`ItemService.ListItemProviderCallAudits`; do not introduce duplicate REST
routes for either operation.
