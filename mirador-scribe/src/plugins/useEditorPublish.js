import { useEffect, useRef } from 'react';
import { annotationCanvasId } from '../utils/iiif';
import { activeCanvasEventDetail } from './activeCanvas';
import { requestPublishResult } from './publishResult';

/** @typedef {import('../types/scribe').IIIFAnnotation} IIIFAnnotation */
/** @typedef {import('../types/scribe').IIIFAnnotationPage} IIIFAnnotationPage */
/** @typedef {import('../types/scribe').ScribeAdapterFactory} ScribeAdapterFactory */

/**
 * @param {Object} options
 * @param {ScribeAdapterFactory | null | undefined} options.adapterFactory
 * @param {string} options.canvasId
 * @param {IIIFAnnotationPage | null} options.localPage
 * @param {{ current: boolean }} options.mountedRef
 * @param {() => Promise<false | { revision: string | number | bigint }>} options.performSave
 * @param {IIIFAnnotation | null} options.selectedAnnotation
 * @param {(busy: boolean) => void} options.setOperationBusy
 * @param {(message: string) => void} options.setStatusMessage
 * @param {string} options.windowId
 */
export function useEditorPublish({
  adapterFactory,
  canvasId,
  localPage,
  mountedRef,
  performSave,
  selectedAnnotation,
  setOperationBusy,
  setStatusMessage,
  windowId,
}) {
  const publishAbortRef = useRef(/** @type {AbortController | null} */ (null));

  useEffect(() => () => {
    publishAbortRef.current?.abort();
    publishAbortRef.current = null;
  }, []);

  async function handlePublish() {
    if (!localPage) return false;
    setStatusMessage('Saving before publish...');
    const targetCanvasId = canvasId || annotationCanvasId(selectedAnnotation);
    const publishIdentity = activeCanvasEventDetail(adapterFactory, targetCanvasId, windowId);
    const saved = await performSave();
    if (!mountedRef.current) return false;
    if (!saved) {
      setStatusMessage('Publish blocked: save failed.');
      return false;
    }
    if (!String(saved.revision || '').trim()) {
      setStatusMessage('Publish unavailable until the focused page revision has loaded.');
      return false;
    }
    const itemImageId = publishIdentity?.itemImageId || '';
    if (!itemImageId) {
      setStatusMessage('Publish unavailable: the focused Canvas has no item image.');
      return false;
    }

    publishAbortRef.current?.abort();
    const publishController = new AbortController();
    publishAbortRef.current = publishController;
    setOperationBusy(true);
    try {
      setStatusMessage('Publishing edits...');
      const result = await requestPublishResult({
        detail: {
          expectedRevision: String(saved.revision),
          canvasId: targetCanvasId,
          itemImageId,
          requestId: `publish-${Date.now()}`,
          windowId,
        },
        signal: publishController.signal,
      });
      if (publishController.signal.aborted || !mountedRef.current) return false;
      if (result.outcome === 'timeout') {
        setStatusMessage('Publish timed out because no result was received. Try again.');
      } else {
        setStatusMessage(result.ok ? 'Edits published.' : 'Publish failed.');
      }
      return result.ok;
    } finally {
      if (publishAbortRef.current === publishController) {
        publishAbortRef.current = null;
        if (mountedRef.current) setOperationBusy(false);
      }
    }
  }

  return { handlePublish };
}
