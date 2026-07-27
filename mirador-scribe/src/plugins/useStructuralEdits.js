import { useEffect, useMemo, useState } from 'react';
import {
  annotationCanvasId,
  annotationText,
  isLineAnnotation,
} from '../utils/iiif';

/** @typedef {import('../types/scribe').EditorRow} EditorRow */
/** @typedef {import('../types/scribe').IIIFAnnotation} IIIFAnnotation */
/** @typedef {import('../types/scribe').IIIFAnnotationPage} IIIFAnnotationPage */
/** @typedef {import('../types/scribe').IdentifiedIIIFAnnotation} IdentifiedIIIFAnnotation */
/** @typedef {import('../types/scribe').ScribeAdapterLike} ScribeAdapterLike */
/** @typedef {'split' | 'join-lines' | 'join-words' | null} StructuralDialog */
/**
 * @typedef {Object} UseStructuralEditsOptions
 * @property {{ current: string }} activeCanvasRef
 * @property {IIIFAnnotation[]} annotations
 * @property {(targetCanvasId: string, submittedPage: IIIFAnnotationPage, transformedPage: IIIFAnnotationPage, selectedIds: string[], options: { atomic: boolean }) => { overlap: boolean }} applyTransformResult
 * @property {boolean} canSplitLine
 * @property {string} canvasId
 * @property {(targetCanvasId?: string) => boolean} editingIsBlocked
 * @property {string} focusedWordAnnotationId
 * @property {IIIFAnnotationPage | null} localPage
 * @property {(targetCanvasId: string) => ScribeAdapterLike} requireAdapter
 * @property {IIIFAnnotation | null} selectedLineAnnotation
 * @property {EditorRow | null} selectedRow
 * @property {(busy: boolean) => void} setOperationBusy
 * @property {(message: string) => void} setStatusMessage
 */

/** @param {string} text @returns {string[]} */
export function splitBoundaryTokens(text) {
  return String(text || '').trim().split(/\s+/).filter(Boolean);
}

/** @param {string[]} requested @param {IIIFAnnotation[]} candidates @returns {string[]} */
export function selectedCandidateIds(requested, candidates) {
  const requestedIds = new Set(requested);
  return candidates.flatMap((candidate) => (
    candidate.id && requestedIds.has(candidate.id) ? [candidate.id] : []
  ));
}

