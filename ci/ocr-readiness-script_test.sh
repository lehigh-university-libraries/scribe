#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TEST_DIR="$(mktemp -d "${TMPDIR:-/tmp}/scribe-ocr-readiness-script-test.XXXXXX")"
trap 'rm -rf -- "$TEST_DIR"' EXIT
mkdir -p "$TEST_DIR/bin" "$TEST_DIR/probe-tmp" "$TEST_DIR/state"

fail() {
  echo "OCR readiness script contract failed: $*" >&2
  exit 1
}

probe_script="$ROOT_DIR/scripts/ocr-readiness.sh"

cat >"$TEST_DIR/bin/curl" <<'FAKE_CURL'
#!/usr/bin/env bash

set -euo pipefail

output_file=""
audience=""
request_url=""
request_body=""
while (($# > 0)); do
  case "$1" in
    --output)
      output_file="$2"
      shift 2
      ;;
    --data-urlencode)
      case "$2" in
        audience=*) audience="${2#audience=}" ;;
      esac
      shift 2
      ;;
    --data-binary)
      request_body="${2#@}"
      shift 2
      ;;
    --noproxy | --proto | --connect-timeout | --max-time | --header)
      shift 2
      ;;
    --fail | --silent | --get)
      shift
      ;;
    http://* | https://*)
      request_url="$1"
      shift
      ;;
    *)
      printf 'FAKE_CURL_ARGUMENT_SECRET_SENTINEL: %s\n' "$1" >&2
      exit 92
      ;;
  esac
done

case "$request_url" in
  http://metadata.google.internal/*)
    kind=token
    state_key="token-$(printf '%s' "$audience" | tr -c 'A-Za-z0-9' '_')"
    ;;
  */v1/segment)
    kind=segment
    state_key=segment
    ;;
  */v1/transcribe)
    kind=transcribe
    state_key=transcribe
    ;;
  */api/generate)
    kind=ollama
    state_key=ollama
    ;;
  *)
    printf '%s\n' 'FAKE_CURL_URL_SECRET_SENTINEL' >&2
    exit 93
    ;;
esac

counter_file="$MOCK_STATE_DIR/$state_key.count"
count=0
if [[ -f "$counter_file" ]]; then
  read -r count <"$counter_file"
fi
count=$((count + 1))
printf '%s\n' "$count" >"$counter_file"
printf '%s %s\n' "$kind" "$count" >>"$MOCK_CURL_LOG"

if [[ "${MOCK_ALWAYS_FAIL_STAGE:-}" == "$kind" ]]; then
  printf '%s\n' 'CURL_FAILURE_SECRET_SENTINEL' >&2
  printf '%s\n' 'CURL_FAILURE_BODY_SECRET_SENTINEL' >"$output_file"
  exit 22
fi
if [[ "${MOCK_ALWAYS_TIMEOUT_STAGE:-}" == "$kind" ]]; then
  printf '%s\n' 'CURL_TIMEOUT_SECRET_SENTINEL' >&2
  printf '%s\n' 'CURL_TIMEOUT_BODY_SECRET_SENTINEL' >"$output_file"
  exit 28
fi
if [[ "${MOCK_TRANSIENT_ONCE:-false}" == true && "$count" -eq 1 ]]; then
  printf '%s\n' 'CURL_TRANSIENT_SECRET_SENTINEL' >&2
  printf '%s\n' 'CURL_TRANSIENT_BODY_SECRET_SENTINEL' >"$output_file"
  exit 22
fi

if [[ "${MOCK_BAD_CONTRACT_STAGE:-}" == "$kind" ]]; then
  printf '%s\n' 'CONTRACT_RESPONSE_SECRET_SENTINEL' >"$output_file"
  exit 0
fi

