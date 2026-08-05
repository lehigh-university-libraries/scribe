import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  createClient: vi.fn(),
  createScribeTransport: vi.fn(),
  defaultTransport: { name: "default" },
  deleteAPIKey: vi.fn(),
  explicitTransport: { name: "workspace-7" },
  getTransport: vi.fn(),
}));

vi.mock("@connectrpc/connect", async (importOriginal) => {
  const original = await importOriginal<typeof import("@connectrpc/connect")>();
  return { ...original, createClient: mocks.createClient };
});

vi.mock("./transport", () => ({
  createScribeTransport: mocks.createScribeTransport,
  getTransport: mocks.getTransport,
}));

import { deleteAPIKey } from "./auth";

describe("API key deletion transport", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.getTransport.mockReturnValue(mocks.defaultTransport);
    mocks.createScribeTransport.mockReturnValue(mocks.explicitTransport);
    mocks.createClient.mockReturnValue({ deleteAPIKey: mocks.deleteAPIKey });
    mocks.deleteAPIKey.mockResolvedValue({});
  });

  it("keeps ordinary deletion on the shared current-workspace transport", async () => {
    await deleteAPIKey("55");

    expect(mocks.getTransport).toHaveBeenCalledOnce();
    expect(mocks.createScribeTransport).not.toHaveBeenCalled();
    expect(mocks.createClient).toHaveBeenCalledWith(expect.anything(), mocks.defaultTransport);
    expect(mocks.deleteAPIKey).toHaveBeenCalledWith({ keyId: 55n });
  });

  it("pins cleanup deletion to the explicitly captured workspace transport", async () => {
    await deleteAPIKey(91n, { workspaceId: "7" });

    expect(mocks.getTransport).not.toHaveBeenCalled();
    expect(mocks.createScribeTransport).toHaveBeenCalledWith({ workspaceId: 7n });
    expect(mocks.createClient).toHaveBeenCalledWith(expect.anything(), mocks.explicitTransport);
    expect(mocks.deleteAPIKey).toHaveBeenCalledWith({ keyId: 91n });
  });

  it("rejects an invalid explicit workspace instead of falling back to current selection", async () => {
    await expect(deleteAPIKey(91n, { workspaceId: "0" }))
      .rejects.toThrow("workspaceId must be a positive integer");

    expect(mocks.getTransport).not.toHaveBeenCalled();
    expect(mocks.createScribeTransport).not.toHaveBeenCalled();
    expect(mocks.deleteAPIKey).not.toHaveBeenCalled();
  });
});
