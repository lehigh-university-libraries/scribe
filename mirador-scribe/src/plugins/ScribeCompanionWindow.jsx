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
import { imageBBoxContainsCenter, wordBBoxBesideSelection } from '../editor/geometry';
import { editorKeyboardCommand } from '../editor/keyboard';
import {
  annotationBBox,
  annotationCanvasId,
  annotationIntersectsImageRect,
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
import { useEditorTranscription } from './useEditorTranscription';
import { useRemoteAnnotationRebase } from './useRemoteAnnotationRebase';
import { useAnnotationCreationBridge } from './useAnnotationCreationBridge';
import { useInlineEditorBridge } from './useInlineEditorBridge';
import { useAnnotationMutationBridge } from './useAnnotationMutationBridge';
import { useEditorRequestBridge, useViewportBridge } from './useEditorRequestBridge';

/**
 * @typedef {import('../types/scribe').EditorSessionAction} EditorSessionAction
 * @typedef {import('../types/scribe').EditorSessionCache} EditorSessionCache
 * @typedef {import('../types/scribe').IIIFAnnotationPage} IIIFAnnotationPage
 * @typedef {import('../types/scribe').MiradorState} MiradorState
 * @typedef {import('../types/scribe').ScribeAdapterFactory} ScribeAdapterFactory
 * @typedef {import('../types/scribe').ScribeAdapterLike} ScribeAdapterLike
 * @typedef {'none' | 'edit' | 'read' | 'outline'} OverlayMode
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
  /** @param {OverlayMode} current @returns {OverlayMode} */
  function cycleOverlayMode(current) {
    if (current === 'none') return 'edit';
    if (current === 'edit') return 'read';
    if (current === 'read') return 'outline';
    return 'none'; // 'outline' → 'none'
  }

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
  const [transcribeDialogOpen, setTranscribeDialogOpen] = useState(false);
  const [batchTranscriptionState, setBatchTranscriptionState] = useState(() => ({
    active: true,
    canvasId,
  }));
  const [transcribeSelection, setTranscribeSelection] = useState(/** @type {string[]} */ ([]));
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
  const foregroundTranscriptionIsBlocked = useCallback(() => {
    const current = batchTranscriptionStateRef.current;
    return current.canvasId !== canvasId || current.active;
  }, [canvasId]);
  const inlineEditorVisible = overlayMode === 'edit';
  const textOverlayVisible = overlayMode === 'read';

  useEffect(() => {
    setTranscribeDialogOpen(false);
  }, [canvasId]);

  useEffect(() => {
    if (batchTranscriptionActive) setTranscribeDialogOpen(false);
  }, [batchTranscriptionActive]);

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

  /** @param {string} targetCanvasId @returns {ScribeAdapterLike} */
  const requireAdapter = useCallback((targetCanvasId) => {
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

  function cycleOverlayModeFromToolbar() {
    setDrawMode(false);
    setOverlayMode(cycleOverlayMode);
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
  const visibleLineAnnotations = useMemo(
    () => visibleAnnotations.filter(isLineAnnotation),
    [visibleAnnotations],
  );
  const hasPageLines = useMemo(
    () => annotations.some(isLineAnnotation),
    [annotations],
  );
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

  useEffect(() => {
    const validIds = new Set(visibleLineAnnotations.map((annotation) => annotation.id));
    const selectedLineId = selectedLineAnnotation?.id || '';
    const preferred = validIds.has(selectedLineId) ? selectedLineId : visibleLineAnnotations[0]?.id || '';
    setTranscribeSelection((current) => {
      const retained = current.filter((id) => validIds.has(id));
      if (retained.length > 0) return retained;
      return preferred ? [preferred] : [];
    });
  }, [selectedLineAnnotation?.id, visibleLineAnnotations]);

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
        const targetId = focusedWordAnnotationId || selectedAnnotation?.id;
        if (targetId) {
          handleDelete(targetId);
        }
      } else if (command === 'dismiss-overlay') {
        setDrawMode(false);
        setOverlayMode('none');
      } else if (command === 'edit-overlay') {
        setDrawMode(false);
        setOverlayMode('edit');
      } else if (command === 'split-line') {
        structuralEdits.openSplit();
      } else if (command === 'join-lines') {
        structuralEdits.openJoinLines();
      } else if (command === 'join-words') {
        structuralEdits.openJoinWords();
      } else if (command === 'retranscribe') {
        if (!foregroundTranscriptionIsBlocked() && !isBusy && hasPageLines) {
          setTranscribeDialogOpen(true);
        }
      } else if (command === 'publish') {
        void handlePublish();
      }
    };

    window.addEventListener('keydown', handleKeyDown);
    return () => window.removeEventListener('keydown', handleKeyDown);
  }, [adapterFactory, canvasId, focusedWordAnnotationId, foregroundTranscriptionIsBlocked, hasPageLines, isBusy, isFocusedWindow, localPage, selectedAnnotation, structuralEdits]);

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
    document.dispatchEvent(new CustomEvent('scribe:editor-state', {
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
        windowId,
      },
    }));
  }, [canvasId, localPage, selectedAnnotation, windowId]);

  useEffect(() => {
    if (didInitialSnapRef.current) return;
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
  }, [annotations, canvasId, windowId]);

  useAnnotationCreationBridge({
    canvasId,
    editingIsBlocked,
    localPage,
    pushHistory,
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
  const { handleTranscribe } = useEditorTranscription({
    activeCanvasRef,
    adapterFactory,
    applyTransformResult,
    canvasId,
    editingIsBlocked,
    foregroundTranscriptionIsBlocked,
    localPage,
    mountedRef,
    operationBusyRef,
    overlayMode,
    requireAdapter,
    selectedAnnotation,
    setDialogOpen: setTranscribeDialogOpen,
    setOperationBusy,
    setOperationBusyState,
    setOverlayMode,
    setStatusMessage,
    transcribeSelection,
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
    const result = applyAdapterMutationToPage(localPage, { annotationId, operation: 'delete' });
    const nextPage = result.page;
    pushHistory(nextPage);
    const nextSelection = selectionAfterPageTransform(localPage, nextPage, [annotationId]);
    setFocusedWordAnnotationId('');
    if (nextSelection) {
      preferredSelectionRef.current = nextSelection;
      selectAnnotation(windowId, nextSelection);
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
    if (word.id) {
      setFocusedWordAnnotationId(word.id);
      selectAnnotation(windowId, word.id);
    }
    setDrawMode(false);
    setOverlayMode('edit');
    setStatusMessage('Draft word created. Save to persist it.');
  }

  async function handleExplode() {
    if (!adapterFactory || !localPage || !selectedLineAnnotation?.id || !canSplitToWords || editingIsBlocked()) return;
    const targetCanvasId = canvasId || annotationCanvasId(selectedLineAnnotation);
    if (!targetCanvasId) return;
    setOperationBusy(true);
    setStatusMessage('Exploding line into words...');
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
      if (activeCanvasRef.current !== targetCanvasId) return;
      setStatusMessage(overlap
        ? 'Words created, but a newer overlapping edit was preserved. Review the pending conflict.'
        : 'Words created.');
    } catch (error) {
      if (activeCanvasRef.current === targetCanvasId) {
        setStatusMessage(error instanceof Error ? error.message : 'Explode failed.');
      }
    } finally {
      setOperationBusy(false);
    }
  }

  function handleUndo() {
    if (editingIsBlocked()) return;
    dispatchSession({ type: 'undo' });
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
      drawMode={drawMode}
      id={id}
      isBusy={isBusy}
      overlayMode={overlayMode}
      onDelete={handleDelete}
      onAddWord={handleAddWord}
      onCreateCenteredLine={createCenteredLine}
      onCreateLine={toggleDrawMode}
      onCycleOverlayMode={cycleOverlayModeFromToolbar}
      onExplode={handleExplode}
      onRedo={handleRedo}
      onPublish={handlePublish}
      onReload={() => reloadAnnotations()}
      onSave={handleSave}
      onTranscribe={handleTranscribe}
      onTranscribeDialogClose={() => setTranscribeDialogOpen(false)}
      onTranscribeDialogOpen={() => {
        if (!foregroundTranscriptionIsBlocked() && !isBusy && hasPageLines) {
          setTranscribeDialogOpen(true);
        }
      }}
      onTranscribeSelectionChange={setTranscribeSelection}
      onUndo={handleUndo}
      pendingRemoteIds={session.pendingRemoteIds}
      saveDisabled={saveDisabled}
      revisionConflict={revisionConflict}
      selectedAnnotation={selectedAnnotation}
      selectedGranularity={selectedGranularity}
      statusMessage={statusMessage}
      structuralEdits={structuralEdits}
      transcribeDialogOpen={transcribeDialogOpen}
      transcribeSelection={transcribeSelection}
      visibleAnnotations={visibleAnnotations}
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
