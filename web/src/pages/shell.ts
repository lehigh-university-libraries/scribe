import { create } from "@bufbuild/protobuf";
import {
  createContext,
  getContextMetrics,
  listContexts,
  type ContextMetrics,
} from "../api/context";
import {
  createAPIKey,
  createProviderSecret,
  deleteAPIKey,
  deleteProviderSecret,
  getAuthMe,
  listAPIKeys,
  listProviderSecrets,
  logout,
  type APIKeyRecord,
  type AuthMeResponse,
  type ProviderSecretRecord,
} from "../api/auth";
import { subscribeToEvents } from "../api/events";
import {
  createItemFromManifest,
  deleteItem,
  listItemProviderCallAudits,
  listItems,
  type ItemProviderCallAudit,
  uploadItemImages,
} from "../api/items";
import {
  processImageUpload,
  processImageURL,
  reprocessItemImage,
} from "../api/processing";
import { listTranscriptionJobs } from "../api/transcription";
import {
  addWorkspaceMember,
  createWorkspace,
  deleteWorkspaceMember,
  listWorkspaceMembers,
  listWorkspaces,
  updateWorkspace,
  updateWorkspaceMember,
} from "../api/workspaces";
import {
  applyWorkspaceToLocation,
  getCurrentWorkspaceId,
  setCurrentWorkspaceId,
  syncWorkspaceSelectionFromLocation,
  workspaceAwarePath,
} from "../lib/workspace";
import { escHtml, uint64ToString } from "../lib/util";
import { ContextSchema, type Context } from "../proto/scribe/v1/context_pb";
import type { Item } from "../proto/scribe/v1/item_pb";
import type { Workspace, WorkspaceAccess, WorkspaceMember } from "../proto/scribe/v1/workspace_pb";

type ShellView = "library" | "contexts" | "settings";
type ShellPanel = Exclude<ShellView, "library"> | null;
type ThemeMode = "light" | "dark";
type LibraryTab = "url" | "single" | "multi" | "manifest";

interface ShellState {
  auth: AuthMeResponse | null;
  authError: string;
  panel: ShellPanel;
  search: string;
  theme: ThemeMode;
  activeLibraryTab: LibraryTab;
  workspaces: WorkspaceAccess[];
  currentWorkspaceId: string;
  items: Item[];
  contexts: Context[];
  contextMetrics: Map<string, ContextMetrics>;
  providerSecrets: ProviderSecretRecord[];
  apiKeys: APIKeyRecord[];
  members: WorkspaceMember[];
}

interface ProcessingOverlay {
  el: HTMLDivElement;
  advance: (step: number) => void;
  complete: (detail?: string) => void;
  setDetail: (detail?: string) => void;
  remove: () => void;
}

const THEME_STORAGE_KEY = "scribe.shell.theme";

const ICONS = {
  chevron: `
    <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
      <path d="M6 8l4 4 4-4"></path>
    </svg>
  `,
  plus: `
    <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
      <path d="M10 4v12"></path>
      <path d="M4 10h12"></path>
    </svg>
  `,
  search: `
    <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
      <circle cx="9" cy="9" r="5"></circle>
      <path d="M13 13l4 4"></path>
    </svg>
  `,
  sparkles: `
    <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
      <path d="M10 2l1.5 4.5L16 8l-4.5 1.5L10 14l-1.5-4.5L4 8l4.5-1.5L10 2z"></path>
      <path d="M15.5 13.5l.7 2.1 2.1.7-2.1.7-.7 2.1-.7-2.1-2.1-.7 2.1-.7.7-2.1z"></path>
    </svg>
  `,
  sun: `
    <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
      <circle cx="10" cy="10" r="3.5"></circle>
      <path d="M10 1.5v2"></path>
      <path d="M10 16.5v2"></path>
      <path d="M1.5 10h2"></path>
      <path d="M16.5 10h2"></path>
      <path d="M4 4l1.4 1.4"></path>
      <path d="M14.6 14.6L16 16"></path>
      <path d="M4 16l1.4-1.4"></path>
      <path d="M14.6 5.4L16 4"></path>
    </svg>
  `,
  moon: `
    <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
      <path d="M15.4 12.9A6.8 6.8 0 017.1 4.6 7.5 7.5 0 1015.4 12.9z"></path>
    </svg>
  `,
  user: `
    <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
      <circle cx="10" cy="6" r="3"></circle>
      <path d="M4 16c1.2-2.5 3.3-3.8 6-3.8s4.8 1.3 6 3.8"></path>
    </svg>
  `,
  logout: `
    <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
      <path d="M8 4H4.5A1.5 1.5 0 003 5.5v9A1.5 1.5 0 004.5 16H8"></path>
      <path d="M13 6l4 4-4 4"></path>
      <path d="M17 10H7"></path>
    </svg>
  `,
  close: `
    <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
      <path d="M5 5l10 10"></path>
      <path d="M15 5L5 15"></path>
    </svg>
  `,
  link: `
    <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
      <path d="M8.5 11.5l3-3"></path>
      <path d="M7 14H5.5A3.5 3.5 0 112 10.5V9a3.5 3.5 0 013.5-3.5H7"></path>
      <path d="M13 6h1.5A3.5 3.5 0 0118 9.5V11a3.5 3.5 0 01-3.5 3.5H13"></path>
    </svg>
  `,
  upload: `
    <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
      <path d="M10 13V4"></path>
      <path d="M6.5 7.5L10 4l3.5 3.5"></path>
      <path d="M3 14.5V16h14v-1.5"></path>
    </svg>
  `,
  file: `
    <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
      <path d="M6 2.5h5l4 4V17.5H6z"></path>
      <path d="M11 2.5v4h4"></path>
    </svg>
  `,
  grid: `
    <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
      <rect x="3" y="3" width="5" height="5" rx="1.2"></rect>
      <rect x="12" y="3" width="5" height="5" rx="1.2"></rect>
      <rect x="3" y="12" width="5" height="5" rx="1.2"></rect>
      <rect x="12" y="12" width="5" height="5" rx="1.2"></rect>
    </svg>
  `,
  check: `
    <svg viewBox="0 0 20 20" fill="currentColor" aria-hidden="true">
      <path fill-rule="evenodd" d="M16.704 5.29a1 1 0 010 1.415l-8 8a1 1 0 01-1.414 0l-4-4A1 1 0 114.704 9.29L8 12.586l7.296-7.296a1 1 0 011.408 0z" clip-rule="evenodd"></path>
    </svg>
  `,
} as const;

function readStoredTheme(): ThemeMode {
  try {
    return window.localStorage.getItem(THEME_STORAGE_KEY) === "dark" ? "dark" : "light";
  } catch {
    return "light";
  }
}

function applyTheme(theme: ThemeMode): void {
  document.documentElement.dataset.theme = theme;
  try {
    window.localStorage.setItem(THEME_STORAGE_KEY, theme);
  } catch {
    // Ignore storage failures in locked-down contexts.
  }
}

function buildLoginHref(auth: AuthMeResponse | null): string {
  const redirect = `${window.location.pathname}${window.location.search}`;
  const loginUrl = auth?.loginUrl || "/auth/google";
  const separator = loginUrl.includes("?") ? "&" : "?";
  return `${loginUrl}${separator}redirect=${encodeURIComponent(redirect)}`;
}

function avatarInitials(auth: AuthMeResponse | null): string {
  const source = auth?.user?.name?.trim() || auth?.user?.email?.trim() || "Scribe";
  const parts = source.split(/\s+/).filter(Boolean);
  if (parts.length === 0) return "SC";
  return parts.slice(0, 2).map((part) => part[0]?.toUpperCase() || "").join("").slice(0, 2) || "SC";
}

function createProcessingOverlay(steps: string[]): ProcessingOverlay {
  const el = document.createElement("div");
  el.className = "fixed inset-0 z-[9999] flex items-center justify-center bg-slate-950/15 px-4 backdrop-blur-sm";
  el.innerHTML = `
    <div class="w-full max-w-md rounded-[32px] border border-slate-200 bg-white px-8 py-9 text-center shadow-2xl shadow-slate-900/10">
      <div>
        <p class="text-xs font-semibold uppercase tracking-[0.34em] text-slate-400">Scribe</p>
        <p class="mt-3 text-3xl font-semibold tracking-tight text-slate-950">Preparing your annotation</p>
      </div>
      <div class="mt-6 flex justify-center">
        <div class="h-12 w-12 animate-spin rounded-full border-4 border-slate-200 border-t-blue-600"></div>
      </div>
      <ul class="mt-8 space-y-3 text-left text-sm">
        ${steps.map((label, index) => `
          <li id="proc-step-${index}" class="flex items-center gap-3 text-slate-400 transition-colors duration-300">
            <span id="proc-icon-${index}" class="flex h-5 w-5 flex-shrink-0 items-center justify-center text-xs">·</span>
            <span>${escHtml(label)}</span>
          </li>
        `).join("")}
      </ul>
      <p id="proc-detail" class="mt-6 text-xs text-slate-500"></p>
    </div>
  `;
  document.body.appendChild(el);

  let activeStep = -1;
  const applyStep = (step: number) => {
    if (step <= activeStep) return;
    for (let i = Math.max(0, activeStep); i <= step; i++) {
      const item = document.getElementById(`proc-step-${i}`);
      const icon = document.getElementById(`proc-icon-${i}`);
      if (!item || !icon) continue;
      if (i < step) {
        item.className = "flex items-center gap-3 text-slate-700 transition-colors duration-300";
        icon.innerHTML = `<span class="h-4 w-4 text-blue-600">${ICONS.check}</span>`;
      } else {
        item.className = "flex items-center gap-3 text-slate-950 transition-colors duration-300";
        icon.innerHTML = `<span class="inline-block h-3 w-3 animate-spin rounded-full border-2 border-slate-300 border-t-blue-600"></span>`;
      }
    }
    activeStep = step;
  };

  applyStep(0);
  return {
    el,
    advance: applyStep,
    complete(detail = "Opening editor…") {
      for (let i = 0; i < steps.length; i++) {
        const item = document.getElementById(`proc-step-${i}`);
        const icon = document.getElementById(`proc-icon-${i}`);
        if (!item || !icon) continue;
        item.className = "flex items-center gap-3 text-slate-700 transition-colors duration-300";
        icon.innerHTML = `<span class="h-4 w-4 text-blue-600">${ICONS.check}</span>`;
      }
      const detailEl = document.getElementById("proc-detail");
      if (detailEl) detailEl.textContent = detail;
    },
    setDetail(detail = "") {
      const detailEl = document.getElementById("proc-detail");
      if (detailEl) detailEl.textContent = detail;
    },
    remove() {
      el.remove();
    },
  };
}

