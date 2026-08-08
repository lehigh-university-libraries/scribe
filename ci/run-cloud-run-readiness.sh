#!/usr/bin/env bash

set -euo pipefail

readonly MAX_LOG_ENTRIES=100
readonly MAX_LOG_MARKERS=32
readonly EXECUTION_WAIT_SECONDS=2700
readonly EXECUTION_POLL_SECONDS=5
readonly CONTROL_PLANE_CALL_TIMEOUT_SECONDS=30
readonly CONTROL_PLANE_KILL_AFTER_SECONDS=5
readonly MAX_CONSECUTIVE_QUERY_FAILURES=3
readonly MAX_CONSECUTIVE_CANCELLATION_QUERY_FAILURES=3
readonly LAUNCH_IDENTITY_RECOVERY_ATTEMPTS=12
readonly CANCELLATION_WAIT_SECONDS=2700
readonly MAX_RECOVERY_EXECUTIONS=16
readonly MAX_PREFLIGHT_PASSES=3
readonly READINESS_ERROR_KIND='(Error|TypeError|AbortError|TimeoutError)(/(EACCES|EADDRINUSE|EAI_AGAIN|ECONNREFUSED|ECONNRESET|EHOSTUNREACH|ENETUNREACH|ENOTFOUND|EPIPE|ENOENT|ETIMEDOUT|ERR_STREAM_PREMATURE_CLOSE))?'
readonly READINESS_PAYLOAD_KIND='(invalid-json|invalid-payload|non-ready-status|missing-status|api-image-mismatch|public-origin-mismatch|ready)'
readonly BACKEND_PAYLOAD_KIND='(invalid-json|invalid-payload|invalid-public-origin|non-ready-status|missing-status|ready-payload-with-non-success-http)'
readonly BACKEND_HTTP_KIND='(http-ready|http-non-ready|http-invalid|http-error|http-timeout|http-transport-(EACCES|EADDRINUSE|EAI_AGAIN|ECONNREFUSED|ECONNRESET|EHOSTUNREACH|ENETUNREACH|ENOTFOUND|EPIPE|ENOENT|ETIMEDOUT|ERR_STREAM_PREMATURE_CLOSE|error))'
readonly BACKEND_NETWORK_KIND="((dns-match|dns-mismatch|dns-empty|dns-timeout|dns-error); (tcp-open|tcp-refused|tcp-timeout|tcp-unreachable|tcp-error); ${BACKEND_HTTP_KIND}|dns-invalid-origin; tcp-skipped; http-skipped)"
readonly BACKEND_READINESS_LOG_PATTERN="^(frontend readiness failed: (frontend-server-exited|frontend did not respond|HTTP [1-5][0-9]{2} \\(${READINESS_PAYLOAD_KIND}\\)|transport-${READINESS_ERROR_KIND}|internal-${READINESS_ERROR_KIND})|frontend proxy request failed \\[${READINESS_ERROR_KIND}\\]|frontend backend startup gate failed \\[${READINESS_ERROR_KIND}; (readiness-contract; HTTP [1-5][0-9]{2} \\((invalid-json|invalid-payload|invalid-public-origin|missing-status|ready-payload-with-non-success-http)\\)|startup-deadline; (backend did not report ready|HTTP [1-5][0-9]{2} \\(${BACKEND_PAYLOAD_KIND}\\)|transport-${READINESS_ERROR_KIND}))\\]|frontend backend network probe \\[${BACKEND_NETWORK_KIND}\\])$"
readonly OCR_READINESS_LOG_PATTERN='^ocr readiness failed: (image-contract|(segment|transcribe|ollama)-(token|request|timeout|contract))$'
readonly BROWSER_READINESS_LOG_PATTERN='^browser readiness failed: (home|context|upload|handoff|transcription|annotations|editor|overlay|retranscribe|structure|save|publish|responsive|token|manifest|cleanup|network|network-(document|auth|workspace|item|context|annotation|processing|transcription|events|presentation|iiif|asset|other)-(client|server)|network-(document|api|events|image|asset|other)-transport|initial-ingress-(forbidden|not-found)|csp|rate)$'

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
       run-cloud-run-readiness.sh --preflight-only JOB KIND

