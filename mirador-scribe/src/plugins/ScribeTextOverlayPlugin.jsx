import { useEffect, useMemo, useRef, useState } from 'react';
import ReactDOM from 'react-dom';
import PropTypes from 'prop-types';
import OpenSeadragon from 'openseadragon';
import { clientPointToImage, normalizeImageBBox } from '../editor/geometry';
import {
  annotationBBox,
  annotationIntersectsImageRect,
  annotationText,
  annotationsShareLine,
  annotationPageForCanvas,
  canvasIdForWindow,
  findEditorRowByAnnotationId,
  groupAnnotationsForEditor,
  isLineAnnotation,
  isWordAnnotation,
  lineAnnotationForSelection,
  rowText,
  selectedAnnotationIdForWindow,
} from '../utils/iiif';

// Inject keyframe animations once at module load
if (typeof document !== 'undefined' && !document.getElementById('scribe-transcription-kf')) {
  const kfStyle = document.createElement('style');
  kfStyle.id = 'scribe-transcription-kf';
  kfStyle.textContent = [
    '@keyframes scribeSegmentPulse{0%,100%{opacity:1;box-shadow:0 0 0 0 rgba(139,92,246,.35)}50%{opacity:.75;box-shadow:0 0 0 5px rgba(139,92,246,0)}}',
    '@keyframes scribeResultDissolve{0%{opacity:0;transform:scaleY(.92)}12%{opacity:1;transform:scaleY(1)}72%{opacity:1}100%{opacity:0}}',
    '@keyframes scribeSpinner{to{transform:rotate(360deg)}}',
    '@media (prefers-reduced-motion: reduce){.scribe-text-overlay *{animation:none!important;transition:none!important;scroll-behavior:auto!important}}',
  ].join('');
  document.head.appendChild(kfStyle);
}

const INLINE_EDITOR_GAP_PX = 0;
const INLINE_EDITOR_HEIGHT_PX = 72;
const INLINE_EDITOR_MIN_WIDTH_PX = 280;
const INLINE_WORD_GAP_PX = 6;
const ACTION_BAR_HEIGHT_PX = 104;
const INLINE_EDITOR_HANDLE_PX = 5;
const INLINE_EDITOR_CONTENT_INSET_PX = 10;

/** @typedef {import('../types/scribe').ImageBBox} Rect */
/** @typedef {import('../types/scribe').IIIFAnnotation} IIIFAnnotation */
/** @typedef {import('../types/scribe').IIIFAnnotationPage} IIIFAnnotationPage */
/** @typedef {import('../types/scribe').MiradorState} MiradorState */
/** @typedef {import('../types/scribe').ScribeFocusResizeHandleEventDetail} FocusResizeHandleEventDetail */
/** @typedef {{ annotationPage?: IIIFAnnotationPage, canvasId?: string, selectedAnnotationId?: string, focusedWordAnnotationId?: string, isBusy?: boolean, overlayMode?: 'none' | 'read' | 'edit' | 'outline', windowId?: string }} OverlayEditorState */
/** @typedef {{ currentTop: number, lineTop: number, pointerY: number, startY: number }} EditorDragState */
/** @typedef {{ annotationId: string, currentClientX: number, currentClientY: number, handle: string, originalBBox: Rect, startClientX: number, startClientY: number }} BBoxDragState */
/** @typedef {{ annotation: IIIFAnnotation, done: number, total: number }} TranscriptionSegment */
/** @typedef {{ annotation: IIIFAnnotation, done: number, total: number, text: string | null }} TranscriptionResult */
/** @typedef {{ annotation?: IIIFAnnotation | null, canvasId?: string, done?: number, total?: number, windowId?: string }} TranscriptionEventDetail */
/** @typedef {{ id: string, isWord: boolean, rect: Rect, text: string }} OverlayLabel */
/** @typedef {{ annotationId: string | null, fallbackIndex?: number, rect: Rect, text: string }} WordEditor */
/** @typedef {Object} ScribeTextOverlayProps
 * @property {IIIFAnnotationPage | null} annotationPage
 * @property {string} canvasId
 * @property {string} selectedAnnotationId
 * @property {OpenSeadragon.Viewer | null | undefined} viewer
 * @property {string} windowId
 */
/** @typedef {{ windowId: string }} WindowOwnProps */

/** @param {OpenSeadragon.Viewer | null | undefined} viewer @param {IIIFAnnotation} annotation @returns {Rect | null} */
function annotationRect(viewer, annotation) {
  if (!viewer?.viewport || !viewer?.world?.getItemCount?.()) return null;
  const tiledImage = viewer.world.getItemAt(0);
  if (!tiledImage?.imageToViewportCoordinates) return null;
  const contentSize = tiledImage.getContentSize?.();
  const { x, y, w, h } = annotationBBox(annotation, contentSize ? {
    height: contentSize.y,
    width: contentSize.x,
  } : null);
  if (w <= 0 || h <= 0) return null;

  const topLeftViewport = tiledImage.imageToViewportCoordinates(x, y);
  const bottomRightViewport = tiledImage.imageToViewportCoordinates(x + w, y + h);
  const topLeft = viewer.viewport.pixelFromPoint(
    new OpenSeadragon.Point(topLeftViewport.x, topLeftViewport.y),
    true,
  );
  const bottomRight = viewer.viewport.pixelFromPoint(
    new OpenSeadragon.Point(bottomRightViewport.x, bottomRightViewport.y),
    true,
  );

  return {
    h: bottomRight.y - topLeft.y,
    w: bottomRight.x - topLeft.x,
    x: topLeft.x,
    y: topLeft.y,
  };
}

/** @param {Rect | null} lineRect @param {number} count @returns {Rect[]} */
function fallbackWordRects(lineRect, count) {
  if (!lineRect || count <= 0) return [];
  const totalGap = INLINE_WORD_GAP_PX * Math.max(0, count - 1);
  const width = Math.max(48, (lineRect.w - totalGap) / count);
  return Array.from({ length: count }, (_, index) => ({
    h: Math.max(34, lineRect.h),
    w: width,
    x: lineRect.x + index * (width + INLINE_WORD_GAP_PX),
    y: lineRect.y,
  }));
}

