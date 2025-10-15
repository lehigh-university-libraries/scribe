FROM ghcr.io/lehigh-university-libraries/scyllaridae-imagemagick:main@sha256:d085b210070148e00a0605ba653e93bc6c6e6d8cfeea74b092a256203864f757

WORKDIR /app

RUN apk update && \
  apk add --no-cache \
      fontconfig \
      ttf-dejavu \
      go && \
  adduser -S -G nobody -u 8888 hocr

COPY --chown=hocr:hocr main.go go.* docker-entrypoint.sh ./
COPY --chown=hocr:hocr internal/ ./internal/

RUN go mod download && \
  go build -o /app/hOCRedit && \
  go clean -cache -modcache

COPY --chown=hocr:hocr static/ ./static/

RUN mkdir uploads cache && \
  chown -R hocr uploads cache

ENTRYPOINT ["/bin/bash"]
CMD ["/app/docker-entrypoint.sh"]

HEALTHCHECK CMD curl -s http://localhost:8888/healthcheck
