import { create } from "@bufbuild/protobuf";
import { getAnnotationPage } from "../api/annotations";
import { createAPIKey, createProviderSecret, deleteAPIKey, deleteProviderSecret, getAuthMe, listAPIKeys, listProviderSecrets, logout, type APIKeyRecord, type GetAuthMeResponse, type ProviderSecretRecord } from "../api/auth";
import { createContext, getContextMetrics, getModelCatalog, listContexts, type ContextMetrics } from "../api/context";
import { subscribeToEvents } from "../api/events";
import { deleteItem, getItemExportSnapshot, importManifest, listItemProviderCallAudits, listItems, prepareItemExport, uploadItemImages } from "../api/items";
import { processImageURL, reprocessItemImage } from "../api/processing";
import { addWorkspaceMember, createWorkspace, deleteWorkspaceMember, listWorkspaceMembers, listWorkspaces, updateWorkspace, updateWorkspaceMember } from "../api/workspaces";
import { applyWorkspaceToLocation, getCurrentWorkspaceId, setCurrentWorkspaceId, syncWorkspaceSelectionFromLocation, workspaceAwarePath } from "../lib/workspace";
import { mapConcurrent } from "../lib/async";
import { html, setHTML, uint64ToString } from "../lib/util";
import type { AnnotationExportFormat } from "../proto/scribe/v1/annotation_pb";
import { ContextSchema, type Context, type GetModelCatalogResponse } from "../proto/scribe/v1/context_pb";
import type { ItemSummary } from "../proto/scribe/v1/item_pb";
import type { WorkspaceAccess, WorkspaceMember } from "../proto/scribe/v1/workspace_pb";
import { avatar, buttons, canAdminWorkspace, canWriteWorkspace, card, contextOptions, currentWorkspace, currentWorkspaceRole, editorHrefForItem, formatDateTime, input, itemExportFormats, loginHref, primary, renderAPIKeys, renderItemActions, renderItemCard, renderProviderSecrets, waitForAutomaticTranscriptionStart, workspaceIdString, type ItemExportActionState } from "./shell_helpers";

