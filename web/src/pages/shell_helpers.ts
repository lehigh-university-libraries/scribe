import { subscribeToEvents } from "../api/events";
import { listTranscriptionJobs } from "../api/transcription";
import { workspaceAwarePath } from "../lib/workspace";
import { html, uint64ToString, type TrustedHTML } from "../lib/util";
import type { APIKeyRecord, GetAuthMeResponse, ProviderSecretRecord } from "../api/auth";
import type { Context } from "../proto/scribe/v1/context_pb";
import type { Item } from "../proto/scribe/v1/item_pb";
import type { Workspace, WorkspaceAccess } from "../proto/scribe/v1/workspace_pb";

export const buttons = "inline-flex items-center gap-2 rounded-md border bg-background px-3.5 py-2 text-sm font-medium text-foreground shadow-xs transition hover:bg-accent hover:text-accent-foreground disabled:pointer-events-none disabled:opacity-50";
export const primary = "inline-flex items-center gap-2 rounded-md bg-primary px-3.5 py-2 text-sm font-medium text-primary-foreground transition hover:bg-primary/90 disabled:pointer-events-none disabled:opacity-50";
export const input = "w-full rounded-md border border-input bg-background px-3 py-2 text-sm text-foreground outline-none placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/50 disabled:pointer-events-none disabled:cursor-not-allowed disabled:opacity-50";
export const card = "rounded-lg border bg-card p-5 text-card-foreground shadow-xs";

export interface ShellStateLike {
  auth: GetAuthMeResponse | null;
  workspaces: WorkspaceAccess[];
  currentWorkspaceId: string;
}

export function workspaceIdString(value: string | number | bigint | null | undefined): string {
  const raw = `${value ?? ""}`.trim();
  return raw === "" || raw === "0" ? "" : raw;
}

export function formatDateTime(raw: string): string {
  const date = new Date(raw);
  return Number.isNaN(date.getTime()) ? raw : date.toLocaleString();
}

export function currentWorkspaceAccess(state: ShellStateLike): WorkspaceAccess | undefined {
  return state.workspaces.find((entry) => uint64ToString(entry.workspace?.id ?? 0n) === state.currentWorkspaceId);
}

export function currentWorkspace(state: ShellStateLike): Workspace | undefined {
  return currentWorkspaceAccess(state)?.workspace;
}

export function currentWorkspaceRole(state: ShellStateLike): string {
  return currentWorkspaceAccess(state)?.role ?? state.auth?.workspace?.role ?? "";
}

export function canAdminWorkspace(state: ShellStateLike): boolean {
  return currentWorkspaceRole(state) === "admin" || Boolean(state.auth?.user?.isAdmin);
}

export function canWriteWorkspace(state: ShellStateLike): boolean {
  return ["admin", "write"].includes(currentWorkspaceRole(state)) || Boolean(state.auth?.user?.isAdmin);
}

export function loginHref(auth: GetAuthMeResponse | null): string {
  const base = auth?.loginUrl || "/auth/google";
  const sep = base.includes("?") ? "&" : "?";
  return `${base}${sep}redirect=${encodeURIComponent(window.location.pathname + window.location.search)}`;
}

export function avatar(auth: GetAuthMeResponse | null): string {
  const source = auth?.user?.name?.trim() || auth?.user?.email?.trim() || "Scribe";
  return source.split(/\s+/).slice(0, 2).map((part) => part[0]?.toUpperCase() ?? "").join("").slice(0, 2) || "SC";
}

export function editorHrefForItem(item: Item): string {
  if (item.images.length === 0) return "";
  const params = new URLSearchParams({ itemImageId: uint64ToString(item.images[0].id) });
  if (item.images.length > 1 || item.sourceType === "manifest") params.set("itemId", item.id);
  return workspaceAwarePath(`/editor?${params.toString()}`);
}

export function exportHref(itemId: string, format: string): string {
  return workspaceAwarePath(`/v1/items/${encodeURIComponent(itemId)}/export?format=${encodeURIComponent(format)}`);
}

