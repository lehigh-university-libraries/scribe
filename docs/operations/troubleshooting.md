# Production troubleshooting

Start with the failed managed readiness job and Cloud Run revision logs. There
is no SSH, COS bootstrap, Docker daemon, or VM lifecycle to repair.

```bash
terraform -chdir=terraform output -raw backend_readiness_job
terraform -chdir=terraform output -raw ocr_readiness_job
gcloud run services describe scribe --region=us-east5
gcloud run services describe scribe-worker --region=us-east5
gcloud sql instances describe scribe-mysql
```

Use the actual workspace's names and configured region. Inspect container names
in logs: frontend validates ingress/origin, api serves Connect and upload reads,
triplet handles Image/Presentation requests, pdf converts exports, cloudsql
provides private authenticated database connectivity, and worker drains leases.

| Symptom | Check |
| --- | --- |
| API never becomes ready | Cloud SQL operation status, proxy connectivity, Secret Manager accessor grants, OAuth maps, completed migration jobs |
| Worker stops consuming jobs | Minimum instance count, cpu_idle=false, worker readiness, subscription IAM, leases, registered provider endpoints |
| Triplet returns errors | triplet database schema, SQL DSN secret version, shared write token, source-read policy, image limits |
| Frontend rejects a request | allowed_ips, exact direct Cloud Run forwarded chain, external HTTPS, canonical PUBLIC_BASE_URL |
| Backend readiness reports wrong digest | SCRIBE_DEPLOYED_API_IMAGE and image_tag resolution; reapply the correct immutable image |
| Secret loading fails | Deployment prefix and workspace, active Secret Manager versions, ADC identity, resource-scoped IAM |
| Schema migration fails | Cloud Run migration execution logs and the dirty migration ledger; repair/reset the unused database deliberately before retrying |

Run make ci before changing runtime configuration. Reapply Terraform to publish
changes; do not patch a live revision's environment outside Terraform. Run the
backend and OCR jobs again after remediation with gcloud run jobs execute JOB
--region REGION --wait. See [job recovery](job-recovery.md) for lease recovery
and [backup and restore](backup-restore.md) for durable data verification.
