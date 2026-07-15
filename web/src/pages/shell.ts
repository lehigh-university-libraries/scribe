import { create } from "@bufbuild/protobuf";
import { createAPIKey, createProviderSecret, deleteAPIKey, deleteProviderSecret, getAuthMe, listAPIKeys, listProviderSecrets, logout, type APIKeyRecord, type GetAuthMeResponse, type ProviderSecretRecord } from "../api/auth";
import { createContext, getContextMetrics, getModelCatalog, listContexts, type ContextMetrics } from "../api/context";
import { subscribeToEvents } from "../api/events";
import { createItemFromManifest, deleteItem, listItemProviderCallAudits, listItems, uploadItemImages } from "../api/items";
import { processImageUpload, processImageURL, reprocessItemImage } from "../api/processing";
import { listTranscriptionJobs } from "../api/transcription";
import { addWorkspaceMember, createWorkspace, deleteWorkspaceMember, listWorkspaceMembers, listWorkspaces, updateWorkspace, updateWorkspaceMember } from "../api/workspaces";
import { applyWorkspaceToLocation, getCurrentWorkspaceId, setCurrentWorkspaceId, syncWorkspaceSelectionFromLocation, workspaceAwarePath } from "../lib/workspace";
import { html, setHTML, uint64ToString } from "../lib/util";
import { ContextSchema, type Context, type GetModelCatalogResponse } from "../proto/scribe/v1/context_pb";
import type { Item } from "../proto/scribe/v1/item_pb";
import type { WorkspaceAccess, WorkspaceMember } from "../proto/scribe/v1/workspace_pb";
import { avatar, buttons, canAdminWorkspace, canWriteWorkspace, card, contextOptions, currentWorkspace, currentWorkspaceRole, editorHrefForItem, formatDateTime, input, loginHref, primary, renderAPIKeys, renderItemActions, renderItemCard, renderProviderSecrets, waitForAutomaticTranscriptionStart, workspaceIdString } from "./shell_helpers";

