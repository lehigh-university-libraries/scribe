# syntax=docker/dockerfile:1.28@sha256:bb22d9815c728170f72750f4e5b0d672e06176142e1d602c7e66c050100b7e5b
FROM golang:1.27.3-alpine@sha256:b8c9b82affbbb82a020bbcf998dad2ba9516944ac7e883524642a481a9505e9c AS go-base
FROM islandora/scyllaridae:7@sha256:ed6d2c2e0e0ba0aa225081912848de010ab706940735775c0b3926c5532739d8 AS scyllaridae
FROM gcr.io/cloud-sql-connectors/cloud-sql-proxy:2.26.3@sha256:f333a9c59890c3c67b8505166c5dbdcc0b29fca9173a2e09ca0446ab6997a736 AS cloudsql

# Repeated containerized tests reuse this prepared toolchain instead of
# resolving Alpine packages for every test invocation. This stage is not a
# dependency of the production image.
FROM go-base AS test-runner
ARG SCRIBE_TEST_RUNNER_FINGERPRINT
WORKDIR /app
RUN apk add --no-cache \
    build-base=0.5-r4 \
    libxml2-utils=2.13.9-r2
LABEL org.libops.scribe.test-runner-fingerprint=${SCRIBE_TEST_RUNNER_FINGERPRINT}

FROM go-base AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod,sharing=locked \
    go mod download

COPY . .
RUN --mount=type=cache,target=/root/.cache/go-build,sharing=locked \
    --mount=type=cache,target=/go/pkg/mod,sharing=locked \
    CGO_ENABLED=0 GOOS=linux go build -tags remoteocr -o /out/scribe-api ./cmd/api \
    && CGO_ENABLED=0 GOOS=linux go build -tags remoteocr -o /out/scribe-worker ./cmd/worker \
    && CGO_ENABLED=0 GOOS=linux go build -tags remoteocr -o /out/scribe-migrate ./cmd/migrate \
    && CGO_ENABLED=0 GOOS=linux go build -o /out/scribe-cloudsql ./cmd/cloudsql \
    && CGO_ENABLED=0 GOOS=linux go build -o /out/scribe-readiness ./cmd/readiness \
    && CGO_ENABLED=0 GOOS=linux go build -o /out/scribe-pdf-export ./cmd/pdf-export

FROM alpine:3.24@sha256:be2656061b536c1883c4034790e21d0a09c5aaaf51968b89368a3c0371ea2c9a
WORKDIR /app
RUN apk add --no-cache \
    ca-certificates=20261020-r0 \
    curl=8.23.0-r0 \
    jq=1.8.2-r0 \
    openssl=3.5.9-r0 \
    python3=3.14.8-r0 \
    py3-pip=26.1.2-r0 \
    poppler-utils=25.12.0-r1
COPY config/pdf/requirements.txt /app/pdf-requirements.txt
RUN python3 -m venv /opt/pdf \
    && /opt/pdf/bin/pip install --no-cache-dir --require-hashes --only-binary=:all: -r /app/pdf-requirements.txt
RUN adduser -D -u 10001 appuser
COPY --from=builder /out/scribe-api /app/scribe-api
COPY --from=builder /out/scribe-worker /app/scribe-worker
COPY --from=builder /out/scribe-migrate /app/scribe-migrate
COPY --from=builder /out/scribe-cloudsql /app/scribe-cloudsql
COPY --from=builder /out/scribe-readiness /app/scribe-readiness
COPY --from=cloudsql /cloud-sql-proxy /cloud-sql-proxy
COPY --from=builder /out/scribe-pdf-export /app/scribe-pdf-export
COPY --from=scyllaridae /app/scyllaridae /app/scyllaridae
COPY config/pdf/scyllaridae.yml /app/scyllaridae.yml
COPY docker-entrypoint.sh /app/docker-entrypoint.sh
COPY scripts/vault-init.sh /usr/local/bin/vault-init.sh
COPY scripts/vault-retry.sh /usr/local/lib/scribe/vault-retry.sh
RUN chmod 755 /app/docker-entrypoint.sh \
    /usr/local/bin/vault-init.sh \
    /usr/local/lib/scribe/vault-retry.sh \
    && mkdir -p /app/uploads /app/cache \
    && chown -R appuser:appuser /app
COPY config.yaml /etc/scribe/config.yaml
USER appuser
EXPOSE 8080
ENTRYPOINT ["/app/docker-entrypoint.sh"]
