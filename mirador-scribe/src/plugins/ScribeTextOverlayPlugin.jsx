import { useEffect, useMemo, useRef, useState } from 'react';
import ReactDOM from 'react-dom';
import PropTypes from 'prop-types';
import OpenSeadragon from 'openseadragon';
import {
  annotationBBox,
  annotationText,
  annotationsShareLine,
  annotationPageForCanvas,
  findEditorRowByAnnotationId,
  findAnnotationPageByAnnotationId,
  findCanvasIdByAnnotationId,
  firstAnnotationCanvasId,
  firstAnnotationPage,
  groupAnnotationsForEditor,
  isLineAnnotation,
  isWordAnnotation,
  lineAnnotationForSelection,
  rowText,
  selectedAnnotationIdForWindow,
} from '../utils/iiif';

const INLINE_EDITOR_GAP_PX = 0;
const INLINE_EDITOR_HEIGHT_PX = 72;
const INLINE_EDITOR_MIN_WIDTH_PX = 280;
const INLINE_WORD_GAP_PX = 6;
const ACTION_BAR_HEIGHT_PX = 104;
const INLINE_EDITOR_HANDLE_PX = 5;
const INLINE_EDITOR_CONTENT_INSET_PX = 10;

function annotationRect(viewer, annotation) {
  if (!viewer?.viewport || !viewer?.world?.getItemCount?.()) return null;
  const tiledImage = viewer.world.getItemAt(0);
  if (!tiledImage?.imageToViewportCoordinates) return null;
  const { x, y, w, h } = annotationBBox(annotation);
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

function dispatchOverlaySelection(label, windowId) {
  document.dispatchEvent(new CustomEvent('scribe:select-annotation', {
    detail: {
      annotationId: label.id,
      focusAnnotationId: label.isWord ? label.id : '',
      windowId,
    },
  }));
}

function ScribeTextOverlayPlugin({
  annotationPage,
  selectedAnnotationId,
  viewer,
  windowId,
}) {
  const [version, setVersion] = useState(0);
  const [editorState, setEditorState] = useState(null);
  const [editorDock, setEditorDock] = useState('below');
  const [dragState, setDragState] = useState(null);
  const [bboxDragState, setBboxDragState] = useState(null);
  const [pendingFocusWordId, setPendingFocusWordId] = useState('');
  const inputRefs = useRef(new Map());
  const dragIntentRef = useRef(null);

  function screenToImagePoint(clientX, clientY) {
    if (!viewer?.viewport || !viewer?.world?.getItemCount?.()) return null;
    const tiledImage = viewer.world.getItemAt(0);
    if (!tiledImage?.windowToImageCoordinates) return null;
    const rect = viewer.element.getBoundingClientRect();
    return tiledImage.windowToImageCoordinates(
      new OpenSeadragon.Point(clientX - rect.left, clientY - rect.top),
    );
  }

  function clearDragIntent() {
    if (!dragIntentRef.current?.timeoutId) return;
    window.clearTimeout(dragIntentRef.current.timeoutId);
    dragIntentRef.current = null;
  }

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
    const update = () => setVersion((value) => value + 1);
    viewer.addHandler('update-viewport', update);
    viewer.addHandler('animation-finish', update);
    viewer.addHandler('tile-loaded', update);
    return () => {
      viewer.removeHandler('update-viewport', update);
      viewer.removeHandler('animation-finish', update);
      viewer.removeHandler('tile-loaded', update);
    };
  }, [viewer]);

  useEffect(() => {
    const handleEditorState = (event) => {
      if (event?.detail?.windowId !== windowId) return;
      setEditorState(event.detail);
    };
    document.addEventListener('scribe:editor-state', handleEditorState);
    return () => document.removeEventListener('scribe:editor-state', handleEditorState);
  }, [windowId]);

  useEffect(() => {
    if (!dragState) return undefined;
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
    const handleMove = (event) => {
      setBboxDragState((current) => current
        ? { ...current, currentClientX: event.clientX, currentClientY: event.clientY }
        : current);
    };
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
          bbox: { x: Math.round(x), y: Math.round(y), w: Math.max(1, Math.round(w)), h: Math.max(1, Math.round(h)) },
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
  }, [bboxDragState, windowId]);

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
  const inlineEditorVisible = overlayMode === 'edit';
  const textOverlayVisible = overlayMode === 'read';

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

  const labels = useMemo(() => {
    if (!textOverlayVisible) return [];
    return groupAnnotationsForEditor(activePage)
      .flatMap((row) => (row.granularity === 'word' ? row.fields : [row.lead || row.fields[0]]))
      .map((annotation) => ({
        id: annotation?.id,
        isWord: isWordAnnotation(annotation),
        rect: annotationRect(viewer, annotation),
        text: annotationText(annotation),
      }))
      .filter((item) => item.text && item.rect && item.rect.w > 4 && item.rect.h > 4);
  }, [activePage, textOverlayVisible, viewer, version]);
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
      wordRect: isWordAnnotation(wordAnnotation) ? annotationRect(viewer, wordAnnotation) : null,
    };
  }, [activeFocusedWordAnnotationId, activePage, activeSelectedAnnotationId, viewer, version]);
  const inlineEditor = useMemo(() => {
    if (!inlineEditorVisible || !viewer) return null;
    const items = Array.isArray(activePage?.items) ? activePage.items : [];
    const selected = items.find((annotation) => annotation?.id === activeSelectedAnnotationId) || null;
    if (!selected) return null;

    const lineAnnotation = lineAnnotationForSelection(activePage, selected) || selected;
    const lineRect = annotationRect(viewer, lineAnnotation);
    const row = findEditorRowByAnnotationId(activePage, activeSelectedAnnotationId) || findEditorRowByAnnotationId(activePage, lineAnnotation.id);
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
        text: token,
      }));

    const fallbackRects = row.granularity === 'word' ? [] : fallbackWordRects(lineRect, Math.max(tokens.length, 1));
    const wordEditors = (tokens.length > 0 ? tokens : [{
      annotationId: null,
      fallbackIndex: 0,
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
    if (overlayMode !== 'edit' || !inlineEditor) return;
    // Don't steal focus if the user has already clicked into one of our inputs
    if (!pendingFocusWordId) {
      const focused = document.activeElement;
      const isOurInput = focused instanceof HTMLInputElement
        && Array.from(inputRefs.current.values()).includes(focused);
      if (isOurInput) return;
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
  }, [activeFocusedWordAnnotationId, activeSelectedAnnotationId, inlineEditor, overlayMode, pendingFocusWordId]);

  const focusBounds = useMemo(() => {
    if (!selectedDecoration.lineRect) return null;
    const linePaddingX = 10;
    const linePaddingY = 6;
    return {
      bottom: Math.min((viewer?.canvas?.getBoundingClientRect()?.height || 0) - 8, selectedDecoration.lineRect.y + selectedDecoration.lineRect.h + linePaddingY),
      left: Math.max(8, selectedDecoration.lineRect.x - linePaddingX),
      right: selectedDecoration.lineRect.x + selectedDecoration.lineRect.w + linePaddingX,
      top: Math.max(8, selectedDecoration.lineRect.y - linePaddingY),
    };
  }, [selectedDecoration.lineRect, viewer]);

  if (!viewer || overlayMode === 'none') return null;

  return ReactDOM.createPortal(
    <div
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

        const HANDLE_SIZE = 8;
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
              <div
                key={handle}
                style={{
                  background: 'rgba(255,255,255,0.95)',
                  border: '2px solid rgba(217,119,6,0.9)',
                  borderRadius: 2,
                  boxSizing: 'border-box',
                  cursor,
                  height: HANDLE_SIZE,
                  left: cx - HANDLE_SIZE / 2,
                  pointerEvents: 'auto',
                  position: 'absolute',
                  top: cy - HANDLE_SIZE / 2,
                  width: HANDLE_SIZE,
                  zIndex: 25,
                }}
                onPointerDown={(event) => {
                  event.stopPropagation();
                  event.preventDefault();
                  const ann = selectedDecoration.lineAnnotation;
                  if (!ann) return;
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
              />
            ))}
          </>
        );
      })() : null}
      {labels.map((label) => (
        <div
          key={label.id}
          onMouseDown={(event) => {
            event.preventDefault();
            event.stopPropagation();
            if (label.isWord) setPendingFocusWordId(label.id);
            dispatchOverlaySelection(label, windowId);
          }}
          onPointerDown={(event) => {
            event.preventDefault();
            event.stopPropagation();
            if (label.isWord) setPendingFocusWordId(label.id);
            dispatchOverlaySelection(label, windowId);
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
            position: 'absolute',
            textOverflow: 'ellipsis',
            top: label.rect.y,
            whiteSpace: 'nowrap',
            width: label.rect.w,
            zIndex: 30,
          }}
        >
          {label.text}
        </div>
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
                key={word.annotationId}
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
                  document.dispatchEvent(new CustomEvent('scribe:inline-change-word', {
                    detail: {
                      annotationId: word.annotationId,
                      text: word.text,
                      windowId,
                    },
                  }));
                }}
                onChange={(event) => {
                  document.dispatchEvent(new CustomEvent('scribe:inline-change-word', {
                    detail: {
                      annotationId: word.annotationId,
                      text: event.target.value,
                      windowId,
                    },
                  }));
                }}
                onKeyDown={(event) => {
                  if (event.key === 'Tab') {
                    event.preventDefault();
                    document.dispatchEvent(new CustomEvent('scribe:inline-step-selection', {
                      detail: {
                        direction: event.shiftKey ? -1 : 1,
                        windowId,
                      },
                    }));
                    return;
                  }
                  if (event.key === 'Enter' && !(event.metaKey || event.ctrlKey)) {
                    event.preventDefault();
                    document.dispatchEvent(new CustomEvent('scribe:inline-save', {
                      detail: { windowId },
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
                  key={`fallback-${index}`}
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
                    const inputs = Array.from(event.currentTarget.parentElement?.querySelectorAll('input') || []);
                    const nextText = inputs.map((input, inputIndex) => (
                      inputIndex === index ? event.target.value : input.value
                    )).join(' ').trim();
                    document.dispatchEvent(new CustomEvent('scribe:inline-change-text', {
                      detail: {
                        selectionStart: event.target.selectionStart,
                        text: nextText,
                        windowId,
                      },
                    }));
                  }}
                  onKeyDown={(event) => {
                    if (event.key === 'Tab') {
                      event.preventDefault();
                      document.dispatchEvent(new CustomEvent('scribe:inline-step-selection', {
                        detail: {
                          direction: event.shiftKey ? -1 : 1,
                          windowId,
                        },
                      }));
                      return;
                    }
                    if (event.key === 'Enter' && !(event.metaKey || event.ctrlKey)) {
                      event.preventDefault();
                      document.dispatchEvent(new CustomEvent('scribe:inline-save', {
                        detail: { windowId },
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
    </div>,
    viewer.canvas,
  );
}

ScribeTextOverlayPlugin.propTypes = {
  annotationPage: PropTypes.shape({
    items: PropTypes.array,
  }),
  selectedAnnotationId: PropTypes.string,
  viewer: PropTypes.object,
  windowId: PropTypes.string.isRequired,
};

const mapStateToProps = (state, { windowId }) => {
  const selectedAnnotationId = selectedAnnotationIdForWindow(state, windowId);
  const pageForSelection = findAnnotationPageByAnnotationId(state, selectedAnnotationId);
  const canvasId = findCanvasIdByAnnotationId(state, selectedAnnotationId) || firstAnnotationCanvasId(state);

  return {
    annotationPage: pageForSelection || annotationPageForCanvas(state, canvasId) || firstAnnotationPage(state),
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
