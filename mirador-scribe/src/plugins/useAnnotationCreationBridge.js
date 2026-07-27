import {
  createDraftLineAnnotation,
  upsertAnnotationInPage,
} from '../utils/iiif';
import { editorBridgeEventDetail, useDocumentEvent } from './useDocumentEvent';

/** @typedef {import('../types/scribe').IIIFAnnotationPage} IIIFAnnotationPage */

/**
 * Owns the viewport-to-draft creation bridge for one Mirador window and
 * Canvas. Exact identity checks happen before editability or draft mutation.
 *
 * @param {Object} options
 * @param {string} options.canvasId
 * @param {(canvasId?: string) => boolean} options.editingIsBlocked
 * @param {IIIFAnnotationPage | null} options.localPage
 * @param {(page: IIIFAnnotationPage) => void} options.pushHistory
 * @param {(windowId: string, annotationId: string) => unknown} options.selectAnnotation
 * @param {(active: boolean) => void} options.setDrawMode
 * @param {(mode: 'edit') => void} options.setOverlayMode
 * @param {(message: string) => void} options.setStatusMessage
 * @param {string} options.windowId
 */
export function useAnnotationCreationBridge({
  canvasId,
  editingIsBlocked,
  localPage,
  pushHistory,
  selectAnnotation,
  setDrawMode,
  setOverlayMode,
  setStatusMessage,
  windowId,
}) {
  useDocumentEvent('scribe:create-annotation', (event) => {
    const detail = editorBridgeEventDetail(event);
    if (detail.windowId !== windowId || detail.canvasId !== canvasId) return;
    if (!canvasId || !detail.bbox || !localPage || editingIsBlocked(canvasId)) return;

    const created = createDraftLineAnnotation(canvasId, detail.bbox, localPage.id || '');
    pushHistory(upsertAnnotationInPage(localPage, created));
    setDrawMode(false);
    if (detail.focusResizeHandle) setOverlayMode('edit');
    if (created?.id) {
      selectAnnotation(windowId, created.id);
      if (detail.focusResizeHandle) {
        document.dispatchEvent(new CustomEvent('scribe:focus-resize-handle', {
          detail: {
            annotationId: created.id,
            canvasId,
            handle: detail.focusResizeHandle,
            windowId,
          },
        }));
      }
    }
    setStatusMessage(detail.focusResizeHandle
      ? 'Draft line created. Its southeast resize handle is focused; use Arrow keys to resize, or Shift+Arrow for larger steps.'
      : 'Draft line created. Save to persist it.');
  });
}
