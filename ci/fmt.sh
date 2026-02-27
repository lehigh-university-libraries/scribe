#!/usr/bin/env bash

set -euo pipefail

echo "Formatting Go code..."

files="$(git diff --name-only | grep '\.go$' || true)"

if [ -z "$files" ]; then
  echo "No changed Go files to format"
  exit 0
fi

echo "$files" | xargs gofmt -w
