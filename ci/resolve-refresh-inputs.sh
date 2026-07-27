#!/usr/bin/env bash

set -euo pipefail

: "${GCLOUD_PROJECT:?GCLOUD_PROJECT is required}"

if ! normalized="$({
  "$({ cd "$(dirname "${BASH_SOURCE[0]}")" && pwd; })/resolve-destroy-inputs.sh" |
    jq -ceS --arg project "$GCLOUD_PROJECT" '
      def exact_keys($expected): (keys | sort) == ($expected | sort);
      def string_array: type == "array" and all(.[]; type == "string");
      def positive_integer: type == "number" and . == floor and . >= 1;

      select(
        exact_keys([
          "api_image",
          "configuration",
          "data_generation",
          "docker_compose_sha",
          "frontend_gar_image",
          "ocr_service_images"
        ]) and
        (.configuration | type == "object" and exact_keys([
          "allowed_ips",
          "allowed_ssh_ipv4",
          "allowed_ssh_ipv6",
          "backup_restore_service_account_email",
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
        (.configuration.project_id == $project) and
        (.configuration.allowed_ips | string_array) and
        (.configuration.allowed_ssh_ipv4 | string_array) and
        (.configuration.allowed_ssh_ipv6 | string_array) and
        (.configuration.dev_external_ocr_impersonators |
          string_array and
          length == (unique | length) and
          all(.[]; test("^(user|group):[^@[:space:]]+@[^@[:space:]]+$"))) and
        (.configuration.monitoring_notification_channels | string_array) and
        (.configuration.vault_admin_emails | string_array) and
        (.configuration.vault_ci_service_account_emails | string_array) and
        (.configuration.backup_restore_service_account_email | type == "string") and
        (.configuration.compose_network_cidr | type == "string" and length > 0) and
        (.configuration.network_ip_cidr_range | type == "string" and length > 0) and
        (.configuration.region | type == "string" and length > 0) and
        (.configuration.zone | type == "string" and length > 0) and
        (.configuration.storage_normalization_cache_max_age | type == "string" and length > 0) and
        (.configuration.storage_reservation_ttl | type == "string" and length > 0) and
        (.configuration.iiif_max_manifest_canvases | positive_integer) and
        (.configuration.iiif_max_manifest_import_bytes | positive_integer) and
        (.configuration.storage_max_bytes_per_workspace | positive_integer) and
        (.configuration.storage_max_bytes_total | positive_integer) and
        (.configuration.storage_max_images_per_workspace | positive_integer) and
        (.configuration.storage_max_images_total | positive_integer) and
        (.configuration.storage_max_items_per_workspace | positive_integer) and
        (.configuration.storage_max_items_total | positive_integer) and
        (.configuration.storage_normalization_cache_max_bytes | positive_integer) and
        (.configuration.transcription_max_active_jobs_per_workspace | positive_integer)
      )
    ' 2>/dev/null
})"; then
  echo "Stored deployment_inputs are missing or invalid; refusing to refresh with guessed or mutable release inputs." >&2
  exit 1
fi

printf '%s\n' "$normalized"
