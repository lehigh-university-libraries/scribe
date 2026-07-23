#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
decoded="$(mktemp)"
trap 'rm -f "$decoded"' EXIT

base64 --decode < "$ROOT_DIR/config/readiness-smoke.png.base64" > "$decoded"
actual_sha="$(sha256sum "$decoded" | awk '{print $1}')"
[ "$actual_sha" = "2d9ee66d4bbccfaf2d306646bd5b4e6810b584c1ed8f4e9d063476bbba5604ff" ] || {
  echo "readiness fixture digest changed unexpectedly: $actual_sha" >&2
  exit 1
}
description="$(file "$decoded")"
case "$description" in
  *'PNG image data, 640 x 160, 1-bit grayscale'*) ;;
  *) echo "readiness fixture has unexpected format: $description" >&2; exit 1 ;;
esac
# The pinned backend test path performs a full stdlib PNG decode. Keep that
# deeper guard paired with this fast shell-level digest and header contract.
grep -Fq 'func TestReadinessSmokeFixtureFullyDecodes' \
  "$ROOT_DIR/internal/segmentor/service_test.go"
grep -Fq 'png.Decode(bytes.NewReader(decoded))' \
  "$ROOT_DIR/internal/segmentor/service_test.go"

grep -Fq '(.words | length) > 0' "$ROOT_DIR/scripts/ocr-readiness.sh"
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
grep -Fq 'value = local.kraken_default_segmentation_key' "$ROOT_DIR/terraform/readiness.tf"
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
