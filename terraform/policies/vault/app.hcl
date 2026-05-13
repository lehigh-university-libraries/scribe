path "secret/data/scribe/google_oauth" {
  capabilities = ["read"]
}

path "secret/data/scribe/openai" {
  capabilities = ["read"]
}

path "secret/data/scribe/gemini" {
  capabilities = ["read"]
}

path "secret/data/scribe/database/app" {
  capabilities = ["read"]
}

path "secret/data/scribe/provider-secrets/workspaces/*" {
  capabilities = ["create", "read"]
}

path "secret/metadata/scribe/provider-secrets/workspaces/*" {
  capabilities = ["delete"]
}
