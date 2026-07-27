import { applyAdapterMutationToPage } from '../editor/adapterMutation';
import { editorSessionForCanvas } from '../editor/sessionCache';
import { selectionAfterPageTransform } from '../utils/iiif';
import { editorBridgeEventDetail, useDocumentEvent } from './useDocumentEvent';

/** @typedef {import('../types/scribe').EditorSessionAction} EditorSessionAction */
/** @typedef {import('../types/scribe').EditorSessionCache} EditorSessionCache */
/** @typedef {import('../types/scribe').IIIFAnnotationPage} IIIFAnnotationPage */
/** @typedef {import('../types/scribe').ScribeAdapterFactory} ScribeAdapterFactory */

/**
 * Owns adapter-driven draft mutations for one focused Mirador window. The
 * bridge validates the full window, Canvas, and item-image tuple before
 * consulting or mutating the session cache.
 *
 * @param {Object} options
 * @param {ScribeAdapterFactory | null | undefined} options.adapterFactory
 * @param {string} options.canvasId
 * @param {(canvasId: string, action: EditorSessionAction) => EditorSessionCache} options.dispatchSessionForCanvas
 * @param {(canvasId?: string) => boolean} options.editingIsBlocked
 * @param {boolean} options.isFocusedWindow
 * @param {{ current: string }} options.preferredSelectionRef
 * @param {(canvasId: string, pageId: string, page: IIIFAnnotationPage) => unknown} options.receiveAnnotation
 * @param {(windowId: string, annotationId: string) => unknown} options.selectAnnotation
 * @param {{ current: EditorSessionCache }} options.sessionCacheRef
 * @param {(message: string) => void} options.setStatusMessage
 * @param {string} options.windowId
 */
export function useAnnotationMutationBridge({
  adapterFactory,
  canvasId,
  dispatchSessionForCanvas,
  editingIsBlocked,
  isFocusedWindow,
  preferredSelectionRef,
  receiveAnnotation,
  selectAnnotation,
  sessionCacheRef,
  setStatusMessage,
  windowId,
}) {
  useDocumentEvent('scribe:annotation-mutation', (event) => {
    const detail = editorBridgeEventDetail(event);
    if (detail.windowId !== windowId
      || detail.canvasId !== canvasId
      || !isFocusedWindow
      || typeof detail.respond !== 'function') return;
    const targetSession = editorSessionForCanvas(sessionCacheRef.current, canvasId);
    const expectedItemImageId = String(adapterFactory?.(canvasId)?.itemImageId || '');
    if (String(detail.itemImageId || '') !== expectedItemImageId) return;
    if (editingIsBlocked(canvasId)) {
      detail.respond({
        error: new Error('Annotation editing is unavailable while another editor operation is in progress.'),
      });
      return;
    }
    if (!detail.operation) {
      detail.respond({ error: new Error('Annotation mutation requires an operation.') });
      return;
    }
    try {
      const result = applyAdapterMutationToPage(targetSession.draftPage, {
        annotation: detail.annotation,
        annotationId: detail.annotationId,
        operation: detail.operation,
      });
      const nextCache = dispatchSessionForCanvas(canvasId, { page: result.page, type: 'edit' });
      const nextSession = editorSessionForCanvas(nextCache, canvasId);
      if (!result.page.id) throw new Error('The draft AnnotationPage has no ID.');
      receiveAnnotation(canvasId, result.page.id, result.page);
      const selectedId = detail.operation === 'delete'
        ? selectionAfterPageTransform(
          targetSession.draftPage,
          result.page,
          detail.annotationId ? [detail.annotationId] : [],
        )
        : result.annotation?.id;
      if (selectedId) {
        preferredSelectionRef.current = selectedId;
        selectAnnotation(windowId, selectedId);
      }
      detail.respond({
        annotation: result.annotation,
        page: nextSession.draftPage || undefined,
        revision: nextSession.revision,
      });
      setStatusMessage('Draft updated. Save to persist it.');
    } catch (error) {
      detail.respond({
        error: error instanceof Error ? error : new Error('Annotation mutation failed.'),
      });
    }
  });
}
