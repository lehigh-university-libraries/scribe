import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import PropTypes from 'prop-types';
import {
  receiveAnnotation as receiveAnnotationAction,
  selectAnnotation as selectAnnotationAction,
} from 'mirador';
import ScribeActionPanel from '../components/ScribeActionPanel';
import { activeCanvasEventDetail, dispatchActiveCanvasEvent, resolveActiveCanvasState } from './activeCanvas';
import {
  annotationLocallyChanged,
  sessionIsDirty,
} from '../editor/session';
import { requestPublishResult } from './publishResult';
import {
  createEditorSessionCache,
  dirtyEditorSessions,
  editorSessionCacheIsDirty,
  editorSessionCacheReducer,
  editorSessionForCanvas,
} from '../editor/sessionCache';
import { saveCachedEditorSessions } from '../editor/sessionPersistence';
import { applyAdapterMutationToPage } from '../editor/adapterMutation';
import { editorKeyboardCommand } from '../editor/keyboard';
import {
  annotationBBox,
  annotationCanvasId,
  annotationIntersectsImageRect,
  createDraftLineAnnotation,
  createDraftWordAnnotation,
  editorRowTransformCapabilities,
  editorSelectedAnnotation,
  findEditorRowByAnnotationId,
  groupAnnotationsForEditor,
  isLineAnnotation,
  isWordAnnotation,
  joinLineCandidates,
  lineAnnotationForSelection,
  selectionAfterPageTransform,
  sortedAnnotations,
  selectedAnnotationIdForWindow,
  synchronizeLineTextFromWords,
  updateRowText,
  upsertAnnotationInPage,
  updateAnnotationBBox,
  updateAnnotationText,
  rowSelectionId,
  wordAnnotationIdForCaret,
} from '../utils/iiif';

