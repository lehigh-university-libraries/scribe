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

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function optionalString(value: unknown): string | undefined {
  return typeof value === "string" ? value : undefined;
}

function optionalStringArray(value: unknown): string[] | undefined {
  return Array.isArray(value) && value.every((entry) => typeof entry === "string") ? value : undefined;
}

function idValue(value: unknown, field: string): string | number {
  if (typeof value === "string" || typeof value === "number") return value;
  throw new Error(`invalid ${field} in auth response`);
}

function stringValue(value: unknown, field: string): string {
  if (typeof value === "string") return value;
  throw new Error(`invalid ${field} in auth response`);
}

function parseAuthMe(value: unknown): AuthMeResponse {
  if (!isRecord(value) || typeof value.authenticated !== "boolean") {
    throw new Error("invalid auth response");
  }
  const response: AuthMeResponse = {
    authenticated: value.authenticated,
    authType: optionalString(value.authType),
    loginUrl: stringValue(value.loginUrl, "loginUrl"),
    logoutUrl: stringValue(value.logoutUrl, "logoutUrl"),
  };
  if (isRecord(value.user)) {
    response.user = {
      id: idValue(value.user.id, "user.id"),
      email: optionalString(value.user.email),
      name: optionalString(value.user.name),
      pictureUrl: optionalString(value.user.pictureUrl),
      isAdmin: typeof value.user.isAdmin === "boolean" ? value.user.isAdmin : undefined,
      defaultWorkspaceId: typeof value.user.defaultWorkspaceId === "string" || typeof value.user.defaultWorkspaceId === "number"
        ? value.user.defaultWorkspaceId
        : undefined,
    };
  }
  if (isRecord(value.workspace)) {
    response.workspace = {
      id: idValue(value.workspace.id, "workspace.id"),
      name: optionalString(value.workspace.name),
      role: optionalString(value.workspace.role),
    };
  }
  return response;
}

function parseAPIKeyRecord(value: unknown): APIKeyRecord {
  if (!isRecord(value)) throw new Error("invalid api key response");
  return {
    id: idValue(value.id, "apiKey.id"),
    workspace_id: idValue(value.workspace_id, "apiKey.workspace_id"),
    created_by_user_id: idValue(value.created_by_user_id, "apiKey.created_by_user_id"),
    name: stringValue(value.name, "apiKey.name"),
    key_prefix: stringValue(value.key_prefix, "apiKey.key_prefix"),
    role: stringValue(value.role, "apiKey.role"),
    scopes: optionalStringArray(value.scopes),
    last_used_at: optionalString(value.last_used_at),
    expires_at: optionalString(value.expires_at),
    created_at: stringValue(value.created_at, "apiKey.created_at"),
    updated_at: stringValue(value.updated_at, "apiKey.updated_at"),
  };
}

function parseProviderSecretRecord(value: unknown): ProviderSecretRecord {
  if (!isRecord(value)) throw new Error("invalid provider secret response");
  const scope = value.scope;
  if (scope !== "user" && scope !== "workspace") {
    throw new Error("invalid provider secret scope in auth response");
  }
  return {
    id: idValue(value.id, "providerSecret.id"),
    user_id: typeof value.user_id === "string" || typeof value.user_id === "number" ? value.user_id : undefined,
    workspace_id: typeof value.workspace_id === "string" || typeof value.workspace_id === "number" ? value.workspace_id : undefined,
    provider: stringValue(value.provider, "providerSecret.provider"),
    name: stringValue(value.name, "providerSecret.name"),
    vault_path: stringValue(value.vault_path, "providerSecret.vault_path"),
    key_hint: optionalString(value.key_hint),
    scope,
    created_at: stringValue(value.created_at, "providerSecret.created_at"),
    updated_at: stringValue(value.updated_at, "providerSecret.updated_at"),
  };
}

export async function getAuthMe(): Promise<AuthMeResponse> {
  const response = await scribeFetch("/auth/me");
  if (!response.ok) {
    throw new Error(`failed to load auth state (${response.status})`);
  }
  return parseAuthMe(await response.json());
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
  const body: unknown = await response.json();
  if (!isRecord(body) || !Array.isArray(body.apiKeys)) {
    throw new Error("invalid api keys response");
  }
  return body.apiKeys.map(parseAPIKeyRecord);
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
  const body: unknown = await response.json();
  if (!isRecord(body)) throw new Error("invalid api key creation response");
  return {
    apiKey: parseAPIKeyRecord(body.apiKey),
    key: stringValue(body.key, "key"),
  };
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
  const body: unknown = await response.json();
  if (!isRecord(body) || !Array.isArray(body.providerSecrets)) {
    throw new Error("invalid provider secrets response");
  }
  return body.providerSecrets.map(parseProviderSecretRecord);
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
  const body: unknown = await response.json();
  if (!isRecord(body) || !body.providerSecret) {
    throw new Error("missing providerSecret in response");
  }
  return parseProviderSecretRecord(body.providerSecret);
}

export async function deleteProviderSecret(secretID: number | string): Promise<void> {
  const response = await scribeFetch(`/auth/provider-secrets/${secretID}`, {
    method: "DELETE",
  });
  if (!response.ok) {
    throw new Error(`failed to delete provider secret (${response.status})`);
  }
}
