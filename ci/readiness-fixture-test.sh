#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
decoded="$(mktemp)"
trap 'rm -f -- "$decoded"' EXIT

base64 --decode < "$ROOT_DIR/config/readiness-smoke.png.base64" > "$decoded"
actual_sha="$(sha256sum "$decoded" | awk '{print $1}')"
expected_sha="e3f3bb2b5ade3c15af262a76ad58b720e7eb3b3d079802df04f1dd50be917b2d"
[ "$actual_sha" = "$expected_sha" ] || {
  echo "readiness fixture digest changed unexpectedly: $actual_sha" >&2
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
bash "$ROOT_DIR/ci/ocr-readiness-script_test.sh"


echo "Readiness fixture and OCR probe behavior passed."
