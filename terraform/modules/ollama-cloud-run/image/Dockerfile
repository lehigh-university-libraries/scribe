ARG OLLAMA_BASE_IMAGE=ollama/ollama:0.21.1@sha256:cd33c5656285ca365c9c5495fe0240a70aef642088c6427c461fd54b62f80ffb
FROM ${OLLAMA_BASE_IMAGE}

ARG OLLAMA_MODEL

ENV \
  OLLAMA_HOST=0.0.0.0:8080 \
  OLLAMA_MODELS=/models \
  OLLAMA_DEBUG=false \
  OLLAMA_KEEP_ALIVE=-1 \
  OLLAMA_MODEL=${OLLAMA_MODEL}

RUN test -n "${OLLAMA_MODEL}"
RUN ollama serve & sleep 5 && ollama pull "${OLLAMA_MODEL}"

WORKDIR /app

COPY docker-entrypoint.sh .
RUN chmod +x /app/docker-entrypoint.sh

ENTRYPOINT ["/app/docker-entrypoint.sh"]
