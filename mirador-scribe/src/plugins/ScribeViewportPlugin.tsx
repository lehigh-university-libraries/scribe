import { useEffect, useEffectEvent, useRef, useState } from 'react';
import PropTypes from 'prop-types';
import Box from '@mui/material/Box';
import OpenSeadragon from 'openseadragon';
import {
  imagePointToViewerElement,
  initialLineBBoxForViewport,
  viewerElementPointToImage,
} from '../editor/geometry';
import { canvasIdForWindow } from '../utils/iiif';
import type { ImageBBox, MiradorState, Point2D } from '../types/scribe';

const INITIAL_BBOX_VIEWPORT_RATIO = 0.22;
const INITIAL_BBOX_WIDTH_RATIO = 0.6;
const FOCUS_BBOX_VIEWPORT_RATIO = 0.16;
const FOCUS_BBOX_WIDTH_RATIO = 0.82;

interface ScribeViewportProps {
  canvasId: string;
  viewer?: OpenSeadragon.Viewer | null;
  windowId: string;
}

interface ViewportEventDetail {
  active?: boolean;
  bbox?: ImageBBox | null;
  canvasId: string;
  windowId: string;
}

interface WindowOwnProps {
  windowId: string;
}

function currentImageBounds(viewer: OpenSeadragon.Viewer | null | undefined): ImageBBox | null {
  if (!viewer?.viewport || !viewer.world?.getItemCount()) return null;
  const tiledImage = viewer.world.getItemAt(0);
  if (!tiledImage?.viewportToImageRectangle) return null;
  const viewportRect = viewer.viewport.getBounds(true);
  const imageRect = tiledImage.viewportToImageRectangle(viewportRect);
  if (!imageRect) return null;
  return {
    h: imageRect.height,
    w: imageRect.width,
    x: imageRect.x,
    y: imageRect.y,
  };
}

function rectFromPoints(start: Point2D, end: Point2D): ImageBBox {
  const left = Math.min(start.x, end.x);
  const top = Math.min(start.y, end.y);
  const right = Math.max(start.x, end.x);
  const bottom = Math.max(start.y, end.y);
  return {
    x: left,
    y: top,
    w: Math.max(1, right - left),
    h: Math.max(1, bottom - top),
  };
}

function fitViewportToBBox(
  viewer: OpenSeadragon.Viewer | null | undefined,
  bbox: ImageBBox | null | undefined,
  heightRatio: number,
  widthRatio: number,
): void {
  if (!viewer?.viewport || !viewer.world?.getItemCount() || !bbox) return;
  const tiledImage = viewer.world.getItemAt(0);
  if (!tiledImage?.imageToViewportRectangle) return;

  const viewportElement = viewer.element;
  const viewportWidthPx = viewportElement?.clientWidth || 1;
  const viewportHeightPx = viewportElement?.clientHeight || 1;
  const viewportAspect = viewportWidthPx / viewportHeightPx;
  const targetHeight = Math.max(bbox.h / heightRatio, bbox.h * 1.8);
  const targetWidth = Math.max(bbox.w / widthRatio, targetHeight * viewportAspect);
  const left = bbox.x + (bbox.w / 2) - (targetWidth / 2);
  const top = bbox.y + (bbox.h / 2) - (targetHeight / 2);
  const nextBounds = tiledImage.imageToViewportRectangle(left, top, targetWidth, targetHeight);
  viewer.viewport.fitBoundsWithConstraints(nextBounds, true);
}

function snapViewportToBBox(
  viewer: OpenSeadragon.Viewer | null | undefined,
  bbox: ImageBBox | null | undefined,
): void {
  fitViewportToBBox(viewer, bbox, INITIAL_BBOX_VIEWPORT_RATIO, INITIAL_BBOX_WIDTH_RATIO);
}

function eventDetail(event: Event): ViewportEventDetail | null {
  return (event as CustomEvent<ViewportEventDetail>).detail || null;
}

