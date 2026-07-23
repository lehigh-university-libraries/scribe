#!/usr/bin/env bash

set -euo pipefail

: "${GITHUB_REPOSITORY:?GITHUB_REPOSITORY is required}"
: "${RELEASE_SHA:?RELEASE_SHA is required}"
: "${GH_TOKEN:?GH_TOKEN is required}"

if [[ ! "${GITHUB_REPOSITORY}" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]]; then
  echo "Release source verification requires an exact GitHub repository name." >&2
  exit 2
fi
if [[ ! "${RELEASE_SHA}" =~ ^[0-9a-f]{40}$ ]]; then
  echo "Release source verification requires a full lowercase commit SHA." >&2
  exit 2
fi

gh_bin="${GH_BIN:-gh}"
max_attempts="${RELEASE_RUN_MAX_ATTEMPTS:-361}"
poll_seconds="${RELEASE_RUN_POLL_SECONDS:-30}"
if [[ ! "${max_attempts}" =~ ^[1-9][0-9]*$ ]] ||
  [[ ! "${poll_seconds}" =~ ^[0-9]+$ ]]; then
  echo "Release run polling limits must be nonnegative integers with at least one attempt." >&2
  exit 2
fi

workflow_endpoint="repos/${GITHUB_REPOSITORY}/actions/workflows/terraform-apply.yaml/runs"
run=""
attempt=1
while [ "${attempt}" -le "${max_attempts}" ]; do
  runs="$(
    "${gh_bin}" api --method GET "${workflow_endpoint}" \
      -f branch=main \
      -f event=push \
      -f head_sha="${RELEASE_SHA}" \
      -f per_page=100
  )"
  if ! jq -e '
    (.total_count | type == "number") and
    (.workflow_runs | type == "array") and
    .total_count <= (.workflow_runs | length)
  ' <<<"${runs}" >/dev/null; then
    echo "GitHub returned an incomplete or malformed Terraform Apply run listing." >&2
    exit 1
  fi

  run="$(
    jq -c --arg sha "${RELEASE_SHA}" '
      [
        .workflow_runs[]
        | select(
            .head_sha == $sha and
            .head_branch == "main" and
            .event == "push" and
            (.id | type == "number") and
            (.run_attempt | type == "number")
          )
      ]
      | sort_by(.id, .run_attempt)
      | last // empty
    ' <<<"${runs}"
  )"

  if [ -n "${run}" ]; then
    status="$(jq -r '.status // ""' <<<"${run}")"
    conclusion="$(jq -r '.conclusion // ""' <<<"${run}")"
    case "${status}" in
      completed)
        if [ "${conclusion}" != "success" ]; then
          echo "The exact release source Terraform Apply run completed with ${conclusion:-no conclusion}." >&2
          exit 1
        fi
        break
        ;;
      queued | in_progress | pending | requested | waiting)
        ;;
      *)
        echo "The exact release source Terraform Apply run has an unexpected status." >&2
        exit 1
        ;;
    esac
  fi

  if [ "${attempt}" -eq "${max_attempts}" ]; then
    echo "No successful exact-source Terraform Apply run became available before the release deadline." >&2
    exit 1
  fi
  if [ "${poll_seconds}" -gt 0 ]; then
    sleep "${poll_seconds}"
  fi
  attempt=$((attempt + 1))
done

run_id="$(jq -r '.id' <<<"${run}")"
jobs="$(
  "${gh_bin}" api --method GET \
    "repos/${GITHUB_REPOSITORY}/actions/runs/${run_id}/jobs" \
    -f filter=latest \
    -f per_page=100
)"
if ! jq -e '
  (.total_count | type == "number") and
  (.jobs | type == "array") and
  .total_count == (.jobs | length) and
  ([.jobs[] | select(.name == "terraform-apply / deploy")] | length) == 1 and
  all(
    .jobs[] | select(.name == "terraform-apply / deploy");
    .status == "completed" and .conclusion == "success"
  ) and
  ([.jobs[] | select(.name | startswith("lint-test / "))] | length) > 0 and
  all(
    .jobs[] | select(.name | startswith("lint-test / "));
    .status == "completed" and .conclusion == "success"
  )
' <<<"${jobs}" >/dev/null; then
  echo "The exact-source run did not complete both repository CI and the protected production deployment." >&2
  exit 1
fi

echo "Exact release source passed repository CI and the protected production deployment."
