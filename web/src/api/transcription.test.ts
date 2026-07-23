import { beforeEach, describe, expect, it, vi } from "vitest";

const apiClient = vi.hoisted(() => ({
  listTranscriptionJobs: vi.fn(),
}));

vi.mock("@connectrpc/connect", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@connectrpc/connect")>()),
  createClient: () => apiClient,
}));
vi.mock("./transport", () => ({ getTransport: vi.fn() }));

import {
  listTranscriptionJobPage,
  listTranscriptionJobs,
} from "./transcription";

describe("transcription job pagination", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    apiClient.listTranscriptionJobs.mockResolvedValue({
      jobs: [{ id: 42n, itemImageId: 7n }],
      nextPageToken: "next-page",
    });
  });

  it("forwards bounded keyset pagination and returns the continuation token", async () => {
    await expect(listTranscriptionJobPage(7n, 25, "current-page")).resolves.toEqual({
      jobs: [{ id: 42n, itemImageId: 7n }],
      nextPageToken: "next-page",
    });
    expect(apiClient.listTranscriptionJobs).toHaveBeenCalledWith({
      itemImageId: 7n,
      pageSize: 25,
      pageToken: "current-page",
    });
  });

  it("loads only the newest scalar summary for current editor callers", async () => {
    await expect(listTranscriptionJobs(7n)).resolves.toEqual([
      { id: 42n, itemImageId: 7n },
    ]);
    expect(apiClient.listTranscriptionJobs).toHaveBeenCalledWith({
      itemImageId: 7n,
      pageSize: 1,
      pageToken: "",
    });
  });

  it("rejects invalid page bounds before sending the request", async () => {
    await expect(listTranscriptionJobPage(7n, 101)).rejects.toBeInstanceOf(
      RangeError,
    );
    await expect(
      listTranscriptionJobPage(7n, 1, "t".repeat(513)),
    ).rejects.toBeInstanceOf(RangeError);
    expect(apiClient.listTranscriptionJobs).not.toHaveBeenCalled();
  });
});
