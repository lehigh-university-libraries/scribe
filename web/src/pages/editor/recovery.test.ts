// @vitest-environment happy-dom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { renderEditorRecovery } from "./recovery";

describe("renderEditorRecovery", () => {
  beforeEach(() => {
    document.body.innerHTML = '<p id="meta"></p><div id="viewer"></div>';
    window.history.replaceState({}, "", "/editor?workspace_id=17");
  });

  it("announces the failure and provides workspace-aware recovery actions", () => {
    const retry = vi.fn();
    const meta = document.getElementById("meta");
    const viewer = document.getElementById("viewer");
    if (!(meta instanceof HTMLElement) || !(viewer instanceof HTMLElement)) {
      throw new Error("test recovery elements are missing");
    }

    renderEditorRecovery(meta, viewer, {
      message: "Failed to load OCR run.",
      retry,
    });

    expect(meta.getAttribute("role")).toBe("alert");
    expect(meta.getAttribute("aria-live")).toBe("assertive");
    expect(viewer.querySelector('[role="alert"]')?.textContent).toContain("Failed to load OCR run.");
    expect(viewer.querySelector('a')?.getAttribute("href")).toBe("/?workspace_id=17");
    (viewer.querySelector("#editor-recovery-retry") as HTMLButtonElement).click();
    expect(retry).toHaveBeenCalledOnce();
  });
});
