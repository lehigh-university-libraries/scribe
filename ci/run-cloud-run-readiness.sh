#!/usr/bin/env bash

set -euo pipefail

readonly MAX_LOG_ENTRIES=100
readonly MAX_LOG_MARKERS=32
readonly READINESS_ERROR_KIND='(Error|TypeError|AbortError|TimeoutError)(/(EACCES|EADDRINUSE|EAI_AGAIN|ECONNREFUSED|ECONNRESET|EHOSTUNREACH|ENETUNREACH|ENOTFOUND|EPIPE|ENOENT|ETIMEDOUT|ERR_STREAM_PREMATURE_CLOSE))?'
readonly READINESS_PAYLOAD_KIND='(invalid-json|invalid-payload|non-ready-status|missing-status|api-image-mismatch|ready)'
readonly BACKEND_PAYLOAD_KIND='(invalid-json|invalid-payload|non-ready-status|missing-status|ready-payload-with-non-success-http)'
readonly BACKEND_HTTP_KIND='(http-ready|http-non-ready|http-invalid|http-error|http-timeout|http-transport-(EACCES|EADDRINUSE|EAI_AGAIN|ECONNREFUSED|ECONNRESET|EHOSTUNREACH|ENETUNREACH|ENOTFOUND|EPIPE|ENOENT|ETIMEDOUT|ERR_STREAM_PREMATURE_CLOSE|error))'
readonly BACKEND_NETWORK_KIND="((dns-match|dns-mismatch|dns-empty|dns-timeout|dns-error); (tcp-open|tcp-refused|tcp-timeout|tcp-unreachable|tcp-error); ${BACKEND_HTTP_KIND}|dns-invalid-origin; tcp-skipped; http-skipped)"
readonly BACKEND_READINESS_LOG_PATTERN="^(frontend readiness failed: (frontend-server-exited|frontend did not respond|HTTP [1-5][0-9]{2} \\(${READINESS_PAYLOAD_KIND}\\)|transport-${READINESS_ERROR_KIND}|internal-${READINESS_ERROR_KIND})|frontend proxy request failed \\[${READINESS_ERROR_KIND}\\]|frontend backend startup gate failed \\[${READINESS_ERROR_KIND}; (readiness-contract; HTTP [1-5][0-9]{2} \\((invalid-json|invalid-payload|missing-status|ready-payload-with-non-success-http)\\)|startup-deadline; (backend did not report ready|HTTP [1-5][0-9]{2} \\(${BACKEND_PAYLOAD_KIND}\\)|transport-${READINESS_ERROR_KIND}))\\]|frontend backend network probe \\[${BACKEND_NETWORK_KIND}\\])$"
readonly OCR_READINESS_LOG_PATTERN='^ocr readiness failed: (image-contract|(segment|transcribe|ollama)-(token|request|timeout|contract))$'

fail() {
  printf 'Cloud Run readiness helper failed: %s\n' "$*" >&2
  exit 2
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || fail "$1 is required"
}

usage() {
  cat >&2 <<'EOF'
Usage: run-cloud-run-readiness.sh JOB KIND DIAGNOSTICS_FILE

Runs one existing Cloud Run readiness job to completion. KIND must be backend
or ocr. On failure, DIAGNOSTICS_FILE receives only typed execution/task fields
and exact allowlisted readiness markers; raw API responses and logs are never
persisted.
EOF
}

[[ "$#" -eq 3 ]] || {
  usage
  fail "expected JOB, KIND, and DIAGNOSTICS_FILE"
}

job="$1"
kind="$2"
diagnostics_file="$3"
project="${GCLOUD_PROJECT:-}"
region="${SCRIBE_REGION:-}"

[[ "$project" =~ ^[a-z][a-z0-9-]{4,28}[a-z0-9]$ ]] ||
  fail "GCLOUD_PROJECT must be a valid project ID"
[[ "$region" =~ ^[a-z]+-[a-z]+[0-9]+$ ]] ||
  fail "SCRIBE_REGION must be a valid GCP region"
[[ "$job" =~ ^scribe(-pr-[1-9][0-9]*)?-(prod|pr-[1-9][0-9]*)-(backend|ocr)-readiness$ ]] ||
  fail "JOB must identify a Scribe backend or OCR readiness job"
case "$kind" in
  backend)
    [[ "$job" == *-backend-readiness ]] ||
      fail "backend KIND does not match JOB"
    readiness_log_pattern="$BACKEND_READINESS_LOG_PATTERN"
    ;;
  ocr)
    [[ "$job" == *-ocr-readiness ]] ||
      fail "ocr KIND does not match JOB"
    readiness_log_pattern="$OCR_READINESS_LOG_PATTERN"
    ;;
  *) fail "KIND must be backend or ocr" ;;
esac
[[ "$diagnostics_file" == *.log && "$diagnostics_file" != *$'\n'* ]] ||
  fail "DIAGNOSTICS_FILE must be a single-line .log path"