type ShellView = "library" | "contexts" | "settings";
type ShellPanel = Exclude<ShellView, "library"> | null;
type LibraryTab = "url" | "single" | "multi" | "manifest";
interface ShellState {
  auth: GetAuthMeResponse | null;
  authError: string;
  dataError: string;
  panel: ShellPanel;
  search: string;
  activeLibraryTab: LibraryTab;
  workspaces: WorkspaceAccess[];
  currentWorkspaceId: string;
  items: Item[];
  contexts: Context[];
  modelCatalog: GetModelCatalogResponse | null;
  contextMetrics: Map<string, ContextMetrics>;
  providerSecrets: ProviderSecretRecord[];
  apiKeys: APIKeyRecord[];
  members: WorkspaceMember[];
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
    contexts: [],
    modelCatalog: null,
    contextMetrics: new Map(),
    providerSecrets: [],
    apiKeys: [],
    members: [],
  };

  setHTML(app, html`
    <div class="min-h-screen text-foreground">
      <div class="mx-auto flex min-h-screen w-full">
        <aside id="shell-sidebar" class="w-full max-w-[280px] border-r border-border bg-muted/40 px-3 py-4"></aside>
        <main class="min-w-0 flex-1 bg-background">
          <div id="shell-topbar" class="border-b border-border px-6 py-4"></div>
          <div id="shell-content" class="px-6 py-6"></div>
        </main>
      </div>
      <div id="shell-account-fab" class="fixed bottom-4 right-4 z-30"></div>
      <div id="shell-drawer-backdrop" class="fixed inset-0 z-40 hidden bg-foreground/20"></div>
      <aside id="shell-drawer" class="fixed bottom-0 right-0 top-0 z-50 w-full max-w-[680px] translate-x-full overflow-y-auto border-l border-border bg-background px-6 py-5 shadow-lg transition-transform"></aside>
      <div id="shell-modal" class="fixed inset-0 z-50 hidden items-center justify-center bg-foreground/20 p-4">
        <div class="${card} max-h-[85vh] w-full max-w-4xl overflow-y-auto">
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

  const selectedContextId = () => {
    const select = document.getElementById("library-context-select") as HTMLSelectElement | null;
    return select?.value ? BigInt(select.value) : 0n;
  };
  const filteredItems = () => {
    const query = state.search.trim().toLowerCase();
    return query ? state.items.filter((item) => `${item.name} ${item.id} ${item.sourceType}`.toLowerCase().includes(query)) : state.items;
  };
  const uniqueOptions = (values: readonly string[]) => [...new Set(values.map((value) => value.trim()).filter(Boolean))].sort((a, b) => a.localeCompare(b));
  const datalistOptions = (values: readonly string[]) => uniqueOptions(values).map((value) => html`<option value="${value}"></option>`);

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
    if (!state.auth?.authenticated) {
      state.items = [];
      state.contexts = [];
      state.modelCatalog = null;
      state.members = [];
      state.providerSecrets = [];
      state.apiKeys = [];
      return;
    }
    const [items, contexts, catalog] = await Promise.allSettled([listItems(), listContexts(), getModelCatalog()]);
    state.items = items.status === "fulfilled" ? [...items.value].sort((a, b) => new Date(b.createdAt).getTime() - new Date(a.createdAt).getTime()) : [];
    state.contexts = contexts.status === "fulfilled" ? [...contexts.value].sort((a, b) => Number(b.isDefault) - Number(a.isDefault) || (a.name || "").localeCompare(b.name || "")) : [];
    state.modelCatalog = catalog.status === "fulfilled" ? catalog.value : null;
    state.dataError = [items, contexts, catalog].some((result) => result.status === "rejected") ? "Some workspace data could not be loaded." : "";
    await refreshMembers();
  }

  async function refreshContextMetrics() {
    const metrics = await Promise.all(state.contexts.map((ctx) => getContextMetrics(ctx.id.toString()).catch(() => ({
      context_id: Number(ctx.id),
      total_runs: 0,
      corrected_runs: 0,
      avg_levenshtein_distance: 0,
      avg_edit_count: 0,
      avg_box_change_score: 0,
    } satisfies ContextMetrics))));
    state.contextMetrics = new Map(metrics.map((metric) => [`${metric.context_id}`, metric]));
  }

  async function refreshSettingsData() {
    if (!state.auth?.authenticated) return;
    const [secrets, keys] = await Promise.all([
      listProviderSecrets().catch(() => [] as ProviderSecretRecord[]),
      canAdminWorkspace(state) ? listAPIKeys().catch(() => [] as APIKeyRecord[]) : Promise.resolve([] as APIKeyRecord[]),
    ]);
    state.providerSecrets = secrets;
    state.apiKeys = keys;
  }

  async function ensurePanelData(panel: ShellPanel) {
    if (panel === "contexts") await refreshContextMetrics();
    if (panel === "settings") await refreshSettingsData();
  }

  async function openPanel(panel: ShellPanel) {
    state.panel = panel;
    if (panel) await ensurePanelData(panel);
    renderAll();
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
    const items = filteredItems();
    setHTML(sidebar, state.auth?.authenticated ? html`
      <label class="text-xs uppercase text-muted-foreground" for="sidebar-workspace-select">Workspace</label>
      <select id="sidebar-workspace-select" class="${input} mt-2">${state.workspaces.map((entry) => {
        const id = uint64ToString(entry.workspace?.id ?? 0n);
        return html`<option value="${id}"${id === state.currentWorkspaceId ? " selected" : ""}>${entry.workspace?.name || "Workspace"}</option>`;
      })}</select>
      <button id="sidebar-create-workspace" class="${buttons} mt-3 w-full justify-center" type="button">New workspace</button>
      <button id="sidebar-new-annotation" class="${primary} mt-4 w-full justify-center" type="button">New annotation</button>
      <input id="sidebar-search" value="${state.search}" placeholder="Search annotations" class="${input} mt-4" />
      <div class="mt-5 grid gap-2">${items.map((item) => html`<article class="rounded-md border px-3 py-2"><a href="${editorHrefForItem(item)}" class="block truncate text-sm font-semibold">${item.name || item.id}</a>${renderItemActions(item)}</article>`)}</div>
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
    document.getElementById("sidebar-search")?.addEventListener("input", (event) => {
      state.search = (event.target as HTMLInputElement).value;
      renderSidebar();
      bindSharedItemActions();
    });
  }

  function renderLibrary() {
    if (!state.auth?.authenticated) {
      setHTML(content, html`<section class="mx-auto max-w-2xl py-16 text-center"><h2 class="text-4xl font-semibold">Create annotations with your workspace</h2><a href="${loginHref(state.auth)}" class="${primary} mt-8">Sign in with Google</a></section>`);
      return;
    }
    const recent = filteredItems().slice(0, 6);
    setHTML(content, html`
      <section class="mx-auto max-w-5xl">
        ${state.dataError ? html`<div class="mb-4 rounded-md border border-destructive/30 bg-destructive/10 px-4 py-3 text-sm text-destructive">${state.dataError}</div>` : ""}
        <div class="${card} mx-auto max-w-3xl">
          <div class="flex flex-wrap justify-center gap-2">${(["url", "single", "multi", "manifest"] as LibraryTab[]).map((tab) => html`<button data-library-tab="${tab}" class="${tab === state.activeLibraryTab ? primary : buttons}" type="button">${tab}</button>`)}</div>
          <div class="mt-5 flex flex-wrap items-center gap-3"><label for="library-context-select" class="text-sm">Context</label><select id="library-context-select" class="${input} max-w-xs"><option value="0">Default</option>${contextOptions(state.contexts)}</select></div>
          <form id="library-form-url" class="${state.activeLibraryTab === "url" ? "mt-5 grid gap-3" : "hidden"}"><input id="library-image-url" type="url" required class="${input}" placeholder="https://example.org/image.jpg" /><button class="${primary}" type="submit">Process URL</button><p id="library-url-status" class="text-sm text-muted-foreground"></p></form>
          <form id="library-form-single" class="${state.activeLibraryTab === "single" ? "mt-5 grid gap-3" : "hidden"}"><input id="library-single-file" type="file" class="${input}" /><button class="${primary}" type="submit">Upload and process</button><p id="library-single-status" class="text-sm text-muted-foreground"></p></form>
          <form id="library-form-multi" class="${state.activeLibraryTab === "multi" ? "mt-5 grid gap-3" : "hidden"}"><input id="library-multi-files" type="file" multiple class="${input}" /><button class="${primary}" type="submit">Upload batch</button><p id="library-multi-status" class="text-sm text-muted-foreground"></p></form>
          <form id="library-form-manifest" class="${state.activeLibraryTab === "manifest" ? "mt-5 grid gap-3" : "hidden"}"><input id="library-manifest-url" type="url" required class="${input}" placeholder="https://example.org/manifest.json" /><label class="text-sm"><input type="radio" name="library-manifest-mode" value="import" checked /> Edit imported hOCR directly</label><label class="text-sm"><input type="radio" name="library-manifest-mode" value="reprocess" /> Reprocess imported pages</label><button class="${primary}" type="submit">Ingest manifest</button><p id="library-manifest-status" class="text-sm text-muted-foreground"></p></form>
        </div>
        <div class="mt-8 flex items-center justify-between"><h2 class="text-2xl font-semibold">Recent annotations</h2><button id="library-refresh" class="${buttons}" type="button">Refresh</button></div>
        <div class="mt-5 grid gap-3 lg:grid-cols-2">${recent.length === 0 ? html`<div class="${card} text-sm text-muted-foreground">No matching annotations yet.</div>` : recent.map(renderItemCard)}</div>
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
        const result = await processImageUpload(file, selectedContextId());
        const itemImageId = uint64ToString(result.itemImageId);
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
      try {
        await uploadItemImages(files);
        await refreshWorkspaceScopedData();
        renderAll();
      } catch (error) { status.textContent = `Error: ${String(error)}`; }
    });
    document.getElementById("library-form-manifest")?.addEventListener("submit", async (event) => {
      event.preventDefault();
      const status = document.getElementById("library-manifest-status") as HTMLParagraphElement;
      const manifestUrl = (document.getElementById("library-manifest-url") as HTMLInputElement).value.trim();
      const mode = (document.querySelector('input[name="library-manifest-mode"]:checked') as HTMLInputElement | null)?.value ?? "import";
      if (!manifestUrl) return;
      try {
        const result = await createItemFromManifest(manifestUrl);
        if (mode === "reprocess") for (const image of result.item.images ?? []) await reprocessItemImage(uint64ToString(image.id), Number(selectedContextId()));
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
    const transcriptionModels = uniqueOptions([
      ...(state.modelCatalog?.ollamaModels ?? []),
      ...(state.modelCatalog?.krakenModels ?? []),
      ...(state.modelCatalog?.openaiModels ?? []),
      ...(state.modelCatalog?.geminiModels ?? []),
    ]);
    const segmentationModels = uniqueOptions(state.modelCatalog?.segmentationModels ?? []);
    const defaultSegmentationModel = segmentationModels[0] ?? "tesseract";
    setHTML(drawer, html`
      <div class="flex items-start justify-between gap-4"><h2 class="text-2xl font-semibold">OCR context library</h2><button id="shell-panel-close" class="${buttons}" type="button">Close</button></div>
      ${state.auth?.authenticated ? html`
        <form id="contexts-create-form" class="${card} mt-5 grid gap-3"><input id="contexts-name" class="${input}" placeholder="Name" /><input id="contexts-provider" class="${input}" list="contexts-provider-options" value="ollama" /><datalist id="contexts-provider-options">${datalistOptions(["ollama", "kraken", "openai", "gemini"])}</datalist><input id="contexts-model" class="${input}" list="contexts-transcription-models" placeholder="Transcription model" /><datalist id="contexts-transcription-models">${datalistOptions(transcriptionModels)}</datalist><input id="contexts-segmentation" class="${input}" list="contexts-segmentation-models" value="${defaultSegmentationModel}" /><datalist id="contexts-segmentation-models">${datalistOptions(segmentationModels)}</datalist><input id="contexts-base-url" class="${input}" placeholder="https://service.run.app" /><input id="contexts-audience" class="${input}" placeholder="Optional audience" /><textarea id="contexts-description" class="${input}" placeholder="Description"></textarea><textarea id="contexts-system-prompt" class="${input}" placeholder="System prompt"></textarea><label><input id="contexts-default" type="checkbox" /> Set as default</label><button class="${primary}" type="submit">Create context</button><p id="contexts-status" class="text-sm text-muted-foreground"></p></form>
        <div class="mt-5 grid gap-3">${state.contexts.map((ctx, index) => html`<article class="${card}"><h3 class="font-semibold">${ctx.name}</h3><p class="text-sm text-muted-foreground">${ctx.transcriptionProvider} · ${ctx.transcriptionModel || "default"}</p><p class="mt-2 text-xs text-muted-foreground">Runs ${metrics[index]?.total_runs ?? 0}</p></article>`)}</div>
      ` : html`<a href="${loginHref(state.auth)}" class="${primary} mt-5">Sign in with Google</a>`}
    `);
    document.getElementById("shell-panel-close")?.addEventListener("click", () => void openPanel(null));
    document.getElementById("contexts-create-form")?.addEventListener("submit", async (event) => {
      event.preventDefault();
      const name = (document.getElementById("contexts-name") as HTMLInputElement).value.trim();
      const status = document.getElementById("contexts-status") as HTMLParagraphElement;
      if (!name) { status.textContent = "Name is required."; return; }
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
    });
  }

  function renderSettingsPanel() {
    const workspace = currentWorkspace(state);
    const admin = canAdminWorkspace(state);
    setHTML(drawer, html`
      <div class="flex items-start justify-between gap-4"><h2 class="text-2xl font-semibold">Workspace and account settings</h2><button id="shell-panel-close" class="${buttons}" type="button">Close</button></div>
      ${state.auth?.authenticated ? html`
        <section class="${card} mt-5"><div class="flex items-center justify-between gap-3"><p>${state.auth.user?.email}</p><button id="settings-logout" class="${buttons}" type="button">Logout</button></div></section>
        <section class="${card} mt-4"><h3 class="text-xl font-semibold">${workspace?.name || "Workspace"}</h3><form id="settings-rename-workspace" class="mt-3 flex gap-2"><input id="settings-workspace-name" class="${input}" value="${workspace?.name || ""}"${admin && !workspace?.isPersonal ? "" : " disabled"} /><button class="${primary}" type="submit"${admin && !workspace?.isPersonal ? "" : " disabled"}>Rename</button></form><form id="settings-create-workspace" class="mt-3 flex gap-2"><input id="settings-new-workspace-name" class="${input}" placeholder="Create a new workspace" /><button class="${buttons}" type="submit">Create workspace</button></form><p id="settings-workspace-status" class="mt-2 text-sm text-muted-foreground"></p></section>
        <section class="${card} mt-4"><h3 class="text-xl font-semibold">Workspace members</h3><div class="mt-3 grid gap-2">${state.members.map((member) => html`<div class="flex flex-wrap items-center justify-between gap-2 border-t py-2"><span>${member.user?.email || member.user?.name}</span><select data-member-role="${uint64ToString(member.user?.id ?? 0n)}" class="${input} max-w-[140px]"${admin && !workspace?.isPersonal ? "" : " disabled"}>${["admin", "write", "create", "read"].map((role) => html`<option value="${role}"${member.role === role ? " selected" : ""}>${role}</option>`)}</select><button data-member-save="${uint64ToString(member.user?.id ?? 0n)}" class="${buttons}" type="button"${admin && !workspace?.isPersonal ? "" : " disabled"}>Save</button><button data-member-remove="${uint64ToString(member.user?.id ?? 0n)}" class="${buttons}" type="button"${admin && !workspace?.isPersonal ? "" : " disabled"}>Remove</button></div>`)}</div><form id="settings-add-member" class="mt-3 grid gap-2 sm:grid-cols-[1fr_140px_auto]"><input id="settings-member-email" class="${input}" placeholder="user@example.edu"${admin && !workspace?.isPersonal ? "" : " disabled"} /><select id="settings-member-role" class="${input}"${admin && !workspace?.isPersonal ? "" : " disabled"}><option>read</option><option>create</option><option>write</option><option>admin</option></select><button class="${primary}" type="submit"${admin && !workspace?.isPersonal ? "" : " disabled"}>Add member</button></form><p id="settings-members-status" class="mt-2 text-sm text-muted-foreground"></p></section>
        <section class="${card} mt-4"><h3 class="text-xl font-semibold">Provider secrets</h3><form id="settings-provider-secret-form" class="mt-3 grid gap-2"><select id="settings-provider-secret-provider" class="${input}"${canWriteWorkspace(state) ? "" : " disabled"}><option value="gemini">gemini</option></select><input id="settings-provider-secret-name" class="${input}" placeholder="Name"${canWriteWorkspace(state) ? "" : " disabled"} /><select id="settings-provider-secret-scope" class="${input}"${canWriteWorkspace(state) ? "" : " disabled"}><option value="user">Personal</option><option value="workspace">Workspace</option></select><input id="settings-provider-secret-api-key" type="password" class="${input}" placeholder="API key"${canWriteWorkspace(state) ? "" : " disabled"} /><button class="${primary}" type="submit"${canWriteWorkspace(state) ? "" : " disabled"}>Save provider key</button><p id="settings-provider-secret-status" class="text-sm text-muted-foreground"></p></form><div class="mt-3">${renderProviderSecrets(state.providerSecrets)}</div></section>
        <section class="${card} mt-4"><h3 class="text-xl font-semibold">Workspace tokens</h3>${admin ? html`<form id="settings-api-key-form" class="mt-3 grid gap-2 sm:grid-cols-[1fr_140px_auto]"><input id="settings-api-key-name" class="${input}" placeholder="Token name" /><select id="settings-api-key-role" class="${input}"><option>read</option><option>create</option><option>write</option><option>admin</option></select><button class="${primary}" type="submit">Create key</button></form>` : ""}<p id="settings-api-key-status" class="mt-2 text-sm text-muted-foreground"></p><div class="mt-3">${admin ? renderAPIKeys(state.apiKeys) : ""}</div></section>
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
    modal.classList.remove("hidden");
    modal.classList.add("flex");
    modalTitle.textContent = `${item.name || item.id} (${item.id})`;
    setHTML(modalBody, html`<p class="text-muted-foreground">Loading logs...</p>`);
    const audits = await listItemProviderCallAudits(item.id, 100).catch(() => []);
    setHTML(modalBody, audits.length === 0 ? html`<p class="text-muted-foreground">No provider logs recorded for this item yet.</p>` : html`<div class="grid gap-3">${audits.map((audit) => html`<article class="${card}"><p>${audit.provider} ${audit.model} (${audit.operation})</p><p class="text-xs text-muted-foreground">${formatDateTime(audit.createdAt)}</p>${audit.errorMessage ? html`<p class="text-destructive">${audit.errorMessage}</p>` : ""}</article>`)}</div>`);
  }

  function bindSharedItemActions() {
    app.querySelectorAll<HTMLButtonElement>("[data-item-logs]").forEach((button) => button.addEventListener("click", () => void openLogsModal(button.dataset.itemLogs ?? "")));
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
      drawer.replaceChildren();
      return;
    }
    backdrop.classList.remove("hidden");
    drawer.classList.remove("translate-x-full");
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
  }

  document.getElementById("shell-modal-close")?.addEventListener("click", () => { modal.classList.add("hidden"); modal.classList.remove("flex"); });
  modal.addEventListener("click", (event) => { if (event.target === modal) modal.classList.add("hidden"); });
  backdrop.addEventListener("click", () => void openPanel(null));

  await refreshAuth();
  await refreshWorkspaces();
  await refreshWorkspaceScopedData();
  if (state.panel) await ensurePanelData(state.panel);
  renderAll();

  const subscription = state.auth?.authenticated ? subscribeToEvents({ types: ["dev.scribe.transcription.task.completed", "dev.scribe.transcription.completed", "dev.scribe.transcription.failed", "dev.scribe.annotations.created", "dev.scribe.annotations.published"] }, () => {
    void (async () => {
      await refreshWorkspaceScopedData();
      if (state.panel) await ensurePanelData(state.panel);
      renderAll();
    })();
  }) : null;
  window.addEventListener("pagehide", () => subscription?.close(), { once: true });
}
