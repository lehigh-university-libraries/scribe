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
for mock_arg in "$@"; do
  if [[ "$mock_arg" == --update-env-vars=*SCRIBE_READINESS_EXECUTION_ID=* ]]; then
    mock_overrides="${mock_arg#--update-env-vars=}"
    IFS=',' read -ra mock_override_pairs <<<"$mock_overrides"
    for mock_pair in "${mock_override_pairs[@]}"; do
      if [[ "$mock_pair" == SCRIBE_READINESS_EXECUTION_ID=* ]]; then
        mock_run_id="${mock_pair#SCRIBE_READINESS_EXECUTION_ID=}"
        if [[ -n "${MOCK_CAPTURED_RUN_ID_FILE:-}" ]]; then
          printf '%s\n' "$mock_run_id" >"$MOCK_CAPTURED_RUN_ID_FILE"
        fi
      fi
    done
  fi
done

if [[ "$1 $2 $3" == "run jobs execute" ]]; then
  [[ -n "${MOCK_LAUNCH_MARKER_FILE:-}" ]] && : >"$MOCK_LAUNCH_MARKER_FILE"
  case "${MOCK_GCLOUD_MODE:-failure}" in
    success|failure|running|cancelling|query-failure|query-failure-cancel-describe-persistent)
      printf '%s\n' "$mock_execution"
      exit 0
      ;;
    empty-success)
      exit 0
      ;;
    hung-launch)
      if [[ "${SCRIBE_FAKE_TIMEOUT_WRAPPED:-false}" != true ]]; then
        exit 88
      fi
      trap '' TERM
      /bin/sleep 60
      exit 89
      ;;
    numeric-resource-success)
      printf 'projects/123456789012/locations/us-east5/jobs/scribe-prod-backend-readiness/executions/%s\n' \
        "$mock_execution"
      exit 0
      ;;
    bad-hint)
      printf '%s\n' 'another-job-abc12'
      exit 0
      ;;
    launch-failure|preflight-launch-failure)
      printf '%s\n' 'CONTROL_PLANE_ERROR_SECRET_SENTINEL' >&2
      exit 19
      ;;
    launch-signal)
      mock_helper_pid="$(ps -o ppid= -p "$PPID" | tr -d ' ')"
      kill -TERM "$mock_helper_pid"
      exit 143
      ;;
    *) exit 90 ;;
  esac
fi

if [[ "$1 $2 $3 $4" == "run jobs executions list" ]]; then
  if [[ ! -e "${MOCK_LAUNCH_MARKER_FILE:-/nonexistent}" ]]; then
    printf '%s\n' '[]'
    exit 0
  fi
  if [[ -e "${MOCK_CANCEL_MARKER:-/nonexistent}" ]]; then
    printf '%s\n' '[]'
    exit 0
  fi
  if [[ "${MOCK_NATURAL_TERMINAL_AFTER_DESCRIBES:-}" =~ ^[1-9][0-9]*$ \
    && -e "${MOCK_NATURAL_DESCRIBE_COUNT_FILE:-/nonexistent}" \
    && "$(<"$MOCK_NATURAL_DESCRIBE_COUNT_FILE")" -ge "$MOCK_NATURAL_TERMINAL_AFTER_DESCRIBES" ]]; then
    printf '%s\n' '[]'
    exit 0
  fi
  printf '[{"metadata":{"name":"%s"}}]\n' "$mock_execution"
  exit 0
fi

