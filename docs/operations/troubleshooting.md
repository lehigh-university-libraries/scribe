# Production troubleshooting

Production runs on Container-Optimized OS (COS). The VM boot disk contains the
reviewed Cloud Compose runtime, while the data and Docker-volume disks retain
the workspace checkout, local secrets, uploads, and named volumes across VM
replacement. Diagnose that managed lifecycle before changing anything by hand.

## Preconditions

Only an authorized production operator should run these commands. First, from
an operator checkout, confirm that no production apply, rollback, or other
lifecycle operation is still running:

```bash
gh run list \
  --repo lehigh-university-libraries/scribe \
  --workflow terraform-apply.yaml \
  --branch main \
  --limit 3
```

Wait for the active workflow to finish. Record its run ID and expected commit
before connecting to the VM. Manual recovery may restart the application and
must not race Terraform or another operator.

## Separate boot failure from application failure

On the production VM, collect read-only evidence first:

```bash
sudo cloud-init status --long
sudo systemctl --no-pager --full status \
  cloud-final.service \
  cloud-compose-bootstrap.service \
  cloud-compose.service
sudo journalctl -b \
  -u cloud-final.service \
  -u cloud-compose-bootstrap.service \
  -u cloud-compose.service \
  --no-pager -n 300
sudo test ! -f /home/cloud-compose/run.log ||
  sudo tail -n 300 /home/cloud-compose/run.log
```

`cloud-compose-bootstrap.service` owns retryable root convergence. Its journal
is the canonical bootstrap log; `run.log`, when present, is legacy or rollout
evidence. A failed `cloud-compose.service` means the unprivileged application
lifecycle reached `/home/cloud-compose/up` and failed during preflight, image
pull, secret generation, convergence, Compose startup, or local readiness.
Cloud final waits for the bootstrap marker, but the enabled bootstrap unit keeps
retrying a transient failure after cloud final exits.

The completion marker and service state distinguish a completed bootstrap from
one that stopped early:

```bash
sudo test -f /home/cloud-compose/.cloud-compose-bootstrap-complete &&
  echo 'cloud-compose bootstrap marker is present'
sudo systemctl is-active cloud-compose.service
```

Do not publish the complete boot log without reviewing it. Never print `.env`,
secret-file contents, tokens, or raw provider responses into an issue or chat.

Failed protected production and preview applies upload
`<workspace>-vm-bootstrap-diagnostics.log` with the Terraform artifacts. That
file contains only typed instance, Direct VPC, effective-firewall, and
module-owned service-account key counts. `capacity=exhausted` means an app or
internal identity has reached GCP's ten user-managed-key limit; it does not
identify or delete any key. Query failures are reported as `unavailable`
without persisting raw API errors or key IDs.

## Verify the managed target

The manifest is the source of truth for the checkout, Compose project, and
immutable Git revision:

```bash
sudo jq -r \
  '.primary | [.project_dir, .compose_project_name, .docker_compose_branch] | @tsv' \
  /home/cloud-compose/compose-projects.json

expected_scribe_sha="$(
  sudo jq -r '.primary.docker_compose_branch' \
    /home/cloud-compose/compose-projects.json
)"
scribe_project_dir="$(
  sudo jq -r '.primary.project_dir' \
    /home/cloud-compose/compose-projects.json
)"
actual_scribe_sha="$(
  sudo -u cloud-compose \
    git -C "$scribe_project_dir" rev-parse --verify HEAD
)"
test "$actual_scribe_sha" = "$expected_scribe_sha" &&
  echo 'managed checkout matches the manifest commit'
```

Production should report `/mnt/disks/data/scribe/prod`, `scribe-prod`, and one
40-character commit. The project directory is stable for the current
persistence generation because it owns ignored local secrets, including
MariaDB's root bootstrap password. The checkout is deliberately detached at
the reviewed commit.

Inspect file metadata without reading values:

```bash
scribe_project_dir="$(
  sudo jq -r '.primary.project_dir' \
    /home/cloud-compose/compose-projects.json
)"
sudo stat -c '%U:%G %a %s %n' \
  "$scribe_project_dir/.env" \
  "$scribe_project_dir/secrets/triplet_presentation_write_token"
sudo test -s \
  "$scribe_project_dir/secrets/triplet_presentation_write_token" &&
  echo 'Triplet write-token bind is present and nonempty'
```

Root convergence makes `.env` writable only by the `cloud-compose` runtime
account; Scribe's atomic updates leave it owner-only with mode `0600`. Every
declared local secret must be a nonempty regular file. The secret group is
recorded in `.env`; do not reveal it by printing the file.

For a port or network collision, restrict inspection to the exact production
project:

```bash
sudo docker ps -a \
  --filter label=com.docker.compose.project=scribe-prod \
  --format 'name={{.Names}} service={{.Label "com.docker.compose.service"}} status={{.Status}}'
sudo docker inspect scribe-prod-traefik-1 \
  --format 'status={{.State.Status}} error={{.State.Error}}'
sudo docker network inspect scribe-prod_default \
  --format '{{range $id,$c := .Containers}}{{printf "name=%s ip=%s\n" $c.Name $c.IPv4Address}}{{end}}'
sudo docker ps --filter publish=80 \
  --format 'name={{.Names}} project={{.Label "com.docker.compose.project"}} ports={{.Ports}}'
sudo ss -ltnp '( sport = :80 )'
```

An empty host-port result does not rule out a stale Docker network endpoint.
The runtime preflight checks both exact project labels and the fixed Traefik
address.

Render status through the same profile, checkout, and overlay used by the
service:

```bash
scribe_project_dir="$(
  sudo jq -r '.primary.project_dir' \
    /home/cloud-compose/compose-projects.json
)"
sudo -u cloud-compose \
  bash -c 'source /home/cloud-compose/profile.sh
cd -- "$1"
docker compose \
  -f docker-compose.yaml \
  -f /home/cloud-compose/scribe-runtime.compose.yaml \
  ps' bash "$scribe_project_dir"
```

## Retry the supported lifecycle

If bootstrap completed and only `cloud-compose.service` failed, restart that
unit:

```bash
sudo systemctl restart cloud-compose.service
sudo systemctl --no-pager --full status cloud-compose.service
```

This takes the shared lifecycle lock, reuses the verified checkout, refreshes
deployment-owned environment values, retries image pulls, creates missing
declared local secret files, and repairs an interrupted short application-token
write in place. A repair first changes a nonsecret Compose generation label, so
the ordinary Compose startup recreates API, worker, and Triplet together rather
than leaving any process with an older token in its environment. The lifecycle
converges only the exact `scribe-prod` containers and network when required,
starts the reviewed services, and waits for readiness. It does not remove named
volumes.

Do not invoke `generate-secrets.sh` directly to repair a live application
token. Partial lifecycles such as `make up-db` deliberately refuse that repair
because they do not follow it with a full Compose startup. Use `make up`
locally or restart `cloud-compose.service` in the hosted runtime so the
generation-label change and consumer recreation happen in the same resumable
lifecycle.

In-place short-token repair depends on Linux `/proc` descriptor semantics. On
Darwin or BSD, if `make up` reports a short token, run `make down`, remove only
the exact reported token file under `secrets/`, and run `make up` again to
regenerate it. Missing-token creation remains supported on those hosts.

If the completion marker is absent, first check whether
`cloud-compose-bootstrap.service` is activating or automatically retrying. Do
not start a second lifecycle while it is active. After resolving a reviewed
input failure, restart the systemd-owned idempotent bootstrap:

```bash
sudo systemctl restart cloud-compose-bootstrap.service
sudo systemctl --no-pager --full status \
  cloud-compose-bootstrap.service cloud-compose.service
sudo journalctl -b \
  -u cloud-compose-bootstrap.service \
  -u cloud-compose.service \
  --no-pager -n 300
```

This is the supported recovery for metadata left by VM replacement, including
an unreadable `.env`. Root convergence operates only on the exact
manifest-owned project directory and `.env`, then source preparation restores
the immutable commit and application initialization scaffolds declared secret
files before the service starts. A retry does not repeat app initialization
after the current-boot marker is published. The original cloud-init failure
remains useful historical evidence; successful convergence is proven by the
completion marker, active service, exact Git SHA, and readiness checks.

Verify recovery:

```bash
sudo test -f /home/cloud-compose/.cloud-compose-bootstrap-complete &&
  echo 'cloud-compose bootstrap marker is present'
sudo systemctl is-active --quiet cloud-compose.service
curl --noproxy '*' --fail --silent --show-error \
  --connect-timeout 2 --max-time 10 http://127.0.0.1/livez
curl --noproxy '*' --fail --silent --show-error \
  --connect-timeout 2 --max-time 10 http://127.0.0.1/readyz
```

### Known failure signatures

| Signature | Meaning and supported recovery |
| --- | --- |
| `.env: permission denied` | The persistent checkout retained metadata from an older VM. Run the full root bootstrap; do not apply recursive ownership changes. |
| `secret file ... does not exist` or `bind source path does not exist` | Verify the declared bind with `test -s`. Restart `cloud-compose.service`: the locked application lifecycle creates a missing local secret and safely regenerates a short Triplet or page-token secret without rotating a valid file. The repair preserves the bind-mounted inode and changes the Compose generation so API, worker, and Triplet are recreated together. Vault-backed database secrets are synchronized before Compose starts. If an externally managed credential remains absent or empty, run the full root bootstrap; never create an empty placeholder manually. |
| `scripts/update-env.py: No such file` under `/mnt/disks/data` | An obsolete data-disk helper was invoked. Stop using it and return to the systemd-owned bootstrap or `cloud-compose.service`; no replacement Python helper is required. |
| `Address already in use` for Traefik | Inspect the exact project containers, network, port 80, and Traefik state above, then restart `cloud-compose.service`. Its preflight repairs only exact-project stale state. If it reports that the canonical network is owned outside the project label, stop and escalate rather than deleting it. |
| Traefik reports a configuration parser error | The immutable application configuration is invalid. Fix and redeploy the repository source; do not edit generated configuration on the VM. |
| Image upload or URL ingest returns `500` after about ten seconds | An old application image is attempting a metadata-only identity-token lookup even though container metadata access is intentionally blocked. Confirm the managed checkout SHA and inspect only the API/worker startup status and redacted logs. Current images mint all configured outbound service audiences from the managed credential before listening, so this condition must fail deployment readiness. Deploy the reviewed fix; do not open metadata access or copy credentials by hand. |