/**
 * @typedef {import('../types/scribe').EditorSessionAction} EditorSessionAction
 * @typedef {import('../types/scribe').EditorSessionCache} EditorSessionCache
 * @typedef {import('../types/scribe').IIIFAnnotation} IIIFAnnotation
 * @typedef {import('../types/scribe').IIIFAnnotationPage} IIIFAnnotationPage
 * @typedef {import('../types/scribe').ImageBBox} ImageBBox
 * @typedef {import('../types/scribe').MiradorState} MiradorState
 * @typedef {import('../types/scribe').ScribeAdapterFactory} ScribeAdapterFactory
 * @typedef {import('../types/scribe').ScribeAdapterLike} ScribeAdapterLike
 * @typedef {'none' | 'edit' | 'read' | 'outline'} OverlayMode
 * @typedef {Object} EditorBridgeEventDetail
 * @property {boolean} [active]
 * @property {IIIFAnnotation} [annotation]
 * @property {string} [annotationId]
 * @property {ImageBBox} [bbox]
 * @property {ImageBBox} [bounds]
 * @property {string} [canvasId]
 * @property {number} [direction]
 * @property {string} [focusAnnotationId]
 * @property {'nw' | 'ne' | 'sw' | 'se'} [focusResizeHandle]
 * @property {string | number | bigint} [itemImageId]
 * @property {string} [message]
 * @property {'create' | 'update' | 'delete'} [operation]
 * @property {boolean} [persisted]
 * @property {string} [requestId]
 * @property {(result: import('../types/scribe').DraftMutationResponse) => void} [respond]
 * @property {number | null} [selectionStart]
 * @property {string} [text]
 * @property {string} [windowId]
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

/** @param {Event} event @returns {EditorBridgeEventDetail} */
function editorBridgeEventDetail(event) {
  return /** @type {CustomEvent<EditorBridgeEventDetail>} */ (event).detail || {};
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
  const [transcribeSelection, setTranscribeSelection] = useState(/** @type {string[]} */ ([]));
  const [drawMode, setDrawMode] = useState(false);
  const [overlayMode, setOverlayMode] = useState(/** @type {OverlayMode} */ ('none'));
  const [focusedWordAnnotationId, setFocusedWordAnnotationId] = useState('');
  const preferredSelectionRef = useRef('');
  const didInitialSnapRef = useRef(false);
  const loadedCanvasRef = useRef('');
  const saveInFlightRef = useRef(false);
  const mountedRef = useRef(true);
  const publishAbortRef = useRef(/** @type {AbortController | null} */ (null));
  const activeCanvasEventRef = useRef('');
  const activeCanvasRef = useRef(canvasId);
  activeCanvasRef.current = canvasId;
  const inlineEditorVisible = overlayMode === 'edit';
  const textOverlayVisible = overlayMode === 'read';

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
  function requireAdapter(targetCanvasId) {
    const adapter = adapterFactory?.(targetCanvasId);
    if (!adapter) throw new Error(`Canvas ${targetCanvasId} has no annotation adapter.`);
    return adapter;
  }

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      publishAbortRef.current?.abort();
      publishAbortRef.current = null;
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

  useEffect(() => {
    let cancelled = false;

    async function bootstrapPage() {
      if (!adapterFactory || !canvasId) return;
      if (loadedCanvasRef.current === canvasId) return;
      loadedCanvasRef.current = canvasId;
      dispatchSessionForCanvas(canvasId, { type: 'load-start' });

      try {
        const targetCanvasId = canvasId;
        const adapter = requireAdapter(targetCanvasId);
        const snapshot = await adapter.loadSnapshot();
        if (cancelled) return;
        dispatchSessionForCanvas(targetCanvasId, {
          page: snapshot.page,
          revision: snapshot.revision,
          type: 'loaded',
        });
        if (!snapshot.page.id) throw new Error('The annotation service returned a page without an ID.');
        receiveAnnotation(targetCanvasId, snapshot.page.id, snapshot.page);
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
    };
  }, [adapterFactory, canvasId, dispatchSessionForCanvas, receiveAnnotation]);

  const annotations = useMemo(() => sortedAnnotations(localPage), [localPage]);
  const visibleAnnotations = useMemo(() => {
    if (!viewportBounds) return annotations;
    return annotations.filter((annotation) => annotationIntersectsImageRect(annotation, viewportBounds));
  }, [annotations, viewportBounds]);
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
  const wordJoinCandidates = useMemo(() => (
    selectedRow?.granularity === 'word' ? [...(selectedRow.fields || [])] : []
  ), [selectedRow]);
  const lineJoinCandidates = useMemo(
    () => joinLineCandidates(selectedLineAnnotation, visibleAnnotations),
    [selectedLineAnnotation, visibleAnnotations],
  );
  const canJoinWords = wordJoinCandidates.length > 1;
  const canJoinLines = lineJoinCandidates.length > 1;

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
  }, [annotations, selectedAnnotationId, selectAnnotation, windowId]);

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
    const validIds = new Set(visibleAnnotations.map((annotation) => annotation.id));
    const preferred = selectedAnnotation?.id || visibleAnnotations[0]?.id || '';
    setTranscribeSelection((current) => {
      const retained = current.filter((id) => validIds.has(id));
      if (retained.length > 0) return retained;
      return preferred ? [preferred] : [];
    });
  }, [selectedAnnotation?.id, visibleAnnotations]);

  useEffect(() => {
    /** @param {Event} event */
    const handleViewport = (event) => {
      const detail = editorBridgeEventDetail(event);
      if (detail.windowId !== windowId || detail.canvasId !== canvasId) return;
      setViewportBounds(detail.bounds || null);
    };
    document.addEventListener('scribe:viewport-change', handleViewport);
    return () => document.removeEventListener('scribe:viewport-change', handleViewport);
  }, [canvasId, windowId]);

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
      }
    };

    window.addEventListener('keydown', handleKeyDown);
    return () => window.removeEventListener('keydown', handleKeyDown);
  }, [adapterFactory, canvasId, focusedWordAnnotationId, isFocusedWindow, localPage, selectedAnnotation]);

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

  useEffect(() => {
    /** @param {Event} event */
    const handleCreateAnnotation = (event) => {
      const detail = editorBridgeEventDetail(event);
      if (detail.windowId !== windowId || detail.canvasId !== canvasId) return;
      if (!canvasId || !detail.bbox || !localPage || editingIsBlocked(canvasId)) return;

      const created = createDraftLineAnnotation(canvasId, detail.bbox, localPage.id || '');
      const nextPage = upsertAnnotationInPage(localPage, created);
      pushHistory(nextPage);
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
    };

    document.addEventListener('scribe:create-annotation', handleCreateAnnotation);
    return () => document.removeEventListener('scribe:create-annotation', handleCreateAnnotation);
  }, [canvasId, localPage, selectAnnotation, windowId]);

  useEffect(() => {
    /** @param {Event} event */
    const handleAdapterMutation = (event) => {
      const detail = editorBridgeEventDetail(event);
      if (!detail
        || detail.windowId !== windowId
        || detail.canvasId !== canvasId
        || !isFocusedWindow
        || typeof detail.respond !== 'function') return;
      const targetSession = editorSessionForCanvas(sessionCacheRef.current, canvasId);
      const expectedItemImageId = String(adapterFactory?.(canvasId)?.itemImageId || '');
      if (String(detail.itemImageId || '') !== expectedItemImageId) return;
      if (editingIsBlocked(canvasId)) {
        detail.respond({ error: new Error('Annotation editing is unavailable while another editor operation is in progress.') });
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
          ? selectionAfterPageTransform(targetSession.draftPage, result.page, detail.annotationId ? [detail.annotationId] : [])
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
        detail.respond({ error: error instanceof Error ? error : new Error('Annotation mutation failed.') });
      }
    };

    document.addEventListener('scribe:annotation-mutation', handleAdapterMutation);
    return () => document.removeEventListener('scribe:annotation-mutation', handleAdapterMutation);
  }, [adapterFactory, canvasId, isFocusedWindow, receiveAnnotation, selectAnnotation, windowId]);

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

  /** @param {string} text @param {number | null | undefined} selectionStart */
  function handleInlineTextChange(text, selectionStart) {
    if (!localPage || editingIsBlocked()) return;
    const targetRow = findEditorRowByAnnotationId(localPage, effectiveSelectedAnnotationId)
      || findEditorRowByAnnotationId(localPage, selectedAnnotation?.id || '');
    if (!targetRow) return;

    if (targetRow.granularity === 'word') {
      const activeWordId = wordAnnotationIdForCaret(targetRow, text, selectionStart);
      setFocusedWordAnnotationId(activeWordId || targetRow.fields[0]?.id || '');
      const lineId = rowSelectionId(targetRow);
      if (lineId) selectAnnotation(windowId, lineId);
      pushHistory(updateRowText(localPage, targetRow, text));
    } else {
      setFocusedWordAnnotationId('');
      const targetId = rowSelectionId(targetRow);
      const targetAnnotation = (localPage.items || []).find((annotation) => annotation?.id === targetId);
      if (!targetAnnotation) return;
      pushHistory(upsertAnnotationInPage(localPage, updateAnnotationText(targetAnnotation, text)));
    }
    setStatusMessage('');
  }

  /** @param {string} annotationId @param {string} text */
  function handleInlineWordChange(annotationId, text) {
    if (!localPage || editingIsBlocked()) return;
    const wordAnnotation = (localPage.items || []).find((annotation) => annotation?.id === annotationId);
    if (!wordAnnotation) return;
    const nextPage = upsertAnnotationInPage(localPage, updateAnnotationText(wordAnnotation, text));
    const changedWord = nextPage.items.find((annotation) => annotation?.id === annotationId);
    if (!changedWord) return;
    const syncedPage = synchronizeLineTextFromWords(nextPage, changedWord);
    pushHistory(syncedPage);
    setFocusedWordAnnotationId(annotationId);
    selectAnnotation(windowId, annotationId);
    setStatusMessage('');
  }

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
    if (!adapterFactory) {
      return blockedSaveResult('The annotation adapter is unavailable.');
    }
    if (saveInFlightRef.current) {
      return blockedSaveResult();
    }
    if (operationBusyRef.current) {
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
        adapterFactory,
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

  async function reloadAnnotations(adapter = adapterFactory?.(canvasId), targetCanvasId = canvasId) {
    if (!adapter || !targetCanvasId || editingIsBlocked(targetCanvasId)) return;
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
    } catch (error) {
      const message = error instanceof Error ? error.message : 'Reload failed.';
      dispatchSessionForCanvas(targetCanvasId, { error: message, type: 'load-error' });
      setStatusMessage(message);
    } finally {
      setOperationBusy(false);
    }
  }

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
          expectedRevision: saved.revision,
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

  useEffect(() => {
    /** @param {Event} event */
    const handleSaveRequest = async (event) => {
      const detail = editorBridgeEventDetail(event);
      if (detail.windowId !== windowId || detail.canvasId !== canvasId) return;
      const requestId = detail.requestId || '';
      const result = await performSaveAllDirty();
      document.dispatchEvent(new CustomEvent('scribe:save-result', {
        detail: {
          dirtyCanvasIds: result.remainingCanvasIds,
          canvasId,
          ok: result.ok,
          requestId,
          windowId,
        },
      }));
    };

    document.addEventListener('scribe:request-save', handleSaveRequest);
    return () => document.removeEventListener('scribe:request-save', handleSaveRequest);
  }, [windowId, adapterFactory, canvasId, isFocusedWindow, receiveAnnotation]);

  useEffect(() => {
    /** @param {Event} event */
    const handleTranscribeAll = (event) => {
      const detail = editorBridgeEventDetail(event);
      if (detail.windowId !== windowId || detail.canvasId !== canvasId) return;
      void handleTranscribe({ all: true });
    };
    document.addEventListener('scribe:request-transcribe-all', handleTranscribeAll);
    return () => document.removeEventListener('scribe:request-transcribe-all', handleTranscribeAll);
  }, [windowId, adapterFactory, canvasId, isFocusedWindow, localPage, selectedAnnotation, transcribeSelection]);

  useEffect(() => {
    /** @param {Event} event */
    const handleBatchState = (event) => {
      const detail = editorBridgeEventDetail(event);
      if (detail.windowId !== windowId || detail.canvasId !== canvasId) return;
      if (detail.itemImageId && String(detail.itemImageId) !== String(adapterFactory?.(canvasId)?.itemImageId || '')) return;
      const { message } = detail;
      if (typeof message === 'string') {
        setStatusMessage(message);
      }
    };

    /** @param {Event} event */
    const handleBatchResult = async (event) => {
      const detail = editorBridgeEventDetail(event);
      if (detail.windowId !== windowId || detail.canvasId !== canvasId) return;
      if (detail.persisted === false) return;
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
      const detail = editorBridgeEventDetail(event);
      if (detail.windowId !== windowId || detail.canvasId !== canvasId) return;
      const adapter = adapterFactory?.(canvasId);
      if (!adapter) return;
      if (detail.itemImageId && String(detail.itemImageId) !== String(adapter.itemImageId || '')) return;
      await reloadAnnotations(adapter);
    };

    document.addEventListener('scribe:transcription-job-state', handleBatchState);
    document.addEventListener('scribe:transcription-result', handleBatchResult);
    document.addEventListener('scribe:reload-annotations', handleReload);
    return () => {
      document.removeEventListener('scribe:transcription-job-state', handleBatchState);
      document.removeEventListener('scribe:transcription-result', handleBatchResult);
      document.removeEventListener('scribe:reload-annotations', handleReload);
    };
  }, [adapterFactory, canvasId, session, windowId]);

  useEffect(() => {
    /** @param {Event} event */
    const handleInlineChange = (event) => {
      const detail = editorBridgeEventDetail(event);
      if (detail.windowId !== windowId || detail.canvasId !== canvasId) return;
      handleInlineTextChange(detail.text || '', detail.selectionStart);
    };
    /** @param {Event} event */
    const handleInlineStep = (event) => {
      const detail = editorBridgeEventDetail(event);
      if (detail.windowId !== windowId || detail.canvasId !== canvasId) return;
      if (editingIsBlocked()) return;
      if (visibleRows.length === 0) return;
      const currentRowId = lineAnnotationForSelection(localPage, selectedAnnotation)?.id || effectiveSelectedAnnotationId;
      const currentIndex = visibleRows.findIndex((row) => (
        row.lead?.id === currentRowId
          || row.fields.some((annotation) => annotation.id === currentRowId)
      ));
      const direction = detail.direction === -1 ? -1 : 1;
      const nextIndex = ((currentIndex >= 0 ? currentIndex : 0) + direction + visibleRows.length) % visibleRows.length;
      const nextRow = visibleRows[nextIndex];
      const nextSelection = rowSelectionId(nextRow);
      setFocusedWordAnnotationId(nextRow?.granularity === 'word' ? (nextRow.fields[0]?.id || '') : '');
      if (nextSelection) selectAnnotation(windowId, nextSelection);
    };
    /** @param {Event} event */
    const handleInlineSave = (event) => {
      const detail = editorBridgeEventDetail(event);
      if (detail.windowId !== windowId || detail.canvasId !== canvasId) return;
      void handleSave();
    };
    /** @param {Event} event */
    const handleOverlaySelect = (event) => {
      const detail = editorBridgeEventDetail(event);
      if (detail.windowId !== windowId || detail.canvasId !== canvasId) return;
      if (editingIsBlocked()) return;
      if (!detail.annotationId) return;
      const sourcePage = localPage || serverPage;
      const clickedAnnotation = (sourcePage?.items || []).find((annotation) => annotation?.id === detail.annotationId) || null;
      setDrawMode(false);
      setOverlayMode('edit');
      setFocusedWordAnnotationId(detail.focusAnnotationId || (clickedAnnotation && isWordAnnotation(clickedAnnotation) ? clickedAnnotation.id || '' : ''));
      selectAnnotation(windowId, detail.focusAnnotationId || detail.annotationId);
    };
    /** @param {Event} event */
    const handleInlineWord = (event) => {
      const detail = editorBridgeEventDetail(event);
      if (detail.windowId !== windowId || detail.canvasId !== canvasId) return;
      if (!detail.annotationId) return;
      handleInlineWordChange(detail.annotationId, detail.text || '');
    };
    /** @param {Event} event */
    const handleResizeAnnotation = (event) => {
      const detail = editorBridgeEventDetail(event);
      if (detail.windowId !== windowId || detail.canvasId !== canvasId) return;
      if (editingIsBlocked()) return;
      const { annotationId, bbox } = detail;
      if (!annotationId || !bbox || !localPage) return;
      const annotation = (localPage.items || []).find((ann) => ann?.id === annotationId);
      if (!annotation) return;
      const nextPage = upsertAnnotationInPage(localPage, updateAnnotationBBox(annotation, bbox));
      pushHistory(nextPage);
    };

    document.addEventListener('scribe:inline-change-text', handleInlineChange);
    document.addEventListener('scribe:inline-change-word', handleInlineWord);
    document.addEventListener('scribe:select-annotation', handleOverlaySelect);
    document.addEventListener('scribe:inline-step-selection', handleInlineStep);
    document.addEventListener('scribe:inline-save', handleInlineSave);
    document.addEventListener('scribe:resize-annotation', handleResizeAnnotation);
    return () => {
      document.removeEventListener('scribe:inline-change-text', handleInlineChange);
      document.removeEventListener('scribe:inline-change-word', handleInlineWord);
      document.removeEventListener('scribe:select-annotation', handleOverlaySelect);
      document.removeEventListener('scribe:inline-step-selection', handleInlineStep);
      document.removeEventListener('scribe:inline-save', handleInlineSave);
      document.removeEventListener('scribe:resize-annotation', handleResizeAnnotation);
    };
  }, [canvasId, effectiveSelectedAnnotationId, visibleRows, windowId, selectedAnnotation, localPage]);

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
    const wordWidth = isWordAnnotation(selectedAnnotation)
      ? Math.max(1, bbox.w)
      : Math.max(1, Math.round(bbox.w / 6));
    const wordBBox = {
      h: Math.max(1, bbox.h),
      w: wordWidth,
      x: isWordAnnotation(selectedAnnotation) ? bbox.x + bbox.w : bbox.x,
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

  async function handleSplit() {
    if (!adapterFactory || !localPage || !selectedLineAnnotation?.id || !canSplitLine || editingIsBlocked()) return;
    const targetCanvasId = canvasId || annotationCanvasId(selectedLineAnnotation);
    if (!targetCanvasId) return;
    setOperationBusy(true);
    setStatusMessage('Splitting line...');
    try {
      const submittedPage = localPage;
      const selectedIds = [selectedLineAnnotation.id];
      const adapter = requireAdapter(targetCanvasId);
      const nextPage = await adapter.splitLineIntoTwoLines(submittedPage, selectedLineAnnotation.id);
      const { overlap } = applyTransformResult(
        targetCanvasId,
        submittedPage,
        nextPage,
        selectedIds,
        { atomic: true },
      );
      if (activeCanvasRef.current !== targetCanvasId) return;
      setStatusMessage(overlap
        ? 'Line split, but a newer overlapping edit was preserved. Review the pending conflict.'
        : 'Line split.');
    } catch (error) {
      if (activeCanvasRef.current === targetCanvasId) {
        setStatusMessage(error instanceof Error ? error.message : 'Split failed.');
      }
    } finally {
      setOperationBusy(false);
    }
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

  async function handleJoinWords() {
    if (!adapterFactory || !localPage || !selectedAnnotation || wordJoinCandidates.length < 2 || editingIsBlocked()) return;
    const targetCanvasId = canvasId || annotationCanvasId(wordJoinCandidates[0]);
    if (!targetCanvasId) return;
    setOperationBusy(true);
    setStatusMessage('Joining words...');
    try {
      const submittedPage = localPage;
      const selectedIds = wordJoinCandidates.flatMap((annotation) => (annotation.id ? [annotation.id] : []));
      const adapter = requireAdapter(targetCanvasId);
      const nextPage = await adapter.joinWordsIntoLine(
        submittedPage,
        selectedIds,
      );
      const { overlap } = applyTransformResult(
        targetCanvasId,
        submittedPage,
        nextPage,
        selectedIds,
        { atomic: true },
      );
      if (activeCanvasRef.current !== targetCanvasId) return;
      setStatusMessage(overlap
        ? 'Words joined, but a newer overlapping edit was preserved. Review the pending conflict.'
        : 'Words joined.');
    } catch (error) {
      if (activeCanvasRef.current === targetCanvasId) {
        setStatusMessage(error instanceof Error ? error.message : 'Join words failed.');
      }
    } finally {
      setOperationBusy(false);
    }
  }

  async function handleJoinLines() {
    if (!adapterFactory || !localPage || !selectedAnnotation || lineJoinCandidates.length < 2 || editingIsBlocked()) return;
    const targetCanvasId = canvasId || annotationCanvasId(lineJoinCandidates[0]);
    if (!targetCanvasId) return;
    setOperationBusy(true);
    setStatusMessage('Joining lines...');
    try {
      const submittedPage = localPage;
      const selectedIds = lineJoinCandidates.flatMap((annotation) => (annotation.id ? [annotation.id] : []));
      const adapter = requireAdapter(targetCanvasId);
      const nextPage = await adapter.joinLinesIntoLine(
        submittedPage,
        selectedIds,
      );
      const { overlap } = applyTransformResult(
        targetCanvasId,
        submittedPage,
        nextPage,
        selectedIds,
        { atomic: true },
      );
      if (activeCanvasRef.current !== targetCanvasId) return;
      setStatusMessage(overlap
        ? 'Lines joined, but a newer overlapping edit was preserved. Review the pending conflict.'
        : 'Lines joined.');
    } catch (error) {
      if (activeCanvasRef.current === targetCanvasId) {
        setStatusMessage(error instanceof Error ? error.message : 'Join lines failed.');
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
    dispatchSession({ type: 'redo' });
  }

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
      // Clear overlays so the magic wand animation is unobstructed.
      // Respect edit mode if the user already has the inline editor open.
      if (overlayMode !== 'edit') setOverlayMode('none');

      const submittedPage = localPage;
      const targetAnnotations = /** @type {IIIFAnnotation[]} */ ((all
        ? (submittedPage.items || [])
        : (annotationIds.length > 0 ? annotationIds : transcribeSelection)
            .map((id) => (submittedPage.items || []).find((a) => a?.id === id))
            .filter(Boolean))
        .filter((annotation) => !isWordAnnotation(annotation)));

      const total = targetAnnotations.length;
      setStatusMessage(`Transcribing… 0 / ${total}`);

      let nextPage = submittedPage;
      const failures = [];
      const adapter = requireAdapter(targetCanvasId || annotationCanvasId(selectedAnnotation));

      for (let i = 0; i < targetAnnotations.length; i++) {
        const annotation = targetAnnotations[i];
        const done = i + 1;
        setStatusMessage(`Transcribing… ${done} / ${total}`);
        document.dispatchEvent(new CustomEvent('scribe:transcription-segment', {
          detail: { annotation, canvasId: targetCanvasId, done, total, windowId },
        }));

        try {
          // eslint-disable-next-line no-await-in-loop
          const transcribed = await adapter.transcribeAnnotation(annotation);
          if (!mountedRef.current || activeCanvasRef.current !== targetCanvasId) return;
          if (transcribed) {
            nextPage = upsertAnnotationInPage(nextPage, transcribed);
          }
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

        if (!mountedRef.current || activeCanvasRef.current !== targetCanvasId) {
          return;
        }

      }

      if (activeCanvasRef.current !== targetCanvasId) return;
      const selectedIds = targetAnnotations.flatMap((annotation) => (annotation.id ? [annotation.id] : []));
      const { overlap } = applyTransformResult(targetCanvasId, submittedPage, nextPage, selectedIds);
      setTranscribeDialogOpen(false);
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

  return (
    <ScribeActionPanel
      annotations={visibleAnnotations}
      canJoinLines={canJoinLines}
      canJoinWords={canJoinWords}
      canSplitLine={canSplitLine}
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
      onJoinLines={handleJoinLines}
      onJoinWords={handleJoinWords}
      onRedo={handleRedo}
      onPublish={handlePublish}
      onReload={() => reloadAnnotations()}
      onSave={handleSave}
      onSplit={handleSplit}
      onTranscribe={handleTranscribe}
      onTranscribeDialogClose={() => setTranscribeDialogOpen(false)}
      onTranscribeDialogOpen={() => setTranscribeDialogOpen(true)}
      onTranscribeSelectionChange={setTranscribeSelection}
      onUndo={handleUndo}
      pendingRemoteIds={session.pendingRemoteIds}
      saveDisabled={saveDisabled}
      revisionConflict={revisionConflict}
      selectedAnnotation={selectedAnnotation}
      selectedGranularity={selectedGranularity}
      statusMessage={statusMessage}
      transcribeDialogOpen={transcribeDialogOpen}
      transcribeSelection={transcribeSelection}
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
