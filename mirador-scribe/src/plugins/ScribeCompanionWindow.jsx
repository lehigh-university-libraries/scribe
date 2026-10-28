import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import PropTypes from 'prop-types';
import {
  receiveAnnotation as receiveAnnotationAction,
  selectAnnotation as selectAnnotationAction,
} from 'mirador';
import ScribeActionPanel from '../components/ScribeActionPanel';
import { activeCanvasEventDetail, dispatchActiveCanvasEvent, resolveActiveCanvasState } from './activeCanvas';
import { sessionIsDirty } from '../editor/session';
import {
  createEditorSessionCache,
  dirtyEditorSessions,
  editorSessionCacheIsDirty,
  editorSessionCacheReducer,
  editorSessionForCanvas,
} from '../editor/sessionCache';
import { applyAdapterMutationToPage } from '../editor/adapterMutation';
import {
  formatEditMetrics,
  pageEditMetrics,
  recordEditOperation,
} from '../editor/editMetrics';
import { imageBBoxContainsCenter, wordBBoxBesideSelection } from '../editor/geometry';
import { editorKeyboardCommand } from '../editor/keyboard';
import {
  annotationBBox,
  annotationCanvasId,
  annotationIntersectsImageRect,
  annotationText,
  createDraftWordAnnotation,
  editorRowTransformCapabilities,
  editorSelectedAnnotation,
  findEditorRowByAnnotationId,
  groupAnnotationsForEditor,
  isLineAnnotation,
  isWordAnnotation,
  lineAnnotationForSelection,
  selectionAfterPageTransform,
  sortedAnnotations,
  selectedAnnotationIdForWindow,
  upsertAnnotationInPage,
} from '../utils/iiif';
import { useStructuralEdits } from './useStructuralEdits';
import { useAnnotationBootstrap } from './useAnnotationBootstrap';
import { useEditorPersistence } from './useEditorPersistence';
import { useEditorPublish } from './useEditorPublish';
import { useRemoteAnnotationRebase } from './useRemoteAnnotationRebase';
import { useAnnotationCreationBridge } from './useAnnotationCreationBridge';
import { useInlineEditorBridge } from './useInlineEditorBridge';
import { useAnnotationMutationBridge } from './useAnnotationMutationBridge';
import { useEditorRequestBridge, useViewportBridge } from './useEditorRequestBridge';

/**
 * @typedef {import('../types/scribe').AnnotationCorrectionMetric} AnnotationCorrectionMetric
 * @typedef {import('../types/scribe').EditOperationCounts} EditOperationCounts
 * @typedef {import('../types/scribe').EditOperationKind} EditOperationKind
 * @typedef {import('../types/scribe').EditorSessionAction} EditorSessionAction
 * @typedef {import('../types/scribe').EditorSessionCache} EditorSessionCache
 * @typedef {import('../types/scribe').IIIFAnnotationPage} IIIFAnnotationPage
 * @typedef {import('../types/scribe').MiradorState} MiradorState
 * @typedef {import('../types/scribe').ScribeAdapterFactory} ScribeAdapterFactory
 * @typedef {import('../types/scribe').ScribeAdapterLike} ScribeAdapterLike
 * @typedef {import('../types/scribe').ScribeOverlayMode} OverlayMode
 * @typedef {Object} ScribeCompanionWindowProps
 * @property {ScribeAdapterFactory | null | undefined} adapterFactory
 * @property {string} canvasId
 * @property {string} id
 * @property {boolean} isFocusedWindow
 * @property {(canvasId: string, pageId: string, page: IIIFAnnotationPage) => unknown} receiveAnnotation
 * @property {(windowId: string, annotationId: string) => unknown} selectAnnotation
 * @property {string} selectedAnnotationId
 * @property {IIIFAnnotationPage | null} serverPage
 * @property {string} windowId
 * @typedef {{ windowId: string }} WindowOwnProps
 * @typedef {(action: Record<string, unknown>) => unknown} MiradorDispatch
 */

/** @param {IIIFAnnotationPage | null | undefined} page @returns {boolean} */
function pageHasText(page) {
  return (Array.isArray(page?.items) ? page.items : [])
    .some((annotation) => isLineAnnotation(annotation) && annotationText(annotation).trim() !== '');
}