case "$kind" in
  token)
    printf '%s' 'header.payload.signature' >"$output_file"
    ;;
  segment)
    grep -aFq "$SEGMENTATION_MODEL" "$request_body"
    segment_provider="$SEGMENTATION_MODEL"
    if [[ "${MOCK_WRONG_SEGMENT_PROVIDER:-false}" == true ]]; then
      segment_provider=unexpected-provider
    fi
    printf '{"provider":"%s","words":[{"text":"hello"}]}\n' "$segment_provider" >"$output_file"
    ;;
  transcribe)
    grep -aFq "$TRANSCRIPTION_MODEL" "$request_body"
    printf '{"model":"%s","text":"hello"}\n' "$TRANSCRIPTION_MODEL" >"$output_file"
    ;;
  ollama)
    printf '%s\n' '{"response":"hello","done":true}' >"$output_file"
    ;;
esac
FAKE_CURL
chmod +x "$TEST_DIR/bin/curl"

cat >"$TEST_DIR/bin/sleep" <<'FAKE_SLEEP'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$1" >>"$MOCK_SLEEP_LOG"
FAKE_SLEEP
chmod +x "$TEST_DIR/bin/sleep"

export PATH="$TEST_DIR/bin:$PATH"
export TMPDIR="$TEST_DIR/probe-tmp"
export MOCK_STATE_DIR="$TEST_DIR/state"
export MOCK_CURL_LOG="$TEST_DIR/curl.log"
export MOCK_SLEEP_LOG="$TEST_DIR/sleep.log"
export SEGMENTOR_URL=https://segment.example
export TRANSCRIBER_URL=https://transcribe.example
export SEGMENTATION_MODEL=layout-lines-v2
export TRANSCRIPTION_MODEL=catmus-print-fondue-large.mlmodel
export OLLAMA_URL=https://ollama.example
export OLLAMA_MODEL=glm-ocr:bf16
export SMOKE_IMAGE_BASE64
SMOKE_IMAGE_BASE64="$(<"$ROOT_DIR/config/readiness-smoke.png.base64")"

run_probe() {
  : >"$MOCK_CURL_LOG"
  : >"$MOCK_SLEEP_LOG"
  find "$MOCK_STATE_DIR" -type f -delete
  set +e
  "$@" "$probe_script" >"$TEST_DIR/probe.out" 2>"$TEST_DIR/probe.err"
  PROBE_STATUS=$?
  set -e
}

run_probe env MOCK_TRANSIENT_ONCE=true
[[ "$PROBE_STATUS" -eq 0 ]] ||
  fail "transient identity and service failures were not retried"
[[ ! -s "$TEST_DIR/probe.out" && ! -s "$TEST_DIR/probe.err" ]] ||
  fail "a recovered transient failure emitted raw diagnostics"
[[ "$(grep -c '^token ' "$MOCK_CURL_LOG")" -eq 6 ]] ||
  fail "identity-token retries were not bounded to one retry per service"
for service in segment transcribe ollama; do
  [[ "$(grep -c "^$service " "$MOCK_CURL_LOG")" -eq 2 ]] ||
    fail "$service was not retried exactly once after a transient failure"
done
[[ "$(wc -l <"$MOCK_SLEEP_LOG")" -eq 6 ]] ||
  fail "retry delays did not cover each transient identity and service failure"

run_probe env MOCK_ALWAYS_FAIL_STAGE=token
[[ "$PROBE_STATUS" -eq 1 ]] ||
  fail "an exhausted identity-token retry did not fail the readiness probe"
[[ "$(cat "$TEST_DIR/probe.err")" == 'ocr readiness failed: segment-token' ]] ||
  fail "identity-token exhaustion did not emit its exact safe stage marker"
[[ "$(grep -c '^token ' "$MOCK_CURL_LOG")" -eq 6 ]] ||
  fail "identity-token retry attempts are not bounded"

run_probe env MOCK_ALWAYS_FAIL_STAGE=segment
[[ "$PROBE_STATUS" -eq 1 ]] ||
  fail "an exhausted segment request retry did not fail the readiness probe"
[[ "$(cat "$TEST_DIR/probe.err")" == 'ocr readiness failed: segment-request' ]] ||
  fail "request exhaustion did not emit its exact safe stage marker"
[[ "$(grep -c '^segment ' "$MOCK_CURL_LOG")" -eq 2 ]] ||
  fail "segment request retry attempts are not bounded"

