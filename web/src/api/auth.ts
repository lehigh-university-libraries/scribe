import { createClient } from "@connectrpc/connect";
import { AuthService } from "../proto/scribe/v1/auth_connect";
import type {
  APIKeyRecord,
  GetAuthMeResponse,
  ProviderSecretRecord,
} from "../proto/scribe/v1/auth_pb";
import { getTransport } from "./transport";
import { scribeFetch } from "./http";

export type { APIKeyRecord, GetAuthMeResponse, ProviderSecretRecord };

export interface CreateAPIKeyRequest {
  name: string;
  role?: string;
  scopes?: string[];
  expiresAt?: string;
}

export interface CreateProviderSecretRequest {
  provider: string;
  name: string;
  apiKey: string;
  scope?: "user" | "workspace";
}

function client() {
  return createClient(AuthService, getTransport());
}

export async function getAuthMe(): Promise<GetAuthMeResponse> {
  return client().getAuthMe({});
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
  const response = await client().listAPIKeys({});
  return response.apiKeys;
}

export async function createAPIKey(input: CreateAPIKeyRequest): Promise<{ apiKey: APIKeyRecord; key: string }> {
  const response = await client().createAPIKey({
    name: input.name,
    role: input.role ?? "",
    scopes: input.scopes ?? [],
    expiresAt: input.expiresAt ?? "",
  });
  if (!response.apiKey) {
    throw new Error("no api key in response");
  }
  return {
    apiKey: response.apiKey,
    key: response.key,
  };
}

export async function deleteAPIKey(keyID: number | string | bigint): Promise<void> {
  await client().deleteAPIKey({ keyId: BigInt(keyID) });
}

export async function listProviderSecrets(): Promise<ProviderSecretRecord[]> {
  const response = await client().listProviderSecrets({});
  return response.providerSecrets;
}

export async function createProviderSecret(input: CreateProviderSecretRequest): Promise<ProviderSecretRecord> {
  const response = await client().createProviderSecret({
    provider: input.provider,
    name: input.name,
    apiKey: input.apiKey,
    scope: input.scope ?? "user",
  });
  if (!response.providerSecret) {
    throw new Error("no provider secret in response");
  }
  return response.providerSecret;
}

export async function deleteProviderSecret(secretID: number | string | bigint): Promise<void> {
  await client().deleteProviderSecret({ secretId: BigInt(secretID) });
}
