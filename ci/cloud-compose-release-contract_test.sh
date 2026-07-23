#!/bin/sh

set -eu

ROOT_DIR="$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)"
cd "$ROOT_DIR"

fail() {
  echo "cloud-compose release contract failed: $*" >&2
  exit 1
}

module_sha="$(
  sed -n 's#.*github.com/libops/cloud-compose/archive/\([0-9a-f][0-9a-f]*\)\.tar\.gz.*#\1#p' \
    terraform/main.tf
)"
foundation_sha="$(
  sed -n 's#.*github.com/libops/cloud-compose/archive/\([0-9a-f][0-9a-f]*\)\.tar\.gz.*#\1#p' \
    terraform/foundation/main.tf
)"
printf '%s\n' "$module_sha" | grep -Eq '^[0-9a-f]{40}$' ||
  fail "terraform/main.tf must pin one exact cloud-compose commit"
[ "$foundation_sha" = "$module_sha" ] ||
  fail "the application and foundation must pin the same cloud-compose commit"

module_root="terraform/.terraform/modules/scribe/cloud-compose-${module_sha}"
[ -d "$module_root" ] || fail "the pinned scribe module is not initialized; run terraform init first"

module_main="${module_root}/modules/gcp/main.tf"
upstream_run="${module_root}/rootfs/home/cloud-compose/run.sh"
upstream_app_init="${module_root}/rootfs/home/cloud-compose/app-init.sh"
upstream_compose_apps="${module_root}/rootfs/home/cloud-compose/compose-apps.sh"
upstream_filesystem_convergence="${module_root}/rootfs/home/cloud-compose/converge-app-filesystems.sh"
upstream_cloud_init="${module_root}/templates/cloud-init.yml"
upstream_bootstrap_unit="${module_root}/rootfs/etc/systemd/system/cloud-compose-bootstrap.service"
upstream_app_unit="${module_root}/rootfs/etc/systemd/system/cloud-compose.service"
upstream_bootstrap_helpers="${module_root}/rootfs/home/cloud-compose/bootstrap-helpers.sh"
upstream_bootstrap_starter="${module_root}/rootfs/home/cloud-compose/start-cloud-compose-bootstrap.sh"
upstream_run_bootstrap="${module_root}/rootfs/home/cloud-compose/run-bootstrap.sh"
upstream_assert_initialized="${module_root}/rootfs/home/cloud-compose/assert-app-initialized.sh"
upstream_cos_portability="${module_root}/ci/cos-jq-portability-contract.sh"
for required_file in \
  "$module_main" \
  "$upstream_run" \
  "$upstream_app_init" \
  "$upstream_compose_apps" \
  "$upstream_filesystem_convergence" \
  "$upstream_cloud_init" \
  "$upstream_bootstrap_unit" \
  "$upstream_app_unit" \
  "$upstream_bootstrap_helpers" \
  "$upstream_bootstrap_starter" \
  "$upstream_run_bootstrap" \
  "$upstream_assert_initialized" \
  "$upstream_cos_portability"; do
  [ -r "$required_file" ] || fail "initialized cloud-compose release file is missing: ${required_file}"
done

line_of() {
  pattern="$1"
  file="$2"
  label="$3"
  matches="$(grep -nF "$pattern" "$file" || true)"
  [ "$(printf '%s\n' "$matches" | sed '/^$/d' | wc -l)" -eq 1 ] ||
    fail "the initialized cloud-compose release must contain one ${label}"
  printf '%s\n' "$matches" | cut -d: -f1
}

# Cloud Compose owns root-only convergence of the exact manifest checkout and
# its existing .env before any unprivileged source or application lifecycle.
# Assert that behavior semantically so ordinary upstream logging changes do not
# require Scribe to maintain brittle whole-file hashes.
converge_line="$(line_of \
  'bash /home/cloud-compose/converge-app-filesystems.sh' \
  "$upstream_run" \
  "filesystem convergence call")"
