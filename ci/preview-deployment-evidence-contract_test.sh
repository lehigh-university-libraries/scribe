#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORKFLOW="${ROOT_DIR}/.github/workflows/terraform-preview.yaml"

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
