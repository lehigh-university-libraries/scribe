// @vitest-environment happy-dom

import { Code, ConnectError } from "@connectrpc/connect";
import { afterAll, beforeEach, describe, expect, it, vi } from "vitest";
import { AnnotationExportFormat } from "../proto/scribe/v1/annotation_pb";
import { UploadBatchFileStatus, UploadBatchStatus } from "../proto/scribe/v1/item_pb";

const apiClient = vi.hoisted(() => ({
  getEditorManifest: vi.fn(),
  getItem: vi.fn(),
  importManifest: vi.fn(),
  listItems: vi.fn(),
  prepareItemExport: vi.fn(),
  startUploadBatch: vi.fn(),
  getUploadBatch: vi.fn(),
  uploadItemImage: vi.fn(),
  cancelUploadBatch: vi.fn(),
}));
const readFileBytes = vi.hoisted(() => vi.fn(async (file: File) => new Uint8Array(await file.arrayBuffer())));

vi.mock("@connectrpc/connect", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@connectrpc/connect")>()),
  createClient: () => apiClient,
}));
vi.mock("./transport", () => ({ getTransport: vi.fn() }));
vi.mock("../lib/util", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../lib/util")>()),
  readFileBytes,
}));

import { getEditorManifest, getItemExportSnapshot, importManifest, listItems, prepareItemExport, UploadBatchCancellationError, UploadBatchError, uploadItemImages, type UploadBatchProgress } from "./items";

const item = {
  id: "item-1",
  images: [],
  name: "Batch",
  sourceType: "upload",
};
const itemSummary = {
  id: item.id,
  imageCount: 0n,
  name: item.name,
  sourceType: item.sourceType,
};
const testBatchID = "00000000-0000-4000-8000-000000000001";

function files(): File[] {
  return [
    new File(["first"], "page-1.png", { lastModified: 1, type: "image/png" }),
    new File(["second"], "page-2.png", { lastModified: 2, type: "image/png" }),
  ];
}

function uploadedImage(sequence: number, overrides: Record<string, unknown> = {}) {
  return {
    id: BigInt(99 + sequence),
    itemId: item.id,
    sequence,
    imageUrl: `/static/uploads/page-${sequence}.png`,
    canvasUri: `https://scribe.example/items/${item.id}/canvas/${sequence}`,
    label: `Page ${sequence}`,
    width: 100,
    height: 100,
    ...overrides,
  };
}

function batch(fileStatuses: UploadBatchFileStatus[], overrides: Record<string, unknown> = {}) {
  const completedFiles = fileStatuses.filter((status) => status === UploadBatchFileStatus.COMPLETED).length;
  const failedFiles = fileStatuses.filter((status) => status === UploadBatchFileStatus.FAILED).length;
  return {
    id: testBatchID,
    itemId: item.id,
    contextId: 7n,
    status: completedFiles === fileStatuses.length ? UploadBatchStatus.COMPLETED : UploadBatchStatus.IN_PROGRESS,
    completedFiles,
    failedFiles,
    files: fileStatuses.map((status, index) => ({
      sequence: index + 1,
      filename: `page-${index + 1}.png`,
      size: BigInt(index === 0 ? 5 : 6),
      contentSha256: "0".repeat(64),
      status,
      attemptCount: status === UploadBatchFileStatus.PENDING ? 0 : 1,
      maxAttempts: 5,
      itemImageId: status === UploadBatchFileStatus.COMPLETED ? BigInt(100 + index) : 0n,
      transcriptionJobId: status === UploadBatchFileStatus.COMPLETED ? BigInt(200 + index) : 0n,
      errorMessage: "",
    })),
    createdAt: "2026-07-20T00:00:00Z",
    updatedAt: "2026-07-20T00:00:00Z",
    ...overrides,
  };
}

