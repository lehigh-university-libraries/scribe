#!/bin/sh

set -eu

ROOT_DIR="$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)"
verifier="$ROOT_DIR/ci/verify-production-persistent-disk-plan.sh"

expect_rejected() {
  fixture="$1"
  label="$2"
  target_generation="${3:-canonical-v2}"
  stderr_file="$(mktemp)"
  if printf '%s\n' "$fixture" | "$verifier" "$target_generation" 2>"$stderr_file"; then
    rm -f "$stderr_file"
    echo "Persistent-disk plan verifier accepted ${label}." >&2
    exit 1
  fi
  grep -F 'Refusing production apply' "$stderr_file" >/dev/null
  rm -f "$stderr_file"
}

# In-place data-disk growth, retained-v1 queue reads, and unrelated replacement
# are safe for this narrowly scoped guard. Terraform must still apply every
# production change from the same saved plan that the guard inspected.
printf '%s\n' '{
  "format_version":"1.2",
  "variables":{"data_generation":{"value":"canonical-v2"}},
  "resource_changes":[
    {
      "address":"module.scribe.module.gcp[0].google_compute_disk.data",
      "change":{"actions":["update"],"before":{"size":20},"after":{"size":170}}
    },
    {
      "address":"module.scribe.module.gcp[0].google_compute_disk.boot",
      "change":{"actions":["delete","create"]}
    },
    {
      "address":"google_pubsub_topic.transcription_jobs",
      "change":{"actions":["no-op"]}
    },
    {
      "address":"google_pubsub_subscription.transcription_workers_forward[\"canonical-v2\"]",
      "change":{"actions":["create"]}
    },
    {
      "address":"google_monitoring_alert_policy.transcription_queue_age[0]",
      "change":{"actions":["update"]}
    }
  ]
}' | "$verifier" canonical-v2

# Rollback explicitly targets v1, so removing the failed v2 graph is safe.
printf '%s\n' '{
  "format_version":"1.2",
  "variables":{"data_generation":{"value":"canonical-v1"}},
  "resource_changes":[{
    "address":"google_pubsub_subscription.transcription_workers_forward[\"canonical-v2\"]",
    "change":{"actions":["delete"]}
  }]
}' | "$verifier" canonical-v1

printf '%s\n' '{"format_version":"1.2","variables":{"data_generation":{"value":"canonical-v2"}}}' |
  "$verifier" canonical-v2

expect_rejected '{
  "format_version":"1.2",
  "variables":{"data_generation":{"value":"canonical-v2"}},
  "resource_changes":[{
    "address":"module.scribe.module.gcp[0].google_compute_disk.data",
    "change":{"actions":["delete"]}
  }]
}' 'a data-disk deletion'

expect_rejected '{
  "format_version":"1.2",
  "variables":{"data_generation":{"value":"canonical-v2"}},
  "resource_changes":[{
    "address":"module.scribe.module.gcp[0].google_compute_disk.docker-volumes",
    "change":{"actions":["delete","create"]}
  }]
}' 'a Docker-volumes disk replacement'

expect_rejected '{
  "format_version":"1.2",
  "variables":{"data_generation":{"value":"canonical-v2"}},
  "resource_changes":[{
    "address":"module.scribe.google_compute_disk.data",
    "change":{"actions":["create","delete"]}
  }]
}' 'a legacy-address data-disk replacement'

expect_rejected '{
  "format_version":"1.2",
  "variables":{"data_generation":{"value":"canonical-v2"}},
  "resource_changes":[{
    "address":"module.scribe.module.gcp[0].google_compute_disk.data",
    "change":{"actions":["future-destructive-action"]}
  }]
}' 'an unknown protected-disk action'

while IFS= read -r protected_address; do
  fixture="$(jq -cn --arg address "$protected_address" '{
    format_version: "1.2",
    variables: {data_generation: {value: "canonical-v2"}},
    resource_changes: [{address: $address, change: {actions: ["delete"]}}]
  }')"
  expect_rejected "$fixture" "a retained canonical-v1 resource deletion at ${protected_address}"
