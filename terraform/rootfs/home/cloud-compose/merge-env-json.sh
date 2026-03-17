#!/usr/bin/env bash

set -euo pipefail

target_env_file="${1:-.env}"
encoded_json="${2:-}"

if [ -z "$encoded_json" ]; then
  exit 0
fi

tmp_json="$(mktemp)"
tmp_env="$(mktemp)"
trap 'rm -f "$tmp_json" "$tmp_env"' EXIT

printf '%s' "$encoded_json" | base64 -d > "$tmp_json"

if [ ! -f "$target_env_file" ]; then
  touch "$target_env_file"
fi

cp "$target_env_file" "$tmp_env"

while IFS= read -r line; do
  key="${line%%=*}"
  grep -v "^${key}=" "$tmp_env" > "${tmp_env}.next" || true
  mv "${tmp_env}.next" "$tmp_env"
  printf '%s\n' "$line" >> "$tmp_env"
done < <(jq -r 'to_entries[] | "\(.key)=\(.value)"' "$tmp_json")

mv "$tmp_env" "$target_env_file"
