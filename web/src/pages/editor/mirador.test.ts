import { describe, expect, it, vi } from "vitest";

import type {
  ScribeAnnotationAdapterConstructor,
  ScribeAnnotationClient,
} from "mirador-scribe";
import { commonViewerOptions } from "./mirador";

describe("commonViewerOptions", () => {
  it("lets companion content fill and scroll within its remaining height", () => {
    const options = commonViewerOptions(
      "https://scribe.example",
      vi.fn() as unknown as ScribeAnnotationAdapterConstructor,
      {} as ScribeAnnotationClient,
      () => ({ contextId: "1", itemImageId: "2", windowId: "window-1" }),
      { ajaxWithCredentials: false, crossOriginPolicy: "Anonymous" },
    );

    expect(options.theme.components.CompanionWindow.styleOverrides.contents).toEqual({
      display: "flex",
      flex: "1 1 auto",
      flexDirection: "column",
      minHeight: 0,
      overflowY: "auto",
    });
  });
});
