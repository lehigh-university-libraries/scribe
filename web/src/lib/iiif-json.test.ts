import { describe, expect, it } from "vitest";

import { parseIIIFJSON } from "./iiif-json";

describe("parseIIIFJSON", () => {
  it("keeps ordinary IIIF numbers native", () => {
    const parsed = parseIIIFJSON('{"width":1200,"confidence":0.875}') as Record<string, unknown>;

    expect(parsed).toEqual({ confidence: 0.875, width: 1200 });
  });

  it("marks numeric extension values that JavaScript cannot round-trip", () => {
    const parsed = parseIIIFJSON(
      '{"largeInteger":9007199254740993,"preciseDecimal":0.123456789012345678901}',
    ) as Record<string, unknown>;

    expect(parsed.largeInteger).toBeInstanceOf(String);
    expect((parsed.largeInteger as String).valueOf()).toBe("9007199254740993");
    expect(parsed.preciseDecimal).toBeInstanceOf(String);
    expect((parsed.preciseDecimal as String).valueOf()).toBe("0.123456789012345678901");
    expect(structuredClone(parsed)).toEqual(parsed);
  });
});