prepare_line="$(line_of \
  'run_as_cloud_compose bash /home/cloud-compose/prepare-app-sources.sh' \
  "$upstream_run" \
  "unprivileged source preparation call")"
app_init_line="$(line_of \
  'run_as_cloud_compose bash /home/cloud-compose/app-init.sh' \
  "$upstream_run" \
  "unprivileged app-init call")"
[ "$converge_line" -lt "$prepare_line" ] && [ "$prepare_line" -lt "$app_init_line" ] ||
  fail "filesystem convergence must precede source preparation and app-init"

grep -Fq 'if ((EUID != 0)); then' "$upstream_filesystem_convergence" ||
  fail "filesystem convergence is not restricted to root"
grep -Fq 'compose_app_names_array apps' "$upstream_filesystem_convergence" ||
  fail "filesystem convergence does not enumerate the validated app manifest"
# shellcheck disable=SC2016 # Match the pinned upstream's literal shell variable.
grep -Fq 'converge_compose_app_filesystem "$app"' "$upstream_filesystem_convergence" ||
  fail "filesystem convergence does not process each validated app"

# shellcheck disable=SC2016 # Match literal upstream Bash expressions.
for convergence_contract in \
  '_converge_compose_app_filesystem_for_ids() (' \
  'validate_compose_project_dir "$project_dir"' \
  'exec {project_fd}<"$project_dir"' \
  'env_file="${project_fd_path}/.env"' \
  'regular file:1' \
  'chmod 0640 "$env_fd_path"' \
  'chmod 0775 "$project_fd_path"'; do
  grep -Fq "$convergence_contract" "$upstream_compose_apps" ||
    fail "filesystem convergence is missing its reviewed safety contract: ${convergence_contract}"
done

project_dir_block="$(
  sed -n '/^[[:space:]]*project_dir = ($/,/^[[:space:]]*compose_project_name = ($/p' "$module_main"
)"
printf '%s\n' "$project_dir_block" | grep -Fq '"/mnt/disks/data/' ||
  fail "the omitted project_dir default is not rooted on the persistent data disk"
# shellcheck disable=SC2016 # Match literal upstream Terraform interpolation.
printf '%s\n' "$project_dir_block" | grep -Fq '${app_name}' ||
  fail "the omitted project_dir default is not stable per application"
if printf '%s\n' "$project_dir_block" | grep -Fq 'docker_compose_branch'; then
  fail "the omitted project_dir default still changes with the deployed Git ref"
fi

# Managed checkouts use exact immutable commits rather than a mutable git pull.
# shellcheck disable=SC2016 # Match literal upstream Bash expressions.
for source_contract in \
  'retry_until_success git fetch --force --no-tags -- origin "$requested_commit"' \
  'git checkout --detach "$requested_commit"' \
  'deployed_head="$(git rev-parse --verify HEAD)"' \
  'if [[ "${deployed_head,,}" != "${requested_commit,,}" ]]; then' \
  'verify_clean_compose_checkout'; do
  grep -Fq "$source_contract" "$upstream_compose_apps" ||
    fail "immutable Compose source convergence is missing: ${source_contract}"
done

# The reviewed upstream release owns retryable root convergence. A completed
# oneshot remains active, while a failed run enters a persistent systemd retry
# loop. Forced convergence synchronously stops the stale active instance before
# the shared helper starts and awaits a fresh run.
for unit_contract in \
  'Type=oneshot' \
  'RemainAfterExit=yes' \
  'Restart=on-failure' \
  'RestartSec=30s' \
  'StartLimitIntervalSec=0' \
  'TimeoutStartSec=2h' \
  'UMask=0022' \
  'StandardOutput=journal' \
  'StandardError=journal' \
  'LogRateLimitIntervalSec=30s' \
  'LogRateLimitBurst=1000' \
  'ExecStart=/bin/bash /home/cloud-compose/run-bootstrap.sh'; do
  grep -Fqx "$unit_contract" "$upstream_bootstrap_unit" ||
    fail "retryable bootstrap unit is missing: ${unit_contract}"