if [[ "$1 $2 $3 $4" == "run jobs executions describe" ]]; then
  if [[ ("${MOCK_GCLOUD_MODE:-failure}" == "query-failure" \
      || "${MOCK_GCLOUD_MODE:-failure}" == "query-failure-cancel-describe-persistent") \
    && ! -e "${MOCK_CANCEL_MARKER:-/nonexistent}" \
    && !( -e "${MOCK_CANCEL_ATTEMPT_MARKER:-/nonexistent}" \
      && "${MOCK_TERMINAL_AFTER_CANCEL_ATTEMPT:-false}" == true ) ]]; then
    printf '%s\n' 'DESCRIBE_ERROR_SECRET_SENTINEL' >&2
    exit 7
  fi
  if [[ "${MOCK_GCLOUD_MODE:-failure}" == "query-failure-cancel-describe-persistent" \
    && -e "${MOCK_CANCEL_MARKER:-/nonexistent}" ]]; then
    printf '%s\n' 'CANCEL_DESCRIBE_ERROR_SECRET_SENTINEL' >&2
    exit 8
  fi
  if [[ "${MOCK_GCLOUD_MODE:-failure}" == "cancelling" ]]; then
    mock_cancelling_count=0
    if [[ -e "${MOCK_CANCELLING_COUNT_FILE:-/nonexistent}" ]]; then
      mock_cancelling_count="$(<"$MOCK_CANCELLING_COUNT_FILE")"
    fi
    if ((mock_cancelling_count == 0)); then
      printf '%s\n' 1 >"$MOCK_CANCELLING_COUNT_FILE"
      sed "s/__MOCK_EXECUTION__/$mock_execution/g" <<'JSON'
{
  "metadata": {"name": "__MOCK_EXECUTION__"},
  "status": {
    "runningCount": 1,
    "conditions": [
      {"type": "Completed", "state": "CONDITION_FAILED", "reason": "CANCELLING"}
    ]
  }
}
JSON
      exit 0
    fi
    sed "s/__MOCK_EXECUTION__/$mock_execution/g" <<'JSON'
{
  "metadata": {"name": "__MOCK_EXECUTION__"},
  "status": {
    "cancelledCount": 1,
    "conditions": [
      {"type": "Completed", "state": "CONDITION_FAILED", "reason": "CANCELLED"}
    ]
  }
}
JSON
    exit 0
  fi
  if [[ -e "${MOCK_CANCEL_MARKER:-/nonexistent}" ]]; then
    if [[ "${MOCK_CANCEL_DESCRIBE_FAILURES:-0}" =~ ^[0-9]+$ \
      && "${MOCK_CANCEL_DESCRIBE_FAILURES:-0}" -gt 0 ]]; then
      mock_cancel_describe_count=0
      if [[ -e "${MOCK_CANCEL_DESCRIBE_COUNT_FILE:-/nonexistent}" ]]; then
        mock_cancel_describe_count="$(<"$MOCK_CANCEL_DESCRIBE_COUNT_FILE")"
      fi
      if ((mock_cancel_describe_count < MOCK_CANCEL_DESCRIBE_FAILURES)); then
        printf '%s\n' "$((mock_cancel_describe_count + 1))" \
          >"$MOCK_CANCEL_DESCRIBE_COUNT_FILE"
        printf '%s\n' 'CANCEL_DESCRIBE_ERROR_SECRET_SENTINEL' >&2
        exit 8
      fi
    fi
    sed "s/__MOCK_EXECUTION__/$mock_execution/g" <<'JSON'
{
  "metadata": {"name": "__MOCK_EXECUTION__"},
  "status": {
    "runningCount": 0,
    "succeededCount": 0,
    "failedCount": 0,
    "cancelledCount": 1,
    "retriedCount": 0,
    "conditions": [
      {"type": "Completed", "state": "CONDITION_FAILED", "reason": "Cancelled"}
    ]
  }
}
JSON
    exit 0
  fi
  if [[ -e "${MOCK_CANCEL_ATTEMPT_MARKER:-/nonexistent}" \
    && "${MOCK_TERMINAL_AFTER_CANCEL_ATTEMPT:-false}" == true ]]; then
    sed "s/__MOCK_EXECUTION__/$mock_execution/g" <<'JSON'
{
  "metadata": {"name": "__MOCK_EXECUTION__"},
  "status": {
    "runningCount": 0,
    "succeededCount": 0,
    "failedCount": 0,
    "cancelledCount": 1,
    "retriedCount": 0,
    "conditions": [
      {"type": "Completed", "state": "CONDITION_FAILED", "reason": "Cancelled"}
    ]
  }
}
JSON
    exit 0
  fi
  if [[ "${MOCK_NATURAL_TERMINAL_AFTER_DESCRIBES:-}" =~ ^[1-9][0-9]*$ ]]; then
    mock_natural_describe_count=0
    if [[ -e "${MOCK_NATURAL_DESCRIBE_COUNT_FILE:-/nonexistent}" ]]; then
      mock_natural_describe_count="$(<"$MOCK_NATURAL_DESCRIBE_COUNT_FILE")"
    fi
    if ((mock_natural_describe_count >= MOCK_NATURAL_TERMINAL_AFTER_DESCRIBES)); then
      sed "s/__MOCK_EXECUTION__/$mock_execution/g" <<'JSON'
{
  "metadata": {"name": "__MOCK_EXECUTION__"},
  "status": {
    "completionTime": "2026-07-23T06:32:40.000Z",
    "runningCount": 0,
    "succeededCount": 1,
    "failedCount": 0,
    "cancelledCount": 0,
    "retriedCount": 0,
    "conditions": [
      {"type": "Completed", "state": "CONDITION_SUCCEEDED"}
    ]
  }
}
JSON
      exit 0
    fi
    printf '%s\n' "$((mock_natural_describe_count + 1))" \
      >"$MOCK_NATURAL_DESCRIBE_COUNT_FILE"
  fi
  if [[ "${MOCK_GCLOUD_MODE:-failure}" == "launch-signal" \
    || "${MOCK_GCLOUD_MODE:-failure}" == "hung-launch" \
    || "${MOCK_GCLOUD_MODE:-failure}" == "numeric-resource-success" ]]; then
    mock_run_id="$(<"$MOCK_CAPTURED_RUN_ID_FILE")"
    sed \
      -e "s/__MOCK_EXECUTION__/$mock_execution/g" \
      -e "s/__MOCK_RUN_ID__/$mock_run_id/g" <<'JSON'
{
  "metadata": {"name": "__MOCK_EXECUTION__"},
  "spec": {
    "template": {
      "spec": {
        "containers": [
          {"env": [{"name": "SCRIBE_READINESS_EXECUTION_ID", "value": "__MOCK_RUN_ID__"}]}
        ]
      }
    }
  },
  "status": {
    "runningCount": 1,
    "conditions": [
      {"type": "Completed", "state": "CONDITION_RECONCILING"}
    ]
  }
}
JSON
    exit 0
  fi
  if [[ "${MOCK_GCLOUD_MODE:-failure}" == "running" \
    || "${MOCK_GCLOUD_MODE:-failure}" == "preflight-launch-failure" ]]; then
    sed "s/__MOCK_EXECUTION__/$mock_execution/g" <<'JSON'
{
  "metadata": {"name": "__MOCK_EXECUTION__"},
  "status": {
    "runningCount": 1,
    "succeededCount": 0,
    "failedCount": 0,
    "cancelledCount": 0,
    "retriedCount": 0,
    "conditions": [
      {"type": "Completed", "state": "CONDITION_RECONCILING"}
    ]
  }
}
JSON
    exit 0
  fi
  if [[ "${MOCK_GCLOUD_MODE:-failure}" == "success" ]]; then
    sed "s/__MOCK_EXECUTION__/$mock_execution/g" <<'JSON'
{
  "metadata": {"name": "__MOCK_EXECUTION__"},
  "status": {
    "startTime": "2026-07-23T06:28:41.000Z",
    "completionTime": "2026-07-23T06:32:40.000Z",
    "runningCount": 0,
    "succeededCount": 1,
    "failedCount": 0,
    "cancelledCount": 0,
    "retriedCount": 0,
    "conditions": [
      {"type": "Completed", "state": "CONDITION_SUCCEEDED"}
    ]
  }
}
JSON
    exit 0
  fi
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

