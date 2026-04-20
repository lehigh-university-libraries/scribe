import { createConnectTransport } from "@connectrpc/connect-web";
import type { Transport } from "@connectrpc/connect";

let _transport: Transport | null = null;

export interface ScribeTransportOptions {
  baseUrl?: string;
  apiKey?: string;
  workspaceId?: string | number | bigint;
  credentials?: RequestCredentials;
}

export function createScribeTransport(options: ScribeTransportOptions = {}): Transport {
  const {
    apiKey,
    baseUrl = window.location.origin,
    credentials = "include",
    workspaceId,
  } = options;
  return createConnectTransport({
    baseUrl,
    fetch: (input, init) => {
      const headers = new Headers(init?.headers ?? undefined);
      if (apiKey) {
        headers.set("X-Scribe-API-Key", apiKey);
      }
      if (workspaceId !== undefined && workspaceId !== null && `${workspaceId}` !== "") {
        headers.set("X-Scribe-Workspace-ID", `${workspaceId}`);
      }
      return fetch(input, { ...init, credentials, headers });
    },
  });
}

export function getTransport(): Transport {
  if (!_transport) {
    _transport = createScribeTransport();
  }
  return _transport;
}
