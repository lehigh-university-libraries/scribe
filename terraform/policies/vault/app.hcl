path "secret/*" {
  capabilities = ["create", "read", "update", "delete", "list"]
}

path "keys/*" {
  capabilities = ["create", "read", "update", "delete", "list"]
}
