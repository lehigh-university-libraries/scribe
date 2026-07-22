#!/bin/sh

set -eu

ROOT_DIR="$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)"
cd "$ROOT_DIR"

fail() {
  echo "cloud-compose release contract failed: $*" >&2
  exit 1
}

module_root=""
for candidate in terraform/.terraform/modules/scribe/cloud-compose-*; do
  if [ -d "$candidate" ]; then
    module_root="$candidate"
    break
  fi
done
[ -n "$module_root" ] || fail "the pinned scribe module is not initialized; run terraform init first"

module_main="${module_root}/modules/gcp/main.tf"
[ -r "$module_main" ] || fail "initialized cloud-compose GCP module is missing"

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

echo "cloud-compose immutable release replacement contracts passed."