if [[ "$1 $2 $3 $4" == "run jobs executions cancel" ]]; then
  [[ -n "${MOCK_CANCEL_ATTEMPT_MARKER:-}" ]] && : >"$MOCK_CANCEL_ATTEMPT_MARKER"
  if [[ "${MOCK_CANCEL_MODE:-success}" == failure ]]; then
    printf '%s\n' 'CANCEL_ERROR_SECRET_SENTINEL' >&2
    exit 7
  fi
  [[ -n "${MOCK_CANCEL_MARKER:-}" ]] || exit 91
  : >"$MOCK_CANCEL_MARKER"
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
    "textPayload": "browser readiness failed: structure\n"
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
    "textPayload": "browser readiness failed: manifest\n"
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
    "textPayload": "browser readiness failed: network-document-client\n"
  },
  {
    "labels": {
      "run.googleapis.com/execution_name": "__MOCK_EXECUTION__"
    },
    "textPayload": "browser readiness failed: network-auth-server\n"
  },
  {
    "labels": {
      "run.googleapis.com/execution_name": "__MOCK_EXECUTION__"
    },
    "textPayload": "browser readiness failed: network-events-client\n"
  },
  {
    "labels": {
      "run.googleapis.com/execution_name": "__MOCK_EXECUTION__"
    },
    "textPayload": "browser readiness failed: network-asset-server\n"
  },
  {
    "labels": {
      "run.googleapis.com/execution_name": "__MOCK_EXECUTION__"
    },
    "textPayload": "browser readiness failed: network-api-transport\n"
  },
  {
    "labels": {
      "run.googleapis.com/execution_name": "__MOCK_EXECUTION__"
    },
    "textPayload": "browser readiness failed: network-image-transport\n"
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
    "textPayload": "browser readiness failed: rate\n"
  },
  {
    "labels": {
      "run.googleapis.com/execution_name": "__MOCK_EXECUTION__"
    },
    "textPayload": "browser readiness failed: token BROWSER_LOG_TRAILING_SECRET_SENTINEL\n"
  },
  {
    "labels": {
      "run.googleapis.com/execution_name": "__MOCK_EXECUTION__"
    },
    "textPayload": "browser readiness failed: network-cookie-client BROWSER_NETWORK_UNKNOWN_ENDPOINT\n"
  }
]
JSON
  exit 0
fi

printf '%s\n' "unexpected fake gcloud invocation: $*" >&2
exit 99
FAKE_GCLOUD
chmod +x "$TEST_DIR/bin/gcloud"

cat >"$TEST_DIR/bin/timeout" <<'FAKE_TIMEOUT'
#!/usr/bin/env bash
set -euo pipefail

timeout_args=("$@")
while [[ "$1" == --* ]]; do
  shift
done
shift

if [[ "${MOCK_GCLOUD_MODE:-}" == "hung-launch" \
  && "$1 $2 $3 $4" == "gcloud run jobs execute" ]]; then
  env SCRIBE_FAKE_TIMEOUT_WRAPPED=true "$@" &
  timeout_pid=$!
  /bin/sleep 0.05
  kill -TERM "$timeout_pid" 2>/dev/null || true
  /bin/sleep 0.05
  kill -KILL "$timeout_pid" 2>/dev/null || true
  set +e
  wait "$timeout_pid" 2>/dev/null
  set -e
  [[ -n "${MOCK_TIMEOUT_KILL_MARKER:-}" ]] && : >"$MOCK_TIMEOUT_KILL_MARKER"
  exit 124
fi

exec /usr/bin/timeout "${timeout_args[@]}"
FAKE_TIMEOUT
chmod +x "$TEST_DIR/bin/timeout"

cat >"$TEST_DIR/bin/sleep" <<'FAKE_SLEEP'
#!/usr/bin/env bash
set -euo pipefail
/bin/sleep 0.02
FAKE_SLEEP
chmod +x "$TEST_DIR/bin/sleep"

export PATH="$TEST_DIR/bin:$PATH"
export TMPDIR="$TEST_DIR/tmp"
export GCLOUD_PROJECT=scribe-test
export SCRIBE_REGION=us-east5
export MOCK_GCLOUD_LOG="$TEST_DIR/gcloud.log"
export MOCK_LAUNCH_MARKER_FILE="$TEST_DIR/launched"
export MOCK_CAPTURED_RUN_ID_FILE="$TEST_DIR/run-id"

grep -Fq 'readonly EXECUTION_WAIT_SECONDS=2700' "$ROOT_DIR/ci/run-cloud-run-readiness.sh" ||
  fail "the exact-execution readiness wait is not bounded"
grep -Fq 'readonly CANCELLATION_WAIT_SECONDS=2700' "$ROOT_DIR/ci/run-cloud-run-readiness.sh" ||
  fail "the exact-execution cancellation wait is not bounded"
grep -Fq "trap 'handle_signal 130' INT" "$ROOT_DIR/ci/run-cloud-run-readiness.sh" ||
  fail "INT does not enter exact-execution cancellation cleanup"
grep -Fq "trap 'handle_signal 143' TERM" "$ROOT_DIR/ci/run-cloud-run-readiness.sh" ||
  fail "TERM does not enter exact-execution cancellation cleanup"

