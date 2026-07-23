import { createClient } from "@connectrpc/connect";
import { TranscriptionService } from "../proto/scribe/v1/transcription_pb";
import type {
  TranscriptionJob,
  TranscriptionJobSummary,
} from "../proto/scribe/v1/transcription_pb";
import { getTransport } from "./transport";

export type { TranscriptionJob, TranscriptionJobSummary };

export interface TranscriptionJobPage {
  jobs: TranscriptionJobSummary[];
  nextPageToken: string;
}

function client() {
  return createClient(TranscriptionService, getTransport());
}

export async function createTranscriptionJob(itemImageId: bigint, contextId?: bigint): Promise<bigint> {
  const resp = await client().createTranscriptionJob({
    itemImageId,
    contextId: contextId ?? 0n,
  });
  return resp.jobId;
}

export async function getTranscriptionJob(jobId: bigint): Promise<TranscriptionJob> {
  const resp = await client().getTranscriptionJob({ jobId });
  if (!resp.job) throw new Error("no job in response");
  return resp.job;
}

export async function cancelTranscriptionJob(jobId: bigint): Promise<TranscriptionJob> {
  const resp = await client().cancelTranscriptionJob({ jobId });
  if (!resp.job) throw new Error("no job in response");
  return resp.job;
}

export async function listTranscriptionJobPage(
  itemImageId: bigint,
  pageSize = 50,
  pageToken = "",
): Promise<TranscriptionJobPage> {
  if (!Number.isInteger(pageSize) || pageSize < 1 || pageSize > 100) {
    throw new RangeError("pageSize must be an integer between 1 and 100");
  }
  if (pageToken.length > 512) {
    throw new RangeError("pageToken must not exceed 512 characters");
  }
  const resp = await client().listTranscriptionJobs({
    itemImageId,
    pageSize,
    pageToken,
  });
  return { jobs: resp.jobs, nextPageToken: resp.nextPageToken };
}

// Current callers need only the newest job for one image. Keeping that query
// at one scalar summary prevents the editor shell from accidentally walking a
// tenant's complete job history.
export async function listTranscriptionJobs(
  itemImageId: bigint,
): Promise<TranscriptionJobSummary[]> {
  const page = await listTranscriptionJobPage(itemImageId, 1);
  return page.jobs;
}

/**
 * Stream real-time updates for a transcription job until it reaches a terminal
 * state (completed, failed, or canceled) or the caller aborts.
 *
 * @returns an AbortController whose `abort()` stops the stream.
 */
export function streamTranscriptionJob(
  jobId: bigint,
  onUpdate: (job: TranscriptionJob) => void,
  onDone?: (job: TranscriptionJob) => void,
  onError?: (err: unknown) => void,
): AbortController {
  const ac = new AbortController();

  (async () => {
    try {
      const stream = client().streamTranscriptionJob(
        { jobId },
        { signal: ac.signal },
      );
      let last: TranscriptionJob | undefined;
      for await (const resp of stream) {
        if (!resp.job) continue;
        last = resp.job;
        onUpdate(resp.job);
      }
      if (last) onDone?.(last);
    } catch (err) {
      if (!ac.signal.aborted) onError?.(err);
    }
  })();

  return ac;
}
