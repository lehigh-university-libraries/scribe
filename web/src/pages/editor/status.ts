import { TranscriptionJobStatus } from "../../proto/scribe/v1/transcription_pb";

export function isPendingStatus(status: TranscriptionJobStatus | string | number): boolean {
  return status === TranscriptionJobStatus.PENDING
    || status === "TRANSCRIPTION_JOB_STATUS_PENDING"
    || status === "pending";
}

export function isRunningStatus(status: TranscriptionJobStatus | string | number): boolean {
  return status === TranscriptionJobStatus.RUNNING
    || status === "TRANSCRIPTION_JOB_STATUS_RUNNING"
    || status === "running";
}

export function isCompletedStatus(status: TranscriptionJobStatus | string | number): boolean {
  return status === TranscriptionJobStatus.COMPLETED
    || status === "TRANSCRIPTION_JOB_STATUS_COMPLETED"
    || status === "completed";
}

export function isCanceledStatus(status: TranscriptionJobStatus | string | number): boolean {
  return status === TranscriptionJobStatus.CANCELED
    || status === "TRANSCRIPTION_JOB_STATUS_CANCELED"
    || status === "canceled";
}

export function isFailedStatus(status: TranscriptionJobStatus | string | number): boolean {
  return status === TranscriptionJobStatus.FAILED
    || status === "TRANSCRIPTION_JOB_STATUS_FAILED"
    || status === "failed"
    || status === TranscriptionJobStatus.SUPERSEDED
    || status === "TRANSCRIPTION_JOB_STATUS_SUPERSEDED"
    || status === "superseded";
}

export function eventBigInt(value: unknown): bigint {
  if (typeof value === "bigint" || typeof value === "number" || typeof value === "string" || typeof value === "boolean") {
    try {
      return BigInt(value);
    } catch {
      return 0n;
    }
  }
  return 0n;
}

export function eventNumber(value: unknown): number {
  return typeof value === "number" || typeof value === "string" || typeof value === "bigint" || typeof value === "boolean"
    ? Number(value)
    : 0;
}
