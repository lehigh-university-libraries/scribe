import { workspaceAwarePath, workspaceHeaders } from "../lib/workspace";

export interface ScribeFetchOptions {
  workspaceId?: string | number | bigint | null;
  includeWorkspace?: boolean;
}

export function scribePath(path: string, workspaceId?: string | number | bigint | null): string {
  return workspaceAwarePath(path, workspaceId);
}

export function scribeFetch(
  input: string,
  init: RequestInit = {},
  options: ScribeFetchOptions = {},
): Promise<Response> {
  const headers = options.includeWorkspace === false
    ? new Headers(init.headers ?? undefined)
    : workspaceHeaders(init.headers, options.workspaceId);
  return fetch(input, {
    ...init,
    credentials: init.credentials ?? "include",
    headers,
  });
}
