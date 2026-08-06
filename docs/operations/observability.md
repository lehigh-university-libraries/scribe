# Observability

Structured logs use stable fields including operation, request or session ID,
workspace, provider, model, job ID, attempt, status, and latency. Never log
prompts, transcription previews, API keys, OAuth tokens, identity-token URLs,
cookies, or raw provider response bodies by default.

The API and worker install bounded OpenTelemetry SDK pipelines. Managed GCP
deployments push metrics directly to Cloud Monitoring and sampled spans to
Cloud Trace with Application Default Credentials; there is no public
`/metrics` endpoint or operator-configurable telemetry URL. Terraform enables
the fixed Google exporter, Cloud Trace API, and only
`roles/monitoring.metricWriter` plus `roles/cloudtrace.agent` on the application
identity. Local telemetry is disabled unless an operator explicitly selects
the Google exporter and provides ADC. PR previews neither enable Google export
nor receive those project-wide writer roles, so reviewed-but-unmerged code
cannot create production-project telemetry.

The application OpenTelemetry pipeline emits these Cloud Monitoring workload
metrics:

- `workload.googleapis.com/scribe.connect.server.requests`, a request counter;
- `workload.googleapis.com/scribe.connect.server.duration`, a seconds
  histogram;
- `workload.googleapis.com/scribe.transcription.queue.depth`, the number of
  jobs claimable now;
- `workload.googleapis.com/scribe.transcription.queue.oldest_age`, the age in
  seconds of the oldest claimable job;
- `workload.googleapis.com/scribe.transcription.queue.expired_leases`, the
  number of running jobs whose worker lease has expired; and
- `workload.googleapis.com/scribe.telemetry.queue.collection_errors`, a
  counter for failed queue sampling queries.

Connect request series use only compiled service, method, and bounded Connect
status-code labels. Unknown procedure paths collapse to `unknown`; workspace,
job, user, provider payload, and error strings are never metric labels or span
attributes. Server spans begin a new trace at the public Connect boundary and
record only the compiled RPC identity plus categorical outcome. The ratio
sampler keeps five percent by default; because the server generates the root
trace ID, public clients cannot choose an ID or `traceparent` flag that forces
export. It never records an exception event because those events can copy an
error string into the trace.

Only the worker samples the SQL queue, immediately on startup and every 30
seconds by default. The snapshot follows the worker claim predicate: due
pending jobs plus expired running leases. Healthy leased jobs and delayed
retries do not inflate actionable depth. The database clock supplies age, and
the deployment-wide gauges have no tenant labels. Every worker replica samples
the same values under its own `service.instance.id`; dashboards must reduce
queue gauges with `MAX`, never `SUM`. Additional replicas also add one bounded
`COUNT`/`MIN` query per poll interval, so elect a singleton collector before
that load becomes material. Every resource has a bounded
`deployment.environment.name` (`dev` or `prod`) label. Each query and export
has a fixed timeout. Initialization, export, sampling, and flush failures produce
only categorical, redacted diagnostics and never change `/livez` or `/readyz`.

The application metrics above support these dashboards and alerts directly:

- API request rate, latency, and Connect error code;
- readiness failures and container restarts;
- queue depth, oldest age, expired leases, and queue-collection failures.

Use the platform sources named below for container health, Pub/Sub delivery,
MariaDB, and backup signals. Provider audits and the diagnostic Connect APIs
are bounded per-item investigation data, not time-series metrics. Scribe does
not currently emit dedicated provider latency, save-conflict, publication-lag,
quota-rejection, or rate-limit-rejection metric series; do not create empty
dashboards that imply those signals exist.

Terraform supplies the release-blocking platform alerts: Pub/Sub dead-letter
depth, oldest unacked transcription age, frontend 5xx responses, and failed
backend/OCR readiness executions. Production refuses to plan without at least
one configured notification channel. The daily protected backup workflow owns
backup-policy and 36-hour freshness alerting and opens a GitHub issue on
failure. Application metrics listed above still belong on the operator
dashboard; absence of a dashboard is not evidence that a platform alert fired.

After every deploy, inspect the backend and OCR readiness job executions. Use
the [production troubleshooting](troubleshooting.md#cloud-run-readiness)
runbook to download the bounded diagnostics or rerun a probe safely. OCR
readiness includes image normalization, Tesseract segmentation, Kraken
transcription, and the production default Ollama request. Segmentation and
transcription each use a 240-second request budget so a scale-to-zero CPU
inference service can load its model and complete useful work; handler and write
deadlines retain
bounded margins below the current 300-second Cloud Run service request timeout.
The frontend proxy caps upstream inactivity at 270 seconds while charging
backend wake time and upstream work to one 285-second request budget below the
platform cutoff. Startup rejects custom values unless the upstream cap is below
the frontend budget and the frontend budget remains below the 300-second
platform boundary. Managed browser readiness allows 300 seconds for the
upload/editor handoff. An upload marker covers the frontend's bounded retry
sequence and requires its last `UploadItemImage` response to succeed;
individual retryable attempts are not promoted to the generic network marker.
Structure and manifest markers separately isolate live editor-transform and
preserve-hOCR import failures.

The cleanup marker can remain active through the 300-second mutation commit
horizon and a 180-second recovery tail when an upload, manifest import, or token
creation loses its response; this is bounded recovery, not an unbounded browser
retry. The runner stops product work after 30 minutes and reserves the final 10
minutes of its 40-minute Cloud Run task for deadline-aware reconciliation and
request/control overhead. A platform `deadline` before the categorical browser
marker therefore indicates runner/job budget drift and must fail deployment.
Reconciliation logs no resource name, URL, response body, or token secret.
A failed production Terraform apply or readiness failure initiates automatic
rollback to the prior recorded reviewed source, configuration, generation, and
digest set.
`attestation-failed-rolled-back` means the Cloud Run
revision, traffic, or frontend digest check failed and the prior deployment was
restored. `readiness-failed-rolled-back` means deep service readiness failed and
the prior deployment was restored; `apply-failed-rolled-back` means Terraform
returned nonzero after potentially committing partial infrastructure and the
prior deployment was restored. `url-failed-rolled-back` means readiness passed
but Terraform's canonical ingress URL could not be resolved, so the prior
deployment was restored. `rollback-failed` is an incident requiring immediate
operator action. A bare `url-failure` means no rollback ran, either for a
preview or because production had no recorded rollback target.
`backup-verification-failure` means readiness passed but the required
production recovery policy did not; every non-success status fails the release.

Correlate one user operation through API, outbox, worker attempt, and provider
call using identifiers rather than captured content.

Use the generated Connect APIs for diagnostic data:
`ContextService.GetContextMetrics` reports context-quality metrics and
`ItemService.ListItemProviderCallAudits` reports per-item provider calls.
Provider audits contain bounded metadata and categorical errors, never prompts
or provider request/response bodies. Neither capability has a parallel REST
route.
