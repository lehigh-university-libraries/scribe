path "auth/token/roles/ci" {
  capabilities = ["create", "read", "update", "delete", "list"]
}

path "sys/auth/gcp" {
  capabilities = ["create", "read", "update", "delete", "list"]
}

path "sys/auth/google-jwt" {
  capabilities = ["create", "read", "update", "delete", "list"]
}

path "sys/mounts/auth/gcp" {
  capabilities = ["read"]
}

path "sys/mounts/auth/gcp/tune" {
  capabilities = ["read", "update"]
}

path "sys/mounts/auth/google-jwt" {
  capabilities = ["read"]
}

path "sys/mounts/auth/google-jwt/tune" {
  capabilities = ["read", "update"]
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
  capabilities = ["create", "read", "update", "delete", "list"]
}

path "sys/audit" {
  capabilities = ["read", "sudo"]
}

path "sys/audit/stdout" {
  capabilities = ["create", "read", "update", "delete", "sudo"]
}

path "sys/policies/acl/ci" {
  capabilities = ["create", "read", "update", "delete", "list"]
}

path "sys/policies/acl/app-*" {
  capabilities = ["create", "read", "update", "delete", "list"]
}

path "sys/policies/acl/operator" {
  capabilities = ["create", "read", "update", "delete", "list"]
}

path "sys/policies/acl/break-glass" {
  capabilities = ["create", "read", "update", "delete", "list"]
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
