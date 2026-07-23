#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TEMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/scribe-vm-bootstrap-diagnostics-test.XXXXXX")"
trap 'rm -rf -- "$TEMP_DIR"' EXIT
mkdir -p "$TEMP_DIR/bin"

cat >"$TEMP_DIR/bin/gcloud" <<'FAKE_GCLOUD'
#!/usr/bin/env bash
set -euo pipefail

printf '%q ' "$@" >>"$MOCK_GCLOUD_LOG"
printf '\n' >>"$MOCK_GCLOUD_LOG"

if [[ "$1 $2 $3" == "compute instances describe" ]]; then
  if [[ "${MOCK_ALL_SOURCES_FAIL:-false}" == true ]]; then
    printf '%s%s\n' 'hvs.' 'DESCRIBE_ERROR_TOKEN' >&2
    exit 6
  fi
  cat <<'JSON'
{
  "id": "1234567890123456789",
  "name": "scribe",
  "status": "RUNNING",
  "creationTimestamp": "2026-07-23T00:00:00.000-00:00",
  "lastStartTimestamp": "2026-07-23T00:00:05.000-00:00",
  "zone": "https://www.googleapis.com/compute/v1/projects/scribe-test/zones/us-east5-b",
  "disks": [
    {"deviceName":"scribe-boot","boot":true},
    {"deviceName":"scribe-data","boot":false}
  ],
  "networkInterfaces": [
    {
      "name": "nic0",
      "network": "https://www.googleapis.com/compute/v1/projects/scribe-test/global/networks/scribe",
      "subnetwork": "https://www.googleapis.com/compute/v1/projects/scribe-test/regions/us-east5/subnetworks/scribe"
    }
  ],
  "tags": {
    "items": ["cloud-compose", "scribe"]
  },
  "metadata": {"items":[{"key":"user-data","value":"DESCRIBE_METADATA_SECRET_SENTINEL"}]}
}
JSON
  exit 0
fi

if [[ "$1 $2 $3 $4" == "run jobs describe scribe-prod-backend-readiness" ]]; then
  if [[ "${MOCK_NETWORK_QUERIES_FAIL:-false}" == true || "${MOCK_ALL_SOURCES_FAIL:-false}" == true ]]; then
    printf '%s%s\n' 'hvs.' 'BACKEND_JOB_ERROR_TOKEN' >&2
    exit 8
  fi
  if [[ "${MOCK_NETWORK_DRIFT:-false}" == true ]]; then
    cat <<'JSON'
{
  "metadata": {
    "name": "scribe-prod-backend-readiness",
    "labels": {"cloud.googleapis.com/location": "us-east5"},
    "annotations": {
      "credential": "BACKEND_JOB_ANNOTATION_SECRET_SENTINEL"
    }
  },
  "spec": {
    "template": {
      "metadata": {
        "annotations": {
          "run.googleapis.com/vpc-access-egress": "all-traffic",
          "run.googleapis.com/network-interfaces": "[{\"network\":\"projects/scribe-test/global/networks/wrong-network\",\"subnetwork\":\"projects/scribe-test/regions/us-east5/subnetworks/wrong-subnetwork\"}]",
          "run.googleapis.com/secret": "BACKEND_JOB_ENV_SECRET_SENTINEL"
        }
      }
    }
  }
}
JSON
    exit 0
  fi
  cat <<'JSON'
{
  "metadata": {
    "name": "scribe-prod-backend-readiness",
    "labels": {"cloud.googleapis.com/location": "us-east5"},
    "annotations": {
      "credential": "BACKEND_JOB_ANNOTATION_SECRET_SENTINEL"
    }
  },
  "spec": {
    "template": {
      "metadata": {
        "annotations": {
          "run.googleapis.com/vpc-access-egress": "private-ranges-only",
          "run.googleapis.com/network-interfaces": "[{\"network\":\"projects/scribe-test/global/networks/scribe\",\"subnetwork\":\"projects/scribe-test/regions/us-east5/subnetworks/scribe\"}]",
          "run.googleapis.com/secret": "BACKEND_JOB_ENV_SECRET_SENTINEL"
        }
      }
    }
  }
}
JSON
  exit 0
fi

