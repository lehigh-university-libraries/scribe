# syntax=docker/dockerfile:1.7@sha256:a57df69d0ea827fb7266491f2813635de6f17269be881f696fbfdf2d83dda33e

FROM node:22-alpine@sha256:8094c002d08262dba12645a3b4a15cd6cd627d30bc782f53229a2ec13ee22a00 AS plugin-build

WORKDIR /plugin
COPY mirador-scribe/package*.json ./
RUN --mount=type=cache,target=/root/.npm \
    npm install --ignore-scripts --prefer-offline --no-audit --progress=false
COPY mirador-scribe/ ./
RUN npm run build

FROM node:22-alpine@sha256:8094c002d08262dba12645a3b4a15cd6cd627d30bc782f53229a2ec13ee22a00 AS web-build

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

FROM golang:1.24-alpine@sha256:8bee1901f1e530bfb4a7850aa7a479d17ae3a18beb6e09064ed54cfd245b7191 AS builder

WORKDIR /app

RUN apk add --no-cache \
    build-base \
    tesseract-ocr-dev \
    leptonica-dev

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=1 GOOS=linux go build -o /out/scribe ./cmd/api

FROM alpine:3.22@sha256:55ae5d250caebc548793f321534bc6a8ef1d116f334f18f4ada1b2daad3251b2
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
