# renovate: datasource=docker depName=hashicorp/vault
FROM hashicorp/vault:1.21.4@sha256:6c77f568e6b6310d5bc68befb5711b9215c574de7da489e7c24332581176888b AS vault

FROM alpine:3.23@sha256:5b10f432ef3da1b8d4c7eb6c487f2f5a8f096bc91145e68878dd4a5019afde11
RUN apk add --no-cache ca-certificates=20260611-r0
RUN mkdir -p /etc/vault
COPY --from=vault /bin/vault /vault
COPY vault-server.hcl.tmpl /etc/vault/config.hcl.tmpl
COPY docker-entrypoint.sh /docker-entrypoint.sh
RUN chmod +x /docker-entrypoint.sh
ENTRYPOINT ["/docker-entrypoint.sh"]
