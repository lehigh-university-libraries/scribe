#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
decoded="$(mktemp)"
trap 'rm -f "$decoded"' EXIT

base64 --decode < "$ROOT_DIR/config/readiness-smoke.png.base64" > "$decoded"
actual_sha="$(sha256sum "$decoded" | awk '{print $1}')"
[ "$actual_sha" = "02b0679f38d66ae72256d39fd44f15960cddc01552e13ad127806f7d3a6f1cf6" ] || {
  echo "readiness fixture digest changed unexpectedly: $actual_sha" >&2
  exit 1
}
description="$(file "$decoded")"
case "$description" in
  *'PNG image data, 640 x 160, 8-bit grayscale'*) ;;
  *) echo "readiness fixture has unexpected format: $description" >&2; exit 1 ;;
esac

grep -Fq '"words":\[[^]]' "$ROOT_DIR/terraform/readiness.tf"
grep -Fq '"text":"[^\"]+"' "$ROOT_DIR/terraform/readiness.tf"
# shellcheck disable=SC2016 # Match literal Terraform interpolation syntax.
grep -Fq '"$${OLLAMA_URL}/api/generate"' "$ROOT_DIR/terraform/readiness.tf"
grep -Fq '"stream":false' "$ROOT_DIR/terraform/readiness.tf"
grep -Fq '"done":true' "$ROOT_DIR/terraform/readiness.tf"
grep -Fq 'google_cloud_run_v2_service_iam_member" "ollama_readiness_invoker"' "$ROOT_DIR/terraform/ollama.tf"

echo "Kraken and Ollama readiness fixture contracts passed."
