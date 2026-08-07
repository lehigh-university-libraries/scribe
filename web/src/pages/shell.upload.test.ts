// @vitest-environment happy-dom

import { beforeEach, describe, expect, it, vi } from "vitest";

import { getAnnotationPage } from "../api/annotations";
import { createAPIKey, createProviderSecret, deleteAPIKey, deleteProviderSecret, getAuthMe, listAPIKeys, listProviderSecrets, logout } from "../api/auth";
import { createContext, getContextMetrics, getModelCatalog, listContexts } from "../api/context";
import { subscribeToEvents } from "../api/events";
import { deleteItem, getItemExportSnapshot, importManifest, listItems, listItemProviderCallAudits, prepareItemExport, UploadBatchCancellationError, uploadItemImages, type UploadBatchProgress } from "../api/items";
import { processImageURL, reprocessItemImage } from "../api/processing";
import { addWorkspaceMember, deleteWorkspaceMember, listWorkspaceMembers, listWorkspaces, updateWorkspaceMember } from "../api/workspaces";
import { AnnotationExportFormat } from "../proto/scribe/v1/annotation_pb";
import { UploadBatchFileStatus } from "../proto/scribe/v1/item_pb";
import { renderShell } from "./shell";

vi.mock("../api/annotations", () => ({
  getAnnotationPage: vi.fn(),
}));

vi.mock("../api/auth", () => ({
  createAPIKey: vi.fn(),
  createProviderSecret: vi.fn(),
  deleteAPIKey: vi.fn(),
  deleteProviderSecret: vi.fn(),
  getAuthMe: vi.fn(),
  listAPIKeys: vi.fn(),
  listProviderSecrets: vi.fn(),
  logout: vi.fn(),
}));

vi.mock("../api/context", () => ({
  createContext: vi.fn(),
  getContextMetrics: vi.fn(),
  getModelCatalog: vi.fn(),
  listContexts: vi.fn(),
}));

vi.mock("../api/events", () => ({
  subscribeToEvents: vi.fn(),
}));

vi.mock("../api/items", () => ({
  UploadBatchCancellationError: class UploadBatchCancellationError extends Error {
    constructor() {
      super("Upload stopped locally, but server cancellation could not be confirmed. Select the same files to resume and verify the batch state.");
      this.name = "UploadBatchCancellationError";
    }
  },
  importManifest: vi.fn(),
  deleteItem: vi.fn(),
  getItemExportSnapshot: vi.fn(),
  listItemProviderCallAudits: vi.fn(),
  listItems: vi.fn(),
  prepareItemExport: vi.fn(),
  uploadItemImages: vi.fn(),
}));

vi.mock("../api/processing", () => ({
  processImageURL: vi.fn(),
  reprocessItemImage: vi.fn(),
}));

vi.mock("../api/workspaces", () => ({
  addWorkspaceMember: vi.fn(),
  createWorkspace: vi.fn(),
  deleteWorkspaceMember: vi.fn(),
  listWorkspaceMembers: vi.fn(),
  listWorkspaces: vi.fn(),
  updateWorkspace: vi.fn(),
  updateWorkspaceMember: vi.fn(),
}));

const authWorkspace = { id: 7n, name: "Manuscripts", role: "admin" };
const workspace = { id: 7n, name: "Manuscripts", role: "admin", isPersonal: false };

async function waitFor(assertion: () => void): Promise<void> {
  const deadline = Date.now() + 1000;
  let lastError: unknown;
  while (Date.now() < deadline) {
    try {
      assertion();
      return;
    } catch (error) {
      lastError = error;
      await new Promise((resolve) => window.setTimeout(resolve, 10));
    }
  }
  throw lastError;
}

type TestModelCatalog = Awaited<ReturnType<typeof getModelCatalog>>;
type TestItemPage = Awaited<ReturnType<typeof listItems>>;

function itemSummary(
  id: string,
  name: string,
  sourceType: "upload" | "manifest" = "upload",
  createdAt = "2026-05-13T00:00:00Z",
): TestItemPage["items"][number] {
  return {
    id,
    name,
    sourceType,
    imageCount: 0n,
    createdAt,
    updatedAt: createdAt,
  } as TestItemPage["items"][number];
}

async function setupShell(
  view: "library" | "contexts" | "settings" = "library",
  eventClose = vi.fn(),
  catalog?: TestModelCatalog,
  itemPage: TestItemPage = { items: [], nextPageToken: "" },
): Promise<HTMLElement> {
  document.body.innerHTML = `<main id="app"></main>`;
  window.history.replaceState(null, "", `/${view}`);
  window.localStorage.clear();

  vi.mocked(getAuthMe).mockResolvedValue({
    authenticated: true,
    authType: "session",
    loginUrl: "/auth/login",
    logoutUrl: "/logout",
    user: { id: 12n, email: "user@example.test", name: "User", pictureUrl: "", isAdmin: false, defaultWorkspaceId: 7n },
    workspace: authWorkspace,
  } as never);
  vi.mocked(listWorkspaces).mockResolvedValue([{ workspace, role: "admin" }] as never);
  vi.mocked(listItems).mockResolvedValue(itemPage);
  vi.mocked(listContexts).mockResolvedValue([]);
  vi.mocked(getModelCatalog).mockResolvedValue(catalog ?? {
    transcriptionProviders: [
      { id: "ollama", label: "Ollama", models: [{ id: "glm-ocr:bf16", label: "glm-ocr:bf16", isDefault: true, supportsTemperature: true }], supportsSystemPrompt: true },
      { id: "gemini", label: "Google Gemini", models: [{ id: "gemini-3.5-flash", label: "gemini-3.5-flash", isDefault: true, supportsTemperature: false }], requiresApiKey: true, supportsSystemPrompt: true },
    ],
    segmentationModels: [{ id: "kraken", label: "Kraken", isDefault: true }],
  } as never);
  vi.mocked(listWorkspaceMembers).mockResolvedValue({ workspace, members: [] } as never);
  vi.mocked(getContextMetrics).mockResolvedValue({
    contextId: 0n,
    totalRuns: 0n,
    correctedRuns: 0n,
    avgLevenshteinDistance: 0,
  } as never);
  vi.mocked(listProviderSecrets).mockResolvedValue([]);
  vi.mocked(listAPIKeys).mockResolvedValue([]);
  vi.mocked(listItemProviderCallAudits).mockResolvedValue([]);
  vi.mocked(subscribeToEvents).mockReturnValue({ close: eventClose });
  vi.mocked(logout).mockResolvedValue();
  const app = document.getElementById("app");
  if (!app) throw new Error("missing app root");
  await renderShell(app, view);
  return app;
}

