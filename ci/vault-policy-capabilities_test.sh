#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

fail() {
  echo "Vault policy capability contract failed: $*" >&2
  exit 1
}

command -v docker >/dev/null 2>&1 || fail "Docker is required"
command -v jq >/dev/null 2>&1 || fail "jq is required"

image="scribe-vault-policy-test:1.21.4"
if ! docker image inspect "$image" >/dev/null 2>&1; then
  docker build --quiet -t "$image" -f terraform/modules/vault-cloud-run/Dockerfile terraform/modules/vault-cloud-run >/dev/null
fi

container="$(
  docker run -d --rm \
    --cap-drop=ALL \
    --security-opt no-new-privileges \
    --entrypoint /vault \
    -e VAULT_DEV_ROOT_TOKEN_ID=policy-test-root \
    "$image" server -dev
)"
cleanup() {
  docker rm -f "$container" >/dev/null 2>&1 || true
}
trap cleanup EXIT

vault_exec() {
  docker exec \
    -e VAULT_ADDR=http://127.0.0.1:8200 \
    -e VAULT_TOKEN="${VAULT_TOKEN_VALUE:-policy-test-root}" \
    "$container" /vault "$@"
}

ready=false
for _ in $(seq 1 30); do
  if vault_exec status >/dev/null 2>&1; then
    ready=true
    break
  fi
  sleep 1
done
[ "$ready" = true ] || fail "Vault dev server did not become ready"

docker cp terraform/policies/vault/ci.hcl "$container:/tmp/ci.hcl"
vault_exec policy write ci /tmp/ci.hcl >/dev/null
token_json="$(vault_exec token create -policy=ci -format=json)"
VAULT_TOKEN_VALUE="$(jq -r '.auth.client_token // empty' <<<"$token_json")"
export VAULT_TOKEN_VALUE
[ -n "$VAULT_TOKEN_VALUE" ] || fail "Vault did not create a CI-policy token"

capabilities() {
  vault_exec token capabilities "$1" | tr -d '\r\n'
}

preview_path="secret/data/scribe/previews/scribe-pr-75@example-project.iam.gserviceaccount.com/database/app"
preview_caps="$(capabilities "$preview_path")"
for capability in create read update; do
  [[ ",$preview_caps," == *", $capability,"* || ",$preview_caps," == *",$capability,"* ]] || fail "preview database capability $capability is missing: $preview_caps"
done
[ "$(capabilities secret/data/scribe/dev/database/app)" = "deny" ] || fail "CI token can access the dev database secret"
[ "$(capabilities secret/data/scribe/prod/database/app)" = "deny" ] || fail "CI token can access the production database secret"
[ "$(capabilities secret/data/scribe/staging/database/app)" = "deny" ] || fail "CI token can access a non-preview database secret"
[ "$(capabilities secret/data/scribe/previews/scribe-dev@example-project.iam.gserviceaccount.com/database/app)" = "deny" ] || fail "CI token can access a non-pr service-account namespace"

metadata_caps="$(capabilities secret/metadata/scribe/previews/scribe-pr-75@example-project.iam.gserviceaccount.com/provider-secrets/workspaces/8/openai)"
for capability in delete list read; do
  [[ ",$metadata_caps," == *", $capability,"* || ",$metadata_caps," == *",$capability,"* ]] || fail "preview metadata capability $capability is missing: $metadata_caps"
done
[ "$(capabilities secret/metadata/scribe/dev/provider-secrets)" = "deny" ] || fail "CI token can access dev metadata"
[ "$(capabilities sys/policies/acl/ci)" = "deny" ] || fail "CI token can rewrite its own policy"
[ "$(capabilities sys/auth/google-jwt)" = "deny" ] || fail "CI token can administer auth backends"
[ "$(capabilities auth/google-jwt/role/admin-example)" = "deny" ] || fail "CI token can mutate administrator roles"
[ "$(capabilities sys/mounts/secret)" = "deny" ] || fail "CI token can administer secret mounts"

vault_exec kv put -mount=secret scribe/previews/scribe-pr-75@example-project.iam.gserviceaccount.com/database/app password=fixture >/dev/null
if vault_exec kv put -mount=secret scribe/dev/database/app password=forbidden >/dev/null 2>&1; then
  fail "CI token wrote the dev database secret"
fi

echo "Vault preview policy syntax and effective capabilities passed."
