#!/bin/sh

set -eu

input_path="${1:--}"
if [ "$#" -gt 1 ]; then
  echo "usage: $0 [terraform-plan.json|-]" >&2
  exit 2
fi

if ! jq -e '
  def no_op_actions: type == "array" and . == ["no-op"];

  type == "object" and
  (.format_version | type == "string" and length > 0) and
  ((.resource_drift // []) |
    type == "array" and all(.[];
      (.address | type == "string" and length > 0) and
      (.previous_address | type == "string" and length > 0) and
      (.address != .previous_address) and
      (.change.actions | no_op_actions))) and
  ((.resource_changes // []) |
    type == "array" and all(.[]; .change.actions | no_op_actions)) and
  ((.output_changes // {}) |
    type == "object" and all(.[]; .actions | no_op_actions))
' "$input_path" >/dev/null 2>&1; then
  echo "Refresh-only plan contains non-move drift or a non-no-op resource/output action; refusing to update Terraform state." >&2
  exit 1
fi
