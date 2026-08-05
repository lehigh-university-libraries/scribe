#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TEST_DIR="$(mktemp -d "${TMPDIR:-/tmp}/scribe-cloud-run-readiness-test.XXXXXX")"
trap 'rm -rf -- "$TEST_DIR"' EXIT
mkdir -p "$TEST_DIR/bin" "$TEST_DIR/artifacts" "$TEST_DIR/tmp"

fail() {
  echo "Cloud Run readiness helper contract failed: $*" >&2
  exit 1
}

cat >"$TEST_DIR/bin/gcloud" <<'FAKE_GCLOUD'
#!/usr/bin/env bash
set -euo pipefail

printf '%q ' "$@" >>"$MOCK_GCLOUD_LOG"
printf '\n' >>"$MOCK_GCLOUD_LOG"
mock_execution="${MOCK_EXECUTION:-scribe-prod-backend-readiness-abc12}"

if [[ "$1 $2 $3" == "run jobs execute" ]]; then
  case "${MOCK_GCLOUD_MODE:-failure}" in
    success) exit 0 ;;
    bad-hint)
      printf '%s\n' 'gcloud run jobs executions describe another-job-abc12' >&2
      exit 19
      ;;
    failure)
      printf '%s\n' 'CONTROL_PLANE_ERROR_SECRET_SENTINEL' >&2
      printf 'gcloud run jobs executions describe %s\n' "$mock_execution" >&2
      exit 19
      ;;
    *) exit 90 ;;
  esac
fi

if [[ "$1 $2 $3 $4" == "run jobs executions describe" ]]; then
  sed "s/__MOCK_EXECUTION__/$mock_execution/g" <<'JSON'
{
  "metadata": {
    "name": "__MOCK_EXECUTION__",
    "creationTimestamp": "2026-07-23T06:28:33.000Z",
    "annotations": {
      "credential": "hvs.EXECUTION_ANNOTATION_SECRET_SENTINEL"
    }
  },
  "status": {
    "startTime": "2026-07-23T06:28:41.000Z",
    "completionTime": "2026-07-23T06:32:40.000Z",
    "runningCount": 0,
    "succeededCount": 0,
    "failedCount": 1,
    "cancelledCount": 0,
    "retriedCount": 0,
    "conditions": [
      {
        "type": "Completed",
        "status": "False",
        "reason": "NonZeroExitCode",
        "message": "EXECUTION_MESSAGE_SECRET_SENTINEL"
      }
    ]
  },
  "template": {
    "containers": [
      {
        "env": [
          {
            "name": "PASSWORD",
            "value": "EXECUTION_ENV_SECRET_SENTINEL"
          }
        ]
      }
    ]
  }
}
JSON
  exit 0
fi

if [[ "$1 $2 $3 $4 $5" == "run jobs executions tasks list" ]]; then
  sed "s/__MOCK_EXECUTION__/$mock_execution/g" <<'JSON'
[
  {
    "metadata": {
      "name": "__MOCK_EXECUTION__-task0",
      "annotations": {
        "secret": "TASK_ANNOTATION_SECRET_SENTINEL"
      }
    },
    "status": {
      "index": 0,
      "retried": 0,
      "lastAttemptResult": {
        "exitCode": 23,
        "termSignal": 0,
        "status": {
          "code": 9,
          "message": "TASK_MESSAGE_SECRET_SENTINEL",
          "details": [
            {
              "token": "TASK_DETAIL_SECRET_SENTINEL"
            }
          ]
        }
      }
    },
    "containers": [
      {
        "env": [
          {
            "name": "JWT",
            "value": "TASK_ENV_SECRET_SENTINEL"
          }
        ]
      }
    ]
  }
]
JSON
  exit 0
fi

if [[ "$1 $2" == "logging read" ]]; then
  if [[ "${MOCK_LOG_DENIED:-false}" == true ]]; then
    printf '%s\n' 'hvs.LOG_QUERY_ERROR_SECRET_SENTINEL' >&2
    exit 7
  fi
  sed "s/__MOCK_EXECUTION__/$mock_execution/g" <<'JSON'