/** @param {ScribeCompanionWindowProps} props */
export function ScribeCompanionWindow({
  adapterFactory,
  canvasId,
  id,
  isFocusedWindow,
  receiveAnnotation,
  selectAnnotation,
  selectedAnnotationId,
  serverPage,
  windowId,
}) {
  const [operationBusy, setOperationBusyState] = useState(false);
  const operationBusyRef = useRef(false);
  /** @param {boolean} value */
  function setOperationBusy(value) {
    operationBusyRef.current = value;
    setOperationBusyState(value);
  }
  const [statusMessage, setStatusMessage] = useState('');
  const [sessionCache, setSessionCache] = useState(() => (
    createEditorSessionCache(canvasId, serverPage || null)
  ));
  const sessionCacheRef = useRef(sessionCache);
  sessionCacheRef.current = sessionCache;
  const dispatchSessionForCanvas = useCallback((/** @type {string} */ targetCanvasId, /** @type {EditorSessionAction} */ action) => {
    const nextCache = editorSessionCacheReducer(sessionCacheRef.current, {
      ...action,
      canvasId: targetCanvasId,
    });
    sessionCacheRef.current = nextCache;
    setSessionCache(nextCache);
    return nextCache;
  }, []);
  const session = useMemo(
    () => editorSessionForCanvas(sessionCache, canvasId),
    [canvasId, sessionCache],
  );
  const isBusy = operationBusy || session.status === 'loading' || session.status === 'saving';
  const revisionConflict = session.status === 'conflict';
  /** @param {EditorSessionAction} action @param {string} [targetCanvasId] @returns {EditorSessionCache} */
  const dispatchSession = (action, targetCanvasId = canvasId) => (
    dispatchSessionForCanvas(targetCanvasId, action)
  );
  const localPage = session.draftPage;
  const [viewportBounds, setViewportBounds] = useState(/** @type {{ x: number, y: number, w: number, h: number } | null} */ (null));
  const [batchTranscriptionState, setBatchTranscriptionState] = useState(() => ({
    active: true,
    canvasId,
  }));
  const [drawMode, setDrawMode] = useState(false);
  const [overlayMode, setOverlayMode] = useState(/** @type {OverlayMode} */ ('none'));
  const [focusedWordAnnotationId, setFocusedWordAnnotationId] = useState('');
  const preferredSelectionRef = useRef('');
  const pendingRedoSelectionRef = useRef(/** @type {{ annotationId: string, canvasId: string } | null} */ (null));
  const didInitialSnapRef = useRef(false);
  const loadedCanvasRef = useRef('');
  const saveInFlightRef = useRef(false);
  const mountedRef = useRef(true);
  const activeCanvasEventRef = useRef('');
  const activeCanvasRef = useRef(canvasId);
  const batchTranscriptionStateRef = useRef({ active: true, canvasId });
  const operationCountsRef = useRef(/** @type {Map<string, EditOperationCounts>} */ (new Map()));
  const autoReadCanvasesRef = useRef(/** @type {Set<string>} */ (new Set()));
  activeCanvasRef.current = canvasId;
  if (batchTranscriptionStateRef.current.canvasId !== canvasId) {
    batchTranscriptionStateRef.current = { active: true, canvasId };
  }
  const batchTranscriptionActive = batchTranscriptionState.canvasId !== canvasId
    || batchTranscriptionState.active;
  const setBatchTranscriptionActive = useCallback((/** @type {boolean} */ active) => {
    const next = { active, canvasId };
    batchTranscriptionStateRef.current = next;
    setBatchTranscriptionState((current) => (
      current.canvasId === next.canvasId && current.active === next.active ? current : next
    ));
  }, [canvasId]);
  const inlineEditorVisible = overlayMode === 'edit';
  const textOverlayVisible = overlayMode === 'read';

  const recordOperation = useCallback((/** @type {EditOperationKind} */ kind, targetCanvasId = activeCanvasRef.current) => {
    if (!targetCanvasId) return;
    const counts = operationCountsRef.current.get(targetCanvasId) || {};
    operationCountsRef.current.set(targetCanvasId, recordEditOperation(counts, kind));
  }, []);

  // Mirador's annotation slice is a rendered projection only. Keep it aligned
  // with the reducer draft so undo, structural transforms, and unsaved geometry
  // edits never leave Mirador overlays showing an older server page.
  useEffect(() => {
    if (!canvasId || !localPage?.id) return;
    receiveAnnotation(canvasId, localPage.id, localPage);
  }, [canvasId, localPage, receiveAnnotation]);

  /** @param {string} [targetCanvasId] @returns {boolean} */
  function editingIsBlocked(targetCanvasId = canvasId) {
    const targetSession = editorSessionForCanvas(sessionCacheRef.current, targetCanvasId);
    return operationBusyRef.current
      || saveInFlightRef.current
      || targetSession.status === 'loading'
      || targetSession.status === 'saving';
  }

  /** @returns {ScribeAdapterLike} */
  const requireAdapter = useCallback((/** @type {string} */ targetCanvasId) => {
    const adapter = adapterFactory?.(targetCanvasId);
    if (!adapter) throw new Error(`Canvas ${targetCanvasId} has no annotation adapter.`);
    return adapter;
  }, [adapterFactory]);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);

  useEffect(() => {
    if (!isFocusedWindow || !canvasId) {
      activeCanvasEventRef.current = '';
      return;
    }
    const detail = activeCanvasEventDetail(adapterFactory, canvasId, windowId);
    if (!detail) return;
    const eventKey = `${detail.windowId}\u0000${detail.canvasId}\u0000${detail.itemImageId}`;
    if (activeCanvasEventRef.current === eventKey) return;
    activeCanvasEventRef.current = eventKey;
    dispatchActiveCanvasEvent(detail);
  }, [adapterFactory, canvasId, isFocusedWindow, windowId]);

  function toggleDrawMode() {
    autoReadCanvasesRef.current.add(canvasId);
    setDrawMode((current) => {
      const next = !current;
      if (next) {
        setOverlayMode('none');
      }
      return next;
    });
  }

  function createCenteredLine() {
    if (!canvasId || editingIsBlocked(canvasId)) return;
    setDrawMode(false);
    document.dispatchEvent(new CustomEvent('scribe:create-line-at-viewport-center', {
      detail: { canvasId, windowId },
    }));
  }

  /** @param {OverlayMode} mode */
  function selectOverlayMode(mode) {
    autoReadCanvasesRef.current.add(canvasId);
    setDrawMode(false);
    setOverlayMode(mode);
  }

  function requestReprocess() {
    if (batchTranscriptionActive || isBusy) return;
    const detail = activeCanvasEventDetail(adapterFactory, canvasId, windowId);
    if (!detail) {
      setStatusMessage('Reprocessing is unavailable until the focused page has loaded.');
      return;
    }
    setDrawMode(false);
    document.dispatchEvent(new CustomEvent('scribe:request-reprocess', { detail }));
  }

  useAnnotationBootstrap({
    adapterFactory,
    canvasId,
    didInitialSnapRef,
    dispatchSessionForCanvas,
    loadedCanvasRef,
    receiveAnnotation,
    requireAdapter,
    setFocusedWordAnnotationId,
    setStatusMessage,
  });

  const annotations = useMemo(() => sortedAnnotations(localPage), [localPage]);
  const visibleAnnotations = useMemo(() => {
    if (!viewportBounds) return annotations;
    return annotations.filter((annotation) => annotationIntersectsImageRect(annotation, viewportBounds));
  }, [annotations, viewportBounds]);
  const hasPageLines = useMemo(
    () => annotations.some(isLineAnnotation),
    [annotations],
  );
  const hasPageText = useMemo(() => pageHasText(localPage), [localPage]);
  const visibleRows = useMemo(() => groupAnnotationsForEditor({ items: visibleAnnotations }), [visibleAnnotations]);
  const selectedAnnotation = useMemo(
    () => editorSelectedAnnotation(
      annotations,
      visibleAnnotations,
      selectedAnnotationId,
      preferredSelectionRef.current,
    ),
    [annotations, visibleAnnotations, selectedAnnotationId],
  );
  const effectiveSelectedAnnotationId = selectedAnnotation?.id || '';
  const selectedRow = useMemo(
    () => findEditorRowByAnnotationId(localPage || serverPage, effectiveSelectedAnnotationId)
      || findEditorRowByAnnotationId(localPage || serverPage, selectedAnnotation?.id || ''),
    [effectiveSelectedAnnotationId, localPage, selectedAnnotation?.id, serverPage],
  );
  const selectedGranularity = selectedRow?.granularity || (selectedAnnotation ? (isWordAnnotation(selectedAnnotation) ? 'word' : 'line') : null);
  const selectedLineAnnotation = useMemo(() => {
    const rowLead = selectedRow?.lead;
    return (isLineAnnotation(rowLead) ? rowLead : null)
      || lineAnnotationForSelection(localPage, selectedAnnotation);
  }, [localPage, selectedAnnotation, selectedRow]);
  const focusedWordAnnotation = useMemo(() => {
    if (!focusedWordAnnotationId) return null;
    const candidate = annotations.find((annotation) => annotation.id === focusedWordAnnotationId) || null;
    return candidate && isWordAnnotation(candidate) ? candidate : null;
  }, [annotations, focusedWordAnnotationId]);
  // Deleting acts on the most specific thing the person is looking at: the
  // focused word when there is one, otherwise the selected annotation.
  const deleteTarget = useMemo(() => {
    const target = focusedWordAnnotation || selectedAnnotation;
    if (!target?.id) return null;
    return {
      granularity: isWordAnnotation(target) ? /** @type {const} */ ('word') : /** @type {const} */ ('line'),
      id: target.id,
      text: annotationText(target),
    };
  }, [focusedWordAnnotation, selectedAnnotation]);
  const { canSplitLine, canSplitToWords } = useMemo(
    () => editorRowTransformCapabilities(selectedRow),
    [selectedRow],
  );
  const saveDisabled = !sessionIsDirty(session);
  const dirtyCanvasIds = useMemo(
    () => dirtyEditorSessions(sessionCache).map(({ canvasId: dirtyCanvasId }) => dirtyCanvasId),
    [sessionCache],
  );
  const hasDirtySessions = editorSessionCacheIsDirty(sessionCache);
  const structuralEdits = useStructuralEdits({
    activeCanvasRef,
    annotations,
    applyTransformResult,
    canSplitLine,
    canvasId,
    editingIsBlocked,
    focusedWordAnnotationId,
    localPage,
    recordOperation,
    requireAdapter,
    selectedLineAnnotation,
    selectedRow,
    setOperationBusy,
    setStatusMessage,
  });
  const canJoinWords = structuralEdits.canChooseWords;
  const canJoinLines = structuralEdits.canChooseLines;

  useEffect(() => {
    document.dispatchEvent(new CustomEvent('scribe:dirty-state', {
      detail: {
        activeCanvasId: canvasId || '',
        dirty: hasDirtySessions,
        dirtyCanvasIds,
        windowId,
      },
    }));
  }, [canvasId, dirtyCanvasIds, hasDirtySessions, windowId]);

  // Text arriving on a page whose overlay is off (a fresh upload finishing
  // its automatic transcription, or opening an already transcribed page)
  // turns the read overlay on once so the transcription is visible without
  // a further click. An explicit mode choice is never overridden.
  useEffect(() => {
    if (!hasPageText) return;
    if (autoReadCanvasesRef.current.has(canvasId)) return;
    autoReadCanvasesRef.current.add(canvasId);
    if (overlayMode === 'none' && !drawMode) setOverlayMode('read');
  }, [canvasId, drawMode, hasPageText, overlayMode]);

  useEffect(() => {
    const pendingRedoSelection = pendingRedoSelectionRef.current;
    if (pendingRedoSelection && pendingRedoSelection.canvasId !== canvasId) {
      pendingRedoSelectionRef.current = null;
    }
    const restoredRedoSelection = annotations.find((annotation) => (
      pendingRedoSelection?.canvasId === canvasId
      && annotation?.id === pendingRedoSelection.annotationId
    ));
    if (restoredRedoSelection?.id) {
      preferredSelectionRef.current = restoredRedoSelection.id;
      if (selectedAnnotationId !== restoredRedoSelection.id) {
        selectAnnotation(windowId, restoredRedoSelection.id);
      } else {
        pendingRedoSelectionRef.current = null;
      }
      return;
    }
    const selectionIsOnActivePage = annotations.some((annotation) => (
      annotation?.id === selectedAnnotationId
    ));
    if (selectionIsOnActivePage) {
      preferredSelectionRef.current = selectedAnnotationId;
      return;
    }
    const preferred = annotations.find((annotation) => annotation?.id === preferredSelectionRef.current)?.id
      || annotations[0]?.id;
    if (preferred) {
      preferredSelectionRef.current = preferred;
      selectAnnotation(windowId, preferred);
    }
  }, [annotations, canvasId, selectedAnnotationId, selectAnnotation, windowId]);

  useEffect(() => {
    if (!localPage || !effectiveSelectedAnnotationId) {
      if (focusedWordAnnotationId) setFocusedWordAnnotationId('');
      return;
    }
    const selectedRow = findEditorRowByAnnotationId(localPage, effectiveSelectedAnnotationId);
    if (!selectedRow || selectedRow.granularity !== 'word') {
      if (focusedWordAnnotationId) setFocusedWordAnnotationId('');
      return;
    }
    const rowWordIds = new Set(selectedRow.fields.map((annotation) => annotation.id));
    if (!focusedWordAnnotationId || !rowWordIds.has(focusedWordAnnotationId)) {
      setFocusedWordAnnotationId(selectedRow.fields[0]?.id || '');
    }
  }, [effectiveSelectedAnnotationId, focusedWordAnnotationId, localPage]);

  useViewportBridge({ canvasId, setViewportBounds, windowId });

  useEffect(() => {
    /** @param {KeyboardEvent} event */
    const handleKeyDown = (event) => {
      if (event.defaultPrevented || !isFocusedWindow) return;
      const command = editorKeyboardCommand(event);
      if (!command) return;
      event.preventDefault();
      if (command === 'save') {
        void handleSave();
      } else if (command === 'undo') {
        handleUndo();
      } else if (command === 'redo') {
        handleRedo();
      } else if (command === 'delete') {
        if (deleteTarget) handleDelete(deleteTarget.id);
      } else if (command === 'dismiss-overlay') {
        selectOverlayMode('none');
      } else if (command === 'edit-overlay') {
        selectOverlayMode('edit');
      } else if (command === 'read-overlay') {
        selectOverlayMode('read');
      } else if (command === 'transcript-overlay') {
        selectOverlayMode('transcript');
      } else if (command === 'add-line') {
        createCenteredLine();
      } else if (command === 'add-word') {
        handleAddWord();
      } else if (command === 'split-line') {
        structuralEdits.openSplit();
      } else if (command === 'join-lines') {
        structuralEdits.openJoinLines();
      } else if (command === 'join-words') {
        structuralEdits.openJoinWords();
      } else if (command === 'reprocess') {
        requestReprocess();
      } else if (command === 'publish') {
        void handlePublish();
      }
    };

    window.addEventListener('keydown', handleKeyDown);
    return () => window.removeEventListener('keydown', handleKeyDown);
  }, [adapterFactory, batchTranscriptionActive, canvasId, deleteTarget, focusedWordAnnotationId, hasPageLines, isBusy, isFocusedWindow, localPage, selectedAnnotation, structuralEdits]);

  useEffect(() => {
    document.dispatchEvent(new CustomEvent('scribe:set-draw-mode', {
      detail: {
        active: drawMode,
        canvasId,
        windowId,
      },
    }));
  }, [canvasId, drawMode, windowId]);

  useEffect(() => {
    const publishState = () => document.dispatchEvent(new CustomEvent('scribe:editor-state', {
      detail: {
        annotationPage: localPage || serverPage || null,
        canJoinLines,
        canJoinWords,
        canvasId,
        drawMode,
        focusedWordAnnotationId,
        hasRevision: Boolean(String(session.revision || '').trim()),
        inlineEditorVisible,
        isBusy,
        overlayMode,
        pendingRemoteIds: session.pendingRemoteIds,
        saveDisabled,
        selectedAnnotationId: effectiveSelectedAnnotationId,
        selectedGranularity,
        statusMessage,
        sessionStatus: session.status,
        textOverlayVisible,
        windowId,
      },
    }));
    /** @param {CustomEvent<import('../types/scribe').ScribeEditorStateRequestEventDetail>} event */
    const handleRequest = (event) => {
      if (event.detail?.windowId === windowId && event.detail?.canvasId === canvasId) {
        publishState();
      }
    };
    document.addEventListener('scribe:request-editor-state', handleRequest);
    publishState();
    return () => document.removeEventListener('scribe:request-editor-state', handleRequest);
  }, [
    canJoinLines,
    canJoinWords,
    canvasId,
    drawMode,
    effectiveSelectedAnnotationId,
    focusedWordAnnotationId,
    inlineEditorVisible,
    isBusy,
    localPage,
    overlayMode,
    session.pendingRemoteIds,
    session.revision,
    session.status,
    saveDisabled,
    selectedAnnotation,
    selectedGranularity,
    serverPage,
    statusMessage,
    textOverlayVisible,
    windowId,
  ]);

  useEffect(() => {
    if (!selectedAnnotation) return;
    const focusTarget = lineAnnotationForSelection(localPage, selectedAnnotation) || selectedAnnotation;
    document.dispatchEvent(new CustomEvent('scribe:focus-annotation', {
      detail: {
        bbox: annotationBBox(focusTarget),
        canvasId,
        // The transcript pane steps through rows; re-framing every row would
        // make the image jump, so only bring an off-screen line into view.
        ensureVisible: overlayMode === 'transcript',
        windowId,
      },
    }));
  }, [canvasId, localPage, overlayMode, selectedAnnotation, windowId]);

  useEffect(() => {
    if (didInitialSnapRef.current) return;
    if (overlayMode === 'transcript') { didInitialSnapRef.current = true; return; }
    const anchor = annotations[0];
    if (!anchor) return;
    document.dispatchEvent(new CustomEvent('scribe:snap-to-bbox', {
      detail: {
        bbox: annotationBBox(anchor),
        canvasId,
        windowId,
      },
    }));
    didInitialSnapRef.current = true;
  }, [annotations, canvasId, overlayMode, windowId]);

  useAnnotationCreationBridge({
    canvasId,
    editingIsBlocked,
    localPage,
    pushHistory,
    recordOperation,
    selectAnnotation,
    setDrawMode,
    setOverlayMode,
    setStatusMessage,
    windowId,
  });

  useAnnotationMutationBridge({
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
  });

  /** @param {IIIFAnnotationPage | null | undefined} nextPage @param {string} [targetCanvasId] */
  function pushHistory(nextPage, targetCanvasId = canvasId) {
    if (!nextPage) return;
    dispatchSession({ page: nextPage, type: 'edit' }, targetCanvasId);
  }

  /**
   * @param {string} targetCanvasId
   * @param {IIIFAnnotationPage} submittedPage
   * @param {IIIFAnnotationPage} transformedPage
   * @param {string[]} selectedIds
   * @param {{ atomic?: boolean }} [options]
   */
  function applyTransformResult(
    targetCanvasId,
    submittedPage,
    transformedPage,
    selectedIds,
    { atomic = false } = {},
  ) {
    const priorPending = new Set(
      editorSessionForCanvas(sessionCacheRef.current, targetCanvasId).pendingRemoteIds,
    );
    const nextCache = dispatchSessionForCanvas(targetCanvasId, {
      affectedIds: selectedIds,
      atomic,
      page: transformedPage,
      submittedPage,
      type: 'transform-result',
    });
    const nextSession = editorSessionForCanvas(nextCache, targetCanvasId);
    const overlap = (nextSession.status === 'conflict' && nextSession.conflictKind === 'transform')
      || nextSession.pendingRemoteIds.some((annotationId) => !priorPending.has(annotationId));
    if (activeCanvasRef.current === targetCanvasId) {
      const nextSelection = selectionAfterPageTransform(
        submittedPage,
        nextSession.draftPage,
        selectedIds,
      );
      if (nextSelection) {
        preferredSelectionRef.current = nextSelection;
        selectAnnotation(windowId, nextSelection);
      }
    }
    return { nextSession, overlap };
  }

  /** @param {IIIFAnnotationPage | null | undefined} page @param {string} fallbackCanvasId */
  async function syncPage(page, fallbackCanvasId) {
    const targetCanvasId = fallbackCanvasId || canvasId || annotationCanvasId(selectedAnnotation);
    if (!targetCanvasId || !page?.id) return;
    receiveAnnotation(targetCanvasId, page.id, page);
  }

  /**
   * Publishes what one committed save changed. The page diff is computed
   * against the base revision the save replaced; the correction metric is
   * whatever the server derived against the OCR baseline.
   *
   * @param {string} targetCanvasId
   * @param {{ basePage: IIIFAnnotationPage | null, correction: AnnotationCorrectionMetric | null, revision: string, savedPage: IIIFAnnotationPage | null }} saved
   * @returns {string}
   */
  function publishEditMetrics(targetCanvasId, saved) {
    const metrics = pageEditMetrics(saved.basePage, saved.savedPage);
    const operations = operationCountsRef.current.get(targetCanvasId) || {};
    operationCountsRef.current.delete(targetCanvasId);
    const summary = formatEditMetrics(metrics);
    const identity = activeCanvasEventDetail(adapterFactory, targetCanvasId, windowId);
    document.dispatchEvent(new CustomEvent('scribe:edit-metrics', {
      detail: {
        canvasId: targetCanvasId,
        correction: saved.correction,
        itemImageId: identity?.itemImageId || '',
        metrics,
        operations,
        revision: saved.revision,
        summary,
        windowId,
      },
    }));
    return summary;
  }

  const {
    handleSave,
    performSave,
    performSaveAllDirty,
    reloadAnnotations,
  } = useEditorPersistence({
    adapterFactory,
    canvasId,
    dispatchSessionForCanvas,
    editingIsBlocked,
    onSaved: publishEditMetrics,
    saveInFlightRef,
    selectedAnnotation,
    sessionCacheRef,
    setOperationBusy,
    setStatusMessage,
    syncPage,
  });

  const { handlePublish } = useEditorPublish({
    adapterFactory,
    canvasId,
    localPage,
    mountedRef,
    performSave,
    selectedAnnotation,
    setOperationBusy,
    setStatusMessage,
    windowId,
  });

  useEditorRequestBridge({
    canvasId,
    performSaveAllDirty,
    windowId,
  });

  useRemoteAnnotationRebase({
    adapterFactory,
    canvasId,
    dispatchSession,
    reloadAnnotations,
    session,
    setBatchTranscriptionActive,
    setStatusMessage,
    syncPage,
    windowId,
  });

  useInlineEditorBridge({
    canvasId,
    effectiveSelectedAnnotationId,
    editingIsBlocked,
    handleSave,
    localPage,
    pushHistory,
    recordOperation,
    selectedAnnotation,
    selectAnnotation,
    serverPage,
    setDrawMode,
    setFocusedWordAnnotationId,
    setOverlayMode,
    setStatusMessage,
    visibleRows,
    windowId,
  });

  /** @param {string} annotationId */
  function handleDelete(annotationId) {
    if (!localPage || editingIsBlocked()) return;
    const deleted = (localPage.items || []).find((annotation) => annotation?.id === annotationId) || null;
    const result = applyAdapterMutationToPage(localPage, { annotationId, operation: 'delete' });
    const nextPage = result.page;
    pushHistory(nextPage);
    recordOperation('delete');
    const nextSelection = selectionAfterPageTransform(localPage, nextPage, [annotationId]);
    setFocusedWordAnnotationId('');
    if (nextSelection) {
      preferredSelectionRef.current = nextSelection;
      selectAnnotation(windowId, nextSelection);
      const kind = deleted && isWordAnnotation(deleted) ? 'Word' : 'Line';
      setStatusMessage(`${kind} deleted. Undo restores it; Save to persist.`);
    }
    else setStatusMessage('The page is empty. Draw a line to continue editing.');
  }

  function handleAddWord() {
    if (!localPage || !selectedAnnotation || !canvasId || editingIsBlocked()) return;
    const bbox = annotationBBox(selectedAnnotation);
    const candidateLine = selectedRow?.lead && !isWordAnnotation(selectedRow.lead)
      ? selectedRow.lead
      : null;
    const containingLineBBox = candidateLine
      && annotationCanvasId(candidateLine) === annotationCanvasId(selectedAnnotation)
      && imageBBoxContainsCenter(annotationBBox(candidateLine), bbox)
      ? annotationBBox(candidateLine)
      : null;
    const wordBBox = isWordAnnotation(selectedAnnotation)
      ? (containingLineBBox
        ? wordBBoxBesideSelection(bbox, containingLineBBox)
        : {
          h: Math.max(1, bbox.h),
          w: Math.max(1, bbox.w),
          x: bbox.x + bbox.w,
          y: bbox.y,
        })
      : {
        h: Math.max(1, bbox.h),
        w: Math.max(1, Math.round(bbox.w / 6)),
        x: bbox.x,
        y: bbox.y,
      };
    const word = createDraftWordAnnotation(canvasId, wordBBox, localPage.id || '');
    pushHistory(upsertAnnotationInPage(localPage, word));
    recordOperation('word-create');
    if (word.id) {
      setFocusedWordAnnotationId(word.id);
      selectAnnotation(windowId, word.id);
    }
    setDrawMode(false);
    setOverlayMode('edit');
    setStatusMessage('Draft word created. Type its text, then use the Move handle or Arrow keys to place its box. Save to persist it.');
  }

  async function handleExplode() {
    if (!adapterFactory || !localPage || !selectedLineAnnotation?.id || !canSplitToWords || editingIsBlocked()) return;
    const targetCanvasId = canvasId || annotationCanvasId(selectedLineAnnotation);
    if (!targetCanvasId) return;
    setOperationBusy(true);
    setStatusMessage('Splitting line into words...');
    try {
      const submittedPage = localPage;
      const selectedIds = [selectedLineAnnotation.id];
      const adapter = requireAdapter(targetCanvasId);
      const nextPage = await adapter.splitLineIntoWords(submittedPage, selectedLineAnnotation.id);
      const { overlap } = applyTransformResult(
        targetCanvasId,
        submittedPage,
        nextPage,
        selectedIds,
        { atomic: true },
      );
      recordOperation('split-words', targetCanvasId);
      if (activeCanvasRef.current !== targetCanvasId) return;
      setStatusMessage(overlap
        ? 'Words created, but a newer overlapping edit was preserved. Review the pending conflict.'
        : 'Words created. Click any word on the image to correct it.');
    } catch (error) {
      if (activeCanvasRef.current === targetCanvasId) {
        setStatusMessage(error instanceof Error ? error.message : 'Split to words failed.');
      }
    } finally {
      setOperationBusy(false);
    }
  }

  function handleUndo() {
    if (editingIsBlocked()) return;
    dispatchSession({ type: 'undo' });
    recordOperation('undo');
  }

  function handleRedo() {
    if (editingIsBlocked()) return;
    const currentPage = editorSessionForCanvas(sessionCacheRef.current, canvasId).draftPage;
    const previousAnnotationIds = new Set(
      (Array.isArray(currentPage?.items) ? currentPage.items : [])
        .map((annotation) => annotation?.id)
        .filter(Boolean),
    );
    const nextCache = dispatchSession({ type: 'redo' });
    recordOperation('redo');
    const nextPage = editorSessionForCanvas(nextCache, canvasId).draftPage;
    const restoredAnnotations = (Array.isArray(nextPage?.items) ? nextPage.items : [])
      .filter((annotation) => annotation?.id && !previousAnnotationIds.has(annotation.id));
    const restoredAnnotationId = restoredAnnotations.length === 1
      ? String(restoredAnnotations[0]?.id || '')
      : '';
    pendingRedoSelectionRef.current = restoredAnnotationId
      ? { annotationId: restoredAnnotationId, canvasId }
      : null;
  }

  return (
    <ScribeActionPanel
      annotations={annotations}
      batchTranscriptionActive={batchTranscriptionActive}
      canSplitToWords={canSplitToWords}
      deleteTarget={deleteTarget}
      drawMode={drawMode}
      id={id}
      isBusy={isBusy}
      overlayMode={overlayMode}
      onDelete={handleDelete}
      onAddWord={handleAddWord}
      onCreateCenteredLine={createCenteredLine}
      onCreateLine={toggleDrawMode}
      onExplode={handleExplode}
      onRedo={handleRedo}
      onPublish={handlePublish}
      onReload={() => reloadAnnotations()}
      onReprocess={requestReprocess}
      onSave={handleSave}
      onSelectOverlayMode={selectOverlayMode}
      onUndo={handleUndo}
      onRegionType={(annotationId, type) => {
        if (!localPage || editingIsBlocked()) return;
        const next = structuredClone(localPage);
        const region = next.items.find((annotation) => annotation.id === annotationId && annotation.textGranularity === 'block');
        if (!region) return;
        const bodies = Array.isArray(region.body) ? region.body : region.body && typeof region.body === 'object' ? [region.body] : typeof region.body === 'string' ? [{ type:'TextualBody', value:region.body }] : [];
        region.body = [...bodies.filter((body) => body.purpose !== 'classifying'), { type:'TextualBody', purpose:'classifying', value:type }];
        pushHistory(next);
      }}
      pendingRemoteIds={session.pendingRemoteIds}
      saveDisabled={saveDisabled}
      revisionConflict={revisionConflict}
      selectedAnnotation={selectedAnnotation}
      selectedGranularity={selectedGranularity}
      statusMessage={statusMessage}
      structuralEdits={structuralEdits}
      windowId={windowId}
    />
  );
}