Runs one existing Cloud Run readiness job to completion. KIND must be backend,
browser, or ocr. On failure, DIAGNOSTICS_FILE receives only typed execution/task fields
and exact allowlisted readiness markers; raw API responses and logs are never
persisted. The preflight-only form fences prior executions without starting a new one.
EOF
}

preflight_only=false
if [[ "$#" -eq 3 && "$1" == "--preflight-only" ]]; then
  preflight_only=true
  shift
elif [[ "$#" -ne 3 ]]; then
  usage
  fail "expected JOB, KIND, and DIAGNOSTICS_FILE or --preflight-only JOB KIND"
fi

job="$1"
kind="$2"
diagnostics_file="${3:-}"
project="${GCLOUD_PROJECT:-}"
region="${SCRIBE_REGION:-}"

[[ "$project" =~ ^[a-z][a-z0-9-]{4,28}[a-z0-9]$ ]] ||
  fail "GCLOUD_PROJECT must be a valid project ID"
[[ "$region" =~ ^[a-z]+-[a-z]+[0-9]+$ ]] ||
  fail "SCRIBE_REGION must be a valid GCP region"
[[ "$job" =~ ^scribe(-pr-[1-9][0-9]*)?-(prod|pr-[1-9][0-9]*)-(backend|ocr)-readiness$ || "$job" =~ ^scribe-pr-[1-9][0-9]*-browser-[0-9a-f]{8}$ || "$job" =~ ^scribe-browser-[0-9a-f]{8}$ ]] ||
  fail "JOB must identify a Scribe backend, browser, or OCR readiness job"
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
  browser)
    [[ "$job" == scribe-pr-*-browser-???????? || "$job" =~ ^scribe-browser-[0-9a-f]{8}$ ]] ||
      fail "browser KIND does not match JOB"
    readiness_log_pattern="$BROWSER_READINESS_LOG_PATTERN"
    ;;
  *) fail "KIND must be backend, browser, or ocr" ;;
esac

execute_override_value=""
if [[ "$job" =~ ^scribe-browser-[0-9a-f]{8}$ && "$preflight_only" != true ]]; then
  expected_secret_version="${SCRIBE_BROWSER_EXPECTED_SECRET_VERSION:-}"
  expected_state_sha256="${SCRIBE_BROWSER_EXPECTED_STORAGE_STATE_SHA256:-}"
  [[ "$expected_secret_version" =~ ^([2-9]|[1-9][0-9]{1,19})$ ]] ||
    fail "production browser readiness requires an exact secret version"
  [[ "$expected_state_sha256" =~ ^[0-9a-f]{64}$ ]] ||
    fail "production browser readiness requires an exact storage-state digest"
  execute_override_value="SCRIBE_BROWSER_EXPECTED_SECRET_VERSION=${expected_secret_version},SCRIBE_BROWSER_EXPECTED_STORAGE_STATE_SHA256=${expected_state_sha256}"
elif [[ "$kind" == "browser" && "$preflight_only" != true \
  && (-n "${SCRIBE_BROWSER_EXPECTED_SECRET_VERSION:-}" || -n "${SCRIBE_BROWSER_EXPECTED_STORAGE_STATE_SHA256:-}") ]]; then
  fail "preview browser readiness must not receive production session metadata"
fi
if [[ "$preflight_only" != true ]]; then
  [[ "$diagnostics_file" == *.log && "$diagnostics_file" != *$'\n'* ]] ||
    fail "DIAGNOSTICS_FILE must be a single-line .log path"
  diagnostics_dir="$(dirname -- "$diagnostics_file")"
  [[ -d "$diagnostics_dir" ]] ||
    fail "DIAGNOSTICS_FILE directory must already exist"
  [[ ! -L "$diagnostics_file" ]] ||
    fail "DIAGNOSTICS_FILE must not be a symbolic link"
fi

for command in gcloud install jq mktemp timeout; do
  require_command "$command"
done

