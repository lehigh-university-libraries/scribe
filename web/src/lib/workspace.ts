const STORAGE_KEY = "scribe.selectedWorkspaceId";

function normalizeWorkspaceId(value: string | number | bigint | null | undefined): string {
  if (value === null || value === undefined) return "";
  const normalized = `${value}`.trim();
  return /^\d+$/.test(normalized) ? normalized : "";
}

function safeStorageGet(key: string): string {
  try {
    return window.localStorage.getItem(key) ?? "";
  } catch {
    return "";
  }
}

function safeStorageSet(key: string, value: string): void {
  try {
    if (!value) {
      window.localStorage.removeItem(key);
      return;
    }
    window.localStorage.setItem(key, value);
  } catch {
    // Ignore storage failures in private browsing / locked-down contexts.
  }
}

export function workspaceIdFromSearch(search = window.location.search): string {
  const params = new URLSearchParams(search);
  return normalizeWorkspaceId(params.get("workspace_id"));
}

export function getStoredWorkspaceId(): string {
  return normalizeWorkspaceId(safeStorageGet(STORAGE_KEY));
}

export function getCurrentWorkspaceId(): string {
  return workspaceIdFromSearch() || getStoredWorkspaceId();
}

export function setCurrentWorkspaceId(value: string | number | bigint | null | undefined): string {
  const normalized = normalizeWorkspaceId(value);
  safeStorageSet(STORAGE_KEY, normalized);
  return normalized;
}

export function syncWorkspaceSelectionFromLocation(): string {
  const fromSearch = workspaceIdFromSearch();
  if (fromSearch) {
    safeStorageSet(STORAGE_KEY, fromSearch);
    return fromSearch;
  }
  return getStoredWorkspaceId();
}

export function applyWorkspaceToLocation(workspaceId: string | number | bigint | null | undefined): string {
  const normalized = setCurrentWorkspaceId(workspaceId);
  const url = new URL(window.location.href);
  if (normalized) {
    url.searchParams.set("workspace_id", normalized);
  } else {
    url.searchParams.delete("workspace_id");
  }
  window.history.replaceState(window.history.state, "", `${url.pathname}${url.search}${url.hash}`);
  return normalized;
}

export function workspaceHeaders(
  init?: HeadersInit,
  workspaceId?: string | number | bigint | null,
): Headers {
  const headers = new Headers(init ?? undefined);
  const normalized = normalizeWorkspaceId(workspaceId ?? getCurrentWorkspaceId());
  if (normalized) {
    headers.set("X-Scribe-Workspace-ID", normalized);
  }
  return headers;
}

export function workspaceAwarePath(
  path: string,
  workspaceId?: string | number | bigint | null,
): string {
  const url = new URL(path, window.location.origin);
  const normalized = normalizeWorkspaceId(workspaceId ?? getCurrentWorkspaceId());
  if (normalized) {
    url.searchParams.set("workspace_id", normalized);
  }
  return `${url.pathname}${url.search}${url.hash}`;
}