run_probe env MOCK_ALWAYS_TIMEOUT_STAGE=transcribe
[[ "$PROBE_STATUS" -eq 1 ]] ||
  fail "an exhausted transcription timeout retry did not fail the readiness probe"
[[ "$(cat "$TEST_DIR/probe.err")" == 'ocr readiness failed: transcribe-timeout' ]] ||
  fail "request timeout exhaustion did not emit its exact safe stage marker"
[[ "$(grep -c '^transcribe ' "$MOCK_CURL_LOG")" -eq 2 ]] ||
  fail "transcription timeout retry attempts are not bounded"

run_probe env MOCK_BAD_CONTRACT_STAGE=segment
[[ "$PROBE_STATUS" -eq 1 ]] ||
  fail "an invalid successful response did not fail the readiness probe"
[[ "$(cat "$TEST_DIR/probe.err")" == 'ocr readiness failed: segment-contract' ]] ||
  fail "invalid response content did not emit its exact safe stage marker"
[[ "$(grep -c '^segment ' "$MOCK_CURL_LOG")" -eq 1 ]] ||
  fail "a deterministic response contract failure was unnecessarily retried"

run_probe env MOCK_WRONG_SEGMENT_PROVIDER=true
[[ "$PROBE_STATUS" -eq 1 ]] ||
  fail "segmentation readiness accepted output from the wrong model route"
[[ "$(cat "$TEST_DIR/probe.err")" == 'ocr readiness failed: segment-contract' ]] ||
  fail "a wrong segmentation provider did not emit its exact safe stage marker"
[[ "$(grep -c '^segment ' "$MOCK_CURL_LOG")" -eq 1 ]] ||
  fail "a wrong segmentation provider was unnecessarily retried"

run_probe env SMOKE_IMAGE_BASE64=not-base64
[[ "$PROBE_STATUS" -eq 1 ]] ||
  fail "an invalid smoke image did not fail the readiness probe"
[[ "$(cat "$TEST_DIR/probe.err")" == 'ocr readiness failed: image-contract' ]] ||
  fail "an invalid smoke image did not emit its exact safe stage marker"
[[ ! -s "$MOCK_CURL_LOG" ]] ||
  fail "an invalid smoke image reached the metadata or OCR services"

if rg -n \
  'SECRET_SENTINEL|header\.payload\.signature|Authorization: Bearer|https://[^ ]+' \
  "$TEST_DIR/probe.out" "$TEST_DIR/probe.err" >/dev/null; then
  fail "the readiness probe exposed a token, URL, response, or transport detail"
fi

if rg -n 'wget|--show-error' "$probe_script" >/dev/null; then
  fail "the readiness probe can emit unbounded transport diagnostics"
fi
# shellcheck disable=SC2016 # Match the literal runtime variable in the probe script.
for option in \
  "--connect-timeout 2" \
  '--max-time "$TOKEN_REQUEST_TIMEOUT_SECONDS"' \
  "--connect-timeout 5" \
  '--max-time "$request_timeout"' \
  'readonly TOKEN_MAX_ATTEMPTS=6' \
  'readonly OLLAMA_REQUEST_TIMEOUT_SECONDS=120'; do
  grep -Fq -- "$option" "$probe_script" ||
    fail "the readiness probe is missing its bounded retry/timeout contract: $option"
done

read_constant() {
  local name="$1"
  local value
  value="$(sed -nE "s/^readonly ${name}=([0-9]+)$/\\1/p" "$probe_script")"
  [[ "$value" =~ ^[1-9][0-9]*$ ]] ||
    fail "the readiness probe has an invalid numeric constant: $name"
  printf '%s\n' "$value"
}