diagnostics_dir="$(dirname -- "$diagnostics_file")"
[[ -d "$diagnostics_dir" ]] ||
  fail "DIAGNOSTICS_FILE directory must already exist"
[[ ! -L "$diagnostics_file" ]] ||
  fail "DIAGNOSTICS_FILE must not be a symbolic link"

for command in gcloud install jq mktemp; do
  require_command "$command"
done

umask 077
temp_dir="$(mktemp -d "${TMPDIR:-/tmp}/scribe-cloud-run-readiness.XXXXXX")"
trap 'rm -rf -- "$temp_dir"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

execute_stdout="$temp_dir/execute.stdout"
execute_stderr="$temp_dir/execute.stderr"
execution_json="$temp_dir/execution.json"
tasks_json="$temp_dir/tasks.json"
logs_json="$temp_dir/logs.json"
rendered_diagnostics="$temp_dir/diagnostics.log"

printf 'Running Cloud Run %s readiness job %s.\n' "$kind" "$job"
set +e
gcloud run jobs execute "$job" \
  --project "$project" \
  --region "$region" \
  --wait \
  --quiet >"$execute_stdout" 2>"$execute_stderr"
execute_status=$?
set -e

if ((execute_status == 0)); then
  printf 'Cloud Run %s readiness passed for %s.\n' "$kind" "$job"
  exit 0
fi

execution="$(
  sed -nE "s/^gcloud run jobs executions describe (${job}-[a-z0-9]{5})\r?$/\\1/p" \
    "$execute_stderr" | tail -n 1
)"

{
  printf 'Scribe Cloud Run readiness diagnostics (typed fields and exact allowlisted markers only)\n'
  printf '[readiness] kind=%s\n' "$kind"
  printf '[readiness] job=%s\n' "$job"
  printf '[readiness] execute_status=%s\n' "$execute_status"

  if [[ -z "$execution" ]]; then
    printf '[status] execution_identity=unavailable\n'
  else
    printf '[readiness] execution=%s\n' "$execution"
    printf '[status] execution_identity=ok\n'
  fi
} >"$rendered_diagnostics"