export function contextOptions(contexts: Context[]): TrustedHTML[] {
  return contexts.filter((ctx) => ctx.id !== 0n).map((ctx) => html`<option value="${ctx.id.toString()}"${ctx.isDefault ? " selected" : ""}>${ctx.name || `Context ${ctx.id}`}</option>`);
}

export function renderItemActions(item: Item): TrustedHTML {
  const openHref = editorHrefForItem(item);
  return html`
    <div class="mt-4 flex flex-wrap items-center gap-2">
      ${openHref ? html`<a href="${openHref}" class="${primary}">Open editor</a>` : html`<span class="rounded-md border px-3 py-2 text-xs text-muted-foreground">No images</span>`}
      <button data-item-logs="${item.id}" class="${buttons}" type="button">Logs</button>
      <button data-item-delete="${item.id}" class="${buttons} text-destructive" type="button">Delete</button>
      ${openHref ? (["hocr", "pagexml", "alto", "txt"] as const).map((format) => html`<a href="${exportHref(item.id, format)}" class="${buttons}" download>${format}</a>`) : ""}
    </div>
  `;
}

export function renderItemCard(item: Item): TrustedHTML {
  return html`
    <article class="${card}">
      <div class="flex items-start justify-between gap-4">
        <div class="min-w-0">
          <h3 class="truncate text-base font-semibold text-foreground">${item.name || item.id}</h3>
          <p class="mt-1 truncate text-xs text-muted-foreground">${item.id}</p>
        </div>
        <span class="rounded-full bg-secondary px-2 py-0.5 text-xs text-secondary-foreground">${item.sourceType}</span>
      </div>
      <p class="mt-3 text-xs text-muted-foreground">${item.images.length} page${item.images.length === 1 ? "" : "s"} · ${formatDateTime(item.createdAt)}</p>
      ${renderItemActions(item)}
    </article>
  `;
}

export function renderProviderSecrets(secrets: ProviderSecretRecord[]): TrustedHTML {
  if (secrets.length === 0) return html`<p class="text-sm text-muted-foreground">No provider keys saved yet.</p>`;
  return html`<div class="grid gap-2">${secrets.map((secret) => html`
    <div class="flex flex-wrap items-center justify-between gap-3 border-t border-border py-3 text-sm">
      <span>${secret.provider} · ${secret.name} · ${secret.scope} · ${secret.keyHint ? `****${secret.keyHint}` : "stored"}</span>
      <button data-provider-secret-delete="${String(secret.id)}" class="${buttons}" type="button">Delete</button>
    </div>
  `)}</div>`;
}

export function renderAPIKeys(keys: APIKeyRecord[]): TrustedHTML {
  if (keys.length === 0) return html`<p class="text-sm text-muted-foreground">No workspace API keys created yet.</p>`;
  return html`<div class="grid gap-2">${keys.map((key) => html`
    <div class="flex flex-wrap items-center justify-between gap-3 border-t border-border py-3 text-sm">
      <span>${key.name} · ${key.role} · <code>${key.keyPrefix}</code></span>
      <button data-api-key-delete="${String(key.id)}" class="${buttons}" type="button">Delete</button>
    </div>
  `)}</div>`;
}

export async function waitForAutomaticTranscriptionStart(itemImageId: string): Promise<void> {
  const jobs = await listTranscriptionJobs(BigInt(itemImageId));
  if (jobs[0]) return;
  await new Promise<void>((resolve) => {
    let sub: { close: () => void } | null = null;
    const timeout = window.setTimeout(() => {
      sub?.close();
      resolve();
    }, 120000);
    sub = subscribeToEvents({ itemImageId, types: ["dev.scribe.transcription.task.started", "dev.scribe.transcription.completed", "dev.scribe.transcription.failed"] }, () => {
      window.clearTimeout(timeout);
      sub?.close();
      resolve();
    });
  });
}
