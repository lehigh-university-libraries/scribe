#!/usr/bin/env sh

set -eu

ROOT_DIR="$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)"
FIXTURE_DIR="${ROOT_DIR}/internal/server/testdata/crosswalk"
SCHEMA_DIR="${FIXTURE_DIR}/schemas"

if ! command -v xmllint >/dev/null 2>&1; then
  echo "xmllint is required for OCR export schema validation." >&2
  echo "Install libxml2-utils (Linux) or libxml2 (Homebrew), or run make test-backend." >&2
  exit 127
fi

if command -v sha256sum >/dev/null 2>&1; then
  (
    cd "${SCHEMA_DIR}"
    sha256sum -c SHA256SUMS
  )
else
  echo "sha256sum is required for pinned OCR export schema validation." >&2
  echo "Install coreutils, or run make test-backend." >&2
  exit 127
fi

export XML_CATALOG_FILES="${SCHEMA_DIR}/catalog.xml"

xmllint --nonet --noout \
  --schema "${SCHEMA_DIR}/pagecontent-2019-07-15.xsd" \
  "${FIXTURE_DIR}/expected_page.xml"
xmllint --nonet --noout \
  --schema "${SCHEMA_DIR}/alto-4-4.xsd" \
  "${FIXTURE_DIR}/expected_alto.xml"