if [[ -n "$execution" ]]; then
  if gcloud run jobs executions describe "$execution" \
    --project "$project" \
    --region "$region" \
    --format=json >"$execution_json" 2>/dev/null; then
    execution_record="$(
      jq -er --arg expected "$execution" '
        def leaf:
          if type == "string" then split("/") | last else "" end;
        def timestamp:
          if type == "string" and test("^(unknown|[0-9TZ:+.-]{10,40})$")
          then . else "unknown" end;
        def bounded_count:
          (tonumber? // -1) as $number
          | if $number >= 0 and $number <= 64 and ($number | floor) == $number
            then $number else error("invalid count") end;
        def terminal_state($condition; $succeeded; $failed; $cancelled; $running):
          if ($condition.status // "") == "True"
              or ($condition.state // "") == "CONDITION_SUCCEEDED" then "succeeded"
          elif ($condition.status // "") == "False"
              or ($condition.state // "") == "CONDITION_FAILED" then "failed"
          elif ($condition.status // "") == "Unknown"
              or ($condition.state // "") == "CONDITION_RECONCILING"
              or ($condition.state // "") == "CONDITION_PENDING" then "running"
          elif $failed > 0 then "failed"
          elif $cancelled > 0 then "cancelled"
          elif $succeeded > 0 then "succeeded"
          elif $running > 0 then "running"
          else "unknown"
          end;
        def terminal_reason($condition):
          (($condition.reason // $condition.executionReason // "")
            | ascii_downcase | gsub("[^a-z0-9]"; "")) as $reason
          | if $reason == "nonzeroexitcode" then "non-zero-exit"
            elif $reason == "cancelled" or $reason == "cancelling" then "cancelled"
            elif $reason == "progressdeadlineexceeded" then "deadline"
            elif $reason == "jobstatusservicepollingerror"
              or $reason == "internal" then "platform"
            else "unknown"
            end;

        ((.metadata.name // .name // "") | leaf) as $name
        | select($name == $expected)
        | ((.status.runningCount // .runningCount // 0) | bounded_count) as $running
        | ((.status.succeededCount // .succeededCount // 0) | bounded_count) as $succeeded
        | ((.status.failedCount // .failedCount // 0) | bounded_count) as $failed
        | ((.status.cancelledCount // .cancelledCount // 0) | bounded_count) as $cancelled
        | ((.status.retriedCount // .retriedCount // 0) | bounded_count) as $retried
        | ([((.status.conditions // .conditions // [])[]?)
            | select((.type // "") == "Completed" or (.type // "") == "Ready")]
            | first // {}) as $terminal
        | [
            $name,
            ((.metadata.creationTimestamp // .createTime // "unknown") | timestamp),
            ((.status.startTime // .startTime // "unknown") | timestamp),
            ((.status.completionTime // .completionTime // "unknown") | timestamp),
            ($running | tostring),
            ($succeeded | tostring),
            ($failed | tostring),
            ($cancelled | tostring),
            ($retried | tostring),
            terminal_state($terminal; $succeeded; $failed; $cancelled; $running),
            terminal_reason($terminal)
          ]
        | @tsv
      ' "$execution_json"
    )" || execution_record=""

    if [[ -n "$execution_record" ]]; then
      IFS=$'\t' read -r \
        execution_name created started completed running succeeded failed cancelled retried state reason \
        <<<"$execution_record"
      {
        printf '[execution] name=%s\n' "$execution_name"
        printf '[execution] create_time=%s\n' "$created"
        printf '[execution] start_time=%s\n' "$started"
        printf '[execution] completion_time=%s\n' "$completed"
        printf '[execution] running_count=%s\n' "$running"
        printf '[execution] succeeded_count=%s\n' "$succeeded"
        printf '[execution] failed_count=%s\n' "$failed"
        printf '[execution] cancelled_count=%s\n' "$cancelled"
        printf '[execution] retried_count=%s\n' "$retried"
        printf '[execution] state=%s\n' "$state"
        printf '[execution] reason=%s\n' "$reason"
        printf '[status] execution_query=ok\n'
      } >>"$rendered_diagnostics"
    else
      printf '[status] execution_query=invalid\n' >>"$rendered_diagnostics"
    fi
  else
    printf '[status] execution_query=unavailable\n' >>"$rendered_diagnostics"
  fi

  if gcloud run jobs executions tasks list \
    --execution "$execution" \
    --project "$project" \
    --region "$region" \
    --limit 4 \
    --format=json >"$tasks_json" 2>/dev/null; then
    task_records="$(
      jq -er '
        def bounded($maximum):
          (tonumber? // -1) as $number
          | if $number >= 0 and $number <= $maximum and ($number | floor) == $number
            then ($number | tostring) else "unknown" end;
        if type != "array" or length > 4 then error("invalid task list") else . end
        | .[]
        | (.status.lastAttemptResult // .lastAttemptResult // {}) as $result
        | [
            ((.status.index // .index // -1) | bounded(63)),
            ((.status.retried // .retried // -1) | bounded(63)),
            (($result.exitCode // -1) | bounded(255)),
            (($result.termSignal // -1) | bounded(64)),
            (($result.status.code // -1) | bounded(16))
          ]
        | @tsv
      ' "$tasks_json"
    )" || task_records=""

    task_count=0
    if [[ -n "$task_records" ]]; then
      while IFS=$'\t' read -r index task_retried exit_code term_signal status_code; do
        printf '[task] index=%s retried=%s exit_code=%s term_signal=%s status_code=%s\n' \
          "$index" "$task_retried" "$exit_code" "$term_signal" "$status_code" \
          >>"$rendered_diagnostics"
        task_count=$((task_count + 1))
      done <<<"$task_records"
    fi
    printf '[status] task_query=ok tasks=%s\n' "$task_count" >>"$rendered_diagnostics"
  else
    printf '[status] task_query=unavailable\n' >>"$rendered_diagnostics"
  fi

  log_filter="resource.type=\"cloud_run_job\" AND resource.labels.job_name=\"${job}\" AND resource.labels.location=\"${region}\" AND labels.\"run.googleapis.com/execution_name\"=\"${execution}\""
  if gcloud logging read "$log_filter" \
    --project "$project" \
    --freshness 2h \
    --order asc \
    --limit "$MAX_LOG_ENTRIES" \
    --format=json >"$logs_json" 2>/dev/null; then
    log_markers="$(
      jq -er \
        --arg pattern "$readiness_log_pattern" \
        --arg execution "$execution" \
        --argjson maximum "$MAX_LOG_MARKERS" '
          if type != "array" then error("invalid log list") else . end
          | [
              .[]
              | select(.labels["run.googleapis.com/execution_name"] == $execution)
              | (.textPayload // "")
              | select(type == "string")
              | gsub("\r?\n$"; "")
              | select(test($pattern))
            ][0:$maximum][]
        ' "$logs_json"
    )" || log_markers=""

    marker_count=0
    if [[ -n "$log_markers" ]]; then
      while IFS= read -r marker; do
        printf '%s\n' "$marker" >>"$rendered_diagnostics"
        marker_count=$((marker_count + 1))
      done <<<"$log_markers"
    fi
    printf '[status] log_query=ok markers=%s\n' "$marker_count" >>"$rendered_diagnostics"
  else
    printf '[status] log_query=unavailable\n' >>"$rendered_diagnostics"
  fi
fi

install -m 0600 "$rendered_diagnostics" "$diagnostics_file"
while IFS= read -r diagnostic_line || [[ -n "$diagnostic_line" ]]; do
  printf '%s\n' "$diagnostic_line" >&2
done <"$diagnostics_file"

exit "$execute_status"