[
  {
    "labels": {
      "run.googleapis.com/execution_name": "__MOCK_EXECUTION__"
    },
    "textPayload": "frontend backend startup gate failed [Error; startup-deadline; transport-TypeError/ENOTFOUND]\n"
  },
  {
    "labels": {
      "run.googleapis.com/execution_name": "__MOCK_EXECUTION__"
    },
    "textPayload": "frontend readiness failed: HTTP 503 (invalid-json)\n"
  },
  {
    "labels": {
      "run.googleapis.com/execution_name": "__MOCK_EXECUTION__"
    },
    "textPayload": "frontend readiness failed: HTTP 200 (public-origin-mismatch)\n"
  },
  {
    "labels": {
      "run.googleapis.com/execution_name": "__MOCK_EXECUTION__"
    },
    "textPayload": "frontend backend startup gate failed [Error; readiness-contract; HTTP 200 (invalid-public-origin)]\n"
  },
  {
    "labels": {
      "run.googleapis.com/execution_name": "__MOCK_EXECUTION__"
    },
    "textPayload": "frontend proxy request failed [Error/EHOSTUNREACH]\n"
  },
  {
    "labels": {
      "run.googleapis.com/execution_name": "__MOCK_EXECUTION__"
    },
    "textPayload": "frontend backend network probe [dns-mismatch; tcp-open; http-ready]\n"
  },
  {
    "labels": {
      "run.googleapis.com/execution_name": "__MOCK_EXECUTION__"
    },
    "textPayload": "frontend backend network probe [dns-match; tcp-refused; http-transport-ECONNREFUSED]\n"
  },
  {
    "labels": {
      "run.googleapis.com/execution_name": "scribe-prod-backend-readiness-other"
    },
    "textPayload": "frontend readiness failed: HTTP 503 (invalid-json)\n"
  },
  {
    "labels": {
      "run.googleapis.com/execution_name": "__MOCK_EXECUTION__"
    },
    "textPayload": "password=LOG_PAYLOAD_SECRET_SENTINEL"
  },
  {
    "labels": {
      "run.googleapis.com/execution_name": "__MOCK_EXECUTION__"
    },
    "textPayload": "frontend readiness failed: HTTP 503 (invalid-json) LOG_TRAILING_SECRET_SENTINEL"
  },
  {
    "labels": {
      "run.googleapis.com/execution_name": "__MOCK_EXECUTION__"
    },
    "textPayload": "frontend backend network probe [dns-match; tcp-open; http-ready] NETWORK_LOG_TRAILING_SECRET_SENTINEL"
  },
  {
    "labels": {
      "run.googleapis.com/execution_name": "__MOCK_EXECUTION__"
    },
    "jsonPayload": {
      "message": "frontend readiness failed: HTTP 503 (invalid-json)",
      "token": "JSON_LOG_SECRET_SENTINEL"
    }
  },
  {
    "labels": {
      "run.googleapis.com/execution_name": "__MOCK_EXECUTION__"
    },
    "textPayload": "ocr readiness failed: image-contract\n"
  },
  {
    "labels": {
      "run.googleapis.com/execution_name": "__MOCK_EXECUTION__"
    },
    "textPayload": "ocr readiness failed: segment-token\n"
  },
  {
    "labels": {
      "run.googleapis.com/execution_name": "__MOCK_EXECUTION__"
    },
    "textPayload": "ocr readiness failed: segment-request\n"
  },
  {
    "labels": {
      "run.googleapis.com/execution_name": "__MOCK_EXECUTION__"
    },
    "textPayload": "ocr readiness failed: segment-contract\n"
  },
  {
    "labels": {
      "run.googleapis.com/execution_name": "__MOCK_EXECUTION__"
    },
    "textPayload": "ocr readiness failed: transcribe-token\n"
  },
  {
    "labels": {
      "run.googleapis.com/execution_name": "__MOCK_EXECUTION__"
    },
    "textPayload": "ocr readiness failed: transcribe-request\n"
  },
  {
    "labels": {
      "run.googleapis.com/execution_name": "__MOCK_EXECUTION__"
    },
    "textPayload": "ocr readiness failed: transcribe-timeout\n"
  },
  {
    "labels": {
      "run.googleapis.com/execution_name": "__MOCK_EXECUTION__"
    },
    "textPayload": "ocr readiness failed: transcribe-contract\n"
  },
  {
    "labels": {
      "run.googleapis.com/execution_name": "__MOCK_EXECUTION__"
    },
    "textPayload": "ocr readiness failed: ollama-token\n"
  },
  {
    "labels": {
      "run.googleapis.com/execution_name": "__MOCK_EXECUTION__"
    },
    "textPayload": "ocr readiness failed: ollama-request\n"
  },
  {
    "labels": {
      "run.googleapis.com/execution_name": "__MOCK_EXECUTION__"
    },
    "textPayload": "ocr readiness failed: ollama-contract\n"
  },
  {
    "labels": {
      "run.googleapis.com/execution_name": "__MOCK_EXECUTION__"
    },
    "textPayload": "ocr readiness failed: segment-request OCR_LOG_TRAILING_SECRET_SENTINEL\n"
  },
  {
    "labels": {
      "run.googleapis.com/execution_name": "__MOCK_EXECUTION__"
    },
    "textPayload": "browser readiness failed: home\n"
  },
  {
    "labels": {
      "run.googleapis.com/execution_name": "__MOCK_EXECUTION__"
    },
    "textPayload": "browser readiness failed: context\n"
  },
  {
    "labels": {
      "run.googleapis.com/execution_name": "__MOCK_EXECUTION__"
    },
    "textPayload": "browser readiness failed: upload\n"
  },
  {
    "labels": {
      "run.googleapis.com/execution_name": "__MOCK_EXECUTION__"
    },
    "textPayload": "browser readiness failed: handoff\n"
  },
  {
    "labels": {
      "run.googleapis.com/execution_name": "__MOCK_EXECUTION__"
    },
    "textPayload": "browser readiness failed: transcription\n"
  },
  {
    "labels": {
      "run.googleapis.com/execution_name": "__MOCK_EXECUTION__"
    },
    "textPayload": "browser readiness failed: annotations\n"
  },
  {
    "labels": {
      "run.googleapis.com/execution_name": "__MOCK_EXECUTION__"
    },
    "textPayload": "browser readiness failed: editor\n"
  },
  {
    "labels": {
      "run.googleapis.com/execution_name": "__MOCK_EXECUTION__"
    },
    "textPayload": "browser readiness failed: overlay\n"
  },
  {
    "labels": {
      "run.googleapis.com/execution_name": "__MOCK_EXECUTION__"
    },
    "textPayload": "browser readiness failed: retranscribe\n"
  },
  {
    "labels": {
      "run.googleapis.com/execution_name": "__MOCK_EXECUTION__"
    },
    "textPayload": "browser readiness failed: save\n"
  },
  {
    "labels": {
      "run.googleapis.com/execution_name": "__MOCK_EXECUTION__"
    },
    "textPayload": "browser readiness failed: publish\n"
  },
  {
    "labels": {
      "run.googleapis.com/execution_name": "__MOCK_EXECUTION__"
    },
    "textPayload": "browser readiness failed: responsive\n"
  },
  {
    "labels": {
      "run.googleapis.com/execution_name": "__MOCK_EXECUTION__"
    },
    "textPayload": "browser readiness failed: token\n"
  },
  {
    "labels": {
      "run.googleapis.com/execution_name": "__MOCK_EXECUTION__"
    },
    "textPayload": "browser readiness failed: cleanup\n"
  },
  {
    "labels": {
      "run.googleapis.com/execution_name": "__MOCK_EXECUTION__"
    },
    "textPayload": "browser readiness failed: network\n"
  },
  {
    "labels": {
      "run.googleapis.com/execution_name": "__MOCK_EXECUTION__"
    },
    "textPayload": "browser readiness failed: csp\n"
  },
  {
    "labels": {
      "run.googleapis.com/execution_name": "__MOCK_EXECUTION__"
    },
    "textPayload": "browser readiness failed: token BROWSER_LOG_TRAILING_SECRET_SENTINEL\n"
  }
]
JSON
  exit 0
