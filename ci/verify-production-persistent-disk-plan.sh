#!/bin/sh

set -eu

input_path="${1:--}"
if [ "$#" -gt 1 ]; then
  echo "usage: $0 [terraform-plan.json|-]" >&2
  exit 2
fi

if ! jq -e '
  def protected_persistent_disk:
    .address |
    test("^module\\.scribe\\.(.*\\.)?google_compute_disk\\.(data|docker-volumes)(\\[[^]]+\\])?$");
  def valid_actions:
    type == "array" and length > 0 and all(.[]; type == "string");
  def non_destructive_actions:
    . == ["no-op"] or . == ["read"] or . == ["create"] or . == ["update"];

  type == "object" and
  (.format_version | type == "string" and startswith("1.")) and
  ((.resource_changes // []) |
    type == "array" and all(.[];
      (.address | type == "string" and length > 0) and
      (.change | type == "object") and
      (.change.actions | valid_actions) and
      (if protected_persistent_disk then
        (.change.actions | non_destructive_actions)
      else
        true
      end)))
' "$input_path" >/dev/null 2>&1; then
  echo "Refusing production apply: the saved Terraform plan is invalid or deletes/replaces a Cloud Compose persistent data disk." >&2
  exit 1
fi