function ScribeViewportPlugin({ canvasId, viewer, windowId }: ScribeViewportProps) {
  const trackerRef = useRef<OpenSeadragon.MouseTracker | null>(null);
  const dragStartRef = useRef<Point2D | null>(null);
  const focusedBBoxRef = useRef<ImageBBox | null>(null);
  const viewportAnimationFrameRef = useRef<number | null>(null);
  const [drawMode, setDrawMode] = useState(false);
  const [draftRect, setDraftRect] = useState<ImageBBox | null>(null);

  const emitViewport = useEffectEvent(() => {
    const bounds = currentImageBounds(viewer);
    document.dispatchEvent(new CustomEvent('scribe:viewport-change', {
      detail: {
        bounds,
        canvasId,
        windowId,
      },
    }));
  });

  const focusAnnotation = useEffectEvent((bbox: ImageBBox | null) => {
    if (!viewer?.viewport || !viewer.world?.getItemCount() || !bbox) return;
    focusedBBoxRef.current = bbox;
    fitViewportToBBox(viewer, bbox, FOCUS_BBOX_VIEWPORT_RATIO, FOCUS_BBOX_WIDTH_RATIO);
  });

  useEffect(() => {
    if (!viewer) return undefined;
    const handleViewport = () => {
      if (viewportAnimationFrameRef.current !== null) return;
      viewportAnimationFrameRef.current = window.requestAnimationFrame(() => {
        viewportAnimationFrameRef.current = null;
        emitViewport();
      });
    };
    viewer.addHandler('animation-finish', handleViewport);
    viewer.addHandler('update-viewport', handleViewport);
    viewer.addHandler('tile-loaded', handleViewport);
    emitViewport();
    return () => {
      if (viewportAnimationFrameRef.current !== null) {
        window.cancelAnimationFrame(viewportAnimationFrameRef.current);
        viewportAnimationFrameRef.current = null;
      }
      viewer.removeHandler('animation-finish', handleViewport);
      viewer.removeHandler('update-viewport', handleViewport);
      viewer.removeHandler('tile-loaded', handleViewport);
    };
  }, [emitViewport, viewer]);

  useEffect(() => {
    const handleFocus = (event: Event) => {
      const detail = eventDetail(event);
      if (detail?.windowId !== windowId || detail.canvasId !== canvasId) return;
      focusAnnotation(detail.bbox || null);
    };
    document.addEventListener('scribe:focus-annotation', handleFocus);
    return () => document.removeEventListener('scribe:focus-annotation', handleFocus);
  }, [canvasId, focusAnnotation, windowId]);

  useEffect(() => {
    const handleSnap = (event: Event) => {
      const detail = eventDetail(event);
      if (detail?.windowId !== windowId || detail.canvasId !== canvasId) return;
      snapViewportToBBox(viewer, detail.bbox || null);
    };
    document.addEventListener('scribe:snap-to-bbox', handleSnap);
    return () => document.removeEventListener('scribe:snap-to-bbox', handleSnap);
  }, [canvasId, viewer, windowId]);

  useEffect(() => {
    const handleDrawMode = (event: Event) => {
      const detail = eventDetail(event);
      if (detail?.windowId !== windowId || detail.canvasId !== canvasId) return;
      setDrawMode(Boolean(detail.active));
      setDraftRect(null);
      dragStartRef.current = null;
    };
    document.addEventListener('scribe:set-draw-mode', handleDrawMode);
    return () => document.removeEventListener('scribe:set-draw-mode', handleDrawMode);
  }, [canvasId, windowId]);

  useEffect(() => {
    const handleCenteredLine = (event: Event) => {
      const detail = eventDetail(event);
      if (detail?.windowId !== windowId || detail.canvasId !== canvasId) return;
      const bounds = currentImageBounds(viewer);
      const size = viewer?.world?.getItemAt(0)?.getContentSize?.();
      const bbox = initialLineBBoxForViewport(bounds, size ? {
        height: size.y,
        width: size.x,
      } : null);
      if (!bbox) return;
      document.dispatchEvent(new CustomEvent('scribe:create-annotation', {
        detail: {
          bbox,
          canvasId,
          focusResizeHandle: 'se',
          windowId,
        },
      }));
    };
    document.addEventListener('scribe:create-line-at-viewport-center', handleCenteredLine);
    return () => document.removeEventListener('scribe:create-line-at-viewport-center', handleCenteredLine);
  }, [canvasId, viewer, windowId]);

  useEffect(() => {
    if (!viewer?.element) return undefined;
    if (trackerRef.current) {
      trackerRef.current.destroy();
      trackerRef.current = null;
    }

    viewer.setMouseNavEnabled(!drawMode);
    if (!drawMode) {
      setDraftRect(null);
      dragStartRef.current = null;
      return undefined;
    }

    const tracker = new OpenSeadragon.MouseTracker({
      element: viewer.element,
      pressHandler: (event) => {
        if (!viewer.viewport || !viewer.world?.getItemCount()) return;
        dragStartRef.current = viewerElementPointToImage(viewer, event.position);
      },
      dragHandler: (event) => {
        if (!viewer.viewport || !viewer.world?.getItemCount() || !dragStartRef.current) return;
        const current = viewerElementPointToImage(viewer, event.position);
        if (!current) return;
        setDraftRect(rectFromPoints(dragStartRef.current, current));
      },
      releaseHandler: (event) => {
        if (!viewer.viewport || !viewer.world?.getItemCount() || !dragStartRef.current) return;
        const end = viewerElementPointToImage(viewer, event.position);
        if (!end) return;
        const bbox = rectFromPoints(dragStartRef.current, end);
        dragStartRef.current = null;
        setDraftRect(null);
        if (bbox.w < 12 || bbox.h < 12) return;
        document.dispatchEvent(new CustomEvent('scribe:create-annotation', {
          detail: {
            bbox,
            canvasId,
            windowId,
          },
        }));
      },
    });

    tracker.setTracking(true);
    trackerRef.current = tracker;

    return () => {
      viewer.setMouseNavEnabled(true);
      tracker.destroy();
      trackerRef.current = null;
      setDraftRect(null);
      dragStartRef.current = null;
    };
  }, [canvasId, drawMode, viewer, windowId]);

  if (!viewer?.viewport || !viewer.world?.getItemCount() || !draftRect) return null;
  const topLeft = imagePointToViewerElement(viewer, draftRect.x, draftRect.y);
  const bottomRight = imagePointToViewerElement(viewer, draftRect.x + draftRect.w, draftRect.y + draftRect.h);
  if (!topLeft || !bottomRight) return null;

  return (
    <Box
      sx={{
        border: '2px solid rgba(217,119,6,0.95)',
        borderRadius: '4px',
        boxShadow: '0 0 0 1px rgba(255,255,255,0.55) inset',
        left: topLeft.x,
        pointerEvents: 'none',
        position: 'absolute',
        top: topLeft.y,
        width: Math.max(1, bottomRight.x - topLeft.x),
        height: Math.max(1, bottomRight.y - topLeft.y),
        zIndex: 7,
      }}
    />
  );
}

ScribeViewportPlugin.propTypes = {
  canvasId: PropTypes.string.isRequired,
  viewer: PropTypes.object,
  windowId: PropTypes.string.isRequired,
};

const mapStateToProps = (state: MiradorState, { windowId }: WindowOwnProps) => ({
  canvasId: canvasIdForWindow(state, windowId),
});

const scribeViewportPlugin = {
  component: ScribeViewportPlugin,
  mapStateToProps,
  mode: 'add',
  target: 'OpenSeadragonViewer',
};

export default scribeViewportPlugin;
