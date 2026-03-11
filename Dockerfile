# syntax=docker/dockerfile:1.22@sha256:4a43a54dd1fedceb30ba47e76cfcf2b47304f4161c0caeac2db1c61804ea3c91

FROM node:24-alpine@sha256:7fddd9ddeae8196abf4a3ef2de34e11f7b1a722119f91f28ddf1e99dcafdf114 AS plugin-build

WORKDIR /plugin
COPY mirador-scribe/package*.json ./
RUN --mount=type=cache,target=/root/.npm \
    npm install --ignore-scripts --prefer-offline --no-audit --progress=false
COPY mirador-scribe/ ./
RUN npm run build

FROM node:24-alpine@sha256:7fddd9ddeae8196abf4a3ef2de34e11f7b1a722119f91f28ddf1e99dcafdf114 AS web-build

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

FROM golang:1.26-alpine@sha256:2389ebfa5b7f43eeafbd6be0c3700cc46690ef842ad962f6c5bd6be49ed82039 AS builder

WORKDIR /app

RUN apk add --no-cache \
    build-base \
    tesseract-ocr-dev \
    leptonica-dev

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=1 GOOS=linux go build -o /out/scribe ./cmd/api

FROM alpine:3.23@sha256:25109184c71bdad752c8312a8623239686a9a2071e8825f20acb8f2198c3f659
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
RUN mkdir -p /app/uploads /app/cache && chown -R appuser:appuser /app
USER appuser
EXPOSE 8080
ENV LISTEN_ADDR=:8080
ENTRYPOINT ["/app/scribe"]
