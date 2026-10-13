import { useEffect, useMemo, useRef, useState } from 'react';
import ReactDOM from 'react-dom';
import PropTypes from 'prop-types';
import OpenSeadragon from 'openseadragon';
import {
  clampBBoxWithin,
  clientPointToImage,
  imageBoundsBBox,
  normalizeImageBBox,
  resizeBBoxFromHandle,
  translateBBox,
} from '../editor/geometry';
import { installViewerGestureGuard, isClickGesture } from '../editor/viewerGestures';
import { scribeTheme } from '../theme';
import {
  annotationBBox,
  annotationIntersectsImageRect,
  annotationText,
  annotationsShareLine,
  annotationPageForCanvas,
  canvasIdForWindow,
  findEditorRowByAnnotationId,
  groupAnnotationsForEditor,
  transcriptRowsInPageOrder,
  isLineAnnotation,
  isWordAnnotation,
  lineAnnotationForSelection,
  rowSelectionId,
  rowText,
  selectedAnnotationIdForWindow,
} from '../utils/iiif';

// Inject keyframe animations once at module load
if (typeof document !== 'undefined' && !document.getElementById('scribe-transcription-kf')) {
  const kfStyle = document.createElement('style');
  kfStyle.id = 'scribe-transcription-kf';
  kfStyle.textContent = [
    '@keyframes scribeSegmentPulse{0%,100%{opacity:1;box-shadow:0 0 0 0 var(--scribe-plugin-transcribe)}50%{opacity:.75;box-shadow:0 0 0 5px transparent}}',
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
const TRANSCRIPTION_SEGMENT_MAX_DISPLAY_MS = 400;
const TRANSCRIPTION_SEGMENT_MIN_DISPLAY_MS = 10;
// Completed-job catch-up is emitted over the shell's five-second replay
// window. Drain live bursts within that same bound so a terminal clear cannot
// leave the wand visibly working through stale lines after the job is done.
const TRANSCRIPTION_SEGMENT_MAX_TOTAL_DISPLAY_MS = 5_000;
const TRANSCRIPTION_SEGMENT_QUEUE_LIMIT = 500;
const HANDLE_SIZE_PX = 32;
const KEYBOARD_NUDGE_PX = 1;
const KEYBOARD_NUDGE_LARGE_PX = 10;
const TRANSCRIPT_PANE_MIN_WIDTH_PX = 260;
const TRANSCRIPT_PANE_WIDTH_RATIO = 0.45;
const TRANSCRIPT_ROW_MIN_HEIGHT_PX = 32;
// Keep flowing transcript rows readable at every image zoom.
export const TRANSCRIPT_FONT_FAMILY = '"IBM Plex Sans", "Helvetica Neue", sans-serif';
export const TRANSCRIPT_FONT_SIZE_PX = 15;

/** @typedef {import('../types/scribe').ImageBBox} Rect */
/** @typedef {import('../types/scribe').IIIFAnnotation} IIIFAnnotation */
/** @typedef {import('../types/scribe').IIIFAnnotationPage} IIIFAnnotationPage */
/** @typedef {import('../types/scribe').MiradorState} MiradorState */
/** @typedef {import('../types/scribe').ScribeOverlayMode} OverlayMode */
/** @typedef {import('../types/scribe').ScribeFocusResizeHandleEventDetail} FocusResizeHandleEventDetail */
/** @typedef {import('../editor/geometry').BBoxHandle} BBoxHandle */
/** @typedef {{ annotationPage?: IIIFAnnotationPage, canvasId?: string, selectedAnnotationId?: string, focusedWordAnnotationId?: string, isBusy?: boolean, overlayMode?: OverlayMode, windowId?: string }} OverlayEditorState */
/** @typedef {{ currentTop: number, lineTop: number, pointerY: number, startY: number }} EditorDragState */
/** @typedef {{ annotationId: string, containerBBox: Rect | null, currentClientX: number, currentClientY: number, handle: BBoxHandle | 'move', originalBBox: Rect, startClientX: number, startClientY: number }} BBoxDragState */
/** @typedef {{ annotationId: string, isWord: boolean, overlayMode: OverlayMode, pointerId: number, x: number, y: number }} HitPressState */
/** @typedef {{ annotation: IIIFAnnotation, attemptNumber: number, done: number, jobId: string, total: number }} TranscriptionSegment */
/** @typedef {{ annotation: IIIFAnnotation, attemptNumber: number, done: number, jobId: string, total: number, text: string | null }} TranscriptionResult */
/** @typedef {{ annotation?: IIIFAnnotation | null, attemptNumber?: number, canvasId?: string, done?: number, jobId?: string, total?: number, windowId?: string }} TranscriptionEventDetail */
/** @typedef {{ id: string, isWord: boolean, rect: Rect, text: string }} OverlayLabel */
/** @typedef {{ granularity: 'line' | 'word', id: string, rect: Rect, selected: boolean }} GranularityMarker */
/** @typedef {{ annotation: IIIFAnnotation, containerBBox: Rect | null, isWord: boolean, rect: Rect }} GeometryTarget */
/** @typedef {{ id: string, selected: boolean, text: string, wordIds: string[] }} TranscriptRow */
/** @typedef {{ annotationId: string | null, fallbackIndex?: number, rect: Rect, text: string }} WordEditor */
/** @typedef {Object} ScribeTextOverlayProps
 * @property {IIIFAnnotationPage | null} annotationPage
 * @property {string} canvasId
 * @property {string} selectedAnnotationId
 * @property {OpenSeadragon.Viewer | null | undefined} viewer
 * @property {string} windowId
 */
/** @typedef {{ windowId: string }} WindowOwnProps */

/** @param {OpenSeadragon.Viewer | null | undefined} viewer @returns {{ height: number, width: number } | null} */
function viewerImageSize(viewer) {
  const contentSize = viewer?.world?.getItemAt?.(0)?.getContentSize?.();
  return contentSize ? { height: contentSize.y, width: contentSize.x } : null;
}

/** @param {OpenSeadragon.Viewer | null | undefined} viewer @param {IIIFAnnotation} annotation @returns {Rect | null} */
function annotationRect(viewer, annotation) {
  if (!viewer?.viewport || !viewer?.world?.getItemCount?.()) return null;
  const tiledImage = viewer.world.getItemAt(0);
  if (!tiledImage?.imageToViewportCoordinates) return null;
  const { x, y, w, h } = annotationBBox(annotation, viewerImageSize(viewer));
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
function visibleImageBounds(viewer, paddingRatio = 0.1) {
  if (!viewer?.viewport || !viewer?.world?.getItemCount?.()) return null;
  const tiledImage = viewer.world.getItemAt(0);
  const viewportBounds = viewer.viewport.getBounds?.(true);
  const imageBounds = tiledImage?.viewportToImageRectangle?.(viewportBounds);
  if (!imageBounds) return null;
  const paddingX = imageBounds.width * paddingRatio;
  const paddingY = imageBounds.height * paddingRatio;
  return {
    h: imageBounds.height + paddingY * 2,
    w: imageBounds.width + paddingX * 2,
    x: imageBounds.x - paddingX,
    y: imageBounds.y - paddingY,
  };
}

/** @param {Rect} inner @param {Rect} outer @returns {boolean} */
function rectIsWithin(inner, outer) {
  return inner.x >= outer.x
    && inner.y >= outer.y
    && inner.x + inner.w <= outer.x + outer.w
    && inner.y + inner.h <= outer.y + outer.h;
}

/** @param {number} total @returns {number} */
function transcriptionSegmentDisplayDuration(total) {
  const boundedTotal = Number.isSafeInteger(total) && total > 0
    ? Math.min(total, TRANSCRIPTION_SEGMENT_QUEUE_LIMIT)
    : 1;
  return Math.min(
    TRANSCRIPTION_SEGMENT_MAX_DISPLAY_MS,
    Math.max(
      TRANSCRIPTION_SEGMENT_MIN_DISPLAY_MS,
      Math.floor(TRANSCRIPTION_SEGMENT_MAX_TOTAL_DISPLAY_MS / boundedTotal),
    ),
  );
}

/** @param {number} canvasWidth @returns {number} */
export function transcriptPaneWidth(canvasWidth) {
  const width = Number.isFinite(canvasWidth) ? canvasWidth : 0;
  return Math.round(Math.max(
    0,
    Math.min(Math.max(0, width - 120), Math.max(TRANSCRIPT_PANE_MIN_WIDTH_PX, width * TRANSCRIPT_PANE_WIDTH_RATIO)),
  ));
}

/**
 * @param {{ id: string, isWord: boolean }} target
 * @param {string} windowId
 * @param {string} canvasId
 * @param {OverlayMode} [overlayMode]
 */
function dispatchOverlaySelection(target, windowId, canvasId, overlayMode = 'edit') {
  document.dispatchEvent(new CustomEvent('scribe:select-annotation', {
    detail: {
      annotationId: target.id,
      canvasId,
      focusAnnotationId: target.isWord ? target.id : '',
      overlayMode,
      windowId,
    },
  }));
}

/**
 * @param {string} annotationId
 * @param {Rect} bbox
 * @param {'move' | 'resize'} operation
 * @param {string} windowId
 * @param {string} canvasId
 */
function dispatchGeometryChange(annotationId, bbox, operation, windowId, canvasId) {
  document.dispatchEvent(new CustomEvent('scribe:resize-annotation', {
    detail: {
      annotationId,
      bbox: normalizeImageBBox(bbox),
      canvasId,
      operation,
      windowId,
    },
  }));
}

/** @param {Rect} bbox @param {Rect | null} container @returns {Rect} */
function containBBox(bbox, container) {
  return container ? clampBBoxWithin(bbox, container) : normalizeImageBBox(bbox);
}

/** @param {ScribeTextOverlayProps} props */
export function ScribeTextOverlayPlugin({
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
  const inlineFocusKeyRef = useRef('');
  const resumeInlineFocusRef = useRef(false);
  const transcriptFocusKeyRef = useRef('');
  const transcriptInputRefs = useRef(/** @type {Map<string, HTMLInputElement>} */ (new Map()));
  const resizeHandleRefs = useRef(/** @type {Map<string, HTMLButtonElement>} */ (new Map()));
  const dragIntentRef = useRef(/** @type {{ timeoutId: number } | null} */ (null));
  const hitPressRef = useRef(/** @type {HitPressState | null} */ (null));
  const transcriptionSegmentClearPendingRef = useRef(false);
  const transcriptionSegmentQueueRef = useRef(/** @type {TranscriptionSegment[]} */ ([]));
  const transcriptionSegmentTimerRef = useRef(/** @type {number | null} */ (null));
  const transcriptionResultTimerRef = useRef(/** @type {number | null} */ (null));
  const transcriptionFocusKeyRef = useRef('');
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

  // Editor controls opt out of viewer gestures instead of disabling mouse
  // navigation, so the page stays pannable and zoomable while editing.
  useEffect(() => installViewerGestureGuard(viewer), [viewer]);

  useEffect(() => {
    setEditorState(null);
    setPendingResizeFocus(null);
    setPendingFocusWordId('');
    setBboxDragState(null);
    hitPressRef.current = null;
    setTranscriptionSegment(null);
    setTranscriptionResult(null);
    transcriptionFocusKeyRef.current = '';
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
    /** @param {TranscriptionSegment} segment */
    const showSegment = (segment) => {
      setTranscriptionSegment(segment);
      transcriptionSegmentTimerRef.current = window.setTimeout(() => {
        transcriptionSegmentTimerRef.current = null;
        const next = transcriptionSegmentQueueRef.current.shift();
        if (next) {
          showSegment(next);
          return;
        }
        if (transcriptionSegmentClearPendingRef.current) {
          transcriptionSegmentClearPendingRef.current = false;
          setTranscriptionSegment(null);
        }
      }, transcriptionSegmentDisplayDuration(segment.total));
    };

    /** @param {Event} event */
    const handle = (event) => {
      const detail = /** @type {CustomEvent<TranscriptionEventDetail>} */ (event).detail;
      if (detail?.windowId !== windowId || detail.canvasId !== canvasId) return;
      const {
        annotation, attemptNumber = 0, done = 0, jobId = '', total = 0,
      } = detail;
      if (!annotation) {
        transcriptionSegmentClearPendingRef.current = true;
        if (
          transcriptionSegmentTimerRef.current === null
          && transcriptionSegmentQueueRef.current.length === 0
        ) {
          transcriptionSegmentClearPendingRef.current = false;
          setTranscriptionSegment(null);
        }
        return;
      }
      const segment = { annotation, attemptNumber, done, jobId, total };
      transcriptionSegmentClearPendingRef.current = false;
      if (transcriptionSegmentTimerRef.current === null) {
        showSegment(segment);
        return;
      }
      if (transcriptionSegmentQueueRef.current.length < TRANSCRIPTION_SEGMENT_QUEUE_LIMIT) {
        transcriptionSegmentQueueRef.current.push(segment);
      }
    };
    document.addEventListener('scribe:transcription-segment', handle);
    return () => {
      if (transcriptionSegmentTimerRef.current !== null) {
        window.clearTimeout(transcriptionSegmentTimerRef.current);
        transcriptionSegmentTimerRef.current = null;
      }
      transcriptionSegmentQueueRef.current.length = 0;
      transcriptionSegmentClearPendingRef.current = false;
      document.removeEventListener('scribe:transcription-segment', handle);
    };
  }, [canvasId, windowId]);

  useEffect(() => {
    /** @param {Event} event */
    const handle = (event) => {
      const detail = /** @type {CustomEvent<TranscriptionEventDetail>} */ (event).detail;
      if (detail?.windowId !== windowId || detail.canvasId !== canvasId) return;
      const {
        annotation, attemptNumber = 0, done = 0, jobId = '', total = 0,
      } = detail;
      if (!annotation) return;
      const text = annotationText(annotation) || null;
      setTranscriptionResult({
        annotation, attemptNumber, text, done, jobId, total,
      });
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
    if (!canvasId || !windowId) return;
    // This effect is deliberately declared after both transcription event
    // listeners. The shell may now replay a durable current-segment snapshot
    // without racing this overlay's mount.
    document.dispatchEvent(new CustomEvent('scribe:transcription-overlay-state', {
      detail: { canvasId, ready: true, windowId },
    }));
    return () => {
      document.dispatchEvent(new CustomEvent('scribe:transcription-overlay-state', {
        detail: { canvasId, ready: false, windowId },
      }));
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
      const {
        annotationId, containerBBox, handle, originalBBox, startClientX, startClientY,
      } = bboxDragState;
      const startPt = screenToImagePoint(startClientX, startClientY);
      const endPt = screenToImagePoint(event.clientX, event.clientY);
      setBboxDragState(null);
      if (!startPt || !endPt) return;
      const dx = endPt.x - startPt.x;
      const dy = endPt.y - startPt.y;
      if (Math.round(dx) === 0 && Math.round(dy) === 0) return;
      const next = handle === 'move'
        ? translateBBox(originalBBox, dx, dy)
        : resizeBBoxFromHandle(originalBBox, handle, dx, dy);
      dispatchGeometryChange(
        annotationId,
        containBBox(next, containerBBox),
        handle === 'move' ? 'move' : 'resize',
        windowId,
        canvasId,
      );
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

  // Hit targets let a drag pan the image, so the selection is committed only
  // on a release that did not travel. Pointer capture by the viewer retargets
  // the release, which is why this listens on the window rather than on the
  // target element.
  useEffect(() => {
    /** @param {PointerEvent} event */
    const handleUp = (event) => {
      const press = hitPressRef.current;
      if (!press || press.pointerId !== event.pointerId) return;
      hitPressRef.current = null;
      if (!isClickGesture(press, { x: event.clientX, y: event.clientY })) return;
      if (press.isWord) setPendingFocusWordId(press.annotationId);
      dispatchOverlaySelection(
        { id: press.annotationId, isWord: press.isWord },
        windowId,
        canvasId,
        press.overlayMode,
      );
    };
    const handleCancel = () => {
      hitPressRef.current = null;
    };
    window.addEventListener('pointerup', handleUp);
    window.addEventListener('pointercancel', handleCancel);
    return () => {
      window.removeEventListener('pointerup', handleUp);
      window.removeEventListener('pointercancel', handleCancel);
    };
  }, [canvasId, windowId]);

  const activePage = editorState?.annotationPage || annotationPage;
  const activeSelectedAnnotationId = editorState?.selectedAnnotationId || selectedAnnotationId;
  const activeFocusedWordAnnotationId = editorState?.focusedWordAnnotationId || '';
  const overlayMode = editorState?.overlayMode || 'none';
  const editorIsBusy = Boolean(editorState?.isBusy);
  const inlineEditorVisible = overlayMode === 'edit';
  const textOverlayVisible = overlayMode === 'read';
  const outlineVisible = overlayMode === 'outline';
  const transcriptVisible = overlayMode === 'transcript';
  const canvasWidth = viewer?.canvas?.clientWidth || 0;
  const paneWidth = transcriptVisible ? transcriptPaneWidth(canvasWidth) : 0;

  // Fit the full page into the viewport's inner area when entering transcript
  // mode, loading a Canvas, or resizing. User pan/zoom does not trigger a fit.
  useEffect(() => {
    if (!transcriptVisible || !viewer?.viewport?.setMargins) return undefined;
    const fit = () => {
      const width = viewer.canvas?.clientWidth || viewer.element?.clientWidth || 0;
      viewer.viewport.setMargins({ right: transcriptPaneWidth(width) });
      const image = viewer.world?.getItemCount() ? viewer.world.getItemAt(0) : null;
      const bounds = image?.getBounds?.();
      if (bounds) viewer.viewport.fitBounds(bounds, true);
      setVersion((value) => value + 1);
    };
    fit();
    viewer.addHandler('open', fit);
    const observer = typeof ResizeObserver !== 'undefined' ? new ResizeObserver(fit) : null;
    if (viewer.element || viewer.canvas) observer?.observe(viewer.element || viewer.canvas);
    return () => {
      observer?.disconnect();
      viewer.removeHandler('open', fit);
      viewer.viewport?.setMargins?.({});
    };
  }, [canvasId, transcriptVisible, viewer]);

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

  useEffect(() => {
    if (!transcriptionSegment?.annotation || !viewer) return;
    const visibleBounds = visibleImageBounds(viewer, 0);
    if (!visibleBounds) return;
    const bbox = annotationBBox(transcriptionSegment.annotation, viewerImageSize(viewer));
    if (bbox.w <= 0 || bbox.h <= 0 || rectIsWithin(bbox, visibleBounds)) return;
    const focusKey = [
      transcriptionSegment.jobId,
      transcriptionSegment.attemptNumber,
      transcriptionSegment.done,
      transcriptionSegment.annotation.id,
    ].join(':');
    if (transcriptionFocusKeyRef.current === focusKey) return;
    transcriptionFocusKeyRef.current = focusKey;
    document.dispatchEvent(new CustomEvent('scribe:focus-annotation', {
      detail: {
        annotationId: transcriptionSegment.annotation.id,
        bbox,
        canvasId,
        windowId,
      },
    }));
  }, [canvasId, transcriptionSegment, viewer, version, windowId]);

  const transcriptionResultRect = useMemo(() => {
    if (!transcriptionResult?.annotation || !viewer) return null;
    return annotationRect(viewer, transcriptionResult.annotation);
  }, [transcriptionResult, viewer, version]);

  const visibleItems = /** @type {IIIFAnnotation[]} */ (useMemo(() => {
    const visibleBounds = visibleImageBounds(viewer);
    return (Array.isArray(activePage?.items) ? activePage.items : [])
      .filter((annotation) => isLineAnnotation(annotation) || isWordAnnotation(annotation))
      .filter((annotation) => annotationIntersectsImageRect(annotation, visibleBounds));
  }, [activePage, viewer, version]));

  const labels = /** @type {OverlayLabel[]} */ (useMemo(() => {
    if (!textOverlayVisible) return [];
    return groupAnnotationsForEditor({ ...activePage, items: visibleItems })
      .flatMap((row) => (row.granularity === 'word' ? row.fields : [row.lead || row.fields[0]]))
      .map((annotation) => ({
        id: annotation?.id,
        isWord: isWordAnnotation(annotation),
        rect: annotationRect(viewer, annotation),
        text: annotationText(annotation),
      }))
      .filter((item) => item.id && item.text && item.rect && item.rect.w > 4 && item.rect.h > 4);
  }, [activePage, textOverlayVisible, viewer, visibleItems, version]));

  // Every word and line is a click target in every mode except "read", where
  // the text labels already are. A drag that starts on one still pans.
  const hitTargets = /** @type {OverlayLabel[]} */ (useMemo(() => {
    if (textOverlayVisible || editorIsBusy) return [];
    return visibleItems
      .map((annotation) => ({
        id: annotation?.id,
        isWord: isWordAnnotation(annotation),
        rect: annotationRect(viewer, annotation),
        text: annotationText(annotation),
      }))
      .filter((item) => item.id && item.rect && item.rect.w > 4 && item.rect.h > 4);
  }, [editorIsBusy, textOverlayVisible, viewer, visibleItems, version]));

  const granularityMarkers = /** @type {GranularityMarker[]} */ (useMemo(() => {
    if (overlayMode === 'none') return [];
    return visibleItems
      .map((annotation) => ({
        granularity: isWordAnnotation(annotation) ? 'word' : 'line',
        id: annotation.id,
        rect: annotationRect(viewer, annotation),
        selected: annotation.id === activeSelectedAnnotationId
          || annotation.id === activeFocusedWordAnnotationId,
      }))
      .filter((marker) => marker.id && marker.rect && marker.rect.w > 4 && marker.rect.h > 4);
  }, [activeFocusedWordAnnotationId, activeSelectedAnnotationId, overlayMode, viewer, visibleItems, version]));

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

  // The geometry handles follow the focused word when there is one, otherwise
  // the selected line. Words stay inside their owning line; lines stay inside
  // the canonical image.
  const geometryTarget = /** @type {GeometryTarget | null} */ (useMemo(() => {
    if (!inlineEditorVisible || !viewer) return null;
    const items = Array.isArray(activePage?.items) ? activePage.items : [];
    const word = items.find((annotation) => annotation?.id === activeFocusedWordAnnotationId) || null;
    if (word && isWordAnnotation(word)) {
      const rect = annotationRect(viewer, word);
      if (!rect) return null;
      const owner = selectedDecoration.lineAnnotation
        || lineAnnotationForSelection(activePage, word);
      return {
        annotation: word,
        containerBBox: owner ? annotationBBox(owner) : imageBoundsBBox(viewerImageSize(viewer)),
        isWord: true,
        rect,
      };
    }
    const line = selectedDecoration.lineAnnotation;
    if (!line?.id) return null;
    const rect = annotationRect(viewer, line);
    if (!rect) return null;
    return {
      annotation: line,
      containerBBox: imageBoundsBBox(viewerImageSize(viewer)),
      isWord: false,
      rect,
    };
  }, [activeFocusedWordAnnotationId, activePage, inlineEditorVisible, selectedDecoration.lineAnnotation, viewer, version]));

  const transcriptRows = /** @type {TranscriptRow[]} */ (useMemo(() => {
    if (!transcriptVisible) return [];
    return transcriptRowsInPageOrder(activePage)
      .map((row) => {
        const id = rowSelectionId(row);
        const wordIds = row.granularity === 'word'
          ? row.fields.map((annotation) => String(annotation.id || '')).filter(Boolean)
          : [];
        return {
          id,
          selected: id === activeSelectedAnnotationId
            || wordIds.includes(activeSelectedAnnotationId)
            || wordIds.includes(activeFocusedWordAnnotationId),
          text: rowText(row),
          wordIds,
        };
      })
      .filter((row) => row.id);
  }, [activeFocusedWordAnnotationId, activePage, activeSelectedAnnotationId, transcriptVisible]));

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
      resumeInlineFocusRef.current = true;
      const focused = document.activeElement;
      if (focused instanceof HTMLInputElement
        && Array.from(inputRefs.current.values()).includes(focused)) focused.blur();
      return;
    }
    if (overlayMode !== 'edit' || !inlineEditor) {
      inlineFocusKeyRef.current = '';
      resumeInlineFocusRef.current = false;
      return;
    }
    const resumeFocus = resumeInlineFocusRef.current;
    resumeInlineFocusRef.current = false;
    const focusKey = `${canvasId}\u0000${activeSelectedAnnotationId}\u0000${activeFocusedWordAnnotationId}`;
    // Viewport animation and text changes also recreate inlineEditor. Only a
    // new selection may take focus back from the toolbar or shell controls.
    if (!resumeFocus && !pendingFocusWordId && inlineFocusKeyRef.current === focusKey) return;
    inlineFocusKeyRef.current = focusKey;
    // Don't steal focus from an editor control after a later state update. In
    // particular, keyboard line creation focuses a resize handle before every
    // related editor-state update has finished rendering.
    if (!resumeFocus && !pendingFocusWordId) {
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
  }, [activeFocusedWordAnnotationId, activeSelectedAnnotationId, canvasId, editorIsBusy, inlineEditor, overlayMode, pendingFocusWordId]);

  // In transcript mode a selection made on the image moves focus to the
  // matching transcript row, unless a transcript row already owns focus.
  useEffect(() => {
    if (!transcriptVisible) {
      transcriptFocusKeyRef.current = '';
      return;
    }
    if (editorIsBusy) return;
    const selection = pendingFocusWordId || activeFocusedWordAnnotationId || activeSelectedAnnotationId;
    const focusKey = `${canvasId}:${selection}`;
    if (!selection) {
      transcriptFocusKeyRef.current = '';
      return;
    }
    if (transcriptFocusKeyRef.current === focusKey) return;
    const row = transcriptRows.find((candidate) => (
      candidate.id === selection || candidate.wordIds.includes(selection)
    ));
    const target = row ? transcriptInputRefs.current.get(row.id) : null;
    if (!(target instanceof HTMLInputElement)) return;
    transcriptFocusKeyRef.current = focusKey;
    target.scrollIntoView?.({ block: 'nearest' });
    target.focus({ preventScroll: true });
    setPendingFocusWordId('');
  }, [activeFocusedWordAnnotationId, activeSelectedAnnotationId, canvasId, editorIsBusy, pendingFocusWordId, transcriptRows, transcriptVisible]);

  useEffect(() => {
    if (!pendingResizeFocus || editorIsBusy || !inlineEditorVisible) return;
    if (geometryTarget?.annotation?.id !== pendingResizeFocus.annotationId) return;
    const target = resizeHandleRefs.current.get(pendingResizeFocus.handle);
    if (!(target instanceof HTMLButtonElement)) return;
    target.focus();
    setPendingResizeFocus(null);
  }, [editorIsBusy, geometryTarget, inlineEditorVisible, pendingResizeFocus]);

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

  /**
   * @param {import('react').KeyboardEvent<HTMLElement>} event
   * @param {BBoxHandle | 'move'} handle
   */
  function nudgeGeometryTarget(event, handle) {
    if (editorIsBusy || !geometryTarget) return;
    if (!['ArrowLeft', 'ArrowRight', 'ArrowUp', 'ArrowDown'].includes(event.key)) return;
    event.preventDefault();
    event.stopPropagation();
    const step = event.shiftKey ? KEYBOARD_NUDGE_LARGE_PX : KEYBOARD_NUDGE_PX;
    const dx = event.key === 'ArrowLeft' ? -step : event.key === 'ArrowRight' ? step : 0;
    const dy = event.key === 'ArrowUp' ? -step : event.key === 'ArrowDown' ? step : 0;
    const original = annotationBBox(geometryTarget.annotation);
    const next = handle === 'move'
      ? translateBBox(original, dx, dy)
      : resizeBBoxFromHandle(original, handle, dx, dy);
    dispatchGeometryChange(
      String(geometryTarget.annotation.id || ''),
      containBBox(next, geometryTarget.containerBBox),
      handle === 'move' ? 'move' : 'resize',
      windowId,
      canvasId,
    );
  }

  /**
   * @param {import('react').PointerEvent<HTMLElement>} event
   * @param {BBoxHandle | 'move'} handle
   */
  function beginGeometryDrag(event, handle) {
    event.stopPropagation();
    event.preventDefault();
    if (editorIsBusy || !geometryTarget?.annotation?.id) return;
    setBboxDragState({
      annotationId: geometryTarget.annotation.id,
      containerBBox: geometryTarget.containerBBox,
      currentClientX: event.clientX,
      currentClientY: event.clientY,
      handle,
      originalBBox: annotationBBox(geometryTarget.annotation),
      startClientX: event.clientX,
      startClientY: event.clientY,
    });
  }

  /** @param {import('react').PointerEvent<HTMLElement>} event @param {OverlayLabel} target */
  function pressHitTarget(event, target) {
    if (editorIsBusy || !event.isPrimary) return;
    hitPressRef.current = {
      annotationId: target.id,
      isWord: target.isWord,
      overlayMode: transcriptVisible ? 'transcript' : 'edit',
      pointerId: event.pointerId,
      x: event.clientX,
      y: event.clientY,
    };
  }

  /** @param {number} index @param {number} direction */
  function focusTranscriptRow(index, direction) {
    if (transcriptRows.length === 0) return;
    const nextIndex = (index + direction + transcriptRows.length) % transcriptRows.length;
    const row = transcriptRows[nextIndex];
    if (!row) return;
    dispatchOverlaySelection({ id: row.id, isWord: false }, windowId, canvasId, 'transcript');
    setPendingFocusWordId(row.id);
  }

  if (!viewer) return null;
  if (overlayMode === 'none'
    && granularityMarkers.length === 0
    && hitTargets.length === 0
    && !transcriptionRect
    && !transcriptionResultRect) return null;

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
      data-scribe-overlay-mode={overlayMode}
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
      {hitTargets.map((target) => (
        <button
          aria-hidden="true"
          key={`hit-${target.id}`}
          tabIndex={-1}
          type="button"
          data-scribe-hit={target.isWord ? 'word' : 'line'}
          data-scribe-hit-id={target.id}
          title={target.text ? `${target.isWord ? 'Word' : 'Line'}: ${target.text}` : undefined}
          onPointerDown={(event) => pressHitTarget(event, target)}
          style={{
            background: 'transparent',
            border: 0,
            cursor: 'text',
            height: target.rect.h,
            left: target.rect.x,
            margin: 0,
            padding: 0,
            pointerEvents: 'auto',
            position: 'absolute',
            top: target.rect.y,
            touchAction: 'none',
            width: target.rect.w,
            zIndex: target.isWord ? 9 : 8,
          }}
        />
      ))}
      {granularityMarkers.map(({ granularity, id: markerId, rect, selected }) => {
        const isWord = granularity === 'word';
        const boundaryColor = isWord ? scribeTheme.word : scribeTheme.line;
        return (
          <div
            aria-hidden="true"
            data-scribe-granularity={granularity}
            key={`granularity-${markerId}`}
            style={{
              border: `${selected ? 2 : 1}px ${isWord ? 'solid' : 'dashed'} ${boundaryColor}`,
              borderRadius: isWord ? 3 : 5,
              boxSizing: 'border-box',
              height: `${rect.h}px`,
              left: rect.x,
              opacity: selected || outlineVisible ? 1 : 0.62,
              pointerEvents: 'none',
              position: 'absolute',
              top: rect.y,
              width: `${rect.w}px`,
              zIndex: selected ? 14 : (isWord ? 12 : 10),
            }}
          >
            {rect.w >= 24 && rect.h >= 12 ? (
              <span
                style={{
                  background: isWord ? scribeTheme.wordSurface : scribeTheme.lineSurface,
                  borderRadius: '0 0 0 3px',
                  color: boundaryColor,
                  fontSize: 9,
                  fontWeight: 800,
                  lineHeight: 1,
                  padding: '2px 3px',
                  position: 'absolute',
                  right: 0,
                  top: 0,
                }}
              >
                {isWord ? 'W' : 'L'}
              </span>
            ) : null}
          </div>
        );
      })}
      {focusBounds ? (
        <>
          <div
            style={{
              background: scribeTheme.overlay,
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
              background: scribeTheme.overlay,
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
              background: scribeTheme.overlay,
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
              background: scribeTheme.overlay,
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
      {geometryTarget ? (() => {
        const tr = geometryTarget.rect;
        const dragDx = bboxDragState ? (bboxDragState.currentClientX - bboxDragState.startClientX) : 0;
        const dragDy = bboxDragState ? (bboxDragState.currentClientY - bboxDragState.startClientY) : 0;
        const dragHandle = bboxDragState?.handle || '';
        const moving = dragHandle === 'move';
        const previewRect = bboxDragState ? {
          x: tr.x + (moving || dragHandle.endsWith('w') ? dragDx : 0),
          y: tr.y + (moving || dragHandle.startsWith('n') ? dragDy : 0),
          w: Math.max(8, tr.w + (!moving && dragHandle.endsWith('e') ? dragDx : !moving && dragHandle.endsWith('w') ? -dragDx : 0)),
          h: Math.max(8, tr.h + (!moving && dragHandle.startsWith('s') ? dragDy : !moving && dragHandle.startsWith('n') ? -dragDy : 0)),
        } : { x: tr.x, y: tr.y, w: tr.w, h: tr.h };
        const targetKind = geometryTarget.isWord ? 'word' : 'line';
        const targetText = annotationText(geometryTarget.annotation) || 'empty text';
        const accent = geometryTarget.isWord ? scribeTheme.word : scribeTheme.line;

        /** @type {Array<{ handle: BBoxHandle, cx: number, cy: number, cursor: string }>} */
        const corners = [
          { handle: 'nw', cx: previewRect.x, cy: previewRect.y, cursor: 'nw-resize' },
          { handle: 'ne', cx: previewRect.x + previewRect.w, cy: previewRect.y, cursor: 'ne-resize' },
          { handle: 'sw', cx: previewRect.x, cy: previewRect.y + previewRect.h, cursor: 'sw-resize' },
          { handle: 'se', cx: previewRect.x + previewRect.w, cy: previewRect.y + previewRect.h, cursor: 'se-resize' },
        ];
        return (
          <>
            <div
              data-scribe-geometry-target={targetKind}
              data-scribe-geometry-target-id={geometryTarget.annotation.id}
              style={{
                border: `2px dashed ${accent}`,
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
            <button
              aria-label={`Move ${targetKind}: ${targetText}`}
              aria-keyshortcuts="ArrowUp ArrowDown ArrowLeft ArrowRight"
              data-scribe-interactive="true"
              data-scribe-move-handle={targetKind}
              type="button"
              disabled={editorIsBusy}
              ref={(node) => {
                if (node) resizeHandleRefs.current.set('move', node);
                else resizeHandleRefs.current.delete('move');
              }}
              style={{
                alignItems: 'center',
                background: scribeTheme.surface,
                border: `2px solid ${accent}`,
                borderRadius: 999,
                boxShadow: `0 4px 10px ${scribeTheme.shadowSoft}`,
                boxSizing: 'border-box',
                color: accent,
                cursor: moving ? 'grabbing' : 'grab',
                display: 'flex',
                fontSize: 11,
                fontWeight: 800,
                gap: 4,
                height: 24,
                justifyContent: 'center',
                left: previewRect.x + previewRect.w / 2 - 34,
                lineHeight: 1,
                padding: '0 8px',
                pointerEvents: 'auto',
                position: 'absolute',
                top: Math.max(0, previewRect.y - 30),
                touchAction: 'none',
                width: 68,
                zIndex: 25,
              }}
              onPointerDown={(event) => beginGeometryDrag(event, 'move')}
              onKeyDown={(event) => nudgeGeometryTarget(event, 'move')}
            >
              <svg width="12" height="12" viewBox="0 0 24 24" fill="currentColor" aria-hidden="true">
                <path d="M12 2l4 4h-3v4h4V7l4 4-4 4v-3h-4v4h3l-4 4-4-4h3v-4H7v3l-4-4 4-4v3h4V6H8z" />
              </svg>
              Move
            </button>
            {corners.map(({ handle, cx, cy, cursor }) => (
              <button
                aria-label={`Resize ${targetKind} from the ${handle} corner`}
                aria-keyshortcuts="ArrowUp ArrowDown ArrowLeft ArrowRight"
                data-scribe-interactive="true"
                data-scribe-resize-handle={handle}
                key={handle}
                type="button"
                disabled={editorIsBusy}
                ref={(node) => {
                  if (node) resizeHandleRefs.current.set(handle, node);
                  else resizeHandleRefs.current.delete(handle);
                }}
                style={{
                  background: `radial-gradient(circle, ${scribeTheme.surface} 0 3px, ${accent} 4px 5px, transparent 6px)`,
                  border: 0,
                  borderRadius: '50%',
                  boxSizing: 'border-box',
                  cursor,
                  height: HANDLE_SIZE_PX,
                  left: cx - HANDLE_SIZE_PX / 2,
                  pointerEvents: 'auto',
                  padding: 0,
                  position: 'absolute',
                  top: cy - HANDLE_SIZE_PX / 2,
                  touchAction: 'none',
                  width: HANDLE_SIZE_PX,
                  zIndex: 25,
                }}
                onPointerDown={(event) => beginGeometryDrag(event, handle)}
                onKeyDown={(event) => nudgeGeometryTarget(event, handle)}
              />
            ))}
          </>
        );
      })() : null}
      {labels.map((label) => (
        <button
          aria-label={`Edit ${label.isWord ? 'word' : 'line'}: ${label.text || 'empty text'}`}
          data-scribe-interactive="true"
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
            background: label.id === activeFocusedWordAnnotationId || label.id === activeSelectedAnnotationId ? scribeTheme.selected : scribeTheme.overlaySurface,
            border: `1px solid ${label.isWord ? scribeTheme.word : scribeTheme.line}`,
            borderRadius: 4,
            boxSizing: 'border-box',
            color: label.id === activeFocusedWordAnnotationId || label.id === activeSelectedAnnotationId
              ? scribeTheme.selectedForeground
              : scribeTheme.overlayForeground,
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
          data-scribe-inline-editor="true"
          data-scribe-interactive="true"
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
                background: scribeTheme.surface,
                border: `1px solid ${scribeTheme.border}`,
                borderRadius: 999,
                height: '6px',
                width: '72px',
              }}
            />
          </div>
          <div
            style={{
              background: scribeTheme.surface,
              borderRadius: 10,
              boxShadow: `0 14px 24px ${scribeTheme.shadow}`,
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
                  background: scribeTheme.surface,
                  border: `2px solid ${scribeTheme.word}`,
                  borderRadius: 8,
                  boxShadow: `0 8px 16px ${scribeTheme.shadowSoft}`,
                  color: scribeTheme.foreground,
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
                    background: scribeTheme.surface,
                    border: `2px solid ${scribeTheme.word}`,
                    borderRadius: 8,
                    boxShadow: `0 8px 16px ${scribeTheme.shadowSoft}`,
                    color: scribeTheme.foreground,
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
      {transcriptVisible ? (
        <section
          aria-label="Transcript"
          data-scribe-interactive="true"
          data-scribe-transcript-pane="true"
          style={{
            background: scribeTheme.surface,
            borderLeft: `1px solid ${scribeTheme.border}`,
            boxShadow: `-8px 0 20px ${scribeTheme.shadowSoft}`,
            boxSizing: 'border-box',
            height: '100%',
            overflowY: 'auto',
            pointerEvents: 'auto',
            position: 'absolute',
            right: 0,
            top: 0,
            width: paneWidth,
            zIndex: 210,
          }}
        >
          {transcriptRows.length === 0 ? (
            <p
              style={{
                color: scribeTheme.mutedForeground,
                fontFamily: '"IBM Plex Sans", "Helvetica Neue", sans-serif',
                fontSize: 13,
                margin: 0,
                padding: 16,
              }}
            >
              No transcribed lines on this page.
            </p>
          ) : null}
          {transcriptRows.map((row, index) => {
            const rowHeight = TRANSCRIPT_ROW_MIN_HEIGHT_PX;
            return (
              <input
                aria-label={`Transcript line ${index + 1}: ${row.text || 'empty text'}`}
                data-scribe-transcript-row={row.id}
                disabled={editorIsBusy}
                key={row.id}
                ref={(node) => {
                  if (node) transcriptInputRefs.current.set(row.id, node);
                  else transcriptInputRefs.current.delete(row.id);
                }}
                value={row.text}
                onFocus={() => {
                  if (row.selected) return;
                  dispatchOverlaySelection({ id: row.id, isWord: false }, windowId, canvasId, 'transcript');
                }}
                onChange={(event) => {
                  if (editorIsBusy) return;
                  document.dispatchEvent(new CustomEvent('scribe:inline-change-text', {
                    detail: {
                      annotationId: row.id,
                      canvasId,
                      selectionStart: event.target.selectionStart,
                      text: event.target.value,
                      windowId,
                    },
                  }));
                }}
                onKeyDown={(event) => {
                  if (editorIsBusy) return;
                  if (event.key === 'Enter' && (event.metaKey || event.ctrlKey)) {
                    event.preventDefault();
                    document.dispatchEvent(new CustomEvent('scribe:inline-save', {
                      detail: { canvasId, windowId },
                    }));
                    return;
                  }
                  if (event.key === 'Enter' || event.key === 'ArrowDown') {
                    event.preventDefault();
                    focusTranscriptRow(index, 1);
                    return;
                  }
                  if (event.key === 'ArrowUp') {
                    event.preventDefault();
                    focusTranscriptRow(index, -1);
                    return;
                  }
                  if (event.key === 'Tab') {
                    event.preventDefault();
                    focusTranscriptRow(index, event.shiftKey ? -1 : 1);
                  }
                }}
                style={{
                  background: row.selected ? scribeTheme.selected : 'transparent',
                  border: 0,
                  borderBottom: `1px solid ${scribeTheme.border}`,
                  borderLeft: `3px solid ${row.selected ? scribeTheme.word : 'transparent'}`,
                  boxSizing: 'border-box',
                  color: row.selected ? scribeTheme.selectedForeground : scribeTheme.foreground,
                  fontFamily: TRANSCRIPT_FONT_FAMILY,
                  fontSize: `${TRANSCRIPT_FONT_SIZE_PX}px`,
                  fontWeight: 500,
                  height: `${rowHeight}px`,
                  left: 0,
                  lineHeight: 1.1,
                  margin: 0,
                  outline: 'none',
                  padding: '0 10px',
                  display: 'block',
                  position: 'relative',
                  width: '100%',
                }}
              />
            );
          })}
        </section>
      ) : null}
      {outlineRects.map(({ id, rect }) => (
        <div
          key={id}
          style={{
            border: `1px solid ${scribeTheme.line}`,
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
              background: scribeTheme.overlaySurface,
              borderRadius: 3,
              boxSizing: 'border-box',
              color: scribeTheme.overlayForeground,
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
        const badgeWidth = 72;
        const badgeLeft = tr.x + tr.w + 8;
        const clampedBadgeLeft = Math.min(badgeLeft, (canvasWidth || 9999) - badgeWidth - 4);
        return (
          <>
            <div
              style={{
                animation: 'scribeSegmentPulse 1.4s ease-in-out infinite',
                border: `2px solid ${scribeTheme.transcribe}`,
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
              aria-label={`Automatic transcription: line ${transcriptionSegment.done} of ${transcriptionSegment.total}`}
              data-scribe-transcription-active="true"
              data-scribe-transcription-annotation={transcriptionSegment.annotation?.id || ''}
              data-scribe-transcription-attempt={transcriptionSegment.attemptNumber}
              data-scribe-transcription-job={transcriptionSegment.jobId}
              data-scribe-transcription-line={transcriptionSegment.done}
              data-scribe-transcription-total={transcriptionSegment.total}
              role="status"
              style={{
                alignItems: 'center',
                background: scribeTheme.transcribe,
                backdropFilter: 'blur(6px)',
                borderRadius: 20,
                boxShadow: `0 2px 10px ${scribeTheme.shadow}`,
                color: scribeTheme.overlayForeground,
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
              <svg width="12" height="12" viewBox="0 0 24 24" fill="currentColor" aria-hidden="true">
                <path d="M7.5 5.6L10 7 8.6 4.5 10 2 7.5 3.4 5 2l1.4 2.5L5 7zm12 9.8L17 14l1.4 2.5L17 19l2.5-1.4L22 19l-1.4-2.5L22 14zM22 2l-2.5 1.4L17 2l1.4 2.5L17 7l2.5-1.4L22 7l-1.4-2.5zm-7.63 5.29a1 1 0 00-1.41 0L1.29 18.96a1 1 0 000 1.41l2.34 2.34c.39.39 1.02.39 1.41 0L16.7 11.05a1 1 0 000-1.41zM14.21 7L17 9.79 9.79 17 7 14.21z" />
              </svg>
              <div
                style={{
                  animation: 'scribeSpinner 0.75s linear infinite',
                  border: `1.5px solid ${scribeTheme.overlayForeground}`,
                  borderRadius: '50%',
                  borderTopColor: scribeTheme.surface,
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
