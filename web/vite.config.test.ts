import { describe, expect, it, vi } from "vitest";
import {
  stripPublicPresentationRequestCredentials,
  stripPublicPresentationResponseCredentials,
} from "./vite.config";

describe("Vite public Presentation proxy", () => {
  it("does not disclose browser credentials to the IIIF upstream", () => {
    const removeHeader = vi.fn();

    stripPublicPresentationRequestCredentials({ removeHeader });

    expect(removeHeader.mock.calls.map(([name]) => name)).toEqual([
      "authorization",
      "cookie",
      "x-scribe-api-key",
      "x-scribe-workspace-id",
    ]);
  });

  it("does not let the IIIF upstream mint application-origin cookies", () => {
    const headers = {
      "content-type": "application/json",
      "Set-Cookie": ["triplet-session=unsafe; HttpOnly"],
    };

    stripPublicPresentationResponseCredentials(headers);

    expect(headers).toEqual({ "content-type": "application/json" });
  });
});
