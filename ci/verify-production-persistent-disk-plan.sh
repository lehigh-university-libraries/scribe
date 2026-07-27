#!/bin/sh

set -eu

target_generation="${1:-}"
input_path="${2:--}"
if [ "$#" -lt 1 ] || [ "$#" -gt 2 ]; then
  echo "usage: $0 <canonical-v1|canonical-v2> [terraform-plan.json|-]" >&2
  exit 2
fi
case "$target_generation" in
  canonical-v1|canonical-v2) ;;
  *)
    echo "target data generation is missing or not reviewed" >&2
    exit 2
    ;;
esac

if ! jq -e --arg target_generation "$target_generation" '
  def protected_persistent_disk:
    .address |
    test("^module\\.scribe\\.(.*\\.)?google_compute_disk\\.(data|docker-volumes)(\\[[^]]+\\])?$");
  def protected_prior_generation_queue:
    .address |
    test("^google_pubsub_(topic|subscription)(_iam_member)?\\.transcription_(jobs|jobs_dead_letter|workers|dead_letter_monitor|jobs_publisher|workers_subscriber|dead_letter_publisher|dead_letter_source_subscriber)$");
  def protected_prior_generation_alert:
    .address |
    test("^google_monitoring_alert_policy\\.(transcription_dead_letter_depth|transcription_queue_age)\\[0\\]$");
  def protected_active_forward_queue($generation):
    $generation == "canonical-v2" and
    (.address |
      test("^google_pubsub_(topic|subscription)(_iam_member)?\\.transcription_[a-z_]+_forward\\[\"canonical-v2\"\\]$"));
  def protected_active_forward_alert($generation):
    $generation == "canonical-v2" and
    (.address |
      test("^google_monitoring_alert_policy\\.(transcription_dead_letter_depth_forward|transcription_queue_age_forward)\\[\"canonical-v2\"\\]$"));
  def valid_actions:
    type == "array" and length > 0 and all(.[]; type == "string");
  def non_destructive_disk_actions:
    . == ["no-op"] or . == ["read"] or . == ["create"] or . == ["update"];
  def retained_queue_actions:
    . == ["no-op"] or . == ["read"] or . == ["create"];

  (.variables.data_generation.value // null) as $data_generation |
  type == "object" and
  (.format_version | type == "string" and startswith("1.")) and
  ($data_generation == $target_generation) and
  ((.resource_changes // []) |
    type == "array" and all(.[];
      (.address | type == "string" and length > 0) and
      (.change | type == "object") and
      (.change.actions | valid_actions) and
      (if (protected_persistent_disk or protected_prior_generation_alert or protected_active_forward_alert($data_generation)) then
        (.change.actions | non_destructive_disk_actions)
      elif (protected_prior_generation_queue or protected_active_forward_queue($data_generation)) then
        (.change.actions | retained_queue_actions)
      else
        true
      end)))
' "$input_path" >/dev/null 2>&1; then
  echo "Refusing production apply: the saved Terraform plan is invalid or deletes/replaces protected persistent data." >&2
  exit 1
fi
