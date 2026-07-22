#!/usr/bin/env bash

set -euo pipefail

: "${GITHUB_OUTPUT:?GITHUB_OUTPUT is required}"
: "${GITHUB_REPOSITORY:?GITHUB_REPOSITORY is required}"
: "${GCLOUD_PROJECT:?GitHub Actions variable GCLOUD_PROJECT must be set}"
: "${SCRIBE_REGION:?SCRIBE_REGION is required}"
: "${SCRIBE_ZONE:?SCRIBE_ZONE is required}"

# Never derive the privileged Terraform checkout from pull-request base data.
# A PR can be retargeted from main to another branch, making base.sha attacker
# controlled while the event still needs permission to destroy the old preview.
trusted_main_sha="$(gh api "repos/${GITHUB_REPOSITORY}/commits/main" --jq '.sha')"
[[ "${trusted_main_sha}" =~ ^[0-9a-f]{40}$ ]] || {
  echo "GitHub returned an invalid protected-main SHA" >&2
  exit 1
}

if [ -n "${EVENT_PR:-}" ]; then
  pr_number="${EVENT_PR}"
  head_sha="${EVENT_HEAD_SHA:-}"
  if [ "${EVENT_HEAD_REPO:-}" != "${GITHUB_REPOSITORY}" ]; then
    echo "Fork pull request detected; skipping credentialed preview deployment."
    mode="skip"
  elif [ "${EVENT_ACTION:-}" = "closed" ] && {
    [ "${EVENT_BASE_REF:-}" = "main" ] || [ "${EVENT_PREVIOUS_BASE_REF:-}" = "main" ]
  }; then
    mode="destroy"
  elif [ "${EVENT_BASE_REF:-}" = "main" ]; then
    mode="apply"
  elif [ "${EVENT_PREVIOUS_BASE_REF:-}" = "main" ]; then
    echo "Pull request was retargeted away from main; destroying its preview."
    mode="destroy"
  else
    echo "Pull request does not target main; no preview is required."
    mode="skip"
  fi
else
  if [ "${WORKFLOW_REF:-}" != "refs/heads/main" ]; then
    echo "Preview dispatches must run from the protected main branch." >&2
    exit 1
  fi
  pr_number="${DISPATCH_PR:-}"
  [[ "${pr_number}" =~ ^[0-9]+$ ]] || {
    echo "pr_number must be numeric" >&2
    exit 1
  }
  pr_json="$(gh api "repos/${GITHUB_REPOSITORY}/pulls/${pr_number}")"
  head_repo="$(jq -r '.head.repo.full_name' <<<"${pr_json}")"
  if [ "${head_repo}" != "${GITHUB_REPOSITORY}" ]; then
    echo "Fork pull requests cannot be deployed with repository credentials." >&2
    exit 1
  fi
  if [ "${DISPATCH_ACTION:-}" = "deploy" ] && [ "$(jq -r '.base.ref' <<<"${pr_json}")" != "main" ]; then
    echo "Only pull requests targeting main can create previews." >&2
    exit 1
  fi
  head_sha="$(jq -r '.head.sha' <<<"${pr_json}")"
  if [ "${DISPATCH_ACTION:-}" = "deploy" ]; then
    mode="apply"
  else
    mode="destroy"
  fi
fi

[[ "${head_sha}" =~ ^[0-9a-f]{40}$ ]] || {
  echo "invalid PR head SHA" >&2
  exit 1
}
[[ "${pr_number}" =~ ^[0-9]+$ ]] || {
  echo "invalid PR number" >&2
  exit 1
}

tag="pr-${pr_number}"
gar_repo="us-docker.pkg.dev/${GCLOUD_PROJECT}/internal"
{
  echo "pr_number=${pr_number}"
  echo "head_sha=${head_sha}"
  echo "base_sha=${trusted_main_sha}"
  echo "mode=${mode}"
  echo "region=${SCRIBE_REGION}"
  echo "tag=${tag}"
  echo "zone=${SCRIBE_ZONE}"
  echo "image_tag=ghcr.io/lehigh-university-libraries/scribe:${tag}"
  echo "frontend_image_tag=ghcr.io/lehigh-university-libraries/scribe-frontend:${tag}"
  echo "frontend_gar_image_tag=${gar_repo}/scribe-frontend:${tag}"
  echo "backend_origin=http://scribe-pr-${pr_number}.${SCRIBE_ZONE}.c.${GCLOUD_PROJECT}.internal"
} >>"${GITHUB_OUTPUT}"
