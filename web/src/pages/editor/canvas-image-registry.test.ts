import { describe, expect, it } from "vitest";
import { createCanvasImageRegistry } from "./canvas-image-registry";

const images = [
  { canvasUri: "https://iiif.example/canvas/a", id: 101n },
  { canvasUri: "https://iiif.example/canvas/b?view=original", id: 202n },
];

describe("createCanvasImageRegistry", () => {
  it("maps every exact Canvas URI to its independent item image", () => {
    const registry = createCanvasImageRegistry(images);

    expect(registry.itemImageIdForCanvas("https://iiif.example/canvas/a")).toBe("101");
    expect(registry.canvasIdForItemImage("101")).toBe("https://iiif.example/canvas/a");
    expect(registry.canvasIdForItemImage(202n)).toBe("https://iiif.example/canvas/b?view=original");
    expect(registry.itemImageIdForCanvas("https://iiif.example/canvas/b?view=original")).toBe("202");
    expect(registry.hasItemImageId("101")).toBe(true);
    expect(registry.hasItemImageId(202n)).toBe(true);
    expect(registry.hasItemImageId("303")).toBe(false);
  });

  it("does not normalize or guess unknown Canvas identities", () => {
    const registry = createCanvasImageRegistry(images);

    expect(() => registry.itemImageIdForCanvas("https://iiif.example/canvas/a/")).toThrow("not part of this item");
    expect(() => registry.itemImageIdForCanvas(" https://iiif.example/canvas/a")).toThrow("without surrounding whitespace");
    expect(() => registry.itemImageIdForCanvas("https://iiif.example/canvas/a#xywh=1,2,3,4")).toThrow("not a fragment");
  });

  it("rejects ambiguous and malformed registry data", () => {
    expect(() => createCanvasImageRegistry([
      { canvasUri: "https://iiif.example/canvas/a", id: 101n },
      { canvasUri: "https://iiif.example/canvas/a", id: 202n },
    ])).toThrow("registered more than once");
    expect(() => createCanvasImageRegistry([
      { canvasUri: "https://iiif.example/canvas/a", id: 101n },
      { canvasUri: "https://iiif.example/canvas/b", id: 101n },
    ])).toThrow("registered for more than one Canvas");
    expect(() => createCanvasImageRegistry([{ canvasUri: "urn:canvas:a", id: 101n }])).toThrow("HTTP(S)");
    expect(() => createCanvasImageRegistry([{ canvasUri: "https://iiif.example/canvas/a", id: 0n }])).toThrow("positive item image ID");
  });

  it("supports an explicit fallback only for a single-image editor", () => {
    const emptyRegistry = createCanvasImageRegistry([], { singleImageFallback: "9007199254740993" });
    expect(emptyRegistry.canvasIdForItemImage("9007199254740993")).toBe("");
    expect(emptyRegistry.itemImageIdForCanvas("https://iiif.example/generated/canvas")).toBe("9007199254740993");
    expect(emptyRegistry.hasItemImageId(9007199254740993n)).toBe(true);

    const oneImageRegistry = createCanvasImageRegistry([images[0]], { singleImageFallback: 101n });
    expect(oneImageRegistry.itemImageIdForCanvas("https://iiif.example/generated/canvas")).toBe("101");

    expect(() => createCanvasImageRegistry(images, { singleImageFallback: 101n })).toThrow("multi-image");
    expect(() => createCanvasImageRegistry([images[0]], { singleImageFallback: 999n })).toThrow("must match");
  });
});
