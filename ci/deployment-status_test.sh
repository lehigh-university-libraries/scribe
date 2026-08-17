#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

run_status_unit_tests() {
  if command -v go >/dev/null 2>&1 &&
    [ "$(go env GOVERSION)" = "go$(tr -d '\n' <"$ROOT_DIR/.go-version")" ]; then
    (cd "$ROOT_DIR" && go test ./internal/deployer -run '^TestResolveStatus')
    return
  fi

  docker run --rm --network none --read-only \
    --tmpfs /tmp:rw,noexec,nosuid,size=256m \
    -e GOCACHE=/tmp/go-build \
    -e GOMODCACHE=/tmp/go-mod \
    -v "$ROOT_DIR:/app:ro" \
    -w /app \
    golang:1.26.6-alpine@sha256:af8d6740070b8906d12eae1c3e3ea0957fb63f492051ea05e354c38ef9fe88df \
    go test ./internal/deployer -run '^TestResolveStatus'
}

run_status_unit_tests

attestation_dir="$(mktemp -d)"
trap 'rm -rf "$attestation_dir"' EXIT
runtime_digest="sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
provenance_digest="sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
cat >"$attestation_dir/frontend-index.json" <<JSON
{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"$runtime_digest","size":1234,"platform":{"os":"linux","architecture":"amd64"}},{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"$provenance_digest","size":567,"platform":{"os":"unknown","architecture":"unknown"}}]}
JSON
index_digest="sha256:$(sha256sum "$attestation_dir/frontend-index.json" | awk '{print $1}')"
expected_image="us-docker.pkg.dev/example/internal/scribe-frontend@$index_digest"
expected_runtime_image="us-docker.pkg.dev/example/internal/scribe-frontend@$runtime_digest"

[ "$(
  "$ROOT_DIR/ci/resolve-oci-platform-image.sh" \
    "$expected_image" linux/amd64 "$attestation_dir/frontend-index.json"
)" = "$expected_runtime_image" ]