umask 077
temp_dir="$(mktemp -d "${TMPDIR:-/tmp}/scribe-cloud-run-readiness.XXXXXX")"
execution_marker="readiness-${BASHPID}-${temp_dir##*.}"
[[ "$execution_marker" =~ ^readiness-[1-9][0-9]*-[A-Za-z0-9]{6}$ ]] ||
  fail "could not construct a bounded execution marker"
execute_override_value+="${execute_override_value:+,}SCRIBE_READINESS_EXECUTION_ID=${execution_marker}"
execute_overrides=("--update-env-vars=${execute_override_value}")
execute_stdout="$temp_dir/execute.stdout"
execute_stderr="$temp_dir/execute.stderr"
execution_json="$temp_dir/execution.json"
cancel_execution_json="$temp_dir/cancel-execution.json"
recovery_executions_json="$temp_dir/recovery-executions.json"
recovery_execution_names="$temp_dir/recovery-execution-names"
recovery_execution_json="$temp_dir/recovery-execution.json"
preflight_executions_json="$temp_dir/preflight-executions.json"
preflight_execution_names="$temp_dir/preflight-execution-names"
preflight_execution_json="$temp_dir/preflight-execution.json"
tasks_json="$temp_dir/tasks.json"
logs_json="$temp_dir/logs.json"
rendered_diagnostics="$temp_dir/diagnostics.log"
execution=""
execution_terminal=false
cleanup_terminal_unconfirmed=false
launch_in_progress=false
pending_signal_status=0

