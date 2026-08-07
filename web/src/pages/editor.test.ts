// @vitest-environment happy-dom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { TranscriptionJobAttemptOutcome } from "../proto/scribe/v1/transcription_pb";
import { renderEditor } from "./editor";

const mocks = vi.hoisted(() => ({
  viewer: vi.fn(),
  getOCRRun: vi.fn(),
  getTranscriptionJob: vi.fn(),
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
  getTranscriptionJob: mocks.getTranscriptionJob,
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
    mocks.getTranscriptionJob.mockReset();
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
    vi.useRealTimers();
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

  it("rejects a malformed exact transcription job before loading editor state", async () => {
    window.history.replaceState({}, "", "/editor?itemImageId=42&jobId=not-a-job");
    const app = document.createElement("div");
    document.body.appendChild(app);

    await renderEditor(app);

    expect(document.querySelector('#mirador-viewer [role="alert"]')?.textContent)
      .toContain("invalid jobId");
    expect(mocks.getOCRRun).not.toHaveBeenCalled();
    expect(mocks.getTranscriptionJob).not.toHaveBeenCalled();
    expect(mocks.viewer).not.toHaveBeenCalled();
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

  it("clears image-bound transcription state permanently after the active Canvas changes", async () => {
    window.history.replaceState(
      {},
      "",
      "/editor?itemId=5&itemImageId=202&workspace_id=17&jobId=91",
    );
    mocks.getOCRRun.mockImplementation(async (itemImageId: string) => ({
      contextId: 33n,
      itemImageId: BigInt(itemImageId),
      model: "test-model",
      imageUrl: `https://example.test/page-${itemImageId}.jpg`,
    }));
    mocks.getTranscriptionJob.mockResolvedValue({
      id: 91n,
      itemImageId: 202n,
      status: "running",
      completedSegments: 1,
      failedSegments: 0,
      totalSegments: 2,
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
    expect(mocks.getTranscriptionJob).toHaveBeenCalledOnce();
    expect(mocks.getTranscriptionJob).toHaveBeenCalledWith(91n);
    mocks.getTranscriptionJob.mockClear();
    mocks.listTranscriptionJobs.mockClear();
    document.dispatchEvent(new CustomEvent("scribe:active-canvas", {
      detail: {
        canvasId: "https://iiif.example/canvas/a",
        itemImageId: "101",
        windowId: "scribe-editor-window",
      },
    }));

    await vi.waitFor(() => {
      expect(mocks.listTranscriptionJobs).toHaveBeenCalledWith(101n);
    });
    let params = new URL(window.location.href).searchParams;
    expect(params.get("itemImageId")).toBe("101");
    expect(params.get("workspace_id")).toBe("17");
    expect(params.has("jobId")).toBe(false);

    mocks.listTranscriptionJobs.mockClear();
    document.dispatchEvent(new CustomEvent("scribe:active-canvas", {
      detail: {
        canvasId: "https://iiif.example/canvas/b",
        itemImageId: "202",
        windowId: "scribe-editor-window",
      },
    }));

    await vi.waitFor(() => {
      expect(mocks.listTranscriptionJobs).toHaveBeenCalledWith(202n);
    });
    params = new URL(window.location.href).searchParams;
    expect(params.get("itemImageId")).toBe("202");
    expect(params.get("workspace_id")).toBe("17");
    expect(params.has("jobId")).toBe(false);
    expect(mocks.getTranscriptionJob).not.toHaveBeenCalled();
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
    document.dispatchEvent(new CustomEvent("scribe:transcription-overlay-state", {
      detail: {
        canvasId: "https://iiif.example/canvas/active",
        ready: true,
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

  it("advances the wand past previously failed segments", async () => {
    window.history.replaceState({}, "", "/editor?itemImageId=7");
    mocks.getOCRRun.mockResolvedValue({
      contextId: 33n,
      itemImageId: 7n,
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
    const segments: Array<Record<string, unknown>> = [];
    const onSegment = (event: Event) => {
      segments.push((event as CustomEvent<Record<string, unknown>>).detail);
    };
    document.addEventListener("scribe:transcription-segment", onSegment);
    const app = document.createElement("div");
    document.body.appendChild(app);

    await renderEditor(app);
    document.dispatchEvent(new CustomEvent("scribe:transcription-overlay-state", {
      detail: {
        canvasId: "https://example.test/canvas/1",
        ready: true,
        windowId: "scribe-editor-window",
      },
    }));
    const annotation = {
      id: "https://example.test/annotations/line-2",
      type: "Annotation",
    };
    onEvent?.({
      type: "dev.scribe.transcription.task.started",
      time: "2026-08-05T10:00:01Z",
      data: {
        annotationId: annotation.id,
        annotationJson: JSON.stringify(annotation),
        completedSegments: 0,
        failedSegments: 1,
        jobId: "92",
        totalSegments: 3,
      },
    });

    expect(segments.at(-1)).toMatchObject({
      annotation,
      done: 2,
      total: 3,
    });
    expect(
      document.getElementById("editor-transcription-status")?.textContent,
    ).toContain("1/3");
    document.removeEventListener("scribe:transcription-segment", onSegment);
  });

  it("replays the current durable transcription line only after the wand overlay is ready", async () => {
    window.history.replaceState(
      {},
      "",
      "/editor?itemImageId=7&jobId=91",
    );
    mocks.getOCRRun.mockResolvedValue({
      contextId: 33n,
      itemImageId: 7n,
      model: "test-model",
      imageUrl: "https://example.test/page.jpg",
    });
    const currentAnnotation = {
      id: "https://example.test/annotations/line-1",
      type: "Annotation",
      textGranularity: "line",
    };
    mocks.getTranscriptionJob.mockResolvedValue({
      id: 91n,
      itemImageId: 7n,
      status: "running",
      completedSegments: 0,
      failedSegments: 0,
      totalSegments: 7,
      currentAnnotationId: currentAnnotation.id,
      currentAnnotationJson: JSON.stringify(currentAnnotation),
      updatedAt: "2026-08-05T10:00:00Z",
    });
    const segments: Array<Record<string, unknown>> = [];
    const onSegment = (event: Event) => {
      segments.push((event as CustomEvent<Record<string, unknown>>).detail);
    };
    document.addEventListener("scribe:transcription-segment", onSegment);
    const app = document.createElement("div");
    document.body.appendChild(app);

    await renderEditor(app);

    expect(segments).toEqual([]);
    document.dispatchEvent(new CustomEvent("scribe:remote-rebase-ready", {
      detail: {
        canvasId: "https://example.test/canvas/1",
        itemImageId: "7",
        windowId: "scribe-editor-window",
      },
    }));
    await vi.waitFor(() => {
      expect(mocks.getTranscriptionJob).toHaveBeenCalledTimes(2);
    });
    expect(segments).toEqual([]);
    document.dispatchEvent(new CustomEvent("scribe:transcription-overlay-state", {
      detail: {
        canvasId: "https://example.test/canvas/1",
        ready: true,
        windowId: "scribe-editor-window",
      },
    }));

    await vi.waitFor(() => {
      expect(segments).toEqual([expect.objectContaining({
        annotation: currentAnnotation,
        canvasId: "https://example.test/canvas/1",
        done: 1,
        itemImageId: "7",
        total: 7,
        windowId: "scribe-editor-window",
      })]);
    });
    expect(mocks.getTranscriptionJob).toHaveBeenCalledTimes(3);
    document.dispatchEvent(new CustomEvent("scribe:transcription-overlay-state", {
      detail: {
        canvasId: "https://example.test/canvas/1",
        ready: true,
        windowId: "scribe-editor-window",
      },
    }));
    await Promise.resolve();
    expect(segments).toHaveLength(1);

    document.dispatchEvent(new CustomEvent("scribe:transcription-overlay-state", {
      detail: {
        canvasId: "https://example.test/canvas/1",
        ready: false,
        windowId: "scribe-editor-window",
      },
    }));
    document.dispatchEvent(new CustomEvent("scribe:transcription-overlay-state", {
      detail: {
        canvasId: "https://example.test/canvas/1",
        ready: true,
        windowId: "scribe-editor-window",
      },
    }));
    await vi.waitFor(() => expect(segments).toHaveLength(2));
    document.removeEventListener("scribe:transcription-segment", onSegment);
  });

  it("does not treat one late durable result as a visible prefix during completed catch-up", async () => {
    vi.useFakeTimers();
    window.history.replaceState(
      {},
      "",
      "/editor?itemImageId=7&jobId=91",
    );
    mocks.getOCRRun.mockResolvedValue({
      contextId: 33n,
      itemImageId: 7n,
      model: "test-model",
      imageUrl: "https://example.test/page.jpg",
    });
    const lines = Array.from({ length: 7 }, (_, index) => ({
      id: `https://example.test/annotations/line-${index + 1}`,
      type: "Annotation",
      textGranularity: "line",
      body: { type: "TextualBody", value: `Line ${index + 1}` },
    }));
    let completed = false;
    mocks.getTranscriptionJob.mockImplementation(async () => ({
      id: 91n,
      itemImageId: 7n,
      status: completed ? "completed" : "running",
      attemptCount: 1,
      completedSegments: completed ? 7 : 5,
      failedSegments: 0,
      totalSegments: 7,
      currentAnnotationId: "",
      currentAnnotationJson: "",
      lastResultAnnotationJson: completed ? "" : JSON.stringify(lines[4]),
      updatedAt: completed
        ? "2026-08-05T10:00:01Z"
        : "2026-08-05T10:00:00Z",
      attempts: completed
        ? [{
            attemptNumber: 1,
            jobId: 91n,
            outcome: TranscriptionJobAttemptOutcome.COMPLETED,
            resultRevision: 20n,
          }]
        : [],
    }));
    mocks.getAnnotationPage.mockResolvedValue({
      canvasUri: "https://example.test/canvas/1",
      page: {
        id: "https://scribe.test/pages/7",
        type: "AnnotationPage",
        items: lines,
      },
      revision: "20",
      updatedAt: "2026-08-05T10:00:01Z",
    });
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
    const segments: Array<Record<string, unknown>> = [];
    const results: Array<Record<string, unknown>> = [];
    const onSegment = (event: Event) => {
      segments.push((event as CustomEvent<Record<string, unknown>>).detail);
    };
    const onResult = (event: Event) => {
      results.push((event as CustomEvent<Record<string, unknown>>).detail);
    };
    document.addEventListener("scribe:transcription-segment", onSegment);
    document.addEventListener("scribe:transcription-result", onResult);
    const app = document.createElement("div");
    document.body.appendChild(app);

    await renderEditor(app);
    document.dispatchEvent(new CustomEvent("scribe:transcription-overlay-state", {
      detail: {
        canvasId: "https://example.test/canvas/1",
        ready: true,
        windowId: "scribe-editor-window",
      },
    }));
    await vi.advanceTimersByTimeAsync(0);
    expect(results.map((detail) => detail.annotationId)).toEqual([lines[4].id]);

    document.dispatchEvent(new CustomEvent("scribe:remote-rebase-ready", {
      detail: {
        canvasId: "https://example.test/canvas/1",
        itemImageId: "7",
        windowId: "scribe-editor-window",
      },
    }));
    await vi.advanceTimersByTimeAsync(0);
    completed = true;
    onEvent?.({
      type: "dev.scribe.transcription.completed",
      time: "2026-08-05T10:00:01Z",
      data: {
        attemptNumber: 1,
        completedSegments: 7,
        failedSegments: 0,
        jobId: "91",
        totalSegments: 7,
      },
    });
    await vi.advanceTimersByTimeAsync(0);

    expect(segments.filter((detail) => detail.annotation).at(-1)).toMatchObject({
      annotationId: lines[0].id,
      done: 1,
      jobId: "91",
    });
    await vi.runAllTimersAsync();

    expect(
      segments
        .filter((detail) => detail.annotation)
        .map((detail) => detail.annotationId),
    ).toEqual(lines.filter((_line, index) => index !== 4).map((line) => line.id));
    expect(results.map((detail) => detail.annotationId)).toEqual([
      lines[4].id,
      ...lines.filter((_line, index) => index !== 4).map((line) => line.id),
    ]);

    document.removeEventListener("scribe:transcription-segment", onSegment);
    document.removeEventListener("scribe:transcription-result", onResult);
  });

  it("replays every canonical line from the successful retry attempt", async () => {
    vi.useFakeTimers();
    window.history.replaceState(
      {},
      "",
      "/editor?itemImageId=7&jobId=91",
    );
    mocks.getOCRRun.mockResolvedValue({
      contextId: 33n,
      itemImageId: 7n,
      model: "test-model",
      imageUrl: "https://example.test/page.jpg",
    });
    const lines = [
      {
        id: "https://example.test/annotations/line-1",
        type: "Annotation",
        textGranularity: "line",
        body: { type: "TextualBody", value: "Successful first line" },
      },
      {
        id: "https://example.test/annotations/line-2",
        type: "Annotation",
        textGranularity: "line",
        body: { type: "TextualBody", value: "Successful second line" },
      },
    ];
    let completed = false;
    mocks.getTranscriptionJob.mockImplementation(async () => ({
      id: 91n,
      itemImageId: 7n,
      status: completed ? "completed" : "running",
      attemptCount: completed ? 2 : 1,
      completedSegments: completed ? 2 : 1,
      failedSegments: 0,
      totalSegments: 2,
      currentAnnotationId: "",
      currentAnnotationJson: "",
      lastResultAnnotationJson: completed ? "" : JSON.stringify(lines[0]),
      updatedAt: completed
        ? "2026-08-05T10:00:02Z"
        : "2026-08-05T10:00:01Z",
      attempts: completed
        ? [
            {
              attemptNumber: 1,
              jobId: 91n,
              outcome: TranscriptionJobAttemptOutcome.FAILED,
              resultRevision: 0n,
            },
            {
              attemptNumber: 2,
              jobId: 91n,
              outcome: TranscriptionJobAttemptOutcome.COMPLETED,
              resultRevision: 20n,
            },
          ]
        : [],
    }));
    mocks.getAnnotationPage.mockResolvedValue({
      canvasUri: "https://example.test/canvas/1",
      page: {
        id: "https://scribe.test/pages/7",
        type: "AnnotationPage",
        items: lines,
      },
      revision: "20",
      updatedAt: "2026-08-05T10:00:02Z",
    });
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
    const segments: Array<Record<string, unknown>> = [];
    const results: Array<Record<string, unknown>> = [];
    const onSegment = (event: Event) => {
      segments.push((event as CustomEvent<Record<string, unknown>>).detail);
    };
    const onResult = (event: Event) => {
      results.push((event as CustomEvent<Record<string, unknown>>).detail);
    };
    document.addEventListener("scribe:transcription-segment", onSegment);
    document.addEventListener("scribe:transcription-result", onResult);
    const app = document.createElement("div");
    document.body.appendChild(app);

    await renderEditor(app);
    document.dispatchEvent(new CustomEvent("scribe:remote-rebase-ready", {
      detail: {
        canvasId: "https://example.test/canvas/1",
        itemImageId: "7",
        windowId: "scribe-editor-window",
      },
    }));
    document.dispatchEvent(new CustomEvent("scribe:transcription-overlay-state", {
      detail: {
        canvasId: "https://example.test/canvas/1",
        ready: true,
        windowId: "scribe-editor-window",
      },
    }));
    await vi.advanceTimersByTimeAsync(0);
    expect(results).toEqual([expect.objectContaining({
      annotationId: lines[0].id,
      attemptNumber: 1,
      catchUp: false,
      done: 1,
    })]);

    completed = true;
    onEvent?.({
      type: "dev.scribe.transcription.completed",
      time: "2026-08-05T10:00:02Z",
      data: {
        attemptNumber: 2,
        completedSegments: 2,
        failedSegments: 0,
        jobId: "91",
        totalSegments: 2,
      },
    });
    await vi.advanceTimersByTimeAsync(0);
    await vi.runAllTimersAsync();

    expect(segments.filter((detail) => detail.annotation)).toEqual([
      expect.objectContaining({
        annotationId: lines[0].id,
        attemptNumber: 2,
        catchUp: true,
        done: 1,
      }),
      expect.objectContaining({
        annotationId: lines[1].id,
        attemptNumber: 2,
        catchUp: true,
        done: 2,
      }),
    ]);
    expect(results.filter((detail) => detail.catchUp === true)).toEqual([
      expect.objectContaining({ annotationId: lines[0].id, attemptNumber: 2 }),
      expect.objectContaining({ annotationId: lines[1].id, attemptNumber: 2 }),
    ]);

    document.removeEventListener("scribe:transcription-segment", onSegment);
    document.removeEventListener("scribe:transcription-result", onResult);
  });

  it("paces a fast completed job from its exact canonical result after the overlay is ready", async () => {
    vi.useFakeTimers();
    window.history.replaceState(
      {},
      "",
      "/editor?itemImageId=7&jobId=91",
    );
    mocks.getOCRRun.mockResolvedValue({
      contextId: 33n,
      itemImageId: 7n,
      model: "test-model",
      imageUrl: "https://example.test/page.jpg",
    });
    mocks.getTranscriptionJob.mockResolvedValue({
      id: 91n,
      itemImageId: 7n,
      status: "completed",
      completedSegments: 2,
      failedSegments: 0,
      totalSegments: 2,
      currentAnnotationId: "",
      currentAnnotationJson: "",
      lastResultAnnotationJson: "",
      attempts: [{
        attemptNumber: 1,
        jobId: 91n,
        outcome: TranscriptionJobAttemptOutcome.COMPLETED,
        resultRevision: 20n,
      }],
    });
    const lines = [
      {
        id: "https://example.test/annotations/line-1",
        type: "Annotation",
        textGranularity: "line",
        body: { type: "TextualBody", value: "First line" },
      },
      {
        id: "https://example.test/annotations/line-2",
        type: "Annotation",
        textGranularity: "line",
        body: { type: "TextualBody", value: "Second line" },
      },
    ];
    mocks.getAnnotationPage.mockResolvedValue({
      canvasUri: "https://example.test/canvas/1",
      page: {
        id: "https://scribe.test/pages/7",
        type: "AnnotationPage",
        items: [
          lines[0],
          {
            id: "https://example.test/annotations/word-1",
            type: "Annotation",
            textGranularity: "word",
          },
          lines[1],
        ],
      },
      revision: "20",
      updatedAt: "2026-08-05T10:00:00Z",
    });
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
    const segments: Array<Record<string, unknown>> = [];
    const results: Array<Record<string, unknown>> = [];
    const reloads: Array<Record<string, unknown>> = [];
    const onSegment = (event: Event) => {
      segments.push((event as CustomEvent<Record<string, unknown>>).detail);
    };
    const onResult = (event: Event) => {
      results.push((event as CustomEvent<Record<string, unknown>>).detail);
    };
    const onReload = (event: Event) => {
      reloads.push((event as CustomEvent<Record<string, unknown>>).detail);
    };
    document.addEventListener("scribe:transcription-segment", onSegment);
    document.addEventListener("scribe:transcription-result", onResult);
    document.addEventListener("scribe:reload-annotations", onReload);
    const app = document.createElement("div");
    document.body.appendChild(app);

    await renderEditor(app);
    expect(mocks.getAnnotationPage).not.toHaveBeenCalled();
    expect(segments).toEqual([]);
    expect(results).toEqual([]);
    expect(reloads).toEqual([]);
    expect(
      document.getElementById("editor-transcription-status")?.textContent,
    ).not.toContain("Batch transcription complete");

    document.dispatchEvent(new CustomEvent("scribe:remote-rebase-ready", {
      detail: {
        canvasId: "https://example.test/canvas/1",
        itemImageId: "7",
        windowId: "scribe-editor-window",
      },
    }));
    document.dispatchEvent(new CustomEvent("scribe:transcription-overlay-state", {
      detail: {
        canvasId: "https://example.test/canvas/1",
        ready: true,
        windowId: "scribe-editor-window",
      },
    }));
    await vi.advanceTimersByTimeAsync(0);

    expect(mocks.getAnnotationPage).toHaveBeenCalledWith("7");
    expect(segments).toEqual([expect.objectContaining({
      annotation: lines[0],
      annotationId: lines[0].id,
      done: 1,
      jobId: "91",
      persisted: false,
      total: 2,
    })]);
    expect(results).toEqual([]);
    expect(reloads).toEqual([]);
    expect(
      document.getElementById("editor-transcription-status")?.textContent,
    ).toContain("Applying completed transcription: line 1/2");

    await vi.advanceTimersByTimeAsync(800);

    expect(segments.map((detail) => detail.annotationId)).toEqual([
      lines[0].id,
      lines[1].id,
      undefined,
    ]);
    expect(results).toEqual(lines.map((annotation, index) =>
      expect.objectContaining({
        annotation,
        annotationId: annotation.id,
        done: index + 1,
        jobId: "91",
        persisted: false,
        total: 2,
      })
    ));
    expect(reloads).toEqual([{
      canvasId: "https://example.test/canvas/1",
      itemImageId: "7",
      requestId: expect.any(String),
      windowId: "scribe-editor-window",
    }]);
    expect(
      document.getElementById("editor-transcription-status")?.textContent,
    ).toContain("Loading completed transcription into the editor");

    document.dispatchEvent(new CustomEvent("scribe:reload-annotations-result", {
      detail: {
        canvasId: "https://example.test/canvas/1",
        itemImageId: "7",
        ok: true,
        requestId: "wrong-reload-request",
        windowId: "scribe-editor-window",
      },
    }));
    expect(
      document.getElementById("editor-transcription-status")?.textContent,
    ).not.toContain("Updated text is now available");

    document.dispatchEvent(new CustomEvent("scribe:reload-annotations-result", {
      detail: {
        ...reloads[0],
        ok: true,
      },
    }));
    expect(
      document.getElementById("editor-transcription-status")?.textContent,
    ).toBe(
      "Batch transcription complete. Updated text is now available in the editor.",
    );

    document.dispatchEvent(new CustomEvent("scribe:transcription-overlay-state", {
      detail: {
        canvasId: "https://example.test/canvas/1",
        ready: false,
        windowId: "scribe-editor-window",
      },
    }));
    document.dispatchEvent(new CustomEvent("scribe:transcription-overlay-state", {
      detail: {
        canvasId: "https://example.test/canvas/1",
        ready: true,
        windowId: "scribe-editor-window",
      },
    }));
    await vi.advanceTimersByTimeAsync(0);

    expect(mocks.getAnnotationPage).toHaveBeenCalledTimes(1);
    expect(segments.filter((detail) => detail.annotation)).toHaveLength(2);
    expect(results).toHaveLength(2);
    expect(reloads).toHaveLength(1);

    onEvent?.({
      type: "dev.scribe.transcription.task.started",
      time: "2026-08-05T09:59:59Z",
      data: {
        annotationId: lines[0].id,
        annotationJson: JSON.stringify(lines[0]),
        completedSegments: 0,
        failedSegments: 0,
        jobId: "91",
        totalSegments: 2,
      },
    });
    expect(segments.filter((detail) => detail.annotation)).toHaveLength(2);
    expect(
      document.getElementById("editor-transcription-status")?.textContent,
    ).toBe(
      "Batch transcription complete. Updated text is now available in the editor.",
    );

    document.removeEventListener("scribe:transcription-segment", onSegment);
    document.removeEventListener("scribe:transcription-result", onResult);
    document.removeEventListener("scribe:reload-annotations", onReload);
  });

  it.each([
    {
      name: "revision",
      canvasUri: "https://example.test/canvas/1",
      revision: "21",
      items: [{
        id: "https://example.test/annotations/line-1",
        type: "Annotation",
        textGranularity: "line",
        body: { type: "TextualBody", value: "Transcribed line" },
      }],
      completedSegments: 1,
      failedSegments: 0,
      totalSegments: 1,
      readsCanonicalPage: true,
      safeReload: true,
    },
    {
      name: "Canvas",
      canvasUri: "https://example.test/canvas/other",
      revision: "20",
      items: [{
        id: "https://example.test/annotations/line-1",
        type: "Annotation",
        textGranularity: "line",
        body: { type: "TextualBody", value: "Transcribed line" },
      }],
      completedSegments: 1,
      failedSegments: 0,
      totalSegments: 1,
      readsCanonicalPage: true,
      safeReload: false,
    },
    {
      name: "line count",
      canvasUri: "https://example.test/canvas/1",
      revision: "20",
      items: [],
      completedSegments: 1,
      failedSegments: 0,
      totalSegments: 1,
      readsCanonicalPage: true,
      safeReload: false,
    },
    {
      name: "line text",
      canvasUri: "https://example.test/canvas/1",
      revision: "20",
      items: [
        {
          id: "https://example.test/annotations/line-1",
          type: "Annotation",
          textGranularity: "line",
          body: "Transcribed line",
        },
        {
          id: "https://example.test/annotations/line-2",
          type: "Annotation",
          textGranularity: "line",
          body: [{
            type: "TextualBody",
            purpose: "supplementing",
            value: "   ",
          }],
        },
      ],
      completedSegments: 1,
      failedSegments: 0,
      totalSegments: 1,
      readsCanonicalPage: true,
      safeReload: false,
    },
    {
      name: "job progress",
      canvasUri: "https://example.test/canvas/1",
      revision: "20",
      items: [{
        id: "https://example.test/annotations/line-1",
        type: "Annotation",
        textGranularity: "line",
        body: { type: "TextualBody", value: "Transcribed line" },
      }],
      completedSegments: 0,
      failedSegments: 0,
      totalSegments: 1,
      readsCanonicalPage: false,
      safeReload: false,
    },
    {
      name: "failed-segment count",
      canvasUri: "https://example.test/canvas/1",
      revision: "20",
      items: [{
        id: "https://example.test/annotations/line-1",
        type: "Annotation",
        textGranularity: "line",
        body: { type: "TextualBody", value: "Transcribed line" },
      }],
      completedSegments: 0,
      failedSegments: 1,
      totalSegments: 1,
      readsCanonicalPage: false,
      safeReload: false,
    },
  ])("does not replay stale or incomplete canonical text when $name does not match", async (testCase) => {
    vi.useFakeTimers();
    window.history.replaceState(
      {},
      "",
      "/editor?itemImageId=7&jobId=91",
    );
    mocks.getOCRRun.mockResolvedValue({
      contextId: 33n,
      itemImageId: 7n,
      model: "test-model",
      imageUrl: "https://example.test/page.jpg",
    });
    mocks.getTranscriptionJob.mockResolvedValue({
      id: 91n,
      itemImageId: 7n,
      status: "completed",
      completedSegments: testCase.completedSegments,
      failedSegments: testCase.failedSegments,
      totalSegments: testCase.totalSegments,
      attempts: [{
        attemptNumber: 1,
        jobId: 91n,
        outcome: TranscriptionJobAttemptOutcome.COMPLETED,
        resultRevision: 20n,
      }],
    });
    mocks.getAnnotationPage.mockResolvedValue({
      canvasUri: testCase.canvasUri,
      page: {
        id: "https://scribe.test/pages/7",
        type: "AnnotationPage",
        items: testCase.items,
      },
      revision: testCase.revision,
      updatedAt: "2026-08-05T10:00:00Z",
    });
    const segments: Array<Record<string, unknown>> = [];
    const results: Array<Record<string, unknown>> = [];
    const batchStates: Array<Record<string, unknown>> = [];
    let reloads = 0;
    const onSegment = (event: Event) => {
      segments.push((event as CustomEvent<Record<string, unknown>>).detail);
    };
    const onResult = (event: Event) => {
      results.push((event as CustomEvent<Record<string, unknown>>).detail);
    };
    const onBatchState = (event: Event) => {
      batchStates.push((event as CustomEvent<Record<string, unknown>>).detail);
    };
    const onReload = (event: Event) => {
      reloads += 1;
      const detail = (
        event as CustomEvent<Record<string, unknown>>
      ).detail;
      if (typeof detail.requestId === "string") {
        document.dispatchEvent(new CustomEvent("scribe:reload-annotations-result", {
          detail: { ...detail, ok: true },
        }));
      }
    };
    document.addEventListener("scribe:transcription-segment", onSegment);
    document.addEventListener("scribe:transcription-result", onResult);
    document.addEventListener("scribe:transcription-job-state", onBatchState);
    document.addEventListener("scribe:reload-annotations", onReload);
    const app = document.createElement("div");
    document.body.appendChild(app);

    await renderEditor(app);
    document.dispatchEvent(new CustomEvent("scribe:remote-rebase-ready", {
      detail: {
        canvasId: "https://example.test/canvas/1",
        itemImageId: "7",
        windowId: "scribe-editor-window",
      },
    }));
    document.dispatchEvent(new CustomEvent("scribe:transcription-overlay-state", {
      detail: {
        canvasId: "https://example.test/canvas/1",
        ready: true,
        windowId: "scribe-editor-window",
      },
    }));
    await vi.advanceTimersByTimeAsync(0);

    expect(segments.filter((detail) => detail.annotation)).toEqual([]);
    expect(results).toEqual([]);
    if (testCase.readsCanonicalPage) {
      expect(mocks.getAnnotationPage).toHaveBeenCalled();
    } else {
      expect(mocks.getAnnotationPage).not.toHaveBeenCalled();
    }
    expect(reloads).toBe(testCase.safeReload ? 1 : 0);
    if (testCase.safeReload) {
      expect(
        document.getElementById("editor-transcription-status")?.textContent,
      ).toBe(
        "Batch transcription complete. Updated text is now available in the editor.",
      );
    } else {
      expect(
        document.getElementById("editor-transcription-status")?.textContent,
      ).toContain("could not be safely applied");
      expect(batchStates.at(-1)).toMatchObject({ active: true });
      expect(
        document.getElementById("editor-batch-banner")?.classList.contains("hidden"),
      ).toBe(false);
      expect(
        document.getElementById("editor-batch-banner-title")?.textContent,
      ).toContain("reload required");
    }

    document.removeEventListener("scribe:transcription-segment", onSegment);
    document.removeEventListener("scribe:transcription-result", onResult);
    document.removeEventListener("scribe:transcription-job-state", onBatchState);
    document.removeEventListener("scribe:reload-annotations", onReload);
  });

  it("keeps a completed job active and retries when its canonical page cannot load", async () => {
    vi.useFakeTimers();
    window.history.replaceState(
      {},
      "",
      "/editor?itemImageId=7&jobId=91",
    );
    mocks.getOCRRun.mockResolvedValue({
      contextId: 33n,
      itemImageId: 7n,
      model: "test-model",
      imageUrl: "https://example.test/page.jpg",
    });
    mocks.getTranscriptionJob.mockResolvedValue({
      id: 91n,
      itemImageId: 7n,
      status: "completed",
      completedSegments: 1,
      failedSegments: 0,
      totalSegments: 1,
      attempts: [{
        attemptNumber: 1,
        jobId: 91n,
        outcome: TranscriptionJobAttemptOutcome.COMPLETED,
        resultRevision: 20n,
      }],
    });
    mocks.getAnnotationPage.mockRejectedValue(new Error("temporarily unavailable"));
    const segments: Array<Record<string, unknown>> = [];
    let reloads = 0;
    const onSegment = (event: Event) => {
      segments.push((event as CustomEvent<Record<string, unknown>>).detail);
    };
    const onReload = () => {
      reloads += 1;
    };
    document.addEventListener("scribe:transcription-segment", onSegment);
    document.addEventListener("scribe:reload-annotations", onReload);
    const app = document.createElement("div");
    document.body.appendChild(app);

    await renderEditor(app);
    document.dispatchEvent(new CustomEvent("scribe:remote-rebase-ready", {
      detail: {
        canvasId: "https://example.test/canvas/1",
        itemImageId: "7",
        windowId: "scribe-editor-window",
      },
    }));
    document.dispatchEvent(new CustomEvent("scribe:transcription-overlay-state", {
      detail: {
        canvasId: "https://example.test/canvas/1",
        ready: true,
        windowId: "scribe-editor-window",
      },
    }));
    await vi.advanceTimersByTimeAsync(0);

    const initialLoadAttempts = mocks.getAnnotationPage.mock.calls.length;
    expect(initialLoadAttempts).toBeGreaterThan(0);
    expect(segments.filter((detail) => detail.annotation)).toEqual([]);
    expect(reloads).toBe(0);
    expect(
      document.getElementById("editor-transcription-status")?.textContent,
    ).toContain("will retry");
    expect(
      document.getElementById("editor-transcription-status")?.textContent,
    ).not.toContain("Batch transcription complete");

    await vi.advanceTimersByTimeAsync(1_999);
    expect(mocks.getAnnotationPage).toHaveBeenCalledTimes(initialLoadAttempts);
    await vi.advanceTimersByTimeAsync(1);
    expect(mocks.getAnnotationPage.mock.calls.length).toBeGreaterThan(
      initialLoadAttempts,
    );
    expect(reloads).toBe(0);

    document.removeEventListener("scribe:transcription-segment", onSegment);
    document.removeEventListener("scribe:reload-annotations", onReload);
  });

  it("cancels a completed replay when its overlay unmounts and resumes from the last visible result", async () => {
    vi.useFakeTimers();
    window.history.replaceState(
      {},
      "",
      "/editor?itemImageId=7&jobId=91",
    );
    mocks.getOCRRun.mockResolvedValue({
      contextId: 33n,
      itemImageId: 7n,
      model: "test-model",
      imageUrl: "https://example.test/page.jpg",
    });
    mocks.getTranscriptionJob.mockResolvedValue({
      id: 91n,
      itemImageId: 7n,
      status: "completed",
      completedSegments: 1,
      failedSegments: 0,
      totalSegments: 1,
      attempts: [{
        attemptNumber: 1,
        jobId: 91n,
        outcome: TranscriptionJobAttemptOutcome.COMPLETED,
        resultRevision: 20n,
      }],
    });
    const line = {
      id: "https://example.test/annotations/line-1",
      type: "Annotation",
      textGranularity: "line",
      body: { type: "TextualBody", value: "Transcribed line" },
    };
    mocks.getAnnotationPage.mockResolvedValue({
      canvasUri: "https://example.test/canvas/1",
      page: {
        id: "https://scribe.test/pages/7",
        type: "AnnotationPage",
        items: [line],
      },
      revision: "20",
      updatedAt: "2026-08-05T10:00:00Z",
    });
    const segments: Array<Record<string, unknown>> = [];
    const results: Array<Record<string, unknown>> = [];
    let reloads = 0;
    const onSegment = (event: Event) => {
      segments.push((event as CustomEvent<Record<string, unknown>>).detail);
    };
    const onResult = (event: Event) => {
      results.push((event as CustomEvent<Record<string, unknown>>).detail);
    };
    const onReload = () => {
      reloads += 1;
    };
    document.addEventListener("scribe:transcription-segment", onSegment);
    document.addEventListener("scribe:transcription-result", onResult);
    document.addEventListener("scribe:reload-annotations", onReload);
    const app = document.createElement("div");
    document.body.appendChild(app);

    await renderEditor(app);
    document.dispatchEvent(new CustomEvent("scribe:remote-rebase-ready", {
      detail: {
        canvasId: "https://example.test/canvas/1",
        itemImageId: "7",
        windowId: "scribe-editor-window",
      },
    }));
    document.dispatchEvent(new CustomEvent("scribe:transcription-overlay-state", {
      detail: {
        canvasId: "https://example.test/canvas/1",
        ready: true,
        windowId: "scribe-editor-window",
      },
    }));
    await vi.advanceTimersByTimeAsync(0);
    expect(segments.filter((detail) => detail.annotation)).toHaveLength(1);
    expect(results).toEqual([]);

    document.dispatchEvent(new CustomEvent("scribe:transcription-overlay-state", {
      detail: {
        canvasId: "https://example.test/canvas/1",
        ready: false,
        windowId: "scribe-editor-window",
      },
    }));
    await vi.advanceTimersByTimeAsync(5_000);

    expect(results).toEqual([]);
    expect(reloads).toBe(0);
    expect(
      document.getElementById("editor-transcription-status")?.textContent,
    ).toContain("Waiting for the editor overlay");

    document.dispatchEvent(new CustomEvent("scribe:transcription-overlay-state", {
      detail: {
        canvasId: "https://example.test/canvas/1",
        ready: true,
        windowId: "scribe-editor-window",
      },
    }));
    await vi.advanceTimersByTimeAsync(0);
    expect(mocks.getAnnotationPage).toHaveBeenCalledTimes(2);
    await vi.runAllTimersAsync();

    expect(segments.filter((detail) => detail.annotation)).toHaveLength(2);
    expect(results).toEqual([expect.objectContaining({
      annotation: line,
      annotationId: line.id,
      done: 1,
      jobId: "91",
      persisted: false,
      total: 1,
    })]);
    expect(reloads).toBe(1);

    document.removeEventListener("scribe:transcription-segment", onSegment);
    document.removeEventListener("scribe:transcription-result", onResult);
    document.removeEventListener("scribe:reload-annotations", onReload);
  });

  it("clears the active wand when a durable transcription job is canceled", async () => {
    window.history.replaceState({}, "", "/editor?itemImageId=7&jobId=91");
    mocks.getOCRRun.mockResolvedValue({
      contextId: 33n,
      itemImageId: 7n,
      model: "test-model",
      imageUrl: "https://example.test/page.jpg",
    });
    const annotation = {
      id: "https://example.test/annotations/line-1",
      type: "Annotation",
      textGranularity: "line",
    };
    let jobStatus = "running";
    mocks.getTranscriptionJob.mockImplementation(async () => ({
      id: 91n,
      itemImageId: 7n,
      status: jobStatus,
      completedSegments: 0,
      failedSegments: 0,
      totalSegments: 7,
      currentAnnotationId: jobStatus === "running" ? annotation.id : "",
      currentAnnotationJson: jobStatus === "running"
        ? JSON.stringify(annotation)
        : "",
      updatedAt: "2026-08-05T10:00:00Z",
    }));
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
    const segments: Array<Record<string, unknown>> = [];
    const onSegment = (event: Event) => {
      segments.push((event as CustomEvent<Record<string, unknown>>).detail);
    };
    document.addEventListener("scribe:transcription-segment", onSegment);
    const app = document.createElement("div");
    document.body.appendChild(app);

    await renderEditor(app);
    document.dispatchEvent(new CustomEvent("scribe:transcription-overlay-state", {
      detail: {
        canvasId: "https://example.test/canvas/1",
        ready: true,
        windowId: "scribe-editor-window",
      },
    }));
    await vi.waitFor(() => expect(segments.at(-1)?.annotation).toEqual(annotation));

    jobStatus = "canceled";
    onEvent?.({
      type: "dev.scribe.transcription.canceled",
      time: "2026-08-05T10:00:01Z",
      data: { itemImageId: "7", jobId: "91" },
    });

    await vi.waitFor(() => {
      expect(segments.at(-1)?.annotation).toBeNull();
      expect(
        document.getElementById("editor-transcription-status")?.textContent,
      ).toBe("Automatic transcription was canceled.");
    });
    document.removeEventListener("scribe:transcription-segment", onSegment);
  });

  it("loads the full active job after latest-job summary discovery", async () => {
    window.history.replaceState({}, "", "/editor?itemImageId=7");
    mocks.getOCRRun.mockResolvedValue({
      contextId: 33n,
      itemImageId: 7n,
      model: "test-model",
      imageUrl: "https://example.test/page.jpg",
    });
    const annotation = {
      id: "https://example.test/annotations/line-1",
      type: "Annotation",
      textGranularity: "line",
    };
    mocks.listTranscriptionJobs.mockResolvedValue([{
      id: 92n,
      itemImageId: 7n,
      status: "running",
      completedSegments: 0,
      failedSegments: 0,
      totalSegments: 4,
    }]);
    mocks.getTranscriptionJob.mockResolvedValue({
      id: 92n,
      itemImageId: 7n,
      status: "running",
      completedSegments: 0,
      failedSegments: 0,
      totalSegments: 4,
      currentAnnotationId: annotation.id,
      currentAnnotationJson: JSON.stringify(annotation),
      updatedAt: "2026-08-05T10:00:00Z",
    });
    const segment = new Promise<CustomEvent>((resolve) => {
      document.addEventListener(
        "scribe:transcription-segment",
        (event) => resolve(event as CustomEvent),
        { once: true },
      );
    });
    const app = document.createElement("div");
    document.body.appendChild(app);

    await renderEditor(app);
    expect(mocks.getTranscriptionJob).toHaveBeenCalledWith(92n);
    document.dispatchEvent(new CustomEvent("scribe:transcription-overlay-state", {
      detail: {
        canvasId: "https://example.test/canvas/1",
        ready: true,
        windowId: "scribe-editor-window",
      },
    }));

    await expect(segment).resolves.toMatchObject({
      detail: {
        annotation,
        done: 1,
        total: 4,
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

  it("fails closed when the latest completed job reload is negatively acknowledged", async () => {
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
    mocks.getTranscriptionJob.mockResolvedValue({
      id: 91n,
      itemImageId: 7n,
      status: "completed",
      completedSegments: 3,
      failedSegments: 0,
      totalSegments: 3,
    });
    const app = document.createElement("div");
    document.body.appendChild(app);
    const reloads: Array<Record<string, unknown>> = [];
    const batchStates: Array<Record<string, unknown>> = [];
    const onReload = (event: Event) => {
      reloads.push((event as CustomEvent<Record<string, unknown>>).detail);
    };
    const onBatchState = (event: Event) => {
      batchStates.push((event as CustomEvent<Record<string, unknown>>).detail);
    };
    document.addEventListener("scribe:reload-annotations", onReload);
    document.addEventListener("scribe:transcription-job-state", onBatchState);

    await renderEditor(app);

    expect(mocks.viewer).toHaveBeenCalled();
    expect(reloads).toEqual([]);

    document.dispatchEvent(new CustomEvent("scribe:remote-rebase-ready", {
      detail: {
        canvasId: "https://example.test/canvas/1",
        itemImageId: "7",
        windowId: "scribe-editor-window",
      },
    }));

    expect(reloads).toEqual([expect.objectContaining({
      canvasId: "https://example.test/canvas/1",
      itemImageId: "7",
      requestId: expect.any(String),
      windowId: "scribe-editor-window",
    })]);
    expect(
      document.getElementById("editor-transcription-status")?.textContent,
    ).toContain("Loading completed transcription into the editor");

    document.dispatchEvent(new CustomEvent("scribe:reload-annotations-result", {
      detail: { ...reloads[0], ok: false },
    }));
    expect(
      document.getElementById("editor-transcription-status")?.textContent,
    ).toContain("could not be loaded into the editor");
    expect(
      document.getElementById("editor-transcription-status")?.textContent,
    ).not.toContain("Updated text is now available");
    expect(batchStates.at(-1)).toMatchObject({ active: true });
    expect(
      document.getElementById("editor-batch-banner")?.classList.contains("hidden"),
    ).toBe(false);
    expect(
      document.getElementById("editor-batch-banner-title")?.textContent,
    ).toContain("reload required");
    document.removeEventListener("scribe:reload-annotations", onReload);
    document.removeEventListener("scribe:transcription-job-state", onBatchState);
  });

  it("starts the completed reload timeout at dispatch and stays blocked after timeout", async () => {
    vi.useFakeTimers();
    window.history.replaceState({}, "", "/editor?itemImageId=7");
    mocks.getOCRRun.mockResolvedValue({
      contextId: 0n,
      itemImageId: 7n,
      model: "test-model",
      imageUrl: "https://example.test/page.jpg",
    });
    mocks.listTranscriptionJobs.mockResolvedValue([{
      id: 91n,
      status: "completed",
      completedSegments: 3,
      failedSegments: 0,
      totalSegments: 3,
    }]);
    mocks.getTranscriptionJob.mockResolvedValue({
      id: 91n,
      itemImageId: 7n,
      status: "completed",
      completedSegments: 3,
      failedSegments: 0,
      totalSegments: 3,
    });
    const reloads: Array<Record<string, unknown>> = [];
    const batchStates: Array<Record<string, unknown>> = [];
    const onReload = (event: Event) => {
      reloads.push((event as CustomEvent<Record<string, unknown>>).detail);
    };
    const onBatchState = (event: Event) => {
      batchStates.push((event as CustomEvent<Record<string, unknown>>).detail);
    };
    document.addEventListener("scribe:reload-annotations", onReload);
    document.addEventListener("scribe:transcription-job-state", onBatchState);
    const app = document.createElement("div");
    document.body.appendChild(app);

    await renderEditor(app);
    await vi.advanceTimersByTimeAsync(60_000);
    expect(reloads).toEqual([]);
    expect(
      document.getElementById("editor-transcription-status")?.textContent,
    ).toContain("Waiting for the editor to load it");
    expect(batchStates.at(-1)).toMatchObject({ active: true });

    document.dispatchEvent(new CustomEvent("scribe:remote-rebase-ready", {
      detail: {
        canvasId: "https://example.test/canvas/1",
        itemImageId: "7",
        windowId: "scribe-editor-window",
      },
    }));
    await vi.advanceTimersByTimeAsync(14_999);
    expect(reloads).toHaveLength(1);
    expect(
      document.getElementById("editor-transcription-status")?.textContent,
    ).toContain("Loading completed transcription into the editor");

    await vi.advanceTimersByTimeAsync(1);
    expect(
      document.getElementById("editor-transcription-status")?.textContent,
    ).toContain("could not be loaded into the editor");
    expect(batchStates.at(-1)).toMatchObject({ active: true });
    expect(
      document.getElementById("editor-batch-banner")?.classList.contains("hidden"),
    ).toBe(false);

    document.dispatchEvent(new CustomEvent("scribe:reload-annotations-result", {
      detail: { ...reloads[0], ok: true },
    }));
    expect(
      document.getElementById("editor-transcription-status")?.textContent,
    ).not.toContain("Updated text is now available");

    document.dispatchEvent(new CustomEvent("scribe:transcription-overlay-state", {
      detail: {
        canvasId: "https://example.test/canvas/1",
        ready: true,
        windowId: "scribe-editor-window",
      },
    }));
    await vi.advanceTimersByTimeAsync(0);
    expect(reloads).toHaveLength(1);
    expect(
      document.getElementById("editor-transcription-status")?.textContent,
    ).toContain("could not be loaded into the editor");
    document.removeEventListener("scribe:reload-annotations", onReload);
    document.removeEventListener("scribe:transcription-job-state", onBatchState);
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
    mocks.getTranscriptionJob.mockResolvedValue({
      id: 91n,
      itemImageId: 7n,
      status: "failed",
      completedSegments: 0,
      failedSegments: 0,
      totalSegments: 3,
      errorMessage: "workspace provider credential is not configured",
    });
    const app = document.createElement("div");
    document.body.appendChild(app);

    await renderEditor(app);

    expect(
      document.getElementById("editor-transcription-status")?.textContent,
    ).toContain("workspace provider credential is not configured");
    expect(
      document.getElementById("editor-transcription-status")?.textContent,
    ).not.toContain("Preparing batch transcription");
    expect(mocks.getTranscriptionJob).toHaveBeenCalledOnce();
    expect(mocks.getTranscriptionJob).toHaveBeenCalledWith(91n);
    expect(mocks.listTranscriptionJobs).not.toHaveBeenCalled();
  });

  it("reconciles an exact running job when a successor event supersedes it", async () => {
    window.history.replaceState(
      {},
      "",
      "/editor?itemImageId=7&jobId=91",
    );
    mocks.getOCRRun.mockResolvedValue({
      contextId: 33n,
      itemImageId: 7n,
      model: "test-model",
      imageUrl: "https://example.test/page.jpg",
    });
    let exactStatus = "running";
    mocks.getTranscriptionJob.mockImplementation(async () => ({
      id: 91n,
      itemImageId: 7n,
      status: exactStatus,
      completedSegments: 0,
      failedSegments: 0,
      totalSegments: 3,
    }));
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

    await renderEditor(app);
    expect(
      document.getElementById("editor-transcription-status")?.textContent,
    ).toContain("Batch transcription is running");
    exactStatus = "superseded";
    onEvent?.({
      type: "dev.scribe.transcription.task.started",
      time: "2026-08-05T10:00:01Z",
      data: {
        completedSegments: 0,
        failedSegments: 0,
        jobId: "92",
        totalSegments: 3,
      },
    });

    await vi.waitFor(() => {
      expect(mocks.getTranscriptionJob).toHaveBeenCalledTimes(2);
      expect(
        document.getElementById("editor-transcription-status")?.textContent,
      ).toBe("Batch transcription failed.");
    });
    expect(mocks.getTranscriptionJob).toHaveBeenLastCalledWith(91n);
    expect(mocks.listTranscriptionJobs).not.toHaveBeenCalled();
  });

  it("adopts the successor transcription job after full reprocessing", async () => {
    window.history.replaceState(
      {},
      "",
      "/editor?itemImageId=7&jobId=91",
    );
    mocks.getOCRRun.mockResolvedValue({
      contextId: 33n,
      itemImageId: 7n,
      model: "test-model",
      imageUrl: "https://example.test/page.jpg",
    });
    let successorStatus = "pending";
    mocks.getTranscriptionJob.mockImplementation(async (jobID: bigint) => ({
      id: jobID,
      itemImageId: 7n,
      status: jobID === 92n ? successorStatus : "completed",
      completedSegments: jobID === 92n && successorStatus === "completed" ? 1 : 0,
      failedSegments: 0,
      totalSegments: 1,
    }));
    mocks.reprocessItemImage.mockResolvedValue({
      itemImageId: 7n,
      transcriptionJobId: 92n,
    });
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
    const onReload = (event: Event) => {
      const detail = (
        event as CustomEvent<Record<string, unknown>>
      ).detail;
      if (typeof detail.requestId !== "string") return;
      document.dispatchEvent(new CustomEvent("scribe:reload-annotations-result", {
        detail: { ...detail, ok: true },
      }));
    };
    document.addEventListener("scribe:reload-annotations", onReload);

    await renderEditor(app);
    document.dispatchEvent(new CustomEvent("scribe:remote-rebase-ready", {
      detail: {
        canvasId: "https://example.test/canvas/1",
        itemImageId: "7",
        windowId: "scribe-editor-window",
      },
    }));
    document.getElementById("reprocess-nav")?.click();

    await vi.waitFor(() => {
      expect(new URL(window.location.href).searchParams.get("jobId"))
        .toBe("92");
      expect(mocks.getTranscriptionJob).toHaveBeenCalledWith(92n);
    });
    successorStatus = "completed";
    onEvent?.({
      type: "dev.scribe.transcription.completed",
      time: "2026-08-05T00:00:00Z",
      data: {
        completedSegments: 1,
        failedSegments: 0,
        jobId: "92",
        totalSegments: 1,
      },
    });

    await vi.waitFor(() => {
      expect(
        document.getElementById("editor-transcription-status")?.textContent,
      ).toBe(
        "Batch transcription complete. Updated text is now available in the editor.",
      );
    });
    expect(mocks.listTranscriptionJobs).not.toHaveBeenCalled();
    document.removeEventListener("scribe:reload-annotations", onReload);
  });

  it("pins a successor job to the reprocessed Canvas after switching pages", async () => {
    window.history.replaceState(
      {},
      "",
      "/editor?itemId=5&itemImageId=101&jobId=91",
    );
    mocks.getOCRRun.mockImplementation(async (targetItemImageID: string) => ({
      contextId: 33n,
      itemImageId: BigInt(targetItemImageID),
      model: "test-model",
      imageUrl: `https://example.test/page-${targetItemImageID}.jpg`,
    }));
    let successorStatus = "pending";
    mocks.getTranscriptionJob.mockImplementation(async (jobID: bigint) => ({
      id: jobID,
      itemImageId: jobID === 91n ? 101n : 202n,
      status: jobID === 92n ? successorStatus : "running",
      completedSegments: jobID === 92n && successorStatus === "completed" ? 1 : 0,
      failedSegments: 0,
      totalSegments: 1,
    }));
    mocks.listTranscriptionJobs.mockResolvedValue([{
      id: 80n,
      itemImageId: 202n,
      status: "pending",
      completedSegments: 0,
      failedSegments: 0,
      totalSegments: 1,
    }]);
    mocks.getEditorManifest.mockResolvedValue({
      item: {
        id: "5",
        images: [
          { id: 101n, canvasUri: "https://iiif.example/canvas/a" },
          { id: 202n, canvasUri: "https://iiif.example/canvas/b" },
        ],
      },
      manifestJSON: "{}",
      selectedCanvasId: "https://iiif.example/canvas/a",
    });
    mocks.reprocessItemImage.mockResolvedValue({
      itemImageId: 202n,
      transcriptionJobId: 92n,
    });
    const eventHandlers = new Map<string, (event: {
      type: string;
      data?: Record<string, unknown>;
      time?: string;
    }) => void>();
    mocks.subscribeToEvents.mockImplementation((filter, handler) => {
      eventHandlers.set(filter.itemImageId, handler);
      return { close: vi.fn() };
    });
    const app = document.createElement("div");
    document.body.appendChild(app);
    const onReload = (event: Event) => {
      const detail = (
        event as CustomEvent<Record<string, unknown>>
      ).detail;
      if (typeof detail.requestId !== "string") return;
      document.dispatchEvent(new CustomEvent("scribe:reload-annotations-result", {
        detail: { ...detail, ok: true },
      }));
    };
    document.addEventListener("scribe:reload-annotations", onReload);

    await renderEditor(app);
    document.dispatchEvent(new CustomEvent("scribe:active-canvas", {
      detail: {
        canvasId: "https://iiif.example/canvas/b",
        itemImageId: "202",
        windowId: "scribe-editor-window",
      },
    }));
    await vi.waitFor(() => {
      expect(mocks.listTranscriptionJobs).toHaveBeenCalledWith(202n);
    });
    document.dispatchEvent(new CustomEvent("scribe:remote-rebase-ready", {
      detail: {
        canvasId: "https://iiif.example/canvas/b",
        itemImageId: "202",
        windowId: "scribe-editor-window",
      },
    }));

    document.getElementById("reprocess-nav")?.click();
    await vi.waitFor(() => {
      expect(mocks.reprocessItemImage).toHaveBeenCalledWith("202", "33", "12");
      expect(mocks.getTranscriptionJob).toHaveBeenCalledWith(92n);
      expect(new URL(window.location.href).searchParams.get("jobId")).toBe("92");
    });

    const onPageBEvent = eventHandlers.get("202");
    expect(onPageBEvent).toBeDefined();
    onPageBEvent?.({
      type: "dev.scribe.transcription.completed",
      time: "2026-08-05T00:00:00Z",
      data: {
        completedSegments: 1,
        failedSegments: 0,
        jobId: "80",
        totalSegments: 1,
      },
    });
    expect(
      document.getElementById("editor-transcription-status")?.textContent,
    ).not.toBe(
      "Batch transcription complete. Updated text is now available in the editor.",
    );

    successorStatus = "completed";
    onPageBEvent?.({
      type: "dev.scribe.transcription.completed",
      time: "2026-08-05T00:00:01Z",
      data: {
        completedSegments: 1,
        failedSegments: 0,
        jobId: "92",
        totalSegments: 1,
      },
    });
    await vi.waitFor(() => {
      expect(
        document.getElementById("editor-transcription-status")?.textContent,
      ).toBe(
        "Batch transcription complete. Updated text is now available in the editor.",
      );
    });
    document.removeEventListener("scribe:reload-annotations", onReload);
  });

  it("fails closed when an exact transcription job belongs to another item image", async () => {
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
    mocks.getTranscriptionJob.mockResolvedValue({
      id: 91n,
      itemImageId: 8n,
      status: "pending",
      completedSegments: 0,
      failedSegments: 0,
      totalSegments: 0,
    });
    const app = document.createElement("div");
    document.body.appendChild(app);

    await renderEditor(app);

    expect(document.querySelector('#mirador-viewer [role="alert"]')?.textContent)
      .toContain("transcription job belongs to a different item image");
    expect(mocks.viewer).not.toHaveBeenCalled();
    expect(mocks.listTranscriptionJobs).not.toHaveBeenCalled();
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
    mocks.getTranscriptionJob
      .mockResolvedValueOnce({
        id: 91n,
        itemImageId: 7n,
        status: "running",
        completedSegments: 2,
        failedSegments: 0,
        totalSegments: 3,
      })
      .mockResolvedValueOnce({
        id: 91n,
        itemImageId: 7n,
        status: "running",
        completedSegments: 2,
        failedSegments: 0,
        totalSegments: 3,
      })
      .mockResolvedValueOnce({
        id: 91n,
        itemImageId: 7n,
        status: "completed",
        completedSegments: 3,
        failedSegments: 0,
        totalSegments: 3,
      });
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
    document.dispatchEvent(new CustomEvent("scribe:remote-rebase-ready", {
      detail: {
        canvasId: "https://example.test/canvas/1",
        itemImageId: "7",
        windowId: "scribe-editor-window",
      },
    }));
    await vi.waitFor(() => expect(mocks.listTranscriptionJobs).toHaveBeenCalledTimes(2));
    expect(reloads).toBe(0);
    onReady?.();
    await vi.waitFor(() => expect(mocks.listTranscriptionJobs).toHaveBeenCalledTimes(3));
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
    document.dispatchEvent(new CustomEvent("scribe:remote-rebase-ready", {
      detail: {
        canvasId: "https://example.test/canvas/1",
        itemImageId: "8",
        windowId: "scribe-editor-window",
      },
    }));
    await vi.waitFor(() => {
      expect(mocks.listTranscriptionJobs).toHaveBeenCalledTimes(2);
    });
    mocks.listTranscriptionJobs.mockResolvedValue([{
      id: 92n,
      itemImageId: 8n,
      status: "completed",
      completedSegments: 2,
      failedSegments: 0,
      totalSegments: 2,
    }]);
    mocks.getTranscriptionJob.mockResolvedValue({
      id: 92n,
      itemImageId: 8n,
      status: "completed",
      completedSegments: 2,
      failedSegments: 0,
      totalSegments: 2,
      attempts: [],
    });
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

    expect(reloads).toBe(0);
    await vi.waitFor(() => expect(reloads).toBe(1));
  });
});