fi

printf '%s\n' "unexpected fake gcloud invocation: $*" >&2
exit 99
FAKE_GCLOUD
chmod +x "$TEST_DIR/bin/gcloud"

export PATH="$TEST_DIR/bin:$PATH"
export TMPDIR="$TEST_DIR/tmp"
export GCLOUD_PROJECT=scribe-test
export SCRIBE_REGION=us-east5
export MOCK_GCLOUD_LOG="$TEST_DIR/gcloud.log"

run_helper() {
  local output_file="$1"
  shift
  set +e
  "$@" >"$TEST_DIR/console.out" 2>"$TEST_DIR/console.err"
  HELPER_STATUS=$?
  set -e
  [[ "$HELPER_STATUS" -ge 0 ]]
  [[ -z "$output_file" || -e "$output_file" || "$HELPER_STATUS" -eq 0 ]]
}

diagnostics="$TEST_DIR/artifacts/backend-readiness.log"
run_helper "$diagnostics" \
  env MOCK_GCLOUD_MODE=failure \
  "$ROOT_DIR/ci/run-cloud-run-readiness.sh" \
  scribe-prod-backend-readiness backend "$diagnostics"
[[ "$HELPER_STATUS" -eq 19 ]] ||
  fail "the helper did not preserve the Cloud Run execution status"