/** @param {OpenSeadragon.Viewer | null | undefined} viewer @returns {Rect | null} */
function visibleImageBounds(viewer) {
  if (!viewer?.viewport || !viewer?.world?.getItemCount?.()) return null;
  const tiledImage = viewer.world.getItemAt(0);
  const viewportBounds = viewer.viewport.getBounds?.(true);
  const imageBounds = tiledImage?.viewportToImageRectangle?.(viewportBounds);
  if (!imageBounds) return null;
  const paddingX = imageBounds.width * 0.1;
  const paddingY = imageBounds.height * 0.1;
  return {
    h: imageBounds.height + paddingY * 2,
    w: imageBounds.width + paddingX * 2,
    x: imageBounds.x - paddingX,
    y: imageBounds.y - paddingY,
  };
}

/** @param {OverlayLabel} label @param {string} windowId @param {string} canvasId */
function dispatchOverlaySelection(label, windowId, canvasId) {
  document.dispatchEvent(new CustomEvent('scribe:select-annotation', {
    detail: {
      annotationId: label.id,
      canvasId,
      focusAnnotationId: label.isWord ? label.id : '',
      windowId,
    },
  }));
}

/** @param {ScribeTextOverlayProps} props */
function ScribeTextOverlayPlugin({
  annotationPage,
  canvasId,
  selectedAnnotationId,
  viewer,
  windowId,
}) {
  const [version, setVersion] = useState(0);
  const [editorState, setEditorState] = useState(/** @type {OverlayEditorState | null} */ (null));
  const [editorDock, setEditorDock] = useState('below');
  const [dragState, setDragState] = useState(/** @type {EditorDragState | null} */ (null));
  const [bboxDragState, setBboxDragState] = useState(/** @type {BBoxDragState | null} */ (null));
  const [pendingFocusWordId, setPendingFocusWordId] = useState('');
  const [pendingResizeFocus, setPendingResizeFocus] = useState(
    /** @type {FocusResizeHandleEventDetail | null} */ (null),
  );
  const [transcriptionSegment, setTranscriptionSegment] = useState(/** @type {TranscriptionSegment | null} */ (null));
  const [transcriptionResult, setTranscriptionResult] = useState(/** @type {TranscriptionResult | null} */ (null));
  const inputRefs = useRef(/** @type {Map<string, HTMLInputElement>} */ (new Map()));
  const resizeHandleRefs = useRef(/** @type {Map<string, HTMLButtonElement>} */ (new Map()));
  const dragIntentRef = useRef(/** @type {{ timeoutId: number } | null} */ (null));
  const transcriptionResultTimerRef = useRef(/** @type {number | null} */ (null));
  const viewportAnimationFrameRef = useRef(/** @type {number | null} */ (null));

  /** @param {number} clientX @param {number} clientY */
  function screenToImagePoint(clientX, clientY) {
    return clientPointToImage(viewer, clientX, clientY);
  }

  function clearDragIntent() {
    if (!dragIntentRef.current?.timeoutId) return;
    window.clearTimeout(dragIntentRef.current.timeoutId);
    dragIntentRef.current = null;
  }

  /** @param {import('react').PointerEvent<HTMLElement>} event @param {{ top: number, lineRect: Rect }} inlineEditor */
  function scheduleEditorDrag(event, inlineEditor) {
    const targetTag = event.target instanceof HTMLElement ? event.target.tagName : '';
    if (targetTag === 'INPUT' || targetTag === 'TEXTAREA') return;
    event.stopPropagation();
    clearDragIntent();
    const startY = event.clientY;
    dragIntentRef.current = {
      timeoutId: window.setTimeout(() => {
        setDragState({
          currentTop: inlineEditor.top,
          lineTop: inlineEditor.lineRect.y,
          pointerY: startY,
          startY,
        });
        dragIntentRef.current = null;
      }, 180),
    };
  }

  useEffect(() => {
    if (!viewer) return undefined;
    const update = () => {
      if (viewportAnimationFrameRef.current !== null) return;
      viewportAnimationFrameRef.current = window.requestAnimationFrame(() => {
        viewportAnimationFrameRef.current = null;
        setVersion((value) => value + 1);
      });
    };
    viewer.addHandler('update-viewport', update);
    viewer.addHandler('animation-finish', update);
    viewer.addHandler('tile-loaded', update);
    return () => {
      if (viewportAnimationFrameRef.current !== null) {
        window.cancelAnimationFrame(viewportAnimationFrameRef.current);
        viewportAnimationFrameRef.current = null;
      }
      viewer.removeHandler('update-viewport', update);
      viewer.removeHandler('animation-finish', update);
      viewer.removeHandler('tile-loaded', update);
    };
  }, [viewer]);

  useEffect(() => {
    setEditorState(null);
    setPendingResizeFocus(null);
    setTranscriptionSegment(null);
    setTranscriptionResult(null);
  }, [canvasId]);

  useEffect(() => {
    /** @param {Event} event */
    const handleEditorState = (event) => {
      const detail = /** @type {CustomEvent<OverlayEditorState>} */ (event).detail;
      if (detail?.windowId !== windowId) return;
      if (detail.canvasId !== canvasId) return;
      setEditorState(detail);
    };
    document.addEventListener('scribe:editor-state', handleEditorState);
    return () => document.removeEventListener('scribe:editor-state', handleEditorState);
  }, [canvasId, windowId]);

  useEffect(() => {
    /** @param {Event} event */
    const handleResizeFocus = (event) => {
      const detail = /** @type {CustomEvent<FocusResizeHandleEventDetail>} */ (event).detail;
      if (detail?.windowId !== windowId || detail.canvasId !== canvasId) return;
      setPendingResizeFocus(detail);
    };
    document.addEventListener('scribe:focus-resize-handle', handleResizeFocus);
    return () => document.removeEventListener('scribe:focus-resize-handle', handleResizeFocus);
  }, [canvasId, windowId]);

  useEffect(() => {
    /** @param {Event} event */
    const handle = (event) => {
      const detail = /** @type {CustomEvent<TranscriptionEventDetail>} */ (event).detail;
      if (detail?.windowId !== windowId || detail.canvasId !== canvasId) return;
      const { annotation, done = 0, total = 0 } = detail;
      setTranscriptionSegment(annotation ? { annotation, done, total } : null);
    };
    document.addEventListener('scribe:transcription-segment', handle);
    return () => document.removeEventListener('scribe:transcription-segment', handle);
  }, [canvasId, windowId]);

  useEffect(() => {
    /** @param {Event} event */
    const handle = (event) => {
      const detail = /** @type {CustomEvent<TranscriptionEventDetail>} */ (event).detail;
      if (detail?.windowId !== windowId || detail.canvasId !== canvasId) return;
      const { annotation, done = 0, total = 0 } = detail;
      if (!annotation) return;
      const text = annotationText(annotation) || null;
      setTranscriptionResult({ annotation, text, done, total });
      if (transcriptionResultTimerRef.current) {
        window.clearTimeout(transcriptionResultTimerRef.current);
      }
      transcriptionResultTimerRef.current = window.setTimeout(() => {
        setTranscriptionResult(null);
        transcriptionResultTimerRef.current = null;
      }, 1400);
    };
    document.addEventListener('scribe:transcription-result', handle);
    return () => {
      if (transcriptionResultTimerRef.current) {
        window.clearTimeout(transcriptionResultTimerRef.current);
        transcriptionResultTimerRef.current = null;
      }
      document.removeEventListener('scribe:transcription-result', handle);
    };
  }, [canvasId, windowId]);

  useEffect(() => {
    if (!dragState) return undefined;
    /** @param {PointerEvent} event */
    const handleMove = (event) => {
      setDragState((current) => (current ? { ...current, pointerY: event.clientY } : current));
    };
    const handleUp = () => {
      const finalTop = (dragState.currentTop || 0) + (dragState.pointerY - dragState.startY);
      setEditorDock(finalTop < dragState.lineTop ? 'above' : 'below');
      setDragState(null);
    };
    window.addEventListener('pointermove', handleMove);
    window.addEventListener('pointerup', handleUp);
    return () => {
      window.removeEventListener('pointermove', handleMove);
      window.removeEventListener('pointerup', handleUp);
    };
  }, [dragState]);

  useEffect(() => {
    if (!bboxDragState) return undefined;
    /** @param {PointerEvent} event */
    const handleMove = (event) => {
      setBboxDragState((current) => current
        ? { ...current, currentClientX: event.clientX, currentClientY: event.clientY }
        : current);
    };
    /** @param {PointerEvent} event */
    const handleUp = (event) => {
      const { handle, startClientX, startClientY, originalBBox, annotationId } = bboxDragState;
      const startPt = screenToImagePoint(startClientX, startClientY);
      const endPt = screenToImagePoint(event.clientX, event.clientY);
      setBboxDragState(null);
      if (!startPt || !endPt) return;
      const dx = endPt.x - startPt.x;
      const dy = endPt.y - startPt.y;
      let { x, y, w, h } = originalBBox;
      if (handle.startsWith('n')) { y += dy; h -= dy; }
      if (handle.startsWith('s')) { h += dy; }
      if (handle.endsWith('w')) { x += dx; w -= dx; }
      if (handle.endsWith('e')) { w += dx; }
      document.dispatchEvent(new CustomEvent('scribe:resize-annotation', {
        detail: {
          annotationId,
          bbox: normalizeImageBBox({ x, y, w, h }),
          canvasId,
          windowId,
        },
      }));
    };
    window.addEventListener('pointermove', handleMove);
    window.addEventListener('pointerup', handleUp);
    return () => {
      window.removeEventListener('pointermove', handleMove);
      window.removeEventListener('pointerup', handleUp);
    };
  }, [bboxDragState, canvasId, windowId]);

  useEffect(() => {
    const clear = () => clearDragIntent();
    window.addEventListener('pointerup', clear);
    window.addEventListener('pointercancel', clear);
    return () => {
      window.removeEventListener('pointerup', clear);
      window.removeEventListener('pointercancel', clear);
    };
  }, []);

  const activePage = editorState?.annotationPage || annotationPage;
  const activeSelectedAnnotationId = editorState?.selectedAnnotationId || selectedAnnotationId;
  const activeFocusedWordAnnotationId = editorState?.focusedWordAnnotationId || '';
  const overlayMode = editorState?.overlayMode || 'none';
  const editorIsBusy = Boolean(editorState?.isBusy);
  const inlineEditorVisible = overlayMode === 'edit';
  const textOverlayVisible = overlayMode === 'read';
  const outlineVisible = overlayMode === 'outline';

  useEffect(() => {
    if (!viewer) return undefined;
    viewer.setMouseNavEnabled(!inlineEditorVisible);
    return () => {
      viewer.setMouseNavEnabled(true);
    };
  }, [inlineEditorVisible, viewer]);

  useEffect(() => {
    setEditorDock('below');
    setDragState(null);
    setPendingFocusWordId('');
    clearDragIntent();
  }, [activeSelectedAnnotationId]);

  const transcriptionRect = useMemo(() => {
    if (!transcriptionSegment?.annotation || !viewer) return null;
    return annotationRect(viewer, transcriptionSegment.annotation);
  }, [transcriptionSegment, viewer, version]);

  const transcriptionResultRect = useMemo(() => {
    if (!transcriptionResult?.annotation || !viewer) return null;
    return annotationRect(viewer, transcriptionResult.annotation);
  }, [transcriptionResult, viewer, version]);

  const labels = /** @type {OverlayLabel[]} */ (useMemo(() => {
    if (!textOverlayVisible) return [];
    const visibleBounds = visibleImageBounds(viewer);
    const visiblePage = {
      ...activePage,
      items: (Array.isArray(activePage?.items) ? activePage.items : [])
        .filter((annotation) => annotationIntersectsImageRect(annotation, visibleBounds)),
    };
    return groupAnnotationsForEditor(visiblePage)
      .flatMap((row) => (row.granularity === 'word' ? row.fields : [row.lead || row.fields[0]]))
      .map((annotation) => ({
        id: annotation?.id,
        isWord: isWordAnnotation(annotation),
        rect: annotationRect(viewer, annotation),
        text: annotationText(annotation),
      }))
      .filter((item) => item.id && item.text && item.rect && item.rect.w > 4 && item.rect.h > 4);
  }, [activePage, textOverlayVisible, viewer, version]));
  const selectedDecoration = useMemo(() => {
    const items = Array.isArray(activePage?.items) ? activePage.items : [];
    const selected = items.find((annotation) => annotation?.id === activeSelectedAnnotationId) || null;
    if (!selected) return { lineAnnotation: null, lineRect: null, wordRect: null };

    const lineAnnotation = lineAnnotationForSelection(activePage, selected)
      || (isLineAnnotation(selected)
        ? selected
        : items.find((annotation) => isLineAnnotation(annotation) && annotationsShareLine(annotation, selected)) || null);
    const wordAnnotation = items.find((annotation) => annotation?.id === activeFocusedWordAnnotationId) || null;

    return {
      lineAnnotation,
      lineRect: lineAnnotation ? annotationRect(viewer, lineAnnotation) : null,
      wordRect: wordAnnotation && isWordAnnotation(wordAnnotation) ? annotationRect(viewer, wordAnnotation) : null,
    };
  }, [activeFocusedWordAnnotationId, activePage, activeSelectedAnnotationId, viewer, version]);
  const inlineEditor = useMemo(() => {
    if (!inlineEditorVisible || !viewer) return null;
    const items = Array.isArray(activePage?.items) ? activePage.items : [];
    const selected = items.find((annotation) => annotation?.id === activeSelectedAnnotationId) || null;
    if (!selected) return null;

    const lineAnnotation = lineAnnotationForSelection(activePage, selected) || selected;
    const lineRect = annotationRect(viewer, lineAnnotation);
    const row = findEditorRowByAnnotationId(activePage, activeSelectedAnnotationId) || findEditorRowByAnnotationId(activePage, lineAnnotation.id || '');
    if (!lineRect || !row || !viewer?.canvas) return null;

    const canvasRect = viewer.canvas.getBoundingClientRect();
    const editorWidth = Math.max(INLINE_EDITOR_MIN_WIDTH_PX, Math.min(canvasRect.width - 24, Math.max(lineRect.w, INLINE_EDITOR_MIN_WIDTH_PX)));
    const editorHeight = INLINE_EDITOR_HEIGHT_PX + INLINE_EDITOR_HANDLE_PX;
    const maxTop = Math.max(12, canvasRect.height - editorHeight - ACTION_BAR_HEIGHT_PX - 20);
    const preferredTop = lineRect.y + lineRect.h + INLINE_EDITOR_GAP_PX;
    const fallbackTop = lineRect.y - editorHeight - INLINE_EDITOR_GAP_PX;
    const baseTop = editorDock === 'above'
      ? Math.max(12, fallbackTop)
      : (preferredTop <= maxTop ? preferredTop : Math.max(12, fallbackTop));
    const dragOffset = dragState ? dragState.pointerY - dragState.startY : 0;
    const top = Math.max(12, Math.min(maxTop, baseTop + dragOffset));
    const left = Math.max(12, Math.min(lineRect.x, canvasRect.width - editorWidth - 12));

    const tokens = row.granularity === 'word'
      ? row.fields.map((annotation) => ({
        annotationId: annotation.id,
        rect: annotationRect(viewer, annotation),
        text: annotationText(annotation),
      }))
      : rowText(row).split(/\s+/).filter(Boolean).map((token, index) => ({
        annotationId: null,
        fallbackIndex: index,
        rect: null,
        text: token,
      }));

    const fallbackRects = row.granularity === 'word' ? [] : fallbackWordRects(lineRect, Math.max(tokens.length, 1));
    const wordEditors = (tokens.length > 0 ? tokens : [{
      annotationId: null,
      fallbackIndex: 0,
      rect: null,
      text: rowText(row),
    }]).map((token, index) => ({
      ...token,
      rect: token.rect || fallbackRects[index] || {
        h: Math.max(34, lineRect.h),
        w: editorWidth,
        x: lineRect.x,
        y: lineRect.y,
      },
    }));

    return {
      editorWidth,
      left,
      lineRect,
      row,
      top,
      width: editorWidth,
      wordEditors,
      height: editorHeight,
      contentTop: INLINE_EDITOR_HANDLE_PX + INLINE_EDITOR_CONTENT_INSET_PX,
    };
  }, [activePage, activeSelectedAnnotationId, dragState, editorDock, inlineEditorVisible, viewer, version]);

  useEffect(() => {
    if (editorIsBusy) {
      const focused = document.activeElement;
      if (focused instanceof HTMLInputElement
        && Array.from(inputRefs.current.values()).includes(focused)) focused.blur();
      return;
    }
    if (overlayMode !== 'edit' || !inlineEditor) return;
    // Don't steal focus from an editor control after a later state update. In
    // particular, keyboard line creation focuses a resize handle before every
    // related editor-state update has finished rendering.
    if (!pendingFocusWordId) {
      const focused = document.activeElement;
      const isOurInput = focused instanceof HTMLInputElement
        && Array.from(inputRefs.current.values()).includes(focused);
      const isOurResizeHandle = focused instanceof HTMLButtonElement
        && Array.from(resizeHandleRefs.current.values()).includes(focused);
      if (isOurInput || isOurResizeHandle) return;
    }
    const targetId = pendingFocusWordId || activeFocusedWordAnnotationId || inlineEditor.wordEditors.find((word) => word.annotationId)?.annotationId || activeSelectedAnnotationId;
    const target = inputRefs.current.get(targetId);
    if (!(target instanceof HTMLInputElement)) return;
    target.focus();
    const end = target.value.length;
    target.setSelectionRange(end, end);
    if (pendingFocusWordId && pendingFocusWordId === targetId) {
      setPendingFocusWordId('');
    }
  }, [activeFocusedWordAnnotationId, activeSelectedAnnotationId, editorIsBusy, inlineEditor, overlayMode, pendingFocusWordId]);

  useEffect(() => {
    if (!pendingResizeFocus || editorIsBusy || !inlineEditorVisible) return;
    if (selectedDecoration.lineAnnotation?.id !== pendingResizeFocus.annotationId) return;
    const target = resizeHandleRefs.current.get(pendingResizeFocus.handle);
    if (!(target instanceof HTMLButtonElement)) return;
    target.focus();
    setPendingResizeFocus(null);
  }, [editorIsBusy, inlineEditorVisible, pendingResizeFocus, selectedDecoration.lineAnnotation]);

  const focusBounds = useMemo(() => {
    if (!inlineEditorVisible || !selectedDecoration.lineRect) return null;
    const linePaddingX = 10;
    const linePaddingY = 6;
    return {
      bottom: Math.min((viewer?.canvas?.getBoundingClientRect()?.height || 0) - 8, selectedDecoration.lineRect.y + selectedDecoration.lineRect.h + linePaddingY),
      left: Math.max(8, selectedDecoration.lineRect.x - linePaddingX),
      right: selectedDecoration.lineRect.x + selectedDecoration.lineRect.w + linePaddingX,
      top: Math.max(8, selectedDecoration.lineRect.y - linePaddingY),
    };
  }, [inlineEditorVisible, selectedDecoration.lineRect, viewer]);

  if (!viewer) return null;
  if (overlayMode === 'none' && !transcriptionRect && !transcriptionResultRect) return null;

  const currentVisibleImageBounds = visibleImageBounds(viewer);
  const outlineRects = /** @type {Array<{ id: string, rect: Rect }>} */ (outlineVisible
    ? (Array.isArray(activePage?.items) ? activePage.items : [])
        .filter(isLineAnnotation)
        .filter((annotation) => annotationIntersectsImageRect(annotation, currentVisibleImageBounds))
        .map((annotation) => ({ id: annotation.id, rect: annotationRect(viewer, annotation) }))
        .filter(({ id, rect }) => id && rect && rect.w > 4 && rect.h > 4)
    : []);

  return ReactDOM.createPortal(
    <div
      className="scribe-text-overlay"
      style={{
        height: '100%',
        left: 0,
        pointerEvents: 'none',
        position: 'absolute',
        top: 0,
        width: '100%',
        zIndex: 1200,
      }}
    >
      {focusBounds ? (
        <>
          <div
            style={{
              background: 'rgba(15,23,42,0.35)',
              left: 0,
              pointerEvents: 'none',
              position: 'absolute',
              top: 0,
              width: '100%',
              height: `${Math.max(0, focusBounds.top)}px`,
            }}
          />
          <div
            style={{
              background: 'rgba(15,23,42,0.35)',
              left: 0,
              pointerEvents: 'none',
              position: 'absolute',
              top: `${focusBounds.top}px`,
              width: `${Math.max(0, focusBounds.left)}px`,
              height: `${Math.max(24, focusBounds.bottom - focusBounds.top)}px`,
            }}
          />
          <div
            style={{
              background: 'rgba(15,23,42,0.35)',
              left: `${focusBounds.right}px`,
              pointerEvents: 'none',
              position: 'absolute',
              top: `${focusBounds.top}px`,
              width: `calc(100% - ${focusBounds.right}px)`,
              height: `${Math.max(24, focusBounds.bottom - focusBounds.top)}px`,
            }}
          />
          <div
            style={{
              background: 'rgba(15,23,42,0.35)',
              left: 0,
              pointerEvents: 'none',
              position: 'absolute',
              top: `${focusBounds.bottom}px`,
              width: '100%',
              height: `calc(100% - ${focusBounds.bottom}px)`,
            }}
          />
        </>
      ) : null}
      {inlineEditorVisible && selectedDecoration.lineRect ? (() => {
        const lr = selectedDecoration.lineRect;
        const dragDx = bboxDragState ? (bboxDragState.currentClientX - bboxDragState.startClientX) : 0;
        const dragDy = bboxDragState ? (bboxDragState.currentClientY - bboxDragState.startClientY) : 0;
        const { handle: dragHandle } = bboxDragState || {};
        const previewRect = bboxDragState ? {
          x: lr.x + (dragHandle?.endsWith('w') ? dragDx : 0),
          y: lr.y + (dragHandle?.startsWith('n') ? dragDy : 0),
          w: Math.max(8, lr.w + (dragHandle?.endsWith('e') ? dragDx : dragHandle?.endsWith('w') ? -dragDx : 0)),
          h: Math.max(8, lr.h + (dragHandle?.startsWith('s') ? dragDy : dragHandle?.startsWith('n') ? -dragDy : 0)),
        } : { x: lr.x, y: lr.y, w: lr.w, h: lr.h };

        const HANDLE_SIZE = 32;
        const corners = [
          { handle: 'nw', cx: previewRect.x, cy: previewRect.y, cursor: 'nw-resize' },
          { handle: 'ne', cx: previewRect.x + previewRect.w, cy: previewRect.y, cursor: 'ne-resize' },
          { handle: 'sw', cx: previewRect.x, cy: previewRect.y + previewRect.h, cursor: 'sw-resize' },
          { handle: 'se', cx: previewRect.x + previewRect.w, cy: previewRect.y + previewRect.h, cursor: 'se-resize' },
        ];
        return (
          <>
            <div
              style={{
                border: '1px dashed rgba(217,119,6,0.65)',
                boxSizing: 'border-box',
                height: `${Math.max(8, previewRect.h)}px`,
                left: previewRect.x,
                pointerEvents: 'none',
                position: 'absolute',
                top: previewRect.y,
                width: `${Math.max(8, previewRect.w)}px`,
                zIndex: 15,
              }}
            />
            {corners.map(({ handle, cx, cy, cursor }) => (
              <button
                aria-label={`Resize annotation from the ${handle} corner`}
                aria-keyshortcuts="ArrowUp ArrowDown ArrowLeft ArrowRight"
                key={handle}
                type="button"
                disabled={editorIsBusy}
                ref={(node) => {
                  if (node) resizeHandleRefs.current.set(handle, node);
                  else resizeHandleRefs.current.delete(handle);
                }}
                style={{
                  background: 'radial-gradient(circle, rgba(255,255,255,0.98) 0 3px, rgba(217,119,6,0.95) 4px 5px, transparent 6px)',
                  border: 0,
                  borderRadius: '50%',
                  boxSizing: 'border-box',
                  cursor,
                  height: HANDLE_SIZE,
                  left: cx - HANDLE_SIZE / 2,
                  pointerEvents: 'auto',
                  padding: 0,
                  position: 'absolute',
                  top: cy - HANDLE_SIZE / 2,
                  width: HANDLE_SIZE,
                  zIndex: 25,
                }}
                onPointerDown={(event) => {
                  event.stopPropagation();
                  event.preventDefault();
                  if (editorIsBusy) return;
                  const ann = selectedDecoration.lineAnnotation;
                  if (!ann?.id) return;
                  setBboxDragState({
                    annotationId: ann.id,
                    currentClientX: event.clientX,
                    currentClientY: event.clientY,
                    handle,
                    originalBBox: annotationBBox(ann),
                    startClientX: event.clientX,
                    startClientY: event.clientY,
                  });
                }}
                onKeyDown={(event) => {
                  if (editorIsBusy) return;
                  if (!['ArrowLeft', 'ArrowRight', 'ArrowUp', 'ArrowDown'].includes(event.key)) return;
                  event.preventDefault();
                  event.stopPropagation();
                  const ann = selectedDecoration.lineAnnotation;
                  if (!ann) return;
                  const step = event.shiftKey ? 10 : 1;
                  const dx = event.key === 'ArrowLeft' ? -step : event.key === 'ArrowRight' ? step : 0;
                  const dy = event.key === 'ArrowUp' ? -step : event.key === 'ArrowDown' ? step : 0;
                  let { x, y, w, h } = annotationBBox(ann);
                  if (handle.endsWith('w')) { x += dx; w -= dx; }
                  if (handle.endsWith('e')) w += dx;
                  if (handle.startsWith('n')) { y += dy; h -= dy; }
                  if (handle.startsWith('s')) h += dy;
                  document.dispatchEvent(new CustomEvent('scribe:resize-annotation', {
                    detail: {
                      annotationId: ann.id,
                      bbox: normalizeImageBBox({ x, y, w, h }),
                      canvasId,
                      windowId,
                    },
                  }));
                }}
              />
            ))}
          </>
        );
      })() : null}
      {labels.map((label) => (
        <button
          aria-label={`Edit ${label.isWord ? 'word' : 'line'}: ${label.text || 'empty text'}`}
          key={label.id}
          type="button"
          disabled={editorIsBusy}
          onMouseDown={(event) => {
            event.preventDefault();
            event.stopPropagation();
            if (editorIsBusy) return;
            if (label.isWord) setPendingFocusWordId(label.id);
            dispatchOverlaySelection(label, windowId, canvasId);
          }}
          onPointerDown={(event) => {
            event.preventDefault();
            event.stopPropagation();
            if (editorIsBusy) return;
            if (label.isWord) setPendingFocusWordId(label.id);
            dispatchOverlaySelection(label, windowId, canvasId);
          }}
          onKeyDown={(event) => {
            if (editorIsBusy) return;
            if (event.key !== 'Enter' && event.key !== ' ') return;
            event.preventDefault();
            if (label.isWord) setPendingFocusWordId(label.id);
            dispatchOverlaySelection(label, windowId, canvasId);
          }}
          style={{
            background: label.id === activeFocusedWordAnnotationId || label.id === activeSelectedAnnotationId ? 'rgba(251, 191, 36, 0.88)' : 'rgba(15, 23, 42, 0.78)',
            border: label.id === activeFocusedWordAnnotationId || label.id === activeSelectedAnnotationId ? '1px solid rgba(245, 158, 11, 0.95)' : '1px solid rgba(148, 163, 184, 0.45)',
            borderRadius: 4,
            boxSizing: 'border-box',
            color: '#f8fafc',
            cursor: 'text',
            display: 'flex',
            fontSize: Math.max(11, Math.min(label.isWord ? 17 : 18, label.rect.h * 0.72)),
            fontWeight: label.isWord ? 700 : 600,
            height: label.rect.h,
            alignItems: 'center',
            justifyContent: 'flex-start',
            left: label.rect.x,
            lineHeight: 1.1,
            maxWidth: label.rect.w,
            overflow: 'hidden',
            pointerEvents: 'auto',
            padding: 0,
            position: 'absolute',
            textOverflow: 'ellipsis',
            top: label.rect.y,
            whiteSpace: 'nowrap',
            width: label.rect.w,
            zIndex: 30,
          }}
        >
          {label.text}
        </button>
      ))}
      {inlineEditor ? (
        <div
          style={{
            height: `${inlineEditor.height}px`,
            left: inlineEditor.left,
            pointerEvents: 'auto',
            position: 'absolute',
            top: inlineEditor.top,
            width: inlineEditor.width,
            zIndex: 200,
          }}
          onMouseDown={(event) => {
            event.stopPropagation();
          }}
          onMouseUp={(event) => {
            event.stopPropagation();
          }}
          onClick={(event) => {
            event.stopPropagation();
          }}
          onPointerDown={(event) => {
            scheduleEditorDrag(event, inlineEditor);
          }}
          onPointerUp={() => {
            clearDragIntent();
          }}
          onPointerLeave={() => {
            clearDragIntent();
          }}
          onPointerCancel={() => {
            clearDragIntent();
          }}
        >
          <div
            style={{
              alignItems: 'center',
              cursor: dragState ? 'grabbing' : 'grab',
              display: 'flex',
              height: `${INLINE_EDITOR_HANDLE_PX}px`,
              justifyContent: 'center',
              pointerEvents: 'auto',
              width: '100%',
            }}
            onPointerDown={(event) => {
              scheduleEditorDrag(event, inlineEditor);
            }}
          >
            <div
              style={{
                background: 'rgba(255,255,255,0.9)',
                border: '1px solid rgba(148,163,184,0.35)',
                borderRadius: 999,
                height: '6px',
                width: '72px',
              }}
            />
          </div>
          <div
            style={{
              background: 'rgba(255,255,255,0.98)',
              borderRadius: 10,
              boxShadow: '0 14px 24px rgba(15,23,42,0.14)',
              height: `${INLINE_EDITOR_HEIGHT_PX}px`,
              left: 0,
              pointerEvents: 'none',
              position: 'absolute',
              top: `${INLINE_EDITOR_HANDLE_PX}px`,
              width: '100%',
              zIndex: 205,
            }}
            onPointerDown={(event) => {
              scheduleEditorDrag(event, inlineEditor);
            }}
          />
          {inlineEditor.row.granularity === 'word' ? inlineEditor.wordEditors.map((word) => {
            const rect = word.rect;
            if (!rect) return null;
            return (
              <input
                aria-label={`Edit word ${word.text || 'with empty text'}`}
                key={word.annotationId}
                disabled={editorIsBusy}
                ref={(node) => {
                  if (!word.annotationId) return;
                  if (node) inputRefs.current.set(word.annotationId, node);
                  else inputRefs.current.delete(word.annotationId);
                }}
                value={word.text}
                onMouseDown={(event) => {
                  event.stopPropagation();
                  event.nativeEvent.stopPropagation();
                }}
                onMouseUp={(event) => {
                  event.stopPropagation();
                  event.nativeEvent.stopPropagation();
                }}
                onClick={(event) => {
                  event.stopPropagation();
                  event.nativeEvent.stopPropagation();
                }}
                onPointerDown={(event) => {
                  event.stopPropagation();
                  event.nativeEvent.stopPropagation();
                }}
                onPointerUp={(event) => {
                  event.stopPropagation();
                  event.nativeEvent.stopPropagation();
                }}
                onFocus={() => {
                  if (!word.annotationId) return;
                  setPendingFocusWordId('');
                  document.dispatchEvent(new CustomEvent('scribe:select-annotation', {
                    detail: { annotationId: word.annotationId, canvasId, windowId },
                  }));
                }}
                onChange={(event) => {
                  if (editorIsBusy) return;
                  document.dispatchEvent(new CustomEvent('scribe:inline-change-word', {
                    detail: {
                      annotationId: word.annotationId,
                      canvasId,
                      text: event.target.value,
                      windowId,
                    },
                  }));
                }}
                onKeyDown={(event) => {
                  if (editorIsBusy) return;
                  if (event.key === 'Tab') {
                    event.preventDefault();
                    document.dispatchEvent(new CustomEvent('scribe:inline-step-selection', {
                      detail: {
                        direction: event.shiftKey ? -1 : 1,
                        canvasId,
                        windowId,
                      },
                    }));
                    return;
                  }
                  if (event.key === 'Enter' && !(event.metaKey || event.ctrlKey)) {
                    event.preventDefault();
                    document.dispatchEvent(new CustomEvent('scribe:inline-save', {
                      detail: { canvasId, windowId },
                    }));
                  }
                }}
                style={{
                  background: 'rgba(255,255,255,0.96)',
                  border: '2px solid rgba(245, 158, 11, 0.86)',
                  borderRadius: 8,
                  boxShadow: '0 8px 16px rgba(15,23,42,0.08)',
                  color: '#0f172a',
                  fontFamily: '"IBM Plex Sans", "Helvetica Neue", sans-serif',
                  fontSize: `${Math.max(14, Math.min(20, rect.h * 0.75))}px`,
                  fontWeight: 600,
                  height: `${Math.max(36, rect.h + 10)}px`,
                  left: Math.max(0, rect.x - inlineEditor.left),
                  lineHeight: 1.1,
                  minWidth: '40px',
                  padding: '10px 10px',
                  pointerEvents: 'auto',
                  position: 'absolute',
                  top: `${inlineEditor.contentTop}px`,
                  width: `${Math.max(40, rect.w)}px`,
                  zIndex: 220,
                }}
              />
            );
          }) : (
            <div
              style={{
                display: 'flex',
                gap: `${INLINE_WORD_GAP_PX}px`,
                pointerEvents: 'auto',
                width: '100%',
              }}
            >
              {inlineEditor.wordEditors.map((word, index) => (
                <input
                  aria-label={`Edit line token ${index + 1}`}
                  key={`fallback-${index}`}
                  disabled={editorIsBusy}
                  ref={(node) => {
                    if (index !== 0 || !activeSelectedAnnotationId) return;
                    if (node) inputRefs.current.set(activeSelectedAnnotationId, node);
                    else inputRefs.current.delete(activeSelectedAnnotationId);
                  }}
                  value={word.text}
                  onMouseDown={(event) => {
                    event.stopPropagation();
                    event.nativeEvent.stopPropagation();
                  }}
                  onMouseUp={(event) => {
                    event.stopPropagation();
                    event.nativeEvent.stopPropagation();
                  }}
                  onClick={(event) => {
                    event.stopPropagation();
                    event.nativeEvent.stopPropagation();
                  }}
                  onPointerDown={(event) => {
                    event.stopPropagation();
                    event.nativeEvent.stopPropagation();
                  }}
                  onPointerUp={(event) => {
                    event.stopPropagation();
                    event.nativeEvent.stopPropagation();
                  }}
                  onChange={(event) => {
                    if (editorIsBusy) return;
                    const inputs = Array.from(event.currentTarget.parentElement?.querySelectorAll('input') || []);
                    const nextText = inputs.map((input, inputIndex) => (
                      inputIndex === index ? event.target.value : input.value
                    )).join(' ').trim();
                    document.dispatchEvent(new CustomEvent('scribe:inline-change-text', {
                      detail: {
                        selectionStart: event.target.selectionStart,
                        canvasId,
                        text: nextText,
                        windowId,
                      },
                    }));
                  }}
                  onKeyDown={(event) => {
                    if (editorIsBusy) return;
                    if (event.key === 'Tab') {
                      event.preventDefault();
                      document.dispatchEvent(new CustomEvent('scribe:inline-step-selection', {
                        detail: {
                          direction: event.shiftKey ? -1 : 1,
                          canvasId,
                          windowId,
                        },
                      }));
                      return;
                    }
                    if (event.key === 'Enter' && !(event.metaKey || event.ctrlKey)) {
                      event.preventDefault();
                      document.dispatchEvent(new CustomEvent('scribe:inline-save', {
                        detail: { canvasId, windowId },
                      }));
                    }
                  }}
                  style={{
                    background: 'rgba(255,255,255,0.96)',
                    border: '2px solid rgba(245, 158, 11, 0.86)',
                    borderRadius: 8,
                    boxShadow: '0 8px 16px rgba(15,23,42,0.08)',
                    color: '#0f172a',
                    flex: `${Math.max(1, word.rect.w)} 1 0`,
                    fontFamily: '"IBM Plex Sans", "Helvetica Neue", sans-serif',
                    fontSize: '16px',
                    fontWeight: 600,
                    height: `${INLINE_EDITOR_HEIGHT_PX - INLINE_EDITOR_CONTENT_INSET_PX * 2}px`,
                    minWidth: '48px',
                    padding: '10px 10px',
                    pointerEvents: 'auto',
                    position: 'relative',
                    zIndex: 220,
                  }}
                />
              ))}
            </div>
          )}
        </div>
      ) : null}
      {outlineRects.map(({ id, rect }) => (
        <div
          key={id}
          style={{
            border: '1px solid rgba(148,163,184,0.55)',
            borderRadius: 2,
            boxSizing: 'border-box',
            height: `${rect.h}px`,
            left: rect.x,
            pointerEvents: 'none',
            position: 'absolute',
            top: rect.y,
            width: `${rect.w}px`,
            zIndex: 5,
          }}
        />
      ))}
      {transcriptionResultRect && transcriptionResult?.text ? (() => {
        const rr = transcriptionResultRect;
        const fontSize = Math.max(11, Math.min(18, rr.h * 0.68));
        return (
          <div
            key={transcriptionResult.annotation?.id}
            style={{
              alignItems: 'center',
              animation: 'scribeResultDissolve 1.2s ease-out forwards',
              background: 'rgba(15,23,42,0.82)',
              borderRadius: 3,
              boxSizing: 'border-box',
              color: '#f1f5f9',
              display: 'flex',
              fontFamily: '"IBM Plex Sans","Helvetica Neue",sans-serif',
              fontSize,
              fontWeight: 500,
              height: `${Math.max(20, rr.h)}px`,
              left: rr.x,
              letterSpacing: '0.01em',
              lineHeight: 1.2,
              overflow: 'hidden',
              padding: '0 6px',
              pointerEvents: 'none',
              position: 'absolute',
              top: rr.y,
              width: `${Math.max(40, rr.w)}px`,
              zIndex: 1402,
            }}
          >
            {transcriptionResult.text}
          </div>
        );
      })() : null}
      {transcriptionRect && transcriptionSegment ? (() => {
        const tr = transcriptionRect;
        const canvasWidth = viewer.canvas?.clientWidth || 9999;
        const badgeWidth = 72;
        const badgeLeft = tr.x + tr.w + 8;
        const clampedBadgeLeft = Math.min(badgeLeft, canvasWidth - badgeWidth - 4);
        return (
          <>
            <div
              style={{
                animation: 'scribeSegmentPulse 1.4s ease-in-out infinite',
                border: '2px solid rgba(139,92,246,0.9)',
                borderRadius: 4,
                boxSizing: 'border-box',
                height: `${Math.max(8, tr.h + 6)}px`,
                left: tr.x - 3,
                pointerEvents: 'none',
                position: 'absolute',
                top: tr.y - 3,
                transition: 'left 0.2s ease, top 0.2s ease, width 0.2s ease, height 0.2s ease',
                width: `${Math.max(8, tr.w + 6)}px`,
                zIndex: 1400,
              }}
            />
            <div
              style={{
                alignItems: 'center',
                background: 'rgba(109,40,217,0.93)',
                backdropFilter: 'blur(6px)',
                borderRadius: 20,
                boxShadow: '0 2px 10px rgba(109,40,217,0.5)',
                color: '#fff',
                display: 'flex',
                fontSize: 11,
                fontWeight: 700,
                gap: 4,
                left: clampedBadgeLeft,
                padding: '3px 8px 3px 5px',
                pointerEvents: 'none',
                position: 'absolute',
                top: tr.y + tr.h / 2 - 13,
                transition: 'left 0.2s ease, top 0.2s ease',
                whiteSpace: 'nowrap',
                zIndex: 1401,
              }}
            >
              <svg width="12" height="12" viewBox="0 0 24 24" fill="white" aria-hidden="true">
                <path d="M7.5 5.6L10 7 8.6 4.5 10 2 7.5 3.4 5 2l1.4 2.5L5 7zm12 9.8L17 14l1.4 2.5L17 19l2.5-1.4L22 19l-1.4-2.5L22 14zM22 2l-2.5 1.4L17 2l1.4 2.5L17 7l2.5-1.4L22 7l-1.4-2.5zm-7.63 5.29a1 1 0 00-1.41 0L1.29 18.96a1 1 0 000 1.41l2.34 2.34c.39.39 1.02.39 1.41 0L16.7 11.05a1 1 0 000-1.41zM14.21 7L17 9.79 9.79 17 7 14.21z" />
              </svg>
              <div
                style={{
                  animation: 'scribeSpinner 0.75s linear infinite',
                  border: '1.5px solid rgba(255,255,255,0.3)',
                  borderRadius: '50%',
                  borderTopColor: '#fff',
                  flexShrink: 0,
                  height: 10,
                  width: 10,
                }}
              />
              <span>{transcriptionSegment.done}&nbsp;/&nbsp;{transcriptionSegment.total}</span>
            </div>
          </>
        );
      })() : null}
    </div>,
    viewer.canvas,
  );
}

ScribeTextOverlayPlugin.propTypes = {
  annotationPage: PropTypes.shape({
    items: PropTypes.array,
  }),
  canvasId: PropTypes.string.isRequired,
  selectedAnnotationId: PropTypes.string,
  viewer: PropTypes.object,
  windowId: PropTypes.string.isRequired,
};

/** @param {MiradorState} state @param {WindowOwnProps} ownProps */
const mapStateToProps = (state, { windowId }) => {
  const selectedAnnotationId = selectedAnnotationIdForWindow(state, windowId);
  // The Mirador window is authoritative. A selection from the previous
  // Canvas may remain in Redux briefly during a page turn and must never route
  // the overlay back to that stale page.
  const canvasId = canvasIdForWindow(state, windowId);

  return {
    annotationPage: annotationPageForCanvas(state, canvasId),
    canvasId,
    selectedAnnotationId,
  };
};

const scribeTextOverlayPlugin = {
  component: ScribeTextOverlayPlugin,
  mapStateToProps,
  mode: 'add',
  target: 'OpenSeadragonViewer',
};

export default scribeTextOverlayPlugin;
