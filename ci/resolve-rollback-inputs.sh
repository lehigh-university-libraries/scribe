#!/usr/bin/env bash

set -euo pipefail

input_path="${1:--}"

if [ "$#" -gt 1 ]; then
  echo "usage: $0 [deployment-inputs.json|-]" >&2
  exit 2
fi

# External OCR impersonators are created only by the Terraform dev workspace.
# Production rollback must replay an empty list so immutable historical inputs
# can never introduce human impersonation grants outside that dev boundary.
set +e
normalized="$(
  jq -ceS '
    def exact_keys($expected): (keys | sort) == ($expected | sort);
    def integer_at_least($minimum): type == "number" and . == floor and . >= $minimum;
    def duration: type == "string" and test("^[1-9][0-9]*(s|m|h)$");
    def cidr: type == "string" and test("^[0-9A-Fa-f:.]+/[0-9]{1,3}$");
    def ipv4_cidr: type == "string" and test("^[0-9.]+/[0-9]{1,2}$");
    def hosted_email: type == "string" and test("^[A-Za-z0-9.!#$%&\u0027*+/=?^_`{|}~-]+@lehigh\\.edu$");
    def service_account_email: type == "string" and test("^[a-z0-9-]+@[a-z0-9-]+\\.iam\\.gserviceaccount\\.com$");
    def digest: type == "string" and test("@sha256:[0-9a-f]{64}$") and (test("@sha256:0{64}$") | not);

    # Production state recorded before the dev-only external OCR IAM feature
    # has no impersonator key. Absence unambiguously means the old [] default;
    # present null, malformed, or non-empty values remain rejected below.
    if (.configuration | type) == "object" then
      if (.configuration | has("dev_external_ocr_impersonators")) then
        .
      else
        .configuration.dev_external_ocr_impersonators = []
      end
    else
      .
    end
    | if type == "object" and (has("browser_readiness_image") | not) then
        .browser_readiness_image = ""
      else
        .
      end
    | if (.configuration | type) == "object" and (.configuration | has("browser_readiness_subnet_cidr") | not) then
        .configuration.browser_readiness_subnet_cidr = "10.43.0.0/26"
      else
        .
      end
    | . as $deployment
    | .configuration as $configuration
    | ($configuration.project_id // "") as $project
    | if
        (($configuration | type) == "object") and
        ($configuration | has("dev_external_ocr_impersonators")) and
        (($configuration.dev_external_ocr_impersonators |
          type == "array" and length == 0) | not)
      then
        halt_error(20)
      elif
        (type == "object") and
        exact_keys([
          "api_image",
          "browser_readiness_image",
          "configuration",
          "data_generation",
          "docker_compose_sha",
          "frontend_gar_image",
          "ocr_service_images"
        ]) and
        ($configuration | type == "object" and exact_keys([
          "allowed_ips",
          "allowed_ssh_ipv4",
          "allowed_ssh_ipv6",
          "backup_restore_service_account_email",
          "browser_readiness_subnet_cidr",
          "compose_network_cidr",
          "dev_external_ocr_impersonators",
          "iiif_max_manifest_canvases",
          "iiif_max_manifest_import_bytes",
          "monitoring_notification_channels",
          "network_ip_cidr_range",
          "project_id",
          "region",
          "storage_max_bytes_per_workspace",
          "storage_max_bytes_total",
          "storage_max_images_per_workspace",
          "storage_max_images_total",
          "storage_max_items_per_workspace",
          "storage_max_items_total",
          "storage_normalization_cache_max_age",
          "storage_normalization_cache_max_bytes",
          "storage_reservation_ttl",
          "transcription_max_active_jobs_per_workspace",
          "vault_admin_emails",
          "vault_ci_service_account_emails",
          "zone"
        ])) and
        (.docker_compose_sha | type == "string" and test("^[0-9a-f]{40}$") and test("^0{40}$") == false) and
        (.data_generation | type == "string" and test("^canonical-v(1|2)$")) and
        (.api_image | digest and startswith("ghcr.io/lehigh-university-libraries/scribe@sha256:")) and
        (.browser_readiness_image == "") and
        ($project | type == "string" and test("^[a-z][a-z0-9-]{4,28}[a-z0-9]$")) and
        (.frontend_gar_image | digest and startswith("us-docker.pkg.dev/\($project)/internal/scribe-frontend@sha256:")) and
        (.ocr_service_images | type == "object" and length > 0 and all(to_entries[];
          (.key | type == "string" and test("^[a-z0-9][a-z0-9._/:-]*$")) and
          (.value | digest and startswith("us-docker.pkg.dev/\($project)/internal/"))
        )) and
        ($configuration.allowed_ips | type == "array" and length > 0 and all(.[]; cidr)) and
        ($configuration.allowed_ssh_ipv4 | type == "array" and all(.[]; ipv4_cidr)) and
        ($configuration.allowed_ssh_ipv6 | type == "array" and all(.[]; cidr and contains(":"))) and
        ($configuration.backup_restore_service_account_email | service_account_email) and
        ($configuration.browser_readiness_subnet_cidr |
          ipv4_cidr and test("/26$") and startswith("169.254.") == false) and
        ($configuration.dev_external_ocr_impersonators | type == "array" and length == 0) and
        ($configuration.network_ip_cidr_range | ipv4_cidr) and
        ($configuration.compose_network_cidr | ipv4_cidr) and
        ($configuration.region | type == "string" and test("^[a-z]+-[a-z]+[0-9]+$")) and
        ($configuration.zone | type == "string" and test("^\($configuration.region)-[a-z]$")) and
        ($configuration.monitoring_notification_channels | type == "array" and length > 0 and all(.[];
          type == "string" and test("^projects/\($project)/notificationChannels/[A-Za-z0-9._-]+$")
        )) and
        ($configuration.vault_admin_emails | type == "array" and length > 0 and all(.[]; hosted_email)) and
        ($configuration.vault_ci_service_account_emails | type == "array" and length > 0 and all(.[]; service_account_email)) and
        ($configuration.transcription_max_active_jobs_per_workspace | integer_at_least(1)) and
        ($configuration.storage_max_bytes_per_workspace | integer_at_least(1)) and
        ($configuration.storage_max_bytes_total | integer_at_least($configuration.storage_max_bytes_per_workspace)) and
        ($configuration.storage_max_items_per_workspace | integer_at_least(1)) and
        ($configuration.storage_max_items_total | integer_at_least($configuration.storage_max_items_per_workspace)) and
        ($configuration.storage_max_images_per_workspace | integer_at_least(1)) and
        ($configuration.storage_max_images_total | integer_at_least($configuration.storage_max_images_per_workspace)) and
        ($configuration.storage_reservation_ttl | duration) and
        ($configuration.storage_normalization_cache_max_bytes | integer_at_least(1)) and
        ($configuration.storage_normalization_cache_max_age | duration) and
        ($configuration.iiif_max_manifest_canvases | integer_at_least(1)) and
        ($configuration.iiif_max_manifest_import_bytes | integer_at_least(1024))
      then
        $deployment
      else
        error("invalid immutable deployment input schema")
      end
  ' "$input_path" 2>/dev/null
)"
resolve_status=$?
set -e

case "${resolve_status}" in
  0) ;;
  20)
    echo "Deployment inputs rejected: dev_external_ocr_impersonators must be empty because external OCR impersonation is restricted to the dev workspace." >&2
    exit 1
    ;;
  *)
    echo "Deployment inputs are missing, malformed, incomplete, or not immutable." >&2
    exit 1
    ;;
esac

printf '%s\n' "$normalized"
