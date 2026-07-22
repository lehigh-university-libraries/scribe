export const PUBLISH_RESULT_TIMEOUT_MS = 30_000;

const PUBLISH_REQUEST_EVENT = 'scribe:request-publish';
const PUBLISH_RESULT_EVENT = 'scribe:publish-result';

export interface PublishRequestDetail {
  canvasId: string;
  expectedRevision?: string;
  itemImageId?: string;
  requestId: string;
  windowId: string;
}

interface PublishResultDetail {
  canvasId: string;
  ok: boolean;
  requestId: string;
  windowId: string;
}

export type PublishOutcome = 'result' | 'aborted' | 'timeout' | 'dispatch-error';

export interface PublishRequestResult {
  ok: boolean;
  outcome: PublishOutcome;
}

interface RequestPublishOptions {
  detail: PublishRequestDetail;
  eventTarget?: EventTarget;
  signal?: AbortSignal;
  timeoutMs?: number;
}

/**
 * Dispatch one publish request and wait for its correlated result. The wait is
 * deliberately bounded because the application shell may be unavailable or
 * may disappear while publishing.
 */
export function requestPublishResult({
  detail,
  eventTarget = document,
  signal,
  timeoutMs = PUBLISH_RESULT_TIMEOUT_MS,
}: RequestPublishOptions): Promise<PublishRequestResult> {
  return new Promise((resolve) => {
    let settled = false;
    let timeoutId: ReturnType<typeof globalThis.setTimeout> | undefined;

    const finish = (result: PublishRequestResult) => {
      if (settled) return;
      settled = true;
      if (timeoutId !== undefined) globalThis.clearTimeout(timeoutId);
      eventTarget.removeEventListener(PUBLISH_RESULT_EVENT, handleResult);
      signal?.removeEventListener('abort', handleAbort);
      resolve(result);
    };
    const handleResult = (event: Event) => {
      const result = (event as CustomEvent<PublishResultDetail>).detail;
      if (!result
        || result.requestId !== detail.requestId
        || result.windowId !== detail.windowId
        || result.canvasId !== detail.canvasId) return;
      finish({ ok: Boolean(result.ok), outcome: 'result' });
    };
    const handleAbort = () => finish({ ok: false, outcome: 'aborted' });

    if (signal?.aborted) {
      finish({ ok: false, outcome: 'aborted' });
      return;
    }

    eventTarget.addEventListener(PUBLISH_RESULT_EVENT, handleResult);
    signal?.addEventListener('abort', handleAbort, { once: true });
    const boundedTimeoutMs = Number.isFinite(timeoutMs) && timeoutMs > 0
      ? timeoutMs
      : PUBLISH_RESULT_TIMEOUT_MS;
    timeoutId = globalThis.setTimeout(() => {
      finish({ ok: false, outcome: 'timeout' });
    }, boundedTimeoutMs);

    try {
      eventTarget.dispatchEvent(new CustomEvent(PUBLISH_REQUEST_EVENT, { detail }));
    } catch {
      finish({ ok: false, outcome: 'dispatch-error' });
    }
  });
}