for expected in \
  'Scribe Cloud Run readiness diagnostics (typed fields and exact allowlisted markers only)' \
  '[readiness] kind=backend' \
  '[readiness] job=scribe-prod-backend-readiness' \
  '[readiness] execute_status=19' \
  '[readiness] execution=scribe-prod-backend-readiness-abc12' \
  '[execution] create_time=2026-07-23T06:28:33.000Z' \
  '[execution] start_time=2026-07-23T06:28:41.000Z' \
  '[execution] completion_time=2026-07-23T06:32:40.000Z' \
  '[execution] failed_count=1' \
  '[execution] state=failed' \
  '[execution] reason=non-zero-exit' \
  '[task] index=0 retried=0 exit_code=23 term_signal=0 status_code=9' \
  'frontend backend startup gate failed [Error; startup-deadline; transport-TypeError/ENOTFOUND]' \
  'frontend readiness failed: HTTP 503 (invalid-json)' \
  'frontend readiness failed: HTTP 200 (public-origin-mismatch)' \
  'frontend backend startup gate failed [Error; readiness-contract; HTTP 200 (invalid-public-origin)]' \
  'frontend proxy request failed [Error/EHOSTUNREACH]' \
  'frontend backend network probe [dns-mismatch; tcp-open; http-ready]' \
  'frontend backend network probe [dns-match; tcp-refused; http-transport-ECONNREFUSED]' \
  '[status] log_query=ok markers=7'; do
  grep -Fq "$expected" "$diagnostics" ||
    fail "diagnostics omitted: $expected"
done

[[ "$(stat -c '%a' "$diagnostics")" == 600 ]] ||
  fail "diagnostics are not owner-only"

secret_pattern='CONTROL_PLANE_ERROR_SECRET_SENTINEL|EXECUTION_(ANNOTATION|MESSAGE|ENV)_SECRET_SENTINEL|TASK_(ANNOTATION|MESSAGE|DETAIL|ENV)_SECRET_SENTINEL|LOG_(PAYLOAD|TRAILING)_SECRET_SENTINEL|NETWORK_LOG_TRAILING_SECRET_SENTINEL|OCR_LOG_TRAILING_SECRET_SENTINEL|BROWSER_LOG_TRAILING_SECRET_SENTINEL|JSON_LOG_SECRET_SENTINEL|hvs\.'
if rg -n "$secret_pattern" "$diagnostics" "$TEST_DIR/console.out" "$TEST_DIR/console.err" >/dev/null; then
  fail "raw execution, task, log, or command diagnostics escaped the bounded renderer"
fi

