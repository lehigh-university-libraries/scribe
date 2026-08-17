import { describe, expect, it } from "vitest";

import {
  productionSessionUserIsNonAdmin,
  protoJSONRepeatedField,
} from "./deployed-readiness-protojson.mjs";

describe("production browser session identity", () => {
  it.each([
    ["omitted ProtoJSON default", {}],
    ["explicit false", { isAdmin: false }],
  ])("accepts a non-admin encoded as %s", (_name, user) => {
    expect(productionSessionUserIsNonAdmin(user)).toBe(true);
  });

  it.each([
    ["true", { isAdmin: true }],
    ["null", { isAdmin: null }],
    ["a string", { isAdmin: "false" }],
    ["a number", { isAdmin: 0 }],
    ["explicit undefined", { isAdmin: undefined }],
  ])("rejects isAdmin encoded as %s", (_name, user) => {
    expect(productionSessionUserIsNonAdmin(user)).toBe(false);
  });

  it.each([null, [], "", false])("rejects a non-object user record", (user) => {
    expect(productionSessionUserIsNonAdmin(user)).toBe(false);
  });
});

describe("ProtoJSON repeated fields", () => {
  it("normalizes an omitted field to an empty array", () => {
    expect(protoJSONRepeatedField({}, "items")).toEqual([]);
  });

  it("accepts explicit empty and populated arrays", () => {
    const empty = [];
    const populated = [{ id: "1" }];
    expect(protoJSONRepeatedField({ items: empty }, "items")).toBe(empty);
    expect(protoJSONRepeatedField({ items: populated }, "items")).toBe(populated);
  });

  it.each([undefined, null, [], "", false, 0])(
    "rejects malformed message payload %s",
    (payload) => {
      expect(() => protoJSONRepeatedField(payload, "items")).toThrow(TypeError);
    },
  );

  it.each([undefined, null, {}, "", false, 0])(
    "rejects malformed present repeated field %s",
    (items) => {
      expect(() => protoJSONRepeatedField({ items }, "items")).toThrow(TypeError);
    },
  );
});
