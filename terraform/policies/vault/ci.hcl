path "auth/token/create" {
  capabilities = ["update"]
}

path "sys/auth/gcp" {
  capabilities = ["create", "read", "update", "list"]
}

path "sys/auth/google-jwt" {
  capabilities = ["create", "read", "update", "list"]
}

path "auth/gcp/role/scribe-app-*" {
  capabilities = ["create", "read", "update", "delete", "list"]
}

path "auth/gcp/role/ci-*" {
  capabilities = ["create", "read", "update", "delete", "list"]
}

path "auth/google-jwt/config" {
  capabilities = ["create", "read", "update", "list"]
}

path "auth/google-jwt/role/ci-*" {
  capabilities = ["create", "read", "update", "delete", "list"]
}

path "sys/mounts/secret" {
  capabilities = ["create", "read", "update", "list"]
}

path "sys/policies/acl/ci" {
  capabilities = ["create", "read", "update", "list"]
}

path "sys/policies/acl/app" {
  capabilities = ["read", "list"]
}

path "sys/policies/acl/operator" {
  capabilities = ["read", "list"]
}

path "sys/policies/acl/break-glass" {
  capabilities = ["read", "list"]
}

path "auth/gcp/role/operator*" {
  capabilities = ["deny"]
}

path "auth/google-jwt/role/admin*" {
  capabilities = ["deny"]
}

path "auth/google-jwt/role/break-glass*" {
  capabilities = ["deny"]
}

path "identity/oidc/config" {
  capabilities = ["create", "read", "update", "list"]
}