describe("annotation upload actions", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("loads the annotation library incrementally with the server continuation token", async () => {
    await setupShell("library", vi.fn(), undefined, {
      items: [itemSummary("item-new", "Newest")],
      nextPageToken: "next-page",
    });
    vi.mocked(listItems).mockResolvedValueOnce({
      items: [itemSummary("item-old", "Older", "manifest", "2026-05-12T00:00:00Z")],
      nextPageToken: "",
    });

    expect(listItems).toHaveBeenCalledWith(expect.objectContaining({ pageSize: 50 }));
    expect(document.body.textContent).toContain("Newest");
    document.getElementById("library-load-more")?.click();

    await waitFor(() => {
      expect(listItems).toHaveBeenLastCalledWith(expect.objectContaining({ pageSize: 50, pageToken: "next-page" }));
      expect(document.body.textContent).toContain("Older");
      expect(document.getElementById("library-load-more")).toBeNull();
    });
  });

  it("prepares item downloads from the complete canonical revision vector", async () => {
    const summary = {
      ...itemSummary("item-export", "Exportable item"),
      imageCount: 2n,
      previewImage: { id: 41n },
    } as TestItemPage["items"][number];
    await setupShell("library", vi.fn(), undefined, { items: [summary], nextPageToken: "" });
    vi.mocked(getItemExportSnapshot).mockResolvedValue({
      item: {
        id: "item-export",
        images: [
          { id: 41n, sequence: 1 },
          { id: 42n, sequence: 2 },
        ],
      },
      expectedRevisions: [
        { itemImageId: 41n, revision: 7n },
        { itemImageId: 42n, revision: 11n },
      ],
    } as never);
    vi.mocked(prepareItemExport).mockResolvedValue({
      downloadUrl: "/v1/item-exports/signed-token",
      filename: "item-export.page.xml",
      mediaType: "application/vnd.prima.page+xml",
    } as never);

    expect(document.querySelector('a[href*="/v1/items/"][href*="export"]')).toBeNull();
    const pageExport = Array.from(document.querySelectorAll<HTMLButtonElement>('[data-item-export="item-export"]'))
      .find((button) => button.dataset.itemExportFormat === `${AnnotationExportFormat.PAGE_XML}`);
    expect(pageExport).toBeTruthy();
    pageExport!.click();

    await waitFor(() => {
      expect(getItemExportSnapshot).toHaveBeenCalledWith("item-export");
      expect(prepareItemExport).toHaveBeenCalledWith(
        "item-export",
        AnnotationExportFormat.PAGE_XML,
        [
          { itemImageId: 41n, revision: 7n },
          { itemImageId: 42n, revision: 11n },
        ],
      );
      expect(window.location.pathname).toBe("/v1/item-exports/signed-token");
      expect(new URLSearchParams(window.location.search).get("workspace_id")).toBe("7");
    });
  });

  it("disables duplicate item export actions while preparing and reports failures", async () => {
    const summary = {
      ...itemSummary("item-busy", "Busy item"),
      imageCount: 1n,
      previewImage: { id: 51n },
    } as TestItemPage["items"][number];
    let rejectGetItemExportSnapshot: ((reason: Error) => void) | undefined;
    vi.mocked(getItemExportSnapshot).mockImplementation(() => new Promise((_resolve, reject) => {
      rejectGetItemExportSnapshot = reject;
    }));
    await setupShell("library", vi.fn(), undefined, { items: [summary], nextPageToken: "" });

    document.querySelector<HTMLButtonElement>('[data-item-export="item-busy"]')?.click();
    await waitFor(() => {
      const exportButtons = Array.from(document.querySelectorAll<HTMLButtonElement>('[data-item-export="item-busy"]'));
      expect(exportButtons.length).toBeGreaterThan(1);
      expect(exportButtons.every((button) => button.disabled)).toBe(true);
      expect(exportButtons.some((button) => button.textContent === "Preparing…")).toBe(true);
    });

    rejectGetItemExportSnapshot?.(new Error("canonical pages could not be loaded"));
    await waitFor(() => {
      expect(document.body.textContent).toContain("Export failed: canonical pages could not be loaded");
      const exportButtons = Array.from(document.querySelectorAll<HTMLButtonElement>('[data-item-export="item-busy"]'));
      expect(exportButtons.every((button) => !button.disabled)).toBe(true);
      expect(prepareItemExport).not.toHaveBeenCalled();
    });
  });

  it.each([
    ["homepage", "#shell-content"],
    ["sidebar", "#shell-sidebar"],
  ])("keeps an item when deletion is dismissed from the %s", async (_location, rootSelector) => {
    const summary = itemSummary("item-keep", "Keep this item");
    const confirm = vi.fn(() => false);
    Object.defineProperty(window, "confirm", { configurable: true, value: confirm });
    await setupShell("library", vi.fn(), undefined, { items: [summary], nextPageToken: "" });

    document.querySelector<HTMLButtonElement>(`${rootSelector} [data-item-delete="item-keep"]`)?.click();

    expect(confirm).toHaveBeenCalledWith('Delete item "item-keep"?');
    expect(deleteItem).not.toHaveBeenCalled();
    expect(document.querySelector(`${rootSelector} [data-item-delete="item-keep"]`)).toBeTruthy();
  });

  it.each([
    ["homepage", "#shell-content"],
    ["sidebar", "#shell-sidebar"],
  ])("deletes and removes an item selected from the %s", async (_location, rootSelector) => {
    const summary = itemSummary("item-delete", "Delete this item");
    Object.defineProperty(window, "confirm", { configurable: true, value: vi.fn(() => true) });
    vi.mocked(deleteItem).mockResolvedValue({} as never);
    await setupShell("library", vi.fn(), undefined, { items: [summary], nextPageToken: "" });
    vi.mocked(listItems).mockResolvedValueOnce({ items: [], nextPageToken: "" });

    document.querySelector<HTMLButtonElement>(`${rootSelector} [data-item-delete="item-delete"]`)?.click();

    await waitFor(() => {
      expect(deleteItem).toHaveBeenCalledOnce();
      expect(deleteItem).toHaveBeenCalledWith("item-delete");
      expect(document.querySelector('[data-item-delete="item-delete"]')).toBeNull();
    });
  });

  it("contains a sidebar delete failure and keeps the item retryable", async () => {
    const summary = itemSummary("item-delete-fails", "Delete failure");
    Object.defineProperty(window, "confirm", { configurable: true, value: vi.fn(() => true) });
    vi.mocked(deleteItem).mockRejectedValue(new Error("storage unavailable"));
    await setupShell("library", vi.fn(), undefined, { items: [summary], nextPageToken: "" });

    document.querySelector<HTMLButtonElement>('#shell-sidebar [data-item-delete="item-delete-fails"]')?.click();

    await waitFor(() => {
      const deleteButtons = Array.from(document.querySelectorAll<HTMLButtonElement>('[data-item-delete="item-delete-fails"]'));
      expect(deleteButtons).toHaveLength(2);
      expect(deleteButtons.every((button) => !button.disabled && button.textContent?.trim() === "Delete")).toBe(true);
      const alerts = Array.from(document.querySelectorAll<HTMLElement>('[role="alert"]'));
      expect(alerts.some((alert) => alert.textContent === "Delete failed: storage unavailable")).toBe(true);
    });
    expect(listItems).toHaveBeenCalledOnce();
  });

  it("submits one delete while duplicate homepage and sidebar actions are in flight", async () => {
    const summary = {
      ...itemSummary("item-delete-once", "Delete once"),
      imageCount: 1n,
      previewImage: { id: 61n },
    } as TestItemPage["items"][number];
    const confirm = vi.fn(() => true);
    Object.defineProperty(window, "confirm", { configurable: true, value: confirm });
    let resolveDelete: (() => void) | undefined;
    vi.mocked(deleteItem).mockImplementation(() => new Promise((resolve) => {
      resolveDelete = () => resolve({} as never);
    }));
    await setupShell("library", vi.fn(), undefined, { items: [summary], nextPageToken: "" });
    vi.mocked(listItems).mockResolvedValueOnce({ items: [], nextPageToken: "" });
    const homeDelete = document.querySelector<HTMLButtonElement>('#shell-content [data-item-delete="item-delete-once"]');
    const sidebarDelete = document.querySelector<HTMLButtonElement>('#shell-sidebar [data-item-delete="item-delete-once"]');
    expect(homeDelete).toBeTruthy();
    expect(sidebarDelete).toBeTruthy();

    homeDelete!.click();
    sidebarDelete!.click();

    expect(confirm).toHaveBeenCalledOnce();
    expect(deleteItem).toHaveBeenCalledOnce();
    const pendingButtons = Array.from(document.querySelectorAll<HTMLButtonElement>('[data-item-delete="item-delete-once"]'));
    expect(pendingButtons).toHaveLength(2);
    expect(pendingButtons.map((button) => ({
      ariaBusy: button.getAttribute("aria-busy"),
      disabled: button.disabled,
      text: button.textContent?.trim(),
    }))).toEqual([
      { ariaBusy: "true", disabled: true, text: "Deleting…" },
      { ariaBusy: "true", disabled: true, text: "Deleting…" },
    ]);
    const pendingExports = Array.from(document.querySelectorAll<HTMLButtonElement>('[data-item-export="item-delete-once"]'));
    expect(pendingExports.length).toBeGreaterThan(1);
    expect(pendingExports.every((button) => button.disabled)).toBe(true);
    pendingExports[0].dispatchEvent(new MouseEvent("click", { bubbles: true }));
    expect(getItemExportSnapshot).not.toHaveBeenCalled();

    resolveDelete?.();
    await waitFor(() => expect(document.querySelector('[data-item-delete="item-delete-once"]')).toBeNull());
  });

  it("searches the complete library through the server", async () => {
    await setupShell("library", vi.fn(), undefined, {
      items: [itemSummary("item-new", "Newest")],
      nextPageToken: "next-page",
    });
    vi.mocked(listItems).mockResolvedValueOnce({
      items: [itemSummary("item-old", "Older", "manifest", "2026-05-12T00:00:00Z")],
      nextPageToken: "",
    });

    const search = document.getElementById("sidebar-search") as HTMLInputElement;
    search.focus();
    search.value = "older";
    search.setSelectionRange(3, 3);
    search.dispatchEvent(new Event("input"));

    await waitFor(() => {
      expect(listItems).toHaveBeenLastCalledWith(expect.objectContaining({ pageSize: 50, query: "older" }));
      expect(document.body.textContent).toContain("Older");
      expect(document.body.textContent).not.toContain("Newest");
      expect(document.activeElement).toBe(document.getElementById("sidebar-search"));
      expect((document.getElementById("sidebar-search") as HTMLInputElement).selectionStart).toBe(3);
    });
  });

  it("aborts a stale server search when the query changes", async () => {
    await setupShell();
    let firstSignal: AbortSignal | undefined;
    vi.mocked(listItems)
      .mockImplementationOnce((options) => new Promise<TestItemPage>((_resolve, reject) => {
        if (!options) throw new Error("missing list options");
        firstSignal = options.signal;
        options.signal?.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")), { once: true });
      }))
      .mockResolvedValueOnce({ items: [itemSummary("item-second", "Second result")], nextPageToken: "" });
    const search = document.getElementById("sidebar-search") as HTMLInputElement;
    search.value = "first";
    search.dispatchEvent(new Event("input"));
    await waitFor(() => expect(listItems).toHaveBeenCalledWith(expect.objectContaining({ query: "first" })));

    const currentSearch = document.getElementById("sidebar-search") as HTMLInputElement;
    currentSearch.value = "second";
    currentSearch.dispatchEvent(new Event("input"));

    await waitFor(() => {
      expect(firstSignal?.aborted).toBe(true);
      expect(document.body.textContent).toContain("Second result");
    });
  });

  it("does not append a stale page after the workspace changes", async () => {
    await setupShell("library", vi.fn(), undefined, {
      items: [itemSummary("item-new", "First workspace")],
      nextPageToken: "workspace-seven-page-two",
    });
    let resolveOldPage: ((page: TestItemPage) => void) | undefined;
    vi.mocked(listItems)
      .mockImplementationOnce(() => new Promise<TestItemPage>((resolve) => { resolveOldPage = resolve; }))
      .mockResolvedValueOnce({
        items: [itemSummary("item-other", "Second workspace", "manifest", "2026-05-14T00:00:00Z")],
        nextPageToken: "",
      });

    document.getElementById("library-load-more")?.click();
    const workspaceSelect = document.getElementById("sidebar-workspace-select") as HTMLSelectElement;
    const otherWorkspace = document.createElement("option");
    otherWorkspace.textContent = "Other workspace";
    otherWorkspace.value = "8";
    workspaceSelect.add(otherWorkspace);
    workspaceSelect.value = "8";
    workspaceSelect.dispatchEvent(new Event("change"));

    await waitFor(() => expect(document.body.textContent).toContain("Second workspace"));
    resolveOldPage?.({
      items: [itemSummary("item-stale", "Stale first workspace page", "upload", "2026-05-12T00:00:00Z")],
      nextPageToken: "",
    });
    await new Promise((resolve) => window.setTimeout(resolve, 0));
    expect(document.body.textContent).not.toContain("Stale first workspace page");
  });

  it("starts URL image processing from the library form and opens the editor", async () => {
    await setupShell();
    vi.mocked(processImageURL).mockResolvedValue({ itemId: "url-item", itemImageId: 101n, transcriptionJobId: 501n } as never);

    const input = document.getElementById("library-image-url") as HTMLInputElement | null;
    const form = document.getElementById("library-form-url") as HTMLFormElement | null;
    expect(input).toBeTruthy();
    expect(form).toBeTruthy();

    input!.value = "https://example.test/page.jpg";
    form!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));

    await waitFor(() => {
      expect(processImageURL).toHaveBeenCalledWith("https://example.test/page.jpg", 0n);
      expect(window.location.href).toContain("/editor?itemImageId=101");
      const params = new URLSearchParams(window.location.search);
      expect(params.get("itemId")).toBe("url-item");
      expect(params.get("jobId")).toBe("501");
      expect(params.has("autoTranscribe")).toBe(false);
    });
  });

  it("auto-submits a selected image, reports progress, and hands the exact job to the editor", async () => {
    await setupShell();
    const file = new File(["image-bytes"], "page-one.jpg", { type: "image/jpeg" });
    let reportProgress: ((progress: UploadBatchProgress) => void) | undefined;
    let resolveUpload: ((result: Awaited<ReturnType<typeof uploadItemImages>>) => void) | undefined;
    vi.mocked(uploadItemImages).mockImplementation((_files, options) => {
      reportProgress = options?.onProgress;
      return new Promise((resolve) => { resolveUpload = resolve; });
    });

    document.querySelector<HTMLButtonElement>('[data-library-tab="single"]')?.click();
    const input = document.getElementById("library-single-file") as HTMLInputElement | null;
    const form = document.getElementById("library-form-single") as HTMLFormElement | null;
    expect(input).toBeTruthy();
    expect(form).toBeTruthy();

    Object.defineProperty(input, "files", { configurable: true, value: [file] });
    input!.dispatchEvent(new Event("change", { bubbles: true }));

    await waitFor(() => {
      expect(uploadItemImages).toHaveBeenCalledWith(
        [file],
        expect.objectContaining({ contextId: 0n, onProgress: expect.any(Function), signal: expect.any(AbortSignal) }),
      );
      expect(document.getElementById("shell-upload-dialog")?.getAttribute("aria-hidden")).toBe("false");
      expect(document.getElementById("shell-content")?.inert).toBe(true);
      expect(document.body.textContent).toContain("Preparing file");
    });

    reportProgress?.({ attempt: 1, completed: 0, filename: file.name, sequence: 1, status: "uploading", total: 1 });
    expect(document.getElementById("shell-upload-status")?.textContent).toBe("Uploading and processing 1/1…");

    reportProgress?.({ attempt: 1, completed: 1, filename: file.name, sequence: 1, status: "completed", total: 1 });
    expect(document.body.textContent).toContain("Uploaded 1/1. Starting automatic transcription");

    form!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
    input!.dispatchEvent(new Event("change", { bubbles: true }));
    expect(uploadItemImages).toHaveBeenCalledTimes(1);

    resolveUpload?.({
      item: { id: "upload-item" },
      batch: { files: [{ sequence: 1, status: UploadBatchFileStatus.COMPLETED, itemImageId: 202n, transcriptionJobId: 502n }] },
    } as never);
    await waitFor(() => {
      expect(window.location.pathname).toBe("/editor");
      const params = new URLSearchParams(window.location.search);
      expect(params.get("itemImageId")).toBe("202");
      expect(params.get("itemId")).toBe("upload-item");
      expect(params.get("jobId")).toBe("502");
      expect(params.has("autoTranscribe")).toBe(false);
    });
  });

  it("hands a durably completed upload to the editor when cancel races with the final response", async () => {
    await setupShell();
    const file = new File(["image-bytes"], "completed-while-canceling.jpg", { type: "image/jpeg" });
    let uploadSignal: AbortSignal | undefined;
    let resolveUpload: ((result: Awaited<ReturnType<typeof uploadItemImages>>) => void) | undefined;
    vi.mocked(uploadItemImages).mockImplementation((_files, options) => {
      uploadSignal = options?.signal;
      return new Promise((resolve) => { resolveUpload = resolve; });
    });

    document.querySelector<HTMLButtonElement>('[data-library-tab="single"]')?.click();
    const input = document.getElementById("library-single-file") as HTMLInputElement;
    Object.defineProperty(input, "files", { configurable: true, value: [file] });
    input.dispatchEvent(new Event("change", { bubbles: true }));
    await waitFor(() => expect(resolveUpload).toBeDefined());

    document.getElementById("shell-upload-cancel")?.click();
    expect(uploadSignal?.aborted).toBe(true);
    resolveUpload?.({
      item: { id: "durable-upload" },
      batch: { files: [{ sequence: 1, status: UploadBatchFileStatus.COMPLETED, itemImageId: 204n, transcriptionJobId: 504n }] },
    } as never);

    await waitFor(() => {
      expect(window.location.pathname).toBe("/editor");
      const params = new URLSearchParams(window.location.search);
      expect(params.get("itemImageId")).toBe("204");
      expect(params.get("itemId")).toBe("durable-upload");
      expect(params.get("jobId")).toBe("504");
      expect(params.get("workspace_id")).toBe("7");
    });
  });

  it.each([
    {
      label: "is not completed",
      uploaded: { sequence: 1, status: UploadBatchFileStatus.PROCESSING, itemImageId: 202n, transcriptionJobId: 502n },
      message: "selected file did not complete",
    },
    {
      label: "has no image identity",
      uploaded: { sequence: 1, status: UploadBatchFileStatus.COMPLETED, itemImageId: 0n, transcriptionJobId: 502n },
      message: "without an image identifier",
    },
    {
      label: "has no transcription job identity",
      uploaded: { sequence: 1, status: UploadBatchFileStatus.COMPLETED, itemImageId: 202n, transcriptionJobId: 0n },
      message: "without a transcription job identifier",
    },
  ])("does not hand an upload to the editor when the selected file $label", async ({ uploaded, message }) => {
    await setupShell();
    const file = new File(["image-bytes"], "incomplete.jpg", { type: "image/jpeg" });
    vi.mocked(uploadItemImages).mockResolvedValue({
      item: { id: "upload-item" },
      batch: { files: [uploaded] },
    } as never);

    document.querySelector<HTMLButtonElement>('[data-library-tab="single"]')?.click();
    const input = document.getElementById("library-single-file") as HTMLInputElement;
    Object.defineProperty(input, "files", { configurable: true, value: [file] });
    input.dispatchEvent(new Event("change", { bubbles: true }));

    await waitFor(() => {
      expect(document.getElementById("shell-upload-status")?.textContent).toContain(message);
      expect(document.getElementById("shell-upload-cancel")?.textContent).toBe("Close");
    });
    expect(window.location.pathname).toBe("/library");
  });

  it("does not navigate when a completed single upload resolves after pagehide", async () => {
    await setupShell();
    const file = new File(["image-bytes"], "late.jpg", { type: "image/jpeg" });
    let resolveUpload: ((result: Awaited<ReturnType<typeof uploadItemImages>>) => void) | undefined;
    vi.mocked(uploadItemImages).mockImplementation(() => new Promise((resolve) => {
      resolveUpload = resolve;
    }));

    document.querySelector<HTMLButtonElement>('[data-library-tab="single"]')?.click();
    const input = document.getElementById("library-single-file") as HTMLInputElement;
    Object.defineProperty(input, "files", { configurable: true, value: [file] });
    input.dispatchEvent(new Event("change", { bubbles: true }));
    await waitFor(() => expect(resolveUpload).toBeDefined());

    window.dispatchEvent(new Event("pagehide"));
    resolveUpload?.({
      item: { id: "late-upload" },
      batch: { files: [{ sequence: 1, status: UploadBatchFileStatus.COMPLETED, itemImageId: 203n, transcriptionJobId: 503n }] },
    } as never);
    await new Promise((resolve) => window.setTimeout(resolve, 0));

    expect(window.location.pathname).toBe("/library");
    expect(window.location.search).not.toContain("itemImageId=203");
  });

  it("keeps single-upload failures visible until the user closes the dialog", async () => {
    await setupShell();
    const file = new File(["image-bytes"], "broken.jpg", { type: "image/jpeg" });
    let rejectUpload: ((reason: Error) => void) | undefined;
    vi.mocked(uploadItemImages).mockImplementation(() => new Promise((_resolve, reject) => {
      rejectUpload = reject;
    }));

    document.querySelector<HTMLButtonElement>('[data-library-tab="single"]')?.click();
    const input = document.getElementById("library-single-file") as HTMLInputElement;
    Object.defineProperty(input, "files", { configurable: true, value: [file] });
    input.dispatchEvent(new Event("change", { bubbles: true }));
    await waitFor(() => expect(rejectUpload).toBeDefined());

    const subscriptionCall = vi.mocked(subscribeToEvents).mock.calls.at(-1);
    subscriptionCall?.[1]({
      specversion: "1.0",
      id: "upload-rerender",
      source: "/scribe",
      type: "dev.scribe.annotations.created",
      time: "2026-08-04T00:00:00Z",
    });
    await waitFor(() => expect(input.isConnected).toBe(false));
    rejectUpload?.(new Error("upload gateway unavailable"));

    await waitFor(() => {
      expect(document.getElementById("shell-upload-status")?.textContent).toContain("upload gateway unavailable");
      expect(document.getElementById("shell-upload-cancel")?.textContent).toBe("Close");
    });
    document.getElementById("shell-upload-cancel")?.click();
    expect(document.getElementById("shell-upload-dialog")?.getAttribute("aria-hidden")).toBe("true");
    expect(document.getElementById("shell-content")?.inert).toBe(false);
    expect(document.activeElement).toBe(document.getElementById("library-single-file"));
  });

  it("clears the file control on close so the same failed file can be selected again", async () => {
    await setupShell();
    const file = new File(["image-bytes"], "retry-same-file.jpg", { type: "image/jpeg" });
    vi.mocked(uploadItemImages).mockRejectedValue(new Error("upload gateway unavailable"));

    document.querySelector<HTMLButtonElement>('[data-library-tab="single"]')?.click();
    const input = document.getElementById("library-single-file") as HTMLInputElement;
    Object.defineProperty(input, "files", { configurable: true, value: [file] });
    Object.defineProperty(input, "value", {
      configurable: true,
      value: "C:\\fakepath\\retry-same-file.jpg",
      writable: true,
    });
    input.dispatchEvent(new Event("change", { bubbles: true }));

    await waitFor(() => {
      expect(uploadItemImages).toHaveBeenCalledTimes(1);
      expect(document.getElementById("shell-upload-cancel")?.textContent).toBe("Close");
    });
    document.getElementById("shell-upload-cancel")?.click();

    expect(input.isConnected).toBe(true);
    expect(input.value).toBe("");
    Object.defineProperty(input, "files", { configurable: true, value: [file] });
    input.value = "C:\\fakepath\\retry-same-file.jpg";
    input.dispatchEvent(new Event("change", { bubbles: true }));
    await waitFor(() => expect(uploadItemImages).toHaveBeenCalledTimes(2));
  });

  it("cancels an active single upload from the progress dialog", async () => {
    await setupShell();
    const file = new File(["image-bytes"], "cancel-me.jpg", { type: "image/jpeg" });
    let uploadSignal: AbortSignal | undefined;
    vi.mocked(uploadItemImages).mockImplementation((_files, options) => new Promise((_resolve, reject) => {
      uploadSignal = options?.signal;
      uploadSignal?.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")), { once: true });
    }));

    document.querySelector<HTMLButtonElement>('[data-library-tab="single"]')?.click();
    const input = document.getElementById("library-single-file") as HTMLInputElement;
    Object.defineProperty(input, "files", { configurable: true, value: [file] });
    input.dispatchEvent(new Event("change", { bubbles: true }));
    await waitFor(() => expect(uploadSignal).toBeDefined());

    document.getElementById("shell-upload-cancel")?.click();
    await waitFor(() => {
      expect(uploadSignal?.aborted).toBe(true);
      expect(document.getElementById("shell-upload-status")?.textContent).toBe("Upload canceled.");
      expect(document.getElementById("shell-upload-cancel")?.textContent).toBe("Close");
    });
  });

  it("reports when server-side cancellation cannot be confirmed", async () => {
    await setupShell();
    const file = new File(["image-bytes"], "cancel-uncertain.jpg", { type: "image/jpeg" });
    let uploadSignal: AbortSignal | undefined;
    vi.mocked(uploadItemImages).mockImplementation((_files, options) => new Promise((_resolve, reject) => {
      uploadSignal = options?.signal;
      uploadSignal?.addEventListener("abort", () => reject(new UploadBatchCancellationError()), { once: true });
    }));

    document.querySelector<HTMLButtonElement>('[data-library-tab="single"]')?.click();
    const input = document.getElementById("library-single-file") as HTMLInputElement;
    Object.defineProperty(input, "files", { configurable: true, value: [file] });
    input.dispatchEvent(new Event("change", { bubbles: true }));
    await waitFor(() => expect(uploadSignal).toBeDefined());

    document.getElementById("shell-upload-cancel")?.click();
    await waitFor(() => {
      expect(document.getElementById("shell-upload-status")?.textContent).toContain("server cancellation could not be confirmed");
      expect(document.getElementById("shell-upload-status")?.textContent).not.toBe("Upload canceled.");
    });
  });

  it("uploads a multi-image batch from the library form and refreshes the annotation list", async () => {
    await setupShell();
    const first = new File(["first"], "page-one.jpg", { type: "image/jpeg" });
    const second = new File(["second"], "page-two.jpg", { type: "image/jpeg" });
    vi.mocked(uploadItemImages).mockResolvedValue({ id: "batch-1", name: "Batch item", sourceType: "upload", images: [] } as never);
    vi.mocked(listItems).mockResolvedValue({
      items: [itemSummary("batch-1", "Batch item")],
      nextPageToken: "",
    });

    document.querySelector<HTMLButtonElement>('[data-library-tab="multi"]')?.click();
    const input = document.getElementById("library-multi-files") as HTMLInputElement | null;
    const form = document.getElementById("library-form-multi") as HTMLFormElement | null;
    expect(input).toBeTruthy();
    expect(form).toBeTruthy();

    Object.defineProperty(input, "files", { configurable: true, value: [first, second] });
    form!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));

    await waitFor(() => {
      expect(uploadItemImages).toHaveBeenCalledWith(
        [first, second],
        expect.objectContaining({ contextId: 0n, onProgress: expect.any(Function), signal: expect.anything() }),
      );
      expect(document.body.textContent).toContain("Batch item");
    });
  });

  it("reports the active multi-image upload without claiming it completed", async () => {
    await setupShell();
    const first = new File(["first"], "page-one.jpg", { type: "image/jpeg" });
    const second = new File(["second"], "page-two.jpg", { type: "image/jpeg" });
    let reportProgress: ((progress: UploadBatchProgress) => void) | undefined;
    vi.mocked(uploadItemImages).mockImplementation((_files, options) => {
      reportProgress = options?.onProgress;
      return new Promise(() => undefined);
    });

    document.querySelector<HTMLButtonElement>('[data-library-tab="multi"]')?.click();
    const input = document.getElementById("library-multi-files") as HTMLInputElement;
    Object.defineProperty(input, "files", { configurable: true, value: [first, second] });
    document.getElementById("library-form-multi")?.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
    await waitFor(() => expect(reportProgress).toBeDefined());

    reportProgress?.({ attempt: 1, completed: 0, filename: first.name, sequence: 1, status: "uploading", total: 2 });
    expect(document.getElementById("library-multi-status")?.textContent).toBe("Uploading and processing 1/2: page-one.jpg");
  });

  it("keeps multi-image cancellation connected across a library rerender", async () => {
    await setupShell();
    const first = new File(["first"], "page-one.jpg", { type: "image/jpeg" });
    let uploadSignal: AbortSignal | undefined;
    vi.mocked(uploadItemImages).mockImplementation((_files, options) => new Promise((_resolve, reject) => {
      uploadSignal = options?.signal;
      uploadSignal?.addEventListener(
        "abort",
        () => reject(new DOMException("aborted", "AbortError")),
        { once: true },
      );
    }));

    document.querySelector<HTMLButtonElement>('[data-library-tab="multi"]')?.click();
    const input = document.getElementById("library-multi-files") as HTMLInputElement;
    const form = document.getElementById("library-form-multi") as HTMLFormElement;
    const originalCancel = document.getElementById("library-multi-cancel");
    Object.defineProperty(input, "files", { configurable: true, value: [first] });
    form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
    await waitFor(() => expect(uploadSignal).toBeDefined());

    document.getElementById("library-refresh")?.click();
    await waitFor(() => {
      expect(document.getElementById("library-multi-cancel")).not.toBe(originalCancel);
    });
    document.getElementById("library-multi-cancel")?.click();

    await waitFor(() => expect(uploadSignal?.aborted).toBe(true));
  });

  it("imports an IIIF manifest from the library form and opens the first image in the editor", async () => {
    await setupShell();
    vi.mocked(importManifest).mockResolvedValue({
      item: { id: "manifest-1", name: "Manifest item", sourceType: "manifest", images: [{ id: 303n }] },
      firstItemImageId: "303",
    } as never);

    document.querySelector<HTMLButtonElement>('[data-library-tab="manifest"]')?.click();
    const input = document.getElementById("library-manifest-url") as HTMLInputElement | null;
    const form = document.getElementById("library-form-manifest") as HTMLFormElement | null;
    expect(input).toBeTruthy();
    expect(form).toBeTruthy();

		const contextSelect = document.getElementById("library-context-select") as HTMLSelectElement;
		const contextOption = document.createElement("option");
		contextOption.textContent = "Selected context";
		contextOption.value = "9";
		contextSelect.add(contextOption);
		contextSelect.value = "9";
    input!.value = "https://iiif.example.test/manifest.json";
    form!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));

    await waitFor(() => {
		expect(importManifest).toHaveBeenCalledWith("https://iiif.example.test/manifest.json", 9n);
      expect(window.location.href).toContain("/editor?itemImageId=303&itemId=manifest-1");
    });
  });

  it("reprocesses each imported manifest image against its loaded canonical revision", async () => {
    await setupShell();
    vi.mocked(importManifest).mockResolvedValue({
      item: {
        id: "manifest-1",
        name: "Manifest item",
        sourceType: "manifest",
        images: [{ id: 303n }, { id: 304n }],
      },
      firstItemImageId: "303",
    } as never);
    vi.mocked(getAnnotationPage)
      .mockResolvedValueOnce({ revision: "7" } as never)
      .mockResolvedValueOnce({ revision: "11" } as never);
    vi.mocked(reprocessItemImage).mockResolvedValue({} as never);

    document.querySelector<HTMLButtonElement>('[data-library-tab="manifest"]')?.click();
    const input = document.getElementById("library-manifest-url") as HTMLInputElement | null;
    const form = document.getElementById("library-form-manifest") as HTMLFormElement | null;
    const reprocessMode = document.querySelector<HTMLInputElement>('input[name="library-manifest-mode"][value="reprocess"]');
    expect(input).toBeTruthy();
    expect(form).toBeTruthy();
    expect(reprocessMode).toBeTruthy();

    input!.value = "https://iiif.example.test/manifest.json";
    reprocessMode!.click();
    form!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));

    await waitFor(() => {
      expect(getAnnotationPage).toHaveBeenNthCalledWith(1, "303");
      expect(getAnnotationPage).toHaveBeenNthCalledWith(2, "304");
      expect(reprocessItemImage).toHaveBeenNthCalledWith(1, "303", 0n, "7");
      expect(reprocessItemImage).toHaveBeenNthCalledWith(2, "304", 0n, "11");
      expect(window.location.href).toContain("/editor?itemImageId=303&itemId=manifest-1");
    });
  });

  it("closes the workspace event stream when the page is hidden", async () => {
    const close = vi.fn();

    await setupShell("library", close);
    window.dispatchEvent(new Event("pagehide"));

    expect(close).toHaveBeenCalledTimes(1);
  });

  it("coalesces workspace event bursts without refreshing for every transcription segment", async () => {
    await setupShell();
    const subscriptionCall = vi.mocked(subscribeToEvents).mock.calls.at(-1);
    expect(subscriptionCall).toBeTruthy();
    const [options, onEvent] = subscriptionCall!;
    expect(options.types).not.toContain("dev.scribe.transcription.task.completed");

    let resolveActiveRefresh: ((page: TestItemPage) => void) | undefined;
    vi.mocked(listItems).mockImplementationOnce(() => new Promise((resolve) => {
      resolveActiveRefresh = resolve;
    }));
    const event = {
      specversion: "1.0",
      id: "workspace-event",
      source: "/scribe",
      type: "dev.scribe.annotations.created",
      time: "2026-07-21T00:00:00Z",
    };
    for (let index = 0; index < 25; index++) onEvent(event);

    await waitFor(() => expect(listItems).toHaveBeenCalledTimes(2));
    await new Promise((resolve) => window.setTimeout(resolve, 20));
    expect(listItems).toHaveBeenCalledTimes(2);

    resolveActiveRefresh?.({ items: [], nextPageToken: "" });
    await waitFor(() => expect(listItems).toHaveBeenCalledTimes(3));
    await new Promise((resolve) => window.setTimeout(resolve, 20));
    expect(listItems).toHaveBeenCalledTimes(3);
  });

  it("creates contexts from the deployed model catalog", async () => {
    const catalog: TestModelCatalog = {
      transcriptionProviders: [
        { id: "ollama", label: "Ollama", models: [{ id: "glm-ocr:bf16", label: "glm-ocr:bf16", isDefault: true, supportsTemperature: true }, { id: "llava", label: "llava", isDefault: false, supportsTemperature: true }], supportsSystemPrompt: true },
        { id: "kraken", label: "Kraken", models: [{ id: "catmus-print-fondue-large.mlmodel", label: "CATMuS", isDefault: true }] },
        { id: "openai", label: "OpenAI", models: [{ id: "gpt-4.1", label: "GPT-4.1", isDefault: true, supportsTemperature: true }], requiresApiKey: true, supportsSystemPrompt: true },
      ],
      segmentationModels: [{ id: "kraken", label: "Kraken", isDefault: true }, { id: "kraken-manuscript", label: "Kraken manuscript", isDefault: false }],
    } as never;
    vi.mocked(createContext).mockResolvedValue({ id: 88n, name: "Catalog context" } as never);

    await setupShell("contexts", vi.fn(), catalog);

    await waitFor(() => {
      const modelOptions = Array.from(document.querySelectorAll("#contexts-model option")).map((option) => option.getAttribute("value"));
      const segmentationOptions = Array.from(document.querySelectorAll("#contexts-segmentation option")).map((option) => option.getAttribute("value"));
      expect(modelOptions).toEqual(["glm-ocr:bf16", "llava"]);
      expect(segmentationOptions).toEqual(["kraken", "kraken-manuscript"]);
    });

    (document.getElementById("contexts-name") as HTMLInputElement).value = "Catalog context";
    (document.getElementById("contexts-provider") as HTMLInputElement).value = "ollama";
    (document.getElementById("contexts-model") as HTMLInputElement).value = "glm-ocr:bf16";
    (document.getElementById("contexts-segmentation") as HTMLInputElement).value = "kraken-manuscript";
    document.getElementById("contexts-create-form")?.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));

    await waitFor(() => {
      expect(createContext).toHaveBeenCalledWith(expect.objectContaining({
        name: "Catalog context",
        transcriptionProvider: "ollama",
        transcriptionModel: "glm-ocr:bf16",
        segmentationModel: "kraken-manuscript",
      }));
    });
  });

  it("updates temperature controls from the selected model capability", async () => {
    const catalog: TestModelCatalog = {
      transcriptionProviders: [{
        id: "gemini",
        label: "Google Gemini",
        models: [
          { id: "gemini-3.5-flash", label: "Gemini 3.5 Flash", isDefault: true, supportsTemperature: false },
          { id: "gemini-2.5-flash", label: "Gemini 2.5 Flash", isDefault: false, supportsTemperature: true },
        ],
        requiresApiKey: true,
        supportsSystemPrompt: true,
      }],
      segmentationModels: [{ id: "kraken", label: "Kraken", isDefault: true }],
    } as never;

    await setupShell("contexts", vi.fn(), catalog);

    const model = document.getElementById("contexts-model") as HTMLSelectElement;
    const temperature = document.getElementById("contexts-temperature") as HTMLInputElement;
    const temperatureLabel = document.getElementById("contexts-temperature-label") as HTMLLabelElement;
    expect(model.value).toBe("gemini-3.5-flash");
    expect(temperature.disabled).toBe(true);
    expect(temperatureLabel.hidden).toBe(true);

    model.value = "gemini-2.5-flash";
    model.dispatchEvent(new Event("change"));
    expect(temperature.disabled).toBe(false);
    expect(temperatureLabel.hidden).toBe(false);

    temperature.value = "0.4";
    model.value = "gemini-3.5-flash";
    model.dispatchEvent(new Event("change"));
    expect(temperature.disabled).toBe(true);
    expect(temperature.value).toBe("");
  });

  it("shows a copyable one-time workspace token in an accessible modal and clears it on close", async () => {
    const oneTimeKey = "scribe_secret_once";
    const writeText = vi.fn().mockResolvedValue(undefined);
    vi.mocked(createAPIKey).mockResolvedValue({
      key: oneTimeKey,
      apiKey: { id: 56n, name: "New key", role: "write", scopes: [], keyPrefix: "sk_new", createdAt: "2026-06-01T00:00:00Z" },
    } as never);
    vi.mocked(deleteAPIKey).mockResolvedValue({} as never);
    Object.defineProperty(window, "alert", { configurable: true, value: vi.fn() });
    Object.defineProperty(window, "confirm", { configurable: true, value: vi.fn(() => true) });
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: { writeText },
    });

    await setupShell("settings");
    vi.mocked(listAPIKeys).mockResolvedValue([
      { id: 55n, name: "Existing key", role: "read", scopes: [], keyPrefix: "sk_live", createdAt: "2026-06-01T00:00:00Z" },
    ] as never);

    const name = document.getElementById("settings-api-key-name") as HTMLInputElement | null;
    const role = document.getElementById("settings-api-key-role") as HTMLSelectElement | null;
    const form = document.getElementById("settings-api-key-form") as HTMLFormElement | null;
    expect(name).toBeTruthy();
    expect(role).toBeTruthy();
    expect(form).toBeTruthy();

    name!.value = "New key";
    role!.value = "write";
    form!.querySelector<HTMLButtonElement>('button[type="submit"]')?.focus();
    form!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));

    await waitFor(() => {
      expect(createAPIKey).toHaveBeenCalledWith({ name: "New key", role: "write" });
      expect(window.alert).not.toHaveBeenCalled();
      expect(document.getElementById("shell-api-key-dialog")?.getAttribute("aria-hidden")).toBe("false");
      expect(document.getElementById("shell-api-key-heading")?.textContent).toBe("Copy workspace token");
      const token = document.getElementById("shell-api-key-value") as HTMLTextAreaElement | null;
      expect(token?.readOnly).toBe(true);
      expect(token?.value).toBe(oneTimeKey);
      expect(document.activeElement).toBe(token);
      expect(token?.selectionStart).toBe(0);
      expect(token?.selectionEnd).toBe(oneTimeKey.length);
    });

    document.getElementById("shell-api-key-copy")?.click();
    await waitFor(() => {
      expect(writeText).toHaveBeenCalledWith(oneTimeKey);
      expect(document.getElementById("shell-api-key-copy-status")?.textContent).toBe("Copied to clipboard.");
    });

    document.getElementById("shell-api-key-done")?.click();
    expect(document.getElementById("shell-api-key-dialog")?.getAttribute("aria-hidden")).toBe("true");
    expect((document.getElementById("shell-api-key-value") as HTMLTextAreaElement | null)?.value).toBe("");
    expect(document.body.textContent).not.toContain(oneTimeKey);
    expect(document.activeElement).toBe(document.getElementById("settings-api-key-name"));

    let deleteButton: HTMLButtonElement | null = null;
    await waitFor(() => {
      deleteButton = document.querySelector<HTMLButtonElement>("[data-api-key-delete=\"55\"]");
      expect(deleteButton).toBeTruthy();
    });
    deleteButton!.click();

    await waitFor(() => {
      expect(deleteAPIKey).toHaveBeenCalledWith("55");
    });
  });

  it("ignores a delayed clipboard failure after the workspace token modal closes", async () => {
    let rejectCopy: ((reason?: unknown) => void) | undefined;
    const pendingCopy = new Promise<void>((_resolve, reject) => {
      rejectCopy = reject;
    });
    const writeText = vi.fn().mockReturnValue(pendingCopy);
    vi.mocked(createAPIKey).mockResolvedValue({
      key: "scribe_secret_once",
      apiKey: { id: 57n, name: "Delayed copy", role: "write", scopes: [], keyPrefix: "sk_new", createdAt: "2026-06-01T00:00:00Z" },
    } as never);
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: { writeText },
    });

    await setupShell("settings");
    const name = document.getElementById("settings-api-key-name") as HTMLInputElement;
    const form = document.getElementById("settings-api-key-form") as HTMLFormElement;
    name.value = "Delayed copy";
    form.querySelector<HTMLButtonElement>('button[type="submit"]')?.focus();
    form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));

    await waitFor(() => {
      expect(document.getElementById("shell-api-key-dialog")?.getAttribute("aria-hidden")).toBe("false");
    });
    document.getElementById("shell-api-key-copy")?.click();
    expect(writeText).toHaveBeenCalledWith("scribe_secret_once");

    document.getElementById("shell-api-key-done")?.click();
    expect(document.activeElement?.id).toBe("settings-api-key-name");
    rejectCopy?.(new Error("clipboard denied"));

    await waitFor(() => {
      expect(document.getElementById("shell-api-key-dialog")?.getAttribute("aria-hidden")).toBe("true");
      expect((document.getElementById("shell-api-key-value") as HTMLTextAreaElement).value).toBe("");
      expect(document.getElementById("shell-api-key-copy-status")?.textContent).toBe("");
      expect(document.activeElement?.id).toBe("settings-api-key-name");
    });
  });

  it("fences delayed workspace token creation and reveals the token before the key list refresh", async () => {
    let resolveCreate: ((value: Awaited<ReturnType<typeof createAPIKey>>) => void) | undefined;
    let resolveKeyRefresh: ((value: Awaited<ReturnType<typeof listAPIKeys>>) => void) | undefined;
    const createResponse = new Promise<Awaited<ReturnType<typeof createAPIKey>>>((resolve) => {
      resolveCreate = resolve;
    });
    const keyRefresh = new Promise<Awaited<ReturnType<typeof listAPIKeys>>>((resolve) => {
      resolveKeyRefresh = resolve;
    });
    vi.mocked(createAPIKey).mockReturnValue(createResponse);

    await setupShell("settings");
    vi.mocked(listAPIKeys).mockReturnValueOnce(keyRefresh);

    const form = document.getElementById("settings-api-key-form") as HTMLFormElement;
    const name = document.getElementById("settings-api-key-name") as HTMLInputElement;
    const role = document.getElementById("settings-api-key-role") as HTMLSelectElement;
    const submit = form.querySelector<HTMLButtonElement>('button[type="submit"]') as HTMLButtonElement;
    name.value = "Race-proof key";
    role.value = "write";

    form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
    form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));

    expect(createAPIKey).toHaveBeenCalledTimes(1);
    expect(createAPIKey).toHaveBeenCalledWith({ name: "Race-proof key", role: "write" });
    expect(name.disabled).toBe(true);
    expect(role.disabled).toBe(true);
    expect(submit.disabled).toBe(true);
    expect(form.getAttribute("aria-busy")).toBe("true");

    resolveCreate?.({
      key: "scribe_first_response",
      apiKey: { id: 91n, name: "Race-proof key", role: "write", scopes: [], keyPrefix: "sk_race", createdAt: "2026-06-01T00:00:00Z" },
    } as never);

    await waitFor(() => {
      expect(document.getElementById("shell-api-key-dialog")?.getAttribute("aria-hidden")).toBe("false");
      expect((document.getElementById("shell-api-key-value") as HTMLTextAreaElement).value).toBe("scribe_first_response");
      expect(listAPIKeys).toHaveBeenCalledTimes(2);
      expect(name.disabled).toBe(true);
      expect(role.disabled).toBe(true);
      expect(submit.disabled).toBe(true);
      expect(form.getAttribute("aria-busy")).toBe("false");
    });

    const rerenderedForm = document.getElementById("settings-api-key-form") as HTMLFormElement;
    rerenderedForm.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
    expect(createAPIKey).toHaveBeenCalledTimes(1);
    expect((document.getElementById("shell-api-key-value") as HTMLTextAreaElement).value).toBe("scribe_first_response");

    resolveKeyRefresh?.([
      { id: 91n, name: "Race-proof key", role: "write", scopes: [], keyPrefix: "sk_race", createdAt: "2026-06-01T00:00:00Z" },
    ] as never);
    await waitFor(() => {
      const refreshedName = document.getElementById("settings-api-key-name") as HTMLInputElement;
      const refreshedRole = document.getElementById("settings-api-key-role") as HTMLSelectElement;
      const refreshedSubmit = document.querySelector<HTMLButtonElement>('#settings-api-key-form button[type="submit"]');
      expect(refreshedName.disabled).toBe(true);
      expect(refreshedRole.disabled).toBe(true);
      expect(refreshedSubmit?.disabled).toBe(true);
      expect((document.getElementById("shell-api-key-value") as HTMLTextAreaElement).value).toBe("scribe_first_response");
    });

    document.getElementById("shell-api-key-done")?.click();
    expect((document.getElementById("shell-api-key-value") as HTMLTextAreaElement).value).toBe("");
    expect(document.querySelector<HTMLButtonElement>('#settings-api-key-form button[type="submit"]')?.disabled).toBe(false);
  });

  it("keeps a created token visible and distinguishes a subsequent key-list refresh failure", async () => {
    vi.mocked(createAPIKey).mockResolvedValue({
      key: "scribe_created_before_refresh_failure",
      apiKey: { id: 94n, name: "Created key", role: "read", scopes: [], keyPrefix: "sk_created", createdAt: "2026-06-01T00:00:00Z" },
    } as never);

    await setupShell("settings");
    vi.mocked(listAPIKeys).mockRejectedValueOnce(new Error("list unavailable"));

    const form = document.getElementById("settings-api-key-form") as HTMLFormElement;
    (document.getElementById("settings-api-key-name") as HTMLInputElement).value = "Created key";
    form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));

    await waitFor(() => {
      expect(document.getElementById("shell-api-key-dialog")?.getAttribute("aria-hidden")).toBe("false");
      expect((document.getElementById("shell-api-key-value") as HTMLTextAreaElement).value).toBe("scribe_created_before_refresh_failure");
      expect(document.getElementById("settings-api-key-status")?.textContent).toContain("created, but the workspace token list could not be refreshed");
      expect(document.getElementById("settings-api-key-status")?.textContent).not.toContain("Create failed");
    });
  });

  it("revokes a workspace token that resolves after the page is hidden without exposing it", async () => {
    let resolveCreate: ((value: Awaited<ReturnType<typeof createAPIKey>>) => void) | undefined;
    vi.mocked(createAPIKey).mockImplementation(() => new Promise((resolve) => {
      resolveCreate = resolve;
    }));
    vi.mocked(deleteAPIKey).mockResolvedValue();

    await setupShell("settings");
    const form = document.getElementById("settings-api-key-form") as HTMLFormElement;
    (document.getElementById("settings-api-key-name") as HTMLInputElement).value = "Abandoned key";
    form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
    window.dispatchEvent(new Event("pagehide"));

    resolveCreate?.({
      key: "scribe_never_expose_after_pagehide",
      apiKey: { id: 92n, name: "Abandoned key", role: "read", scopes: [], keyPrefix: "sk_gone", createdAt: "2026-06-01T00:00:00Z" },
    } as never);

    await waitFor(() => {
      expect(deleteAPIKey).toHaveBeenCalledWith(92n, { workspaceId: "7" });
    });
    expect(document.getElementById("shell-api-key-dialog")?.getAttribute("aria-hidden")).toBe("true");
    expect((document.getElementById("shell-api-key-value") as HTMLTextAreaElement).value).toBe("");
    expect(document.body.textContent).not.toContain("scribe_never_expose_after_pagehide");
  });

  it("revokes a stale workspace token even when the user switches away and back before creation resolves", async () => {
    let resolveCreate: ((value: Awaited<ReturnType<typeof createAPIKey>>) => void) | undefined;
    vi.mocked(createAPIKey).mockImplementation(() => new Promise((resolve) => {
      resolveCreate = resolve;
    }));
    vi.mocked(deleteAPIKey).mockResolvedValue();

    await setupShell("settings");
    const form = document.getElementById("settings-api-key-form") as HTMLFormElement;
    (document.getElementById("settings-api-key-name") as HTMLInputElement).value = "Stale key";
    form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));

    const workspaceSelect = document.getElementById("sidebar-workspace-select") as HTMLSelectElement;
    const otherWorkspace = document.createElement("option");
    otherWorkspace.textContent = "Other workspace";
    otherWorkspace.value = "8";
    workspaceSelect.add(otherWorkspace);
    workspaceSelect.value = "8";
    workspaceSelect.dispatchEvent(new Event("change"));
    workspaceSelect.value = "7";
    workspaceSelect.dispatchEvent(new Event("change"));

    resolveCreate?.({
      key: "scribe_never_expose_after_workspace_switch",
      apiKey: { id: 93n, name: "Stale key", role: "read", scopes: [], keyPrefix: "sk_stale", createdAt: "2026-06-01T00:00:00Z" },
    } as never);

    await waitFor(() => {
      expect(deleteAPIKey).toHaveBeenCalledWith(93n, { workspaceId: "7" });
    });
    expect(document.getElementById("shell-api-key-dialog")?.getAttribute("aria-hidden")).toBe("true");
    expect((document.getElementById("shell-api-key-value") as HTMLTextAreaElement).value).toBe("");
    expect(document.body.textContent).not.toContain("scribe_never_expose_after_workspace_switch");
  });

  it("creates provider secrets and removes stored provider secrets from settings", async () => {
    vi.mocked(createProviderSecret).mockResolvedValue({
      id: 77n,
      provider: "gemini",
      name: "Gemini key",
      scope: "workspace",
      keyHint: "1234",
      createdAt: "2026-06-01T00:00:00Z",
    } as never);
    vi.mocked(deleteProviderSecret).mockResolvedValue({} as never);
    Object.defineProperty(window, "confirm", { configurable: true, value: vi.fn(() => true) });

    await setupShell("settings");
    vi.mocked(listProviderSecrets).mockResolvedValue([
      {
        id: 77n,
        provider: "gemini",
        name: "Gemini key",
        scope: "workspace",
        keyHint: "1234",
        createdAt: "2026-06-01T00:00:00Z",
      } as never,
    ]);

    const name = document.getElementById("settings-provider-secret-name") as HTMLInputElement | null;
    const scope = document.getElementById("settings-provider-secret-scope") as HTMLSelectElement | null;
    const apiKey = document.getElementById("settings-provider-secret-api-key") as HTMLInputElement | null;
    const form = document.getElementById("settings-provider-secret-form") as HTMLFormElement | null;
    expect(name).toBeTruthy();
    expect(scope).toBeTruthy();
    expect(apiKey).toBeTruthy();
    expect(form).toBeTruthy();
	expect(scope!.value).toBe("workspace");

    name!.value = "Gemini key";
    scope!.value = "workspace";
    apiKey!.value = "secret-token";
    form!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));

    await waitFor(() => {
      expect(createProviderSecret).toHaveBeenCalledWith({
        provider: "gemini",
        name: "Gemini key",
        apiKey: "secret-token",
        scope: "workspace",
      });
    });

    let deleteButton: HTMLButtonElement | null = null;
    await waitFor(() => {
      deleteButton = document.querySelector<HTMLButtonElement>("[data-provider-secret-delete=\"77\"]");
      expect(deleteButton).toBeTruthy();
    });
    deleteButton!.click();

    await waitFor(() => {
      expect(deleteProviderSecret).toHaveBeenCalledWith("77");
    });
  });

  it("adds, updates, and removes workspace members from settings", async () => {
    vi.mocked(addWorkspaceMember).mockResolvedValue({ user: { id: 13n, email: "member@example.test" }, role: "read" } as never);
    vi.mocked(updateWorkspaceMember).mockResolvedValue({ user: { id: 13n, email: "member@example.test" }, role: "write" } as never);
    vi.mocked(deleteWorkspaceMember).mockResolvedValue({} as never);
    Object.defineProperty(window, "confirm", { configurable: true, value: vi.fn(() => true) });

    await setupShell("settings");
    vi.mocked(listWorkspaceMembers).mockResolvedValue({
      workspace,
      members: [{ user: { id: 13n, email: "member@example.test" }, role: "read" }],
    } as never);

    const email = document.getElementById("settings-member-email") as HTMLInputElement | null;
    const role = document.getElementById("settings-member-role") as HTMLSelectElement | null;
    const form = document.getElementById("settings-add-member") as HTMLFormElement | null;
    expect(email).toBeTruthy();
    expect(role).toBeTruthy();
    expect(form).toBeTruthy();

    email!.value = "member@example.test";
    role!.value = "read";
    form!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));

    await waitFor(() => {
      expect(addWorkspaceMember).toHaveBeenCalledWith(7n, "member@example.test", "read");
      expect(document.body.textContent).toContain("member@example.test");
    });

    const memberRole = document.querySelector<HTMLSelectElement>("[data-member-role=\"13\"]");
    const saveButton = document.querySelector<HTMLButtonElement>("[data-member-save=\"13\"]");
    expect(memberRole).toBeTruthy();
    expect(saveButton).toBeTruthy();
    memberRole!.value = "write";
    saveButton!.click();

    await waitFor(() => {
      expect(updateWorkspaceMember).toHaveBeenCalledWith(7n, "13", "write");
    });

    const removeButton = document.querySelector<HTMLButtonElement>("[data-member-remove=\"13\"]");
    expect(removeButton).toBeTruthy();
    removeButton!.click();

    await waitFor(() => {
      expect(deleteWorkspaceMember).toHaveBeenCalledWith(7n, "13");
    });
  });

  it("creates OCR contexts from the contexts panel", async () => {
    vi.mocked(createContext).mockResolvedValue({
      id: 88n,
      name: "Special collections",
      description: "Bound volumes",
      isDefault: true,
      segmentationModel: "kraken",
      transcriptionProvider: "gemini",
      transcriptionModel: "gemini-3.5-flash",
      systemPrompt: "Read marginalia carefully.",
    } as never);

    await setupShell("contexts");
    vi.mocked(listContexts).mockResolvedValue([
      {
        id: 88n,
        name: "Special collections",
        isDefault: true,
        segmentationModel: "kraken",
        transcriptionProvider: "gemini",
        transcriptionModel: "gemini-3.5-flash",
      } as never,
    ]);

    (document.getElementById("contexts-name") as HTMLInputElement).value = "Special collections";
    (document.getElementById("contexts-provider") as HTMLInputElement).value = "gemini";
    document.getElementById("contexts-provider")?.dispatchEvent(new Event("change"));
    (document.getElementById("contexts-model") as HTMLInputElement).value = "gemini-3.5-flash";
    (document.getElementById("contexts-segmentation") as HTMLInputElement).value = "kraken";
    (document.getElementById("contexts-description") as HTMLTextAreaElement).value = "Bound volumes";
    (document.getElementById("contexts-system-prompt") as HTMLTextAreaElement).value = "Read marginalia carefully.";
    (document.getElementById("contexts-default") as HTMLInputElement).checked = true;
    document.getElementById("contexts-create-form")?.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));

    await waitFor(() => {
      const request = vi.mocked(createContext).mock.calls[0]?.[0];
      expect(request).toMatchObject({
        name: "Special collections",
        description: "Bound volumes",
        isDefault: true,
        segmentationModel: "kraken",
        transcriptionProvider: "gemini",
        transcriptionModel: "gemini-3.5-flash",
        systemPrompt: "Read marginalia carefully.",
      });
      expect(document.body.textContent).toContain("Special collections");
    });
  });
});