if [[ "$1 $2 $3 $4" == "compute instances network-interfaces get-effective-firewalls" ]]; then
  if [[ "${MOCK_NETWORK_QUERIES_FAIL:-false}" == true || "${MOCK_ALL_SOURCES_FAIL:-false}" == true ]]; then
    printf '%s%s\n' 'hvs.' 'EFFECTIVE_FIREWALL_ERROR_TOKEN' >&2
    exit 9
  fi
  if [[ "${MOCK_NETWORK_DRIFT:-false}" == true ]]; then
    cat <<'JSON'
{
  "firewalls": [
    {
      "name": "allow-cloud-run-scribe",
      "network": "https://www.googleapis.com/compute/v1/projects/scribe-test/global/networks/scribe",
      "direction": "INGRESS",
      "priority": 20,
      "disabled": false,
      "sourceRanges": ["10.99.0.0/24"],
      "targetTags": ["wrong-target"],
      "allowed": [
        {
          "IPProtocol": "tcp",
          "ports": ["443"]
        }
      ],
      "description": "EFFECTIVE_FIREWALL_DESCRIPTION_SECRET_SENTINEL"
    }
  ],
  "firewallPolicys": [
    {
      "type": "HIERARCHY",
      "name": "POLICY_NAME_SECRET_SENTINEL",
      "rules": [
        {
          "direction": "INGRESS",
          "priority": 10,
          "disabled": false,
          "action": "deny",
          "match": {
            "srcIpRanges": ["10.0.0.0/8"],
            "layer4Configs": [
              {
                "ipProtocol": "tcp",
                "ports": ["80"]
              }
            ]
          },
          "description": "POLICY_DESCRIPTION_SECRET_SENTINEL"
        }
      ]
    }
  ]
}
JSON
    exit 0
  fi
  cat <<'JSON'
{
  "firewalls": [
    {
      "name": "allow-cloud-run-scribe",
      "network": "https://www.googleapis.com/compute/v1/projects/scribe-test/global/networks/scribe",
      "direction": "INGRESS",
      "priority": 20,
      "disabled": false,
      "sourceRanges": ["10.42.0.0/24"],
      "targetTags": ["scribe"],
      "allowed": [
        {
          "IPProtocol": "tcp",
          "ports": ["80"]
        }
      ],
      "description": "EFFECTIVE_FIREWALL_DESCRIPTION_SECRET_SENTINEL"
    },
    {
      "name": "unrelated-allow",
      "network": "https://www.googleapis.com/compute/v1/projects/scribe-test/global/networks/scribe",
      "direction": "INGRESS",
      "priority": 100,
      "disabled": false,
      "sourceRanges": ["192.0.2.0/24"],
      "targetTags": ["scribe"],
      "allowed": [
        {
          "IPProtocol": "tcp",
          "ports": ["443"]
        }
      ],
      "description": "IMPLIED_FIREWALL_DESCRIPTION_SECRET_SENTINEL"
    }
  ],
  "firewallPolicys": []
}
JSON
  exit 0
fi

echo "unexpected fake gcloud invocation: $*" >&2
exit 99
FAKE_GCLOUD
chmod +x "$TEMP_DIR/bin/gcloud"

export PATH="$TEMP_DIR/bin:$PATH"
export MOCK_GCLOUD_LOG="$TEMP_DIR/gcloud.log"
export GCLOUD_PROJECT=scribe-test
export SCRIBE_INSTANCE=scribe
export SCRIBE_REGION=us-east5
export SCRIBE_ZONE=us-east5-b
export TF_VAR_network_ip_cidr_range=10.42.0.0/24

output="$TEMP_DIR/diagnostics.log"
bash "$ROOT_DIR/ci/gcp-vm-bootstrap-diagnostics.sh" >"$output"

grep -Fq 'Scribe GCP VM diagnostics (typed control-plane fields only)' "$output"
grep -Fq '[instance] id=1234567890123456789' "$output"
grep -Fq '[instance] name=scribe' "$output"
grep -Fq '[instance] status=RUNNING' "$output"
grep -Fq '[instance] zone=us-east5-b' "$output"
grep -Fq '[instance] attached_disk_count=2' "$output"
grep -Fq '[instance] boot_disk=scribe-boot' "$output"
grep -Fq '[backend-network] query=ok identity=match egress=private-ranges-only interfaces=1 network=match subnetwork=match' "$output"
grep -Fq '[effective-firewall] query=ok expected_allow=exact matching_deny_candidate=absent rules=2' "$output"

if grep -Eq 'DESCRIBE_METADATA_SECRET_SENTINEL|BACKEND_JOB_(ANNOTATION|ENV)_SECRET_SENTINEL|EFFECTIVE_FIREWALL_DESCRIPTION_SECRET_SENTINEL|IMPLIED_FIREWALL_DESCRIPTION_SECRET_SENTINEL|POLICY_NAME_SECRET_SENTINEL' "$output"; then
  echo "VM diagnostics persisted a non-allowlisted API field" >&2
  exit 1
fi

