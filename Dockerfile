FROM node:22-alpine AS mae-build

WORKDIR /plugin
COPY mirador-annotation-editor/package*.json ./
RUN npm install --ignore-scripts
COPY mirador-annotation-editor/ ./
RUN npm run build

FROM node:22-alpine AS web-build

WORKDIR /app
RUN mkdir -p /app/mirador-annotation-editor/dist
COPY --from=mae-build /plugin/package.json /app/mirador-annotation-editor/package.json
COPY --from=mae-build /plugin/dist /app/mirador-annotation-editor/dist

WORKDIR /app/web

COPY web/package*.json ./
RUN npm install --ignore-scripts

COPY web/ ./
RUN mkdir -p /app/web/vendor/mirador-annotation-editor \
    && cp -R /app/mirador-annotation-editor/dist /app/web/vendor/mirador-annotation-editor/dist
RUN npm run build

FROM golang:1.24-alpine AS builder

WORKDIR /app

RUN apk add --no-cache \
    build-base \
    tesseract-ocr-dev \
    leptonica-dev

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=1 GOOS=linux go build -o /out/hocredit ./cmd/api

FROM alpine:3.22
WORKDIR /app
RUN apk add --no-cache \
    ca-certificates \
    imagemagick \
    tesseract-ocr \
    tesseract-ocr-data-eng \
    libstdc++
RUN adduser -D -u 10001 appuser
COPY --from=builder /out/hocredit /app/hocredit
COPY --from=web-build /app/web/dist /app/web-dist
RUN mkdir -p /app/uploads /app/cache && chown -R appuser:appuser /app
USER appuser
EXPOSE 8080
ENV LISTEN_ADDR=:8080
ENTRYPOINT ["/app/hocredit"]
