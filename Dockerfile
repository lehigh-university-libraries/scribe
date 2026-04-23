# syntax=docker/dockerfile:1.23@sha256:2780b5c3bab67f1f76c781860de469442999ed1a0d7992a5efdf2cffc0e3d769
FROM golang:1.26-alpine@sha256:f85330846cde1e57ca9ec309382da3b8e6ae3ab943d2739500e08c86393a21b1 AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -tags remoteocr -o /out/scribe-api ./cmd/api \
    && CGO_ENABLED=0 GOOS=linux go build -tags remoteocr -o /out/scribe-worker ./cmd/worker

FROM alpine:3.23@sha256:5b10f432ef3da1b8d4c7eb6c487f2f5a8f096bc91145e68878dd4a5019afde11
WORKDIR /app
RUN apk add --no-cache ca-certificates
RUN adduser -D -u 10001 appuser
COPY --from=builder /out/scribe-api /app/scribe-api
COPY --from=builder /out/scribe-worker /app/scribe-worker
COPY docker-entrypoint.sh /app/docker-entrypoint.sh
RUN chmod 755 /app/docker-entrypoint.sh \
    && mkdir -p /app/uploads /app/cache \
    && chown -R appuser:appuser /app
COPY config.yaml /etc/scribe/config.yaml
USER appuser
EXPOSE 8080
ENTRYPOINT ["/app/docker-entrypoint.sh"]
