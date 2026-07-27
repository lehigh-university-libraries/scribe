import { editorBridgeEventDetail, useDocumentEvent } from './useDocumentEvent';

/**
 * Routes shell save and transcribe-all requests to exactly one Mirador window
 * and Canvas, and correlates save responses with the caller's request ID.
 *
 * @param {Object} options
 * @param {string} options.canvasId
 * @param {(options: { all: boolean }) => void | Promise<void>} options.handleTranscribe
 * @param {() => Promise<{ ok: boolean, remainingCanvasIds: string[] }>} options.performSaveAllDirty
 * @param {string} options.windowId
 */
export function useEditorRequestBridge({
  canvasId,
  handleTranscribe,
  performSaveAllDirty,
  windowId,
}) {
  useDocumentEvent('scribe:request-save', (event) => {
    const detail = editorBridgeEventDetail(event);
    if (detail.windowId !== windowId || detail.canvasId !== canvasId) return;
    const requestId = detail.requestId || '';
    void (async () => {
      const result = await performSaveAllDirty();
      document.dispatchEvent(new CustomEvent('scribe:save-result', {
        detail: {
          dirtyCanvasIds: result.remainingCanvasIds,
          canvasId,
          ok: result.ok,
          requestId,
          windowId,
        },
      }));
    })();
  });

  useDocumentEvent('scribe:request-transcribe-all', (event) => {
    const detail = editorBridgeEventDetail(event);
    if (detail.windowId !== windowId || detail.canvasId !== canvasId) return;
    void handleTranscribe({ all: true });
  });
}
/**
 * Tracks the visible image rectangle reported by one scoped viewport bridge.
 *
 * @param {Object} options
 * @param {string} options.canvasId
 * @param {(bounds: import('../types/scribe').ImageBBox | null) => void} options.setViewportBounds
 * @param {string} options.windowId
 */
export function useViewportBridge({ canvasId, setViewportBounds, windowId }) {
  useDocumentEvent('scribe:viewport-change', (event) => {
    const detail = editorBridgeEventDetail(event);
    if (detail.windowId !== windowId || detail.canvasId !== canvasId) return;
    setViewportBounds(detail.bounds || null);
  });
}
