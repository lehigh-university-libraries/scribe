import { Code, ConnectError } from "@connectrpc/connect";
import { beforeEach, describe, expect, it, vi } from "vitest";

const apiClient = vi.hoisted(() => ({
  processHOCR: vi.fn(),
  processImageURL: vi.fn(),
  reprocessItemImage: vi.fn(),
}));

vi.mock("@connectrpc/connect", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@connectrpc/connect")>()),
  createClient: () => apiClient,
}));
vi.mock("./transport", () => ({ getTransport: vi.fn() }));

import { processHOCR, processImageURL, ReprocessRevisionConflictError, reprocessItemImage } from "./processing";

describe("idempotent processing", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    apiClient.processImageURL.mockResolvedValue({ itemImageId: 1n });
    apiClient.processHOCR.mockResolvedValue({ itemImageId: 2n });
  });

  it("derives a stable URL-ingest key from the normalized request", async () => {
    await processImageURL("  https://images.example.test/page.jpg  ", 7n);

    expect(apiClient.processImageURL).toHaveBeenCalledWith({
      imageUrl: "https://images.example.test/page.jpg",
      contextId: 7n,
      idempotencyKey: expect.stringMatching(/^[0-9a-f]{64}$/),
    });
  });

  it("binds an hOCR import key to both annotation and image bytes", async () => {
    await processHOCR("<html>one</html>", "", new Uint8Array([1, 2, 3]), "page.png");
    await processHOCR("<html>two</html>", "", new Uint8Array([1, 2, 3]), "page.png");

    const firstKey = apiClient.processHOCR.mock.calls[0][0].idempotencyKey;
    const secondKey = apiClient.processHOCR.mock.calls[1][0].idempotencyKey;
    expect(firstKey).toMatch(/^[0-9a-f]{64}$/);
    expect(secondKey).toMatch(/^[0-9a-f]{64}$/);
    expect(secondKey).not.toBe(firstKey);
  });

  it("rejects an ambiguous hOCR image source before sending the RPC", async () => {
    await expect(processHOCR("<html></html>", "https://images.example.test/page.jpg", new Uint8Array([1]))).rejects.toThrow("exactly one");
    expect(apiClient.processHOCR).not.toHaveBeenCalled();
  });
});

describe("reprocessItemImage", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    apiClient.reprocessItemImage.mockResolvedValue({ canonicalRevision: 20n });
  });

  it("sends the caller-reviewed canonical revision as an exact bigint CAS fence", async () => {
    await reprocessItemImage("9007199254740993", 33n, "9007199254740995");

    expect(apiClient.reprocessItemImage).toHaveBeenCalledWith({
      contextId: 33n,
      expectedRevision: 9007199254740995n,
      itemImageId: 9007199254740993n,
    });
  });

  it("turns an aborted save into a caller-visible revision conflict", async () => {
    apiClient.reprocessItemImage.mockRejectedValue(new ConnectError("revision conflict", Code.Aborted));

    const error = await reprocessItemImage("7", 33, "19").catch((cause: unknown) => cause);

    expect(error).toBeInstanceOf(ReprocessRevisionConflictError);
    expect(error).toMatchObject({ name: "RevisionConflict" });
    expect(apiClient.reprocessItemImage).toHaveBeenCalledOnce();
  });

  it("preserves non-conflict transport failures", async () => {
    const unavailable = new ConnectError("unavailable", Code.Unavailable);
    apiClient.reprocessItemImage.mockRejectedValue(unavailable);

    await expect(reprocessItemImage("7", 33, "19")).rejects.toBe(unavailable);
  });
});
