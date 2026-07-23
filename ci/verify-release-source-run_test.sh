#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
test_root="$(mktemp -d)"
trap 'rm -rf "${test_root}"' EXIT

release_sha="1111111111111111111111111111111111111111"
other_sha="2222222222222222222222222222222222222222"

cat >"${test_root}/gh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

endpoint=""
for argument in "$@"; do
  case "${argument}" in
    repos/*/actions/workflows/*/runs | repos/*/actions/runs/*/jobs)
      endpoint="${argument}"
      ;;
  esac
done

case "${endpoint}" in
  repos/*/actions/workflows/*/runs)
    count=0
    if [ -f "${FAKE_GH_STATE}" ]; then
      count="$(cat "${FAKE_GH_STATE}")"
    fi
    count=$((count + 1))
    printf '%s\n' "${count}" >"${FAKE_GH_STATE}"
    if [ "${count}" -gt 1 ] && [ -n "${FAKE_RUNS_SECOND:-}" ]; then
      cat "${FAKE_RUNS_SECOND}"
    else
      cat "${FAKE_RUNS_FIRST}"
    fi
    ;;
  repos/*/actions/runs/*/jobs)
    cat "${FAKE_JOBS}"
    ;;
  *)
    echo "unexpected fake gh invocation: $*" >&2
    exit 2
    ;;
esac
EOF
chmod +x "${test_root}/gh"

run_payload() {
  local output="$1" sha="$2" status="$3" conclusion="$4" id="${5:-100}"
  jq -n \
    --arg sha "${sha}" \
    --arg status "${status}" \
    --arg conclusion "${conclusion}" \
    --argjson id "${id}" \
    '{
      total_count: 1,
      workflow_runs: [{
        id: $id,
        run_attempt: 1,
        head_sha: $sha,
        head_branch: "main",
        event: "push",
        status: $status,
        conclusion: (if $conclusion == "" then null else $conclusion end)
      }]
    }' >"${output}"
}

jobs_payload() {
  local output="$1" deploy_conclusion="${2:-success}" lint_conclusion="${3:-success}"
  jq -n \
    --arg deploy "${deploy_conclusion}" \
    --arg lint "${lint_conclusion}" \
    '{
      total_count: 3,
      jobs: [
        {name: "lint-test / contracts-and-lint", status: "completed", conclusion: $lint},
        {name: "lint-test / security", status: "completed", conclusion: $lint},
        {name: "terraform-apply / deploy", status: "completed", conclusion: $deploy}
      ]
    }' >"${output}"
}

verify() {
  local state="$1"
  shift
  GH_BIN="${test_root}/gh" \
    GH_TOKEN=test-token \
    GITHUB_REPOSITORY=lehigh-university-libraries/scribe \
    RELEASE_SHA="${release_sha}" \
    RELEASE_RUN_MAX_ATTEMPTS=2 \
    RELEASE_RUN_POLL_SECONDS=0 \
    FAKE_GH_STATE="${state}" \
    "$@" \
    "${repo_root}/ci/verify-release-source-run.sh"
}

expect_failure() {
  local label="$1"
  shift
  if verify "${test_root}/${label}.state" "$@" >/dev/null 2>&1; then
    echo "release source verification accepted ${label}" >&2
    exit 1
  fi
}

success_runs="${test_root}/runs-success.json"
success_jobs="${test_root}/jobs-success.json"
run_payload "${success_runs}" "${release_sha}" completed success
jobs_payload "${success_jobs}"
verify "${test_root}/success.state" \
  env FAKE_RUNS_FIRST="${success_runs}" FAKE_JOBS="${success_jobs}" >/dev/null

queued_runs="${test_root}/runs-queued.json"
run_payload "${queued_runs}" "${release_sha}" queued ""
verify "${test_root}/queued.state" \
  env FAKE_RUNS_FIRST="${queued_runs}" FAKE_RUNS_SECOND="${success_runs}" \
  FAKE_JOBS="${success_jobs}" >/dev/null

missing_runs="${test_root}/runs-missing.json"
printf '%s\n' '{"total_count":0,"workflow_runs":[]}' >"${missing_runs}"
expect_failure missing \
  env FAKE_RUNS_FIRST="${missing_runs}" FAKE_JOBS="${success_jobs}"

failed_runs="${test_root}/runs-failed.json"
run_payload "${failed_runs}" "${release_sha}" completed failure
expect_failure failed \
  env FAKE_RUNS_FIRST="${failed_runs}" FAKE_JOBS="${success_jobs}"

wrong_runs="${test_root}/runs-wrong-sha.json"
run_payload "${wrong_runs}" "${other_sha}" completed success
expect_failure wrong-sha \
  env FAKE_RUNS_FIRST="${wrong_runs}" FAKE_JOBS="${success_jobs}"

skipped_jobs="${test_root}/jobs-skipped.json"
jobs_payload "${skipped_jobs}" skipped success
expect_failure skipped-deploy \
  env FAKE_RUNS_FIRST="${success_runs}" FAKE_JOBS="${skipped_jobs}"

failed_lint_jobs="${test_root}/jobs-failed-lint.json"
jobs_payload "${failed_lint_jobs}" success failure
expect_failure failed-lint \
  env FAKE_RUNS_FIRST="${success_runs}" FAKE_JOBS="${failed_lint_jobs}"

newer_runs="${test_root}/runs-newer.json"
jq -n --arg sha "${release_sha}" '{
  total_count: 2,
  workflow_runs: [
    {
      id: 100,
      run_attempt: 1,
      head_sha: $sha,
      head_branch: "main",
      event: "push",
      status: "completed",
      conclusion: "failure"
    },
    {
      id: 101,
      run_attempt: 1,
      head_sha: $sha,
      head_branch: "main",
      event: "push",
      status: "completed",
      conclusion: "success"
    }
  ]
}' >"${newer_runs}"
verify "${test_root}/newer.state" \
  env FAKE_RUNS_FIRST="${newer_runs}" FAKE_JOBS="${success_jobs}" >/dev/null

echo "Release source verification requires exact-SHA CI and production success."
