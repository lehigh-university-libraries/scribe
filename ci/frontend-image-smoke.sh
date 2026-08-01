#!/usr/bin/env bash

set -euo pipefail

image="${1:-${FRONTEND_IMAGE:-}}"
if [[ ! "$image" =~ @sha256:[0-9a-f]{64}$ ]] && [[ ! "$image" =~ ^scribe-frontend:[A-Za-z0-9._-]+$ ]]; then
  echo "usage: $0 <digest-pinned-image>; local scribe-frontend:<tag> is also accepted" >&2
  exit 2
fi
command -v docker >/dev/null 2>&1 || {
  echo "docker is required for the frontend runtime image smoke test" >&2
  exit 127
}

if ! container_id="$(docker create \
  --read-only \
  --tmpfs /tmp:rw,noexec,nosuid,size=16m \
  --env SCRIBE_FRONTEND_EDGE_MODE=direct \
  "$image")"; then
  echo "failed to create frontend image $image" >&2
  exit 1
fi

# shellcheck disable=SC2329 # Invoked through the EXIT trap below.
cleanup() {
  docker rm -f "$container_id" >/dev/null 2>&1 || true
}
trap cleanup EXIT

docker start "$container_id" >/dev/null
for _ in $(seq 1 30); do
  if docker exec "$container_id" node -e \
    "fetch('http://127.0.0.1:8888/').then(r => { if (!r.ok) throw new Error('status ' + r.status) })" \
    >/dev/null 2>&1; then
    echo "Frontend runtime image smoke passed for $image."
    exit 0
  fi
  if [ "$(docker inspect --format '{{.State.Running}}' "$container_id" 2>/dev/null || printf false)" != "true" ]; then
    echo "frontend image exited before serving its packaged application" >&2
    docker logs "$container_id" >&2 || true
    exit 1
  fi
  sleep 1
done

echo "frontend image did not serve its packaged application within 30 seconds" >&2
docker logs "$container_id" >&2 || true
exit 1
