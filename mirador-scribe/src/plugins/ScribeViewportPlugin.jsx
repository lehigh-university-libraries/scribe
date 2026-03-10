import { useEffect, useEffectEvent, useRef, useState } from 'react';
import PropTypes from 'prop-types';
import Box from '@mui/material/Box';
import OpenSeadragon from 'openseadragon';

const MIN_READABLE_BBOX_HEIGHT_PX = 28;

function currentImageBounds(viewer) {
  if (!viewer?.viewport || !viewer?.world?.getItemCount?.()) return null;
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

function containsBBox(bounds, bbox) {
  if (!bounds || !bbox) return false;
  return bbox.x >= bounds.x
    && bbox.y >= bounds.y
    && (bbox.x + bbox.w) <= (bounds.x + bounds.w)
    && (bbox.y + bbox.h) <= (bounds.y + bounds.h);
}

function rectFromPoints(start, end) {
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

function bboxScreenHeight(viewer, bbox) {
  if (!viewer?.world?.getItemCount?.() || !bbox) return 0;
  const tiledImage = viewer.world.getItemAt(0);
  if (!tiledImage?.imageToWindowCoordinates) return 0;
  const topLeft = tiledImage.imageToWindowCoordinates(bbox.x, bbox.y);
  const bottomRight = tiledImage.imageToWindowCoordinates(bbox.x + bbox.w, bbox.y + bbox.h);
  return Math.max(0, bottomRight.y - topLeft.y);
}

function ensureReadableBBox(viewer, bbox) {
  if (!viewer?.viewport || !viewer?.world?.getItemCount?.() || !bbox) return;
  const currentHeight = bboxScreenHeight(viewer, bbox);
  if (currentHeight >= MIN_READABLE_BBOX_HEIGHT_PX || currentHeight <= 0) return;

  const tiledImage = viewer.world.getItemAt(0);
  if (!tiledImage?.imageToViewportCoordinates) return;

  const center = tiledImage.imageToViewportCoordinates(
    bbox.x + bbox.w / 2,
    bbox.y + bbox.h / 2,
  );
  const scale = MIN_READABLE_BBOX_HEIGHT_PX / currentHeight;
  viewer.viewport.zoomBy(scale, new OpenSeadragon.Point(center.x, center.y), true);
  viewer.viewport.applyConstraints(true);
}

function ScribeViewportPlugin({ viewer, windowId }) {
  const trackerRef = useRef(null);
  const dragStartRef = useRef(null);
  const focusedBBoxRef = useRef(null);
  const [drawMode, setDrawMode] = useState(false);
  const [draftRect, setDraftRect] = useState(null);

  const emitViewport = useEffectEvent(() => {
    const bounds = currentImageBounds(viewer);
    document.dispatchEvent(new CustomEvent('scribe:viewport-change', {
      detail: {
        bounds,
        windowId,
      },
    }));
  });

  const focusAnnotation = useEffectEvent((bbox) => {
    if (!viewer?.viewport || !viewer?.world?.getItemCount?.() || !bbox) return;
    focusedBBoxRef.current = bbox;
    const bounds = currentImageBounds(viewer);
    if (!containsBBox(bounds, bbox)) {
      const tiledImage = viewer.world.getItemAt(0);
      if (!tiledImage?.imageToViewportCoordinates) return;

      const center = tiledImage.imageToViewportCoordinates(
        bbox.x + bbox.w / 2,
        bbox.y + bbox.h / 2,
      );
      viewer.viewport.panTo(new OpenSeadragon.Point(center.x, center.y), true);
    }

    ensureReadableBBox(viewer, bbox);
  });

  useEffect(() => {
    if (!viewer) return undefined;
    const handleViewport = () => {
      if (focusedBBoxRef.current && !drawMode) {
        ensureReadableBBox(viewer, focusedBBoxRef.current);
      }
      emitViewport();
    };
    viewer.addHandler('animation-finish', handleViewport);
    viewer.addHandler('update-viewport', handleViewport);
    viewer.addHandler('tile-loaded', handleViewport);
    emitViewport();
    return () => {
      viewer.removeHandler('animation-finish', handleViewport);
      viewer.removeHandler('update-viewport', handleViewport);
      viewer.removeHandler('tile-loaded', handleViewport);
    };
  }, [emitViewport, viewer]);

  useEffect(() => {
    const handleFocus = (event) => {
      if (event?.detail?.windowId !== windowId) return;
      focusAnnotation(event.detail.bbox || null);
    };
    document.addEventListener('scribe:focus-annotation', handleFocus);
    return () => document.removeEventListener('scribe:focus-annotation', handleFocus);
  }, [focusAnnotation, windowId]);

  useEffect(() => {
    const handleDrawMode = (event) => {
      if (event?.detail?.windowId !== windowId) return;
      setDrawMode(Boolean(event.detail.active));
      setDraftRect(null);
      dragStartRef.current = null;
    };
    document.addEventListener('scribe:set-draw-mode', handleDrawMode);
    return () => document.removeEventListener('scribe:set-draw-mode', handleDrawMode);
  }, [windowId]);

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
        if (!viewer.viewport || !viewer.world?.getItemCount?.()) return;
        const tiledImage = viewer.world.getItemAt(0);
        if (!tiledImage?.windowToImageCoordinates) return;
        dragStartRef.current = tiledImage.windowToImageCoordinates(event.position);
      },
      dragHandler: (event) => {
        if (!viewer.viewport || !viewer.world?.getItemCount?.() || !dragStartRef.current) return;
        const tiledImage = viewer.world.getItemAt(0);
        if (!tiledImage?.windowToImageCoordinates) return;
        const current = tiledImage.windowToImageCoordinates(event.position);
        setDraftRect(rectFromPoints(dragStartRef.current, current));
      },
      releaseHandler: (event) => {
        if (!viewer.viewport || !viewer.world?.getItemCount?.() || !dragStartRef.current) return;
        const tiledImage = viewer.world.getItemAt(0);
        if (!tiledImage?.windowToImageCoordinates) return;
        const end = tiledImage.windowToImageCoordinates(event.position);
        const bbox = rectFromPoints(dragStartRef.current, end);
        dragStartRef.current = null;
        setDraftRect(null);
        if (bbox.w < 12 || bbox.h < 12) return;
        document.dispatchEvent(new CustomEvent('scribe:create-annotation', {
          detail: {
            bbox,
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
  }, [drawMode, viewer, windowId]);

  if (!viewer?.viewport || !viewer.world?.getItemCount?.() || !draftRect) return null;
  const tiledImage = viewer.world.getItemAt(0);
  if (!tiledImage?.imageToWindowCoordinates || !viewer.element) return null;
  const viewerBounds = viewer.element.getBoundingClientRect();
  const topLeft = tiledImage.imageToWindowCoordinates(draftRect.x, draftRect.y);
  const bottomRight = tiledImage.imageToWindowCoordinates(draftRect.x + draftRect.w, draftRect.y + draftRect.h);

  return (
    <Box
      sx={{
        border: '2px solid rgba(217,119,6,0.95)',
        borderRadius: '4px',
        boxShadow: '0 0 0 1px rgba(255,255,255,0.55) inset',
        left: topLeft.x - viewerBounds.left,
        pointerEvents: 'none',
        position: 'absolute',
        top: topLeft.y - viewerBounds.top,
        width: Math.max(1, bottomRight.x - topLeft.x),
        height: Math.max(1, bottomRight.y - topLeft.y),
        zIndex: 7,
      }}
    />
  );
}

ScribeViewportPlugin.propTypes = {
  viewer: PropTypes.object,
  windowId: PropTypes.string.isRequired,
};

const scribeViewportPlugin = {
  component: ScribeViewportPlugin,
  mode: 'add',
  target: 'OpenSeadragonViewer',
};

export default scribeViewportPlugin;