## Cloud Run readiness

The protected deployment uploads bounded diagnostics that contain typed
execution/task fields and exact allowlisted markers, not raw responses. Download
the exact failed run and inspect only those files:

```bash
scribe_run_id=RUN_ID
scribe_artifact_dir="$(mktemp -d)"
gh run download "$scribe_run_id" \
  --repo lehigh-university-libraries/scribe \
  --dir "$scribe_artifact_dir"
find "$scribe_artifact_dir" \
  -type f -name '*readiness-diagnostics.log' \
  -print -exec sed -n '1,160p' {} \;
```

Use [observability](observability.md) to interpret deployment status and
readiness markers. A backend marker isolates frontend-to-VM startup, transport,
or readiness-contract failures. An OCR marker identifies the failed image,
segmentation, transcription, or Ollama stage. A preview browser marker names
only the failed home, context, upload, handoff, transcription, annotations,
editor, overlay, retranscribe, structure, save, publish, responsive, token,
manifest, cleanup, network, or CSP stage. Upload covers the complete bounded
frontend retry sequence; structure covers live draft transforms through Save;
manifest covers preserve-hOCR import and first-page canonical OCR. Inspect the
named product boundary and the ordinary redacted service telemetry; browser
state, DOM, URLs, tokens, console text, and provider responses are intentionally
unavailable as artifacts.

If `cleanup` follows an interrupted mutation, allow the runner its full
10-minute cleanup reserve. It has already closed the UI page and is polling the
workspace-scoped API for the exact generated upload name, manifest source tuple,
or token name. The reserve contains the 300-second commit horizon, a 180-second
recovery tail, and bounded request/control overhead. A cleanup failure after
that bound means stable absence was not proved; rerun only after investigating
the ordinary redacted service telemetry. A Cloud Run `deadline` before the
categorical marker means the 30-minute scenario or 10-minute cleanup budget no
longer fits the configured 40-minute task and is an infrastructure contract
failure, not permission to skip cleanup.

An authorized production deploy operator may rerun the same bounded probes from
a trusted checkout of the deployed `main` commit. This creates new Cloud Run
job executions; it does not repair a service:

```bash
export GCLOUD_PROJECT=your-gcp-project-id
export SCRIBE_REGION=us-east5
scribe_readiness_dir="$(mktemp -d)"

./ci/run-cloud-run-readiness.sh \
  scribe-prod-backend-readiness \
  backend \
  "$scribe_readiness_dir/backend.log"
./ci/run-cloud-run-readiness.sh \
  scribe-prod-ocr-readiness \
  ocr \
  "$scribe_readiness_dir/ocr.log"
```

For a segmentation failure, this query selects only Scribe's known redacted
failure message and categorical fields from the exact production service:

```bash
gcloud logging read \
  'resource.type="cloud_run_revision" AND resource.labels.service_name="scribe-segmentor-prod" AND textPayload:"segmentor request failed"' \
  --project "$GCLOUD_PROJECT" \
  --freshness=2h \
  --order=asc \
  --limit=50 \
  --format='value(timestamp,textPayload)'
```

Do not broaden the query to raw provider payloads. Preserve the run ID,
execution name, operation, category, error type, and subprocess byte count as
incident evidence.

## Unsafe actions

- Do not run `git pull`, switch branches, or edit the detached managed checkout.
- Do not invoke legacy `/mnt/disks/data/{init,up,down}` helpers or run
  `/home/cloud-compose/run.sh` outside its systemd bootstrap unit.
- Do not manually `chmod -R`, `chown -R`, touch secret files, or print their
  contents.
- Do not run `docker system prune`, `docker volume rm`, `docker compose down
  --volumes`, or remove a network/container outside the exact validated project.
- Do not edit `.env`, Compose YAML, or generated runtime overlays on the VM;
  change reviewed source or Terraform inputs and deploy them.
- Do not run credentialed deployment or readiness helpers from a pull-request
  checkout.

If bounded convergence refuses ownership or identity checks, preserve the
evidence and fix the reviewed source or infrastructure. A refusal is not
authorization to widen the recovery boundary.
