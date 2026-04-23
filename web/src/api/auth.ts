import { scribeFetch } from "./http";

export interface AuthMeResponse {
  authenticated: boolean;
  authType?: string;
  loginUrl: string;
  logoutUrl: string;
  user?: {
    id: number | string;
    email?: string;
    name?: string;
    pictureUrl?: string;
    isAdmin?: boolean;
    defaultWorkspaceId?: number | string;
  };
  workspace?: {
    id: number | string;
    name?: string;
    role?: string;
  };
}

export interface APIKeyRecord {
  id: number | string;
  workspace_id: number | string;
  created_by_user_id: number | string;
  name: string;
  key_prefix: string;
  role: string;
  scopes?: string[];
  last_used_at?: string;
  expires_at?: string;
  created_at: string;
  updated_at: string;
}

export interface CreateAPIKeyRequest {
  name: string;
  role?: string;
  scopes?: string[];
  expiresAt?: string;
}

export interface ProviderSecretRecord {
  id: number | string;
  user_id?: number | string;
  workspace_id?: number | string;
  provider: string;
  name: string;
  vault_path: string;
  key_hint?: string;
  scope: "user" | "workspace";
  created_at: string;
  updated_at: string;
}

export interface CreateProviderSecretRequest {
  provider: string;
  name: string;
  apiKey: string;
  scope?: "user" | "workspace";
}

export async function getAuthMe(): Promise<AuthMeResponse> {
  const response = await scribeFetch("/auth/me");
  if (!response.ok) {
    throw new Error(`failed to load auth state (${response.status})`);
  }
  return response.json() as Promise<AuthMeResponse>;
}

export async function logout(): Promise<void> {
  const response = await scribeFetch("/logout", {
    method: "POST",
    headers: { Accept: "application/json" },
  });
  if (!response.ok) {
    throw new Error(`failed to logout (${response.status})`);
  }
}

export async function listAPIKeys(): Promise<APIKeyRecord[]> {
  const response = await scribeFetch("/auth/api-keys", {
    headers: { Accept: "application/json" },
  });
  if (!response.ok) {
    throw new Error(`failed to list api keys (${response.status})`);
  }
  const body = await response.json() as { apiKeys?: APIKeyRecord[] };
  return body.apiKeys ?? [];
}

export async function createAPIKey(input: CreateAPIKeyRequest): Promise<{ apiKey: APIKeyRecord; key: string }> {
  const response = await scribeFetch("/auth/api-keys", {
    method: "POST",
    headers: {
      Accept: "application/json",
      "Content-Type": "application/json",
    },
    body: JSON.stringify(input),
  });
  if (!response.ok) {
    throw new Error(`failed to create api key (${response.status})`);
  }
  return response.json() as Promise<{ apiKey: APIKeyRecord; key: string }>;
}

export async function deleteAPIKey(keyID: number | string): Promise<void> {
  const response = await scribeFetch(`/auth/api-keys/${keyID}`, {
    method: "DELETE",
  });
  if (!response.ok) {
    throw new Error(`failed to delete api key (${response.status})`);
  }
}

export async function listProviderSecrets(): Promise<ProviderSecretRecord[]> {
  const response = await scribeFetch("/auth/provider-secrets", {
    headers: { Accept: "application/json" },
  });
  if (!response.ok) {
    throw new Error(`failed to list provider secrets (${response.status})`);
  }
  const body = await response.json() as { providerSecrets?: ProviderSecretRecord[] };
  return body.providerSecrets ?? [];
}

export async function createProviderSecret(input: CreateProviderSecretRequest): Promise<ProviderSecretRecord> {
  const response = await scribeFetch("/auth/provider-secrets", {
    method: "POST",
    headers: {
      Accept: "application/json",
      "Content-Type": "application/json",
    },
    body: JSON.stringify(input),
  });
  if (!response.ok) {
    throw new Error(`failed to create provider secret (${response.status})`);
  }
  const body = await response.json() as { providerSecret?: ProviderSecretRecord };
  if (!body.providerSecret) {
    throw new Error("missing providerSecret in response");
  }
  return body.providerSecret;
}

export async function deleteProviderSecret(secretID: number | string): Promise<void> {
  const response = await scribeFetch(`/auth/provider-secrets/${secretID}`, {
    method: "DELETE",
  });
  if (!response.ok) {
    throw new Error(`failed to delete provider secret (${response.status})`);
  }
}
