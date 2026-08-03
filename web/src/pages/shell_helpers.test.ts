// @vitest-environment happy-dom

import { describe, expect, it } from "vitest";

import type { Context } from "../proto/scribe/v1/context_pb";
import { contextOptions } from "./shell_helpers";

describe("contextOptions", () => {
  it("keeps resolver-backed Default selected instead of a concrete default row", () => {
    const markup = contextOptions([
      { id: 0n, name: "synthetic", isDefault: true },
      { id: 11n, name: "Scribe Custom", isDefault: true },
      { id: 12n, name: "Workspace default", isDefault: true },
      { id: 13n, name: "Gemini Pro", isDefault: false },
    ] as Context[]).map((option) => option.value).join("");

    expect(markup).not.toContain('value="0"');
    expect(markup).not.toContain(" selected");
    expect(markup).toContain('value="11"');
    expect(markup).toContain('value="13"');
  });
});
