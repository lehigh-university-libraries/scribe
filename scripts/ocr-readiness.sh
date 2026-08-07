#!/bin/sh

set -eu
umask 077

# The current maximum transfer, sleep, and token retry budget is 1460 seconds.
# The focused contract recomputes it and requires at least five minutes of
# headroom below the Cloud Run job timeout.
readonly TOKEN_MAX_ATTEMPTS=6
readonly TOKEN_REQUEST_TIMEOUT_SECONDS=5
readonly TOKEN_RETRY_DELAY_SECONDS=2
readonly SEGMENT_MAX_ATTEMPTS=2
readonly SEGMENT_REQUEST_TIMEOUT_SECONDS=240
readonly TRANSCRIBE_MAX_ATTEMPTS=2
readonly TRANSCRIBE_REQUEST_TIMEOUT_SECONDS=240
readonly OLLAMA_MAX_ATTEMPTS=3
readonly OLLAMA_REQUEST_TIMEOUT_SECONDS=120
readonly SERVICE_RETRY_DELAY_SECONDS=5
readonly SMOKE_IMAGE_SHA256=e3f3bb2b5ade3c15af262a76ad58b720e7eb3b3d079802df04f1dd50be917b2d

fail_stage() {
  # This exact marker vocabulary is the only probe detail allowed into
  # deployment diagnostics. Tokens, URLs, and response bodies stay private.
  printf 'ocr readiness failed: %s\n' "$1" >&2
  exit 1
}

boundary="scribe-readiness-boundary"
work_dir="$(mktemp -d "${TMPDIR:-/tmp}/scribe-ocr-readiness.XXXXXX")" ||
  fail_stage image-contract
trap 'rm -rf -- "$work_dir"' EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

retry_command() (
  max_attempts="$1"
  delay_seconds="$2"
  shift 2
  attempt=1
  while [ "$attempt" -le "$max_attempts" ]; do
    if "$@" >/dev/null 2>&1; then
      exit 0
    fi
    if [ "$attempt" -lt "$max_attempts" ]; then
      sleep "$delay_seconds"
    fi
    attempt=$((attempt + 1))
  done
  exit 1
)

fetch_identity_token_once() (
  audience="$1"
  token_file="$2"
  rm -f -- "$token_file"
  curl --noproxy '*' \
    --proto '=http' \
    --fail \
    --silent \
    --connect-timeout 2 \
    --max-time "$TOKEN_REQUEST_TIMEOUT_SECONDS" \
    --get \
    --data-urlencode "audience=$audience" \
    --data-urlencode 'format=full' \
    --header 'Metadata-Flavor: Google' \
    --output "$token_file" \
    'http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/identity' &&
    grep -Eq '^[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+$' "$token_file"
)

fetch_identity_token() (
  kind="$1"
  audience="$2"
  token_file="$work_dir/$kind.identity-token"
  if ! retry_command \
    "$TOKEN_MAX_ATTEMPTS" \
    "$TOKEN_RETRY_DELAY_SECONDS" \
    fetch_identity_token_once \
    "$audience" \
    "$token_file"; then
    fail_stage "$kind-token"
  fi
)

write_headers() (
  token_file="$1"
  header_file="$2"
  content_type="$3"
  {
    printf 'Authorization: Bearer '
    cat "$token_file"
    printf '\nContent-Type: %s\n' "$content_type"
  } >"$header_file"
)

if ! printf '%s' "$SMOKE_IMAGE_BASE64" |
  base64 -d >"$work_dir/readiness.png" 2>/dev/null; then
  fail_stage image-contract
fi
if [ "$(sha256sum "$work_dir/readiness.png" | awk '{print $1}')" != "$SMOKE_IMAGE_SHA256" ]; then
  fail_stage image-contract
fi

make_body() {
  kind="$1"
  model="$2"
  {
    printf -- '--%s\r\nContent-Disposition: form-data; name="model"\r\n\r\n%s\r\n' "$boundary" "$model"
    printf -- '--%s\r\nContent-Disposition: form-data; name="image"; filename="readiness.png"\r\nContent-Type: image/png\r\n\r\n' "$boundary"
    cat "$work_dir/readiness.png"
    printf '\r\n--%s--\r\n' "$boundary"
  } >"$work_dir/$kind.multipart"
}

multipart_request_once() (
  kind="$1"
  audience="$2"
  path="$3"
  request_timeout="$4"
  rm -f -- "$work_dir/$kind.response"
  curl --noproxy '*' \
    --proto '=https' \
    --fail \
    --silent \
    --connect-timeout 5 \
    --max-time "$request_timeout" \
    --header "@$work_dir/$kind.headers" \
    --data-binary "@$work_dir/$kind.multipart" \
    --output "$work_dir/$kind.response" \
    "$audience$path"
)

