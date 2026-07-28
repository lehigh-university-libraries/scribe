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
  local output=""

  for _ in 1 2 3 4 5; do
    if output="$(vault_exec token capabilities "$1" 2>/dev/null)"; then
      output="${output//$'\r'/}"
      output="${output//$'\n'/}"
      if [ -n "$output" ]; then
        printf '%s' "$output"
        return 0
      fi
    fi
    sleep 1
  done

  return 1
}

require_denied() {
  local path="$1"
  local failure="$2"
  local actual=""

  actual="$(capabilities "$path")" ||
    fail "Vault capability query remained unavailable while checking a denied path"
  [ "$actual" = "deny" ] || fail "$failure"
}

preview_path="secret/data/scribe/previews/scribe-pr-75@example-project.iam.gserviceaccount.com/database/app"
preview_caps="$(capabilities "$preview_path")" ||
  fail "Vault capability query remained unavailable while checking the preview database path"
for capability in create read update; do
  [[ ",$preview_caps," == *", $capability,"* || ",$preview_caps," == *",$capability,"* ]] || fail "preview database capability $capability is missing: $preview_caps"
done
require_denied secret/data/scribe/dev/database/app "CI token can access the dev database secret"
require_denied secret/data/scribe/prod/database/app "CI token can access the production database secret"
require_denied secret/data/scribe/staging/database/app "CI token can access a non-preview database secret"
require_denied secret/data/scribe/previews/scribe-dev@example-project.iam.gserviceaccount.com/database/app "CI token can access a non-pr service-account namespace"

metadata_caps="$(capabilities secret/metadata/scribe/previews/scribe-pr-75@example-project.iam.gserviceaccount.com/provider-secrets/workspaces/8/openai)" ||
  fail "Vault capability query remained unavailable while checking the preview metadata path"
for capability in delete list read; do
  [[ ",$metadata_caps," == *", $capability,"* || ",$metadata_caps," == *",$capability,"* ]] || fail "preview metadata capability $capability is missing: $metadata_caps"
done
require_denied secret/metadata/scribe/dev/provider-secrets "CI token can access dev metadata"
require_denied sys/policies/acl/ci "CI token can rewrite its own policy"
require_denied sys/auth/google-jwt "CI token can administer auth backends"
require_denied auth/google-jwt/role/admin-example "CI token can mutate administrator roles"
require_denied sys/mounts/secret "CI token can administer secret mounts"

vault_exec kv put -mount=secret scribe/previews/scribe-pr-75@example-project.iam.gserviceaccount.com/database/app password=fixture >/dev/null
if vault_exec kv put -mount=secret scribe/dev/database/app password=forbidden >/dev/null 2>&1; then
  fail "CI token wrote the dev database secret"
fi

echo "Vault preview policy syntax and effective capabilities passed."
