import {
  annotationCanvasId,
  isWordAnnotation,
  upsertAnnotationInPage,
} from '../utils/iiif';

/** @typedef {import('../types/scribe').IIIFAnnotation} IIIFAnnotation */
/** @typedef {import('../types/scribe').IIIFAnnotationPage} IIIFAnnotationPage */
/** @typedef {import('../types/scribe').ScribeAdapterFactory} ScribeAdapterFactory */
/** @typedef {import('../types/scribe').ScribeAdapterLike} ScribeAdapterLike */

/**
 * @param {Object} options
 * @param {{ current: string }} options.activeCanvasRef
 * @param {ScribeAdapterFactory | null | undefined} options.adapterFactory
 * @param {(targetCanvasId: string, submittedPage: IIIFAnnotationPage, transformedPage: IIIFAnnotationPage, selectedIds: string[]) => { overlap: boolean }} options.applyTransformResult
 * @param {string} options.canvasId
 * @param {(canvasId?: string) => boolean} options.editingIsBlocked
 * @param {IIIFAnnotationPage | null} options.localPage
 * @param {{ current: boolean }} options.mountedRef
 * @param {{ current: boolean }} options.operationBusyRef
 * @param {'none' | 'read' | 'edit' | 'outline'} options.overlayMode
 * @param {(canvasId: string) => ScribeAdapterLike} options.requireAdapter
 * @param {IIIFAnnotation | null} options.selectedAnnotation
 * @param {(open: boolean) => void} options.setDialogOpen
 * @param {(busy: boolean) => void} options.setOperationBusy
 * @param {(busy: boolean) => void} options.setOperationBusyState
 * @param {(mode: 'none' | 'read' | 'edit' | 'outline') => void} options.setOverlayMode
 * @param {(message: string) => void} options.setStatusMessage
 * @param {string[]} options.transcribeSelection
 * @param {string} options.windowId
 */
export function useEditorTranscription({
  activeCanvasRef,
  adapterFactory,
  applyTransformResult,
  canvasId,
  editingIsBlocked,
  localPage,
  mountedRef,
  operationBusyRef,
  overlayMode,
  requireAdapter,
  selectedAnnotation,
  setDialogOpen,
  setOperationBusy,
  setOperationBusyState,
  setOverlayMode,
  setStatusMessage,
  transcribeSelection,
  windowId,
}) {
  function clearTranscriptionOverlay(targetCanvasId = canvasId) {
    document.dispatchEvent(new CustomEvent('scribe:transcription-segment', {
      detail: { annotation: null, canvasId: targetCanvasId, done: 0, total: 0, windowId },
    }));
  }

  /** @param {{ all?: boolean, annotationIds?: string[] }} [options] */
  async function handleTranscribe({ all = false, annotationIds = [] } = {}) {
    if (!adapterFactory || !localPage || editingIsBlocked()) return;
    const targetCanvasId = canvasId;
    if (!targetCanvasId) return;
    setOperationBusy(true);
    try {
      if (overlayMode !== 'edit') setOverlayMode('none');

      const submittedPage = localPage;
      const targetAnnotations = /** @type {IIIFAnnotation[]} */ ((all
        ? (submittedPage.items || [])
        : (annotationIds.length > 0 ? annotationIds : transcribeSelection)
            .map((id) => (submittedPage.items || []).find((annotation) => annotation?.id === id))
            .filter(Boolean))
        .filter((annotation) => !isWordAnnotation(annotation)));

      const total = targetAnnotations.length;
      setStatusMessage(`Transcribing… 0 / ${total}`);
      let nextPage = submittedPage;
      const failures = [];
      const adapter = requireAdapter(targetCanvasId || annotationCanvasId(selectedAnnotation));

      for (let index = 0; index < targetAnnotations.length; index += 1) {
        const annotation = targetAnnotations[index];
        const done = index + 1;
        setStatusMessage(`Transcribing… ${done} / ${total}`);
        document.dispatchEvent(new CustomEvent('scribe:transcription-segment', {
          detail: { annotation, canvasId: targetCanvasId, done, total, windowId },
        }));

        try {
          // Sequential model requests keep progress deterministic and avoid
          // overwhelming a foreground provider with unbounded fan-out.
          // eslint-disable-next-line no-await-in-loop
          const transcribed = await adapter.transcribeAnnotation(annotation);
          if (!mountedRef.current || activeCanvasRef.current !== targetCanvasId) return;
          if (transcribed) nextPage = upsertAnnotationInPage(nextPage, transcribed);
          document.dispatchEvent(new CustomEvent('scribe:transcription-result', {
            detail: {
              annotation: transcribed || annotation,
              canvasId: targetCanvasId,
              done,
              persisted: false,
              total,
              windowId,
            },
          }));
        } catch (error) {
          failures.push({
            id: annotation?.id || `segment ${done}`,
            message: error instanceof Error ? error.message : 'Retranscription failed',
          });
        }

        if (!mountedRef.current || activeCanvasRef.current !== targetCanvasId) return;
      }

      if (activeCanvasRef.current !== targetCanvasId) return;
      const selectedIds = targetAnnotations.flatMap((annotation) => (annotation.id ? [annotation.id] : []));
      const { overlap } = applyTransformResult(targetCanvasId, submittedPage, nextPage, selectedIds);
      setDialogOpen(false);
      if (overlap) {
        setStatusMessage('Retranscription finished, but newer overlapping edits were preserved. Review the pending conflict.');
      } else if (failures.length > 0) {
        const first = failures[0];
        setStatusMessage(`Retranscribed ${total - failures.length}/${total}. ${first.id}: ${first.message}`);
      } else {
        setStatusMessage(all ? 'Document transcribed.' : 'Selected text transcribed.');
      }
    } catch (error) {
      if (mountedRef.current && activeCanvasRef.current === targetCanvasId) {
        setStatusMessage(error instanceof Error ? error.message : 'Retranscription failed.');
      }
    } finally {
      clearTranscriptionOverlay(targetCanvasId);
      operationBusyRef.current = false;
      if (mountedRef.current) setOperationBusyState(false);
    }
  }

  return { handleTranscribe };
}
