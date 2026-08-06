import { useEffect } from 'react';
import { annotationLocallyChanged } from '../editor/session';
import { editorSessionForCanvas } from '../editor/sessionCache';
import { annotationCanvasId } from '../utils/iiif';

/** @typedef {import('../types/scribe').EditorSession} EditorSession */
/** @typedef {import('../types/scribe').EditorSessionAction} EditorSessionAction */
/** @typedef {import('../types/scribe').EditorSessionCache} EditorSessionCache */
/** @typedef {import('../types/scribe').IIIFAnnotation} IIIFAnnotation */
/** @typedef {import('../types/scribe').IIIFAnnotationPage} IIIFAnnotationPage */
/** @typedef {import('../types/scribe').ScribeAdapterFactory} ScribeAdapterFactory */
/** @typedef {import('../types/scribe').ScribeAdapterLike} ScribeAdapterLike */
/**
 * @typedef {Object} RemoteEditorEventDetail
 * @property {boolean} [active]
 * @property {IIIFAnnotation} [annotation]
 * @property {string} [canvasId]
 * @property {string | number | bigint} [itemImageId]
 * @property {string} [message]
 * @property {boolean} [persisted]
 * @property {string} [requestId]
 * @property {string} [windowId]
 */

/** @param {Event} event @returns {RemoteEditorEventDetail} */
function remoteEventDetail(event) {
  return /** @type {CustomEvent<RemoteEditorEventDetail>} */ (event).detail || {};
}

/**
 * Reconciles persisted worker results with the reducer draft. The reducer owns
 * the three-way merge; this hook scopes bridge events and obtains the newest
 * canonical snapshot before dispatching that rebase.
 *
 * @param {Object} options
 * @param {ScribeAdapterFactory | null | undefined} options.adapterFactory
 * @param {string} options.canvasId
 * @param {(action: EditorSessionAction) => EditorSessionCache} options.dispatchSession
 * @param {(adapter?: ScribeAdapterLike | null, canvasId?: string) => Promise<boolean | void>} options.reloadAnnotations
 * @param {EditorSession} options.session
 * @param {(active: boolean) => void} options.setBatchTranscriptionActive
 * @param {(message: string) => void} options.setStatusMessage
 * @param {(page: IIIFAnnotationPage | null | undefined, canvasId: string) => Promise<void>} options.syncPage
 * @param {string} options.windowId
 */
export function useRemoteAnnotationRebase({
  adapterFactory,
  canvasId,
  dispatchSession,
  reloadAnnotations,
  session,
  setBatchTranscriptionActive,
  setStatusMessage,
  syncPage,
  windowId,
}) {
  useEffect(() => {
    /** @param {Event} event */
    const handleBatchState = (event) => {
      const detail = remoteEventDetail(event);
      if (detail.windowId !== windowId || detail.canvasId !== canvasId) return;
      if (detail.itemImageId
        && String(detail.itemImageId) !== String(adapterFactory?.(canvasId)?.itemImageId || '')) return;
      if (typeof detail.active === 'boolean') setBatchTranscriptionActive(detail.active);
      if (typeof detail.message === 'string') setStatusMessage(detail.message);
    };

    /** @param {Event} event */
    const handleBatchResult = async (event) => {
      const detail = remoteEventDetail(event);
      if (detail.windowId !== windowId || detail.canvasId !== canvasId || detail.persisted === false) return;
      const annotation = detail.annotation;
      if (!annotation?.id) return;
      const adapter = adapterFactory?.(canvasId);
      if (detail.itemImageId && String(detail.itemImageId) !== String(adapter?.itemImageId || '')) return;
      const resultCanvasId = annotationCanvasId(annotation);
      if (resultCanvasId && canvasId && resultCanvasId !== canvasId) return;
      const localWins = annotationLocallyChanged(session, annotation.id);
      try {
        const snapshot = await adapter?.loadSnapshot();
        if (snapshot?.page) {
          const nextCache = dispatchSession({
            page: snapshot.page,
            revision: snapshot.revision,
            type: 'rebase',
          });
          const nextPage = editorSessionForCanvas(nextCache, canvasId).draftPage;
          await syncPage(nextPage || snapshot.page, canvasId);
        } else {
          const nextCache = dispatchSession({ annotation, type: 'remote-annotation' });
          await syncPage(editorSessionForCanvas(nextCache, canvasId).draftPage, canvasId);
        }
      } catch {
        const nextCache = dispatchSession({ annotation, type: 'remote-annotation' });
        await syncPage(editorSessionForCanvas(nextCache, canvasId).draftPage, canvasId);
      }
      if (localWins) {
        setStatusMessage('Automatic text arrived for a line you edited; your draft was preserved.');
      }
    };

    /** @param {Event} event */
    const handleReload = async (event) => {
      const detail = remoteEventDetail(event);
      if (detail.windowId !== windowId || detail.canvasId !== canvasId) return;
      const adapter = adapterFactory?.(canvasId);
      if (!adapter) return;
      if (detail.itemImageId && String(detail.itemImageId) !== String(adapter.itemImageId || '')) return;
      let ok = false;
      try {
        ok = await reloadAnnotations(adapter, canvasId) !== false;
      } catch {
        ok = false;
      }
      const requestId = detail.requestId?.trim();
      if (!requestId) return;
      document.dispatchEvent(new CustomEvent('scribe:reload-annotations-result', {
        detail: {
          canvasId,
          itemImageId: String(adapter.itemImageId || ''),
          ok,
          requestId,
          windowId,
        },
      }));
    };

    document.addEventListener('scribe:transcription-job-state', handleBatchState);
    document.addEventListener('scribe:transcription-result', handleBatchResult);
    document.addEventListener('scribe:reload-annotations', handleReload);
    const adapter = adapterFactory?.(canvasId);
    const itemImageId = String(adapter?.itemImageId || '');
    const canonicalPageReady = Boolean(session?.draftPage)
      && session.status !== 'loading'
      && session.status !== 'saving';
    if (canvasId && itemImageId && windowId && canonicalPageReady) {
      document.dispatchEvent(new CustomEvent('scribe:remote-rebase-ready', {
        detail: { canvasId, itemImageId, windowId },
      }));
    }
    return () => {
      document.removeEventListener('scribe:transcription-job-state', handleBatchState);
      document.removeEventListener('scribe:transcription-result', handleBatchResult);
      document.removeEventListener('scribe:reload-annotations', handleReload);
    };
  }, [adapterFactory, canvasId, dispatchSession, reloadAnnotations, session, setBatchTranscriptionActive, setStatusMessage, syncPage, windowId]);
}