describe("listItems", () => {
  beforeEach(() => vi.clearAllMocks());

  it("forwards bounded keyset pagination and returns the continuation token", async () => {
    apiClient.listItems.mockResolvedValue({ items: [itemSummary], nextPageToken: "next-page" });

    await expect(listItems({ pageSize: 25, pageToken: "current-page", query: "  notes  " })).resolves.toEqual({
      items: [itemSummary],
      nextPageToken: "next-page",
    });
    expect(apiClient.listItems).toHaveBeenCalledWith(
      { pageSize: 25, pageToken: "current-page", query: "notes" },
      { signal: undefined },
    );
  });

  it("rejects an invalid page size before sending a request", async () => {
    await expect(listItems({ pageSize: 101 })).rejects.toBeInstanceOf(RangeError);
    expect(apiClient.listItems).not.toHaveBeenCalled();
  });

  it("rejects an oversized Unicode query before sending a request", async () => {
    await expect(listItems({ query: "🙂".repeat(201) })).rejects.toBeInstanceOf(RangeError);
    expect(apiClient.listItems).not.toHaveBeenCalled();
  });
});

describe("importManifest", () => {
  beforeEach(() => vi.clearAllMocks());

  it("binds manifest idempotency and ingest to the selected context", async () => {
    apiClient.importManifest.mockResolvedValue({
      item: { ...item, sourceType: "manifest", images: [{ id: 42n }] },
    });

    await expect(importManifest("  https://iiif.example/manifest  ", 9n)).resolves.toMatchObject({
      firstItemImageId: "42",
    });
    expect(apiClient.importManifest).toHaveBeenCalledWith({
      manifestUrl: "https://iiif.example/manifest",
      contextId: 9n,
      idempotencyKey: expect.stringMatching(/^[0-9a-f]{64}$/),
    });
  });
});

describe("getEditorManifest", () => {
  beforeEach(() => vi.clearAllMocks());

  it("rejects invalid IDs and incomplete responses", async () => {
    await expect(getEditorManifest("0")).rejects.toThrow(/positive integer/);
    expect(apiClient.getEditorManifest).not.toHaveBeenCalled();
    apiClient.getEditorManifest.mockResolvedValue({ item, manifestJson: "", selectedCanvasId: "canvas" });
    await expect(getEditorManifest("7")).rejects.toThrow(/empty/);
  });
});

describe("prepareItemExport", () => {
  beforeEach(() => vi.clearAllMocks());

  it("sends the generated export format and complete canonical revision vector", async () => {
    const expectedRevisions = [
      { itemImageId: 41n, revision: 7n },
      { itemImageId: 42n, revision: 11n },
    ];
    const response = {
      downloadUrl: "/v1/item-exports/signed-token",
      expiresAt: "2026-07-21T12:00:00Z",
      filename: "item-1.page.xml",
      mediaType: "application/vnd.prima.page+xml",
      revisions: expectedRevisions,
    };
    apiClient.prepareItemExport.mockResolvedValue(response);

    await expect(prepareItemExport(
      "item-1",
      AnnotationExportFormat.PAGE_XML,
      expectedRevisions,
    )).resolves.toBe(response);
    expect(apiClient.prepareItemExport).toHaveBeenCalledOnce();
    expect(apiClient.prepareItemExport).toHaveBeenCalledWith({
      expectedRevisions,
      format: AnnotationExportFormat.PAGE_XML,
      itemId: "item-1",
    });
  });
});

describe("getItemExportSnapshot", () => {
  beforeEach(() => vi.clearAllMocks());

  it("returns the complete server-batched canonical revision vector", async () => {
    apiClient.getItem.mockResolvedValue({
      annotationRevisions: [
        { itemImageId: 41n, revision: 7n },
        { itemImageId: 42n, revision: 11n },
      ],
      item: { ...item, images: [{ id: 41n }, { id: 42n }] },
    });

    await expect(getItemExportSnapshot("item-1")).resolves.toEqual({
      expectedRevisions: [
        { itemImageId: 41n, revision: 7n },
        { itemImageId: 42n, revision: 11n },
      ],
      item: { ...item, images: [{ id: 41n }, { id: 42n }] },
    });
    expect(apiClient.getItem).toHaveBeenCalledOnce();
    expect(apiClient.getItem).toHaveBeenCalledWith({ itemId: "item-1" });
  });

  it("rejects partial, reordered, or zero revision vectors", async () => {
    const invalidVectors = [
      [{ itemImageId: 41n, revision: 7n }],
      [{ itemImageId: 42n, revision: 11n }, { itemImageId: 41n, revision: 7n }],
      [{ itemImageId: 41n, revision: 0n }, { itemImageId: 42n, revision: 11n }],
    ];
    for (const annotationRevisions of invalidVectors) {
      apiClient.getItem.mockResolvedValueOnce({
        annotationRevisions,
        item: { ...item, images: [{ id: 41n }, { id: 42n }] },
      });
      await expect(getItemExportSnapshot("item-1")).rejects.toThrow(/committed annotation page|revision vector/);
    }
  });
});

