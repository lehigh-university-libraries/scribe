# Add a transcription provider

Provider installation is server-owned. A context may select only a provider
and model returned by `ContextService.GetModelCatalog`; it cannot provide a
network endpoint, Cloud Run audience, or credential. Those values come from
trusted runtime configuration, and credentials are resolved from Vault for the
authenticated user or workspace.

To add a provider:

1. Add or reuse a byte-oriented client implementing
   `htr/pkg/providers.Client`. Provider HTTP payloads, authentication header
   placement, response parsing, byte ceilings, redirect rejection, and typed
   redacted errors belong in the general-purpose HTR repository. Scribe must
   not add a vendor HTTP implementation under `internal/`. Native local
   implementations such as Tesseract may use a `providerregistry.Execution`
   mode; remote providers use an HTR client factory.
2. Add one descriptor in `internal/providerregistry/registry.go`. That single
   record owns the identifier, label, factory, execution mode, configured
   models/default, context capabilities, request/response limits, retry policy,
   credential schema, and endpoint policy. Do not add a second provider switch
   or default-model table elsewhere.
3. Add administrator-owned endpoint, audience, model, deadline, retry, and
   response-limit settings to both `config.yaml` copies and their validation.
   Recheck the vendor lifecycle documentation whenever a default changes, and
   remove retired model identifiers from the allowlist. A syntactically valid
   model identifier is not evidence that the vendor still serves it.
4. Resolve credentials through the existing provider-secret/Vault path. Durable
   upload, reprocess, and worker jobs use only administrator-managed workspace
   credentials; personal credentials are intentionally limited to interactive
   editor enrichment and are never inferred from a job creator. Do not
   accept an API key, base URL, or audience from `Context` or an item payload.
   Attach request credentials with `providerregistry.WithCredential`; the HTR
   client's credential callback resolves them through its immutable
   descriptor's `Credential` method. Never copy a
   request credential into a process environment variable: concurrent
   workspaces must remain isolated. `newOpenAIClient` is the Scribe policy
   wiring reference; the request implementation itself is in HTR.
5. Map HTR's `providers.Error` categories into Scribe's stable audit/job error
   vocabulary. Never inspect response bodies or provider error strings to
   decide retry behavior, and keep request/response body capture disabled.
6. Add Scribe registry-policy tests and HTR client contract tests with a local HTTP server for
   success, timeout, oversized/malformed response, redirect rejection,
   retryable failure, secret redaction, and concurrent credential isolation.
7. Add a worker integration test proving a stale job lease or stale
   AnnotationPage revision cannot overwrite a newer correction.

The context UI must discover the new choice from `GetModelCatalog`. If the
addition requires a provider switch in a handler, service, or browser module,
the decision belongs in the registry descriptor/factory instead. The public
catalog is produced only by `Registry.Catalog`; its type deliberately omits
endpoint URLs, audiences, factories, retry details, and credential field names.

Segmentation engines follow the same rule. Register the engine descriptor and
factory in `providerregistry`, add its approved selection IDs to trusted runtime
configuration, and let `Registry.NewSegmentor` resolve exact server-owned
origins. Remote `/v1/segment` and `/v1/transcribe` calls use
`htr/pkg/remoteocr`; cached Cloud Run identity tokens use
`htr/pkg/auth/gcpidtoken`. Scribe owns only image preparation and conversion to
its `WordBox` domain type. Remote failures remain failures and are never hidden
by a different engine. Unknown selection IDs are rejected; they never silently
fall back to a different model.

Reusable evaluation belongs in `htr/pkg/metrics`. Scribe may define the text
normalization associated with a persisted metric, but it must call HTR for
Unicode character distance, CER/WER, similarity, and word alignment so model
benchmarking and production correction metrics share one implementation.

Operational metadata is read through the generated Connect
`ItemService.ListItemProviderCallAudits` RPC. Keep this as the single audit
query surface; do not add a parallel REST audit route or capture prompts,
provider requests, or provider responses.