execution_state_from_file() {
  local source_file="$1"
  local expected_execution="${2:-$execution}"

  jq -er --arg expected "$expected_execution" '
    def leaf:
      if type == "string" then split("/") | last else "" end;
    ((.metadata.name // .name // "") | leaf) as $name
    | select($name == $expected)
    | ([((.status.conditions // .conditions // [])[]?)
        | select((.type // "") == "Completed")]
        | first // {}) as $completed
    | (.status.completionTime // .completionTime // "") as $completion_time
    | ((.status.succeededCount // .succeededCount // 0) | tonumber? // 0) as $succeeded
    | ((.status.failedCount // .failedCount // 0) | tonumber? // 0) as $failed
    | ((.status.cancelledCount // .cancelledCount // 0) | tonumber? // 0) as $cancelled
    | (($completed.reason // $completed.executionReason // "")
        | ascii_downcase | gsub("[^a-z0-9]"; "")) as $reason
    | if $reason == "cancelling" then "running"
      elif ($completed.status // "") == "True"
        or ($completed.state // "") == "CONDITION_SUCCEEDED"
      then "succeeded"
      elif ($completed.status // "") == "False"
        or ($completed.state // "") == "CONDITION_FAILED" then
        if $reason == "cancelled"
          or $cancelled > 0
        then "cancelled" else "failed" end
      elif ($completion_time | type) == "string" and $completion_time != "" then
        if $cancelled > 0 then "cancelled"
        elif $failed > 0 then "failed"
        elif $succeeded > 0 then "succeeded"
        else "unknown"
        end
      else "running"
      end
  ' "$source_file" 2>/dev/null
}

list_job_executions() {
  local destination_file="$1"

  timeout --kill-after="${CONTROL_PLANE_KILL_AFTER_SECONDS}s" "${CONTROL_PLANE_CALL_TIMEOUT_SECONDS}s" \
    gcloud run jobs executions list \
      --job "$job" \
      --project "$project" \
      --region "$region" \
      --filter='NOT status.completionTime:*' \
      --sort-by='~metadata.creationTimestamp' \
      --limit "$((MAX_RECOVERY_EXECUTIONS + 1))" \
      --format=json >"$destination_file" 2>/dev/null
}

execution_names_from_list() {
  local source_file="$1"

  jq -r --arg job "$job" --argjson maximum "$MAX_RECOVERY_EXECUTIONS" '
    def leaf:
      if type == "string" then split("/") | last else "" end;
    if type != "array" or length > $maximum then error("invalid execution list")
    else . end
    | .[]
    | ((.metadata.name // .name // "") | leaf)
    | select(startswith($job + "-"))
    | select((ltrimstr($job + "-")) | test("^[a-z0-9]{5}$"))
  ' "$source_file" 2>/dev/null
}

resolve_execution_from_marker() {
  local attempt candidate matches=0 matched=""
  local -a candidates=()

  for ((attempt = 1; attempt <= LAUNCH_IDENTITY_RECOVERY_ATTEMPTS; attempt++)); do
    if list_job_executions "$recovery_executions_json"; then
      if ! execution_names_from_list "$recovery_executions_json" \
        >"$recovery_execution_names"; then
        continue
      fi
      mapfile -t candidates <"$recovery_execution_names"

      matches=0
      matched=""
      for candidate in "${candidates[@]}"; do
        if timeout --kill-after="${CONTROL_PLANE_KILL_AFTER_SECONDS}s" "${CONTROL_PLANE_CALL_TIMEOUT_SECONDS}s" \
          gcloud run jobs executions describe "$candidate" \
            --project "$project" \
            --region "$region" \
            --format=json >"$recovery_execution_json" 2>/dev/null \
          && jq -e --arg expected "$candidate" --arg marker "$execution_marker" '
            def leaf:
              if type == "string" then split("/") | last else "" end;
            ((.metadata.name // .name // "") | leaf) == $expected
            and ([
              (.spec.template.spec.containers // [])[]?.env[]?,
              (.spec.template.containers // [])[]?.env[]?,
              (.template.containers // [])[]?.env[]?
            ] | any(.name == "SCRIBE_READINESS_EXECUTION_ID" and .value == $marker))
          ' "$recovery_execution_json" >/dev/null 2>&1; then
          matches=$((matches + 1))
          matched="$candidate"
        fi
      done

      if ((matches == 1)); then
        execution="$matched"
        return 0
      fi
      if ((matches > 1)); then
        return 1
      fi
    fi
    if ((attempt < LAUNCH_IDENTITY_RECOVERY_ATTEMPTS)); then
      sleep "$EXECUTION_POLL_SECONDS"
    fi
  done

  return 1
}

settle_execution_and_wait() {
  local cancellation_deadline state consecutive_query_failures=0

  [[ -n "$execution" && "$execution_terminal" != true ]] || return 0
  if [[ "$kind" == "browser" ]]; then
    printf 'Waiting for Cloud Run browser readiness execution %s to finish cleanup.\n' \
      "$execution" >&2
  else
    printf 'Cancelling Cloud Run %s readiness execution %s.\n' "$kind" "$execution" >&2
    timeout --kill-after="${CONTROL_PLANE_KILL_AFTER_SECONDS}s" "${CONTROL_PLANE_CALL_TIMEOUT_SECONDS}s" \
      gcloud run jobs executions cancel "$execution" \
        --project "$project" \
        --region "$region" \
        --async \
        --quiet >/dev/null 2>/dev/null || true
  fi

  cancellation_deadline=$((SECONDS + CANCELLATION_WAIT_SECONDS))
  while ((SECONDS < cancellation_deadline)); do
    if timeout --kill-after="${CONTROL_PLANE_KILL_AFTER_SECONDS}s" "${CONTROL_PLANE_CALL_TIMEOUT_SECONDS}s" \
      gcloud run jobs executions describe "$execution" \
        --project "$project" \
        --region "$region" \
        --format=json >"$cancel_execution_json" 2>/dev/null; then
      state="$(execution_state_from_file "$cancel_execution_json")" || state="unknown"
      case "$state" in
        succeeded | failed | cancelled)
          execution_terminal=true
          printf 'Cloud Run %s readiness execution %s reached %s.\n' \
            "$kind" "$execution" "$state" >&2
          return 0
          ;;
        running) consecutive_query_failures=0 ;;
        *) consecutive_query_failures=$((consecutive_query_failures + 1)) ;;
      esac
    else
      consecutive_query_failures=$((consecutive_query_failures + 1))
    fi
    if ((consecutive_query_failures >= MAX_CONSECUTIVE_CANCELLATION_QUERY_FAILURES)); then
      printf 'Cloud Run %s readiness terminal state remains unconfirmed for %s.\n' \
        "$kind" "$execution" >&2
      return 1
    fi
    sleep "$EXECUTION_POLL_SECONDS"
  done

  printf 'Cloud Run %s readiness terminal state remains unconfirmed for %s.\n' \
    "$kind" "$execution" >&2
  return 1
}

drain_prior_executions() {
  local candidate pass state
  local -a candidates=()

  for ((pass = 1; pass <= MAX_PREFLIGHT_PASSES; pass++)); do
    list_job_executions "$preflight_executions_json" || return 1
    execution_names_from_list "$preflight_executions_json" \
      >"$preflight_execution_names" || return 1
    mapfile -t candidates <"$preflight_execution_names"
    ((${#candidates[@]} == 0)) && return 0

    for candidate in "${candidates[@]}"; do
      if ! timeout --kill-after="${CONTROL_PLANE_KILL_AFTER_SECONDS}s" "${CONTROL_PLANE_CALL_TIMEOUT_SECONDS}s" \
        gcloud run jobs executions describe "$candidate" \
          --project "$project" \
          --region "$region" \
          --format=json >"$preflight_execution_json" 2>/dev/null; then
        return 1
      fi
      state="$(execution_state_from_file "$preflight_execution_json" "$candidate")" || state="unknown"
      case "$state" in
        succeeded | failed | cancelled) ;;
        running)
          execution="$candidate"
          execution_terminal=false
          if ! settle_execution_and_wait; then
            cleanup_terminal_unconfirmed=true
            return 1
          fi
          execution=""
          execution_terminal=false
          ;;
        *) return 1 ;;
      esac
    done
  done

  return 1
}

# shellcheck disable=SC2329 # Invoked through the EXIT trap below.
cleanup() {
  local exit_status=$?
  local cancellation_status=0

  trap - EXIT INT TERM
  if [[ "$cleanup_terminal_unconfirmed" == true ]]; then
    cancellation_status=1
  else
    settle_execution_and_wait || cancellation_status=$?
  fi
  rm -rf -- "$temp_dir"
  if ((cancellation_status != 0)); then
    printf 'Cloud Run %s readiness cleanup failed: execution %s may still be running.\n' \
      "$kind" "$execution" >&2
    exit 126
  fi
  exit "$exit_status"
}

# shellcheck disable=SC2329 # Invoked through the INT and TERM traps below.
handle_signal() {
  local signal_status="$1"

  pending_signal_status="$signal_status"
  if [[ "$launch_in_progress" != true ]]; then
    exit "$signal_status"
  fi
}

trap cleanup EXIT
trap 'handle_signal 130' INT
trap 'handle_signal 143' TERM

if ! drain_prior_executions; then
  printf 'Cloud Run %s readiness preflight could not confirm a clear execution set for %s.\n' \
    "$kind" "$job" >&2
  exit 126
fi
if ((pending_signal_status != 0)); then
  exit "$pending_signal_status"
fi
if [[ "$preflight_only" == true ]]; then
  printf 'Cloud Run %s readiness preflight is clear for %s.\n' "$kind" "$job"
  exit 0
fi

printf 'Running Cloud Run %s readiness job %s.\n' "$kind" "$job"
launch_in_progress=true
set +e
timeout \
  --signal=TERM \
  --kill-after="${CONTROL_PLANE_KILL_AFTER_SECONDS}s" \
  "${CONTROL_PLANE_CALL_TIMEOUT_SECONDS}s" \
  gcloud run jobs execute "$job" \
    --project "$project" \
    --region "$region" \
    "${execute_overrides[@]}" \
    --async \
    --format='value(metadata.name)' \
    --quiet >"$execute_stdout" 2>"$execute_stderr"
execute_status=$?
set -e

execution_wait_status="not-started"
execution_candidate="$(<"$execute_stdout")"
if [[ -n "$execution_candidate" ]]; then
  execution_leaf="${execution_candidate##*/}"
  canonical_execution="projects/${project}/locations/${region}/jobs/${job}/executions/${execution_leaf}"
  if [[ ${#execution_candidate} -le 512 \
    && "$execution_candidate" != *$'\n'* \
    && "$execution_candidate" != *$'\r'* \
    && "$execution_leaf" =~ ^${job}-[a-z0-9]{5}$ \
    && ("$execution_candidate" == "$execution_leaf" \
      || "$execution_candidate" == "$canonical_execution") ]]; then
    execution="$execution_leaf"
    execution_wait_status="running"
  else
    execute_status=125
    execution_wait_status="invalid-identity"
  fi
fi

if [[ -z "$execution" ]]; then
  if resolve_execution_from_marker; then
    execution_wait_status="launch-interrupted"
    ((execute_status == 0)) && execute_status=125
  fi
fi
if [[ -z "$execution" ]]; then
  [[ "$execution_wait_status" != "not-started" ]] ||
    execution_wait_status="identity-unavailable"
  ((execute_status != 0)) || execute_status=125
fi

launch_in_progress=false
if ((pending_signal_status != 0)); then
  exit "$pending_signal_status"
fi

if [[ -n "$execution" ]]; then
  wait_deadline=$((SECONDS + EXECUTION_WAIT_SECONDS))
  consecutive_query_failures=0
  while ((SECONDS < wait_deadline)); do
    if timeout --kill-after="${CONTROL_PLANE_KILL_AFTER_SECONDS}s" "${CONTROL_PLANE_CALL_TIMEOUT_SECONDS}s" \
      gcloud run jobs executions describe "$execution" \
        --project "$project" \
        --region "$region" \
        --format=json >"$execution_json" 2>/dev/null; then
      execution_state="$(execution_state_from_file "$execution_json")" || execution_state="unknown"
      if [[ "$execution_state" == "unknown" ]]; then
        consecutive_query_failures=$((consecutive_query_failures + 1))
      else
        consecutive_query_failures=0
      fi
      case "$execution_state" in
        succeeded)
          execution_terminal=true
          printf 'Cloud Run %s readiness passed for %s.\n' "$kind" "$job"
          exit 0
          ;;
        failed | cancelled)
          execution_terminal=true
          execution_wait_status="$execution_state"
          execute_status=1
          break
          ;;
      esac
    else
      consecutive_query_failures=$((consecutive_query_failures + 1))
    fi

    if ((consecutive_query_failures >= MAX_CONSECUTIVE_QUERY_FAILURES)); then
      execution_wait_status="control-plane-unavailable"
      execute_status=125
      break
    fi
    sleep "$EXECUTION_POLL_SECONDS"
  done

  if [[ "$execution_wait_status" == "running" ]]; then
    execution_wait_status="timeout"
    execute_status=124
  fi
fi

if [[ -n "$execution" && "$execution_terminal" != true ]]; then
  if ! settle_execution_and_wait; then
    cleanup_terminal_unconfirmed=true
  fi
fi

{
  printf 'Scribe Cloud Run readiness diagnostics (typed fields and exact allowlisted markers only)\n'
  printf '[readiness] kind=%s\n' "$kind"
  printf '[readiness] job=%s\n' "$job"
  printf '[readiness] execute_status=%s\n' "$execute_status"
  printf '[status] execution_wait=%s\n' "$execution_wait_status"

  if [[ -z "$execution" ]]; then
    printf '[status] execution_identity=unavailable\n'
  else
    printf '[readiness] execution=%s\n' "$execution"
    printf '[status] execution_identity=ok\n'
  fi
} >"$rendered_diagnostics"

if [[ -n "$execution" ]]; then
  if timeout --kill-after="${CONTROL_PLANE_KILL_AFTER_SECONDS}s" "${CONTROL_PLANE_CALL_TIMEOUT_SECONDS}s" \
    gcloud run jobs executions describe "$execution" \
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

  if timeout --kill-after="${CONTROL_PLANE_KILL_AFTER_SECONDS}s" "${CONTROL_PLANE_CALL_TIMEOUT_SECONDS}s" \
    gcloud run jobs executions tasks list \
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
  if timeout --kill-after="${CONTROL_PLANE_KILL_AFTER_SECONDS}s" "${CONTROL_PLANE_CALL_TIMEOUT_SECONDS}s" \
    gcloud logging read "$log_filter" \
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