validate_multipart_response() (
  kind="$1"
  model="$2"
  case "$kind" in
    segment)
      jq -e \
        --arg model "$model" \
        '.provider == $model and (.words | type) == "array" and (.words | length) > 0' \
        "$work_dir/$kind.response" >/dev/null
      ;;
    transcribe)
      jq -e \
        --arg model "$model" \
        '.model == $model and (.text | type) == "string" and (.text | length) > 0' \
        "$work_dir/$kind.response" >/dev/null
      ;;
    *) exit 2 ;;
  esac
)

probe_multipart() (
  kind="$1"
  audience="$2"
  path="$3"
  model="$4"
  max_attempts="$5"
  request_timeout="$6"

  if ! printf '%s' "$model" | grep -Eq '^[A-Za-z0-9._:/-]+$'; then
    fail_stage "$kind-contract"
  fi
  fetch_identity_token "$kind" "$audience"
  if ! make_body "$kind" "$model" >/dev/null 2>&1 ||
    ! write_headers \
      "$work_dir/$kind.identity-token" \
      "$work_dir/$kind.headers" \
      "multipart/form-data; boundary=$boundary" >/dev/null 2>&1; then
    fail_stage "$kind-contract"
  fi

  attempt=1
  last_failure=request
  while [ "$attempt" -le "$max_attempts" ]; do
    if multipart_request_once \
      "$kind" \
      "$audience" \
      "$path" \
      "$request_timeout" >/dev/null 2>&1; then
      if validate_multipart_response "$kind" "$model" >/dev/null 2>&1; then
        exit 0
      fi
      fail_stage "$kind-contract"
    else
      request_status=$?
      if [ "$request_status" -eq 28 ]; then
        last_failure=timeout
      else
        last_failure=request
      fi
    fi
    if [ "$attempt" -lt "$max_attempts" ]; then
      sleep "$SERVICE_RETRY_DELAY_SECONDS"
    fi
    attempt=$((attempt + 1))
  done
  fail_stage "$kind-$last_failure"
)

ollama_request_once() (
  rm -f -- "$work_dir/ollama.response"
  curl --noproxy '*' \
    --proto '=https' \
    --fail \
    --silent \
    --connect-timeout 5 \
    --max-time "$OLLAMA_REQUEST_TIMEOUT_SECONDS" \
    --header "@$work_dir/ollama.headers" \
    --data-binary "@$work_dir/ollama.json" \
    --output "$work_dir/ollama.response" \
    "$OLLAMA_URL/api/generate"
)

validate_ollama_response() {
  jq -e \
    '(.response | type) == "string" and (.response | length) > 0 and .done == true' \
    "$work_dir/ollama.response" >/dev/null
}

probe_ollama() (
  if ! printf '%s' "$OLLAMA_MODEL" | grep -Eq '^[A-Za-z0-9._:/-]+$'; then
    fail_stage ollama-contract
  fi
  fetch_identity_token ollama "$OLLAMA_URL"
  if ! jq -n \
    --arg model "$OLLAMA_MODEL" \
    --arg image "$SMOKE_IMAGE_BASE64" \
    '{
      model: $model,
      prompt: "Transcribe the visible text. Return only the text.",
      images: [$image],
      stream: false
    }' >"$work_dir/ollama.json" 2>/dev/null ||
    ! write_headers \
      "$work_dir/ollama.identity-token" \
      "$work_dir/ollama.headers" \
      'application/json' >/dev/null 2>&1; then
    fail_stage ollama-contract
  fi

  attempt=1
  last_failure=request
  while [ "$attempt" -le "$OLLAMA_MAX_ATTEMPTS" ]; do
    if ollama_request_once >/dev/null 2>&1; then
      if validate_ollama_response >/dev/null 2>&1; then
        exit 0
      fi
      fail_stage ollama-contract
    else
      request_status=$?
      if [ "$request_status" -eq 28 ]; then
        last_failure=timeout
      else
        last_failure=request
      fi
    fi
    if [ "$attempt" -lt "$OLLAMA_MAX_ATTEMPTS" ]; then
      sleep "$SERVICE_RETRY_DELAY_SECONDS"
    fi
    attempt=$((attempt + 1))
  done
  fail_stage "ollama-$last_failure"
)

probe_multipart \
  segment \
  "$SEGMENTOR_URL" \
  /v1/segment \
  "$SEGMENTATION_MODEL" \
  "$SEGMENT_MAX_ATTEMPTS" \
  "$SEGMENT_REQUEST_TIMEOUT_SECONDS"
probe_multipart \
  transcribe \
  "$TRANSCRIBER_URL" \
  /v1/transcribe \
  "$TRANSCRIPTION_MODEL" \
  "$TRANSCRIBE_MAX_ATTEMPTS" \
  "$TRANSCRIBE_REQUEST_TIMEOUT_SECONDS"
if [ -n "$OLLAMA_URL" ]; then
  probe_ollama
fi
