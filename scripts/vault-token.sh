#!/usr/bin/env bash
# Print a token for Terraform's Vault provider in the given workspace. dev and
# prod each own a Vault server; previews use dev's. Prints nothing when that
# Vault does not exist yet, so the first apply of dev or prod can create it.
#
# Logs in as the current gcloud account through Google JWT, falling back to the
# stored root token (a new Vault has no JWT roles yet, and CI has none).

set -euo pipefail

workspace="${1:?usage: scripts/vault-token.sh <workspace>}"
: "${GCLOUD_PROJECT:?GCLOUD_PROJECT is required}"
region="${TF_VAR_region:-us-east5}"
service="vault-server-dev"
[ "$workspace" = "prod" ] && service="vault-server-prod"

addr="$(gcloud run services describe "$service" --project "$GCLOUD_PROJECT" --region "$region" \
  --format='value(status.url)' 2>/dev/null || true)"
if [ -z "$addr" ]; then
  echo "${service} does not exist yet; continuing without a Vault token." >&2
  exit 0
fi

account="$(gcloud config get-value account 2>/dev/null)"
slug="$(printf '%s' "$account" | sed 's/@/-at-/g; s/\./-/g')"
jwt="$(gcloud auth print-identity-token "$account" 2>/dev/null || true)"
for role in "break-glass-admin-${slug}" "admin-${slug}"; do
  [ -n "$jwt" ] || break
  if curl -fsS -H "X-Admin-Token: $(gcloud auth print-access-token)" \
    --data "$(jq -cn --arg role "$role" --arg jwt "$jwt" '{role: $role, jwt: $jwt}')" \
    "${addr%/}/v1/auth/google-jwt/login" | jq -er '.auth.client_token'; then
    exit 0
  fi
done 2>/dev/null

gcloud storage cat "gs://${GCLOUD_PROJECT}-${service}-key/root-token.enc" | base64 --decode |
  gcloud kms decrypt --project "$GCLOUD_PROJECT" --location global --keyring "$service" --key vault \
    --ciphertext-file - --plaintext-file - | tr -d '\n'
