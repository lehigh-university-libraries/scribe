import { beforeEach, describe, expect, it, vi } from "vitest";

const apiClient = vi.hoisted(() => ({
  listContexts: vi.fn(),
  listSelectionRules: vi.fn(),
}));

vi.mock("@connectrpc/connect", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@connectrpc/connect")>()),
  createClient: () => apiClient,
}));
vi.mock("./transport", () => ({ getTransport: vi.fn() }));

import {
  listContextPage,
  listContexts,
  listSelectionRulePage,
  listSelectionRules,
} from "./context";

describe("context catalog pagination", () => {
  beforeEach(() => vi.clearAllMocks());

  it("forwards page state and exposes the opaque continuation", async () => {
    apiClient.listContexts.mockResolvedValue({ contexts: [{ id: 1n }], nextPageToken: "next" });
    await expect(listContextPage(true, "current", 25)).resolves.toEqual({
      contexts: [{ id: 1n }],
      nextPageToken: "next",
    });
    expect(apiClient.listContexts).toHaveBeenCalledWith({
      systemOnly: true,
      pageToken: "current",
      pageSize: 25,
    });
  });

  it("traverses bounded pages without dropping catalog entries", async () => {
    apiClient.listContexts
      .mockResolvedValueOnce({ contexts: [{ id: 1n }], nextPageToken: "page-2" })
      .mockResolvedValueOnce({ contexts: [{ id: 2n }], nextPageToken: "" });
    await expect(listContexts()).resolves.toEqual([{ id: 1n }, { id: 2n }]);
    expect(apiClient.listContexts).toHaveBeenNthCalledWith(2, {
      systemOnly: false,
      pageToken: "page-2",
      pageSize: 100,
    });
  });

  it("rejects invalid bounds and repeated server tokens", async () => {
    await expect(listContextPage(false, "", 101)).rejects.toBeInstanceOf(RangeError);
    await expect(listContextPage(false, "x".repeat(513), 1)).rejects.toBeInstanceOf(RangeError);
    apiClient.listContexts.mockResolvedValue({ contexts: [], nextPageToken: "repeat" });
    await expect(listContexts()).rejects.toThrow("repeated token");
  });
});

describe("selection rule pagination", () => {
  beforeEach(() => vi.clearAllMocks());

  it("binds requests to the context filter and aggregates all pages", async () => {
    apiClient.listSelectionRules
      .mockResolvedValueOnce({ rules: [{ id: 11n }], nextPageToken: "page-2" })
      .mockResolvedValueOnce({ rules: [{ id: 12n }], nextPageToken: "" });
    await expect(listSelectionRules("7")).resolves.toEqual([{ id: 11n }, { id: 12n }]);
    expect(apiClient.listSelectionRules).toHaveBeenNthCalledWith(2, {
      contextId: 7n,
      pageToken: "page-2",
      pageSize: 100,
    });
  });

  it("returns one incremental rule page and rejects invalid bounds", async () => {
    apiClient.listSelectionRules.mockResolvedValue({ rules: [{ id: 11n }], nextPageToken: "next" });
    await expect(listSelectionRulePage("7", "current", 10)).resolves.toEqual({
      rules: [{ id: 11n }],
      nextPageToken: "next",
    });
    await expect(listSelectionRulePage("7", "", 0)).rejects.toBeInstanceOf(RangeError);
  });
});