done
for app_unit_contract in \
  'StartLimitIntervalSec=0' \
  'ExecStartPre=/bin/bash /home/cloud-compose/assert-app-initialized.sh' \
  'Restart=on-failure' \
  'RestartSec=30s' \
  'TimeoutStartSec=1h'; do
  grep -Fqx "$app_unit_contract" "$upstream_app_unit" ||
    fail "retryable application unit is missing: ${app_unit_contract}"
done
if grep -Fq 'cloud-compose.service' "$upstream_bootstrap_unit" ||
  grep -Fq 'cloud-compose-bootstrap.service' "$upstream_app_unit"; then
  fail "bootstrap and application units must not introduce a systemd dependency cycle"
fi
# shellcheck disable=SC2016 # Match literal upstream Bash expressions.
for starter_contract in \
  'if cloud_compose_marker_exists "$durable_marker"; then' \
  'systemctl stop -- "$bootstrap_unit"' \
  'cloud_compose_start_and_wait_for_oneshot "$bootstrap_unit" "$wait_seconds"'; do
  grep -Fq "$starter_contract" "$upstream_bootstrap_starter" ||
    fail "retryable bootstrap starter is missing: ${starter_contract}"
done
# shellcheck disable=SC2016 # Match literal upstream Bash expressions.
for marker_contract in \
  'if cloud_compose_should_run_app_init' \
  'cloud_compose_publish_marker "$current_boot_app_init_marker"' \
  'cloud_compose_start_and_wait_for_oneshot cloud-compose.service "$app_wait_seconds"' \
  'cloud_compose_publish_marker "$durable_bootstrap_marker"'; do
  grep -Fq "$marker_contract" "$upstream_run" ||
    fail "retryable bootstrap marker ordering is missing: ${marker_contract}"
done
grep -Fq 'cloud_compose_start_and_wait_for_oneshot()' "$upstream_bootstrap_helpers" ||
  fail "retryable bootstrap release lacks the shared unit wait helper"
grep -Fq 'exec bash /home/cloud-compose/run.sh' "$upstream_run_bootstrap" ||
  fail "the root bootstrap wrapper does not execute the reviewed convergence runner"
grep -Fq 'cloud_compose_marker_exists "$durable_marker"' "$upstream_assert_initialized" ||
  fail "application preflight does not accept durable bootstrap readiness"
grep -Fq 'cloud_compose_marker_exists "$boot_marker"' "$upstream_assert_initialized" ||
  fail "application preflight does not accept current-boot initialization readiness"
grep -Fq 'acquire_cloud_compose_lifecycle_lock "init"' "$upstream_app_init" ||
  fail "application initialization does not acquire the shared lifecycle lock"
grep -Fq 'trap release_cloud_compose_lifecycle_lock EXIT' "$upstream_app_init" ||
  fail "application initialization does not release the lifecycle lock"

cloud_init_remove_line="$(line_of \
  'rm -f /home/cloud-compose/.cloud-compose-bootstrap-complete' \
  "$upstream_cloud_init" \
  "cloud-init durable-marker removal")"
cloud_init_start_line="$(line_of \
  'bash /home/cloud-compose/start-cloud-compose-bootstrap.sh' \
  "$upstream_cloud_init" \
  "cloud-init bootstrap starter")"
cloud_init_check_line="$(line_of \
  'test -f /home/cloud-compose/.cloud-compose-bootstrap-complete || {' \
  "$upstream_cloud_init" \
  "cloud-init post-bootstrap readiness check")"
[ "$cloud_init_remove_line" -lt "$cloud_init_start_line" ] &&
  [ "$cloud_init_start_line" -lt "$cloud_init_check_line" ] ||
  fail "cloud-init must clear readiness, await bootstrap, then gate post-init commands"
