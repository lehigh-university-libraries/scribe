# renovate: datasource=docker depName=hashicorp/vault
FROM hashicorp/vault:1.21.4@sha256:6c77f568e6b6310d5bc68befb5711b9215c574de7da489e7c24332581176888b AS vault

FROM alpine:3.24@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6
RUN apk add --no-cache ca-certificates=20260611-r0
RUN mkdir -p /etc/vault
COPY --from=vault /bin/vault /vault
COPY vault-server.hcl.tmpl /etc/vault/config.hcl.tmpl
COPY docker-entrypoint.sh /docker-entrypoint.sh
RUN chmod +x /docker-entrypoint.sh
ENTRYPOINT ["/docker-entrypoint.sh"]
