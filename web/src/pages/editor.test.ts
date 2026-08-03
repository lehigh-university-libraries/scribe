// @vitest-environment happy-dom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { renderEditor } from "./editor";

const mocks = vi.hoisted(() => ({
  viewer: vi.fn(),
  getOCRRun: vi.fn(),
  reprocessItemImage: vi.fn(),
  listTranscriptionJobs: vi.fn(),
  subscribeToEvents: vi.fn(),
  publishItemImageEdits: vi.fn(),
  getAnnotationPage: vi.fn(),
  annotationAdapter: vi.fn(),
  getEditorManifest: vi.fn(),
}));

vi.mock("mirador", () => ({
  default: { viewer: mocks.viewer },
}));

vi.mock("mirador-scribe", () => ({
  default: [],
  annotationAdapters: {
    ScribeAnnotationAdapter: mocks.annotationAdapter,
  },
}));

vi.mock("../api/annotations", () => ({
  annotationClient: {},
  getAnnotationPage: mocks.getAnnotationPage,
  publishItemImageEdits: mocks.publishItemImageEdits,
}));

vi.mock("../api/processing", () => ({
  getOCRRun: mocks.getOCRRun,
  reprocessItemImage: mocks.reprocessItemImage,
}));

vi.mock("../api/transcription", () => ({
  listTranscriptionJobs: mocks.listTranscriptionJobs,
}));

vi.mock("../api/items", () => ({
  getEditorManifest: mocks.getEditorManifest,
}));

vi.mock("../api/events", () => ({
  subscribeToEvents: mocks.subscribeToEvents,
}));

