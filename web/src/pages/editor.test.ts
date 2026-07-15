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
}));

vi.mock("mirador", () => ({
  default: { viewer: mocks.viewer },
}));

vi.mock("mirador-scribe", () => ({
  default: [],
  annotationAdapters: {
    ScribeAnnotationAdapter: vi.fn(),
  },
}));

vi.mock("../api/annotations", () => ({
  annotationClient: {},
  publishItemImageEdits: mocks.publishItemImageEdits,
}));

vi.mock("../api/processing", () => ({
  getOCRRun: mocks.getOCRRun,
  reprocessItemImage: mocks.reprocessItemImage,
}));

vi.mock("../api/transcription", () => ({
  listTranscriptionJobs: mocks.listTranscriptionJobs,
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
    mocks.subscribeToEvents.mockReturnValue({ close: vi.fn() });
  });

  afterEach(() => {
    window.dispatchEvent(new Event("pagehide"));
  });

  it("requests a save before leaving a dirty editor", async () => {
    const app = document.createElement("div");
    document.body.appendChild(app);
    await renderEditor(app);

    document.dispatchEvent(new CustomEvent("scribe:dirty-state", { detail: { dirty: true } }));
    document.getElementById("home-nav")?.click();
    expect(document.getElementById("leave-dialog")?.classList.contains("flex")).toBe(true);

    let saveRequested = false;
    document.addEventListener("scribe:request-save", (event) => {
      saveRequested = true;
      const detail = (event as CustomEvent<{ requestId: string }>).detail;
      document.dispatchEvent(new CustomEvent("scribe:save-result", {
        detail: { ok: true, requestId: detail.requestId },
      }));
    }, { once: true });

    document.getElementById("leave-save")?.click();
    await vi.waitFor(() => expect(saveRequested).toBe(true));
    await vi.waitFor(() => expect(window.location.pathname).toBe("/"));
  });

  it("stays in the editor when save-before-leave fails", async () => {
    const app = document.createElement("div");
    document.body.appendChild(app);
    await renderEditor(app);

    document.dispatchEvent(new CustomEvent("scribe:dirty-state", { detail: { dirty: true } }));
    document.getElementById("home-nav")?.click();
    expect(document.getElementById("leave-dialog")?.classList.contains("flex")).toBe(true);

    document.addEventListener("scribe:request-save", (event) => {
      const detail = (event as CustomEvent<{ requestId: string }>).detail;
      document.dispatchEvent(new CustomEvent("scribe:save-result", {
        detail: { ok: false, requestId: detail.requestId },
      }));
    }, { once: true });

    document.getElementById("leave-save")?.click();
    await vi.waitFor(() => expect((document.getElementById("leave-save") as HTMLButtonElement | null)?.disabled).toBe(false));
    expect(window.location.pathname).toBe("/editor");
    expect(document.getElementById("leave-dialog")?.classList.contains("flex")).toBe(true);
  });

  it("reloads annotations when the latest loaded transcription job is complete", async () => {
    window.history.replaceState({}, "", "/editor?itemImageId=7");
    mocks.getOCRRun.mockResolvedValue({
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

  it("routes completed transcription events into Mirador reload events", async () => {
    window.history.replaceState({}, "", "/editor?itemImageId=8");
    mocks.getOCRRun.mockResolvedValue({
      itemImageId: 8n,
      model: "test-model",
      imageUrl: "https://example.test/page.jpg",
    });
    mocks.listTranscriptionJobs.mockResolvedValue([]);
    let onEvent: ((event: { type: string; data?: Record<string, unknown>; time?: string }) => void) | undefined;
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