async function waitForAutomaticTranscriptionStart(itemImageId: string, overlay: ProcessingOverlay): Promise<void> {
  overlay.setDetail("Preparing automatic transcription...");
  const startedAt = Date.now();
  while (Date.now() - startedAt < 120000) {
    try {
      const jobs = await listTranscriptionJobs(BigInt(itemImageId));
      const latest = jobs[0];
      if (latest) {
        const total = latest.totalSegments > 0 ? latest.totalSegments : "?";
        overlay.setDetail(`Automatic transcription progress ${latest.completedSegments}/${total}`);
        return;
      }
    } catch {
      overlay.setDetail("Preparing automatic transcription...");
    }
    await new Promise((resolve) => window.setTimeout(resolve, 500));
  }
}

function formatDate(raw: string): string {
  const date = new Date(raw);
  if (Number.isNaN(date.getTime())) return raw;
  return date.toLocaleDateString();
}

function formatDateTime(raw: string): string {
  const date = new Date(raw);
  if (Number.isNaN(date.getTime())) return raw;
  return date.toLocaleString();
}

function currentWorkspaceAccess(state: ShellState): WorkspaceAccess | undefined {
  return state.workspaces.find((entry) => uint64ToString(entry.workspace?.id ?? 0n) === state.currentWorkspaceId);
}

function currentWorkspace(state: ShellState): Workspace | undefined {
  return currentWorkspaceAccess(state)?.workspace;
}

function currentWorkspaceRole(state: ShellState): string {
  return currentWorkspaceAccess(state)?.role ?? "";
}

function canAdminWorkspace(state: ShellState): boolean {
  const role = currentWorkspaceRole(state);
  return role === "admin" || Boolean(state.auth?.user?.isAdmin);
}

function canManageWorkspaceSecrets(state: ShellState): boolean {
  const role = currentWorkspaceRole(state);
  return role === "admin" || role === "write" || Boolean(state.auth?.user?.isAdmin);
}

function canManageWorkspaceAPIKeys(state: ShellState): boolean {
  return canAdminWorkspace(state);
}

function workspaceIdString(value: string | number | bigint | null | undefined): string {
  const raw = `${value ?? ""}`.trim();
  return raw === "" || raw === "0" ? "" : raw;
}

function contextOptionMarkup(contexts: Context[]): string {
  return contexts
    .filter((context) => context.id !== 0n)
    .map((context) => `<option value="${escHtml(context.id.toString())}"${context.isDefault ? " selected" : ""}>${escHtml(context.name || `Context ${context.id}`)}</option>`)
    .join("");
}

function editorHrefForItem(item: Item): string {
  if (item.images.length === 0) return "";
  const params = new URLSearchParams();
  params.set("itemImageId", uint64ToString(item.images[0].id));
  if (item.images.length > 1 || item.sourceType === "manifest") {
    params.set("itemId", item.id);
  }
  return workspaceAwarePath(`/editor?${params.toString()}`);
}

function exportHref(itemId: string, format: "hocr" | "pagexml" | "alto" | "txt"): string {
  return workspaceAwarePath(`/v1/items/${encodeURIComponent(itemId)}/export?format=${encodeURIComponent(format)}`);
}

function renderAuditCard(audit: ItemProviderCallAudit): string {
  const pageLabel = audit.itemImageLabel?.trim()
    || (audit.itemImageSequence ? `Page ${audit.itemImageSequence}` : audit.itemImageId ? `Image ${audit.itemImageId}` : "Item");
  const status = audit.httpStatus != null ? `HTTP ${audit.httpStatus}` : audit.errorMessage?.trim() ? "Error" : "OK";
  return `
    <article class="shell-section">
      <div class="flex flex-wrap items-center gap-2 text-xs text-slate-500">
        <span class="shell-chip">${escHtml(pageLabel)}</span>
        <span>${escHtml(formatDateTime(audit.createdAt))}</span>
        <span>${escHtml(status)}</span>
      </div>
      <p class="mt-3 text-sm font-medium text-slate-950">${escHtml(audit.provider)} ${escHtml(audit.model)} <span class="text-slate-500">(${escHtml(audit.operation)})</span></p>
      ${audit.errorMessage?.trim() ? `<p class="mt-3 text-xs text-rose-600">${escHtml(audit.errorMessage)}</p>` : ""}
      <div class="mt-4 space-y-3">
        ${audit.prompt?.trim() ? `<details class="rounded-2xl border border-slate-200 bg-slate-50 p-3"><summary class="cursor-pointer text-xs font-medium text-slate-700">Prompt</summary><pre class="mt-2 overflow-x-auto whitespace-pre-wrap text-xs text-slate-500">${escHtml(audit.prompt)}</pre></details>` : ""}
        ${audit.requestJson?.trim() ? `<details class="rounded-2xl border border-slate-200 bg-slate-50 p-3"><summary class="cursor-pointer text-xs font-medium text-slate-700">Request JSON</summary><pre class="mt-2 overflow-x-auto whitespace-pre-wrap text-xs text-slate-500">${escHtml(audit.requestJson)}</pre></details>` : ""}
        ${audit.responseJson?.trim() ? `<details class="rounded-2xl border border-slate-200 bg-slate-50 p-3"><summary class="cursor-pointer text-xs font-medium text-slate-700">Response JSON</summary><pre class="mt-2 overflow-x-auto whitespace-pre-wrap text-xs text-slate-500">${escHtml(audit.responseJson)}</pre></details>` : ""}
      </div>
    </article>
  `;
}

function renderContextCard(context: Context, metrics: ContextMetrics): string {
  return `
    <article class="shell-section">
      <div class="flex items-start justify-between gap-4">
        <div>
          <h3 class="text-lg font-semibold text-slate-950">${escHtml(context.name)}</h3>
          <p class="mt-2 text-sm text-slate-500">${escHtml(context.description || "No description.")}</p>
        </div>
        ${context.isDefault ? `<span class="shell-chip shell-chip-active">Default</span>` : ""}
      </div>
      <dl class="mt-5 grid gap-3 sm:grid-cols-2">
        <div class="rounded-2xl border border-slate-200 bg-slate-50 p-3">
          <dt class="text-[11px] font-semibold uppercase tracking-[0.24em] text-slate-400">Provider</dt>
          <dd class="mt-1 text-sm text-slate-900">${escHtml(context.transcriptionProvider || "—")}</dd>
        </div>
        <div class="rounded-2xl border border-slate-200 bg-slate-50 p-3">
          <dt class="text-[11px] font-semibold uppercase tracking-[0.24em] text-slate-400">Model</dt>
          <dd class="mt-1 truncate font-mono text-xs text-slate-700">${escHtml(context.transcriptionModel || "—")}</dd>
        </div>
        <div class="rounded-2xl border border-slate-200 bg-slate-50 p-3">
          <dt class="text-[11px] font-semibold uppercase tracking-[0.24em] text-slate-400">Segmentation</dt>
          <dd class="mt-1 truncate font-mono text-xs text-slate-700">${escHtml(context.segmentationModel || "—")}</dd>
        </div>
        <div class="rounded-2xl border border-slate-200 bg-slate-50 p-3">
          <dt class="text-[11px] font-semibold uppercase tracking-[0.24em] text-slate-400">Endpoint</dt>
          <dd class="mt-1 truncate font-mono text-xs text-slate-700">${escHtml(context.transcriptionBaseUrl || "Global config default")}</dd>
        </div>
      </dl>
      <div class="mt-5 grid gap-3 sm:grid-cols-3">
        <div class="rounded-2xl border border-slate-200 bg-white p-3">
          <p class="text-[11px] font-semibold uppercase tracking-[0.24em] text-slate-400">Tracked runs</p>
          <p class="mt-2 text-2xl font-semibold text-slate-950">${metrics.total_runs}</p>
        </div>
        <div class="rounded-2xl border border-slate-200 bg-white p-3">
          <p class="text-[11px] font-semibold uppercase tracking-[0.24em] text-slate-400">Runs with edits</p>
          <p class="mt-2 text-2xl font-semibold text-slate-950">${metrics.corrected_runs}</p>
        </div>
        <div class="rounded-2xl border border-slate-200 bg-white p-3">
          <p class="text-[11px] font-semibold uppercase tracking-[0.24em] text-slate-400">Avg Levenshtein</p>
          <p class="mt-2 text-2xl font-semibold text-slate-950">${metrics.avg_levenshtein_distance.toFixed(1)}</p>
        </div>
      </div>
    </article>
  `;
}