if grep -Fq 'bash /home/cloud-compose/run.sh > /home/cloud-compose/run.log' \
  "$upstream_cloud_init"; then
  fail "cloud-init bypasses the retryable bootstrap service"
fi

for shadowed_path in \
  terraform/rootfs/etc/systemd/system/cloud-compose-bootstrap.service \
  terraform/rootfs/etc/systemd/system/cloud-compose.service \
  terraform/rootfs/home/cloud-compose/assert-app-initialized.sh \
  terraform/rootfs/home/cloud-compose/bootstrap-helpers.sh \
  terraform/rootfs/home/cloud-compose/run-bootstrap.sh \
  terraform/rootfs/home/cloud-compose/run.sh \
  terraform/rootfs/home/cloud-compose/start-cloud-compose-bootstrap.sh; do
  [ ! -e "$shadowed_path" ] ||
    fail "Scribe must not shadow the pinned upstream lifecycle file: ${shadowed_path}"
done

# Scribe injects both immutable release inputs into the Compose manifest that
# cloud-compose includes in the rendered cloud-init document.
grep -Eq 'name[[:space:]]*=[[:space:]]*"SCRIBE_API_IMAGE"' terraform/main.tf ||
  fail "Scribe does not inject the reviewed API image"
grep -Eq 'value[[:space:]]*=[[:space:]]*var\.api_image' terraform/main.tf ||
  fail "Scribe API image injection is not tied to var.api_image"
grep -Eq 'docker_compose_branch[[:space:]]*=[[:space:]]*var\.docker_compose_branch' terraform/main.tf ||
  fail "Scribe does not inject the reviewed source SHA"

# Follow the initialized upstream implementation rather than assuming a
# metadata update reruns cloud-init: the complete manifest enters cloud-init,
# its rendering names a unique boot disk, and the instance consumes that disk.
grep -Eq 'jsonencode\(local\.validated_compose_projects\)' "$module_main" ||
  fail "cloud-compose no longer embeds the validated Compose manifest"
grep -Eq 'COMPOSE_PROJECTS_FILE[[:space:]]*=[[:space:]]*local\.compose_projects_file' "$module_main" ||
  fail "cloud-compose no longer includes the Compose manifest in cloud-init"
grep -Eq 'content[[:space:]]*=[[:space:]]*local\.cloud_init_yaml' "$module_main" ||
  fail "cloud-compose no longer renders the cloud-init document"
grep -Eq 'name[[:space:]]*=[[:space:]]*format\("%s-boot-%s",[[:space:]]*var\.name,[[:space:]]*md5\(data\.cloudinit_config\.ci\.rendered\)\)' "$module_main" ||
  fail "cloud-compose no longer rotates the boot disk when cloud-init changes"
grep -Eq 'source[[:space:]]*=[[:space:]]*google_compute_disk\.boot\.self_link' "$module_main" ||
  fail "the VM no longer consumes the release-specific boot disk"
grep -Eq 'image[[:space:]]*=[[:space:]]*"projects/cos-cloud/global/images/\$\{var\.os\}"' "$module_main" ||
  fail "the GCP host runtime must remain Container-Optimized OS"
# The Terraform validation image intentionally contains only POSIX sh. Keep
# this contract executable there while proving the exact upstream release
# retains its own Bash portability gate; that gate already passed before the
# pinned Cloud Compose release was published.
grep -Fq 'regex-dependent jq-style call' "$upstream_cos_portability" ||
  fail "cloud-compose no longer gates jq regex functions unavailable on COS"
grep -Fq 'jq 1.6-incompatible NUL validation' "$upstream_cos_portability" ||
  fail "cloud-compose no longer gates jq 1.6-incompatible NUL validation"

echo "cloud-compose immutable release replacement contracts passed."
