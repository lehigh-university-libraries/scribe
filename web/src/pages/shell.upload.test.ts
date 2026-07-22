// @vitest-environment happy-dom

import { beforeEach, describe, expect, it, vi } from "vitest";

import { getAnnotationPage } from "../api/annotations";
import { createAPIKey, createProviderSecret, deleteAPIKey, deleteProviderSecret, getAuthMe, listAPIKeys, listProviderSecrets, logout } from "../api/auth";
import { createContext, getContextMetrics, getModelCatalog, listContexts } from "../api/context";
import { subscribeToEvents } from "../api/events";
import { getItemExportSnapshot, importManifest, listItems, listItemProviderCallAudits, prepareItemExport, uploadItemImages } from "../api/items";
import { processImageURL, reprocessItemImage } from "../api/processing";
import { listTranscriptionJobs } from "../api/transcription";
import { addWorkspaceMember, deleteWorkspaceMember, listWorkspaceMembers, listWorkspaces, updateWorkspaceMember } from "../api/workspaces";
import { AnnotationExportFormat } from "../proto/scribe/v1/annotation_pb";
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

vi.mock("../api/transcription", () => ({
  listTranscriptionJobs: vi.fn(),
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
      { id: "ollama", label: "Ollama", models: [{ id: "glm-ocr:bf16", label: "glm-ocr:bf16", isDefault: true }], supportsSystemPrompt: true, supportsTemperature: true },
      { id: "gemini", label: "Google Gemini", models: [{ id: "gemini-3.5-flash", label: "gemini-3.5-flash", isDefault: true }], requiresApiKey: true, supportsSystemPrompt: true, supportsTemperature: true },
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
  vi.mocked(listTranscriptionJobs).mockResolvedValue([{ totalSegments: 1, completedSegments: 0 } as never]);

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
    vi.mocked(processImageURL).mockResolvedValue({ itemImageId: 101n } as never);

    const input = document.getElementById("library-image-url") as HTMLInputElement | null;
    const form = document.getElementById("library-form-url") as HTMLFormElement | null;
    expect(input).toBeTruthy();
    expect(form).toBeTruthy();

    input!.value = "https://example.test/page.jpg";
    form!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));

    await waitFor(() => {
      expect(processImageURL).toHaveBeenCalledWith("https://example.test/page.jpg", 0n);
      expect(window.location.href).toContain("/editor?itemImageId=101");
    });
  });

  it("reconciles the durable job snapshot after the event stream becomes ready", async () => {
    await setupShell();
    const close = vi.fn();
    let onReady: (() => void) | undefined;
    vi.mocked(processImageURL).mockResolvedValue({ itemImageId: 111n } as never);
    vi.mocked(listTranscriptionJobs)
      .mockResolvedValueOnce([])
      .mockResolvedValueOnce([{ totalSegments: 1, completedSegments: 0 } as never]);
    vi.mocked(subscribeToEvents).mockImplementationOnce((options) => {
      onReady = options.onReady;
      return { close };
    });

    const input = document.getElementById("library-image-url") as HTMLInputElement;
    const form = document.getElementById("library-form-url") as HTMLFormElement;
    input.value = "https://example.test/late-job.jpg";
    form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));

    await waitFor(() => {
      expect(subscribeToEvents).toHaveBeenLastCalledWith(
        expect.objectContaining({ itemImageId: "111", onReady: expect.any(Function) }),
        expect.any(Function),
      );
      expect(listTranscriptionJobs).toHaveBeenCalledTimes(1);
    });
    expect(window.location.pathname).not.toBe("/editor");

    onReady?.();
    await waitFor(() => {
      expect(listTranscriptionJobs).toHaveBeenCalledTimes(2);
      expect(window.location.href).toContain("/editor?itemImageId=111");
      expect(close).toHaveBeenCalledOnce();
    });
  });

  it("closes a stream that reports a job synchronously during subscription", async () => {
    await setupShell();
    const close = vi.fn();
    vi.mocked(processImageURL).mockResolvedValue({ itemImageId: 112n } as never);
    vi.mocked(subscribeToEvents).mockImplementationOnce((_options, onEvent) => {
      onEvent({
        specversion: "1.0",
        id: "event-1",
        source: "https://scribe.test/events",
        type: "dev.scribe.transcription.task.started",
        time: "2026-07-21T00:00:00Z",
      });
      return { close };
    });

    const input = document.getElementById("library-image-url") as HTMLInputElement;
    const form = document.getElementById("library-form-url") as HTMLFormElement;
    input.value = "https://example.test/immediate-job.jpg";
    form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));

    await waitFor(() => {
      expect(window.location.href).toContain("/editor?itemImageId=112");
      expect(close).toHaveBeenCalledOnce();
    });
    expect(listTranscriptionJobs).not.toHaveBeenCalled();
  });

  it("starts single image upload processing from the library form and opens the editor", async () => {
    await setupShell();
    const file = new File(["image-bytes"], "page-one.jpg", { type: "image/jpeg" });
    vi.mocked(uploadItemImages).mockResolvedValue({ images: [{ id: 202n }] } as never);

    document.querySelector<HTMLButtonElement>('[data-library-tab="single"]')?.click();
    const input = document.getElementById("library-single-file") as HTMLInputElement | null;
    const form = document.getElementById("library-form-single") as HTMLFormElement | null;
    expect(input).toBeTruthy();
    expect(form).toBeTruthy();

    Object.defineProperty(input, "files", { configurable: true, value: [file] });
    form!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));

    await waitFor(() => {
      expect(uploadItemImages).toHaveBeenCalledWith([file], { contextId: 0n });
      expect(window.location.href).toContain("/editor?itemImageId=202");
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
        { id: "ollama", label: "Ollama", models: [{ id: "glm-ocr:bf16", label: "glm-ocr:bf16", isDefault: true }, { id: "llava", label: "llava", isDefault: false }], supportsSystemPrompt: true, supportsTemperature: true },
        { id: "kraken", label: "Kraken", models: [{ id: "catmus-print-fondue-large.mlmodel", label: "CATMuS", isDefault: true }] },
        { id: "openai", label: "OpenAI", models: [{ id: "gpt-4.1", label: "GPT-4.1", isDefault: true }], requiresApiKey: true, supportsSystemPrompt: true, supportsTemperature: true },
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

  it("creates and deletes API keys from settings", async () => {
    vi.mocked(createAPIKey).mockResolvedValue({
      key: "scribe_secret_once",
      apiKey: { id: 56n, name: "New key", role: "write", scopes: [], keyPrefix: "sk_new", createdAt: "2026-06-01T00:00:00Z" },
    } as never);
    vi.mocked(deleteAPIKey).mockResolvedValue({} as never);
    Object.defineProperty(window, "alert", { configurable: true, value: vi.fn() });
    Object.defineProperty(window, "confirm", { configurable: true, value: vi.fn(() => true) });

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
    form!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));

    await waitFor(() => {
      expect(createAPIKey).toHaveBeenCalledWith({ name: "New key", role: "write" });
      expect(window.alert).toHaveBeenCalledWith(expect.stringContaining("scribe_secret_once"));
    });

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
