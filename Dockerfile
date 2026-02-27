FROM node:22-alpine AS web-build

WORKDIR /web

COPY web/package*.json ./
RUN npm install

COPY web/ ./
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
COPY --from=web-build /web/dist /app/web-dist
RUN mkdir -p /app/uploads /app/cache && chown -R appuser:appuser /app
USER appuser
EXPOSE 8080
ENV LISTEN_ADDR=:8080
ENTRYPOINT ["/app/hocredit"]