ScribeCompanionWindow.propTypes = {
  adapterFactory: PropTypes.func,
  canvasId: PropTypes.string,
  id: PropTypes.string.isRequired,
  isFocusedWindow: PropTypes.bool.isRequired,
  receiveAnnotation: PropTypes.func.isRequired,
  selectAnnotation: PropTypes.func.isRequired,
  selectedAnnotationId: PropTypes.string,
  serverPage: PropTypes.shape({
    id: PropTypes.string,
    items: PropTypes.array,
  }),
  windowId: PropTypes.string.isRequired,
};

/** @param {MiradorState} state @param {WindowOwnProps} ownProps */
function mapStateToProps(state, { windowId }) {
  const selectedAnnotationId = selectedAnnotationIdForWindow(state, windowId);
  const activeCanvas = resolveActiveCanvasState(state, windowId, selectedAnnotationId);

  return {
    adapterFactory: state?.config?.annotation?.adapter,
    canvasId: activeCanvas.canvasId,
    isFocusedWindow: !state?.workspace?.focusedWindowId || state.workspace.focusedWindowId === windowId,
    selectedAnnotationId,
    serverPage: activeCanvas.serverPage,
  };
}

/** @param {MiradorDispatch} dispatch */
const mapDispatchToProps = (dispatch) => ({
  /** @param {string} targetId @param {string} annotationId @param {IIIFAnnotationPage} annotationJson */
  receiveAnnotation: (targetId, annotationId, annotationJson) => dispatch(
    receiveAnnotationAction(targetId, annotationId, annotationJson),
  ),
  /** @param {string} targetWindowId @param {string} annotationId */
  selectAnnotation: (targetWindowId, annotationId) => dispatch(selectAnnotationAction(targetWindowId, annotationId)),
});

const scribeCompanionWindowPlugin = {
  companionWindowKey: 'scribeEditor',
  component: ScribeCompanionWindow,
  mapDispatchToProps,
  mapStateToProps,
};

export default scribeCompanionWindowPlugin;
