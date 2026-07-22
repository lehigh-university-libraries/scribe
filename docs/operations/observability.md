# Observability

Structured logs use stable fields including operation, request or session ID,
workspace, provider, model, job ID, attempt, status, and latency. Never log
prompts, transcription previews, API keys, OAuth tokens, identity-token URLs,
cookies, or raw provider response bodies by default.

Minimum dashboards and alerts:

- API request rate, latency, and Connect error code;
- readiness failures and container restarts;
- queue depth, oldest age, lease expiry, retries, and dead-letter depth;
- provider latency/error category by registered provider and model;
- canonical save conflicts and publication lag;
- MariaDB connections, storage, slow queries, and backup freshness;
- workspace quota and rate-limit rejection counts.

Terraform supplies the release-blocking platform alerts: Pub/Sub dead-letter
depth, oldest unacked transcription age, frontend 5xx responses, and failed
backend/OCR readiness executions. Production refuses to plan without at least
one configured notification channel. The daily protected backup workflow owns
backup-policy and 36-hour freshness alerting and opens a GitHub issue on
failure. Application metrics listed above still belong on the operator
dashboard; absence of a dashboard is not evidence that a platform alert fired.

After every deploy, inspect the backend and OCR readiness job executions. OCR
readiness includes image normalization, segmentation, Kraken transcription,
and the production default Ollama request. A failed production Terraform apply
or readiness failure initiates automatic rollback to the prior recorded
reviewed source, configuration, generation, and digest set.
`attestation-failed-rolled-back` means the Cloud Run
revision, traffic, or frontend digest check failed and the prior deployment was
restored. `readiness-failed-rolled-back` means deep service readiness failed and
the prior deployment was restored; `apply-failed-rolled-back` means Terraform
returned nonzero after potentially committing partial infrastructure and the
prior deployment was restored. `rollback-failed` is an incident requiring
immediate operator action. `url-failure` means readiness passed but Terraform's ingress
could not be resolved. `backup-verification-failure` means readiness passed but
the required production recovery policy did not; both statuses fail the release.

Correlate one user operation through API, outbox, worker attempt, and provider
call using identifiers rather than captured content.

Use the generated Connect APIs for diagnostic data:
`ContextService.GetContextMetrics` reports context-quality metrics and
`ItemService.ListItemProviderCallAudits` reports per-item provider calls.
Provider audits contain bounded metadata and categorical errors, never prompts
or provider request/response bodies. Neither capability has a parallel REST
route.
