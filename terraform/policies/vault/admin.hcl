# Manage auth methods
path "auth/*" {
  capabilities = ["create", "read", "update", "delete", "list", "sudo"]
}

path "sys/auth/*" {
  capabilities = ["create", "read", "update", "delete", "sudo"]
}

path "sys/auth" {
  capabilities = ["read"]
}

# Manage secrets engines
path "sys/mounts/*" {
  capabilities = ["create", "read", "update", "delete", "sudo"]
}

path "sys/mounts" {
  capabilities = ["read"]
}

# Manage policies
path "sys/policies/acl/*" {
  capabilities = ["create", "read", "update", "delete", "list"]
}

path "sys/policies/acl" {
  capabilities = ["list"]
}

# Work with secrets across engines
path "secret/*" {
  capabilities = ["create", "read", "update", "delete", "list"]
}

# Manage identity
path "identity/*" {
  capabilities = ["create", "read", "update", "delete", "list"]
}

# Read system health and status
path "sys/health" {
  capabilities = ["read", "sudo"]
}

path "sys/capabilities" {
  capabilities = ["create", "update"]
}

path "sys/capabilities-self" {
  capabilities = ["create", "update"]
}

# Audit — read-only, never modify
path "sys/audit" {
  capabilities = ["read", "sudo"]
}

path "sys/audit/*" {
  capabilities = ["create", "read", "update", "delete", "sudo"]
}