describe("renderEditor", () => {
  beforeEach(() => {
    document.body.innerHTML = "";
    window.history.replaceState({}, "", "/editor");
    mocks.viewer.mockReset();
    mocks.getOCRRun.mockReset();
    mocks.reprocessItemImage.mockReset();
    mocks.listTranscriptionJobs.mockReset();
    mocks.subscribeToEvents.mockReset();
    mocks.publishItemImageEdits.mockReset();
    mocks.getAnnotationPage.mockReset();
    mocks.annotationAdapter.mockReset();
    mocks.getEditorManifest.mockReset();
    mocks.getEditorManifest.mockImplementation(async (itemImageId: string) => ({
      item: {
        id: "test-item",
        images: [{ id: BigInt(itemImageId), canvasUri: "https://example.test/canvas/1" }],
      },
      manifestJSON: JSON.stringify({
        "@context": "http://iiif.io/api/presentation/3/context.json",
        id: "https://example.test/manifest",
        type: "Manifest",
        label: { none: ["Test"] },
        items: [],
      }),
      selectedCanvasId: "https://example.test/canvas/1",
    }));
    mocks.subscribeToEvents.mockReturnValue({ close: vi.fn() });
    mocks.getAnnotationPage.mockResolvedValue({
      canvasUri: "https://example.test/canvas/1",
      page: {
        id: "https://scribe.test/pages/7",
        type: "AnnotationPage",
        items: [],
      },
      revision: "12",
      updatedAt: "2026-07-20T00:00:00Z",
    });
  });

  afterEach(() => {
    window.dispatchEvent(new Event("pagehide"));
  });

  it("announces a malformed deep link and offers a route back to the library", async () => {
    const app = document.createElement("div");
    document.body.appendChild(app);

    await renderEditor(app);

    const meta = document.getElementById("editor-meta");
    expect(meta?.getAttribute("role")).toBe("alert");
    expect(meta?.getAttribute("aria-live")).toBe("assertive");
    expect(document.querySelector('#mirador-viewer [role="alert"]')?.textContent)
      .toContain("missing the required itemImageId");
    expect(document.querySelector('#mirador-viewer a')?.textContent).toContain("Back to library");
  });

  it("offers retry and library recovery when the OCR run cannot load", async () => {
    window.history.replaceState({}, "", "/editor?itemImageId=42");
    mocks.getOCRRun.mockRejectedValue(new Error("service unavailable"));
    const app = document.createElement("div");
    document.body.appendChild(app);

    await renderEditor(app);

    expect(document.getElementById("editor-meta")?.getAttribute("role")).toBe("alert");
    expect(document.querySelector('#mirador-viewer [role="alert"]')?.textContent)
      .toContain("Failed to load the OCR run");
    expect(document.getElementById("editor-recovery-retry")?.textContent).toContain("Retry");
    expect(document.querySelector('#mirador-viewer a')?.textContent).toContain("Back to library");
  });

  it("requests a save before leaving a dirty editor", async () => {
    const app = document.createElement("div");
    document.body.appendChild(app);
    await renderEditor(app);

    document.dispatchEvent(
      new CustomEvent("scribe:dirty-state", {
        detail: { dirty: true, windowId: "scribe-editor-window" },
      }),
    );
    document.getElementById("home-nav")?.click();
    expect(
      document.getElementById("leave-dialog")?.classList.contains("flex"),
    ).toBe(true);

    let saveRequested = false;
    document.addEventListener(
      "scribe:request-save",
      (event) => {
        saveRequested = true;
        const detail = (
          event as CustomEvent<{ requestId: string; windowId: string }>
        ).detail;
        document.dispatchEvent(
          new CustomEvent("scribe:save-result", {
            detail: {
              ok: true,
              requestId: detail.requestId,
              windowId: detail.windowId,
            },
          }),
        );
      },
      { once: true },
    );

    document.getElementById("leave-save")?.click();
    await vi.waitFor(() => expect(saveRequested).toBe(true));
    await vi.waitFor(() => expect(window.location.pathname).toBe("/"));
  });

  it("routes the Scribe brand through the dirty-editor leave guard", async () => {
    const app = document.createElement("div");
    document.body.appendChild(app);
    await renderEditor(app);

    document.dispatchEvent(
      new CustomEvent("scribe:dirty-state", {
        detail: { dirty: true, windowId: "scribe-editor-window" },
      }),
    );
    document.getElementById("brand-nav")?.click();

    expect(window.location.pathname).toBe("/editor");
    expect(
      document.getElementById("leave-dialog")?.classList.contains("flex"),
    ).toBe(true);
  });

  it("registers beforeunload only while any editor window is dirty", async () => {
    const app = document.createElement("div");
    document.body.appendChild(app);
    await renderEditor(app);

    const dispatchBeforeUnload = () => {
      const event = new Event("beforeunload", { cancelable: true });
      window.dispatchEvent(event);
      return event.defaultPrevented;
    };

    expect(dispatchBeforeUnload()).toBe(false);
    document.dispatchEvent(
      new CustomEvent("scribe:dirty-state", {
        detail: { dirty: true, windowId: "window-a" },
      }),
    );
    expect(dispatchBeforeUnload()).toBe(true);

    document.dispatchEvent(
      new CustomEvent("scribe:dirty-state", {
        detail: { dirty: true, windowId: "window-b" },
      }),
    );
    document.dispatchEvent(
      new CustomEvent("scribe:dirty-state", {
        detail: { dirty: false, windowId: "window-a" },
      }),
    );
    expect(dispatchBeforeUnload()).toBe(true);

    document.dispatchEvent(
      new CustomEvent("scribe:dirty-state", {
        detail: { dirty: false, windowId: "window-b" },
      }),
    );
    expect(dispatchBeforeUnload()).toBe(false);
  });

  it("keeps the unload guard after cancel and clears it after explicit discard", async () => {
    const app = document.createElement("div");
    document.body.appendChild(app);
    await renderEditor(app);
    const dispatchBeforeUnload = () => {
      const event = new Event("beforeunload", { cancelable: true });
      window.dispatchEvent(event);
      return event.defaultPrevented;
    };

    document.dispatchEvent(
      new CustomEvent("scribe:dirty-state", {
        detail: { dirty: true, windowId: "scribe-editor-window" },
      }),
    );
    document.getElementById("home-nav")?.click();
    document.getElementById("leave-cancel")?.click();
    expect(dispatchBeforeUnload()).toBe(true);

    document.getElementById("home-nav")?.click();
    document.getElementById("leave-discard")?.click();
    expect(dispatchBeforeUnload()).toBe(false);
  });

  it("stays in the editor when save-before-leave fails", async () => {
    const app = document.createElement("div");
    document.body.appendChild(app);
    await renderEditor(app);

    document.dispatchEvent(
      new CustomEvent("scribe:dirty-state", {
        detail: { dirty: true, windowId: "scribe-editor-window" },
      }),
    );
    document.getElementById("home-nav")?.click();
    expect(
      document.getElementById("leave-dialog")?.classList.contains("flex"),
    ).toBe(true);

    document.addEventListener(
      "scribe:request-save",
      (event) => {
        const detail = (
          event as CustomEvent<{ requestId: string; windowId: string }>
        ).detail;
        document.dispatchEvent(
          new CustomEvent("scribe:save-result", {
            detail: {
              ok: false,
              requestId: detail.requestId,
              windowId: detail.windowId,
            },
          }),
        );
      },
      { once: true },
    );

    document.getElementById("leave-save")?.click();
    await vi.waitFor(() =>
      expect(
        (document.getElementById("leave-save") as HTMLButtonElement | null)
          ?.disabled,
      ).toBe(false),
    );
    expect(window.location.pathname).toBe("/editor");
    expect(
      document.getElementById("leave-dialog")?.classList.contains("flex"),
    ).toBe(true);
  });

  it("keeps dirty state isolated per Mirador window", async () => {
    const app = document.createElement("div");
    document.body.appendChild(app);
    await renderEditor(app);

    document.dispatchEvent(
      new CustomEvent("scribe:dirty-state", {
        detail: { dirty: true, windowId: "window-a" },
      }),
    );
    document.dispatchEvent(
      new CustomEvent("scribe:dirty-state", {
        detail: { dirty: false, windowId: "window-b" },
      }),
    );
    document.getElementById("home-nav")?.click();

    expect(
      document.getElementById("leave-dialog")?.classList.contains("flex"),
    ).toBe(true);
  });

  it("ignores dirty-state events that are not scoped to a Mirador window", async () => {
    const app = document.createElement("div");
    document.body.appendChild(app);
    await renderEditor(app);

    document.dispatchEvent(
      new CustomEvent("scribe:dirty-state", { detail: { dirty: true } }),
    );
    document.getElementById("home-nav")?.click();

    expect(
      document.getElementById("leave-dialog")?.classList.contains("flex"),
    ).toBe(false);
    expect(window.location.pathname).toBe("/");
  });

  it("skips both editor history sentinels after confirming a dirty back navigation", async () => {
    const app = document.createElement("div");
    document.body.appendChild(app);
    await renderEditor(app);
    const historyGo = vi.spyOn(window.history, "go").mockImplementation(() => {});

    document.dispatchEvent(
      new CustomEvent("scribe:dirty-state", {
        detail: { dirty: true, windowId: "scribe-editor-window" },
      }),
    );
    window.dispatchEvent(new PopStateEvent("popstate"));

    expect(
      document.getElementById("leave-dialog")?.classList.contains("flex"),
    ).toBe(true);
    document.getElementById("leave-discard")?.click();

    expect(historyGo).toHaveBeenCalledWith(-2);
  });

  it("publishes the exact revision saved by the Mirador editor", async () => {
    window.history.replaceState({}, "", "/editor?itemImageId=42");
    mocks.getOCRRun.mockResolvedValue({
      contextId: 33n,
      itemImageId: 42n,
      model: "test-model",
      imageUrl: "https://example.test/page.jpg",
    });
    mocks.listTranscriptionJobs.mockResolvedValue([]);
    mocks.getEditorManifest.mockResolvedValue({
      item: {
        id: "publish-item",
        images: [{ id: 42n, canvasUri: "https://source.example/canvas/1" }],
      },
      manifestJSON: "{}",
      selectedCanvasId: "https://source.example/canvas/1",
    });
    const app = document.createElement("div");
    document.body.appendChild(app);
    mocks.publishItemImageEdits.mockResolvedValue({
      annotationPageJson: "{}",
      canvasUri: "https://source.example/canvas/1",
      itemImageId: "42",
      publicUrl: "https://scribe.example/presentation/v3/item-image-42/canvas/page-1/annotations",
      publishedAt: "2026-01-01T00:00:00Z",
      publishedRevision: "7",
    });
    await renderEditor(app);

    const result = new Promise<CustomEvent>((resolve) => {
      document.addEventListener(
        "scribe:publish-result",
        (event) => resolve(event as CustomEvent),
        { once: true },
      );
    });
    document.dispatchEvent(
      new CustomEvent("scribe:request-publish", {
        detail: {
          canvasId: "https://source.example/canvas/1",
          expectedRevision: "7",
          itemImageId: "42",
          requestId: "publish-request-1",
          windowId: "window-a",
        },
      }),
    );

    await vi.waitFor(() =>
      expect(mocks.publishItemImageEdits).toHaveBeenCalledWith("42", "7"),
    );
    await expect(result).resolves.toMatchObject({
      detail: {
        canvasId: "https://source.example/canvas/1",
        ok: true,
        publicUrl: "https://scribe.example/presentation/v3/item-image-42/canvas/page-1/annotations",
        publishedRevision: "7",
        requestId: "publish-request-1",
        windowId: "window-a",
      },
    });
  });

  it("injects item image and persisted processing context into each annotation adapter", async () => {
    window.history.replaceState({}, "", "/editor?itemImageId=7");
    mocks.getOCRRun.mockResolvedValue({
      contextId: 33n,
      itemImageId: 7n,
      model: "test-model",
      imageUrl: "https://example.test/page.jpg",
    });
    mocks.listTranscriptionJobs.mockResolvedValue([]);
    const app = document.createElement("div");
    document.body.appendChild(app);

    await renderEditor(app);
    const config = mocks.viewer.mock.calls[0]?.[0] as {
      annotation: { adapter: (canvasId: string) => unknown };
    };
    config.annotation.adapter("https://example.test/canvas/1");

    expect(mocks.annotationAdapter).toHaveBeenCalledWith(
      expect.any(String),
      3,
      "https://example.test/canvas/1",
      "Scribe User",
      expect.objectContaining({ contextId: "33", itemImageId: "7" }),
    );

    document.getElementById("reprocess-nav")?.click();
    await vi.waitFor(() =>
      expect(mocks.getAnnotationPage).toHaveBeenCalledWith("7"),
    );
    await vi.waitFor(() =>
      expect(mocks.reprocessItemImage).toHaveBeenCalledWith("7", "33", "12"),
    );
  });

  it("opens a multi-Canvas manifest at the Canvas named by the deep-linked item image", async () => {
    window.history.replaceState({}, "", "/editor?itemId=5&itemImageId=202");
    mocks.getOCRRun.mockResolvedValue({
      contextId: 33n,
      itemImageId: 202n,
      model: "test-model",
      imageUrl: "https://example.test/page-b.jpg",
    });
    mocks.getEditorManifest.mockResolvedValue({
      item: {
        id: "5",
        images: [
          { id: 101n, canvasUri: "https://iiif.example/canvas/a" },
          { id: 202n, canvasUri: "https://iiif.example/canvas/b" },
        ],
      },
      manifestJSON: "{}",
      selectedCanvasId: "https://iiif.example/canvas/b",
    });
    mocks.listTranscriptionJobs.mockResolvedValue([]);
    const app = document.createElement("div");
    document.body.appendChild(app);

    await renderEditor(app);

    expect(mocks.viewer.mock.calls[0]?.[0]).toEqual(expect.objectContaining({
      windows: [expect.objectContaining({
        canvasId: "https://iiif.example/canvas/b",
        id: "scribe-editor-window",
      })],
    }));
  });

  it("scopes asynchronous transcription events to the active Mirador window and Canvas", async () => {
    window.history.replaceState({}, "", "/editor?itemImageId=8");
    mocks.getOCRRun.mockResolvedValue({
      contextId: 0n,
      itemImageId: 8n,
      model: "test-model",
      imageUrl: "https://example.test/page.jpg",
    });
    mocks.listTranscriptionJobs.mockResolvedValue([]);
    mocks.getEditorManifest.mockResolvedValue({
      item: {
        id: "event-item",
        images: [{ id: 8n, canvasUri: "https://iiif.example/canvas/active" }],
      },
      manifestJSON: "{}",
      selectedCanvasId: "https://iiif.example/canvas/active",
    });
    let onEvent: ((event: { type: string; data?: Record<string, unknown>; time?: string }) => void) | undefined;
    mocks.subscribeToEvents.mockImplementation((_filter, handler) => {
      onEvent = handler;
      return { close: vi.fn() };
    });
    const app = document.createElement("div");
    document.body.appendChild(app);
    await renderEditor(app);
    document.dispatchEvent(new CustomEvent("scribe:active-canvas", {
      detail: {
        canvasId: "https://iiif.example/canvas/active",
        itemImageId: "8",
        windowId: "scribe-editor-window",
      },
    }));
    const result = new Promise<CustomEvent>((resolve) => {
      document.addEventListener("scribe:transcription-result", (event) => resolve(event as CustomEvent), { once: true });
    });

    onEvent?.({
      type: "dev.scribe.transcription.task.completed",
      time: "2026-06-01T00:00:00Z",
      data: {
        annotationJson: JSON.stringify({ id: "line-1", type: "Annotation" }),
        completedSegments: 1,
        failedSegments: 0,
        jobId: "92",
        totalSegments: 2,
      },
    });

    await expect(result).resolves.toMatchObject({
      detail: {
        canvasId: "https://iiif.example/canvas/active",
        itemImageId: "8",
        windowId: "scribe-editor-window",
      },
    });
  });

  it("saves dirty edits before loading the revision used to reprocess", async () => {
    window.history.replaceState({}, "", "/editor?itemImageId=7");
    mocks.getOCRRun.mockResolvedValue({
      contextId: 33n,
      itemImageId: 7n,
      model: "test-model",
      imageUrl: "https://example.test/page.jpg",
    });
    mocks.listTranscriptionJobs.mockResolvedValue([]);
    const order: string[] = [];
    mocks.getAnnotationPage.mockImplementation(async () => {
      order.push("load-revision");
      return {
        canvasUri: "https://example.test/canvas/1",
        page: {
          id: "https://scribe.test/pages/7",
          type: "AnnotationPage",
          items: [],
        },
        revision: "19",
        updatedAt: "2026-07-20T00:00:00Z",
      };
    });
    mocks.reprocessItemImage.mockImplementation(async () => {
      order.push("reprocess");
      return {};
    });
    const app = document.createElement("div");
    document.body.appendChild(app);
    await renderEditor(app);

    document.dispatchEvent(
      new CustomEvent("scribe:dirty-state", {
        detail: { dirty: true, windowId: "window-a" },
      }),
    );
    document.addEventListener(
      "scribe:request-save",
      (event) => {
        order.push("save");
        const detail = (
          event as CustomEvent<{ requestId: string; windowId: string }>
        ).detail;
        document.dispatchEvent(
          new CustomEvent("scribe:save-result", {
            detail: {
              ok: true,
              requestId: detail.requestId,
              windowId: detail.windowId,
            },
          }),
        );
      },
      { once: true },
    );

    document.getElementById("reprocess-nav")?.click();

    await vi.waitFor(() =>
      expect(mocks.reprocessItemImage).toHaveBeenCalledWith("7", "33", "19"),
    );
    expect(order).toEqual(["save", "load-revision", "reprocess"]);
  });

  it("does not reprocess when pending edits fail to save", async () => {
    window.history.replaceState({}, "", "/editor?itemImageId=7");
    mocks.getOCRRun.mockResolvedValue({
      contextId: 33n,
      itemImageId: 7n,
      model: "test-model",
      imageUrl: "https://example.test/page.jpg",
    });
    mocks.listTranscriptionJobs.mockResolvedValue([]);
    const app = document.createElement("div");
    document.body.appendChild(app);
    await renderEditor(app);

    document.dispatchEvent(
      new CustomEvent("scribe:dirty-state", {
        detail: { dirty: true, windowId: "scribe-editor-window" },
      }),
    );
    document.addEventListener(
      "scribe:request-save",
      (event) => {
        const detail = (
          event as CustomEvent<{ requestId: string; windowId: string }>
        ).detail;
        document.dispatchEvent(
          new CustomEvent("scribe:save-result", {
            detail: {
              ok: false,
              requestId: detail.requestId,
              windowId: detail.windowId,
            },
          }),
        );
      },
      { once: true },
    );

    document.getElementById("reprocess-nav")?.click();

    await vi.waitFor(() => {
      expect(
        document.getElementById("editor-transcription-status")?.textContent,
      ).toContain("Pending edits could not be saved");
    });
    expect(mocks.getAnnotationPage).not.toHaveBeenCalled();
    expect(mocks.reprocessItemImage).not.toHaveBeenCalled();
  });

  it("reloads annotations when the latest loaded transcription job is complete", async () => {
    window.history.replaceState({}, "", "/editor?itemImageId=7");
    mocks.getOCRRun.mockResolvedValue({
      contextId: 0n,
      itemImageId: 7n,
      model: "test-model",
      imageUrl: "https://example.test/page.jpg",
    });
    mocks.listTranscriptionJobs.mockResolvedValue([
      {
        id: 91n,
        status: "completed",
        completedSegments: 3,
        failedSegments: 0,
        totalSegments: 3,
      },
    ]);
    const app = document.createElement("div");
    document.body.appendChild(app);
    let reloads = 0;
    document.addEventListener("scribe:reload-annotations", () => {
      reloads += 1;
    });

    await renderEditor(app);

    expect(mocks.viewer).toHaveBeenCalled();
    expect(reloads).toBe(1);
  });

  it("preserves a reconciled terminal job status on a job deep link", async () => {
    window.history.replaceState(
      {},
      "",
      "/editor?itemImageId=7&jobId=91",
    );
    mocks.getOCRRun.mockResolvedValue({
      contextId: 0n,
      itemImageId: 7n,
      model: "test-model",
      imageUrl: "https://example.test/page.jpg",
    });
    mocks.listTranscriptionJobs.mockResolvedValue([
      {
        id: 91n,
        status: "failed",
        completedSegments: 0,
        failedSegments: 0,
        totalSegments: 3,
        errorMessage: "workspace provider credential is not configured",
      },
    ]);
    const app = document.createElement("div");
    document.body.appendChild(app);

    await renderEditor(app);

    expect(
      document.getElementById("editor-transcription-status")?.textContent,
    ).toContain("workspace provider credential is not configured");
    expect(
      document.getElementById("editor-transcription-status")?.textContent,
    ).not.toContain("Preparing batch transcription");
  });

  it("reconciles the durable job snapshot after the resumable stream is ready", async () => {
    window.history.replaceState({}, "", "/editor?itemImageId=7");
    mocks.getOCRRun.mockResolvedValue({
      contextId: 0n,
      itemImageId: 7n,
      model: "test-model",
      imageUrl: "https://example.test/page.jpg",
    });
    mocks.listTranscriptionJobs
      .mockResolvedValueOnce([
        {
          id: 91n,
          status: "running",
          completedSegments: 2,
          failedSegments: 0,
          totalSegments: 3,
        },
      ])
      .mockResolvedValueOnce([
        {
          id: 91n,
          status: "completed",
          completedSegments: 3,
          failedSegments: 0,
          totalSegments: 3,
        },
      ]);
    let onReady: (() => void) | undefined;
    mocks.subscribeToEvents.mockImplementation((options: { onReady?: () => void }) => {
      onReady = options.onReady;
      return { close: vi.fn() };
    });
    const app = document.createElement("div");
    document.body.appendChild(app);
    let reloads = 0;
    document.addEventListener("scribe:reload-annotations", () => {
      reloads += 1;
    });

    await renderEditor(app);

    expect(mocks.subscribeToEvents.mock.invocationCallOrder[0]).toBeLessThan(
      mocks.listTranscriptionJobs.mock.invocationCallOrder[0],
    );
    expect(reloads).toBe(0);
    onReady?.();
    await vi.waitFor(() => expect(mocks.listTranscriptionJobs).toHaveBeenCalledTimes(2));
    expect(reloads).toBe(1);
  });

  it("routes completed transcription events into Mirador reload events", async () => {
    window.history.replaceState({}, "", "/editor?itemImageId=8");
    mocks.getOCRRun.mockResolvedValue({
      contextId: 0n,
      itemImageId: 8n,
      model: "test-model",
      imageUrl: "https://example.test/page.jpg",
    });
    mocks.listTranscriptionJobs.mockResolvedValue([]);
    let onEvent:
      | ((event: {
          type: string;
          data?: Record<string, unknown>;
          time?: string;
        }) => void)
      | undefined;
    mocks.subscribeToEvents.mockImplementation((_filter, handler) => {
      onEvent = handler;
      return { close: vi.fn() };
    });
    const app = document.createElement("div");
    document.body.appendChild(app);
    let reloads = 0;
    document.addEventListener("scribe:reload-annotations", () => {
      reloads += 1;
    });

    await renderEditor(app);
    onEvent?.({
      type: "dev.scribe.transcription.completed",
      time: "2026-06-01T00:00:00Z",
      data: {
        jobId: "92",
        completedSegments: 2,
        failedSegments: 0,
        totalSegments: 2,
      },
    });

    expect(reloads).toBe(1);
  });
});