cat >"$attestation_dir/service.json" <<'JSON'
{
  "status": {
    "conditions": [{"type": "Ready", "status": "True"}],
    "latestCreatedRevisionName": "scribe-00042-abc",
    "latestReadyRevisionName": "scribe-00042-abc",
    "traffic": [{"revisionName": "scribe-00042-abc", "percent": 100}]
  }
}
JSON
cat >"$attestation_dir/revision.json" <<JSON
{
  "metadata": {"name": "scribe-00042-abc"},
  "spec": {"containers": [
    {"name": "proxy-power-button", "image": "example.invalid/ppb@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
    {"name": "frontend", "image": "$expected_runtime_image"}
  ]},
  "status": {"conditions": [{"type": "Ready", "status": "True"}]}
}
JSON
[ "$(
  "$ROOT_DIR/ci/attest-cloud-run-revision.sh" \
    "$attestation_dir/service.json" \
    "$attestation_dir/revision.json" \
    "$expected_image" \
    "$attestation_dir/frontend-index.json"
)" = "scribe-00042-abc" ]

jq --arg image "$expected_image" '.spec.containers[1].image = $image' \
  "$attestation_dir/revision.json" >"$attestation_dir/index-image.json"
"$ROOT_DIR/ci/attest-cloud-run-revision.sh" \
  "$attestation_dir/service.json" \
  "$attestation_dir/index-image.json" \
  "$expected_image" \
  "$attestation_dir/frontend-index.json" >/dev/null

jq '.status.traffic[0].percent = 99' "$attestation_dir/service.json" >"$attestation_dir/bad-traffic.json"
if "$ROOT_DIR/ci/attest-cloud-run-revision.sh" "$attestation_dir/bad-traffic.json" "$attestation_dir/revision.json" "$expected_image" "$attestation_dir/frontend-index.json" >/dev/null 2>&1; then
  echo "attestation accepted less than 100% traffic" >&2
  exit 1
fi
jq '.spec.containers[1].image = "example.invalid/frontend:mutable"' "$attestation_dir/revision.json" >"$attestation_dir/bad-image.json"
if "$ROOT_DIR/ci/attest-cloud-run-revision.sh" "$attestation_dir/service.json" "$attestation_dir/bad-image.json" "$expected_image" "$attestation_dir/frontend-index.json" >/dev/null 2>&1; then
  echo "attestation accepted the wrong frontend image" >&2
  exit 1
fi
jq --arg image "us-docker.pkg.dev/example/internal/scribe-frontend@sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd" '.spec.containers[1].image = $image' \
  "$attestation_dir/revision.json" >"$attestation_dir/wrong-digest.json"
if "$ROOT_DIR/ci/attest-cloud-run-revision.sh" "$attestation_dir/service.json" "$attestation_dir/wrong-digest.json" "$expected_image" "$attestation_dir/frontend-index.json" >/dev/null 2>&1; then
  echo "attestation accepted an unreviewed digest from the reviewed repository" >&2
  exit 1
fi
jq --arg image "other.invalid/frontend@$runtime_digest" '.spec.containers[1].image = $image' \
  "$attestation_dir/revision.json" >"$attestation_dir/wrong-repository.json"
if "$ROOT_DIR/ci/attest-cloud-run-revision.sh" "$attestation_dir/service.json" "$attestation_dir/wrong-repository.json" "$expected_image" "$attestation_dir/frontend-index.json" >/dev/null 2>&1; then
  echo "attestation accepted the runtime digest from an unreviewed repository" >&2
  exit 1
fi
jq '.status.latestReadyRevisionName = "scribe-00041-old"' "$attestation_dir/service.json" >"$attestation_dir/stale-ready.json"
if "$ROOT_DIR/ci/attest-cloud-run-revision.sh" "$attestation_dir/stale-ready.json" "$attestation_dir/revision.json" "$expected_image" "$attestation_dir/frontend-index.json" >/dev/null 2>&1; then
  echo "attestation accepted a stale Ready revision" >&2
  exit 1
fi

cat >"$attestation_dir/ambiguous-index.json" <<JSON
{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[{"digest":"$runtime_digest","platform":{"os":"linux","architecture":"amd64"}},{"digest":"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","platform":{"os":"linux","architecture":"amd64"}}]}
JSON
ambiguous_digest="sha256:$(sha256sum "$attestation_dir/ambiguous-index.json" | awk '{print $1}')"
if "$ROOT_DIR/ci/resolve-oci-platform-image.sh" \
  "us-docker.pkg.dev/example/internal/scribe-frontend@$ambiguous_digest" \
  linux/amd64 "$attestation_dir/ambiguous-index.json" >/dev/null 2>&1; then
  echo "platform resolver accepted an ambiguous OCI index" >&2
  exit 1
fi

cat >"$attestation_dir/arm-only-index.json" <<JSON
{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[{"digest":"$runtime_digest","platform":{"os":"linux","architecture":"arm64"}}]}
JSON
arm_only_digest="sha256:$(sha256sum "$attestation_dir/arm-only-index.json" | awk '{print $1}')"
if "$ROOT_DIR/ci/resolve-oci-platform-image.sh" \
  "us-docker.pkg.dev/example/internal/scribe-frontend@$arm_only_digest" \
  linux/amd64 "$attestation_dir/arm-only-index.json" >/dev/null 2>&1; then
  echo "platform resolver accepted an OCI index without linux/amd64" >&2
  exit 1
fi

if "$ROOT_DIR/ci/resolve-oci-platform-image.sh" \
  "us-docker.pkg.dev/example/internal/scribe-frontend@sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee" \
  linux/amd64 "$attestation_dir/frontend-index.json" >/dev/null 2>&1; then
  echo "platform resolver accepted manifest bytes from a different digest" >&2
  exit 1
fi

single_manifest='{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"digest":"sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"}}'
printf '%s' "$single_manifest" >"$attestation_dir/single-manifest.json"
single_digest="sha256:$(sha256sum "$attestation_dir/single-manifest.json" | awk '{print $1}')"
single_image="us-docker.pkg.dev/example/internal/scribe-frontend@$single_digest"
[ "$("$ROOT_DIR/ci/resolve-oci-platform-image.sh" "$single_image" linux/amd64 "$attestation_dir/single-manifest.json")" = "$single_image" ]

mkdir "$attestation_dir/bin"
cat >"$attestation_dir/bin/docker" <<'SH'
#!/bin/sh
set -eu
if [ "${1:-}" = "buildx" ]; then
  attempt=0
  [ ! -f "$OCI_TEST_ATTEMPTS" ] || attempt="$(cat "$OCI_TEST_ATTEMPTS")"
  attempt=$((attempt + 1))
  printf '%s\n' "$attempt" >"$OCI_TEST_ATTEMPTS"
  [ "$attempt" -gt 1 ] || exit 1
  cat "$OCI_TEST_MANIFEST"
elif [ "${1:-}" = "login" ]; then
  read -r token
  [ "$token" = "refreshed-token" ]
  : >"$OCI_TEST_LOGIN"
else
  exit 2
fi
SH
cat >"$attestation_dir/bin/gcloud" <<'SH'
#!/bin/sh
set -eu
[ "$*" = "auth print-access-token" ]
printf '%s\n' refreshed-token
SH
chmod 0755 "$attestation_dir/bin/docker" "$attestation_dir/bin/gcloud"
OCI_TEST_ATTEMPTS="$attestation_dir/attempts" \
  OCI_TEST_LOGIN="$attestation_dir/login" \
  OCI_TEST_MANIFEST="$attestation_dir/single-manifest.json" \
  PATH="$attestation_dir/bin:$PATH" \
  "$ROOT_DIR/ci/resolve-oci-platform-image.sh" "$single_image" linux/amd64 >/dev/null
[ -f "$attestation_dir/login" ] && [ "$(cat "$attestation_dir/attempts")" = 2 ] || {
  echo "platform resolver did not refresh an expired GAR login exactly once" >&2
  exit 1
}

echo "Deployment status matrix contracts passed."
