import { useEffect, useMemo, useRef, useState } from 'react';
import PropTypes from 'prop-types';
import {
  receiveAnnotation as receiveAnnotationAction,
  removeCompanionWindow as removeCompanionWindowAction,
  selectAnnotation as selectAnnotationAction,
} from 'mirador';
import ScribeActionPanel from '../components/ScribeActionPanel';
import {
  annotationBBox,
  annotationCanvasId,
  annotationIntersectsImageRect,
  annotationPageForCanvas,
  canvasIdForWindow,
  createDraftLineAnnotation,
  findEditorRowByAnnotationId,
  findAnnotationPageByAnnotationId,
  findCanvasIdByAnnotationId,
  firstAnnotationCanvasId,
  firstAnnotationPage,
  groupAnnotationsForEditor,
  isWordAnnotation,
  joinLineCandidates,
  joinWordCandidates,
  lineAnnotationForSelection,
  removeAnnotationsFromPage,
  replaceAnnotationInPage,
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

function ScribeCompanionWindow({
  adapterFactory,
  canvasId,
  id,
  receiveAnnotation,
  removeCompanionWindow,
  selectAnnotation,
  selectedAnnotationId,
  serverPage,
  windowId,
}) {
  function cycleOverlayMode(current) {
    if (current === 'edit') return 'read';
    if (current === 'read') return 'none';
    return 'edit';
  }

  const [isBusy, setIsBusy] = useState(false);
  const [statusMessage, setStatusMessage] = useState('');
  const [localPage, setLocalPage] = useState(serverPage);
  const [undoStack, setUndoStack] = useState([]);
  const [redoStack, setRedoStack] = useState([]);
  const [viewportBounds, setViewportBounds] = useState(null);
  const [transcribeDialogOpen, setTranscribeDialogOpen] = useState(false);
  const [transcribeSelection, setTranscribeSelection] = useState([]);
  const [drawMode, setDrawMode] = useState(false);
  const [overlayMode, setOverlayMode] = useState('edit');
  const [focusedWordAnnotationId, setFocusedWordAnnotationId] = useState('');
  const didInitialSnapRef = useRef(false);
  const inlineEditorVisible = overlayMode === 'edit';
  const textOverlayVisible = overlayMode === 'read';

  function toggleDrawMode() {
    setDrawMode((current) => {
      const next = !current;
      if (next) {
        setOverlayMode('none');
      }
      return next;
    });
  }

  function cycleOverlayModeFromToolbar() {
    setDrawMode(false);
    setOverlayMode(cycleOverlayMode);
  }

  useEffect(() => {
    setLocalPage(serverPage);
    setUndoStack([]);
    setRedoStack([]);
    setStatusMessage('');
    setFocusedWordAnnotationId('');
    didInitialSnapRef.current = false;
  }, [serverPage]);

  useEffect(() => {
    let cancelled = false;

    async function bootstrapPage() {
      if (!adapterFactory || !canvasId) return;
      const hasServerItems = Array.isArray(serverPage?.items) && serverPage.items.length > 0;
      const hasLocalItems = Array.isArray(localPage?.items) && localPage.items.length > 0;
      if (hasServerItems || hasLocalItems) return;

      try {
        const adapter = adapterFactory(canvasId);
        const page = await adapter.all();
        if (cancelled) return;
        if (Array.isArray(page?.items) && page.items.length > 0) {
          setLocalPage(page);
          receiveAnnotation(canvasId, page.id, page);
        }
      } catch (error) {
        if (!cancelled) {
          setStatusMessage(error instanceof Error ? error.message : 'Failed to load annotations.');
        }
      }
    }

    void bootstrapPage();
    return () => {
      cancelled = true;
    };
  }, [adapterFactory, canvasId, localPage, receiveAnnotation, serverPage]);

  const annotations = useMemo(() => sortedAnnotations(localPage), [localPage]);
  const visibleAnnotations = useMemo(() => {
    if (!viewportBounds) return annotations;
    return annotations.filter((annotation) => annotationIntersectsImageRect(annotation, viewportBounds));
  }, [annotations, viewportBounds]);
  const visibleRows = useMemo(() => groupAnnotationsForEditor({ items: visibleAnnotations }), [visibleAnnotations]);
  const selectedAnnotation = useMemo(
    () => visibleAnnotations.find((annotation) => annotation?.id === (selectedAnnotationId || visibleAnnotations[0]?.id)) || visibleAnnotations[0] || null,
    [visibleAnnotations, selectedAnnotationId],
  );
  const effectiveSelectedAnnotationId = selectedAnnotation?.id || '';
  const saveDisabled = JSON.stringify(serverPage?.items || []) === JSON.stringify(localPage?.items || []);
  const wordJoinCandidates = useMemo(
    () => joinWordCandidates(selectedAnnotation, visibleAnnotations),
    [selectedAnnotation, visibleAnnotations],
  );
  const lineJoinCandidates = useMemo(
    () => joinLineCandidates(selectedAnnotation, visibleAnnotations),
    [selectedAnnotation, visibleAnnotations],
  );
  const canJoinWords = wordJoinCandidates.length > 1;
  const canJoinLines = lineJoinCandidates.length > 1;

  useEffect(() => {
    document.dispatchEvent(new CustomEvent('scribe:dirty-state', {
      detail: {
        dirty: !saveDisabled,
        windowId,
      },
    }));
  }, [saveDisabled, windowId]);

  useEffect(() => {
    if (!selectedAnnotationId && visibleAnnotations[0]?.id) {
      selectAnnotation(windowId, visibleAnnotations[0].id);
    }
  }, [selectedAnnotationId, selectAnnotation, visibleAnnotations, windowId]);

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
    const handleViewport = (event) => {
      if (event?.detail?.windowId !== windowId) return;
      setViewportBounds(event.detail.bounds || null);
    };
    document.addEventListener('scribe:viewport-change', handleViewport);
    return () => document.removeEventListener('scribe:viewport-change', handleViewport);
  }, [windowId]);

  useEffect(() => {
    const handleKeyDown = (event) => {
      if (event.defaultPrevented || event.metaKey || event.ctrlKey || event.altKey) return;
      const target = event.target;
      const isEditableTarget = target instanceof HTMLElement
        && (target.tagName === 'INPUT' || target.tagName === 'TEXTAREA' || target.isContentEditable);

      if (event.key === 'Escape') {
        event.preventDefault();
        setDrawMode(false);
        setOverlayMode('none');
        return;
      }

      if (isEditableTarget) return;

      if (event.key.toLowerCase() === 'e') {
        event.preventDefault();
        setDrawMode(false);
        setOverlayMode('edit');
      }
    };

    window.addEventListener('keydown', handleKeyDown);
    return () => window.removeEventListener('keydown', handleKeyDown);
  }, []);

  useEffect(() => {
    document.dispatchEvent(new CustomEvent('scribe:set-draw-mode', {
      detail: {
        active: drawMode,
        windowId,
      },
    }));
  }, [drawMode, windowId]);

  useEffect(() => {
    document.dispatchEvent(new CustomEvent('scribe:editor-state', {
      detail: {
        annotationPage: localPage || serverPage || null,
        canJoinLines,
        canJoinWords,
        drawMode,
        focusedWordAnnotationId,
        inlineEditorVisible,
        isBusy,
        overlayMode,
        saveDisabled,
        selectedAnnotationId: effectiveSelectedAnnotationId,
        selectedGranularity: selectedAnnotation ? (isWordAnnotation(selectedAnnotation) ? 'word' : 'line') : null,
        statusMessage,
        textOverlayVisible,
        windowId,
      },
    }));
  }, [
    canJoinLines,
    canJoinWords,
    drawMode,
    effectiveSelectedAnnotationId,
    focusedWordAnnotationId,
    inlineEditorVisible,
    isBusy,
    localPage,
    overlayMode,
    saveDisabled,
    selectedAnnotation,
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
        windowId,
      },
    }));
  }, [localPage, selectedAnnotation, windowId]);

  useEffect(() => {
    if (didInitialSnapRef.current) return;
    const anchor = annotations[0];
    if (!anchor) return;
    document.dispatchEvent(new CustomEvent('scribe:snap-to-bbox', {
      detail: {
        bbox: annotationBBox(anchor),
        windowId,
      },
    }));
    didInitialSnapRef.current = true;
  }, [annotations, windowId]);

  useEffect(() => {
    const handleCreateAnnotation = async (event) => {
      if (event?.detail?.windowId !== windowId) return;
      if (!adapterFactory || !canvasId || !event.detail?.bbox) return;

      setIsBusy(true);
      setStatusMessage('Creating line...');
      try {
        const adapter = adapterFactory(canvasId);
        const created = await adapter.createOne(createDraftLineAnnotation(canvasId, event.detail.bbox));
        const nextPage = upsertAnnotationInPage(localPage || serverPage || { type: 'AnnotationPage', items: [] }, created);
        pushHistory(nextPage);
        setDrawMode(false);
        if (created?.id) selectAnnotation(windowId, created.id);
        setStatusMessage('Line created.');
      } catch (error) {
        setStatusMessage(error instanceof Error ? error.message : 'Create line failed.');
      } finally {
        setIsBusy(false);
      }
    };

    document.addEventListener('scribe:create-annotation', handleCreateAnnotation);
    return () => document.removeEventListener('scribe:create-annotation', handleCreateAnnotation);
  }, [adapterFactory, canvasId, localPage, selectAnnotation, serverPage, windowId]);

  function pushHistory(nextPage) {
    if (!nextPage) return;
    if (localPage) {
      setUndoStack((current) => [...current, structuredClone(localPage)]);
    }
    setRedoStack([]);
    setLocalPage(nextPage);
  }

  async function syncPage(page, fallbackCanvasId) {
    const targetCanvasId = fallbackCanvasId || canvasId || annotationCanvasId(selectedAnnotation);
    if (!targetCanvasId || !page) return;
    receiveAnnotation(targetCanvasId, page.id, page);
  }

  function handleInlineTextChange(text, selectionStart) {
    setLocalPage((currentPage) => {
      if (!currentPage) return currentPage;
      const targetRow = findEditorRowByAnnotationId(currentPage, effectiveSelectedAnnotationId)
        || findEditorRowByAnnotationId(currentPage, selectedAnnotation?.id);
      if (!targetRow) return currentPage;

      if (targetRow.granularity === 'word') {
        const activeWordId = wordAnnotationIdForCaret(targetRow, text, selectionStart);
        setFocusedWordAnnotationId(activeWordId || targetRow.fields[0]?.id || '');
        const lineId = rowSelectionId(targetRow);
        if (lineId) selectAnnotation(windowId, lineId);
        return updateRowText(currentPage, targetRow, text);
      }

      setFocusedWordAnnotationId('');
      const targetId = rowSelectionId(targetRow);
      const targetAnnotation = (currentPage.items || []).find((annotation) => annotation?.id === targetId);
      if (!targetAnnotation) return currentPage;
      return upsertAnnotationInPage(currentPage, updateAnnotationText(targetAnnotation, text));
    });
    setStatusMessage('');
  }

  function handleInlineWordChange(annotationId, text) {
    const currentAnnotation = (localPage?.items || []).find((annotation) => annotation?.id === annotationId);
    const lineSelection = currentAnnotation ? (lineAnnotationForSelection(localPage, currentAnnotation) || currentAnnotation) : null;
    setLocalPage((currentPage) => {
      if (!currentPage) return currentPage;
      const wordAnnotation = (currentPage.items || []).find((annotation) => annotation?.id === annotationId);
      if (!wordAnnotation) return currentPage;
      const nextPage = upsertAnnotationInPage(currentPage, updateAnnotationText(wordAnnotation, text));
      const syncedPage = synchronizeLineTextFromWords(
        nextPage,
        nextPage.items.find((annotation) => annotation?.id === annotationId),
      );
      return syncedPage;
    });
    setFocusedWordAnnotationId(annotationId);
    if (lineSelection?.id) selectAnnotation(windowId, lineSelection.id);
    setStatusMessage('');
  }

  async function performSave() {
    if (!localPage) return;
    setIsBusy(true);
    setStatusMessage('Saving page changes...');
    try {
      const adapter = adapterFactory(canvasId || annotationCanvasId(selectedAnnotation));
      const baselineById = new Map((serverPage?.items || []).map((annotation) => [annotation.id, annotation]));
      const localById = new Map((localPage?.items || []).map((annotation) => [annotation.id, annotation]));

      for (const [annotationId] of baselineById) {
        if (!localById.has(annotationId)) await adapter.deleteOne(annotationId);
      }

      for (const [annotationId, annotation] of localById) {
        const baseline = baselineById.get(annotationId);
        if (!baseline) {
          await adapter.createOne(annotation);
        } else if (JSON.stringify(baseline) !== JSON.stringify(annotation)) {
          await adapter.updateOne(annotation);
        }
      }

      const page = await adapter.all();
      await syncPage(page, canvasId || annotationCanvasId(selectedAnnotation));
      setLocalPage(page);
      setStatusMessage('Saved page.');
      return true;
    } catch (error) {
      setStatusMessage(error instanceof Error ? error.message : 'Save failed.');
      return false;
    } finally {
      setIsBusy(false);
    }
  }

  async function handleSave() {
    await performSave();
  }

  useEffect(() => {
    const handleSaveRequest = async (event) => {
      if (event?.detail?.windowId && event.detail.windowId !== windowId) return;
      const requestId = event.detail.requestId;
      const ok = await performSave();
      document.dispatchEvent(new CustomEvent('scribe:save-result', {
        detail: {
          ok,
          requestId,
          windowId,
        },
      }));
    };

    document.addEventListener('scribe:request-save', handleSaveRequest);
    return () => document.removeEventListener('scribe:request-save', handleSaveRequest);
  }, [windowId, adapterFactory, canvasId, localPage, serverPage, selectedAnnotation]);

  useEffect(() => {
    const handleInlineChange = (event) => {
      if (event?.detail?.windowId !== windowId) return;
      handleInlineTextChange(event.detail.text || '', event.detail.selectionStart);
    };
    const handleInlineStep = (event) => {
      if (event?.detail?.windowId !== windowId) return;
      if (visibleRows.length === 0) return;
      const currentRowId = lineAnnotationForSelection(localPage, selectedAnnotation)?.id || effectiveSelectedAnnotationId;
      const currentIndex = visibleRows.findIndex((row) => (
        row.lead?.id === currentRowId
          || row.fields.some((annotation) => annotation.id === currentRowId)
      ));
      const direction = event.detail.direction === -1 ? -1 : 1;
      const nextIndex = ((currentIndex >= 0 ? currentIndex : 0) + direction + visibleRows.length) % visibleRows.length;
      const nextRow = visibleRows[nextIndex];
      const nextSelection = rowSelectionId(nextRow);
      setFocusedWordAnnotationId(nextRow?.granularity === 'word' ? (nextRow.fields[0]?.id || '') : '');
      if (nextSelection) selectAnnotation(windowId, nextSelection);
    };
    const handleInlineToggle = (event) => {
      if (event?.detail?.windowId !== windowId) return;
      setDrawMode(false);
      setOverlayMode((current) => (current === 'edit' ? 'none' : 'edit'));
    };
    const handleInlineSave = (event) => {
      if (event?.detail?.windowId !== windowId) return;
      void handleSave();
    };
    const handleOverlaySelect = (event) => {
      if (event?.detail?.windowId !== windowId) return;
      if (!event.detail.annotationId) return;
      const sourcePage = localPage || serverPage;
      const clickedAnnotation = (sourcePage?.items || []).find((annotation) => annotation?.id === event.detail.annotationId) || null;
      const lineSelection = clickedAnnotation ? (lineAnnotationForSelection(sourcePage, clickedAnnotation) || clickedAnnotation) : null;
      setDrawMode(false);
      setOverlayMode('edit');
      setFocusedWordAnnotationId(event.detail.focusAnnotationId || (isWordAnnotation(clickedAnnotation) ? clickedAnnotation.id : ''));
      selectAnnotation(windowId, lineSelection?.id || event.detail.annotationId);
    };
    const handleInlineWord = (event) => {
      if (event?.detail?.windowId !== windowId) return;
      if (!event.detail.annotationId) return;
      handleInlineWordChange(event.detail.annotationId, event.detail.text || '');
    };
    const handleAction = (event) => {
      if (event?.detail?.windowId !== windowId) return;
      switch (event.detail.action) {
        case 'toggle-inline-editor':
          setDrawMode(false);
          setOverlayMode((current) => (current === 'edit' ? 'none' : 'edit'));
          break;
        case 'toggle-text-overlay':
          setDrawMode(false);
          setOverlayMode((current) => (current === 'read' ? 'none' : 'read'));
          break;
        case 'cycle-overlay-mode':
          setDrawMode(false);
          setOverlayMode(cycleOverlayMode);
          break;
        case 'toggle-draw':
          toggleDrawMode();
          break;
        case 'split-words':
          void handleExplode();
          break;
        case 'join-words':
          void handleJoinWords();
          break;
        case 'split-line':
          void handleSplit();
          break;
        case 'join-lines':
          void handleJoinLines();
          break;
        case 'transcribe':
          setTranscribeDialogOpen(true);
          break;
        case 'undo':
          handleUndo();
          break;
        case 'redo':
          handleRedo();
          break;
        case 'delete':
          if (selectedAnnotation?.id) handleDelete(selectedAnnotation.id);
          break;
        case 'save':
          void handleSave();
          break;
        default:
          break;
      }
    };

    const handleResizeAnnotation = (event) => {
      if (event?.detail?.windowId !== windowId) return;
      const { annotationId, bbox } = event.detail;
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
    document.addEventListener('scribe:inline-toggle-editor', handleInlineToggle);
    document.addEventListener('scribe:inline-save', handleInlineSave);
    document.addEventListener('scribe:editor-action', handleAction);
    document.addEventListener('scribe:resize-annotation', handleResizeAnnotation);
    return () => {
      document.removeEventListener('scribe:inline-change-text', handleInlineChange);
      document.removeEventListener('scribe:inline-change-word', handleInlineWord);
      document.removeEventListener('scribe:select-annotation', handleOverlaySelect);
      document.removeEventListener('scribe:inline-step-selection', handleInlineStep);
      document.removeEventListener('scribe:inline-toggle-editor', handleInlineToggle);
      document.removeEventListener('scribe:inline-save', handleInlineSave);
      document.removeEventListener('scribe:editor-action', handleAction);
      document.removeEventListener('scribe:resize-annotation', handleResizeAnnotation);
    };
  }, [effectiveSelectedAnnotationId, visibleRows, windowId, selectedAnnotation, localPage]);

  function handleDelete(annotationId) {
    if (!localPage) return;
    const nextPage = removeAnnotationsFromPage(localPage, [annotationId]);
    pushHistory(nextPage);
    const nextSelection = sortedAnnotations(nextPage)[0]?.id;
    if (nextSelection) selectAnnotation(windowId, nextSelection);
    else removeCompanionWindow();
  }

  async function handleSplit() {
    if (!adapterFactory || !localPage || !selectedAnnotation) return;
    setIsBusy(true);
    setStatusMessage('Splitting line...');
    try {
      const adapter = adapterFactory(canvasId || annotationCanvasId(selectedAnnotation));
      const replacements = await adapter.splitLineIntoTwoLines(selectedAnnotation);
      const nextPage = replaceAnnotationInPage(localPage, selectedAnnotation.id, replacements);
      pushHistory(nextPage);
      if (replacements[0]?.id) selectAnnotation(windowId, replacements[0].id);
      setStatusMessage('Line split.');
    } catch (error) {
      setStatusMessage(error instanceof Error ? error.message : 'Split failed.');
    } finally {
      setIsBusy(false);
    }
  }

  async function handleExplode() {
    if (!adapterFactory || !localPage || !selectedAnnotation) return;
    setIsBusy(true);
    setStatusMessage('Exploding line into words...');
    try {
      const adapter = adapterFactory(canvasId || annotationCanvasId(selectedAnnotation));
      const splitPage = await adapter.splitLineIntoWords(selectedAnnotation);
      const nextPage = replaceAnnotationInPage(localPage, selectedAnnotation.id, splitPage.items || []);
      pushHistory(nextPage);
      const nextSelection = sortedAnnotations(nextPage)[0]?.id;
      if (nextSelection) selectAnnotation(windowId, nextSelection);
      setStatusMessage('Words created.');
    } catch (error) {
      setStatusMessage(error instanceof Error ? error.message : 'Explode failed.');
    } finally {
      setIsBusy(false);
    }
  }

  async function handleJoinWords() {
    if (!adapterFactory || !localPage || !selectedAnnotation || wordJoinCandidates.length < 2) return;
    setIsBusy(true);
    setStatusMessage('Joining words...');
    try {
      const adapter = adapterFactory(canvasId || annotationCanvasId(wordJoinCandidates[0]));
      const merged = await adapter.joinWordsIntoLine(wordJoinCandidates);
      const nextPage = upsertAnnotationInPage(
        removeAnnotationsFromPage(localPage, wordJoinCandidates.map((annotation) => annotation.id)),
        merged,
      );
      pushHistory(nextPage);
      if (merged?.id) selectAnnotation(windowId, merged.id);
      setStatusMessage('Words joined.');
    } catch (error) {
      setStatusMessage(error instanceof Error ? error.message : 'Join words failed.');
    } finally {
      setIsBusy(false);
    }
  }

  async function handleJoinLines() {
    if (!adapterFactory || !localPage || !selectedAnnotation || lineJoinCandidates.length < 2) return;
    setIsBusy(true);
    setStatusMessage('Joining lines...');
    try {
      const adapter = adapterFactory(canvasId || annotationCanvasId(lineJoinCandidates[0]));
      const merged = await adapter.joinLinesIntoLine(lineJoinCandidates);
      const nextPage = upsertAnnotationInPage(
        removeAnnotationsFromPage(localPage, lineJoinCandidates.map((annotation) => annotation.id)),
        merged,
      );
      pushHistory(nextPage);
      if (merged?.id) selectAnnotation(windowId, merged.id);
      setStatusMessage('Lines joined.');
    } catch (error) {
      setStatusMessage(error instanceof Error ? error.message : 'Join lines failed.');
    } finally {
      setIsBusy(false);
    }
  }

  function handleUndo() {
    if (undoStack.length === 0 || !localPage) return;
    const previous = undoStack[undoStack.length - 1];
    setRedoStack((current) => [...current, structuredClone(localPage)]);
    setUndoStack((current) => current.slice(0, -1));
    setLocalPage(previous);
  }

  function handleRedo() {
    if (redoStack.length === 0 || !localPage) return;
    const next = redoStack[redoStack.length - 1];
    setUndoStack((current) => [...current, structuredClone(localPage)]);
    setRedoStack((current) => current.slice(0, -1));
    setLocalPage(next);
  }

  async function handleTranscribe({ all = false, annotationIds = [] } = {}) {
    if (!adapterFactory || !localPage) return;
    setIsBusy(true);
    setStatusMessage(all ? 'Transcribing document...' : 'Transcribing selected text...');
    try {
      const adapter = adapterFactory(canvasId || annotationCanvasId(selectedAnnotation));
      let nextPage = localPage;

      if (all) {
        nextPage = await adapter.transcribeAnnotationPage(localPage);
      } else {
        const targetIds = annotationIds.length > 0 ? annotationIds : transcribeSelection;
        const replacements = await Promise.all(
          targetIds.map(async (annotationId) => {
            const source = (localPage.items || []).find((annotation) => annotation?.id === annotationId);
            if (!source) return null;
            return adapter.transcribeAnnotation(source);
          }),
        );

        nextPage = replacements.filter(Boolean).reduce(
          (page, annotation) => upsertAnnotationInPage(page, annotation),
          localPage,
        );
      }

      pushHistory(nextPage);
      const focusId = all ? nextPage?.items?.[0]?.id : (annotationIds[0] || transcribeSelection[0] || nextPage?.items?.[0]?.id);
      if (focusId) selectAnnotation(windowId, focusId);
      setTranscribeDialogOpen(false);
      setStatusMessage(all ? 'Document transcribed.' : 'Selected text transcribed.');
    } catch (error) {
      setStatusMessage(error instanceof Error ? error.message : 'Transcription failed.');
    } finally {
      setIsBusy(false);
    }
  }

  return (
    <ScribeActionPanel
      annotations={visibleAnnotations}
      canJoinLines={canJoinLines}
      canJoinWords={canJoinWords}
      drawMode={drawMode}
      id={id}
      isBusy={isBusy}
      overlayMode={overlayMode}
      onDelete={handleDelete}
      onCreateLine={toggleDrawMode}
      onCycleOverlayMode={cycleOverlayModeFromToolbar}
      onExplode={handleExplode}
      onJoinLines={handleJoinLines}
      onJoinWords={handleJoinWords}
      onRedo={handleRedo}
      onSave={handleSave}
      onSplit={handleSplit}
      onTranscribe={handleTranscribe}
      onTranscribeDialogClose={() => setTranscribeDialogOpen(false)}
      onTranscribeDialogOpen={() => setTranscribeDialogOpen(true)}
      onTranscribeSelectionChange={setTranscribeSelection}
      onUndo={handleUndo}
      saveDisabled={saveDisabled}
      selectedAnnotation={selectedAnnotation}
      selectedGranularity={selectedAnnotation ? (isWordAnnotation(selectedAnnotation) ? 'word' : 'line') : null}
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
  receiveAnnotation: PropTypes.func.isRequired,
  removeCompanionWindow: PropTypes.func.isRequired,
  selectAnnotation: PropTypes.func.isRequired,
  selectedAnnotationId: PropTypes.string,
  serverPage: PropTypes.shape({
    id: PropTypes.string,
    items: PropTypes.array,
  }),
  windowId: PropTypes.string.isRequired,
};

function mapStateToProps(state, { windowId }) {
  const selectedAnnotationId = selectedAnnotationIdForWindow(state, windowId);
  const pageForSelection = findAnnotationPageByAnnotationId(state, selectedAnnotationId);
  const resolvedCanvasId = findCanvasIdByAnnotationId(state, selectedAnnotationId)
    || canvasIdForWindow(state, windowId)
    || firstAnnotationCanvasId(state);

  return {
    adapterFactory: state?.config?.annotation?.adapter,
    canvasId: resolvedCanvasId,
    selectedAnnotationId,
    serverPage: pageForSelection || annotationPageForCanvas(state, resolvedCanvasId) || firstAnnotationPage(state),
  };
}

const mapDispatchToProps = (dispatch, { id, windowId }) => ({
  receiveAnnotation: (targetId, annotationId, annotationJson) => dispatch(
    receiveAnnotationAction(targetId, annotationId, annotationJson),
  ),
  removeCompanionWindow: () => dispatch(removeCompanionWindowAction(windowId, id)),
  selectAnnotation: (targetWindowId, annotationId) => dispatch(selectAnnotationAction(targetWindowId, annotationId)),
});

const scribeCompanionWindowPlugin = {
  companionWindowKey: 'scribeEditor',
  component: ScribeCompanionWindow,
  mapDispatchToProps,
  mapStateToProps,
};

export default scribeCompanionWindowPlugin;
