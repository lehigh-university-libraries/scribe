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

Terraform supplies the platform alerts: Pub/Sub dead-letter depth, oldest
unacked transcription age, frontend 5xx responses, and failed backend/OCR
readiness executions. Application metrics listed above still belong
on the operator dashboard; absence of a dashboard is not evidence that a
platform alert fired.

Every apply runs the backend and OCR readiness jobs and fails if either does;
see [production troubleshooting](troubleshooting.md#cloud-run-readiness) to
read their logs or rerun one. OCR readiness covers image normalization, the
default Scribe segmentation, Kraken transcription, and the production default
Ollama request. Segmentation and transcription each use a 240-second request
budget so a scale-to-zero CPU inference service can load its model and complete
useful work; handler and write deadlines keep bounded margins below the
300-second Cloud Run service request timeout. The frontend proxy caps upstream
inactivity at 270 seconds while charging backend wake time and upstream work to
one 285-second request budget below the platform cutoff. Startup rejects custom
values unless the upstream cap is below the frontend budget and the frontend
budget remains below the 300-second platform boundary.

A failed apply or readiness job fails the workflow; nothing is rolled back
automatically. Fix forward, or revert the commit on `main`.

Correlate one user operation through API, outbox, worker attempt, and provider
call using identifiers rather than captured content.

Use the generated Connect APIs for diagnostic data:
`ContextService.GetContextMetrics` reports context-quality metrics and
`ItemService.ListItemProviderCallAudits` reports per-item provider calls.
Provider audits contain bounded metadata and categorical errors, never prompts
or provider request/response bodies. Neither capability has a parallel REST
route. Remote layout detection emits `operation=segment_image` with the
registered segmentation selection, duration, and (on failure) only a redacted
provider category and HTTP status. It never records the endpoint, image path,
response body, or detected document text.
