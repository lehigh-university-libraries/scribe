import { sessionIsDirty } from '../editor/session';
import {
  dirtyEditorSessions,
  editorSessionForCanvas,
} from '../editor/sessionCache';
import { saveCachedEditorSessions } from '../editor/sessionPersistence';
import { annotationCanvasId } from '../utils/iiif';

/** @typedef {import('../types/scribe').EditorSessionAction} EditorSessionAction */
/** @typedef {import('../types/scribe').EditorSessionCache} EditorSessionCache */
/** @typedef {import('../types/scribe').IIIFAnnotation} IIIFAnnotation */
/** @typedef {import('../types/scribe').IIIFAnnotationPage} IIIFAnnotationPage */
/** @typedef {import('../types/scribe').ScribeAdapterFactory} ScribeAdapterFactory */
/** @typedef {import('../types/scribe').ScribeAdapterLike} ScribeAdapterLike */

/**
 * Owns save/reload orchestration for the per-Canvas session cache. Canonical
 * session reduction remains in the reducer; this hook only sequences adapter
 * I/O and maps failures back to reducer actions and accessible status text.
 *
 * @param {Object} options
 * @param {ScribeAdapterFactory | null | undefined} options.adapterFactory
 * @param {string} options.canvasId
 * @param {(canvasId: string, action: EditorSessionAction) => EditorSessionCache} options.dispatchSessionForCanvas
 * @param {(canvasId?: string) => boolean} options.editingIsBlocked
 * @param {{ current: boolean }} options.saveInFlightRef
 * @param {IIIFAnnotation | null} options.selectedAnnotation
 * @param {{ current: EditorSessionCache }} options.sessionCacheRef
 * @param {(busy: boolean) => void} options.setOperationBusy
 * @param {(message: string) => void} options.setStatusMessage
 * @param {(page: IIIFAnnotationPage | null | undefined, canvasId: string) => Promise<void>} options.syncPage
 */
