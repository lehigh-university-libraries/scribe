#!/usr/bin/env bash

set -euo pipefail

usage() {
  echo "usage: update-env.sh PATH NAME --base64 VALUE" >&2
  exit 2
}

[ "$#" -eq 4 ] || usage
target_path="$1"
name="$2"
[ "$3" = "--base64" ] || usage
encoded_value="$4"

if [[ ! "$name" =~ ^[A-Z_][A-Z0-9_]*$ ]]; then
  echo "invalid environment variable name" >&2
  exit 2
fi

target_directory="$(dirname -- "$target_path")"
if [ ! -d "$target_directory" ]; then
  echo "environment file directory does not exist" >&2
  exit 1
fi

decoded_file="$(mktemp "$target_directory/.env-value.XXXXXX")"
output_file="$(mktemp "$target_directory/.env-next.XXXXXX")"
cleanup() {
  rm -f -- "$decoded_file" "$output_file"
}
trap cleanup EXIT

if ! printf '%s' "$encoded_value" | base64 --decode >"$decoded_file" 2>/dev/null; then
  echo "invalid base64 environment value" >&2
  exit 2
fi

decoded_size="$(wc -c <"$decoded_file")"
safe_size="$(LC_ALL=C tr -d '\000\r\n' <"$decoded_file" | wc -c)"
if [ "$decoded_size" -ne "$safe_size" ]; then
  echo "environment value contains a forbidden character" >&2
  exit 2
fi

value=""
IFS= read -r -d '' value <"$decoded_file" || true
replaced=false
if [ -f "$target_path" ]; then
  while IFS= read -r line || [ -n "$line" ]; do
    if [[ "$line" == "$name="* ]]; then
      if [ "$replaced" = "false" ]; then
        printf '%s=%s\n' "$name" "$value" >>"$output_file"
        replaced=true
      fi
      continue
    fi
    printf '%s\n' "$line" >>"$output_file"
  done <"$target_path"
fi
if [ "$replaced" = "false" ]; then
  printf '%s=%s\n' "$name" "$value" >>"$output_file"
fi

chmod 600 "$output_file"
mv -f -- "$output_file" "$target_path"