done <<'EOF'
google_pubsub_topic.transcription_jobs
google_pubsub_topic.transcription_jobs_dead_letter
google_pubsub_subscription.transcription_workers
google_pubsub_subscription.transcription_dead_letter_monitor
google_pubsub_topic_iam_member.transcription_jobs_publisher
google_pubsub_subscription_iam_member.transcription_workers_subscriber
google_pubsub_topic_iam_member.transcription_dead_letter_publisher
google_pubsub_subscription_iam_member.transcription_dead_letter_source_subscriber
google_monitoring_alert_policy.transcription_dead_letter_depth[0]
google_monitoring_alert_policy.transcription_queue_age[0]
EOF

expect_rejected '{
  "format_version":"1.2",
  "variables":{"data_generation":{"value":"canonical-v2"}},
  "resource_changes":[{
    "address":"google_pubsub_subscription.transcription_workers",
    "change":{"actions":["delete","create"]}
  }]
}' 'a prior-generation transcription subscription replacement'

expect_rejected '{
  "format_version":"1.2",
  "variables":{"data_generation":{"value":"canonical-v2"}},
  "resource_changes":[{
    "address":"google_pubsub_subscription.transcription_workers",
    "change":{"actions":["update"]}
  }]
}' 'an arbitrary retained-queue update'

while IFS= read -r protected_address; do
  fixture="$(jq -cn --arg address "$protected_address" '{
    format_version: "1.2",
    variables: {data_generation: {value: "canonical-v2"}},
    resource_changes: [{address: $address, change: {actions: ["delete"]}}]
  }')"
  expect_rejected "$fixture" "an active canonical-v2 resource deletion at ${protected_address}"
done <<'EOF'
google_pubsub_topic.transcription_jobs_forward["canonical-v2"]
google_pubsub_topic.transcription_jobs_dead_letter_forward["canonical-v2"]
google_pubsub_subscription.transcription_workers_forward["canonical-v2"]
google_pubsub_subscription.transcription_dead_letter_monitor_forward["canonical-v2"]
google_pubsub_topic_iam_member.transcription_jobs_publisher_forward["canonical-v2"]
google_pubsub_subscription_iam_member.transcription_workers_subscriber_forward["canonical-v2"]
google_pubsub_topic_iam_member.transcription_dead_letter_publisher_forward["canonical-v2"]
google_pubsub_subscription_iam_member.transcription_dead_letter_source_subscriber_forward["canonical-v2"]
google_monitoring_alert_policy.transcription_dead_letter_depth_forward["canonical-v2"]
google_monitoring_alert_policy.transcription_queue_age_forward["canonical-v2"]
EOF

expect_rejected '{"format_version":"1.2","variables":{"data_generation":{"value":"canonical-v2"}},"resource_changes":"not-an-array"}' 'an invalid plan schema'
expect_rejected '{"format_version":"1.2"}' 'a missing plan generation'
expect_rejected '{"format_version":"1.2","variables":{"data_generation":{"value":"canonical-v3"}}}' 'an unreviewed plan generation'
expect_rejected '{"format_version":"1.2","variables":{"data_generation":{"value":"canonical-v2"}}}' 'a target/plan generation mismatch' canonical-v1
expect_rejected 'not-json' 'invalid JSON'

for invalid_target in '' canonical-v3; do
  stderr_file="$(mktemp)"
  if printf '%s\n' '{"format_version":"1.2","variables":{"data_generation":{"value":"canonical-v2"}}}' |
    "$verifier" "$invalid_target" 2>"$stderr_file"; then
    rm -f "$stderr_file"
    echo "Persistent-disk plan verifier accepted invalid target generation: ${invalid_target:-missing}." >&2
    exit 1
  fi
  grep -F 'target data generation is missing or not reviewed' "$stderr_file" >/dev/null
  rm -f "$stderr_file"
done

echo "Production persistent-disk saved-plan contracts passed."