grep -Fq 'run jobs executions describe scribe-prod-backend-readiness-abc12' "$MOCK_GCLOUD_LOG"
grep -Fq 'run jobs executions tasks list --execution scribe-prod-backend-readiness-abc12' "$MOCK_GCLOUD_LOG"
grep -Fq 'resource.labels.job_name=\"scribe-prod-backend-readiness\"' "$MOCK_GCLOUD_LOG"
grep -Fq 'resource.labels.location=\"us-east5\"' "$MOCK_GCLOUD_LOG"
grep -Fq 'labels.\"run.googleapis.com/execution_name\"=\"scribe-prod-backend-readiness-abc12\"' "$MOCK_GCLOUD_LOG"

: >"$MOCK_GCLOUD_LOG"
ocr_diagnostics="$TEST_DIR/artifacts/ocr-readiness.log"
run_helper "$ocr_diagnostics" \
  env \
  MOCK_GCLOUD_MODE=failure \
  MOCK_EXECUTION=scribe-prod-ocr-readiness-def34 \
  "$ROOT_DIR/ci/run-cloud-run-readiness.sh" \
  scribe-prod-ocr-readiness ocr "$ocr_diagnostics"
[[ "$HELPER_STATUS" -eq 19 ]] ||
  fail "the OCR helper did not preserve the Cloud Run execution status"
for expected in \
  '[readiness] kind=ocr' \
  '[readiness] job=scribe-prod-ocr-readiness' \
  '[readiness] execution=scribe-prod-ocr-readiness-def34' \
  'ocr readiness failed: image-contract' \
  'ocr readiness failed: segment-token' \
  'ocr readiness failed: segment-request' \
  'ocr readiness failed: segment-contract' \
  'ocr readiness failed: transcribe-token' \
  'ocr readiness failed: transcribe-request' \
  'ocr readiness failed: transcribe-timeout' \
  'ocr readiness failed: transcribe-contract' \
  'ocr readiness failed: ollama-token' \
  'ocr readiness failed: ollama-request' \
  'ocr readiness failed: ollama-contract' \
  '[status] log_query=ok markers=11'; do
  grep -Fq "$expected" "$ocr_diagnostics" ||
    fail "OCR diagnostics omitted: $expected"
done
if rg -n \
  'frontend readiness failed|frontend backend|frontend proxy|OCR_LOG_TRAILING_SECRET_SENTINEL' \
  "$ocr_diagnostics" "$TEST_DIR/console.out" "$TEST_DIR/console.err" >/dev/null; then
  fail "OCR diagnostics accepted a marker from another readiness kind or trailing content"
fi
grep -Fq 'resource.labels.job_name=\"scribe-prod-ocr-readiness\"' "$MOCK_GCLOUD_LOG"
grep -Fq 'labels.\"run.googleapis.com/execution_name\"=\"scribe-prod-ocr-readiness-def34\"' "$MOCK_GCLOUD_LOG"

: >"$MOCK_GCLOUD_LOG"
browser_diagnostics="$TEST_DIR/artifacts/browser-readiness.log"
run_helper "$browser_diagnostics" \
  env \
  MOCK_GCLOUD_MODE=failure \
  MOCK_EXECUTION=scribe-pr-42-browser-deadbeef-ghi56 \
  "$ROOT_DIR/ci/run-cloud-run-readiness.sh" \
  scribe-pr-42-browser-deadbeef browser "$browser_diagnostics"
[[ "$HELPER_STATUS" -eq 19 ]] ||
  fail "the browser helper did not preserve the Cloud Run execution status"
for expected in \
  '[readiness] kind=browser' \
  '[readiness] job=scribe-pr-42-browser-deadbeef' \
  '[readiness] execution=scribe-pr-42-browser-deadbeef-ghi56' \
  'browser readiness failed: home' \
  'browser readiness failed: context' \
  'browser readiness failed: upload' \
  'browser readiness failed: handoff' \
  'browser readiness failed: transcription' \
  'browser readiness failed: annotations' \
  'browser readiness failed: editor' \
  'browser readiness failed: overlay' \
  'browser readiness failed: retranscribe' \
  'browser readiness failed: save' \
  'browser readiness failed: publish' \
  'browser readiness failed: responsive' \
  'browser readiness failed: token' \
  'browser readiness failed: cleanup' \
  'browser readiness failed: network' \
  'browser readiness failed: csp' \
  '[status] log_query=ok markers=16'; do
  grep -Fq "$expected" "$browser_diagnostics" ||
    fail "browser diagnostics omitted: $expected"