token_attempts="$(read_constant TOKEN_MAX_ATTEMPTS)"
token_timeout="$(read_constant TOKEN_REQUEST_TIMEOUT_SECONDS)"
token_delay="$(read_constant TOKEN_RETRY_DELAY_SECONDS)"
segment_attempts="$(read_constant SEGMENT_MAX_ATTEMPTS)"
segment_timeout="$(read_constant SEGMENT_REQUEST_TIMEOUT_SECONDS)"
transcribe_attempts="$(read_constant TRANSCRIBE_MAX_ATTEMPTS)"
transcribe_timeout="$(read_constant TRANSCRIBE_REQUEST_TIMEOUT_SECONDS)"
ollama_attempts="$(read_constant OLLAMA_MAX_ATTEMPTS)"
ollama_timeout="$(read_constant OLLAMA_REQUEST_TIMEOUT_SECONDS)"
service_delay="$(read_constant SERVICE_RETRY_DELAY_SECONDS)"
[[ "$segment_attempts" -eq 2 && "$transcribe_attempts" -eq 2 ]] ||
  fail "Kraken readiness must use one initial request and one transient retry"
application_timeout="$(
  sed -nE \
    's/^const InferenceRequestTimeout = ([0-9]+) \* time\.Second$/\1/p' \
    "$ROOT_DIR/internal/segmentor/client.go"
)"
[[ "$application_timeout" =~ ^[1-9][0-9]*$ ]] ||
  fail "the application segmentor timeout is missing or invalid"
[[ "$application_timeout" -eq 240 ]] ||
  fail "the application segmentor timeout must cover bounded scale-to-zero cold inference"
[[ "$segment_timeout" -eq "$application_timeout" ]] ||
  fail "the segmentation readiness timeout differs from the application client timeout"
[[ "$transcribe_timeout" -eq "$application_timeout" ]] ||
  fail "the transcription readiness timeout differs from the application client timeout"

token_budget=$((token_attempts * token_timeout + (token_attempts - 1) * token_delay))
maximum_probe_budget=$((
  3 * token_budget +
    segment_attempts * segment_timeout + (segment_attempts - 1) * service_delay +
    transcribe_attempts * transcribe_timeout + (transcribe_attempts - 1) * service_delay +
    ollama_attempts * ollama_timeout + (ollama_attempts - 1) * service_delay
))
[[ "$maximum_probe_budget" -eq 1460 ]] ||
  fail "the documented maximum retry/transfer budget drifted to ${maximum_probe_budget}s"
job_timeout="$(
  sed -n '/^resource "google_cloud_run_v2_job" "ocr_readiness"/,/^}/p' \
    "$ROOT_DIR/terraform/readiness.tf" |
    sed -nE 's/^[[:space:]]*timeout[[:space:]]*=[[:space:]]*"([0-9]+)s"$/\1/p'
)"
[[ "$job_timeout" =~ ^[1-9][0-9]*$ ]] ||
  fail "the OCR Cloud Run job timeout is missing or invalid"
[[ "$((job_timeout - maximum_probe_budget))" -ge 300 ]] ||
  fail "the OCR Cloud Run job lacks five minutes of control-plane and shell headroom"
backend_job_timeout="$(
  sed -n '/^resource "google_cloud_run_v2_job" "backend_readiness"/,/^}/p' \
    "$ROOT_DIR/terraform/readiness.tf" |
    sed -nE 's/^[[:space:]]*timeout[[:space:]]*=[[:space:]]*"([0-9]+)s"$/\1/p'
)"
[[ "$backend_job_timeout" =~ ^[1-9][0-9]*$ ]] ||
  fail "the backend Cloud Run job timeout is missing or invalid"
browser_job_timeout="$(
  sed -n '/^resource "google_cloud_run_v2_job" "browser_readiness"/,/^}/p' \
    "$ROOT_DIR/terraform/readiness.tf" |
    sed -nE 's/^[[:space:]]*timeout[[:space:]]*=[[:space:]]*"([0-9]+)s"$/\1/p'
)"
[[ "$browser_job_timeout" =~ ^[1-9][0-9]*$ ]] ||
  fail "the browser Cloud Run job timeout is missing or invalid"
