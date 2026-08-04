// @vitest-environment happy-dom

import { describe, expect, it } from "vitest";

import { renderEditorLayout } from "./layout";

describe("renderEditorLayout", () => {
  it("allocates the dynamic viewport to a wrapping header and flexible viewer", () => {
    const app = document.createElement("div");

    renderEditorLayout(app);

    const main = app.querySelector("main");
    const header = app.querySelector("header");
    const viewerRegion = app.querySelector("section");
    const viewer = app.querySelector("#mirador-viewer");

    expect(main?.classList.contains("h-dvh")).toBe(true);
    expect(main?.classList.contains("flex-col")).toBe(true);
    expect(main?.classList.contains("h-screen")).toBe(false);
    expect(header?.classList.contains("flex-wrap")).toBe(true);
    expect(header?.classList.contains("flex-none")).toBe(true);
    expect(viewerRegion?.classList.contains("flex-1")).toBe(true);
    expect(viewerRegion?.classList.contains("min-h-0")).toBe(true);
    expect(viewerRegion?.className).not.toContain("calc(100vh");
    expect(viewer?.classList.contains("h-full")).toBe(true);
    expect(viewer?.classList.contains("min-h-0")).toBe(true);
  });
});
