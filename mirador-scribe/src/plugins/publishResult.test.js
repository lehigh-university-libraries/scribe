// @vitest-environment happy-dom

import { afterEach, describe, expect, it, vi } from 'vitest';
import { requestPublishResult } from './publishResult';

describe('publish result event bridge', () => {
  afterEach(() => {
    vi.restoreAllMocks();
    vi.useRealTimers();
  });

  it('settles false and removes its listener when no publish result arrives', async () => {
    vi.useFakeTimers();
    const eventTarget = new EventTarget();
    const removeEventListener = vi.spyOn(eventTarget, 'removeEventListener');
    const request = requestPublishResult({
      detail: {
        canvasId: 'canvas-a',
        expectedRevision: '7',
        itemImageId: '42',
        requestId: 'publish-missing-result',
        windowId: 'window-a',
      },
      eventTarget,
      timeoutMs: 250,
    });

    await vi.advanceTimersByTimeAsync(250);

    await expect(request).resolves.toEqual({ ok: false, outcome: 'timeout' });
    expect(removeEventListener).toHaveBeenCalledWith(
      'scribe:publish-result',
      expect.any(Function),
    );
  });

  it('cleans up after a matching result or abort', async () => {
    const resultTarget = new EventTarget();
    const removeResultListener = vi.spyOn(resultTarget, 'removeEventListener');
    resultTarget.addEventListener('scribe:request-publish', (event) => {
      resultTarget.dispatchEvent(new CustomEvent('scribe:publish-result', {
        detail: {
          canvasId: event.detail.canvasId,
          ok: true,
          requestId: event.detail.requestId,
          windowId: event.detail.windowId,
        },
      }));
    });

    await expect(requestPublishResult({
      detail: {
        canvasId: 'canvas-a',
        requestId: 'publish-success',
        windowId: 'window-a',
      },
      eventTarget: resultTarget,
      timeoutMs: 250,
    })).resolves.toEqual({ ok: true, outcome: 'result' });
    expect(removeResultListener).toHaveBeenCalledWith(
      'scribe:publish-result',
      expect.any(Function),
    );

    const abortTarget = new EventTarget();
    const removeAbortListener = vi.spyOn(abortTarget, 'removeEventListener');
    const controller = new AbortController();
    const abortedRequest = requestPublishResult({
      detail: {
        canvasId: 'canvas-a',
        requestId: 'publish-aborted',
        windowId: 'window-a',
      },
      eventTarget: abortTarget,
      signal: controller.signal,
      timeoutMs: 250,
    });
    controller.abort();

    await expect(abortedRequest).resolves.toEqual({ ok: false, outcome: 'aborted' });
    expect(removeAbortListener).toHaveBeenCalledWith(
      'scribe:publish-result',
      expect.any(Function),
    );
  });

  it('ignores a matching request id from another window or Canvas', async () => {
    const eventTarget = new EventTarget();
    eventTarget.addEventListener('scribe:request-publish', (event) => {
      eventTarget.dispatchEvent(new CustomEvent('scribe:publish-result', {
        detail: {
          canvasId: 'canvas-b',
          ok: true,
          requestId: event.detail.requestId,
          windowId: 'window-a',
        },
      }));
      eventTarget.dispatchEvent(new CustomEvent('scribe:publish-result', {
        detail: {
          canvasId: 'canvas-a',
          ok: true,
          requestId: event.detail.requestId,
          windowId: 'window-b',
        },
      }));
      eventTarget.dispatchEvent(new CustomEvent('scribe:publish-result', {
        detail: {
          canvasId: event.detail.canvasId,
          ok: true,
          requestId: event.detail.requestId,
          windowId: event.detail.windowId,
        },
      }));
    });

    await expect(requestPublishResult({
      detail: {
        canvasId: 'canvas-a',
        requestId: 'publish-scoped',
        windowId: 'window-a',
      },
      eventTarget,
      timeoutMs: 250,
    })).resolves.toEqual({ ok: true, outcome: 'result' });
  });
});
