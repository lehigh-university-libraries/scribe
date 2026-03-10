import { useEffect, useMemo, useState } from 'react';
import PropTypes from 'prop-types';
import {
  receiveAnnotation as receiveAnnotationAction,
  removeCompanionWindow as removeCompanionWindowAction,
  selectAnnotation as selectAnnotationAction,
} from 'mirador';
import ScribeEditorPanel from '../components/ScribeEditorPanel';
import {
  annotationBBox,
  annotationCanvasId,
  annotationIntersectsImageRect,
  annotationPageForCanvas,
  canvasIdForWindow,
  createDraftLineAnnotation,
  findAnnotationPageByAnnotationId,
  findCanvasIdByAnnotationId,
  firstAnnotationCanvasId,
  firstAnnotationPage,
  isLineAnnotation,
  isWordAnnotation,
  joinLineCandidates,
  joinWordCandidates,
  removeAnnotationsFromPage,
  replaceAnnotationInPage,
  sortedAnnotations,
  selectedAnnotationIdForWindow,
  synchronizeLineTextFromWords,
  upsertAnnotationInPage,
  updateAnnotationText,
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
  const [isBusy, setIsBusy] = useState(false);
  const [statusMessage, setStatusMessage] = useState('');
  const [localPage, setLocalPage] = useState(serverPage);
  const [undoStack, setUndoStack] = useState([]);
  const [redoStack, setRedoStack] = useState([]);
  const [viewportBounds, setViewportBounds] = useState(null);
  const [transcribeDialogOpen, setTranscribeDialogOpen] = useState(false);
  const [transcribeSelection, setTranscribeSelection] = useState([]);
  const [drawMode, setDrawMode] = useState(false);

  useEffect(() => {
    setLocalPage(serverPage);
    setUndoStack([]);
    setRedoStack([]);
    setStatusMessage('');
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
    document.dispatchEvent(new CustomEvent('scribe:set-draw-mode', {
      detail: {
        active: drawMode,
        windowId,
      },
    }));
  }, [drawMode, windowId]);

  useEffect(() => {
    if (!selectedAnnotation) return;
    document.dispatchEvent(new CustomEvent('scribe:focus-annotation', {
      detail: {
        bbox: annotationBBox(selectedAnnotation),
        windowId,
      },
    }));
  }, [selectedAnnotation, windowId]);

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

  function handleChangeText(annotationId, text) {
    setLocalPage((currentPage) => {
      const items = Array.isArray(currentPage?.items) ? currentPage.items : [];
      const currentAnnotation = items.find((annotation) => annotation?.id === annotationId);
      if (!currentPage || !currentAnnotation) return currentPage;
      const nextPage = upsertAnnotationInPage(currentPage, updateAnnotationText(currentAnnotation, text));
      return synchronizeLineTextFromWords(
        nextPage,
        nextPage.items.find((annotation) => annotation?.id === annotationId),
      );
    });
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
      const response = await adapter.splitLineIntoTwoLines(selectedAnnotation);
      const replacements = (response.annotation_jsons || []).map((value) => JSON.parse(value));
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
      const response = await adapter.splitLineIntoWords(selectedAnnotation);
      const splitPage = JSON.parse(response.annotation_page_json || '{}');
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
      const response = await adapter.joinWordsIntoLine(wordJoinCandidates);
      const merged = JSON.parse(response.annotation_json || '{}');
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
      const response = await adapter.joinLinesIntoLine(lineJoinCandidates);
      const merged = JSON.parse(response.annotation_json || '{}');
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
        const response = await adapter.transcribeAnnotationPage(localPage);
        nextPage = JSON.parse(response.annotation_page_json || '{}');
      } else {
        const targetIds = annotationIds.length > 0 ? annotationIds : transcribeSelection;
        const replacements = await Promise.all(
          targetIds.map(async (annotationId) => {
            const source = (localPage.items || []).find((annotation) => annotation?.id === annotationId);
            if (!source) return null;
            const response = await adapter.transcribeAnnotation(source);
            return JSON.parse(response.annotation_json || '{}');
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
    <ScribeEditorPanel
      annotations={visibleAnnotations}
      canvasId={canvasId}
      canJoinLines={canJoinLines}
      canJoinWords={canJoinWords}
      drawMode={drawMode}
      id={id}
      isBusy={isBusy}
      onDelete={handleDelete}
      onChangeText={handleChangeText}
      onCreateLine={() => setDrawMode((current) => !current)}
      onExplode={handleExplode}
      onJoinLines={handleJoinLines}
      onJoinWords={handleJoinWords}
      onRedo={handleRedo}
      onSave={handleSave}
      onSelect={(annotationId) => selectAnnotation(windowId, annotationId)}
      onSplit={handleSplit}
      onTranscribe={handleTranscribe}
      onTranscribeDialogClose={() => setTranscribeDialogOpen(false)}
      onTranscribeDialogOpen={() => setTranscribeDialogOpen(true)}
      onTranscribeSelectionChange={setTranscribeSelection}
      onUndo={handleUndo}
      saveDisabled={saveDisabled}
      selectedGranularity={selectedAnnotation ? (isWordAnnotation(selectedAnnotation) ? 'word' : 'line') : null}
      selectedAnnotationId={effectiveSelectedAnnotationId}
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
