import { subscribeToEvents } from "../api/events";
import { listTranscriptionJobs } from "../api/transcription";
import { workspaceAwarePath } from "../lib/workspace";
import { html, uint64ToString, type TrustedHTML } from "../lib/util";
import type { APIKeyRecord, GetAuthMeResponse, ProviderSecretRecord } from "../api/auth";
import { AnnotationExportFormat } from "../proto/scribe/v1/annotation_pb";
import type { Context } from "../proto/scribe/v1/context_pb";
import type { ItemSummary } from "../proto/scribe/v1/item_pb";
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

export function editorHrefForItem(item: ItemSummary): string {
  if (!item.previewImage) return "";
  const params = new URLSearchParams({ itemImageId: uint64ToString(item.previewImage.id) });
  if (item.imageCount > 1n || item.sourceType === "manifest") params.set("itemId", item.id);
  return workspaceAwarePath(`/editor?${params.toString()}`);
}

export const itemExportFormats = [
  { format: AnnotationExportFormat.HOCR, label: "hOCR" },
  { format: AnnotationExportFormat.PAGE_XML, label: "PAGE XML" },
  { format: AnnotationExportFormat.ALTO_XML, label: "ALTO XML" },
  { format: AnnotationExportFormat.PLAIN_TEXT, label: "Text" },
] as const;

export interface ItemExportActionState {
  busyFormat?: AnnotationExportFormat;
  error?: string;
}

export function contextOptions(contexts: Context[]): TrustedHTML[] {
  // Value 0 is the resolver-backed "Default" choice rendered by the shell.
  // Concrete presets remain explicit choices even when one of them currently
  // backs the system or workspace default.
  return contexts.filter((ctx) => ctx.id !== 0n).map((ctx) => html`<option value="${ctx.id.toString()}">${ctx.name || `Context ${ctx.id}`}</option>`);
}

export function renderItemActions(item: ItemSummary, exportState?: ItemExportActionState): TrustedHTML {
  const openHref = editorHrefForItem(item);
  const exportBusy = exportState?.busyFormat !== undefined;
  return html`
    <div class="mt-4 flex flex-wrap items-center gap-2">
      ${openHref ? html`<a href="${openHref}" class="${primary}">Open editor</a>` : html`<span class="rounded-md border px-3 py-2 text-xs text-muted-foreground">No images</span>`}
      <button data-item-logs="${item.id}" class="${buttons}" type="button">Logs</button>
      <button data-item-delete="${item.id}" class="${buttons} text-destructive" type="button">Delete</button>
      ${openHref ? itemExportFormats.map(({ format, label }) => html`<button data-item-export="${item.id}" data-item-export-format="${format}" class="${buttons}" type="button"${exportBusy ? " disabled" : ""}${exportState?.busyFormat === format ? ' aria-busy="true"' : ""}>${exportState?.busyFormat === format ? "Preparing…" : label}</button>`) : ""}
      ${exportState?.error ? html`<p class="basis-full text-xs text-destructive" role="alert">${exportState.error}</p>` : ""}
    </div>
  `;
}

export function renderItemCard(item: ItemSummary, exportState?: ItemExportActionState): TrustedHTML {
  return html`
    <article class="${card}">
      <div class="flex items-start justify-between gap-4">
        <div class="min-w-0">
          <h3 class="truncate text-base font-semibold text-foreground">${item.name || item.id}</h3>
          <p class="mt-1 truncate text-xs text-muted-foreground">${item.id}</p>
        </div>
        <span class="rounded-full bg-secondary px-2 py-0.5 text-xs text-secondary-foreground">${item.sourceType}</span>
      </div>
      <p class="mt-3 text-xs text-muted-foreground">${item.imageCount.toString()} page${item.imageCount === 1n ? "" : "s"} · ${formatDateTime(item.createdAt)}</p>
      ${renderItemActions(item, exportState)}
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
  await new Promise<void>((resolve) => {
    let sub: { close: () => void } | null = null;
    let settled = false;
    let reconciliation = Promise.resolve();
    const finish = () => {
      if (settled) return;
      settled = true;
      window.clearTimeout(timeout);
      sub?.close();
      resolve();
    };
    const reconcile = () => {
      // Queue every ready-edge reconciliation. If the stream becomes ready
      // while the initial snapshot is still in flight, the second lookup must
      // run afterward or a just-created job can remain invisible until timeout.
      reconciliation = reconciliation
        .then(async () => {
          if (settled) return;
          const jobs = await listTranscriptionJobs(BigInt(itemImageId));
          if (jobs[0]) finish();
        })
        .catch(() => undefined);
    };
    const timeout = window.setTimeout(() => {
      finish();
    }, 120000);
    // Subscribe before reading the durable snapshot. The stream's ready event
    // establishes its high-water mark; reconciling at both edges closes the
    // snapshot/subscription race without treating transient event data as state.
    sub = subscribeToEvents({
      itemImageId,
      onReady: reconcile,
      types: [
        "dev.scribe.transcription.task.started",
        "dev.scribe.transcription.completed",
        "dev.scribe.transcription.failed",
      ],
    }, finish);
    if (settled) {
      // A test transport or an already-buffered in-process transport may
      // deliver synchronously before subscribeToEvents returns its handle.
      sub.close();
      return;
    }
    reconcile();
  });
}
