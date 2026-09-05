import { useEffect } from 'react';

/** @typedef {import('../types/scribe').EditorSessionAction} EditorSessionAction */
/** @typedef {import('../types/scribe').EditorSessionCache} EditorSessionCache */
/** @typedef {import('../types/scribe').IIIFAnnotationPage} IIIFAnnotationPage */
/** @typedef {import('../types/scribe').ScribeAdapterFactory} ScribeAdapterFactory */
/** @typedef {import('../types/scribe').ScribeAdapterLike} ScribeAdapterLike */

/**
 * Loads one canonical page exactly once per active Canvas. The caller retains
 * session-cache ownership; this hook owns only the asynchronous bootstrap
 * lifecycle and stale-response fence.
 *
 * @param {Object} options
 * @param {ScribeAdapterFactory | null | undefined} options.adapterFactory
 * @param {string} options.canvasId
 * @param {(canvasId: string, action: EditorSessionAction) => EditorSessionCache} options.dispatchSessionForCanvas
 * @param {{ current: boolean }} options.didInitialSnapRef
 * @param {{ current: string }} options.loadedCanvasRef
 * @param {(canvasId: string) => ScribeAdapterLike} options.requireAdapter
 * @param {(canvasId: string, pageId: string, page: IIIFAnnotationPage) => unknown} options.receiveAnnotation
 * @param {(id: string) => void} options.setFocusedWordAnnotationId
 * @param {(message: string) => void} options.setStatusMessage
 */
export function useAnnotationBootstrap({
  adapterFactory,
  canvasId,
  didInitialSnapRef,
  dispatchSessionForCanvas,
  loadedCanvasRef,
  receiveAnnotation,
  requireAdapter,
  setFocusedWordAnnotationId,
  setStatusMessage,
}) {
  useEffect(() => {
    let cancelled = false;
    let settled = false;

    async function bootstrapPage() {
      if (!adapterFactory || !canvasId || loadedCanvasRef.current === canvasId) return;
      loadedCanvasRef.current = canvasId;
      dispatchSessionForCanvas(canvasId, { type: 'load-start' });

      try {
        const adapter = requireAdapter(canvasId);
        const snapshot = await adapter.loadSnapshot();
        if (cancelled) return;
        dispatchSessionForCanvas(canvasId, {
          page: snapshot.page,
          revision: snapshot.revision,
          type: 'loaded',
        });
        settled = true;
        if (!snapshot.page.id) throw new Error('The annotation service returned a page without an ID.');
        receiveAnnotation(canvasId, snapshot.page.id, snapshot.page);
        setStatusMessage('');
        setFocusedWordAnnotationId('');
        didInitialSnapRef.current = false;
      } catch (error) {
        loadedCanvasRef.current = '';
        if (!cancelled) {
          const message = error instanceof Error ? error.message : 'Failed to load annotations.';
          dispatchSessionForCanvas(canvasId, { error: message, type: 'load-error' });
          setStatusMessage(message);
        }
      }
    }

    void bootstrapPage();
    return () => {
      cancelled = true;
      if (!settled && loadedCanvasRef.current === canvasId) {
        loadedCanvasRef.current = '';
      }
    };
  }, [
    adapterFactory,
    canvasId,
    didInitialSnapRef,
    dispatchSessionForCanvas,
    loadedCanvasRef,
    receiveAnnotation,
    setFocusedWordAnnotationId,
    setStatusMessage,
  ]);
}
