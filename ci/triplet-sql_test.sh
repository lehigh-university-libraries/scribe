#!/usr/bin/env bash
set -euo pipefail
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"
COMPOSE_PROJECT_NAME="scribe-ci-triplet-$(date +%s)-$$"
COMPOSE_FILE="$ROOT_DIR/ci/mysql-compose.yaml"
export COMPOSE_PROJECT_NAME COMPOSE_FILE
TRIPLET_IMAGE='ghcr.io/libops/triplet:v1.2.4@sha256:1a1abbae387129c6a157005fdab43f0256adc6a1652a0144df58cdcce825cd20'
export TRIPLET_PRESENTATION_WRITE_TOKEN=triplet-sql-contract-write-token-32bytes
containers=()
runner=
cleanup() {
 if [ -n "$runner" ]; then docker rm -f "$runner" >/dev/null 2>&1 || true; fi
 for container in "${containers[@]}"; do docker rm -f "$container" >/dev/null 2>&1 || true; done
 docker compose down --volumes --remove-orphans >/dev/null
}
trap cleanup EXIT
docker compose up -d --wait --wait-timeout 180
docker compose exec -T mariadb mysql -uroot -pmysql-contract-root-password -e "CREATE DATABASE triplet; GRANT ALL ON triplet.* TO 'scribe'@'%';" >/dev/null
network="${COMPOSE_PROJECT_NAME}_default"
triplet_args=(--network "$network" --env PUBLIC_BASE_URL=https://iiif.example.org --env TRIPLET_PRESENTATION_WRITE_TOKEN --env 'TRIPLET_DATABASE_DSN=scribe:mysql-contract-password@tcp(mariadb:3306)/triplet?parseTime=true')
migration="$(docker create "${triplet_args[@]}" "$TRIPLET_IMAGE" -config /etc/triplet/cloud-run.yaml -migrate-presentation-mariadb)"
containers+=("$migration")
docker cp config/triplet-cloud-run.yaml "$migration:/etc/triplet/cloud-run.yaml"
docker start -a "$migration"
docker rm "$migration" >/dev/null
containers=()
for _ in 1 2; do
 container="$(docker create --publish 127.0.0.1::8082 "${triplet_args[@]}" "$TRIPLET_IMAGE" -config /etc/triplet/cloud-run.yaml)"
 containers+=("$container")
 docker cp config/triplet-cloud-run.yaml "$container:/etc/triplet/cloud-run.yaml"
 docker start "$container" >/dev/null
done
wait_ready() {
 for container in "${containers[@]}"; do
  local ready=false
  for _ in $(seq 1 60); do
   if docker exec "$container" /usr/local/bin/triplet-healthcheck -url http://127.0.0.1:8082/healthz >/dev/null 2>&1; then ready=true; break; fi
   sleep 1
  done
  if [ "$ready" != true ]; then docker logs "$container"; return 1; fi
 done
}
wait_ready
first_ip="$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "${containers[0]}")"
second_ip="$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "${containers[1]}")"
run_test() {
 local phase="$1"
 runner="$(docker create --network "$network" --workdir /app --env "TRIPLET_SQL_FIRST=http://$first_ip:8082" --env "TRIPLET_SQL_SECOND=http://$second_ip:8082" --env TRIPLET_PRESENTATION_WRITE_TOKEN --env "TRIPLET_SQL_PHASE=$phase" --mount type=volume,src=scribe-go-test-mod-cache-v1,dst=/go/pkg/mod --mount type=volume,src=scribe-go-test-build-cache-v1,dst=/root/.cache/go-build golang:1.27.1-alpine@sha256:8a5910f31396cd4d89662f56c68b3ae31d374308270a1c3bd96672ee5ed43414 go test ./internal/iiif -run '^TestTripletSQLReplicas$' -count=1)"
 tar -cf - go.mod go.sum internal | docker cp - "$runner:/app"
 docker start -a "$runner"
 docker rm "$runner" >/dev/null
 runner=
}
run_test initial
docker restart "${containers[@]}" >/dev/null
wait_ready
run_test restart