type ShellView = "library" | "contexts" | "settings";
type ShellPanel = Exclude<ShellView, "library"> | null;
type LibraryTab = "url" | "single" | "multi" | "manifest";
const itemPageSize = 50;
interface ShellState {
  auth: GetAuthMeResponse | null;
  authError: string;
  dataError: string;
  panel: ShellPanel;
  search: string;
  activeLibraryTab: LibraryTab;
  workspaces: WorkspaceAccess[];
  currentWorkspaceId: string;
  items: ItemSummary[];
  itemsWorkspaceId: string;
  itemsQuery: string;
  itemsNextPageToken: string;
  itemsLoadingMore: boolean;
  contexts: Context[];
  modelCatalog: GetModelCatalogResponse | null;
  contextMetrics: Map<string, ContextMetrics>;
  providerSecrets: ProviderSecretRecord[];
  apiKeys: APIKeyRecord[];
  members: WorkspaceMember[];
  itemExports: Map<string, ItemExportActionState>;
}
export async function renderShell(app: HTMLElement, initialView: ShellView): Promise<void> {
  syncWorkspaceSelectionFromLocation();
  const state: ShellState = {
    auth: null,
    authError: "",
    dataError: "",
    panel: initialView === "library" ? null : initialView,
    search: "",
    activeLibraryTab: "url",
    workspaces: [],
    currentWorkspaceId: getCurrentWorkspaceId(),
    items: [],
    itemsWorkspaceId: "",
    itemsQuery: "",
    itemsNextPageToken: "",
    itemsLoadingMore: false,
    contexts: [],
    modelCatalog: null,
    contextMetrics: new Map(),
    providerSecrets: [],
    apiKeys: [],
    members: [],
    itemExports: new Map(),
  };
  let itemListGeneration = 0;
  let itemSearchTimer: number | undefined;
  let itemListAbortController: AbortController | undefined;
  // This controller must outlive a render pass. Event-driven library refreshes
  // replace the form DOM while an upload is active; keeping it inside
  // bindLibraryActions would make the newly rendered Cancel button a no-op.
  let activeMultiUpload: AbortController | undefined;
  let drawerReturnFocus: HTMLElement | null = null;
  let modalReturnFocus: HTMLElement | null = null;
  let modalLoadGeneration = 0;
  let contextMetricsGeneration = 0;
  let settingsDataGeneration = 0;
  let workspaceEventRefreshRunning = false;
  let workspaceEventRefreshPending = false;
  let disposed = false;

  setHTML(app, html`
    <div class="min-h-screen text-foreground">
      <div class="mx-auto flex min-h-screen w-full flex-col md:flex-row">
        <aside id="shell-sidebar" class="w-full border-b border-border bg-muted/40 px-3 py-4 md:sticky md:top-0 md:h-screen md:w-[280px] md:flex-none md:overflow-y-auto md:border-b-0 md:border-r"></aside>
        <main class="min-w-0 flex-1 bg-background">
          <div id="shell-topbar" class="border-b border-border px-6 py-4"></div>
          <div id="shell-content" class="px-6 py-6"></div>
        </main>
      </div>
      <div id="shell-account-fab" class="fixed bottom-4 right-4 z-30"></div>
      <div id="shell-drawer-backdrop" class="fixed inset-0 z-40 hidden bg-foreground/20"></div>
      <aside id="shell-drawer" aria-hidden="true" aria-modal="true" role="dialog" class="fixed bottom-0 right-0 top-0 z-50 w-full max-w-[680px] translate-x-full overflow-y-auto border-l border-border bg-background px-6 py-5 shadow-lg transition-transform motion-reduce:transition-none"></aside>
      <div id="shell-modal" aria-hidden="true" aria-labelledby="shell-modal-heading" aria-modal="true" role="dialog" class="fixed inset-0 z-50 hidden items-center justify-center bg-foreground/20 p-4">
        <div class="${card} max-h-[85vh] w-full max-w-4xl overflow-y-auto" tabindex="-1">
          <div class="flex items-center justify-between gap-4">
            <div><h2 id="shell-modal-heading" class="text-lg font-semibold">Item logs</h2><p id="shell-modal-title" class="text-xs text-muted-foreground"></p></div>
            <button id="shell-modal-close" class="${buttons}" type="button">Close</button>
          </div>
          <div id="shell-modal-body" class="mt-5"></div>
        </div>
      </div>
    </div>
  `);

  const sidebar = document.getElementById("shell-sidebar") as HTMLDivElement;
  const topbar = document.getElementById("shell-topbar") as HTMLDivElement;
  const content = document.getElementById("shell-content") as HTMLDivElement;
  const drawer = document.getElementById("shell-drawer") as HTMLDivElement;
  const backdrop = document.getElementById("shell-drawer-backdrop") as HTMLDivElement;
  const accountFab = document.getElementById("shell-account-fab") as HTMLDivElement;
  const modal = document.getElementById("shell-modal") as HTMLDivElement;
  const modalTitle = document.getElementById("shell-modal-title") as HTMLParagraphElement;
  const modalBody = document.getElementById("shell-modal-body") as HTMLDivElement;

  function syncDialogInertness(): void {
    const modalOpen = !modal.classList.contains("hidden");
    const drawerOpen = state.panel !== null;
    for (const element of [sidebar, topbar, content, accountFab]) {
      element.inert = modalOpen || drawerOpen;
    }
    drawer.inert = modalOpen;
    modal.inert = !modalOpen;
  }

  const selectedContextId = () => {
    const select = document.getElementById("library-context-select") as HTMLSelectElement | null;
    return select?.value ? BigInt(select.value) : 0n;
  };
  const listItemPage = (query: string, pageToken = "", signal?: AbortSignal) => listItems({
    pageSize: itemPageSize,
    ...(pageToken ? { pageToken } : {}),
    ...(query ? { query } : {}),
    signal,
  });
  async function refreshAuth() {
    try {
      state.auth = await getAuthMe();
      state.authError = "";
      const desired = state.currentWorkspaceId || `${state.auth.user?.defaultWorkspaceId ?? state.auth.workspace?.id ?? ""}`;
      if (state.auth.authenticated && desired) state.currentWorkspaceId = setCurrentWorkspaceId(desired);
    } catch (error) {
      state.auth = null;
      state.authError = String(error);
    }
  }

  async function refreshWorkspaces(): Promise<void> {
    if (!state.auth?.authenticated) {
      state.workspaces = [];
      return;
    }
    state.workspaces = await listWorkspaces().catch((error) => {
      state.dataError = `Workspace data could not be loaded: ${String(error)}`;
      return [] as WorkspaceAccess[];
    });
    const ids = new Set(state.workspaces.map((entry) => uint64ToString(entry.workspace?.id ?? 0n)));
    if (!ids.has(state.currentWorkspaceId)) state.currentWorkspaceId = applyWorkspaceToLocation(workspaceIdString(state.workspaces[0]?.workspace?.id));
  }

  async function refreshMembers() {
    if (!state.currentWorkspaceId || !state.auth?.authenticated) {
      state.members = [];
      return;
    }
    state.members = (await listWorkspaceMembers(state.currentWorkspaceId).catch(() => ({ members: [] as WorkspaceMember[] }))).members;
  }

  async function refreshWorkspaceScopedData() {
    const refreshGeneration = ++itemListGeneration;
    itemListAbortController?.abort();
    const listController = new AbortController();
    itemListAbortController = listController;
    if (!state.auth?.authenticated) {
      state.items = [];
      state.itemsWorkspaceId = "";
      state.itemsQuery = "";
      state.itemsNextPageToken = "";
      state.itemsLoadingMore = false;
      state.contexts = [];
      state.modelCatalog = null;
      state.members = [];
      state.providerSecrets = [];
      state.apiKeys = [];
      if (itemListAbortController === listController) itemListAbortController = undefined;
      return;
    }
    const workspaceID = state.currentWorkspaceId;
    const query = state.search.trim();
    if (state.itemsWorkspaceId !== workspaceID || state.itemsQuery !== query) {
      state.items = [];
      state.itemsWorkspaceId = workspaceID;
      state.itemsQuery = query;
      state.itemsNextPageToken = "";
      state.itemsLoadingMore = false;
    }
    const [items, contexts, catalog] = await Promise.allSettled([listItemPage(query, "", listController.signal), listContexts(), getModelCatalog()]);
    if (refreshGeneration !== itemListGeneration) return;
    if (itemListAbortController === listController) itemListAbortController = undefined;
    state.items = items.status === "fulfilled" ? items.value.items : [];
    state.itemsNextPageToken = items.status === "fulfilled" ? items.value.nextPageToken : "";
    state.itemsLoadingMore = false;
    state.contexts = contexts.status === "fulfilled" ? [...contexts.value].sort((a, b) => Number(b.isDefault) - Number(a.isDefault) || (a.name || "").localeCompare(b.name || "")) : [];
    state.modelCatalog = catalog.status === "fulfilled" ? catalog.value : null;
    state.dataError = [items, contexts, catalog].some((result) => result.status === "rejected") ? "Some workspace data could not be loaded." : "";
    await refreshMembers();
  }

  async function refreshAfterWorkspaceEvent(): Promise<void> {
    if (disposed) return;
    if (workspaceEventRefreshRunning) {
      workspaceEventRefreshPending = true;
      return;
    }
    workspaceEventRefreshRunning = true;
    try {
      do {
        workspaceEventRefreshPending = false;
        await refreshWorkspaceScopedData();
        if (disposed) return;
        if (state.panel) await ensurePanelData(state.panel);
        if (disposed) return;
        renderAll();
      } while (workspaceEventRefreshPending && !disposed);
    } finally {
      workspaceEventRefreshRunning = false;
    }
  }

  async function loadMoreItems(): Promise<void> {
    if (!state.itemsNextPageToken || state.itemsLoadingMore) return;
    const pageToken = state.itemsNextPageToken;
    const pageGeneration = itemListGeneration;
    const workspaceID = state.currentWorkspaceId;
    const query = state.itemsQuery;
    state.itemsLoadingMore = true;
    itemListAbortController?.abort();
    const listController = new AbortController();
    itemListAbortController = listController;
    renderAll();
    try {
      const page = await listItemPage(query, pageToken, listController.signal);
      if (pageGeneration !== itemListGeneration || workspaceID !== state.currentWorkspaceId) return;
      const existing = new Set(state.items.map((item) => item.id));
      state.items.push(...page.items.filter((item) => !existing.has(item.id)));
      state.itemsNextPageToken = page.nextPageToken;
      if (state.dataError.startsWith("More annotations could not be loaded:")) state.dataError = "";
    } catch (error) {
      if (pageGeneration === itemListGeneration && workspaceID === state.currentWorkspaceId) {
        state.dataError = `More annotations could not be loaded: ${String(error)}`;
      }
    } finally {
      if (pageGeneration === itemListGeneration && workspaceID === state.currentWorkspaceId) {
        state.itemsLoadingMore = false;
        if (itemListAbortController === listController) itemListAbortController = undefined;
        renderAll();
      }
    }
  }

  async function refreshContextMetrics() {
    const generation = ++contextMetricsGeneration;
    const workspaceID = state.currentWorkspaceId;
    const contexts = [...state.contexts];
    const metrics = await mapConcurrent(contexts, 4, (ctx) => getContextMetrics(ctx.id.toString()).catch(() => undefined));
    if (generation !== contextMetricsGeneration || workspaceID !== state.currentWorkspaceId) return;
    state.contextMetrics = new Map(metrics.flatMap((metric) => metric ? [[metric.contextId.toString(), metric] as const] : []));
  }

  async function refreshSettingsData() {
    if (!state.auth?.authenticated) return;
    const generation = ++settingsDataGeneration;
    const workspaceID = state.currentWorkspaceId;
    const [secrets, keys] = await Promise.all([
      listProviderSecrets().catch(() => [] as ProviderSecretRecord[]),
      canAdminWorkspace(state) ? listAPIKeys().catch(() => [] as APIKeyRecord[]) : Promise.resolve([] as APIKeyRecord[]),
    ]);
    if (generation !== settingsDataGeneration || workspaceID !== state.currentWorkspaceId) return;
    state.providerSecrets = secrets;
    state.apiKeys = keys;
  }

  async function ensurePanelData(panel: ShellPanel) {
    if (panel === "contexts") await refreshContextMetrics();
    if (panel === "settings") await refreshSettingsData();
  }

  async function openPanel(panel: ShellPanel) {
    const opening = panel !== null && state.panel === null;
    const closing = panel === null && state.panel !== null;
    if (opening) {
      drawerReturnFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    }
    state.panel = panel;
    if (panel) await ensurePanelData(panel);
    renderAll();
    if (panel) {
      (document.getElementById("shell-panel-close") as HTMLButtonElement | null)
        ?.focus({ preventScroll: true });
    } else if (closing) {
      drawerReturnFocus?.focus({ preventScroll: true });
      drawerReturnFocus = null;
    }
  }

  function renderTopbar() {
    setHTML(topbar, html`
      <div class="flex flex-wrap items-center justify-between gap-4">
        <div><p class="text-sm text-muted-foreground">${currentWorkspace(state)?.name || "Workspace"}</p><h1 class="mt-1 text-3xl font-semibold">${state.auth?.authenticated ? "Create new annotation" : "Welcome to Scribe"}</h1></div>
        <button id="shell-contexts-button" class="${buttons}" type="button">Contexts</button>
      </div>
    `);
    document.getElementById("shell-contexts-button")?.addEventListener("click", () => void openPanel("contexts"));
  }

  function renderSidebar() {
    const items = state.items;
    const previousSearch = document.getElementById("sidebar-search") as HTMLInputElement | null;
    const restoreSearchFocus = previousSearch !== null && document.activeElement === previousSearch;
    const searchSelection = restoreSearchFocus ? {
      direction: previousSearch.selectionDirection,
      end: previousSearch.selectionEnd,
      start: previousSearch.selectionStart,
    } : null;
    setHTML(sidebar, state.auth?.authenticated ? html`
      <label class="text-xs uppercase text-muted-foreground" for="sidebar-workspace-select">Workspace</label>
      <select id="sidebar-workspace-select" aria-label="Select workspace" class="${input} mt-2">${state.workspaces.map((entry) => {
        const id = uint64ToString(entry.workspace?.id ?? 0n);
        return html`<option value="${id}"${id === state.currentWorkspaceId ? " selected" : ""}>${entry.workspace?.name || "Workspace"}</option>`;
      })}</select>
      <button id="sidebar-create-workspace" class="${buttons} mt-3 w-full justify-center" type="button">New workspace</button>
      <button id="sidebar-new-annotation" class="${primary} mt-4 w-full justify-center" type="button">New annotation</button>
      <label class="sr-only" for="sidebar-search">Search annotations</label>
      <input id="sidebar-search" aria-label="Search annotations" type="search" value="${state.search}" placeholder="Search annotations" class="${input} mt-4" />
      <div class="mt-5 grid gap-2">${items.map((item) => html`<article class="rounded-md border px-3 py-2"><a href="${editorHrefForItem(item)}" class="block truncate text-sm font-semibold">${item.name || item.id}</a>${renderItemActions(item, state.itemExports.get(item.id))}</article>`)}</div>
      ${state.itemsNextPageToken ? html`<button id="sidebar-load-more" class="${buttons} mt-3 w-full justify-center" type="button"${state.itemsLoadingMore ? " disabled" : ""}>${state.itemsLoadingMore ? "Loading…" : "Load more"}</button>` : ""}
    ` : html`<h2 class="text-2xl font-semibold">Scribe</h2><p class="mt-3 text-sm text-muted-foreground">${state.authError || "Sign in to manage workspaces."}</p><a href="${loginHref(state.auth)}" class="${primary} mt-4">Sign in with Google</a>`);

    document.getElementById("sidebar-workspace-select")?.addEventListener("change", async (event) => {
      state.currentWorkspaceId = applyWorkspaceToLocation((event.target as HTMLSelectElement).value);
      await refreshWorkspaceScopedData();
      if (state.panel) await ensurePanelData(state.panel);
      renderAll();
    });
    document.getElementById("sidebar-create-workspace")?.addEventListener("click", async () => {
      const name = window.prompt("New workspace name")?.trim();
      if (!name) return;
      const created = await createWorkspace(name);
      await refreshWorkspaces();
      state.currentWorkspaceId = applyWorkspaceToLocation(workspaceIdString(created.workspace?.id));
      await refreshWorkspaceScopedData();
      renderAll();
    });
    document.getElementById("sidebar-new-annotation")?.addEventListener("click", () => {
      state.panel = null;
      state.activeLibraryTab = "url";
      renderAll();
    });
    const nextSearch = document.getElementById("sidebar-search") as HTMLInputElement | null;
    nextSearch?.addEventListener("input", (event) => {
      state.search = (event.target as HTMLInputElement).value;
      if (itemSearchTimer !== undefined) window.clearTimeout(itemSearchTimer);
      itemSearchTimer = window.setTimeout(() => {
        itemSearchTimer = undefined;
        void (async () => {
          await refreshWorkspaceScopedData();
          renderAll();
        })();
      }, 250);
    });
    if (restoreSearchFocus && nextSearch) {
      nextSearch.focus({ preventScroll: true });
      if (searchSelection && searchSelection.start !== null && searchSelection.end !== null) {
        nextSearch.setSelectionRange(searchSelection.start, searchSelection.end, searchSelection.direction ?? undefined);
      }
    }
    document.getElementById("sidebar-load-more")?.addEventListener("click", () => void loadMoreItems());
  }

  function renderLibrary() {
    if (!state.auth?.authenticated) {
      setHTML(content, html`<section class="mx-auto max-w-2xl py-16 text-center"><h2 class="text-4xl font-semibold">Create annotations with your workspace</h2><a href="${loginHref(state.auth)}" class="${primary} mt-8">Sign in with Google</a></section>`);
      return;
    }
    const annotations = state.items;
    setHTML(content, html`
      <section class="mx-auto max-w-5xl">
        ${state.dataError ? html`<div class="mb-4 rounded-md border border-destructive/30 bg-destructive/10 px-4 py-3 text-sm text-destructive">${state.dataError}</div>` : ""}
        <div class="${card} mx-auto max-w-3xl">
          <div class="flex flex-wrap justify-center gap-2">${(["url", "single", "multi", "manifest"] as LibraryTab[]).map((tab) => html`<button data-library-tab="${tab}" class="${tab === state.activeLibraryTab ? primary : buttons}" type="button">${tab}</button>`)}</div>
          <div class="mt-5 flex flex-wrap items-center gap-3"><label for="library-context-select" class="text-sm">Context</label><select id="library-context-select" class="${input} max-w-xs"><option value="0">Default</option>${contextOptions(state.contexts)}</select></div>
          <form id="library-form-url" class="${state.activeLibraryTab === "url" ? "mt-5 grid gap-3" : "hidden"}"><input id="library-image-url" aria-label="Image URL" type="url" required class="${input}" placeholder="https://example.org/image.jpg" /><button class="${primary}" type="submit">Process URL</button><p id="library-url-status" aria-live="polite" class="text-sm text-muted-foreground"></p></form>
          <form id="library-form-single" class="${state.activeLibraryTab === "single" ? "mt-5 grid gap-3" : "hidden"}"><input id="library-single-file" aria-label="Upload one image" type="file" class="${input}" /><button class="${primary}" type="submit">Upload and process</button><p id="library-single-status" aria-live="polite" class="text-sm text-muted-foreground"></p></form>
          <form id="library-form-multi" class="${state.activeLibraryTab === "multi" ? "mt-5 grid gap-3" : "hidden"}"><input id="library-multi-files" aria-label="Upload multiple images" type="file" multiple class="${input}" /><div class="flex gap-2"><button class="${primary}" type="submit">Upload or resume batch</button><button id="library-multi-cancel" class="${buttons}" type="button">Cancel</button></div><p id="library-multi-status" aria-live="polite" class="text-sm text-muted-foreground"></p></form>
          <form id="library-form-manifest" class="${state.activeLibraryTab === "manifest" ? "mt-5 grid gap-3" : "hidden"}"><input id="library-manifest-url" aria-label="IIIF manifest URL" type="url" required class="${input}" placeholder="https://example.org/manifest.json" /><label class="text-sm"><input type="radio" name="library-manifest-mode" value="import" checked /> Edit imported hOCR directly</label><label class="text-sm"><input type="radio" name="library-manifest-mode" value="reprocess" /> Reprocess imported pages</label><button class="${primary}" type="submit">Ingest manifest</button><p id="library-manifest-status" aria-live="polite" class="text-sm text-muted-foreground"></p></form>
        </div>
        <div class="mt-8 flex flex-wrap items-center justify-between gap-3"><h2 class="text-2xl font-semibold">Annotations</h2><div class="flex gap-2">${state.itemsNextPageToken ? html`<button id="library-load-more" class="${buttons}" type="button"${state.itemsLoadingMore ? " disabled" : ""}>${state.itemsLoadingMore ? "Loading…" : "Load more"}</button>` : ""}<button id="library-refresh" class="${buttons}" type="button">Refresh</button></div></div>
        <div class="mt-5 grid gap-3 lg:grid-cols-2">${annotations.length === 0 ? html`<div class="${card} text-sm text-muted-foreground">${state.itemsQuery ? "No annotations match this search." : "No annotations yet."}</div>` : annotations.map((item) => renderItemCard(item, state.itemExports.get(item.id)))}</div>
      </section>
    `);
    bindLibraryActions();
  }

  function bindLibraryActions() {
    content.querySelectorAll<HTMLButtonElement>("[data-library-tab]").forEach((button) => button.addEventListener("click", () => {
      state.activeLibraryTab = button.dataset.libraryTab as LibraryTab;
      renderAll();
    }));
    document.getElementById("library-refresh")?.addEventListener("click", async () => {
      await refreshWorkspaceScopedData();
      renderAll();
    });
    document.getElementById("library-load-more")?.addEventListener("click", () => void loadMoreItems());
    document.getElementById("library-form-url")?.addEventListener("submit", async (event) => {
      event.preventDefault();
      const status = document.getElementById("library-url-status") as HTMLParagraphElement;
      const imageUrl = (document.getElementById("library-image-url") as HTMLInputElement).value.trim();
      if (!imageUrl) return;
      try {
        const result = await processImageURL(imageUrl, selectedContextId());
        const itemImageId = uint64ToString(result.itemImageId);
        if (itemImageId && itemImageId !== "0") {
          await waitForAutomaticTranscriptionStart(itemImageId);
          window.location.href = workspaceAwarePath(`/editor?itemImageId=${encodeURIComponent(itemImageId)}`);
          return;
        }
        await refreshWorkspaceScopedData();
        renderAll();
      } catch (error) { status.textContent = `Error: ${String(error)}`; }
    });
    document.getElementById("library-form-single")?.addEventListener("submit", async (event) => {
      event.preventDefault();
      const status = document.getElementById("library-single-status") as HTMLParagraphElement;
      const file = (document.getElementById("library-single-file") as HTMLInputElement).files?.[0];
      if (!file) return;
      try {
        const item = await uploadItemImages([file], { contextId: selectedContextId() });
        const itemImageId = item.images[0] ? uint64ToString(item.images[0].id) : "";
        if (itemImageId && itemImageId !== "0") {
          await waitForAutomaticTranscriptionStart(itemImageId);
          window.location.href = workspaceAwarePath(`/editor?itemImageId=${encodeURIComponent(itemImageId)}`);
          return;
        }
      } catch (error) { status.textContent = `Error: ${String(error)}`; }
    });
    document.getElementById("library-form-multi")?.addEventListener("submit", async (event) => {
      event.preventDefault();
      const status = document.getElementById("library-multi-status") as HTMLParagraphElement;
      const files = Array.from((document.getElementById("library-multi-files") as HTMLInputElement).files ?? []);
      if (files.length === 0) return;
      activeMultiUpload?.abort();
      const controller = new AbortController();
      activeMultiUpload = controller;
      try {
        await uploadItemImages(files, {
          contextId: selectedContextId(),
          signal: controller.signal,
          onProgress: ({ completed, total, filename, status: progressStatus }) => {
            status.textContent = progressStatus === "hashing"
              ? `Preparing ${completed + 1}/${total}: ${filename}`
              : `Uploaded ${completed}/${total}: ${filename}`;
          },
        });
        await refreshWorkspaceScopedData();
        renderAll();
      } catch (error) {
        status.textContent = controller.signal.aborted ? "Batch canceled; select the same files to resume." : `Error: ${String(error)}`;
      } finally {
        if (activeMultiUpload === controller) activeMultiUpload = undefined;
      }
    });
    document.getElementById("library-multi-cancel")?.addEventListener("click", () => activeMultiUpload?.abort());
    document.getElementById("library-form-manifest")?.addEventListener("submit", async (event) => {
      event.preventDefault();
      const status = document.getElementById("library-manifest-status") as HTMLParagraphElement;
      const manifestUrl = (document.getElementById("library-manifest-url") as HTMLInputElement).value.trim();
      const mode = (document.querySelector('input[name="library-manifest-mode"]:checked') as HTMLInputElement | null)?.value ?? "import";
      if (!manifestUrl) return;
      try {
		const contextId = selectedContextId();
		const result = await importManifest(manifestUrl, contextId);
        if (mode === "reprocess") {
          for (const image of result.item.images ?? []) {
            const itemImageId = uint64ToString(image.id);
            const canonicalPage = await getAnnotationPage(itemImageId);
			await reprocessItemImage(itemImageId, contextId, canonicalPage.revision);
          }
        }
        if (result.firstItemImageId) {
          window.location.href = workspaceAwarePath(`/editor?${new URLSearchParams({ itemImageId: result.firstItemImageId, itemId: result.item.id }).toString()}`);
          return;
        }
        await refreshWorkspaceScopedData();
        renderAll();
      } catch (error) { status.textContent = `Error: ${String(error)}`; }
    });
  }

  function renderContextsPanel() {
    const metrics = state.contexts.map((ctx) => state.contextMetrics.get(ctx.id.toString()));
    const providers = state.modelCatalog?.transcriptionProviders ?? [];
    const selectedProvider = providers.find((provider) => provider.id === "ollama") ?? providers[0];
    const segmentationModels = state.modelCatalog?.segmentationModels ?? [];
    const defaultSegmentationModel = segmentationModels.find((model) => model.isDefault)?.id ?? segmentationModels[0]?.id ?? "tesseract";
    const providerOptions = providers.map((provider) => html`<option value="${provider.id}"${provider.id === selectedProvider?.id ? " selected" : ""}>${provider.label || provider.id}</option>`);
    const modelOptions = (selectedProvider?.models ?? []).map((model) => html`<option value="${model.id}"${model.isDefault ? " selected" : ""}>${model.label || model.id}</option>`);
    const segmentationOptions = segmentationModels.map((model) => html`<option value="${model.id}"${model.id === defaultSegmentationModel ? " selected" : ""}>${model.label || model.id}</option>`);
    setHTML(drawer, html`
      <div class="flex items-start justify-between gap-4"><h2 id="shell-panel-heading" class="text-2xl font-semibold">OCR context library</h2><button id="shell-panel-close" class="${buttons}" type="button">Close</button></div>
      ${state.auth?.authenticated ? html`
        <form id="contexts-create-form" class="${card} mt-5 grid gap-3"><input id="contexts-name" aria-label="Context name" class="${input}" placeholder="Name" required /><label class="grid gap-1 text-sm">Transcription provider<select id="contexts-provider" class="${input}">${providerOptions}</select></label><label class="grid gap-1 text-sm">Transcription model<select id="contexts-model" class="${input}">${modelOptions}</select></label><label class="grid gap-1 text-sm">Segmentation model<select id="contexts-segmentation" class="${input}">${segmentationOptions}</select></label><textarea id="contexts-description" aria-label="Context description" class="${input}" placeholder="Description"></textarea><textarea id="contexts-system-prompt" aria-label="System prompt" class="${input}" placeholder="System prompt"></textarea><label id="contexts-temperature-label" class="grid gap-1 text-sm">Temperature<input id="contexts-temperature" class="${input}" type="number" min="0" max="2" step="0.05" placeholder="Provider default" /></label><label><input id="contexts-default" type="checkbox" /> Set as default</label><button class="${primary}" type="submit">Create context</button><p id="contexts-status" aria-live="polite" class="text-sm text-muted-foreground"></p></form>
        <div class="mt-5 grid gap-3">${state.contexts.map((ctx, index) => html`<article class="${card}"><h3 class="font-semibold">${ctx.name}</h3><p class="text-sm text-muted-foreground">${ctx.transcriptionProvider} · ${ctx.transcriptionModel || "default"}</p><p class="mt-2 text-xs text-muted-foreground">Runs ${metrics[index]?.totalRuns ?? 0}</p></article>`)}</div>
      ` : html`<a href="${loginHref(state.auth)}" class="${primary} mt-5">Sign in with Google</a>`}
    `);
    document.getElementById("shell-panel-close")?.addEventListener("click", () => void openPanel(null));
    const providerSelect = document.getElementById("contexts-provider") as HTMLSelectElement | null;
    const modelSelect = document.getElementById("contexts-model") as HTMLSelectElement | null;
    const systemPrompt = document.getElementById("contexts-system-prompt") as HTMLTextAreaElement | null;
    const temperature = document.getElementById("contexts-temperature") as HTMLInputElement | null;
    const temperatureLabel = document.getElementById("contexts-temperature-label") as HTMLLabelElement | null;
    const syncProviderCapabilities = () => {
      const provider = providers.find((candidate) => candidate.id === providerSelect?.value) ?? selectedProvider;
      // Some DOM implementations do not initialize select.value from a
      // dynamically-rendered selected option until the next layout pass.
      // Make the catalog default explicit so the dependent model selector is
      // deterministic in browsers, tests, and embedded webviews.
      if (providerSelect && provider && providerSelect.value !== provider.id) {
        providerSelect.value = provider.id;
      }
      if (modelSelect) {
        setHTML(modelSelect, html`${(provider?.models ?? []).map((model) => html`<option value="${model.id}"${model.isDefault ? " selected" : ""}>${model.label || model.id}</option>`)}`);
      }
      if (systemPrompt) {
        systemPrompt.disabled = !provider?.supportsSystemPrompt;
        if (systemPrompt.disabled) systemPrompt.value = "";
      }
      if (temperature && temperatureLabel) {
        temperature.disabled = !provider?.supportsTemperature;
        temperatureLabel.hidden = temperature.disabled;
        if (temperature.disabled) temperature.value = "";
      }
    };
    providerSelect?.addEventListener("change", syncProviderCapabilities);
    syncProviderCapabilities();
    document.getElementById("contexts-create-form")?.addEventListener("submit", async (event) => {
      event.preventDefault();
      const name = (document.getElementById("contexts-name") as HTMLInputElement).value.trim();
      const status = document.getElementById("contexts-status") as HTMLParagraphElement;
      if (!name) { status.textContent = "Name is required."; return; }
      const temperatureValue = (document.getElementById("contexts-temperature") as HTMLInputElement).value;
      await createContext(create(ContextSchema, {
        name,
        description: (document.getElementById("contexts-description") as HTMLTextAreaElement).value.trim(),
        isDefault: (document.getElementById("contexts-default") as HTMLInputElement).checked,
        segmentationModel: (document.getElementById("contexts-segmentation") as HTMLInputElement).value.trim() || "tesseract",
        transcriptionProvider: (document.getElementById("contexts-provider") as HTMLInputElement).value.trim() || "ollama",
        transcriptionModel: (document.getElementById("contexts-model") as HTMLInputElement).value.trim(),
        temperature: temperatureValue === "" ? undefined : Number(temperatureValue),
        systemPrompt: (document.getElementById("contexts-system-prompt") as HTMLTextAreaElement).value.trim(),
      }));
      await refreshWorkspaceScopedData();
      await refreshContextMetrics();
      renderAll();
    });
  }

  function renderSettingsPanel() {
    const workspace = currentWorkspace(state);
    const admin = canAdminWorkspace(state);
    const secretProviderOptions = (state.modelCatalog?.transcriptionProviders ?? [])
      .filter((provider) => provider.requiresApiKey)
      .map((provider) => html`<option value="${provider.id}">${provider.label || provider.id}</option>`);
    setHTML(drawer, html`
      <div class="flex items-start justify-between gap-4"><h2 id="shell-panel-heading" class="text-2xl font-semibold">Workspace and account settings</h2><button id="shell-panel-close" class="${buttons}" type="button">Close</button></div>
      ${state.auth?.authenticated ? html`
        <section class="${card} mt-5"><div class="flex items-center justify-between gap-3"><p>${state.auth.user?.email}</p><button id="settings-logout" class="${buttons}" type="button">Logout</button></div></section>
        <section class="${card} mt-4"><h3 class="text-xl font-semibold">${workspace?.name || "Workspace"}</h3><form id="settings-rename-workspace" class="mt-3 flex gap-2"><input id="settings-workspace-name" aria-label="Workspace name" class="${input}" value="${workspace?.name || ""}"${admin && !workspace?.isPersonal ? "" : " disabled"} /><button class="${primary}" type="submit"${admin && !workspace?.isPersonal ? "" : " disabled"}>Rename</button></form><form id="settings-create-workspace" class="mt-3 flex gap-2"><input id="settings-new-workspace-name" aria-label="New workspace name" class="${input}" placeholder="Create a new workspace" /><button class="${buttons}" type="submit">Create workspace</button></form><p id="settings-workspace-status" aria-live="polite" class="mt-2 text-sm text-muted-foreground"></p></section>
        <section class="${card} mt-4"><h3 class="text-xl font-semibold">Workspace members</h3><div class="mt-3 grid gap-2">${state.members.map((member) => html`<div class="flex flex-wrap items-center justify-between gap-2 border-t py-2"><span>${member.user?.email || member.user?.name}</span><select aria-label="Role for ${member.user?.email || member.user?.name}" data-member-role="${uint64ToString(member.user?.id ?? 0n)}" class="${input} max-w-[140px]"${admin && !workspace?.isPersonal ? "" : " disabled"}>${["admin", "write", "create", "read"].map((role) => html`<option value="${role}"${member.role === role ? " selected" : ""}>${role}</option>`)}</select><button data-member-save="${uint64ToString(member.user?.id ?? 0n)}" class="${buttons}" type="button"${admin && !workspace?.isPersonal ? "" : " disabled"}>Save</button><button data-member-remove="${uint64ToString(member.user?.id ?? 0n)}" class="${buttons}" type="button"${admin && !workspace?.isPersonal ? "" : " disabled"}>Remove</button></div>`)}</div><form id="settings-add-member" class="mt-3 grid gap-2 sm:grid-cols-[1fr_140px_auto]"><input id="settings-member-email" aria-label="Member email" class="${input}" placeholder="user@example.edu"${admin && !workspace?.isPersonal ? "" : " disabled"} /><select id="settings-member-role" aria-label="New member role" class="${input}"${admin && !workspace?.isPersonal ? "" : " disabled"}><option>read</option><option>create</option><option>write</option><option>admin</option></select><button class="${primary}" type="submit"${admin && !workspace?.isPersonal ? "" : " disabled"}>Add member</button></form><p id="settings-members-status" aria-live="polite" class="mt-2 text-sm text-muted-foreground"></p></section>
        <section class="${card} mt-4"><h3 class="text-xl font-semibold">Provider secrets</h3><p class="mt-2 text-sm text-muted-foreground">Workspace keys power uploads, reprocessing, and queued transcription. Personal keys are limited to interactive editor enrichment.</p><form id="settings-provider-secret-form" class="mt-3 grid gap-2"><select id="settings-provider-secret-provider" aria-label="Provider" class="${input}"${canWriteWorkspace(state) && secretProviderOptions.length > 0 ? "" : " disabled"}>${secretProviderOptions}</select><input id="settings-provider-secret-name" aria-label="Provider key name" class="${input}" placeholder="Name"${canWriteWorkspace(state) ? "" : " disabled"} /><select id="settings-provider-secret-scope" aria-label="Provider key scope" class="${input}"${canWriteWorkspace(state) ? "" : " disabled"}>${admin ? html`<option value="workspace" selected>Workspace (queued processing)</option>` : ""}<option value="user"${admin ? "" : " selected"}>Personal (editor enrichment only)</option></select><input id="settings-provider-secret-api-key" aria-label="Provider API key" type="password" class="${input}" placeholder="API key"${canWriteWorkspace(state) ? "" : " disabled"} /><button class="${primary}" type="submit"${canWriteWorkspace(state) && secretProviderOptions.length > 0 ? "" : " disabled"}>Save provider key</button><p id="settings-provider-secret-status" aria-live="polite" class="text-sm text-muted-foreground"></p></form><div class="mt-3">${renderProviderSecrets(state.providerSecrets)}</div></section>
        <section class="${card} mt-4"><h3 class="text-xl font-semibold">Workspace tokens</h3>${admin ? html`<form id="settings-api-key-form" class="mt-3 grid gap-2 sm:grid-cols-[1fr_140px_auto]"><input id="settings-api-key-name" aria-label="Workspace token name" class="${input}" placeholder="Token name" /><select id="settings-api-key-role" aria-label="Workspace token role" class="${input}"><option>read</option><option>create</option><option>write</option><option>admin</option></select><button class="${primary}" type="submit">Create key</button></form>` : ""}<p id="settings-api-key-status" aria-live="polite" class="mt-2 text-sm text-muted-foreground"></p><div class="mt-3">${admin ? renderAPIKeys(state.apiKeys) : ""}</div></section>
      ` : html`<a href="${loginHref(state.auth)}" class="${primary} mt-5">Sign in with Google</a>`}
    `);
    bindSettingsActions();
  }

  function bindSettingsActions() {
    const workspace = currentWorkspace(state);
    const workspaceStatus = document.getElementById("settings-workspace-status") as HTMLParagraphElement | null;
    const membersStatus = document.getElementById("settings-members-status") as HTMLParagraphElement | null;
    const apiKeyStatus = document.getElementById("settings-api-key-status") as HTMLParagraphElement | null;
    const secretStatus = document.getElementById("settings-provider-secret-status") as HTMLParagraphElement | null;
    document.getElementById("shell-panel-close")?.addEventListener("click", () => void openPanel(null));
    document.getElementById("settings-logout")?.addEventListener("click", async () => { await logout(); window.location.href = "/"; });
    document.getElementById("settings-rename-workspace")?.addEventListener("submit", async (event) => {
      event.preventDefault(); if (!workspace) return;
      await updateWorkspace(workspace.id, (document.getElementById("settings-workspace-name") as HTMLInputElement).value.trim());
      await refreshWorkspaces(); renderAll();
    });
    document.getElementById("settings-create-workspace")?.addEventListener("submit", async (event) => {
      event.preventDefault();
      const name = (document.getElementById("settings-new-workspace-name") as HTMLInputElement).value.trim();
      if (!name) return;
      const created = await createWorkspace(name);
      await refreshWorkspaces(); state.currentWorkspaceId = applyWorkspaceToLocation(workspaceIdString(created.workspace?.id)); await refreshWorkspaceScopedData(); await refreshSettingsData(); renderAll();
    });
    document.getElementById("settings-add-member")?.addEventListener("submit", async (event) => {
      event.preventDefault(); if (!workspace) return;
      await addWorkspaceMember(workspace.id, (document.getElementById("settings-member-email") as HTMLInputElement).value.trim(), (document.getElementById("settings-member-role") as HTMLSelectElement).value).catch((error) => { if (membersStatus) membersStatus.textContent = `Add failed: ${String(error)}`; });
      await refreshMembers(); renderAll();
    });
    drawer.querySelectorAll<HTMLButtonElement>("[data-member-save]").forEach((button) => button.addEventListener("click", async () => {
      if (!workspace || !button.dataset.memberSave) return;
      await updateWorkspaceMember(workspace.id, button.dataset.memberSave, drawer.querySelector<HTMLSelectElement>(`[data-member-role="${button.dataset.memberSave}"]`)?.value ?? "read");
      await refreshMembers(); renderAll();
    }));
    drawer.querySelectorAll<HTMLButtonElement>("[data-member-remove]").forEach((button) => button.addEventListener("click", async () => {
      if (!workspace || !button.dataset.memberRemove || !window.confirm("Remove this workspace member?")) return;
      await deleteWorkspaceMember(workspace.id, button.dataset.memberRemove); await refreshMembers(); renderAll();
    }));
    document.getElementById("settings-api-key-form")?.addEventListener("submit", async (event) => {
      event.preventDefault();
      const created = await createAPIKey({ name: (document.getElementById("settings-api-key-name") as HTMLInputElement).value.trim(), role: (document.getElementById("settings-api-key-role") as HTMLSelectElement).value }).catch((error) => { if (apiKeyStatus) apiKeyStatus.textContent = `Create failed: ${String(error)}`; return null; });
      if (!created) return;
      window.alert(`Copy this API key now. It will not be shown again.\n\n${created.key}`);
      await refreshSettingsData(); renderAll();
    });
    drawer.querySelectorAll<HTMLButtonElement>("[data-api-key-delete]").forEach((button) => button.addEventListener("click", async () => {
      if (!button.dataset.apiKeyDelete || !window.confirm("Delete this API key?")) return;
      await deleteAPIKey(button.dataset.apiKeyDelete).catch((error) => { if (apiKeyStatus) apiKeyStatus.textContent = `Delete failed: ${String(error)}`; });
      await refreshSettingsData(); renderAll();
    }));
    document.getElementById("settings-provider-secret-form")?.addEventListener("submit", async (event) => {
      event.preventDefault();
      await createProviderSecret({ provider: (document.getElementById("settings-provider-secret-provider") as HTMLSelectElement).value, name: (document.getElementById("settings-provider-secret-name") as HTMLInputElement).value.trim(), apiKey: (document.getElementById("settings-provider-secret-api-key") as HTMLInputElement).value.trim(), scope: (document.getElementById("settings-provider-secret-scope") as HTMLSelectElement).value as "user" | "workspace" }).catch((error) => { if (secretStatus) secretStatus.textContent = `Save failed: ${String(error)}`; });
      await refreshSettingsData(); renderAll();
    });
    drawer.querySelectorAll<HTMLButtonElement>("[data-provider-secret-delete]").forEach((button) => button.addEventListener("click", async () => {
      if (!button.dataset.providerSecretDelete || !window.confirm("Delete this provider secret?")) return;
      await deleteProviderSecret(button.dataset.providerSecretDelete).catch((error) => { if (secretStatus) secretStatus.textContent = `Delete failed: ${String(error)}`; });
      await refreshSettingsData(); renderAll();
    }));
    void workspaceStatus;
  }

  async function openLogsModal(itemId: string) {
    const item = state.items.find((entry) => entry.id === itemId);
    if (!item) return;
    const loadGeneration = ++modalLoadGeneration;
    modalReturnFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    modal.classList.remove("hidden");
    modal.classList.add("flex");
    modal.setAttribute("aria-hidden", "false");
    modalTitle.textContent = `${item.name || item.id} (${item.id})`;
    setHTML(modalBody, html`<p class="text-muted-foreground">Loading logs...</p>`);
    syncDialogInertness();
    (document.getElementById("shell-modal-close") as HTMLButtonElement | null)?.focus({ preventScroll: true });
    const audits = await listItemProviderCallAudits(item.id, 100).catch(() => []);
    if (loadGeneration !== modalLoadGeneration) return;
    setHTML(modalBody, audits.length === 0 ? html`<p class="text-muted-foreground">No provider logs recorded for this item yet.</p>` : html`<div class="grid gap-3">${audits.map((audit) => html`<article class="${card}"><p>${audit.provider} ${audit.model} (${audit.operation})</p><p class="text-xs text-muted-foreground">${formatDateTime(audit.createdAt)} · ${audit.durationMs} ms${audit.httpStatus ? ` · HTTP ${audit.httpStatus}` : ""}</p>${audit.errorMessage ? html`<p class="text-destructive">${audit.errorMessage}</p>` : ""}</article>`)}</div>`);
  }

  async function downloadItemExport(itemId: string, format: AnnotationExportFormat): Promise<void> {
    if (state.itemExports.get(itemId)?.busyFormat !== undefined) return;
    state.itemExports.set(itemId, { busyFormat: format });
    renderAll();
    try {
      const { item, expectedRevisions } = await getItemExportSnapshot(itemId);
      const prepared = await prepareItemExport(item.id, format, expectedRevisions);
      const downloadURL = new URL(prepared.downloadUrl, window.location.origin);
      if (
        !prepared.downloadUrl.startsWith("/")
        || prepared.downloadUrl.startsWith("//")
        || downloadURL.origin !== window.location.origin
        || !downloadURL.pathname.startsWith("/v1/item-exports/")
        || downloadURL.pathname === "/v1/item-exports/"
      ) {
        throw new Error("export service returned an invalid download URL");
      }
      state.itemExports.delete(itemId);
      window.location.href = workspaceAwarePath(`${downloadURL.pathname}${downloadURL.search}${downloadURL.hash}`);
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error);
      state.itemExports.set(itemId, { error: `Export failed: ${message}` });
    } finally {
      renderAll();
    }
  }

  function bindSharedItemActions() {
    app.querySelectorAll<HTMLButtonElement>("[data-item-logs]").forEach((button) => button.addEventListener("click", () => void openLogsModal(button.dataset.itemLogs ?? "")));
    app.querySelectorAll<HTMLButtonElement>("[data-item-export]").forEach((button) => button.addEventListener("click", () => {
      const itemId = button.dataset.itemExport ?? "";
      const exportOption = itemExportFormats.find(({ format }) => `${format}` === button.dataset.itemExportFormat);
      if (itemId && exportOption) void downloadItemExport(itemId, exportOption.format);
    }));
    app.querySelectorAll<HTMLButtonElement>("[data-item-delete]").forEach((button) => button.addEventListener("click", async () => {
      if (!button.dataset.itemDelete || !window.confirm(`Delete item "${button.dataset.itemDelete}"?`)) return;
      await deleteItem(button.dataset.itemDelete);
      await refreshWorkspaceScopedData();
      if (state.panel) await ensurePanelData(state.panel);
      renderAll();
    }));
  }

  function renderDrawer() {
    if (!state.panel) {
      backdrop.classList.add("hidden");
      drawer.classList.add("translate-x-full");
      drawer.setAttribute("aria-hidden", "true");
      drawer.removeAttribute("aria-labelledby");
      drawer.replaceChildren();
      return;
    }
    backdrop.classList.remove("hidden");
    drawer.classList.remove("translate-x-full");
    drawer.setAttribute("aria-hidden", "false");
    drawer.setAttribute("aria-labelledby", "shell-panel-heading");
    if (state.panel === "contexts") renderContextsPanel();
    else renderSettingsPanel();
  }

  function renderAccountFab() {
    setHTML(accountFab, state.auth?.authenticated ? html`<button id="shell-account-button" class="${buttons}" type="button"><span class="flex size-8 items-center justify-center rounded-full bg-primary text-xs font-semibold text-primary-foreground">${avatar(state.auth)}</span><span>${currentWorkspaceRole(state) || "Settings"}</span></button>` : html`<a href="${loginHref(state.auth)}" class="${buttons}">Sign in</a>`);
    document.getElementById("shell-account-button")?.addEventListener("click", () => void openPanel("settings"));
  }

  function renderAll() {
    renderTopbar();
    renderSidebar();
    renderLibrary();
    renderDrawer();
    renderAccountFab();
    bindSharedItemActions();
    syncDialogInertness();
  }

  function closeLogsModal() {
    modalLoadGeneration++;
    modal.classList.add("hidden");
    modal.classList.remove("flex");
    modal.setAttribute("aria-hidden", "true");
    syncDialogInertness();
    modalReturnFocus?.focus({ preventScroll: true });
    modalReturnFocus = null;
  }

  function trapDialogFocus(event: KeyboardEvent, container: HTMLElement) {
    if (event.key !== "Tab") return;
    const focusable = Array.from(container.querySelectorAll<HTMLElement>(
      'button:not([disabled]), a[href], input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])',
    )).filter((element) => !element.hidden && element.getAttribute("aria-hidden") !== "true");
    if (focusable.length === 0) return;
    const first = focusable[0];
    const last = focusable[focusable.length - 1];
    if (event.shiftKey && document.activeElement === first) {
      event.preventDefault();
      last.focus();
    } else if (!event.shiftKey && document.activeElement === last) {
      event.preventDefault();
      first.focus();
    }
  }

  const handleDialogKeyDown = (event: KeyboardEvent) => {
    if (!modal.classList.contains("hidden")) {
      if (event.key === "Escape") {
        event.preventDefault();
        closeLogsModal();
      } else {
        trapDialogFocus(event, modal);
      }
      return;
    }
    if (state.panel) {
      if (event.key === "Escape") {
        event.preventDefault();
        void openPanel(null);
      } else {
        trapDialogFocus(event, drawer);
      }
    }
  };

  document.getElementById("shell-modal-close")?.addEventListener("click", closeLogsModal);
  modal.addEventListener("click", (event) => { if (event.target === modal) closeLogsModal(); });
  backdrop.addEventListener("click", () => void openPanel(null));
  document.addEventListener("keydown", handleDialogKeyDown);

  await refreshAuth();
  await refreshWorkspaces();
  await refreshWorkspaceScopedData();
  if (state.panel) await ensurePanelData(state.panel);
  renderAll();

  const subscription = state.auth?.authenticated ? subscribeToEvents({ types: ["dev.scribe.transcription.completed", "dev.scribe.transcription.failed", "dev.scribe.annotations.created", "dev.scribe.annotations.published"] }, () => {
    void refreshAfterWorkspaceEvent();
  }) : null;
  window.addEventListener("pagehide", () => {
    disposed = true;
    modalLoadGeneration++;
    if (itemSearchTimer !== undefined) window.clearTimeout(itemSearchTimer);
    itemListAbortController?.abort();
    activeMultiUpload?.abort();
    document.removeEventListener("keydown", handleDialogKeyDown);
    subscription?.close();
  }, { once: true });
}
