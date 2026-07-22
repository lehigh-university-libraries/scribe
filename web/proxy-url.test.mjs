import { describe, expect, it } from "vitest";
import {
  createUpstreamURL,
  isAllowedIIIFMethod,
  isIIIFPath,
  isPresentationPath,
} from "./proxy-url.mjs";

describe("frontend proxy target resolution", () => {
  it("preserves the configured authority for an absolute-form request", () => {
    const target = createUpstreamURL(
      "https://trusted.example",
      "https://attacker.example//other-host/path?size=full",
    );

    expect(target.origin).toBe("https://trusted.example");
    expect(target.pathname).toBe("//other-host/path");
    expect(target.search).toBe("?size=full");
  });

  it("preserves an encoded IIIF path and query", () => {
    const target = createUpstreamURL("https://trusted.example", "/iiif/id%2F1/full/max/0/default.jpg?download=1");

    expect(target.href).toBe("https://trusted.example/iiif/id%2F1/full/max/0/default.jpg?download=1");
  });

  it("limits the identity-bearing IIIF proxy to CORS-safe read methods", () => {
    expect(isIIIFPath("/iiif/3/image/full/max/0/default.jpg")).toBe(true);
    expect(isPresentationPath("/presentation/v3/item/canvas/page/annotations")).toBe(true);
    expect(isIIIFPath("/v1/items")).toBe(false);
    expect(isPresentationPath("/v1/items")).toBe(false);
    expect(isAllowedIIIFMethod("GET")).toBe(true);
    expect(isAllowedIIIFMethod("HEAD")).toBe(true);
    expect(isAllowedIIIFMethod("OPTIONS")).toBe(true);
    expect(isAllowedIIIFMethod("POST")).toBe(false);
    expect(isAllowedIIIFMethod("DELETE")).toBe(false);
  });
});
