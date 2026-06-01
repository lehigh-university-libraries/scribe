# Routine operator access: manage the app's KV tree and inspect platform
# configuration without global sudo, auth-backend mutation, or ACL rewrites.
path "auth/*" {
  capabilities = ["read", "list"]
}

path "sys/auth/*" {
  capabilities = ["read", "list"]
}

path "sys/auth" {
  capabilities = ["read"]
}

path "sys/mounts/*" {
  capabilities = ["create", "read", "update", "delete", "list"]
}

path "sys/mounts" {
  capabilities = ["read"]
}

path "sys/policies/acl/*" {
  capabilities = ["read", "list"]
}

path "sys/policies/acl" {
  capabilities = ["list"]
}

path "secret/data/scribe/*" {
  capabilities = ["create", "read", "update", "delete", "list"]
}

path "secret/metadata/scribe/*" {
  capabilities = ["create", "read", "update", "delete", "list"]
}

path "identity/*" {
  capabilities = ["read", "list"]
}

path "sys/health" {
  capabilities = ["read"]
}

path "sys/capabilities" {
  capabilities = ["create", "update"]
}

path "sys/capabilities-self" {
  capabilities = ["create", "update"]
}

path "sys/audit" {
  capabilities = ["read"]
}

path "sys/audit/*" {
  capabilities = ["deny"]
}
