import { describe, expect, it, vi } from "vitest";

import type {
  ScribeAnnotationAdapterConstructor,
  ScribeAnnotationClient,
} from "mirador-scribe";
import {
  bottomPaneHeightForViewport,
  commonViewerOptions,
} from "./mirador";

describe("commonViewerOptions", () => {
  it("lets companion content fill and scroll within its remaining height", () => {
    const options = commonViewerOptions(
      "https://scribe.example",
      vi.fn() as unknown as ScribeAnnotationAdapterConstructor,
      {} as ScribeAnnotationClient,
      () => ({ contextId: "1", itemImageId: "2", windowId: "window-1" }),
      { ajaxWithCredentials: false, crossOriginPolicy: "Anonymous" },
      320,
    );

    expect(options.theme.components.CompanionWindow.styleOverrides.contents).toEqual({
      display: "flex",
      flex: "1 1 auto",
      flexDirection: "column",
      minHeight: 0,
      overflowY: "auto",
    });
    expect(options.theme.components.CompanionWindow.styleOverrides.resize({
      ownerState: {
        defaultSidebarPanelHeight: 320,
        position: "bottom",
      },
    })).toMatchObject({ height: "320px !important" });
    expect(options.theme.components.CompanionWindow.styleOverrides.resize({
      ownerState: {
        defaultSidebarPanelHeight: 320,
        position: "right",
      },
    })).not.toHaveProperty("height");
    expect(options.window.defaultSidebarPanelHeight).toBe(320);
  });

  it.each([
    [{ width: 360, height: 590 }, 360, 170],
    [{ width: 667, height: 325 }, 193, 72],
    [{ width: 768, height: 970 }, 420, 220],
    [{ width: 1440, height: 850 }, 320, 220],
  ])("allocates a full responsive bottom pane for %o", (viewport, expected, minimumCanvasHeight) => {
    expect(bottomPaneHeightForViewport(viewport)).toBe(expected);
    expect(viewport.height - expected - 60).toBeGreaterThanOrEqual(minimumCanvasHeight);
  });
});