export function useEditorPersistence({
  adapterFactory,
  canvasId,
  dispatchSessionForCanvas,
  editingIsBlocked,
  saveInFlightRef,
  selectedAnnotation,
  sessionCacheRef,
  setOperationBusy,
  setStatusMessage,
  syncPage,
}) {
  function blockedSaveResult(message = 'A save is already in progress.') {
    setStatusMessage(message);
    return {
      error: new Error(message),
      failedCanvasId: '',
      ok: false,
      remainingCanvasIds: dirtyEditorSessions(sessionCacheRef.current)
        .map(({ canvasId: dirtyCanvasId }) => dirtyCanvasId),
      snapshots: new Map(),
    };
  }

  /**
   * @param {{ requireAllClean?: boolean, successMessage?: string, targetCanvasIds?: string[] }} [options]
   */
  async function persistCachedSessions({
    requireAllClean = false,
    successMessage = 'Saved page.',
    targetCanvasIds,
  } = {}) {
    const factory = adapterFactory;
    if (!factory) return blockedSaveResult('The annotation adapter is unavailable.');
    if (saveInFlightRef.current) return blockedSaveResult();
    if (editingIsBlocked()) {
      return blockedSaveResult('Finish the current editor operation before saving.');
    }

    saveInFlightRef.current = true;
    const requestedCanvasIds = targetCanvasIds?.length
      ? [...targetCanvasIds]
      : dirtyEditorSessions(sessionCacheRef.current).map(({ canvasId: dirtyCanvasId }) => dirtyCanvasId);
    const savingCanvasIds = requestedCanvasIds.filter((targetCanvasId) => (
      sessionIsDirty(editorSessionForCanvas(sessionCacheRef.current, targetCanvasId))
    ));
    if (savingCanvasIds.length > 0) setOperationBusy(true);
    setStatusMessage(savingCanvasIds.length > 0
      ? (requireAllClean ? 'Saving all page changes...' : 'Saving page changes...')
      : successMessage);
    try {
      const result = await saveCachedEditorSessions({
        acceptSaved: (targetCanvasId, action) => {
          dispatchSessionForCanvas(targetCanvasId, action);
        },
        adapterFactory: factory,
        beginSave: (targetCanvasId) => {
          dispatchSessionForCanvas(targetCanvasId, { type: 'save-start' });
        },
        canvasIds: targetCanvasIds,
        getCache: () => sessionCacheRef.current,
        requireAllClean,
        syncPage,
      });

      const resultError = /** @type {(Error & { code?: unknown, cause?: { code?: unknown } }) | null | undefined} */ (result.error);
      const code = String(resultError?.code || resultError?.cause?.code || '').toLowerCase();
      const conflict = resultError?.name === 'RevisionConflict' || code === 'aborted' || code === '10';
      if (result.ok) {
        setStatusMessage(successMessage);
      } else if (conflict) {
        const message = 'This page changed on the server. Reload to rebase your draft, then save again.';
        if (result.failedCanvasId) {
          dispatchSessionForCanvas(result.failedCanvasId, { error: message, type: 'save-conflict' });
        }
        setStatusMessage(message);
      } else if (result.error) {
        const message = result.error.message || 'Save failed.';
        if (result.failedCanvasId) {
          dispatchSessionForCanvas(result.failedCanvasId, { error: message, type: 'save-error' });
        }
        setStatusMessage(message);
      } else {
        setStatusMessage('Save incomplete: newer edits remain unsaved. Save again before continuing.');
      }
      return result;
    } catch (error) {
      const message = error instanceof Error ? error.message : 'Save failed.';
      savingCanvasIds.forEach((targetCanvasId) => {
        dispatchSessionForCanvas(targetCanvasId, { error: message, type: 'save-error' });
      });
      setStatusMessage(message);
      return blockedSaveResult(message);
    } finally {
      saveInFlightRef.current = false;
      setOperationBusy(false);
    }
  }

  async function performSave() {
    const targetCanvasId = canvasId || annotationCanvasId(selectedAnnotation);
    const targetSession = editorSessionForCanvas(sessionCacheRef.current, targetCanvasId);
    if (!targetCanvasId || !targetSession.draftPage) return false;

    const result = await persistCachedSessions({ targetCanvasIds: [targetCanvasId] });
    if (!result.ok) return false;
    return result.snapshots.get(targetCanvasId) || {
      page: targetSession.draftPage,
      revision: targetSession.revision,
    };
  }

  async function performSaveAllDirty() {
    return persistCachedSessions({
      requireAllClean: true,
      successMessage: 'All page changes saved.',
    });
  }

  async function handleSave() {
    await performSave();
  }

  async function reloadAnnotations(
    adapter = adapterFactory?.(canvasId),
    targetCanvasId = canvasId,
  ) {
    if (!adapter || !targetCanvasId || editingIsBlocked(targetCanvasId)) return false;
    dispatchSessionForCanvas(targetCanvasId, { type: 'load-start' });
    setOperationBusy(true);
    setStatusMessage('Reloading server updates...');
    try {
      const snapshot = await adapter.loadSnapshot();
      const nextCache = dispatchSessionForCanvas(targetCanvasId, {
        page: snapshot.page,
        revision: snapshot.revision,
        type: 'loaded',
      });
      const nextSession = editorSessionForCanvas(nextCache, targetCanvasId);
      await syncPage(nextSession.draftPage || snapshot.page, targetCanvasId);
      setStatusMessage(sessionIsDirty(nextSession)
        ? 'Server updates loaded; your unsaved edits were preserved.'
        : 'Server updates loaded.');
      return true;
    } catch (error) {
      const message = error instanceof Error ? error.message : 'Reload failed.';
      dispatchSessionForCanvas(targetCanvasId, { error: message, type: 'load-error' });
      setStatusMessage(message);
      return false;
    } finally {
      setOperationBusy(false);
    }
  }

  return {
    handleSave,
    performSave,
    performSaveAllDirty,
    persistCachedSessions,
    reloadAnnotations,
  };
}
