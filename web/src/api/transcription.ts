import { createClient } from "@connectrpc/connect";
import { TranscriptionService } from "../proto/scribe/v1/transcription_pb";
import type { TranscriptionJob } from "../proto/scribe/v1/transcription_pb";
import { getTransport } from "./transport";

export type { TranscriptionJob };

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
  return client().getTranscriptionJob({ jobId });
}

export async function listTranscriptionJobs(itemImageId: bigint): Promise<TranscriptionJob[]> {
  const resp = await client().listTranscriptionJobs({ itemImageId });
  return resp.jobs;
}

/**
 * Stream real-time updates for a transcription job until it reaches a terminal
 * state (completed or failed) or the caller aborts.
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
      for await (const job of stream) {
        last = job;
        onUpdate(job);
      }
      if (last) onDone?.(last);
    } catch (err) {
      if (!ac.signal.aborted) onError?.(err);
    }
  })();

  return ac;
}
