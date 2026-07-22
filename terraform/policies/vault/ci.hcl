# Protected preview orchestration creates only an isolated MariaDB bootstrap
# value. It may enumerate and delete a preview namespace during teardown, but it
# cannot administer Vault, rewrite this policy, or access long-lived dev/prod
# application data.
# The protected workflow uses one shared orchestration identity. Restrict it to
# deterministic preview service-account namespaces; runtime isolation is
# enforced separately by the identity-templated scribe-preview-app policy.
path "secret/data/scribe/previews/scribe-pr-*" {
  capabilities = ["create", "read", "update"]
}

path "secret/metadata/scribe/previews/scribe-pr-*" {
  capabilities = ["read", "delete", "list"]
}

path "secret/data/scribe/dev/*" {
  capabilities = ["deny"]
}

path "secret/data/scribe/prod/*" {
  capabilities = ["deny"]
}

path "secret/metadata/scribe/dev" {
  capabilities = ["deny"]
}

path "secret/metadata/scribe/dev/*" {
  capabilities = ["deny"]
}

path "secret/metadata/scribe/prod" {
  capabilities = ["deny"]
}

path "secret/metadata/scribe/prod/*" {
  capabilities = ["deny"]
}
