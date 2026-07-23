#!/bin/sh

set -eu

ROOT_DIR="$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)"
verifier="$ROOT_DIR/ci/verify-production-persistent-disk-plan.sh"
deploy_helper="$ROOT_DIR/terraform/deploy-local.sh"

expect_rejected() {
  fixture="$1"
  label="$2"
  stderr_file="$(mktemp)"
  if printf '%s\n' "$fixture" | "$verifier" 2>"$stderr_file"; then
    rm -f "$stderr_file"
    echo "Persistent-disk plan verifier accepted ${label}." >&2
    exit 1
  fi
  grep -F 'Refusing production apply' "$stderr_file" >/dev/null
  rm -f "$stderr_file"
}

# In-place data-disk growth and unrelated replacement are safe for this
# narrowly scoped guard. Terraform must still apply every production change
# from the same saved plan that the guard inspected.
printf '%s\n' '{
  "format_version":"1.2",
  "resource_changes":[
    {
      "address":"module.scribe.module.gcp[0].google_compute_disk.data",
      "change":{"actions":["update"],"before":{"size":20},"after":{"size":170}}
    },
    {
      "address":"module.scribe.module.gcp[0].google_compute_disk.boot",
      "change":{"actions":["delete","create"]}
    }
  ]
}' | "$verifier"

printf '%s\n' '{"format_version":"1.2"}' | "$verifier"

expect_rejected '{
  "format_version":"1.2",
  "resource_changes":[{
    "address":"module.scribe.module.gcp[0].google_compute_disk.data",
    "change":{"actions":["delete"]}
  }]
}' 'a data-disk deletion'

expect_rejected '{
  "format_version":"1.2",
  "resource_changes":[{
    "address":"module.scribe.module.gcp[0].google_compute_disk.docker-volumes",
    "change":{"actions":["delete","create"]}
  }]
}' 'a Docker-volumes disk replacement'

expect_rejected '{
  "format_version":"1.2",
  "resource_changes":[{
    "address":"module.scribe.google_compute_disk.data",
    "change":{"actions":["create","delete"]}
  }]
}' 'a legacy-address data-disk replacement'

expect_rejected '{
  "format_version":"1.2",
  "resource_changes":[{
    "address":"module.scribe.module.gcp[0].google_compute_disk.data",
    "change":{"actions":["future-destructive-action"]}
  }]
}' 'an unknown protected-disk action'

expect_rejected '{"format_version":"1.2","resource_changes":"not-an-array"}' 'an invalid plan schema'
expect_rejected 'not-json' 'invalid JSON'

# shellcheck disable=SC2016 # Match literal deploy-helper shell variables.
grep -F 'terraform plan -out="$apply_plan_path"' "$deploy_helper" >/dev/null
# shellcheck disable=SC2016 # Match literal deploy-helper shell variables.
grep -F '"$repo_root/ci/verify-production-persistent-disk-plan.sh" <"$apply_plan_json"' "$deploy_helper" >/dev/null
# shellcheck disable=SC2016 # Match literal deploy-helper shell variables.
grep -F 'terraform apply -auto-approve "$apply_plan_path"' "$deploy_helper" >/dev/null

echo "Production persistent-disk saved-plan contracts passed."
