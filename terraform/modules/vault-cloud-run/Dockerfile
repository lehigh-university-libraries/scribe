# renovate: datasource=docker depName=hashicorp/vault
FROM hashicorp/vault:1.21.4@sha256:6c77f568e6b6310d5bc68befb5711b9215c574de7da489e7c24332581176888b AS vault

FROM alpine:3.24@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b
RUN apk add --no-cache ca-certificates=20260611-r0
RUN mkdir -p /etc/vault
COPY --from=vault /bin/vault /vault
COPY vault-server.hcl.tmpl /etc/vault/config.hcl.tmpl
COPY docker-entrypoint.sh /docker-entrypoint.sh
RUN chmod +x /docker-entrypoint.sh
ENTRYPOINT ["/docker-entrypoint.sh"]
