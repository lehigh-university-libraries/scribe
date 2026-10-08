#!/usr/bin/env bash
# One-time project setup that Terraform cannot do for itself: the GitHub
# Actions identities CI uses to run Terraform and publish OCR images, the
# Terraform state bucket's retention, and the production alert email channel.
# Safe to re-run; existing resources are left as they are.
#
# The production deploy identity (secrets.GSA in the production environment)
# is created by hand and already holds the roles Terraform needs.
#
#   GCLOUD_PROJECT=... MONITORING_NOTIFICATION_EMAIL=ops@lehigh.edu scripts/bootstrap-gcp-identities.sh

set -euo pipefail

: "${GCLOUD_PROJECT:?GCLOUD_PROJECT is required}"
: "${MONITORING_NOTIFICATION_EMAIL:?MONITORING_NOTIFICATION_EMAIL is required}"
state_bucket="${TF_STATE_BUCKET:-${GCLOUD_PROJECT}-terraform}"
repo="lehigh-university-libraries/scribe"
number="$(gcloud projects describe "$GCLOUD_PROJECT" --format='value(projectNumber)')"
gcloud config set project "$GCLOUD_PROJECT" >/dev/null

# identity <account> <pool> <github environment> <workflow file> <project roles...>
identity() {
  local account="$1" pool="$2" environment="$3" workflow="$4"
  shift 4
  local email="${account}@${GCLOUD_PROJECT}.iam.gserviceaccount.com"
  local condition="assertion.repository == '${repo}' && assertion.ref == 'refs/heads/main' && assertion.environment == '${environment}' && assertion.workflow_ref == '${repo}/.github/workflows/${workflow}@refs/heads/main'"

  gcloud iam service-accounts describe "$email" >/dev/null 2>&1 ||
    gcloud iam service-accounts create "$account" --display-name="Scribe ${environment} ${account}"
  gcloud iam workload-identity-pools describe "$pool" --location=global >/dev/null 2>&1 ||
    gcloud iam workload-identity-pools create "$pool" --location=global --display-name="$pool"
  gcloud iam workload-identity-pools providers describe github-main --workload-identity-pool="$pool" --location=global >/dev/null 2>&1 ||
    gcloud iam workload-identity-pools providers create-oidc github-main --workload-identity-pool="$pool" --location=global \
      --issuer-uri=https://token.actions.githubusercontent.com \
      --attribute-mapping="google.subject=assertion.sub,attribute.repository=assertion.repository,attribute.workflow_ref=assertion.workflow_ref,attribute.ref=assertion.ref,attribute.environment=assertion.environment" \
      --attribute-condition="$condition"
  gcloud iam service-accounts add-iam-policy-binding "$email" --role=roles/iam.workloadIdentityUser \
    --member="principalSet://iam.googleapis.com/projects/${number}/locations/global/workloadIdentityPools/${pool}/attribute.repository/${repo}" >/dev/null
  local role
  for role in "$@"; do
    gcloud projects add-iam-policy-binding "$GCLOUD_PROJECT" --member="serviceAccount:${email}" --role="$role" --condition=None >/dev/null
  done
  echo "${environment}: provider projects/${number}/locations/global/workloadIdentityPools/${pool}/providers/github-main, service account ${email}"
}

# Previews run Terraform for their own workspace and push preview images.
identity scribe-preview-deploy scribe-preview-deploy-wif preview terraform-preview.yaml \
  roles/compute.admin roles/iam.serviceAccountAdmin roles/iam.serviceAccountUser roles/pubsub.admin \
  roles/resourcemanager.projectIamAdmin roles/run.admin roles/serviceusage.serviceUsageConsumer roles/storage.admin \
  roles/cloudsql.admin roles/secretmanager.admin roles/dns.admin roles/cloudscheduler.admin
gcloud artifacts repositories add-iam-policy-binding internal --location=us \
  --member="serviceAccount:scribe-preview-deploy@${GCLOUD_PROJECT}.iam.gserviceaccount.com" --role=roles/artifactregistry.repoAdmin >/dev/null

# Production pushes OCR images built from main.
identity scribe-prod-ocr scribe-production-ocr-wif production terraform-apply.yaml
gcloud artifacts repositories add-iam-policy-binding internal --location=us \
  --member="serviceAccount:scribe-prod-ocr@${GCLOUD_PROJECT}.iam.gserviceaccount.com" --role=roles/artifactregistry.writer >/dev/null

gcloud storage buckets update "gs://${state_bucket}" --versioning --soft-delete-duration=14d
gcloud storage buckets add-iam-policy-binding "gs://${state_bucket}" \
  --member="serviceAccount:scribe-preview-deploy@${GCLOUD_PROJECT}.iam.gserviceaccount.com" --role=roles/storage.objectAdmin >/dev/null

channel="$(gcloud beta monitoring channels list --filter="type=email AND labels.email_address=${MONITORING_NOTIFICATION_EMAIL}" --format='value(name)' | head -n1)"
if [ -z "$channel" ]; then
  channel="$(gcloud beta monitoring channels create --display-name="Scribe production alerts" --type=email \
    --channel-labels="email_address=${MONITORING_NOTIFICATION_EMAIL}" --format='value(name)')"
fi
echo "Set MONITORING_NOTIFICATION_CHANNELS to [\"${channel}\"] in the production environment."