function renderProviderSecrets(secrets: ProviderSecretRecord[]): string {
  if (secrets.length === 0) {
    return `<p class="text-sm text-slate-500">No provider keys saved yet.</p>`;
  }
  return `
    <div class="overflow-x-auto">
      <table class="min-w-full text-left text-sm">
        <thead class="text-[11px] font-semibold uppercase tracking-[0.24em] text-slate-400">
          <tr>
            <th class="px-3 py-2">Provider</th>
            <th class="px-3 py-2">Name</th>
            <th class="px-3 py-2">Scope</th>
            <th class="px-3 py-2">Key</th>
            <th class="px-3 py-2">Updated</th>
            <th class="px-3 py-2"></th>
          </tr>
        </thead>
        <tbody>
          ${secrets.map((secret) => `
            <tr class="border-t border-slate-200">
              <td class="px-3 py-3 text-slate-900">${escHtml(secret.provider)}</td>
              <td class="px-3 py-3 text-slate-900">${escHtml(secret.name)}</td>
              <td class="px-3 py-3 text-slate-600">${escHtml(secret.scope)}</td>
              <td class="px-3 py-3 font-mono text-xs text-slate-500">${secret.key_hint ? `****${escHtml(secret.key_hint)}` : "Stored"}</td>
              <td class="px-3 py-3 text-slate-500">${escHtml(formatDateTime(secret.updated_at))}</td>
              <td class="px-3 py-3 text-right">
                <button data-provider-secret-delete="${escHtml(String(secret.id))}" class="shell-button-secondary !px-3 !py-1.5 !text-xs">Delete</button>
              </td>
            </tr>
          `).join("")}
        </tbody>
      </table>
    </div>
  `;
}

function renderAPIKeys(keys: APIKeyRecord[]): string {
  if (keys.length === 0) {
    return `<p class="text-sm text-slate-500">No workspace API keys created yet.</p>`;
  }
  return `
    <div class="overflow-x-auto">
      <table class="min-w-full text-left text-sm">
        <thead class="text-[11px] font-semibold uppercase tracking-[0.24em] text-slate-400">
          <tr>
            <th class="px-3 py-2">Name</th>
            <th class="px-3 py-2">Role</th>
            <th class="px-3 py-2">Prefix</th>
            <th class="px-3 py-2">Updated</th>
            <th class="px-3 py-2"></th>
          </tr>
        </thead>
        <tbody>
          ${keys.map((key) => `
            <tr class="border-t border-slate-200">
              <td class="px-3 py-3 text-slate-900">${escHtml(key.name)}</td>
              <td class="px-3 py-3 text-slate-600">${escHtml(key.role)}</td>
              <td class="px-3 py-3 font-mono text-xs text-slate-500">${escHtml(key.key_prefix)}</td>
              <td class="px-3 py-3 text-slate-500">${escHtml(formatDateTime(key.updated_at))}</td>
              <td class="px-3 py-3 text-right">
                <button data-api-key-delete="${escHtml(String(key.id))}" class="shell-button-secondary !px-3 !py-1.5 !text-xs">Delete</button>
              </td>
            </tr>
          `).join("")}
        </tbody>
      </table>
    </div>
  `;
}

function renderRecentItemCard(item: Item): string {
  const openHref = editorHrefForItem(item);
  const pageLabel = `${item.images.length} page${item.images.length === 1 ? "" : "s"}`;
  return `
    <article class="shell-item-card">
      <div class="flex items-start justify-between gap-4">
        <div class="min-w-0">
          <p class="truncate text-base font-semibold text-slate-950">${escHtml(item.name || item.id)}</p>
          <p class="mt-1 truncate text-xs text-slate-500">${escHtml(item.id)}</p>
        </div>
        <span class="shell-chip">${escHtml(item.sourceType)}</span>
      </div>
      <div class="mt-4 flex flex-wrap items-center gap-2 text-xs text-slate-500">
        <span>${escHtml(pageLabel)}</span>
        <span>•</span>
        <span>${escHtml(formatDate(item.createdAt))}</span>
      </div>
      <div class="mt-5 flex flex-wrap items-center gap-2">
        ${openHref ? `<a href="${openHref}" class="shell-button-primary">Open editor</a>` : `<span class="shell-chip">No images</span>`}
        <button data-item-logs="${escHtml(item.id)}" class="shell-button-secondary">Logs</button>
        <button data-item-delete="${escHtml(item.id)}" class="shell-button-danger">Delete</button>
        ${openHref ? `
          <select data-item-export="${escHtml(item.id)}" class="shell-select shell-select-inline">
            <option value="">Export</option>
            <option value="hocr">hOCR</option>
            <option value="pagexml">PAGE XML</option>
            <option value="alto">ALTO XML</option>
            <option value="txt">Plain text</option>
          </select>
        ` : ""}
      </div>
    </article>
  `;
}