deploy_timeout_expression="$(
  sed -nE 's/^[[:space:]]*timeout-minutes:[[:space:]]*(.+)$/\1/p' \
    "$ROOT_DIR/.github/workflows/terraform-deploy.yaml"
)"
readonly EXPECTED_DEPLOY_TIMEOUT_EXPRESSION="\${{ inputs.mode == 'destroy' && 180 || inputs.mode == 'apply' && inputs.environment_name == 'production' && 240 || 120 }}"
[[ "$deploy_timeout_expression" == "$EXPECTED_DEPLOY_TIMEOUT_EXPRESSION" ]] ||
  fail "the reusable deploy workflow does not isolate its extended destroy and production recovery timeouts"
readonly preview_deploy_timeout_minutes=120
readonly production_deploy_timeout_minutes=240
readonly DEPLOY_CONTROL_PLANE_HEADROOM_SECONDS=1800
minimum_production_deploy_budget=$((
  2 * (backend_job_timeout + job_timeout) +
    2 * browser_job_timeout +
    DEPLOY_CONTROL_PLANE_HEADROOM_SECONDS
))
[[ "$((production_deploy_timeout_minutes * 60))" -ge "$minimum_production_deploy_budget" ]] ||
  fail "the reusable deploy workflow cannot fence production browser work and complete rollback readiness plus control-plane work"
minimum_preview_deploy_budget=$((
  backend_job_timeout + job_timeout + browser_job_timeout +
    DEPLOY_CONTROL_PLANE_HEADROOM_SECONDS
))
[[ "$((preview_deploy_timeout_minutes * 60))" -ge "$minimum_preview_deploy_budget" ]] ||
  fail "the reusable deploy workflow cannot complete preview readiness plus control-plane work"

deploy_workflow="$ROOT_DIR/.github/workflows/terraform-deploy.yaml"
grep -F -- '- name: Fence prior production browser execution' "$deploy_workflow" >/dev/null ||
  fail "production apply does not fence a prior browser execution before rollout"
grep -F -- "if: inputs.mode == 'apply' && inputs.pr_number == ''" "$deploy_workflow" >/dev/null ||
  fail "the prior browser fence is not production-apply-only"
# shellcheck disable=SC2016 # Match literal workflow shell source.
grep -F -- 'if ! terraform -chdir=terraform output -json >"$prior_outputs" 2>/dev/null; then' "$deploy_workflow" >/dev/null ||
  fail "the prior browser fence does not fail closed when production state cannot be read"
# shellcheck disable=SC2016 # Match literal workflow shell source.
grep -F -- 'prior_browser_job="$(jq -r '\''.browser_readiness_job.value'\'' "$prior_outputs")"' "$deploy_workflow" >/dev/null ||
  fail "the prior browser fence does not resolve the exact state-owned job"
# shellcheck disable=SC2016 # Match literal workflow and GitHub expressions.
grep -F -- 'if [ '\''${{ steps.previous.outputs.available }}'\'' = true ] && [ -n "$previous_browser_image" ]; then' "$deploy_workflow" >/dev/null ||
  fail "the prior browser fence does not reject an empty job identity for a recorded runner"
grep -F -- 'The prior production browser output is missing from non-historical state.' "$deploy_workflow" >/dev/null ||
  fail "the prior browser fence accepts a missing job output outside historical empty state"
# shellcheck disable=SC2016 # Match literal workflow shell source.
grep -F -- '"$GITHUB_WORKSPACE/ci/run-cloud-run-readiness.sh" --preflight-only "$prior_browser_job" browser' "$deploy_workflow" >/dev/null ||
  fail "the prior browser fence does not use the non-launching readiness preflight"
fence_line="$(rg -n -m1 -- '- name: Fence prior production browser execution' "$deploy_workflow" | cut -d: -f1)"
apply_line="$(rg -n -m1 -- '- name: Run production Terraform$' "$deploy_workflow" | cut -d: -f1)"
[[ "$fence_line" =~ ^[1-9][0-9]*$ && "$apply_line" =~ ^[1-9][0-9]*$ && "$fence_line" -lt "$apply_line" ]] ||
  fail "the prior browser execution is not fenced before production apply"