done
if rg -n \
  'frontend readiness failed|frontend backend|frontend proxy|ocr readiness failed|BROWSER_LOG_TRAILING_SECRET_SENTINEL' \
  "$browser_diagnostics" "$TEST_DIR/console.out" "$TEST_DIR/console.err" >/dev/null; then
  fail "browser diagnostics accepted a marker from another readiness kind or trailing content"
fi
if rg -n "$secret_pattern" \
  "$browser_diagnostics" "$TEST_DIR/console.out" "$TEST_DIR/console.err" >/dev/null; then
  fail "browser diagnostics exposed raw execution, task, log, or command content"
fi
grep -Fq 'resource.labels.job_name=\"scribe-pr-42-browser-deadbeef\"' "$MOCK_GCLOUD_LOG"
grep -Fq 'labels.\"run.googleapis.com/execution_name\"=\"scribe-pr-42-browser-deadbeef-ghi56\"' "$MOCK_GCLOUD_LOG"

denied_diagnostics="$TEST_DIR/artifacts/backend-readiness-denied.log"
run_helper "$denied_diagnostics" \
  env MOCK_GCLOUD_MODE=failure MOCK_LOG_DENIED=true \
  "$ROOT_DIR/ci/run-cloud-run-readiness.sh" \
  scribe-prod-backend-readiness backend "$denied_diagnostics"
[[ "$HELPER_STATUS" -eq 19 ]] ||
  fail "a denied optional log query masked the readiness failure"
grep -Fq '[status] log_query=unavailable' "$denied_diagnostics"
if rg -n 'LOG_QUERY_ERROR_SECRET_SENTINEL|hvs\.' \
  "$denied_diagnostics" "$TEST_DIR/console.out" "$TEST_DIR/console.err" >/dev/null; then
  fail "a denied log query exposed its raw error"
fi

: >"$MOCK_GCLOUD_LOG"
bad_hint_diagnostics="$TEST_DIR/artifacts/backend-readiness-bad-hint.log"
run_helper "$bad_hint_diagnostics" \
  env MOCK_GCLOUD_MODE=bad-hint \
  "$ROOT_DIR/ci/run-cloud-run-readiness.sh" \
  scribe-prod-backend-readiness backend "$bad_hint_diagnostics"
[[ "$HELPER_STATUS" -eq 19 ]] ||
  fail "an untrusted execution hint changed the readiness status"
grep -Fq '[status] execution_identity=unavailable' "$bad_hint_diagnostics"
[[ "$(wc -l <"$MOCK_GCLOUD_LOG")" -eq 1 ]] ||
  fail "the helper queried an execution that was not bound to the requested job"

: >"$MOCK_GCLOUD_LOG"
success_diagnostics="$TEST_DIR/artifacts/backend-readiness-success.log"
run_helper "$success_diagnostics" \
  env MOCK_GCLOUD_MODE=success \
  "$ROOT_DIR/ci/run-cloud-run-readiness.sh" \
  scribe-prod-backend-readiness backend "$success_diagnostics"
[[ "$HELPER_STATUS" -eq 0 ]] ||
  fail "a successful readiness execution failed"
[[ ! -e "$success_diagnostics" ]] ||
  fail "a successful readiness execution emitted a failure artifact"
[[ "$(wc -l <"$MOCK_GCLOUD_LOG")" -eq 1 ]] ||
  fail "a successful readiness execution made diagnostic queries"

: >"$MOCK_GCLOUD_LOG"
run_helper "" \
  "$ROOT_DIR/ci/run-cloud-run-readiness.sh" \
  another-job backend "$TEST_DIR/artifacts/invalid.log"
[[ "$HELPER_STATUS" -eq 2 ]] ||
  fail "an invalid readiness job identity was accepted"
[[ ! -s "$MOCK_GCLOUD_LOG" ]] ||
  fail "invalid input reached gcloud"

echo "Cloud Run readiness execution, task, and log diagnostics are exact-scoped and redact raw cloud payloads."
