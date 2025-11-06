FROM islandora/leptonica:main AS leptonica
FROM islandora/houdini:main

WORKDIR /app

ARG \
    # renovate: datasource=repology depName=alpine_3_22/poppler-utils
    POPPLER_VERSION=25.04.0-r0 \
    # renovate: datasource=repology depName=alpine_3_22/tesseract-ocr
    TESSERACT_VERSION=5.5.0-r2

RUN --mount=type=cache,id=hypercube-apk,sharing=locked,target=/var/cache/apk \
    --mount=type=bind,from=leptonica,source=/packages,target=/packages \
    --mount=type=bind,from=leptonica,source=/etc/apk/keys,target=/etc/apk/keys \
  apk add --no-cache \
      /packages/leptonica-*.apk \
      poppler-utils=="${POPPLER_VERSION}" \
      fontconfig \
      ttf-dejavu \
      go \
      g++ \
      git \
      musl-dev \
      tesseract-ocr=="${TESSERACT_VERSION}" \
      tesseract-ocr-dev=="${TESSERACT_VERSION}" \
      tesseract-ocr-data-eng=="${TESSERACT_VERSION}"

COPY main.go go.* ./
COPY internal/ ./internal/

ENV \
  TESSDATA_PREFIX=/usr/share/tessdata \
  PORT=8888

RUN go mod download && \
  go build -o /app/hOCRedit && \
  go clean -cache -modcache

COPY --chown=scyllaridae:scyllaridae static/ ./static/

COPY --link rootfs /

RUN chown -R scyllaridae uploads cache

HEALTHCHECK CMD curl -s http://localhost:8888/healthcheck