describe("uploadItemImages", () => {
  const randomUUID = vi.spyOn(globalThis.crypto, "randomUUID").mockReturnValue(testBatchID);

  afterAll(() => randomUUID.mockRestore());

  beforeEach(() => {
    vi.clearAllMocks();
    randomUUID.mockReturnValue(testBatchID);
    window.localStorage.clear();
    apiClient.startUploadBatch.mockResolvedValue({ item, batch: batch([
      UploadBatchFileStatus.PENDING,
      UploadBatchFileStatus.PENDING,
    ]) });
    apiClient.getUploadBatch.mockResolvedValue({ item, batch: batch([
      UploadBatchFileStatus.COMPLETED,
      UploadBatchFileStatus.FAILED,
    ]) });
    apiClient.cancelUploadBatch.mockResolvedValue({ batch: batch([
      UploadBatchFileStatus.COMPLETED,
      UploadBatchFileStatus.CANCELED,
    ], { status: UploadBatchStatus.CANCELED }) });
  });

  it("resumes from durable server progress without re-uploading completed files", async () => {
    const progress = vi.fn<(progress: UploadBatchProgress) => void>();
    apiClient.uploadItemImage
      .mockResolvedValueOnce({
        item,
        image: uploadedImage(1),
        batch: batch([
          UploadBatchFileStatus.COMPLETED,
          UploadBatchFileStatus.PENDING,
        ]),
        transcriptionJobId: 200n,
      })
      .mockRejectedValueOnce(new ConnectError("temporary upload failure", Code.Unavailable));

    await expect(uploadItemImages(files(), { contextId: 7n, maxAttempts: 1, onProgress: progress }))
      .rejects.toMatchObject({ name: "UploadBatchError", completed: 1, failedSequence: 2 });

    const firstBatchID = apiClient.startUploadBatch.mock.calls[0][0].batchId;
    expect(firstBatchID).toMatch(/^[A-Za-z0-9_-]{1,64}$/);
    expect(apiClient.uploadItemImage.mock.calls.map(([request]) => request.sequence)).toEqual([1, 2]);
    expect(window.localStorage.length).toBe(1);

    apiClient.startUploadBatch.mockResolvedValueOnce({ item, batch: batch([
      UploadBatchFileStatus.COMPLETED,
      UploadBatchFileStatus.FAILED,
    ]) });
    apiClient.uploadItemImage.mockReset();
    const completedBatch = batch([UploadBatchFileStatus.COMPLETED, UploadBatchFileStatus.COMPLETED]);
    apiClient.uploadItemImage.mockResolvedValueOnce({
      item,
      image: uploadedImage(2),
      batch: completedBatch,
      transcriptionJobId: 201n,
    });

    await expect(uploadItemImages(files(), { contextId: 7n, maxAttempts: 1, onProgress: progress })).resolves.toEqual({
      item,
      batch: completedBatch,
    });

    expect(apiClient.startUploadBatch.mock.calls[1][0].batchId).toBe(firstBatchID);
    expect(apiClient.uploadItemImage).toHaveBeenCalledOnce();
    expect(apiClient.uploadItemImage.mock.calls[0][0]).toMatchObject({ batchId: firstBatchID, sequence: 2 });
    expect(window.localStorage.length).toBe(0);
  });

  it("returns the final durable batch with every exact image and transcription job identity", async () => {
    const completedBatch = batch([
      UploadBatchFileStatus.COMPLETED,
      UploadBatchFileStatus.COMPLETED,
    ]);
    apiClient.startUploadBatch.mockResolvedValueOnce({ item, batch: completedBatch });

    await expect(uploadItemImages(files(), { contextId: 7n })).resolves.toEqual({
      item,
      batch: completedBatch,
    });
    expect(apiClient.uploadItemImage).not.toHaveBeenCalled();
    expect(completedBatch.files).toEqual([
      expect.objectContaining({ itemImageId: 100n, transcriptionJobId: 200n }),
      expect.objectContaining({ itemImageId: 101n, transcriptionJobId: 201n }),
    ]);
  });

  it("reports hashing, uploading, and durable completion in order", async () => {
    const singleFile = [files()[0]];
    const progress: UploadBatchProgress[] = [];
    apiClient.startUploadBatch.mockResolvedValueOnce({
      item,
      batch: batch([UploadBatchFileStatus.PENDING]),
    });
    apiClient.uploadItemImage.mockResolvedValueOnce({
      item,
      image: uploadedImage(1),
      batch: batch([UploadBatchFileStatus.COMPLETED]),
      transcriptionJobId: 200n,
    });

    await uploadItemImages(singleFile, {
      contextId: 7n,
      onProgress: (entry) => progress.push(entry),
    });

    expect(progress.map(({ status }) => status)).toEqual(["hashing", "uploading", "completed"]);
    expect(progress[1]).toEqual({
      attempt: 1,
      completed: 0,
      filename: "page-1.png",
      sequence: 1,
      status: "uploading",
      total: 1,
    });
  });

  it.each([
    ["omits a durable job identity", (value: ReturnType<typeof batch>) => { value.files[1].transcriptionJobId = 0n; }, /transcription job identity/],
    ["omits an image identity", (value: ReturnType<typeof batch>) => { value.files[0].itemImageId = 0n; }, /image identity/],
    ["names a different item", (value: ReturnType<typeof batch>) => { value.itemId = "item-other"; }, /item identity/],
    ["names a different batch", (value: ReturnType<typeof batch>) => { value.id = "different-batch"; }, /batch identity/],
    ["omits a declared file", (value: ReturnType<typeof batch>) => { value.files.pop(); }, /exact declared file set/],
    ["duplicates a file sequence", (value: ReturnType<typeof batch>) => { value.files[1].sequence = 1; }, /file sequence/],
    ["reuses an image identity", (value: ReturnType<typeof batch>) => { value.files[1].itemImageId = value.files[0].itemImageId; }, /duplicate image identity/],
    ["reuses a job identity", (value: ReturnType<typeof batch>) => { value.files[1].transcriptionJobId = value.files[0].transcriptionJobId; }, /duplicate transcription job identity/],
    ["contains a non-completed file", (value: ReturnType<typeof batch>) => { value.files[1].status = UploadBatchFileStatus.PENDING; }, /file 2 is not completed/],
    ["has inconsistent counters", (value: ReturnType<typeof batch>) => { value.completedFiles = 1; value.failedFiles = 1; }, /completion counters/],
  ])("retains resumable state when a completed batch %s", async (_description, mutate, expectedMessage) => {
    const completedBatch = batch([UploadBatchFileStatus.COMPLETED, UploadBatchFileStatus.COMPLETED]);
    mutate(completedBatch);
    apiClient.startUploadBatch.mockResolvedValueOnce({ item, batch: completedBatch });

    await expect(uploadItemImages(files(), { contextId: 7n }))
      .rejects.toThrow(expectedMessage);
    expect(apiClient.uploadItemImage).not.toHaveBeenCalled();
    expect(window.localStorage.length).toBe(1);
  });

  it("retains resumable state when the final batch is not completed", async () => {
    apiClient.uploadItemImage
      .mockResolvedValueOnce({
        item,
        image: uploadedImage(1),
        batch: batch([UploadBatchFileStatus.COMPLETED, UploadBatchFileStatus.PENDING]),
        transcriptionJobId: 200n,
      })
      .mockResolvedValueOnce({
        item,
        image: uploadedImage(2),
        batch: batch([UploadBatchFileStatus.COMPLETED, UploadBatchFileStatus.COMPLETED], {
          status: UploadBatchStatus.IN_PROGRESS,
        }),
        transcriptionJobId: 201n,
      });

    await expect(uploadItemImages(files(), { contextId: 7n, maxAttempts: 1 }))
      .rejects.toThrow(/completed upload batch/);
    expect(window.localStorage.length).toBe(1);
  });

  it("retains resumable state when the final file response has mismatched image identity", async () => {
    const singleFile = [files()[0]];
    apiClient.startUploadBatch.mockResolvedValueOnce({
      item,
      batch: batch([UploadBatchFileStatus.PENDING]),
    });
    apiClient.uploadItemImage.mockResolvedValueOnce({
      item,
      image: uploadedImage(1, { id: 999n }),
      batch: batch([UploadBatchFileStatus.COMPLETED]),
      transcriptionJobId: 200n,
    });

    await expect(uploadItemImages(singleFile, { contextId: 7n, maxAttempts: 1 }))
      .rejects.toMatchObject({
        name: "UploadBatchError",
        cause: expect.objectContaining({ message: expect.stringMatching(/exact image identity/) }),
      });
    expect(apiClient.uploadItemImage).toHaveBeenCalledOnce();
    expect(window.localStorage.length).toBe(1);
  });

  it("retains resumable state when a file response switches to another item", async () => {
    const singleFile = [files()[0]];
    const otherItem = { ...item, id: "item-other" };
    apiClient.startUploadBatch.mockResolvedValueOnce({
      item,
      batch: batch([UploadBatchFileStatus.PENDING]),
    });
    apiClient.uploadItemImage.mockResolvedValueOnce({
      item: otherItem,
      image: uploadedImage(1, { itemId: otherItem.id }),
      batch: batch([UploadBatchFileStatus.COMPLETED], { itemId: otherItem.id }),
      transcriptionJobId: 200n,
    });

    const error = await uploadItemImages(singleFile, { contextId: 7n, maxAttempts: 1 }).catch((caught: unknown) => caught);
    expect(error).toBeInstanceOf(UploadBatchError);
    expect((error as Error).cause).toMatchObject({ message: expect.stringMatching(/item identity/) });
    expect(apiClient.uploadItemImage).toHaveBeenCalledOnce();
    expect(window.localStorage.length).toBe(1);
  });

  it("does not retry a file response with a mismatched transcription job identity", async () => {
    const singleFile = [files()[0]];
    apiClient.startUploadBatch.mockResolvedValueOnce({
      item,
      batch: batch([UploadBatchFileStatus.PENDING]),
    });
    apiClient.uploadItemImage.mockResolvedValue({
      item,
      image: uploadedImage(1),
      batch: batch([UploadBatchFileStatus.COMPLETED]),
      transcriptionJobId: 999n,
    });

    const error = await uploadItemImages(singleFile, { contextId: 7n }).catch((caught: unknown) => caught);
    expect(error).toBeInstanceOf(UploadBatchError);
    expect((error as Error).cause).toMatchObject({ message: expect.stringMatching(/exact transcription job identity/) });
    expect(apiClient.uploadItemImage).toHaveBeenCalledOnce();
    expect(window.localStorage.length).toBe(1);
  });

  it("reports and honors cancellation while files are being hashed", async () => {
    const controller = new AbortController();
    const progress: UploadBatchProgress[] = [];
    readFileBytes.mockImplementationOnce(async (file: File) => {
      controller.abort();
      return new Uint8Array(await file.arrayBuffer());
    });

    await expect(uploadItemImages(files(), {
      contextId: 7n,
      signal: controller.signal,
      onProgress: (entry) => progress.push(entry),
    })).rejects.toMatchObject({ name: "AbortError" });

    expect(progress[0]).toEqual(expect.objectContaining({
      filename: "page-1.png",
      sequence: 1,
      status: "hashing",
    }));
    expect(apiClient.startUploadBatch).not.toHaveBeenCalled();
    expect(readFileBytes).toHaveBeenCalledOnce();
  });

  it("retries transient failures with the same durable file identity", async () => {
    const progress: UploadBatchProgress[] = [];
    apiClient.uploadItemImage
      .mockRejectedValueOnce(new ConnectError("temporarily unavailable", Code.Unavailable))
      .mockResolvedValueOnce({
        item,
        image: uploadedImage(1),
        batch: batch([UploadBatchFileStatus.COMPLETED, UploadBatchFileStatus.PENDING]),
        transcriptionJobId: 200n,
      })
      .mockResolvedValueOnce({
        item,
        image: uploadedImage(2),
        batch: batch([UploadBatchFileStatus.COMPLETED, UploadBatchFileStatus.COMPLETED]),
        transcriptionJobId: 201n,
      });

    await uploadItemImages(files(), { contextId: 7n, retryDelayMs: 0, onProgress: (entry) => progress.push(entry) });

    expect(apiClient.uploadItemImage.mock.calls.slice(0, 2).map(([request]) => request)).toEqual([
      expect.objectContaining({ sequence: 1 }),
      expect.objectContaining({ sequence: 1 }),
    ]);
    expect(progress).toContainEqual(expect.objectContaining({ sequence: 1, status: "retrying", attempt: 1 }));
    expect(progress).toContainEqual(expect.objectContaining({ completed: 2, sequence: 2, status: "completed" }));
  });

  it("uses the full durable five-attempt budget by default", async () => {
    const singleFile = [files()[0]];
    const progress: UploadBatchProgress[] = [];
    apiClient.startUploadBatch.mockResolvedValueOnce({
      item,
      batch: batch([UploadBatchFileStatus.PENDING]),
    });
    apiClient.uploadItemImage
      .mockRejectedValueOnce(new ConnectError("temporarily unavailable", Code.Unavailable))
      .mockRejectedValueOnce(new ConnectError("temporarily unavailable", Code.Unavailable))
      .mockRejectedValueOnce(new ConnectError("temporarily unavailable", Code.Unavailable))
      .mockRejectedValueOnce(new ConnectError("temporarily unavailable", Code.Unavailable))
      .mockResolvedValueOnce({
        item,
        image: uploadedImage(1),
        batch: batch([UploadBatchFileStatus.COMPLETED]),
        transcriptionJobId: 200n,
      });

    await uploadItemImages(singleFile, {
      contextId: 7n,
      retryDelayMs: 0,
      onProgress: (entry) => progress.push(entry),
    });

    expect(apiClient.uploadItemImage).toHaveBeenCalledTimes(5);
    expect(apiClient.uploadItemImage.mock.calls.map(([request]) => request)).toEqual(
      Array.from({ length: 5 }, () => expect.objectContaining({
        batchId: testBatchID,
        sequence: 1,
      })),
    );
    expect(progress.filter(({ status }) => status === "retrying").map(({ attempt }) => attempt))
      .toEqual([1, 2, 3, 4]);
    expect(progress.at(-1)).toEqual(expect.objectContaining({
      attempt: 5,
      completed: 1,
      sequence: 1,
      status: "completed",
    }));
  });

  it("cancels the durable batch after abort so response-loss jobs are fenced", async () => {
    const controller = new AbortController();
    apiClient.uploadItemImage.mockResolvedValueOnce({
      item,
      image: uploadedImage(1),
      batch: batch([UploadBatchFileStatus.COMPLETED, UploadBatchFileStatus.PENDING]),
      transcriptionJobId: 200n,
    });

    await expect(uploadItemImages(files(), {
      contextId: 7n,
      signal: controller.signal,
      onProgress: ({ status }) => {
        if (status === "completed") controller.abort();
      },
    })).rejects.toMatchObject({ name: "AbortError" });

    const batchID = apiClient.startUploadBatch.mock.calls[0][0].batchId;
    expect(apiClient.uploadItemImage).toHaveBeenCalledOnce();
    expect(apiClient.cancelUploadBatch).toHaveBeenCalledOnce();
    expect(apiClient.cancelUploadBatch).toHaveBeenCalledWith({ batchId: batchID });
    expect(window.localStorage.length).toBe(0);
  });

  it("lets committed single-file completion win over a late progress-callback abort", async () => {
    const controller = new AbortController();
    const singleFile = [files()[0]];
    apiClient.startUploadBatch.mockResolvedValueOnce({
      item,
      batch: batch([UploadBatchFileStatus.PENDING]),
    });
    const completedBatch = batch([UploadBatchFileStatus.COMPLETED]);
    apiClient.uploadItemImage.mockResolvedValueOnce({
      item,
      image: uploadedImage(1),
      batch: completedBatch,
      transcriptionJobId: 200n,
    });

    await expect(uploadItemImages(singleFile, {
      contextId: 7n,
      signal: controller.signal,
      onProgress: ({ status }) => {
        if (status === "completed") controller.abort();
      },
    })).resolves.toEqual({ item, batch: completedBatch });

    expect(apiClient.uploadItemImage).toHaveBeenCalledOnce();
    expect(apiClient.cancelUploadBatch).not.toHaveBeenCalled();
    expect(window.localStorage.length).toBe(0);
  });

  it("reports when durable cancellation cannot be confirmed and preserves resume state", async () => {
    const controller = new AbortController();
    apiClient.uploadItemImage.mockResolvedValueOnce({
      item,
      image: uploadedImage(1),
      batch: batch([UploadBatchFileStatus.COMPLETED, UploadBatchFileStatus.PENDING]),
      transcriptionJobId: 200n,
    });
    apiClient.cancelUploadBatch.mockRejectedValueOnce(new ConnectError("temporarily unavailable", Code.Unavailable));

    await expect(uploadItemImages(files(), {
      contextId: 7n,
      signal: controller.signal,
      onProgress: ({ status }) => {
        if (status === "completed") controller.abort();
      },
    })).rejects.toBeInstanceOf(UploadBatchCancellationError);

    expect(apiClient.cancelUploadBatch).toHaveBeenCalledOnce();
    expect(window.localStorage.length).toBe(1);
  });

  it("binds browser resume identity to file content and selected context", async () => {
    randomUUID
      .mockReturnValueOnce(testBatchID)
      .mockReturnValueOnce("00000000-0000-4000-8000-000000000002")
      .mockReturnValueOnce("00000000-0000-4000-8000-000000000003");
    apiClient.startUploadBatch.mockImplementation(({ batchId }: { batchId: string }) => ({
      item,
      batch: batch([UploadBatchFileStatus.PENDING, UploadBatchFileStatus.PENDING], { id: batchId }),
    }));
    apiClient.uploadItemImage.mockImplementation(({ batchId, sequence }: { batchId: string; sequence: number }) => ({
      item,
      image: uploadedImage(sequence),
      batch: batch([UploadBatchFileStatus.COMPLETED, UploadBatchFileStatus.COMPLETED], { id: batchId }),
      transcriptionJobId: BigInt(199 + sequence),
    }));

    await uploadItemImages(files(), { contextId: 7n });
    await uploadItemImages(files(), { contextId: 8n });
    const changedContent = files();
    changedContent[0] = new File(["FIRST"], "page-1.png", { lastModified: 1, type: "image/png" });
    await uploadItemImages(changedContent, { contextId: 7n });

    const batchIDs = apiClient.startUploadBatch.mock.calls.map(([request]) => request.batchId);
    expect(new Set(batchIDs).size).toBe(3);
    expect(apiClient.startUploadBatch.mock.calls[0][0].files).toEqual([
      expect.objectContaining({ filename: "page-1.png", size: 5n, contentSha256: expect.stringMatching(/^[0-9a-f]{64}$/) }),
      expect.objectContaining({ filename: "page-2.png", size: 6n, contentSha256: expect.stringMatching(/^[0-9a-f]{64}$/) }),
    ]);
  });

  it("reports partial failure without discarding the resumable batch", async () => {
    apiClient.uploadItemImage.mockRejectedValueOnce(new ConnectError("invalid image", Code.InvalidArgument));
    const durableFailure = batch([
      UploadBatchFileStatus.FAILED,
      UploadBatchFileStatus.PENDING,
    ]);
    durableFailure.files[0].errorMessage = "provider request failed with HTTP status 403";
    apiClient.getUploadBatch.mockResolvedValueOnce({ item, batch: durableFailure });

    const promise = uploadItemImages(files(), { contextId: 7n, maxAttempts: 1 });
    await expect(promise).rejects.toBeInstanceOf(UploadBatchError);
    await expect(promise).rejects.toMatchObject({
      failedSequence: 1,
      message: "upload failed for page-1.png: provider request failed with HTTP status 403",
    });
    expect(apiClient.cancelUploadBatch).not.toHaveBeenCalled();
    expect(window.localStorage.length).toBe(1);
  });
});