# shellcheck disable=SC2016 # Match literal workflow shell source.
grep -F -- 'forward_browser_readiness_image="$SCRIBE_BROWSER_READINESS_IMAGE"' "$deploy_workflow" >/dev/null ||
  fail "production rollback does not preserve the reviewed forward browser runner identity"
# shellcheck disable=SC2016 # Match literal workflow shell source.
grep -F -- 'forward_browser_readiness_subnet_cidr="$TF_VAR_browser_readiness_subnet_cidr"' "$deploy_workflow" >/dev/null ||
  fail "production rollback does not preserve the reviewed forward browser subnet identity"
# shellcheck disable=SC2016 # Match literal workflow shell source.
grep -F -- 'if [ -z "$previous_browser_readiness_image" ]; then' "$deploy_workflow" >/dev/null ||
  fail "production rollback does not recognize historical deployment state without a browser runner"
# shellcheck disable=SC2016 # Match literal workflow shell source.
grep -F -- 'SCRIBE_BROWSER_READINESS_IMAGE="$forward_browser_readiness_image"' "$deploy_workflow" >/dev/null ||
  fail "historical first-runner rollback can delete the newly reviewed browser safety graph"
# shellcheck disable=SC2016 # Match literal workflow shell source.
grep -F -- 'TF_VAR_browser_readiness_subnet_cidr="$forward_browser_readiness_subnet_cidr"' "$deploy_workflow" >/dev/null ||
  fail "production rollback can replace the already applied Direct VPC subnet"
grep -F -- 'Keep the already applied' "$deploy_workflow" >/dev/null ||
  fail "production rollback does not document its forward subnet safety exception"
# shellcheck disable=SC2016 # Match literal workflow shell source.
grep -F -- 'SCRIBE_BROWSER_READINESS_IMAGE="$previous_browser_readiness_image"' "$deploy_workflow" >/dev/null ||
  fail "ordinary production rollback does not replay the previously recorded browser runner"

rollback_block="$(sed -n '/^[[:space:]]*- name: Roll back failed production rollout$/,/^[[:space:]]*- name: Read production backup outputs$/p' "$deploy_workflow")"
# shellcheck disable=SC2016 # Match a literal GitHub workflow expression.
grep -F -- 'FORWARD_APPLY_OUTCOME: ${{ steps.apply.outcome }}' <<<"$rollback_block" >/dev/null ||
  fail "production rollback does not distinguish a partial apply from post-apply readiness failure"
# shellcheck disable=SC2016 # Match literal workflow shell source.
grep -F -- 'if terraform -chdir=terraform output -json >"$rollback_outputs" 2>/dev/null; then' <<<"$rollback_block" >/dev/null ||
  fail "production rollback does not parse the current output set as a typed document"
# shellcheck disable=SC2016 # Match literal workflow shell source.
grep -F -- 'if [ "$FORWARD_APPLY_OUTCOME" = success ] && [ -z "$rollback_browser_job" ]; then' <<<"$rollback_block" >/dev/null ||
  fail "post-apply rollback does not fail closed when the current browser job cannot be resolved"
# shellcheck disable=SC2016 # Match literal workflow shell source.
grep -F -- '"$GITHUB_WORKSPACE/ci/run-cloud-run-readiness.sh" --preflight-only "$rollback_browser_job" browser' <<<"$rollback_block" >/dev/null ||
  fail "production rollback does not fence the current browser execution"
rollback_fence_line="$(rg -n -m1 -- 'run-cloud-run-readiness\.sh.*--preflight-only.*rollback_browser_job' <<<"$rollback_block" | cut -d: -f1)"
rollback_apply_line="$(rg -n -m1 -- 'capture-command-log\.sh.*make tf-prod.*ACTION=apply' <<<"$rollback_block" | cut -d: -f1)"
[[ "$rollback_fence_line" =~ ^[1-9][0-9]*$ && "$rollback_apply_line" =~ ^[1-9][0-9]*$ && "$rollback_fence_line" -lt "$rollback_apply_line" ]] ||
  fail "the current browser execution is not fenced immediately before production rollback"

echo "OCR readiness retries, timeouts, response contracts, and redacted stage markers passed."
