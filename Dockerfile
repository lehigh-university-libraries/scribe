# syntax=docker/dockerfile:1.23@sha256:2780b5c3bab67f1f76c781860de469442999ed1a0d7992a5efdf2cffc0e3d769

FROM node:24-alpine@sha256:d1b3b4da11eefd5941e7f0b9cf17783fc99d9c6fc34884a665f40a06dbdfc94f AS plugin-build

WORKDIR /plugin
COPY mirador-scribe/package*.json ./
RUN --mount=type=cache,target=/root/.npm \
    npm install --ignore-scripts --prefer-offline --no-audit --progress=false
COPY mirador-scribe/ ./
RUN npm run build

FROM node:24-alpine@sha256:d1b3b4da11eefd5941e7f0b9cf17783fc99d9c6fc34884a665f40a06dbdfc94f AS web-build

WORKDIR /app
RUN mkdir -p /app/mirador-scribe/dist
COPY --from=plugin-build /plugin/package.json /app/mirador-scribe/package.json
COPY --from=plugin-build /plugin/dist /app/mirador-scribe/dist

WORKDIR /app/web

COPY web/package*.json ./
RUN --mount=type=cache,target=/root/.npm \
    npm install --ignore-scripts --prefer-offline --no-audit --progress=false

COPY web/ ./
RUN mkdir -p /app/web/vendor/mirador-scribe \
    && cp -R /app/mirador-scribe/dist /app/web/vendor/mirador-scribe/dist
RUN npm run build

FROM golang:1.26-alpine@sha256:f85330846cde1e57ca9ec309382da3b8e6ae3ab943d2739500e08c86393a21b1 AS builder

WORKDIR /app

RUN apk add --no-cache \
    build-base \
    tesseract-ocr-dev \
    leptonica-dev

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=1 GOOS=linux go build -o /out/scribe ./cmd/api

FROM alpine:3.23@sha256:5b10f432ef3da1b8d4c7eb6c487f2f5a8f096bc91145e68878dd4a5019afde11
WORKDIR /app
RUN apk add --no-cache \
    ca-certificates \
    imagemagick \
    tesseract-ocr \
    tesseract-ocr-data-eng \
    libstdc++
RUN adduser -D -u 10001 appuser
COPY --from=builder /out/scribe /app/scribe
COPY --from=web-build /app/web/dist /app/web-dist
COPY docker-entrypoint.sh /app/docker-entrypoint.sh
RUN chmod 755 /app/docker-entrypoint.sh \
    && mkdir -p /app/uploads /app/cache \
    && chown -R appuser:appuser /app
USER appuser
EXPOSE 8080
ENV LISTEN_ADDR=:8080
ENTRYPOINT ["/app/docker-entrypoint.sh"]