export async function renderShell(app: HTMLElement, initialView: ShellView): Promise<void> {
  syncWorkspaceSelectionFromLocation();

  const state: ShellState = {
    auth: null,
    authError: "",
    panel: initialView === "library" ? null : initialView,
    search: "",
    theme: readStoredTheme(),
    activeLibraryTab: "url",
    workspaces: [],
    currentWorkspaceId: getCurrentWorkspaceId(),
    items: [],
    contexts: [],
    contextMetrics: new Map(),
    providerSecrets: [],
    apiKeys: [],
    members: [],
  };

  applyTheme(state.theme);

  app.innerHTML = `
    <div class="shell-page">
      <div class="shell-layout">
        <aside id="shell-sidebar" class="shell-sidebar"></aside>
        <main class="shell-main">
          <div id="shell-topbar" class="shell-topbar"></div>
          <div id="shell-content" class="shell-main-body"></div>
        </main>
      </div>

      <div id="shell-account-fab" class="fixed bottom-4 right-4 z-30"></div>

      <div id="shell-drawer-backdrop" class="shell-drawer-backdrop hidden"></div>
      <aside id="shell-drawer" class="shell-drawer translate-x-full"></aside>

      <div id="shell-modal" class="hidden fixed inset-0 z-50 items-center justify-center bg-slate-950/30 p-4 backdrop-blur-sm">
        <div class="flex max-h-[85vh] w-full max-w-4xl flex-col rounded-[32px] border border-slate-200 bg-white shadow-2xl shadow-slate-900/10">
          <div class="flex items-center justify-between border-b border-slate-200 px-6 py-4">
            <div>
              <h2 class="text-lg font-semibold text-slate-950">Item logs</h2>
              <p id="shell-modal-title" class="text-xs text-slate-500"></p>
            </div>
            <button id="shell-modal-close" class="shell-button-secondary">Close</button>
          </div>
          <div id="shell-modal-body" class="overflow-y-auto px-6 py-5 text-sm text-slate-700"></div>
        </div>
      </div>
    </div>
  `;

  const sidebar = document.getElementById("shell-sidebar") as HTMLDivElement;
  const topbar = document.getElementById("shell-topbar") as HTMLDivElement;
  const content = document.getElementById("shell-content") as HTMLDivElement;
  const drawerBackdrop = document.getElementById("shell-drawer-backdrop") as HTMLDivElement;
  const drawer = document.getElementById("shell-drawer") as HTMLDivElement;
  const accountFab = document.getElementById("shell-account-fab") as HTMLDivElement;
  const modal = document.getElementById("shell-modal") as HTMLDivElement;
  const modalTitle = document.getElementById("shell-modal-title") as HTMLParagraphElement;
  const modalBody = document.getElementById("shell-modal-body") as HTMLDivElement;
  const modalClose = document.getElementById("shell-modal-close") as HTMLButtonElement;

  function selectedContextId(): bigint {
    const select = document.getElementById("library-context-select") as HTMLSelectElement | null;
    return select?.value ? BigInt(select.value) : 0n;
  }

  function filteredItems(): Item[] {
    const query = state.search.trim().toLowerCase();
    if (!query) return state.items;
    return state.items.filter((item) => {
      const text = `${item.name} ${item.id} ${item.sourceType}`.toLowerCase();
      return text.includes(query);
    });
  }

  async function openPanel(panel: ShellPanel): Promise<void> {
    state.panel = panel;
    if (panel) {
      await ensurePanelData(panel);
    }
    renderAll();
  }

  async function openLogsModal(itemId: string): Promise<void> {
    const item = state.items.find((entry) => entry.id === itemId);
    if (!item) return;
    modal.classList.remove("hidden");
    modal.classList.add("flex");
    modalTitle.textContent = `${item.name || item.id} (${item.id})`;
    modalBody.innerHTML = `<p class="text-slate-500">Loading logs…</p>`;
    try {
      const audits = await listItemProviderCallAudits(item.id, 100);
      modalBody.innerHTML = audits.length === 0
        ? `<p class="text-slate-500">No provider logs recorded for this item yet.</p>`
        : `<div class="space-y-3">${audits.map(renderAuditCard).join("")}</div>`;
    } catch (error) {
      modalBody.innerHTML = `<p class="text-rose-600">Failed to load logs: ${escHtml(String(error))}</p>`;
    }
  }

  async function bindSharedItemActions(): Promise<void> {
    app.querySelectorAll<HTMLButtonElement>("[data-item-logs]").forEach((button) => {
      button.addEventListener("click", async () => {
        const itemId = button.dataset.itemLogs;
        if (!itemId) return;
        await openLogsModal(itemId);
      });
    });

    app.querySelectorAll<HTMLButtonElement>("[data-item-delete]").forEach((button) => {
      button.addEventListener("click", async () => {
        const itemId = button.dataset.itemDelete;
        if (!itemId || !window.confirm(`Delete item "${itemId}"?`)) return;
        await deleteItem(itemId);
        await refreshWorkspaceScopedData();
        if (state.panel) {
          await ensurePanelData(state.panel);
        }
        renderAll();
      });
    });

    app.querySelectorAll<HTMLSelectElement>("[data-item-export]").forEach((select) => {
      select.addEventListener("change", () => {
        const format = select.value as "" | "hocr" | "pagexml" | "alto" | "txt";
        const itemId = select.dataset.itemExport;
        if (!format || !itemId) return;
        window.location.href = exportHref(itemId, format);
      });
    });
  }

  function renderTopbar() {
    const workspace = currentWorkspace(state);
    topbar.innerHTML = `
      <div class="flex flex-wrap items-start justify-between gap-4">
        <div class="min-w-0">
          <p class="text-sm font-medium text-slate-500">${escHtml(workspace?.name || state.auth?.workspace?.name || "Workspace")}</p>
          <h1 class="mt-1 text-3xl font-semibold tracking-tight text-slate-950">${state.auth?.authenticated ? "Create new annotation" : "Welcome to Scribe"}</h1>
          <p class="mt-2 max-w-2xl text-sm text-slate-500">${escHtml(
            state.auth?.authenticated
              ? "Start from an image, upload a batch, or import a IIIF manifest. Existing annotations stay on the left like a conversation list."
              : "Sign in to work with your workspace, manage contexts, and keep your documents organized."
          )}</p>
        </div>
        <div class="flex items-center gap-2">
          <button id="shell-contexts-button" class="shell-button-secondary" type="button" title="Manage contexts">
            <span class="inline-flex h-5 w-5 items-center justify-center">${ICONS.sparkles}</span>
            <span>Contexts</span>
          </button>
          <button id="shell-theme-toggle" class="shell-icon-button" type="button" title="Toggle theme">
            ${state.theme === "light" ? ICONS.moon : ICONS.sun}
          </button>
        </div>
      </div>
    `;

    document.getElementById("shell-contexts-button")?.addEventListener("click", () => {
      void openPanel("contexts");
    });

    document.getElementById("shell-theme-toggle")?.addEventListener("click", () => {
      state.theme = state.theme === "light" ? "dark" : "light";
      applyTheme(state.theme);
      renderAll();
    });
  }

  function renderSidebar() {
    const items = filteredItems();
    const loginHref = buildLoginHref(state.auth);
    sidebar.innerHTML = `
      <div class="shell-sidebar-group">
        ${state.auth?.authenticated ? `
          <div class="flex items-end gap-2">
            <div class="min-w-0 flex-1">
              <label class="shell-caption" for="sidebar-workspace-select">Workspace</label>
              <div class="relative mt-2">
                <select id="sidebar-workspace-select" class="shell-workspace-select">
                  ${state.workspaces.map((entry) => {
                    const id = uint64ToString(entry.workspace?.id ?? 0n);
                    const selected = id === state.currentWorkspaceId ? " selected" : "";
                    return `<option value="${escHtml(id)}"${selected}>${escHtml(entry.workspace?.name || "Workspace")}</option>`;
                  }).join("")}
                </select>
                <span class="pointer-events-none absolute inset-y-0 right-3 flex items-center text-slate-400">${ICONS.chevron}</span>
              </div>
              <p class="mt-2 text-xs text-slate-500">${escHtml(currentWorkspaceRole(state) || state.auth?.workspace?.role || "")}</p>
            </div>
            <button id="sidebar-create-workspace" class="shell-icon-button shell-icon-button-sm" type="button" title="Create workspace">
              ${ICONS.plus}
            </button>
          </div>
        ` : `
          <div>
            <p class="shell-caption">Workspace</p>
            <h2 class="mt-2 text-2xl font-semibold text-slate-950">Scribe</h2>
            <p class="mt-3 text-sm text-slate-500">${escHtml(state.authError || "Sign in to manage workspaces, members, contexts, and saved provider keys.")}</p>
            <a href="${escHtml(loginHref)}" class="shell-button-primary mt-4">Sign in with Google</a>
          </div>
        `}
      </div>

      <button id="sidebar-new-annotation" class="shell-button-primary w-full justify-center"${state.auth?.authenticated ? "" : " disabled"}>
        <span class="inline-flex h-5 w-5 items-center justify-center">${ICONS.plus}</span>
        <span>New annotation</span>
      </button>

      <div class="relative mt-4">
        <span class="pointer-events-none absolute inset-y-0 left-4 flex items-center text-slate-400">${ICONS.search}</span>
        <input
          id="sidebar-search"
          value="${escHtml(state.search)}"
          placeholder="Search annotations"
          class="shell-input pl-12"
          ${state.auth?.authenticated ? "" : "disabled"}
        />
      </div>

      <div class="mt-5 min-h-0 flex-1 overflow-y-auto">
        <div class="mb-3 flex items-center justify-between">
          <p class="shell-caption">Annotations</p>
          <span class="text-xs text-slate-500">${items.length}</span>
        </div>
        <div class="space-y-2">
          ${items.length === 0 ? `
            <div class="shell-flat-note">
              ${state.auth?.authenticated ? "No annotations in this workspace yet." : "Sign in to load annotations."}
            </div>
          ` : items.map((item) => {
            const openHref = editorHrefForItem(item);
            return `
              <article class="shell-item-card">
                ${openHref ? `
                  <a href="${openHref}" class="block">
                    <div class="flex items-start justify-between gap-3">
                      <div class="min-w-0">
                        <p class="truncate text-sm font-semibold text-slate-950">${escHtml(item.name || item.id)}</p>
                        <p class="mt-1 truncate text-xs text-slate-500">${escHtml(item.id)}</p>
                      </div>
                      <span class="shell-chip">${item.images.length}</span>
                    </div>
                    <div class="mt-3 flex flex-wrap items-center gap-2 text-xs text-slate-500">
                      <span>${escHtml(item.sourceType)}</span>
                      <span>•</span>
                      <span>${escHtml(formatDate(item.createdAt))}</span>
                    </div>
                  </a>
                ` : `
                  <div>
                    <p class="truncate text-sm font-semibold text-slate-950">${escHtml(item.name || item.id)}</p>
                    <p class="mt-1 truncate text-xs text-slate-500">${escHtml(item.id)}</p>
                    <div class="mt-3 text-xs text-slate-500">No images yet</div>
                  </div>
                `}
                <div class="mt-4 flex flex-wrap items-center gap-2">
                  <button data-item-logs="${escHtml(item.id)}" class="shell-button-secondary !px-3 !py-1.5 !text-xs">Logs</button>
                  <button data-item-delete="${escHtml(item.id)}" class="shell-button-danger !px-3 !py-1.5 !text-xs">Delete</button>
                </div>
              </article>
            `;
          }).join("")}
        </div>
      </div>
    `;

    const workspaceSelect = document.getElementById("sidebar-workspace-select") as HTMLSelectElement | null;
    workspaceSelect?.addEventListener("change", async () => {
      state.currentWorkspaceId = applyWorkspaceToLocation(workspaceSelect.value);
      await refreshWorkspaceScopedData();
      if (state.panel) {
        await ensurePanelData(state.panel);
      }
      renderAll();
    });

    const createWorkspaceButton = document.getElementById("sidebar-create-workspace");
    createWorkspaceButton?.addEventListener("click", async () => {
      const name = window.prompt("New workspace name");
      if (!name?.trim()) return;
      try {
        const created = await createWorkspace(name.trim());
        await refreshWorkspaces();
        state.currentWorkspaceId = applyWorkspaceToLocation(workspaceIdString(created.workspace?.id));
        await refreshWorkspaceScopedData();
        if (state.panel) {
          await ensurePanelData(state.panel);
        }
        renderAll();
      } catch (error) {
        window.alert(`Create workspace failed: ${String(error)}`);
      }
    });

    document.getElementById("sidebar-new-annotation")?.addEventListener("click", async () => {
      state.panel = null;
      state.activeLibraryTab = "url";
      renderAll();
      content.scrollIntoView({ behavior: "smooth", block: "start" });
    });

    const searchInput = document.getElementById("sidebar-search") as HTMLInputElement | null;
    searchInput?.addEventListener("input", () => {
      const start = searchInput.selectionStart ?? searchInput.value.length;
      const end = searchInput.selectionEnd ?? searchInput.value.length;
      state.search = searchInput.value;
      renderAll();
      const nextInput = document.getElementById("sidebar-search") as HTMLInputElement | null;
      nextInput?.focus();
      nextInput?.setSelectionRange(start, end);
    });
  }

  function renderLibraryContent() {
    const loginHref = buildLoginHref(state.auth);
    const recentItems = filteredItems().slice(0, 6);
    const workspace = currentWorkspace(state);

    if (!state.auth?.authenticated) {
      content.innerHTML = `
        <section class="mx-auto flex min-h-[68vh] max-w-4xl items-center">
          <div class="shell-hero-card">
            <p class="shell-caption text-center">Get Started</p>
            <h2 class="mt-4 text-5xl font-semibold tracking-tight text-slate-950">Create annotations with your workspace</h2>
            <p class="mx-auto mt-4 max-w-2xl text-base text-slate-500">Sign in to import images, manage workspace members, bring your own model keys, and open existing annotations from the left sidebar.</p>
            <div class="mt-8 flex justify-center">
              <a href="${escHtml(loginHref)}" class="shell-button-primary">Sign in with Google</a>
            </div>
          </div>
        </section>
      `;
      return;
    }

    content.innerHTML = `
      <section class="mx-auto flex w-full max-w-5xl flex-col gap-6">
        <div class="pt-6 text-center">
          <p class="shell-caption text-center">Create</p>
          <h2 class="mt-4 text-4xl font-semibold tracking-tight text-slate-950">What do you want to annotate?</h2>
          <p class="mx-auto mt-3 max-w-2xl text-sm text-slate-500">This workspace behaves like a modern docs or chat shell: your annotations live on the left, and the center stays focused on starting the next document.</p>
        </div>

        <section class="shell-composer mx-auto w-full max-w-3xl">
          <div class="flex flex-wrap justify-center gap-2">
            ${([
              ["url", "Image URL", ICONS.link],
              ["single", "Single upload", ICONS.upload],
              ["multi", "Batch upload", ICONS.grid],
              ["manifest", "IIIF manifest", ICONS.file],
            ] as Array<[LibraryTab, string, string]>).map(([tab, label, icon]) => `
              <button data-library-tab="${tab}" class="${state.activeLibraryTab === tab ? "shell-chip shell-chip-active" : "shell-chip"}" type="button">
                <span class="inline-flex h-4 w-4 items-center justify-center">${icon}</span>
                <span>${label}</span>
              </button>
            `).join("")}
          </div>

          <div class="mt-6 flex flex-wrap items-center justify-between gap-3">
            <div class="flex items-center gap-2 text-sm text-slate-500">
              <span class="shell-stat">${escHtml(workspace?.name || "Workspace")}</span>
              <span class="shell-stat">${escHtml(currentWorkspaceRole(state) || "workspace")}</span>
            </div>
            <div class="flex items-center gap-2">
              <label for="library-context-select" class="text-sm font-medium text-slate-600">Context</label>
              <select id="library-context-select" class="shell-select min-w-[220px] !rounded-full !py-2.5">
                <option value="0">Default</option>
                ${contextOptionMarkup(state.contexts)}
              </select>
            </div>
          </div>

          <div class="mt-6">
            <form id="library-form-url" class="${state.activeLibraryTab === "url" ? "space-y-4" : "hidden space-y-4"}">
              <input id="library-image-url" type="url" required placeholder="https://example.org/image.jpg" class="shell-input text-base" />
              <div class="flex flex-wrap items-center gap-3">
                <button class="shell-button-primary" type="submit">Process URL</button>
                <p id="library-url-status" class="text-sm text-slate-500"></p>
              </div>
            </form>

            <form id="library-form-single" class="${state.activeLibraryTab === "single" ? "space-y-4" : "hidden space-y-4"}">
              <input id="library-single-file" type="file" accept=".jpg,.jpeg,.png,.gif,.webp,.jp2,.jpx,.j2k,.tif,.tiff" class="shell-file-input" />
              <div class="flex flex-wrap items-center gap-3">
                <button class="shell-button-primary" type="submit">Upload and process</button>
                <p id="library-single-status" class="text-sm text-slate-500"></p>
              </div>
            </form>

            <form id="library-form-multi" class="${state.activeLibraryTab === "multi" ? "space-y-4" : "hidden space-y-4"}">
              <input id="library-multi-files" type="file" multiple accept=".jpg,.jpeg,.png,.gif,.webp,.jp2,.jpx,.j2k,.tif,.tiff" class="shell-file-input" />
              <div class="flex flex-wrap items-center gap-3">
                <button class="shell-button-primary" type="submit">Upload batch</button>
                <p id="library-multi-status" class="text-sm text-slate-500"></p>
              </div>
            </form>

            <form id="library-form-manifest" class="${state.activeLibraryTab === "manifest" ? "space-y-4" : "hidden space-y-4"}">
              <input id="library-manifest-url" type="url" required placeholder="https://example.org/manifest.json" class="shell-input text-base" />
              <fieldset class="shell-subtle-section">
                <legend class="px-2 text-[11px] font-semibold uppercase tracking-[0.24em] text-slate-400">Manifest OCR handling</legend>
                <label class="mt-2 flex items-start gap-3 text-sm text-slate-700">
                  <input type="radio" name="library-manifest-mode" value="import" checked class="mt-1" />
                  <span>
                    <span class="font-medium text-slate-950">Edit imported hOCR directly</span>
                    <span class="mt-1 block text-xs text-slate-500">Use manifest seeAlso hOCR when available and open it in the editor.</span>
                  </span>
                </label>
                <label class="mt-4 flex items-start gap-3 text-sm text-slate-700">
                  <input type="radio" name="library-manifest-mode" value="reprocess" class="mt-1" />
                  <span>
                    <span class="font-medium text-slate-950">Reprocess imported pages</span>
                    <span class="mt-1 block text-xs text-slate-500">Import the manifest, then rerun OCR on each page with the selected context.</span>
                  </span>
                </label>
              </fieldset>
              <div class="flex flex-wrap items-center gap-3">
                <button class="shell-button-primary" type="submit">Ingest manifest</button>
                <p id="library-manifest-status" class="text-sm text-slate-500"></p>
              </div>
            </form>
          </div>
        </section>

        <section class="flex flex-wrap justify-center gap-2">
          <span class="shell-stat">Items ${state.items.length}</span>
          <span class="shell-stat">Contexts ${state.contexts.length}</span>
          <span class="shell-stat">Members ${state.members.length}</span>
        </section>

        <section class="pt-2">
          <div class="flex flex-wrap items-start justify-between gap-4">
            <div>
              <p class="shell-caption">Recent</p>
              <h3 class="mt-2 text-2xl font-semibold text-slate-950">Recent annotations</h3>
            </div>
            <button id="library-refresh" class="shell-button-secondary" type="button">Refresh</button>
          </div>
          <div class="shell-divider mt-5"></div>
          <div class="mt-5">
            ${recentItems.length === 0
              ? `<div class="shell-flat-note px-5 py-10 text-center">No matching annotations yet. Use the composer above to create one.</div>`
              : `<div class="grid gap-3 lg:grid-cols-2">${recentItems.map(renderRecentItemCard).join("")}</div>`}
          </div>
        </section>
      </section>
    `;

    content.querySelectorAll<HTMLButtonElement>("[data-library-tab]").forEach((button) => {
      button.addEventListener("click", () => {
        state.activeLibraryTab = button.dataset.libraryTab as LibraryTab;
        renderAll();
      });
    });

    document.getElementById("library-refresh")?.addEventListener("click", async () => {
      await refreshWorkspaceScopedData();
      renderAll();
    });

    const urlForm = document.getElementById("library-form-url") as HTMLFormElement;
    const singleForm = document.getElementById("library-form-single") as HTMLFormElement;
    const multiForm = document.getElementById("library-form-multi") as HTMLFormElement;
    const manifestForm = document.getElementById("library-form-manifest") as HTMLFormElement;

    urlForm.addEventListener("submit", async (event) => {
      event.preventDefault();
      const imageUrl = (document.getElementById("library-image-url") as HTMLInputElement).value.trim();
      const status = document.getElementById("library-url-status") as HTMLParagraphElement;
      if (!imageUrl) return;
      const overlay = createProcessingOverlay(["Fetching image", "Detecting layout", "Building document structure", "Starting automatic transcription"]);
      const timers = [600, 1800, 3200].map((ms, index) => window.setTimeout(() => overlay.advance(index + 1), ms));
      try {
        const result = await processImageURL(imageUrl, selectedContextId());
        timers.forEach(clearTimeout);
        overlay.advance(3);
        const itemImageId = uint64ToString(result.itemImageId);
        if (itemImageId && itemImageId !== "0") {
          await waitForAutomaticTranscriptionStart(itemImageId, overlay);
          overlay.complete();
          window.location.href = workspaceAwarePath(`/editor?itemImageId=${encodeURIComponent(itemImageId)}`);
          return;
        }
        overlay.remove();
        status.textContent = "Done. Refreshing annotations…";
        await refreshWorkspaceScopedData();
        renderAll();
      } catch (error) {
        timers.forEach(clearTimeout);
        overlay.remove();
        status.textContent = `Error: ${String(error)}`;
      }
    });

    singleForm.addEventListener("submit", async (event) => {
      event.preventDefault();
      const fileInput = document.getElementById("library-single-file") as HTMLInputElement;
      const status = document.getElementById("library-single-status") as HTMLParagraphElement;
      const file = fileInput.files?.[0];
      if (!file) return;
      const overlay = createProcessingOverlay(["Uploading image", "Detecting layout", "Building document structure", "Starting automatic transcription"]);
      const timers = [800, 2200, 3600].map((ms, index) => window.setTimeout(() => overlay.advance(index + 1), ms));
      try {
        const result = await processImageUpload(file, selectedContextId());
        timers.forEach(clearTimeout);
        overlay.advance(3);
        const itemImageId = uint64ToString(result.itemImageId);
        if (itemImageId && itemImageId !== "0") {
          await waitForAutomaticTranscriptionStart(itemImageId, overlay);
          overlay.complete();
          window.location.href = workspaceAwarePath(`/editor?itemImageId=${encodeURIComponent(itemImageId)}`);
          return;
        }
        overlay.remove();
        status.textContent = "Done. Refreshing annotations…";
        await refreshWorkspaceScopedData();
        renderAll();
      } catch (error) {
        timers.forEach(clearTimeout);
        overlay.remove();
        status.textContent = `Error: ${String(error)}`;
      }
    });

    multiForm.addEventListener("submit", async (event) => {
      event.preventDefault();
      const fileInput = document.getElementById("library-multi-files") as HTMLInputElement;
      const status = document.getElementById("library-multi-status") as HTMLParagraphElement;
      const files = Array.from(fileInput.files ?? []);
      if (files.length === 0) return;
      status.textContent = `Uploading ${files.length} file(s)…`;
      try {
        await uploadItemImages(files);
        status.textContent = "Uploaded. Refreshing annotations…";
        await refreshWorkspaceScopedData();
        renderAll();
      } catch (error) {
        status.textContent = `Error: ${String(error)}`;
      }
    });

    manifestForm.addEventListener("submit", async (event) => {
      event.preventDefault();
      const status = document.getElementById("library-manifest-status") as HTMLParagraphElement;
      const manifestUrl = (document.getElementById("library-manifest-url") as HTMLInputElement).value.trim();
      const mode = (document.querySelector('input[name="library-manifest-mode"]:checked') as HTMLInputElement | null)?.value || "import";
      if (!manifestUrl) return;
      try {
        const result = await createItemFromManifest(manifestUrl);
        if (mode === "reprocess") {
          const imageIds = (result.item.images ?? []).map((image) => uint64ToString(image.id)).filter(Boolean);
          for (const itemImageId of imageIds) {
            await reprocessItemImage(itemImageId, Number(selectedContextId()));
          }
        }
        if (result.firstItemImageId) {
          const params = new URLSearchParams({ itemImageId: result.firstItemImageId, itemId: result.item.id });
          window.location.href = workspaceAwarePath(`/editor?${params.toString()}`);
          return;
        }
        status.textContent = "Manifest imported. Refreshing annotations…";
        await refreshWorkspaceScopedData();
        renderAll();
      } catch (error) {
        status.textContent = `Error: ${String(error)}`;
      }
    });
  }

  function renderContextsPanel() {
    const metricsResults = state.contexts.map((context) => state.contextMetrics.get(context.id.toString()) ?? {
      context_id: Number(context.id),
      total_runs: 0,
      corrected_runs: 0,
      avg_levenshtein_distance: 0,
      avg_edit_count: 0,
      avg_box_change_score: 0,
    });
    const totalRuns = metricsResults.reduce((sum, metrics) => sum + metrics.total_runs, 0);
    const contextsWithEdits = metricsResults.filter((metrics) => metrics.corrected_runs > 0).length;
    const loginHref = buildLoginHref(state.auth);

    drawer.innerHTML = `
      <div class="flex h-full flex-col">
        <div class="mb-5 flex items-start justify-between gap-4">
          <div>
            <p class="shell-caption">Contexts</p>
            <h2 class="mt-2 text-3xl font-semibold tracking-tight text-slate-950">OCR context library</h2>
            <p class="mt-3 max-w-xl text-sm text-slate-500">Contexts are workspace-scoped profiles for OCR segmentation, transcription, and model endpoint routing.</p>
          </div>
          <button id="shell-panel-close" class="shell-icon-button" type="button" title="Close">
            ${ICONS.close}
          </button>
        </div>

        <div class="shell-drawer-body">
          ${state.auth?.authenticated ? `
            <section class="grid gap-3 sm:grid-cols-3">
              <div class="shell-section">
                <p class="shell-caption">Contexts</p>
                <p class="mt-3 text-3xl font-semibold text-slate-950">${state.contexts.length}</p>
              </div>
              <div class="shell-section">
                <p class="shell-caption">Runs tracked</p>
                <p class="mt-3 text-3xl font-semibold text-slate-950">${totalRuns}</p>
              </div>
              <div class="shell-section">
                <p class="shell-caption">With edits</p>
                <p class="mt-3 text-3xl font-semibold text-slate-950">${contextsWithEdits}</p>
              </div>
            </section>

            <section class="shell-section mt-4">
              <div class="mb-5 flex items-center justify-between gap-4">
                <div>
                  <p class="shell-caption">Create</p>
                  <h3 class="mt-2 text-2xl font-semibold text-slate-950">New context</h3>
                </div>
                <button id="contexts-refresh" class="shell-button-secondary" type="button">Refresh</button>
              </div>
              <form id="contexts-create-form" class="grid gap-4 sm:grid-cols-2">
                <input id="contexts-name" placeholder="Name" class="shell-input" />
                <input id="contexts-provider" value="ollama" placeholder="Provider" class="shell-input" />
                <textarea id="contexts-description" rows="2" placeholder="Description" class="shell-textarea sm:col-span-2"></textarea>
                <input id="contexts-model" placeholder="Transcription model" class="shell-input" />
                <input id="contexts-segmentation" value="tesseract" placeholder="Segmentation model" class="shell-input" />
                <input id="contexts-base-url" placeholder="https://service.run.app" class="shell-input font-mono text-xs sm:col-span-2" />
                <input id="contexts-audience" placeholder="Optional Cloud Run audience" class="shell-input font-mono text-xs sm:col-span-2" />
                <textarea id="contexts-system-prompt" rows="4" placeholder="System prompt" class="shell-textarea sm:col-span-2"></textarea>
                <label class="inline-flex items-center gap-2 text-sm text-slate-600">
                  <input id="contexts-default" type="checkbox" />
                  <span>Set as default system context</span>
                </label>
                <div class="flex items-center gap-3 sm:col-span-2">
                  <button class="shell-button-primary" type="submit">Create context</button>
                  <p id="contexts-status" class="text-sm text-slate-500"></p>
                </div>
              </form>
            </section>

            <section class="mt-4 space-y-3">
              ${state.contexts.length === 0
                ? `<div class="shell-section text-sm text-slate-500">No contexts in this workspace yet.</div>`
                : state.contexts.map((context, index) => renderContextCard(context, metricsResults[index])).join("")}
            </section>
          ` : `
            <section class="shell-hero-card !mx-0 !max-w-none">
              <h3 class="text-2xl font-semibold text-slate-950">Sign in to manage contexts</h3>
              <p class="mx-auto mt-3 max-w-xl text-sm text-slate-500">Contexts let you point a workspace at different OCR providers, model IDs, and dedicated Cloud Run endpoints.</p>
              <a href="${escHtml(loginHref)}" class="shell-button-primary mt-6">Sign in with Google</a>
            </section>
          `}
        </div>
      </div>
    `;

    document.getElementById("shell-panel-close")?.addEventListener("click", () => {
      void openPanel(null);
    });

    if (!state.auth?.authenticated) {
      return;
    }

    document.getElementById("contexts-refresh")?.addEventListener("click", async () => {
      await refreshWorkspaceScopedData();
      await refreshContextMetrics();
      renderAll();
    });

    document.getElementById("contexts-create-form")?.addEventListener("submit", async (event) => {
      event.preventDefault();
      const status = document.getElementById("contexts-status") as HTMLParagraphElement;
      const name = (document.getElementById("contexts-name") as HTMLInputElement).value.trim();
      if (!name) {
        status.textContent = "Name is required.";
        return;
      }
      status.textContent = "Creating context…";
      try {
        await createContext(create(ContextSchema, {
          name,
          description: (document.getElementById("contexts-description") as HTMLTextAreaElement).value.trim(),
          isDefault: (document.getElementById("contexts-default") as HTMLInputElement).checked,
          segmentationModel: (document.getElementById("contexts-segmentation") as HTMLInputElement).value.trim() || "tesseract",
          transcriptionProvider: (document.getElementById("contexts-provider") as HTMLInputElement).value.trim() || "ollama",
          transcriptionModel: (document.getElementById("contexts-model") as HTMLInputElement).value.trim(),
          transcriptionBaseUrl: (document.getElementById("contexts-base-url") as HTMLInputElement).value.trim(),
          transcriptionAudience: (document.getElementById("contexts-audience") as HTMLInputElement).value.trim(),
          temperature: -1,
          systemPrompt: (document.getElementById("contexts-system-prompt") as HTMLTextAreaElement).value.trim(),
        }));
        await refreshWorkspaceScopedData();
        await refreshContextMetrics();
        renderAll();
      } catch (error) {
        status.textContent = `Create failed: ${String(error)}`;
      }
    });
  }

  function renderSettingsPanel() {
    const workspace = currentWorkspace(state);
    const workspaceAccess = currentWorkspaceAccess(state);
    const admin = canAdminWorkspace(state);
    const canManageSecrets = canManageWorkspaceSecrets(state);
    const canManageAPIKeys = canManageWorkspaceAPIKeys(state);
    const loginHref = buildLoginHref(state.auth);

    drawer.innerHTML = `
      <div class="flex h-full flex-col">
        <div class="mb-5 flex items-start justify-between gap-4">
          <div>
            <p class="shell-caption">Account</p>
            <h2 class="mt-2 text-3xl font-semibold tracking-tight text-slate-950">Workspace and account settings</h2>
            <p class="mt-3 max-w-xl text-sm text-slate-500">Manage members, workspace metadata, provider keys, and API tokens from one place.</p>
          </div>
          <button id="shell-panel-close" class="shell-icon-button" type="button" title="Close">
            ${ICONS.close}
          </button>
        </div>

        <div class="shell-drawer-body">
          ${state.auth?.authenticated ? `
            <section class="shell-section">
              <div class="flex flex-wrap items-center justify-between gap-4">
                <div class="flex items-center gap-4">
                  <span class="shell-account-avatar !h-14 !w-14 !text-base">${escHtml(avatarInitials(state.auth))}</span>
                  <div>
                    <h3 class="text-xl font-semibold text-slate-950">${escHtml(state.auth.user?.name || state.auth.user?.email || "Account")}</h3>
                    <p class="mt-1 text-sm text-slate-500">${escHtml(state.auth.user?.email || "")}</p>
                  </div>
                </div>
                <button id="settings-logout" class="shell-button-secondary" type="button">
                  <span class="inline-flex h-5 w-5 items-center justify-center">${ICONS.logout}</span>
                  <span>Logout</span>
                </button>
              </div>
              <dl class="mt-5 grid gap-3 sm:grid-cols-3">
                <div class="rounded-2xl border border-slate-200 bg-slate-50 p-4">
                  <dt class="shell-caption">Default workspace</dt>
                  <dd class="mt-2 text-sm text-slate-950">${escHtml(state.auth.workspace?.name || "")}</dd>
                </div>
                <div class="rounded-2xl border border-slate-200 bg-slate-50 p-4">
                  <dt class="shell-caption">Workspace role</dt>
                  <dd class="mt-2 text-sm text-slate-950">${escHtml(workspaceAccess?.role || "")}</dd>
                </div>
                <div class="rounded-2xl border border-slate-200 bg-slate-50 p-4">
                  <dt class="shell-caption">Admin</dt>
                  <dd class="mt-2 text-sm text-slate-950">${state.auth.user?.isAdmin ? "Yes" : "No"}</dd>
                </div>
              </dl>
            </section>

            <section class="shell-section mt-4">
              <p class="shell-caption">Workspace</p>
              <h3 class="mt-2 text-2xl font-semibold text-slate-950">${escHtml(workspace?.name || "Workspace")}</h3>
              <p class="mt-3 text-sm text-slate-500">${workspace?.isPersonal ? "Personal workspaces are created automatically for each user." : "Rename this workspace or create another one."}</p>
              <form id="settings-rename-workspace" class="mt-5 flex flex-col gap-3 sm:flex-row">
                <input id="settings-workspace-name" value="${escHtml(workspace?.name || "")}" class="shell-input flex-1" ${admin && !workspace?.isPersonal ? "" : "disabled"} />
                <button class="shell-button-primary" type="submit"${admin && !workspace?.isPersonal ? "" : " disabled"}>Rename</button>
              </form>
              <p id="settings-workspace-status" class="mt-3 text-sm text-slate-500"></p>
              <form id="settings-create-workspace" class="mt-6 flex flex-col gap-3 sm:flex-row">
                <input id="settings-new-workspace-name" placeholder="Create a new workspace" class="shell-input flex-1" />
                <button class="shell-button-secondary" type="submit">Create workspace</button>
              </form>
            </section>

            <section class="shell-section mt-4">
              <div class="flex flex-wrap items-start justify-between gap-4">
                <div>
                  <p class="shell-caption">Members</p>
                  <h3 class="mt-2 text-2xl font-semibold text-slate-950">Workspace members</h3>
                </div>
                <span class="shell-chip">${escHtml(workspaceAccess?.role || "")}</span>
              </div>
              <div class="mt-5 overflow-x-auto">
                ${state.members.length === 0 ? `<p class="text-sm text-slate-500">No members found.</p>` : `
                  <table class="min-w-full text-left text-sm">
                    <thead class="text-[11px] font-semibold uppercase tracking-[0.24em] text-slate-400">
                      <tr>
                        <th class="px-3 py-2">User</th>
                        <th class="px-3 py-2">Role</th>
                        <th class="px-3 py-2">Added</th>
                        <th class="px-3 py-2"></th>
                      </tr>
                    </thead>
                    <tbody>
                      ${state.members.map((member) => `
                        <tr class="border-t border-slate-200">
                          <td class="px-3 py-3">
                            <p class="text-slate-900">${escHtml(member.user?.name || member.user?.email || `User ${member.user?.id || ""}`)}</p>
                            <p class="mt-1 text-xs text-slate-500">${escHtml(member.user?.email || "")}</p>
                          </td>
                          <td class="px-3 py-3">
                            <select data-member-role="${escHtml(uint64ToString(member.user?.id ?? 0n))}" class="shell-select !w-[140px] !py-2 !text-xs"${admin && !workspace?.isPersonal ? "" : " disabled"}>
                              ${["admin", "write", "create", "read"].map((role) => `<option value="${role}"${member.role === role ? " selected" : ""}>${role}</option>`).join("")}
                            </select>
                          </td>
                          <td class="px-3 py-3 text-slate-500">${escHtml(formatDateTime(member.createdAt))}</td>
                          <td class="px-3 py-3 text-right">
                            <div class="flex justify-end gap-2">
                              <button data-member-save="${escHtml(uint64ToString(member.user?.id ?? 0n))}" class="shell-button-secondary !px-3 !py-1.5 !text-xs"${admin && !workspace?.isPersonal ? "" : " disabled"}>Save</button>
                              <button data-member-remove="${escHtml(uint64ToString(member.user?.id ?? 0n))}" class="shell-button-danger !px-3 !py-1.5 !text-xs"${admin && !workspace?.isPersonal ? "" : " disabled"}>Remove</button>
                            </div>
                          </td>
                        </tr>
                      `).join("")}
                    </tbody>
                  </table>
                `}
              </div>
              <form id="settings-add-member" class="mt-6 grid gap-3 sm:grid-cols-[1.2fr_0.5fr_auto]">
                <input id="settings-member-email" placeholder="user@example.edu" class="shell-input"${admin && !workspace?.isPersonal ? "" : " disabled"} />
                <select id="settings-member-role" class="shell-select"${admin && !workspace?.isPersonal ? "" : " disabled"}>
                  <option value="read">read</option>
                  <option value="create">create</option>
                  <option value="write">write</option>
                  <option value="admin">admin</option>
                </select>
                <button class="shell-button-primary" type="submit"${admin && !workspace?.isPersonal ? "" : " disabled"}>Add member</button>
              </form>
              <p id="settings-members-status" class="mt-3 text-sm text-slate-500"></p>
            </section>

            <section class="shell-section mt-4">
              <p class="shell-caption">Bring Your Own Keys</p>
              <h3 class="mt-2 text-2xl font-semibold text-slate-950">Provider secrets</h3>
              <p class="mt-3 text-sm text-slate-500">Save personal or workspace-scoped provider keys. These are stored server-side and referenced by the app when needed.</p>
              <form id="settings-provider-secret-form" class="mt-5 grid gap-3">
                <div class="grid gap-3 sm:grid-cols-3">
                  <select id="settings-provider-secret-provider" class="shell-select"${canManageSecrets ? "" : " disabled"}>
                    <option value="gemini">gemini</option>
                  </select>
                  <input id="settings-provider-secret-name" placeholder="Name" class="shell-input"${canManageSecrets ? "" : " disabled"} />
                  <select id="settings-provider-secret-scope" class="shell-select"${canManageSecrets ? "" : " disabled"}>
                    <option value="user">Personal</option>
                    <option value="workspace">Workspace</option>
                  </select>
                </div>
                <input id="settings-provider-secret-api-key" type="password" placeholder="API key" class="shell-input"${canManageSecrets ? "" : " disabled"} />
                <div class="flex flex-wrap items-center gap-3">
                  <button class="shell-button-primary" type="submit"${canManageSecrets ? "" : " disabled"}>Save provider key</button>
                  <p id="settings-provider-secret-status" class="text-sm text-slate-500"></p>
                </div>
              </form>
              ${canManageSecrets ? "" : `<p class="mt-3 text-sm text-slate-500">Editing provider secrets requires a workspace write role.</p>`}
              <div class="mt-5">${renderProviderSecrets(state.providerSecrets)}</div>
            </section>

            <section class="shell-section mt-4">
              <div class="flex items-end justify-between gap-4">
                <div>
                  <p class="shell-caption">API Keys</p>
                  <h3 class="mt-2 text-2xl font-semibold text-slate-950">Workspace tokens</h3>
                </div>
              </div>
              ${canManageAPIKeys ? `
                <form id="settings-api-key-form" class="mt-5 grid gap-3 sm:grid-cols-[1fr_0.5fr_auto]">
                  <input id="settings-api-key-name" placeholder="Token name" class="shell-input" />
                  <select id="settings-api-key-role" class="shell-select">
                    <option value="read">read</option>
                    <option value="create">create</option>
                    <option value="write">write</option>
                    <option value="admin">admin</option>
                  </select>
                  <button class="shell-button-primary" type="submit">Create key</button>
                </form>
              ` : `
                <p class="mt-5 rounded-[24px] border border-dashed border-slate-200 px-4 py-5 text-sm text-slate-500">Workspace tokens are limited to workspace admins.</p>
              `}
              <p id="settings-api-key-status" class="mt-3 text-sm text-slate-500"></p>
              <div class="mt-5">${canManageAPIKeys ? renderAPIKeys(state.apiKeys) : ""}</div>
            </section>
          ` : `
            <section class="shell-hero-card !mx-0 !max-w-none">
              <h3 class="text-2xl font-semibold text-slate-950">Sign in to manage your workspace</h3>
              <p class="mx-auto mt-3 max-w-xl text-sm text-slate-500">Workspace members, organization settings, provider keys, and account-level controls live here.</p>
              <a href="${escHtml(loginHref)}" class="shell-button-primary mt-6">Sign in with Google</a>
            </section>
          `}
        </div>
      </div>
    `;

    document.getElementById("shell-panel-close")?.addEventListener("click", () => {
      void openPanel(null);
    });

    if (!state.auth?.authenticated) {
      return;
    }

    const workspaceStatus = document.getElementById("settings-workspace-status") as HTMLParagraphElement;
    const membersStatus = document.getElementById("settings-members-status") as HTMLParagraphElement;
    const apiKeyStatus = document.getElementById("settings-api-key-status") as HTMLParagraphElement;
    const providerSecretStatus = document.getElementById("settings-provider-secret-status") as HTMLParagraphElement;

    document.getElementById("settings-logout")?.addEventListener("click", async () => {
      await logout();
      window.location.href = "/";
    });

    document.getElementById("settings-rename-workspace")?.addEventListener("submit", async (event) => {
      event.preventDefault();
      if (!workspace) return;
      const name = (document.getElementById("settings-workspace-name") as HTMLInputElement).value.trim();
      if (!name) return;
      workspaceStatus.textContent = "Renaming workspace…";
      try {
        await updateWorkspace(workspace.id, name);
        await refreshWorkspaces();
        renderAll();
      } catch (error) {
        workspaceStatus.textContent = `Rename failed: ${String(error)}`;
      }
    });

    document.getElementById("settings-create-workspace")?.addEventListener("submit", async (event) => {
      event.preventDefault();
      const input = document.getElementById("settings-new-workspace-name") as HTMLInputElement;
      const name = input.value.trim();
      if (!name) return;
      workspaceStatus.textContent = "Creating workspace…";
      try {
        const created = await createWorkspace(name);
        await refreshWorkspaces();
        state.currentWorkspaceId = applyWorkspaceToLocation(workspaceIdString(created.workspace?.id));
        await refreshWorkspaceScopedData();
        await refreshSettingsData();
        renderAll();
      } catch (error) {
        workspaceStatus.textContent = `Create failed: ${String(error)}`;
      }
    });

    document.getElementById("settings-add-member")?.addEventListener("submit", async (event) => {
      event.preventDefault();
      if (!workspace) return;
      const email = (document.getElementById("settings-member-email") as HTMLInputElement).value.trim();
      const role = (document.getElementById("settings-member-role") as HTMLSelectElement).value;
      if (!email) return;
      membersStatus.textContent = "Adding member…";
      try {
        await addWorkspaceMember(workspace.id, email, role);
        await refreshMembers();
        renderAll();
      } catch (error) {
        membersStatus.textContent = `Add failed: ${String(error)}`;
      }
    });

    drawer.querySelectorAll<HTMLButtonElement>("[data-member-save]").forEach((button) => {
      button.addEventListener("click", async () => {
        if (!workspace) return;
        const userId = button.dataset.memberSave;
        if (!userId) return;
        const select = drawer.querySelector<HTMLSelectElement>(`[data-member-role="${userId}"]`);
        if (!select) return;
        membersStatus.textContent = "Updating member role…";
        try {
          await updateWorkspaceMember(workspace.id, userId, select.value);
          await refreshMembers();
          renderAll();
        } catch (error) {
          membersStatus.textContent = `Update failed: ${String(error)}`;
        }
      });
    });

    drawer.querySelectorAll<HTMLButtonElement>("[data-member-remove]").forEach((button) => {
      button.addEventListener("click", async () => {
        if (!workspace) return;
        const userId = button.dataset.memberRemove;
        if (!userId || !window.confirm("Remove this workspace member?")) return;
        membersStatus.textContent = "Removing member…";
        try {
          await deleteWorkspaceMember(workspace.id, userId);
          await refreshMembers();
          renderAll();
        } catch (error) {
          membersStatus.textContent = `Remove failed: ${String(error)}`;
        }
      });
    });

    document.getElementById("settings-api-key-form")?.addEventListener("submit", async (event) => {
      event.preventDefault();
      apiKeyStatus.textContent = "Creating API key…";
      try {
        const created = await createAPIKey({
          name: (document.getElementById("settings-api-key-name") as HTMLInputElement).value.trim(),
          role: (document.getElementById("settings-api-key-role") as HTMLSelectElement).value,
        });
        window.alert(`Copy this API key now. It will not be shown again.\n\n${created.key}`);
        await refreshSettingsData();
        renderAll();
      } catch (error) {
        apiKeyStatus.textContent = `Create failed: ${String(error)}`;
      }
    });

    drawer.querySelectorAll<HTMLButtonElement>("[data-api-key-delete]").forEach((button) => {
      button.addEventListener("click", async () => {
        const keyId = button.dataset.apiKeyDelete;
        if (!keyId || !window.confirm("Delete this API key?")) return;
        apiKeyStatus.textContent = "Deleting API key…";
        try {
          await deleteAPIKey(keyId);
          await refreshSettingsData();
          renderAll();
        } catch (error) {
          apiKeyStatus.textContent = `Delete failed: ${String(error)}`;
        }
      });
    });

    document.getElementById("settings-provider-secret-form")?.addEventListener("submit", async (event) => {
      event.preventDefault();
      providerSecretStatus.textContent = "Saving provider key…";
      try {
        await createProviderSecret({
          provider: (document.getElementById("settings-provider-secret-provider") as HTMLSelectElement).value,
          name: (document.getElementById("settings-provider-secret-name") as HTMLInputElement).value.trim(),
          apiKey: (document.getElementById("settings-provider-secret-api-key") as HTMLInputElement).value.trim(),
          scope: (document.getElementById("settings-provider-secret-scope") as HTMLSelectElement).value as "user" | "workspace",
        });
        await refreshSettingsData();
        renderAll();
      } catch (error) {
        providerSecretStatus.textContent = `Save failed: ${String(error)}`;
      }
    });

    drawer.querySelectorAll<HTMLButtonElement>("[data-provider-secret-delete]").forEach((button) => {
      button.addEventListener("click", async () => {
        const secretId = button.dataset.providerSecretDelete;
        if (!secretId || !window.confirm("Delete this provider secret?")) return;
        providerSecretStatus.textContent = "Deleting provider key…";
        try {
          await deleteProviderSecret(secretId);
          await refreshSettingsData();
          renderAll();
        } catch (error) {
          providerSecretStatus.textContent = `Delete failed: ${String(error)}`;
        }
      });
    });
  }

  function renderDrawer() {
    if (!state.panel) {
      drawerBackdrop.classList.add("hidden");
      drawer.classList.add("translate-x-full");
      drawer.innerHTML = "";
      return;
    }

    drawerBackdrop.classList.remove("hidden");
    drawer.classList.remove("translate-x-full");

    if (state.panel === "contexts") {
      renderContextsPanel();
      return;
    }
    renderSettingsPanel();
  }

  function renderAccountFab() {
    const loginHref = buildLoginHref(state.auth);
    if (state.auth?.authenticated && state.auth.user) {
      accountFab.innerHTML = `
        <button id="shell-account-button" class="shell-account-fab" type="button">
          <span class="shell-account-avatar">${escHtml(avatarInitials(state.auth))}</span>
          <span class="hidden text-left sm:block">
            <span class="block text-sm font-medium text-slate-950">${escHtml(state.auth.user.name || state.auth.user.email || "Account")}</span>
            <span class="block text-xs text-slate-500">${escHtml(currentWorkspaceRole(state) || "Settings")}</span>
          </span>
        </button>
      `;
      document.getElementById("shell-account-button")?.addEventListener("click", () => {
        void openPanel("settings");
      });
      return;
    }

    accountFab.innerHTML = `
      <a href="${escHtml(loginHref)}" class="shell-account-fab">
        <span class="shell-account-avatar">${ICONS.user}</span>
        <span class="hidden text-left sm:block">
          <span class="block text-sm font-medium text-slate-950">Sign in</span>
          <span class="block text-xs text-slate-500">Workspace access</span>
        </span>
      </a>
    `;
  }

  function renderAll() {
    renderTopbar();
    renderSidebar();
    renderLibraryContent();
    renderDrawer();
    renderAccountFab();
    void bindSharedItemActions();
  }

  async function refreshAuth() {
    try {
      state.auth = await getAuthMe();
      state.authError = "";
      if (state.auth.authenticated) {
        const desired = state.currentWorkspaceId || `${state.auth.user?.defaultWorkspaceId ?? state.auth.workspace?.id ?? ""}`;
        if (desired) {
          state.currentWorkspaceId = setCurrentWorkspaceId(desired);
        }
      }
    } catch (error) {
      state.auth = null;
      state.authError = String(error);
    }
  }

  async function refreshWorkspaces() {
    if (!state.auth?.authenticated) {
      state.workspaces = [];
      return;
    }
    state.workspaces = await listWorkspaces();
    const availableIds = new Set(state.workspaces.map((entry) => uint64ToString(entry.workspace?.id ?? 0n)));
    if (!availableIds.has(state.currentWorkspaceId)) {
      const fallback = workspaceIdString(state.workspaces[0]?.workspace?.id);
      state.currentWorkspaceId = applyWorkspaceToLocation(fallback);
    }
  }

  async function refreshMembers() {
    const workspaceId = state.currentWorkspaceId;
    if (!workspaceId || !state.auth?.authenticated) {
      state.members = [];
      return;
    }
    state.members = (await listWorkspaceMembers(workspaceId)).members;
  }

  async function refreshContextMetrics() {
    const metrics = await Promise.all(state.contexts.map(async (context) => {
      try {
        return await getContextMetrics(context.id.toString());
      } catch {
        return {
          context_id: Number(context.id),
          total_runs: 0,
          corrected_runs: 0,
          avg_levenshtein_distance: 0,
          avg_edit_count: 0,
          avg_box_change_score: 0,
        } satisfies ContextMetrics;
      }
    }));
    state.contextMetrics = new Map(metrics.map((metric) => [`${metric.context_id}`, metric]));
  }

  async function refreshWorkspaceScopedData() {
    if (!state.auth?.authenticated) {
      state.items = [];
      state.contexts = [];
      state.members = [];
      state.providerSecrets = [];
      state.apiKeys = [];
      return;
    }
    const [items, contexts] = await Promise.all([
      listItems(),
      listContexts(),
    ]);
    state.items = [...items].sort((a, b) => new Date(b.createdAt).getTime() - new Date(a.createdAt).getTime());
    state.contexts = [...contexts].sort((a, b) => {
      if (a.isDefault !== b.isDefault) {
        return a.isDefault ? -1 : 1;
      }
      return (a.name || "").localeCompare(b.name || "");
    });
    await refreshMembers();
  }

  async function refreshSettingsData() {
    if (!state.auth?.authenticated) {
      state.providerSecrets = [];
      state.apiKeys = [];
      return;
    }
    const [providerSecrets, apiKeys] = await Promise.all([
      listProviderSecrets().catch(() => [] as ProviderSecretRecord[]),
      canManageWorkspaceAPIKeys(state) ? listAPIKeys().catch(() => [] as APIKeyRecord[]) : Promise.resolve([] as APIKeyRecord[]),
    ]);
    state.providerSecrets = providerSecrets;
    state.apiKeys = apiKeys;
  }

  async function ensurePanelData(panel: ShellPanel) {
    if (panel === "contexts") {
      await refreshContextMetrics();
      return;
    }
    if (panel === "settings") {
      await refreshSettingsData();
    }
  }

  modalClose.addEventListener("click", () => {
    modal.classList.add("hidden");
    modal.classList.remove("flex");
    modalTitle.textContent = "";
    modalBody.innerHTML = "";
  });

  modal.addEventListener("click", (event) => {
    if (event.target === modal) {
      modal.classList.add("hidden");
      modal.classList.remove("flex");
      modalTitle.textContent = "";
      modalBody.innerHTML = "";
    }
  });

  drawerBackdrop.addEventListener("click", () => {
    void openPanel(null);
  });

  await refreshAuth();
  await refreshWorkspaces();
  await refreshWorkspaceScopedData();
  if (state.panel) {
    await ensurePanelData(state.panel);
  }
  renderAll();

  const subscription = state.auth?.authenticated
    ? subscribeToEvents(
      {
        types: [
          "dev.scribe.transcription.task.completed",
          "dev.scribe.transcription.completed",
          "dev.scribe.transcription.failed",
          "dev.scribe.annotations.created",
          "dev.scribe.annotations.published",
        ],
      },
      () => {
        void (async () => {
          await refreshWorkspaceScopedData();
          if (state.panel) {
            await ensurePanelData(state.panel);
          }
          renderAll();
        })();
      },
    )
    : null;

  window.addEventListener("beforeunload", () => {
    subscription?.close();
  }, { once: true });
}