run_helper() {
  local output_file="$1"
  shift
  if [[ "${MOCK_PRESERVE_LAUNCH_MARKER:-false}" != true ]]; then
    rm -f -- "$MOCK_LAUNCH_MARKER_FILE"
  fi
  rm -f -- "$MOCK_CAPTURED_RUN_ID_FILE"
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
[[ "$HELPER_STATUS" -eq 1 ]] ||
  fail "the helper did not fail a terminally failed Cloud Run execution"

for expected in \
  'Scribe Cloud Run readiness diagnostics (typed fields and exact allowlisted markers only)' \
  '[readiness] kind=backend' \
  '[readiness] job=scribe-prod-backend-readiness' \
  '[readiness] execute_status=1' \
  '[status] execution_wait=failed' \
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

secret_pattern='CONTROL_PLANE_ERROR_SECRET_SENTINEL|DESCRIBE_ERROR_SECRET_SENTINEL|CANCEL(_DESCRIBE)?_ERROR_SECRET_SENTINEL|EXECUTION_(ANNOTATION|MESSAGE|ENV)_SECRET_SENTINEL|TASK_(ANNOTATION|MESSAGE|DETAIL|ENV)_SECRET_SENTINEL|LOG_(PAYLOAD|TRAILING)_SECRET_SENTINEL|NETWORK_LOG_TRAILING_SECRET_SENTINEL|OCR_LOG_TRAILING_SECRET_SENTINEL|BROWSER_LOG_TRAILING_SECRET_SENTINEL|JSON_LOG_SECRET_SENTINEL|hvs\.'
if rg -n "$secret_pattern" "$diagnostics" "$TEST_DIR/console.out" "$TEST_DIR/console.err" >/dev/null; then
  fail "raw execution, task, log, or command diagnostics escaped the bounded renderer"
fi

grep -Fq 'run jobs executions describe scribe-prod-backend-readiness-abc12' "$MOCK_GCLOUD_LOG"
grep -Fq -- '--async --format=value\(metadata.name\)' "$MOCK_GCLOUD_LOG" ||
  fail "the readiness execution was not started asynchronously with a machine-readable identity"
if grep -Fq -- '--wait' "$MOCK_GCLOUD_LOG"; then
  fail "the helper waited before capturing the execution identity"
fi
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
[[ "$HELPER_STATUS" -eq 1 ]] ||
  fail "the OCR helper did not fail a terminally failed Cloud Run execution"
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
[[ "$HELPER_STATUS" -eq 1 ]] ||
  fail "the browser helper did not fail a terminally failed Cloud Run execution"
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
  'browser readiness failed: structure' \
  'browser readiness failed: save' \
  'browser readiness failed: publish' \
  'browser readiness failed: responsive' \
  'browser readiness failed: token' \
  'browser readiness failed: manifest' \
  'browser readiness failed: cleanup' \
  'browser readiness failed: network' \
  'browser readiness failed: network-document-client' \
  'browser readiness failed: network-auth-server' \
  'browser readiness failed: network-events-client' \
  'browser readiness failed: network-asset-server' \
  'browser readiness failed: network-api-transport' \
  'browser readiness failed: network-image-transport' \
  'browser readiness failed: csp' \
  'browser readiness failed: rate' \
  '[status] log_query=ok markers=25'; do
  grep -Fq "$expected" "$browser_diagnostics" ||
    fail "browser diagnostics omitted: $expected"
done
if rg -n \
  'frontend readiness failed|frontend backend|frontend proxy|ocr readiness failed|BROWSER_LOG_TRAILING_SECRET_SENTINEL|BROWSER_NETWORK_UNKNOWN_ENDPOINT' \
  "$browser_diagnostics" "$TEST_DIR/console.out" "$TEST_DIR/console.err" >/dev/null; then
  fail "browser diagnostics accepted a marker from another readiness kind or trailing content"
fi
if rg -n "$secret_pattern" \
  "$browser_diagnostics" "$TEST_DIR/console.out" "$TEST_DIR/console.err" >/dev/null; then
  fail "browser diagnostics exposed raw execution, task, log, or command content"
fi
grep -Fq 'resource.labels.job_name=\"scribe-pr-42-browser-deadbeef\"' "$MOCK_GCLOUD_LOG"
grep -Fq 'labels.\"run.googleapis.com/execution_name\"=\"scribe-pr-42-browser-deadbeef-ghi56\"' "$MOCK_GCLOUD_LOG"

: >"$MOCK_GCLOUD_LOG"
cancelling_count_file="$TEST_DIR/browser-cancelling-count"
rm -f -- "$cancelling_count_file"
cancelling_diagnostics="$TEST_DIR/artifacts/browser-cancelling.log"
run_helper "$cancelling_diagnostics" \
  env \
  MOCK_GCLOUD_MODE=cancelling \
  MOCK_EXECUTION=scribe-pr-42-browser-deadbeef-stu89 \
  MOCK_CANCELLING_COUNT_FILE="$cancelling_count_file" \
  "$ROOT_DIR/ci/run-cloud-run-readiness.sh" \
  scribe-pr-42-browser-deadbeef browser "$cancelling_diagnostics"
[[ "$HELPER_STATUS" -eq 1 ]] ||
  fail "a terminally cancelled browser execution did not fail readiness"
[[ "$(grep -Fc 'run jobs executions describe scribe-pr-42-browser-deadbeef-stu89' "$MOCK_GCLOUD_LOG")" -ge 2 ]] ||
  fail "a CANCELLING condition was incorrectly treated as terminal"
if rg -n 'run jobs executions cancel' "$MOCK_GCLOUD_LOG" >/dev/null; then
  fail "the helper platform-cancelled a browser execution already cleaning up"
fi

: >"$MOCK_GCLOUD_LOG"
production_browser_diagnostics="$TEST_DIR/artifacts/production-browser-readiness.log"
production_state_sha256='aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
run_helper "$production_browser_diagnostics" \
  env \
  MOCK_GCLOUD_MODE=success \
  MOCK_EXECUTION=scribe-browser-acde1234-jkl78 \
  SCRIBE_BROWSER_EXPECTED_SECRET_VERSION=17 \
  SCRIBE_BROWSER_EXPECTED_STORAGE_STATE_SHA256="$production_state_sha256" \
  "$ROOT_DIR/ci/run-cloud-run-readiness.sh" \
  scribe-browser-acde1234 browser "$production_browser_diagnostics"
[[ "$HELPER_STATUS" -eq 0 ]] ||
  fail "a valid production browser execution failed"
[[ ! -e "$production_browser_diagnostics" ]] ||
  fail "a successful production browser execution emitted diagnostics"
grep -Fq -- \
  "--update-env-vars=SCRIBE_BROWSER_EXPECTED_SECRET_VERSION=17\\,SCRIBE_BROWSER_EXPECTED_STORAGE_STATE_SHA256=${production_state_sha256}" \
  "$MOCK_GCLOUD_LOG" || fail "production browser execution omitted its exact version and digest fence"

: >"$MOCK_GCLOUD_LOG"
run_helper "" \
  env MOCK_GCLOUD_MODE=success \
  "$ROOT_DIR/ci/run-cloud-run-readiness.sh" \
  scribe-browser-acde1234 browser "$TEST_DIR/artifacts/production-browser-missing-fence.log"
[[ "$HELPER_STATUS" -eq 2 ]] ||
  fail "production browser readiness accepted a missing version and digest fence"
[[ ! -s "$MOCK_GCLOUD_LOG" ]] ||
  fail "an unfenced production browser invocation reached gcloud"

: >"$MOCK_GCLOUD_LOG"
run_helper "" \
  env \
  MOCK_GCLOUD_MODE=success \
  SCRIBE_BROWSER_EXPECTED_SECRET_VERSION=1 \
  SCRIBE_BROWSER_EXPECTED_STORAGE_STATE_SHA256="$production_state_sha256" \
  "$ROOT_DIR/ci/run-cloud-run-readiness.sh" \
  scribe-browser-acde1234 browser "$TEST_DIR/artifacts/production-browser-placeholder-version.log"
[[ "$HELPER_STATUS" -eq 2 ]] ||
  fail "production browser readiness accepted inert placeholder version 1"
[[ ! -s "$MOCK_GCLOUD_LOG" ]] ||
  fail "a production browser invocation using placeholder version 1 reached gcloud"

: >"$MOCK_GCLOUD_LOG"
run_helper "" \
  env \
  SCRIBE_BROWSER_EXPECTED_SECRET_VERSION=17 \
  SCRIBE_BROWSER_EXPECTED_STORAGE_STATE_SHA256="$production_state_sha256" \
  "$ROOT_DIR/ci/run-cloud-run-readiness.sh" \
  scribe-pr-42-browser-deadbeef browser "$TEST_DIR/artifacts/preview-browser-production-fence.log"
[[ "$HELPER_STATUS" -eq 2 ]] ||
  fail "preview browser readiness accepted production session metadata"
[[ ! -s "$MOCK_GCLOUD_LOG" ]] ||
  fail "a preview browser invocation with production metadata reached gcloud"

denied_diagnostics="$TEST_DIR/artifacts/backend-readiness-denied.log"
run_helper "$denied_diagnostics" \
  env MOCK_GCLOUD_MODE=failure MOCK_LOG_DENIED=true \
  "$ROOT_DIR/ci/run-cloud-run-readiness.sh" \
  scribe-prod-backend-readiness backend "$denied_diagnostics"
[[ "$HELPER_STATUS" -eq 1 ]] ||
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
[[ "$HELPER_STATUS" -eq 125 ]] ||
  fail "an untrusted execution identity was accepted"
grep -Fq '[status] execution_identity=unavailable' "$bad_hint_diagnostics"
if grep -Fq 'run jobs executions describe another-job-abc12' "$MOCK_GCLOUD_LOG"; then
  fail "the helper queried the untrusted execution identity returned by launch"
fi
if rg -n 'run jobs executions cancel' "$MOCK_GCLOUD_LOG" >/dev/null; then
  fail "an unverified launch identity reached cancellation"
fi

: >"$MOCK_GCLOUD_LOG"
launch_failure_diagnostics="$TEST_DIR/artifacts/backend-readiness-launch-failure.log"
run_helper "$launch_failure_diagnostics" \
  env MOCK_GCLOUD_MODE=launch-failure \
  "$ROOT_DIR/ci/run-cloud-run-readiness.sh" \
  scribe-prod-backend-readiness backend "$launch_failure_diagnostics"
[[ "$HELPER_STATUS" -eq 19 ]] ||
  fail "a launch failure did not preserve the gcloud status"
grep -Fq '[status] execution_identity=unavailable' "$launch_failure_diagnostics"
if rg -n 'run jobs executions cancel' "$MOCK_GCLOUD_LOG" >/dev/null; then
  fail "a failed launch cancelled an execution without its unique marker"
fi
if rg -n "$secret_pattern" \
  "$launch_failure_diagnostics" "$TEST_DIR/console.out" "$TEST_DIR/console.err" >/dev/null; then
  fail "a failed asynchronous launch exposed raw control-plane output"
fi

: >"$MOCK_GCLOUD_LOG"
empty_identity_diagnostics="$TEST_DIR/artifacts/empty-launch-identity.log"
run_helper "$empty_identity_diagnostics" \
  env MOCK_GCLOUD_MODE=empty-success \
  "$ROOT_DIR/ci/run-cloud-run-readiness.sh" \
  scribe-prod-backend-readiness backend "$empty_identity_diagnostics"
[[ "$HELPER_STATUS" -eq 125 ]] ||
  fail "a successful launch with no recoverable execution identity did not fail closed"
grep -Fq '[status] execution_wait=identity-unavailable' "$empty_identity_diagnostics" ||
  fail "an unrecoverable empty launch identity omitted its typed status"
if rg -n 'run jobs executions cancel' "$MOCK_GCLOUD_LOG" >/dev/null; then
  fail "an unrecoverable empty launch identity cancelled an unverified execution"
fi

: >"$MOCK_GCLOUD_LOG"
hung_launch_kill_marker="$TEST_DIR/hung-launch-killed"
hung_launch_natural_count="$TEST_DIR/hung-launch-natural-count"
rm -f -- "$hung_launch_kill_marker" "$hung_launch_natural_count"
run_helper "$TEST_DIR/artifacts/hung-launch.log" \
  env \
  MOCK_GCLOUD_MODE=hung-launch \
  MOCK_EXECUTION=scribe-prod-backend-readiness-vwx01 \
  MOCK_TIMEOUT_KILL_MARKER="$hung_launch_kill_marker" \
  MOCK_NATURAL_TERMINAL_AFTER_DESCRIBES=1 \
  MOCK_NATURAL_DESCRIBE_COUNT_FILE="$hung_launch_natural_count" \
  "$ROOT_DIR/ci/run-cloud-run-readiness.sh" \
  scribe-prod-backend-readiness backend "$TEST_DIR/artifacts/hung-launch.log"
[[ "$HELPER_STATUS" -eq 0 ]] ||
  fail "an accepted hung launch was not recovered through its unique marker"
[[ -e "$hung_launch_kill_marker" ]] ||
  fail "the hung launch was not force-killed after the control-plane timeout"
grep -Fq 'run jobs executions list --job scribe-prod-backend-readiness' "$MOCK_GCLOUD_LOG" ||
  fail "the hung launch did not enter marker recovery"
grep -Fq 'run jobs executions describe scribe-prod-backend-readiness-vwx01' "$MOCK_GCLOUD_LOG" ||
  fail "the hung launch did not wait on the recovered exact execution"

: >"$MOCK_GCLOUD_LOG"
numeric_resource_count="$TEST_DIR/numeric-resource-natural-count"
rm -f -- "$numeric_resource_count"
run_helper "$TEST_DIR/artifacts/numeric-resource-launch.log" \
  env \
  MOCK_GCLOUD_MODE=numeric-resource-success \
  MOCK_EXECUTION=scribe-prod-backend-readiness-yza23 \
  MOCK_NATURAL_TERMINAL_AFTER_DESCRIBES=1 \
  MOCK_NATURAL_DESCRIBE_COUNT_FILE="$numeric_resource_count" \
  "$ROOT_DIR/ci/run-cloud-run-readiness.sh" \
  scribe-prod-backend-readiness backend "$TEST_DIR/artifacts/numeric-resource-launch.log"
[[ "$HELPER_STATUS" -eq 0 ]] ||
  fail "a numeric-project full resource name was not recovered by its exact marker"
grep -Fq 'run jobs executions describe scribe-prod-backend-readiness-yza23' "$MOCK_GCLOUD_LOG" ||
  fail "numeric-project recovery did not bind to the exact execution leaf"
if rg -n 'run jobs executions describe projects/123456789012' "$MOCK_GCLOUD_LOG" >/dev/null; then
  fail "numeric-project recovery queried the untrusted full resource identity"
fi

: >"$MOCK_GCLOUD_LOG"
launch_signal_count_file="$TEST_DIR/launch-signal-describe-count"
rm -f -- "$launch_signal_count_file"
launch_signal_execution='scribe-browser-acde1234-lmn45'
run_helper "" \
  env \
  MOCK_GCLOUD_MODE=launch-signal \
  MOCK_EXECUTION="$launch_signal_execution" \
  MOCK_NATURAL_TERMINAL_AFTER_DESCRIBES=1 \
  MOCK_NATURAL_DESCRIBE_COUNT_FILE="$launch_signal_count_file" \
  SCRIBE_BROWSER_EXPECTED_SECRET_VERSION=17 \
  SCRIBE_BROWSER_EXPECTED_STORAGE_STATE_SHA256="$production_state_sha256" \
  "$ROOT_DIR/ci/run-cloud-run-readiness.sh" \
  scribe-browser-acde1234 browser "$TEST_DIR/artifacts/launch-signal-browser-readiness.log"
[[ "$HELPER_STATUS" -eq 143 ]] ||
  fail "a signal during launch did not retain the TERM status"
grep -Fq 'run jobs executions list --job scribe-browser-acde1234' "$MOCK_GCLOUD_LOG" ||
  fail "launch interruption did not recover through the exact job execution set"
if rg -n 'run jobs executions cancel' "$MOCK_GCLOUD_LOG" >/dev/null; then
  fail "launch interruption platform-cancelled browser cleanup"
fi
grep -Fq "run jobs executions describe ${launch_signal_execution}" "$MOCK_GCLOUD_LOG" ||
  fail "launch interruption did not wait for its uniquely marked execution"
if rg 'run jobs executions (cancel|describe)' "$MOCK_GCLOUD_LOG" \
  | rg -v "run jobs executions (cancel|describe) ${launch_signal_execution}" >/dev/null; then
  fail "launch interruption targeted an execution other than its marked identity"
fi
if rg -n "$secret_pattern" \
  "$TEST_DIR/console.out" "$TEST_DIR/console.err" >/dev/null; then
  fail "launch interruption exposed raw execution or control-plane content"
fi

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
[[ "$(wc -l <"$MOCK_GCLOUD_LOG")" -eq 3 ]] ||
  fail "a successful readiness execution made calls beyond preflight, launch, and its exact poll"

: >"$MOCK_GCLOUD_LOG"
preflight_cancel_marker="$TEST_DIR/preflight-cancelled"
rm -f -- "$preflight_cancel_marker"
: >"$MOCK_LAUNCH_MARKER_FILE"
export MOCK_PRESERVE_LAUNCH_MARKER=true
run_helper "$TEST_DIR/artifacts/preflight-launch-failure.log" \
  env \
  MOCK_GCLOUD_MODE=preflight-launch-failure \
  MOCK_CANCEL_MARKER="$preflight_cancel_marker" \
  "$ROOT_DIR/ci/run-cloud-run-readiness.sh" \
  scribe-prod-backend-readiness backend "$TEST_DIR/artifacts/preflight-launch-failure.log"
unset MOCK_PRESERVE_LAUNCH_MARKER
[[ "$HELPER_STATUS" -eq 19 ]] ||
  fail "a drained prior execution changed the subsequent launch failure status"
[[ -e "$preflight_cancel_marker" ]] ||
  fail "retry preflight did not cancel the prior nonterminal execution"
preflight_cancel_line="$(rg -n -m 1 'run jobs executions cancel scribe-prod-backend-readiness-abc12' "$MOCK_GCLOUD_LOG" | cut -d: -f1)"
preflight_execute_line="$(rg -n -m 1 'run jobs execute scribe-prod-backend-readiness' "$MOCK_GCLOUD_LOG" | cut -d: -f1)"
[[ "$preflight_cancel_line" =~ ^[1-9][0-9]*$ \
  && "$preflight_execute_line" =~ ^[1-9][0-9]*$ \
  && "$preflight_cancel_line" -lt "$preflight_execute_line" ]] ||
  fail "retry preflight launched before the prior execution reached terminal state"

: >"$MOCK_GCLOUD_LOG"
browser_preflight_count_file="$TEST_DIR/browser-preflight-natural-count"
rm -f -- "$browser_preflight_count_file"
: >"$MOCK_LAUNCH_MARKER_FILE"
export MOCK_PRESERVE_LAUNCH_MARKER=true
run_helper "" \
  env \
  MOCK_GCLOUD_MODE=running \
  MOCK_EXECUTION=scribe-browser-acde1234-pqr67 \
  MOCK_NATURAL_TERMINAL_AFTER_DESCRIBES=1 \
  MOCK_NATURAL_DESCRIBE_COUNT_FILE="$browser_preflight_count_file" \
  "$ROOT_DIR/ci/run-cloud-run-readiness.sh" \
  --preflight-only scribe-browser-acde1234 browser
unset MOCK_PRESERVE_LAUNCH_MARKER
[[ "$HELPER_STATUS" -eq 0 ]] ||
  fail "browser preflight-only fencing did not reach a clear execution set"
grep -Fq 'preflight is clear' "$TEST_DIR/console.out" ||
  fail "browser preflight-only fencing omitted its bounded success status"
if rg -n 'run jobs execute |run jobs executions cancel' "$MOCK_GCLOUD_LOG" >/dev/null; then
  fail "browser preflight-only fencing launched or platform-cancelled an execution"
fi
[[ "$(grep -Fc 'run jobs executions describe scribe-browser-acde1234-pqr67' "$MOCK_GCLOUD_LOG")" -ge 2 ]] ||
  fail "browser preflight-only fencing did not wait for exact natural terminal state"

: >"$MOCK_GCLOUD_LOG"
query_failure_cancel_marker="$TEST_DIR/query-failure-cancelled"
rm -f -- "$query_failure_cancel_marker"
query_failure_diagnostics="$TEST_DIR/artifacts/backend-readiness-query-failure.log"
run_helper "$query_failure_diagnostics" \
  env \
  MOCK_GCLOUD_MODE=query-failure \
  MOCK_CANCEL_MARKER="$query_failure_cancel_marker" \
  "$ROOT_DIR/ci/run-cloud-run-readiness.sh" \
  scribe-prod-backend-readiness backend "$query_failure_diagnostics"
[[ "$HELPER_STATUS" -eq 125 ]] ||
  fail "persistent exact-execution query failures were not bounded"
grep -Fq '[status] execution_wait=control-plane-unavailable' "$query_failure_diagnostics" ||
  fail "persistent query failure diagnostics omitted their category"
[[ -e "$query_failure_cancel_marker" ]] ||
  fail "EXIT cleanup did not cancel the captured execution"
[[ "$(grep -Fc 'run jobs executions cancel scribe-prod-backend-readiness-abc12' "$MOCK_GCLOUD_LOG")" -eq 1 ]] ||
  fail "EXIT cleanup did not cancel exactly the captured execution once"
if rg 'run jobs executions (cancel|describe)' "$MOCK_GCLOUD_LOG" \
  | rg -v 'run jobs executions (cancel|describe) scribe-prod-backend-readiness-abc12' >/dev/null; then
  fail "EXIT cleanup targeted an execution other than the captured identity"
fi
if rg -n "$secret_pattern" \
  "$query_failure_diagnostics" "$TEST_DIR/console.out" "$TEST_DIR/console.err" >/dev/null; then
  fail "cancellation after query failure exposed raw control-plane output"
fi

: >"$MOCK_GCLOUD_LOG"
cancel_failure_attempt_marker="$TEST_DIR/cancel-failure-attempted"
rm -f -- "$cancel_failure_attempt_marker"
cancel_failure_diagnostics="$TEST_DIR/artifacts/cancel-api-failure.log"
run_helper "$cancel_failure_diagnostics" \
  env \
  MOCK_GCLOUD_MODE=query-failure \
  MOCK_CANCEL_MODE=failure \
  MOCK_CANCEL_ATTEMPT_MARKER="$cancel_failure_attempt_marker" \
  MOCK_TERMINAL_AFTER_CANCEL_ATTEMPT=true \
  "$ROOT_DIR/ci/run-cloud-run-readiness.sh" \
  scribe-prod-backend-readiness backend "$cancel_failure_diagnostics"
[[ "$HELPER_STATUS" -eq 125 ]] ||
  fail "a failed cancel API call masked the original bounded readiness failure"
[[ -e "$cancel_failure_attempt_marker" ]] ||
  fail "the exact cancellation API was not attempted"
grep -Fq 'reached cancelled' "$TEST_DIR/console.err" ||
  fail "cleanup did not wait for terminal state after the cancel API failed"
if rg -n "$secret_pattern" \
  "$cancel_failure_diagnostics" "$TEST_DIR/console.out" "$TEST_DIR/console.err" >/dev/null; then
  fail "a failed cancellation API call exposed raw control-plane output"
fi

: >"$MOCK_GCLOUD_LOG"
cancel_retry_marker="$TEST_DIR/cancel-retry-cancelled"
cancel_describe_count_file="$TEST_DIR/cancel-describe-count"
rm -f -- "$cancel_retry_marker" "$cancel_describe_count_file"
cancel_retry_diagnostics="$TEST_DIR/artifacts/cancel-describe-retry.log"
run_helper "$cancel_retry_diagnostics" \
  env \
  MOCK_GCLOUD_MODE=query-failure \
  MOCK_CANCEL_MARKER="$cancel_retry_marker" \
  MOCK_CANCEL_DESCRIBE_FAILURES=2 \
  MOCK_CANCEL_DESCRIBE_COUNT_FILE="$cancel_describe_count_file" \
  "$ROOT_DIR/ci/run-cloud-run-readiness.sh" \
  scribe-prod-backend-readiness backend "$cancel_retry_diagnostics"
[[ "$HELPER_STATUS" -eq 125 ]] ||
  fail "transient cancellation describe failures masked the readiness status"
[[ "$(<"$cancel_describe_count_file")" == 2 ]] ||
  fail "cleanup did not retry transient exact-execution describe failures"
grep -Fq 'reached cancelled' "$TEST_DIR/console.err" ||
  fail "cleanup did not confirm terminal state after transient describe failures"
if rg -n "$secret_pattern" \
  "$cancel_retry_diagnostics" "$TEST_DIR/console.out" "$TEST_DIR/console.err" >/dev/null; then
  fail "transient cancellation describe failures exposed raw control-plane output"
fi

: >"$MOCK_GCLOUD_LOG"
cancel_unconfirmed_marker="$TEST_DIR/cancel-unconfirmed"
rm -f -- "$cancel_unconfirmed_marker"
cancel_unconfirmed_diagnostics="$TEST_DIR/artifacts/cancel-unconfirmed.log"
run_helper "$cancel_unconfirmed_diagnostics" \
  env \
  MOCK_GCLOUD_MODE=query-failure-cancel-describe-persistent \
  MOCK_CANCEL_MARKER="$cancel_unconfirmed_marker" \
  "$ROOT_DIR/ci/run-cloud-run-readiness.sh" \
  scribe-prod-backend-readiness backend "$cancel_unconfirmed_diagnostics"
[[ "$HELPER_STATUS" -eq 126 ]] ||
  fail "unconfirmed terminal state did not surface a distinct cleanup failure"
grep -Fq 'may still be running' "$TEST_DIR/console.err" ||
  fail "unconfirmed terminal state was reported as safe cancellation"
if grep -Fq 'reached cancelled' "$TEST_DIR/console.err"; then
  fail "persistent describe failures falsely reported terminal cancellation"
fi
if rg -n "$secret_pattern" \
  "$cancel_unconfirmed_diagnostics" "$TEST_DIR/console.out" "$TEST_DIR/console.err" >/dev/null; then
  fail "unconfirmed cancellation exposed raw control-plane output"
fi

: >"$MOCK_GCLOUD_LOG"
signal_natural_count_file="$TEST_DIR/signal-natural-count"
rm -f -- "$signal_natural_count_file" "$MOCK_LAUNCH_MARKER_FILE"
signal_execution='scribe-browser-acde1234-mno90'
set +e
env \
  MOCK_GCLOUD_MODE=running \
  MOCK_EXECUTION="$signal_execution" \
  MOCK_NATURAL_TERMINAL_AFTER_DESCRIBES=1 \
  MOCK_NATURAL_DESCRIBE_COUNT_FILE="$signal_natural_count_file" \
  SCRIBE_BROWSER_EXPECTED_SECRET_VERSION=17 \
  SCRIBE_BROWSER_EXPECTED_STORAGE_STATE_SHA256="$production_state_sha256" \
  "$ROOT_DIR/ci/run-cloud-run-readiness.sh" \
  scribe-browser-acde1234 browser "$TEST_DIR/artifacts/signal-browser-readiness.log" \
  >"$TEST_DIR/signal-console.out" 2>"$TEST_DIR/signal-console.err" &
signal_helper_pid=$!
set -e
signal_poll_seen=false
for _ in $(seq 1 100); do
  if grep -Fq "run jobs executions describe ${signal_execution}" "$MOCK_GCLOUD_LOG"; then
    signal_poll_seen=true
    break
  fi
  /bin/sleep 0.02
done
[[ "$signal_poll_seen" == true ]] ||
  fail "the signal test did not observe an exact-execution poll"
kill -TERM "$signal_helper_pid"
set +e
wait "$signal_helper_pid"
signal_helper_status=$?
set -e
[[ "$signal_helper_status" -eq 143 ]] ||
  fail "TERM cancellation did not retain the signal exit status"
if rg -n 'run jobs executions cancel' "$MOCK_GCLOUD_LOG" >/dev/null; then
  fail "TERM platform-cancelled browser cleanup"
fi
[[ "$(grep -Fc "run jobs executions describe ${signal_execution}" "$MOCK_GCLOUD_LOG")" -ge 2 ]] ||
  fail "TERM cancellation did not wait on the exact browser execution"
if rg 'run jobs executions (cancel|describe)' "$MOCK_GCLOUD_LOG" \
  | rg -v 'run jobs executions (cancel|describe) scribe-browser-acde1234-mno90' >/dev/null; then
  fail "TERM cleanup targeted an execution other than the captured identity"
fi
if rg -n "$secret_pattern" \
  "$TEST_DIR/signal-console.out" "$TEST_DIR/signal-console.err" >/dev/null; then
  fail "signal cancellation exposed raw execution or control-plane content"
fi

if rg -n 'timeout "[$][{]CONTROL_PLANE_CALL_TIMEOUT_SECONDS[}]s"' \
  "$ROOT_DIR/ci/run-cloud-run-readiness.sh" >/dev/null; then
  fail "a control-plane timeout can wait forever after TERM"
fi
[[ "$(rg -c -- '--kill-after=' "$ROOT_DIR/ci/run-cloud-run-readiness.sh")" -eq 10 ]] ||
  fail "Cloud Run control-plane calls are not all hard-bounded"

: >"$MOCK_GCLOUD_LOG"
run_helper "" \
  "$ROOT_DIR/ci/run-cloud-run-readiness.sh" \
  another-job backend "$TEST_DIR/artifacts/invalid.log"
[[ "$HELPER_STATUS" -eq 2 ]] ||
  fail "an invalid readiness job identity was accepted"
[[ ! -s "$MOCK_GCLOUD_LOG" ]] ||
  fail "invalid input reached gcloud"

echo "Cloud Run readiness launch recovery, stale-run fencing, terminal settlement, and diagnostics are exact-scoped."