[[ "$(grep -c '^compute instances describe ' "$MOCK_GCLOUD_LOG")" -eq 1 ]]
[[ "$(grep -c '^run jobs describe scribe-prod-backend-readiness ' "$MOCK_GCLOUD_LOG")" -eq 1 ]]
[[ "$(grep -c '^compute instances network-interfaces get-effective-firewalls scribe ' "$MOCK_GCLOUD_LOG")" -eq 1 ]]
[[ "$(wc -l <"$MOCK_GCLOUD_LOG")" -eq 3 ]]
[[ "$(grep -c '^logging read ' "$MOCK_GCLOUD_LOG" || true)" -eq 0 ]]
grep -Fq -- '--project scribe-test --zone us-east5-b --format=json' "$MOCK_GCLOUD_LOG"
grep -Fq -- '--project scribe-test --region us-east5 --format=json' "$MOCK_GCLOUD_LOG"
grep -Fq -- '--project scribe-test --zone us-east5-b --network-interface=nic0 --format=json' "$MOCK_GCLOUD_LOG"
if grep -Eq '(^| )(create|delete|reset|start|stop|update|add-metadata)( |$)' "$MOCK_GCLOUD_LOG"; then
  echo "VM diagnostics invoked a mutating gcloud operation" >&2
  exit 1
fi

all_fail_output="$TEMP_DIR/all-fail.log"
MOCK_ALL_SOURCES_FAIL=true MOCK_GCLOUD_LOG="$TEMP_DIR/all-fail-gcloud.log" \
  bash "$ROOT_DIR/ci/gcp-vm-bootstrap-diagnostics.sh" >"$all_fail_output" 2>&1
grep -Fq '[status] instance_query=unavailable' "$all_fail_output"
grep -Fq '[backend-network] query=unavailable identity=unknown egress=unknown interfaces=unknown network=unknown subnetwork=unknown' "$all_fail_output"
grep -Fq '[effective-firewall] query=unavailable expected_allow=unknown matching_deny_candidate=unknown rules=unknown' "$all_fail_output"
if grep -Eq 'DESCRIBE_ERROR_TOKEN|EFFECTIVE_FIREWALL_ERROR_TOKEN|hvs\.' "$all_fail_output"; then
  echo "VM diagnostics exposed failed-query stderr" >&2
  exit 1
fi

network_fail_output="$TEMP_DIR/network-fail.log"
network_fail_gcloud_log="$TEMP_DIR/network-fail-gcloud.log"
MOCK_NETWORK_QUERIES_FAIL=true MOCK_GCLOUD_LOG="$network_fail_gcloud_log" \
  bash "$ROOT_DIR/ci/gcp-vm-bootstrap-diagnostics.sh" >"$network_fail_output" 2>&1
grep -Fq '[backend-network] query=unavailable identity=unknown egress=unknown interfaces=unknown network=unknown subnetwork=unknown' "$network_fail_output"
grep -Fq '[effective-firewall] query=unavailable expected_allow=unknown matching_deny_candidate=unknown rules=unknown' "$network_fail_output"
if grep -Eq 'BACKEND_JOB_ERROR_TOKEN|EFFECTIVE_FIREWALL_ERROR_TOKEN|hvs\.' "$network_fail_output"; then
  echo "VM diagnostics exposed failed network-query stderr" >&2
  exit 1
fi

network_drift_output="$TEMP_DIR/network-drift.log"
network_drift_gcloud_log="$TEMP_DIR/network-drift-gcloud.log"
MOCK_NETWORK_DRIFT=true MOCK_GCLOUD_LOG="$network_drift_gcloud_log" \
  bash "$ROOT_DIR/ci/gcp-vm-bootstrap-diagnostics.sh" >"$network_drift_output" 2>&1
grep -Fq '[backend-network] query=ok identity=match egress=mismatch interfaces=1 network=mismatch subnetwork=mismatch' "$network_drift_output"
grep -Fq '[effective-firewall] query=ok expected_allow=drift matching_deny_candidate=present rules=2' "$network_drift_output"
if grep -Eq 'BACKEND_JOB_(ANNOTATION|ENV)_SECRET_SENTINEL|EFFECTIVE_FIREWALL_DESCRIPTION_SECRET_SENTINEL|POLICY_DESCRIPTION_SECRET_SENTINEL' "$network_drift_output"; then
  echo "VM diagnostics exposed raw drift-query content" >&2
  exit 1
fi

wrong_instance_output="$TEMP_DIR/wrong-instance.log"
wrong_instance_gcloud_log="$TEMP_DIR/wrong-instance-gcloud.log"
: >"$wrong_instance_gcloud_log"
if SCRIBE_INSTANCE=scribe-pr-75 MOCK_GCLOUD_LOG="$wrong_instance_gcloud_log" \
  bash "$ROOT_DIR/ci/gcp-vm-bootstrap-diagnostics.sh" >"$wrong_instance_output" 2>&1; then
  echo "VM diagnostics accepted a non-production instance" >&2
  exit 1
fi
grep -Fq 'SCRIBE_INSTANCE must identify the production scribe instance' "$wrong_instance_output"
[[ ! -s "$wrong_instance_gcloud_log" ]] || {
  echo "VM diagnostics queried GCP before validating the production instance" >&2
  exit 1
}

echo "GCP VM diagnostics persist only bounded typed control-plane fields."
