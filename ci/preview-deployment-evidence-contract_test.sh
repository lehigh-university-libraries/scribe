#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORKFLOW="${ROOT_DIR}/.github/workflows/terraform-preview.yaml"
DEPLOY_WORKFLOW="${ROOT_DIR}/.github/workflows/terraform-deploy.yaml"

fail() {
  echo "preview deployment evidence contract failed: $*" >&2
  exit 1
}

require() {
  local pattern="$1"
  rg -q -- "$pattern" <<<"${record_job}" || fail "record job is missing: ${pattern}"
}

forbid() {
  local pattern="$1"
  if rg -q -- "$pattern" <<<"${record_job}"; then
    fail "record job contains forbidden pattern: ${pattern}"
  fi
}

rg -q '^  pull_request_target:$' "${WORKFLOW}" || fail "preview orchestration must use trusted pull_request_target"
rg -q '^  cancel-in-progress: false$' "${WORKFLOW}" || fail "preview apply and teardown must remain serialized"
rg -q '^        id: destroy_preview_vault$' "${DEPLOY_WORKFLOW}" \
  || fail "preview Vault cleanup must have a status-bearing step id"
rg -q 'DESTROY_PREVIEW_VAULT_OUTCOME=.*steps\.destroy_preview_vault\.outcome' "${DEPLOY_WORKFLOW}" \
  || fail "preview destroy status must include Vault namespace cleanup"
destroy_steps="$(sed -n '/name: Destroy preview Terraform/,/name: Upload Terraform logs/p' "${DEPLOY_WORKFLOW}")"
rg -q '^        id: refresh_vault_access$' <<<"${destroy_steps}" \
  || fail "preview teardown must refresh its expiring Vault proxy token after a long destroy"
refresh_vault_step="$(sed -n '/name: Refresh Vault admin header token for preview teardown/,/name: Remove preview Vault namespace/p' "${DEPLOY_WORKFLOW}")"
rg -q 'uses: google-github-actions/auth@[0-9a-f]{40} # v3' <<<"${refresh_vault_step}" \
  || fail "preview teardown Vault refresh must use the pinned Google auth action"
rg -q '^          token_format: access_token$' <<<"${refresh_vault_step}" \
  || fail "preview teardown Vault refresh must mint an access token"
rg -q '^          access_token_scopes: https://www.googleapis.com/auth/userinfo.email$' \
  <<<"${refresh_vault_step}" \
  || fail "preview teardown Vault refresh must retain its narrow proxy scope"
rg -q '^          create_credentials_file: false$' <<<"${refresh_vault_step}" \
  || fail "preview teardown Vault refresh must not replace Terraform ADC"
rg -q '^        id: refresh_vault_id$' <<<"${refresh_vault_step}" \
  || fail "preview teardown must refresh its expiring Vault identity token after a long destroy"
rg -q '^          token_format: id_token$' <<<"${refresh_vault_step}" \
  || fail "preview teardown must mint a fresh Vault identity token"
rg -q 'id_token_audience: \$\{\{ steps\.vault_addr\.outputs\.vault_audience \}\}' \
  <<<"${refresh_vault_step}" \
  || fail "preview teardown Vault identity token must retain the resolved audience"
rg -q 'VAULT_ADMIN_TOKEN: \$\{\{ steps\.refresh_vault_access\.outputs\.access_token \}\}' \
  <<<"${refresh_vault_step}" \
  || fail "preview teardown Vault login must consume the post-destroy proxy token"
rg -q 'VAULT_ID_TOKEN: \$\{\{ steps\.refresh_vault_id\.outputs\.id_token \}\}' \
  <<<"${refresh_vault_step}" \
  || fail "preview teardown Vault login must consume the post-destroy identity token"
rg -q 'bash ./ci/vault-login\.sh' <<<"${refresh_vault_step}" \
  || fail "preview teardown must obtain a fresh Vault client token before namespace cleanup"
rg -q "if: inputs\.mode == 'destroy' && inputs\.pr_number != '' && steps\.destroy_preview\.outcome == 'success'" \
  <<<"${destroy_steps}" \
  || fail "preview teardown Vault token refresh must run only after successful destroy"
rg -q 'VAULT_ADMIN_TOKEN: \$\{\{ steps\.refresh_vault_access\.outputs\.access_token \}\}' \
  <<<"${destroy_steps}" \
  || fail "preview Vault cleanup must consume the post-destroy token"
if rg -q 'VAULT_ADMIN_TOKEN: \$\{\{ steps\.auth_vault_access\.outputs\.access_token \}\}' \
  <<<"${destroy_steps}"; then
  fail "preview Vault cleanup reuses the expiring pre-destroy token"
fi

record_job="$(sed -n '/^  record-preview-deployment:/,/^  preview-comment:/p' "${WORKFLOW}")"
[ -n "${record_job}" ] || fail "record-preview-deployment job is missing"

require '^    if: always\(\).+mode == '\''apply'\''.+mode == '\''destroy'\'''
require '^    needs: \[prepare, deploy, destroy\]$'
require '^      deployments: write$'
require '^      pull-requests: read$'
require 'uses: actions/github-script@[0-9a-f]{40}'
require 'HEAD_SHA: \$\{\{ needs\.prepare\.outputs\.head_sha \}\}'
require 'PREVIEW_URL: \$\{\{ needs\.deploy\.outputs\.preview_url \}\}'
require 'github\.rest\.pulls\.get'
require 'live\.head\.sha === headSha'
# shellcheck disable=SC2016 # Literal JavaScript template expression in an rg pattern.
require 'live\.head\.repo\?\.full_name === `\$\{owner\}/\$\{repo\}`'
require 'github\.rest\.repos\.createDeployment'
require 'ref: headSha'
require 'auto_merge: false'
require 'required_contexts: \[\]'
require "environment = 'preview'"
require 'transient_environment: true'
require 'production_environment: false'
require 'response\.data\.sha !== headSha'
require 'github\.rest\.repos\.createDeploymentStatus'
require "await postStatus\(deploymentId, 'in_progress'"
require "await postStatus\(deploymentId, terminalState"
require "let terminalState = 'failure'"
require "terminalState = 'success'"
require "terminalState = 'error'"
require "process\.env\.DEPLOY_RESULT === 'success' && process\.env\.DEPLOY_STATUS === 'success'"
require 'environmentUrl = validPreviewURL\(\)'
require "if \(terminalState !== 'success'\) core\.setFailed"
require "await postStatus\(deploymentId, 'error'"
require "await postStatus\(deployment\.id, 'inactive'"
require 'auto_inactive: false'
require 'parsed\.protocol !== '\''https:'\'''
require 'parsed\.hostname\.startsWith\(expectedPrefix\)'
require 'parsed\.hostname\.endsWith\('\''.run.app'\''\)'
require 'destroySucceeded'
# shellcheck disable=SC2016 # Literal JavaScript template expression in an rg pattern.
require 'await inactivate\(prior, `Preview PR #\$\{prNumber\} was destroyed`\)'
require 'if \(!isCurrentHead\)'
require 'Skipped stale preview evidence'

forbid 'actions/checkout@'
forbid 'ref:.*pull_request\.head'
forbid '^    environment: preview$'
forbid '^      contents:|packages: write|id-token: write|secrets\.|github\.event\.pull_request'

echo "Trusted exact-head preview deployment evidence contracts passed."
