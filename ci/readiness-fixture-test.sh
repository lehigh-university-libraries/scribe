#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
decoded="$(mktemp)"
embedded_decoded="$(mktemp)"
trap 'rm -f -- "$decoded" "$embedded_decoded"' EXIT

base64 --decode < "$ROOT_DIR/config/readiness-smoke.png.base64" > "$decoded"
actual_sha="$(sha256sum "$decoded" | awk '{print $1}')"
expected_sha="e3f3bb2b5ade3c15af262a76ad58b720e7eb3b3d079802df04f1dd50be917b2d"
[ "$actual_sha" = "$expected_sha" ] || {
  echo "readiness fixture digest changed unexpectedly: $actual_sha" >&2
  exit 1
}
runner="$ROOT_DIR/web/e2e/deployed-readiness.mjs"
embedded_matches="$(grep -Ec '^const readinessSmokeFixtureBase64 = "[A-Za-z0-9+/=]+";$' "$runner" || true)"
[ "$embedded_matches" = "1" ] || {
  echo "deployed readiness must embed exactly one base64 fixture" >&2
  exit 1
}
embedded_base64="$(sed -n 's/^const readinessSmokeFixtureBase64 = "\(.*\)";$/\1/p' "$runner")"
if ! printf '%s' "$embedded_base64" | base64 --decode > "$embedded_decoded"; then
  echo "deployed readiness embedded fixture is not valid base64" >&2
  exit 1
fi
embedded_sha="$(sha256sum "$embedded_decoded" | awk '{print $1}')"
[ "$embedded_sha" = "$expected_sha" ] || {
  echo "deployed readiness embedded fixture digest changed unexpectedly: $embedded_sha" >&2
  exit 1
}
grep -Fq "const readinessSmokeFixtureSHA256 = \"$expected_sha\";" "$runner"
cmp -s "$decoded" "$embedded_decoded" || {
  echo "deployed readiness embedded fixture differs from the committed fixture" >&2
  exit 1
}
description="$(file "$decoded")"
case "$description" in
  *'PNG image data, 640 x 160, 1-bit grayscale'*) ;;
  *) echo "readiness fixture has unexpected format: $description" >&2; exit 1 ;;
esac
for forbidden_chunk in tRNS tIME tEXt zTXt iTXt; do
  if grep -aFq "$forbidden_chunk" "$decoded"; then
    echo "readiness fixture contains forbidden PNG chunk: $forbidden_chunk" >&2
    exit 1
  fi
done
# The pinned backend test path performs a full stdlib PNG decode. Keep that
# deeper guard paired with this fast shell-level digest and header contract.
grep -Fq 'func TestReadinessSmokeFixtureFullyDecodes' \
  "$ROOT_DIR/internal/segmentor/service_test.go"
grep -Fq 'png.Decode(bytes.NewReader(decoded))' \
  "$ROOT_DIR/internal/segmentor/service_test.go"
grep -Fq 'transparentPixels != 0' \
  "$ROOT_DIR/internal/segmentor/service_test.go"

grep -Fq '(.words | length) > 0' "$ROOT_DIR/scripts/ocr-readiness.sh"
# shellcheck disable=SC2016 # Match the literal jq expression in the probe.
grep -Fq '.provider == $model' "$ROOT_DIR/scripts/ocr-readiness.sh"
grep -Fq '(.text | length) > 0' "$ROOT_DIR/scripts/ocr-readiness.sh"
# shellcheck disable=SC2016 # Match the literal runtime model in the probe.
grep -Fq '"$SEGMENTATION_MODEL"' "$ROOT_DIR/scripts/ocr-readiness.sh"
# shellcheck disable=SC2016 # Match the literal runtime expression in the probe.
grep -Fq '"$OLLAMA_URL/api/generate"' "$ROOT_DIR/scripts/ocr-readiness.sh"
grep -Fq 'stream: false' "$ROOT_DIR/scripts/ocr-readiness.sh"
grep -Fq '.done == true' "$ROOT_DIR/scripts/ocr-readiness.sh"
# shellcheck disable=SC2016 # Match the literal Terraform file expression.
grep -Fq 'ocr_readiness_script = file("${local.repo_root}/scripts/ocr-readiness.sh")' \
  "$ROOT_DIR/terraform/readiness.tf"
grep -Fq 'name  = "SEGMENTATION_MODEL"' "$ROOT_DIR/terraform/readiness.tf"
grep -Fq 'value = "tesseract"' "$ROOT_DIR/terraform/readiness.tf"
ocr_readiness_resource="$(
  sed -n '/^resource "google_cloud_run_v2_job" "ocr_readiness"/,/^}/p' \
    "$ROOT_DIR/terraform/readiness.tf"
)"
grep -Eq '^[[:space:]]*count[[:space:]]*=[[:space:]]*1$' <<<"$ocr_readiness_resource" || {
  echo "OCR readiness resource count must not depend on newly-created service URLs" >&2
  exit 1
}
grep -Fq 'google_cloud_run_v2_service_iam_member" "ollama_readiness_invoker"' "$ROOT_DIR/terraform/ollama.tf"
grep -Fq 'name  = "SCRIBE_EXPECTED_BACKEND_IP"' "$ROOT_DIR/terraform/readiness.tf"
grep -Fq 'value = module.scribe.internal_ip' "$ROOT_DIR/terraform/readiness.tf"
bash "$ROOT_DIR/ci/ocr-readiness-script_test.sh"

compose_up_block="$(
  sed -n '/^  docker_compose_up = concat/,/^  docker_compose_down = concat/p' \
    "$ROOT_DIR/terraform/main.tf"
)"
compose_up='format("docker compose -f docker-compose.yaml -f /home/cloud-compose/scribe-runtime.compose.yaml up --no-build --wait --wait-timeout 180 %s", join(" ", local.docker_compose_services)),'
host_readiness="curl --noproxy '*' --fail --silent --show-error --connect-timeout 2 --max-time 10 --output /dev/null http://127.0.0.1/readyz"
host_readiness_count=0
while IFS= read -r line; do
  line="${line#"${line%%[![:space:]]*}"}"
  [[ "$line" == "\"${host_readiness}\"," ]] &&
    host_readiness_count=$((host_readiness_count + 1))
done <<<"$compose_up_block"
[ "$host_readiness_count" -eq 1 ]
compose_up_line="$(grep -nF "$compose_up" <<<"$compose_up_block")"
host_readiness_line="$(grep -nF "$host_readiness" <<<"$compose_up_block")"
[ -n "$compose_up_line" ] && [ -n "$host_readiness_line" ]
[ "${compose_up_line%%:*}" -lt "${host_readiness_line%%:*}" ]

echo "Cloud Run, host, Kraken, and Ollama readiness contracts passed."
