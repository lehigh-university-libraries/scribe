# syntax=docker/dockerfile:1.27@sha256:bde3983e9c939224420ddaf6b784cc30e09b035a4dea01f581230c50809f372e
FROM golang:1.27.1-alpine@sha256:cf6fca6641884b8433441b2b0652976f975e1d0fdd26d177eaaf8596087f3125 AS go-base

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
    && CGO_ENABLED=0 GOOS=linux go build -tags remoteocr -o /out/scribe-browser-session ./cmd/browser-session

FROM alpine:3.24@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b
WORKDIR /app
RUN apk add --no-cache \
    ca-certificates=20260611-r0 \
    curl=8.22.0-r0 \
    jq=1.8.2-r0 \
    openssl=3.5.8-r0
RUN adduser -D -u 10001 appuser
COPY --from=builder /out/scribe-api /app/scribe-api
COPY --from=builder /out/scribe-worker /app/scribe-worker
COPY --from=builder /out/scribe-browser-session /app/scribe-browser-session
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