/** @param {UseStructuralEditsOptions} options */
export function useStructuralEdits({
  activeCanvasRef,
  annotations,
  applyTransformResult,
  canSplitLine,
  canvasId,
  editingIsBlocked,
  focusedWordAnnotationId,
  localPage,
  requireAdapter,
  selectedLineAnnotation,
  selectedRow,
  setOperationBusy,
  setStatusMessage,
}) {
  const [dialog, setDialog] = useState(/** @type {StructuralDialog} */ (null));
  const splitTokens = useMemo(
    () => splitBoundaryTokens(annotationText(selectedLineAnnotation)),
    [selectedLineAnnotation],
  );
  const lineCandidates = useMemo(
    () => /** @type {IdentifiedIIIFAnnotation[]} */ (annotations.filter(
      (annotation) => Boolean(annotation.id) && isLineAnnotation(annotation),
    )),
    [annotations],
  );
  const wordCandidates = useMemo(
    () => /** @type {IdentifiedIIIFAnnotation[]} */ (selectedRow?.granularity === 'word'
      ? (selectedRow.fields || []).filter((annotation) => Boolean(annotation.id))
      : []),
    [selectedRow],
  );
  const selectedLineId = selectedLineAnnotation?.id || '';
  const selectedWordId = wordCandidates.some(({ id }) => id === focusedWordAnnotationId)
    ? focusedWordAnnotationId
    : wordCandidates[0]?.id || '';
  const canChooseSplit = Boolean(canSplitLine && selectedLineId && splitTokens.length > 1);
  const canChooseLines = Boolean(selectedLineId && lineCandidates.length > 1);
  const canChooseWords = wordCandidates.length > 1;

  useEffect(() => {
    setDialog(null);
  }, [canvasId, selectedLineId, selectedWordId]);

  /**
   * @param {{
   *   failureMessage: string,
   *   pendingMessage: string,
   *   selectedIds: string[],
   *   successMessage: string,
   *   transform: (adapter: ScribeAdapterLike, submittedPage: IIIFAnnotationPage) => Promise<IIIFAnnotationPage>,
   * }} operation
   */
  async function runTransform({
    failureMessage,
    pendingMessage,
    selectedIds,
    successMessage,
    transform,
  }) {
    if (!localPage || selectedIds.length === 0 || editingIsBlocked()) return false;
    const targetAnnotation = annotations.find(({ id }) => id === selectedIds[0])
      || selectedLineAnnotation;
    const targetCanvasId = canvasId || annotationCanvasId(targetAnnotation);
    if (!targetCanvasId) return false;

    setDialog(null);
    setOperationBusy(true);
    setStatusMessage(pendingMessage);
    try {
      const submittedPage = localPage;
      const adapter = requireAdapter(targetCanvasId);
      const nextPage = await transform(adapter, submittedPage);
      const { overlap } = applyTransformResult(
        targetCanvasId,
        submittedPage,
        nextPage,
        selectedIds,
        { atomic: true },
      );
      if (activeCanvasRef.current === targetCanvasId) {
        setStatusMessage(overlap
          ? `${successMessage}, but a newer overlapping edit was preserved. Review the pending conflict.`
          : `${successMessage}.`);
      }
      return true;
    } catch (error) {
      if (activeCanvasRef.current === targetCanvasId) {
        setStatusMessage(error instanceof Error ? error.message : failureMessage);
      }
      return false;
    } finally {
      setOperationBusy(false);
    }
  }

  /** @param {number} splitAtWord */
  async function splitAtWord(splitAtWord) {
    if (!canChooseSplit
      || !Number.isInteger(splitAtWord)
      || splitAtWord < 1
      || splitAtWord >= splitTokens.length) return false;
    return runTransform({
      failureMessage: 'Split failed.',
      pendingMessage: 'Splitting line...',
      selectedIds: [selectedLineId],
      successMessage: 'Line split',
      transform: (adapter, submittedPage) => adapter.splitLineIntoTwoLines(
        submittedPage,
        selectedLineId,
        splitAtWord,
      ),
    });
  }

  /** @param {string[]} requestedIds */
  async function joinLines(requestedIds) {
    const selectedIds = selectedCandidateIds(requestedIds, lineCandidates);
    if (!selectedIds.includes(selectedLineId) || selectedIds.length < 2) return false;
    return runTransform({
      failureMessage: 'Join lines failed.',
      pendingMessage: 'Joining selected lines...',
      selectedIds,
      successMessage: 'Lines joined',
      transform: (adapter, submittedPage) => adapter.joinLinesIntoLine(submittedPage, selectedIds),
    });
  }

  /** @param {string[]} requestedIds */
  async function joinWords(requestedIds) {
    const selectedIds = selectedCandidateIds(requestedIds, wordCandidates);
    if (selectedIds.length < 2) return false;
    return runTransform({
      failureMessage: 'Join words failed.',
      pendingMessage: 'Joining selected words...',
      selectedIds,
      successMessage: 'Words joined',
      transform: (adapter, submittedPage) => adapter.joinWordsIntoLine(submittedPage, selectedIds),
    });
  }

  return {
    canChooseLines,
    canChooseSplit,
    canChooseWords,
    closeDialog: () => setDialog(null),
    dialog,
    joinLines,
    joinWords,
    lineCandidates,
    openJoinLines: () => {
      if (canChooseLines && !editingIsBlocked()) setDialog('join-lines');
    },
    openJoinWords: () => {
      if (canChooseWords && !editingIsBlocked()) setDialog('join-words');
    },
    openSplit: () => {
      if (canChooseSplit && !editingIsBlocked()) setDialog('split');
    },
    selectedLineId,
    selectedWordId,
    splitAtWord,
    splitTokens,
    wordCandidates,
  };
}
