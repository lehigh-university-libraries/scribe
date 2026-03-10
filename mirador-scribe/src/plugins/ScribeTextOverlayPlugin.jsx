import { useEffect, useMemo, useState } from 'react';
import ReactDOM from 'react-dom';
import PropTypes from 'prop-types';
import OpenSeadragon from 'openseadragon';
import {
  annotationBBox,
  annotationText,
  annotationPageForCanvas,
  findAnnotationPageByAnnotationId,
  findCanvasIdByAnnotationId,
  firstAnnotationCanvasId,
  firstAnnotationPage,
  selectedAnnotationIdForWindow,
} from '../utils/iiif';

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

function ScribeTextOverlayPlugin({
  annotationPage,
  selectedAnnotationId,
  viewer,
  windowId,
}) {
  const [displayMode, setDisplayMode] = useState('segments');
  const [version, setVersion] = useState(0);

  useEffect(() => {
    const handleModeChange = (event) => {
      if (event?.detail?.windowId !== windowId) return;
      setDisplayMode(event.detail.mode || 'segments');
    };
    document.addEventListener('scribe:display-mode-change', handleModeChange);
    return () => document.removeEventListener('scribe:display-mode-change', handleModeChange);
  }, [windowId]);

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

  const labels = useMemo(() => {
    if (displayMode !== 'text') return [];
    const items = Array.isArray(annotationPage?.items) ? annotationPage.items : [];
    return items
      .map((annotation) => ({
        id: annotation?.id,
        rect: annotationRect(viewer, annotation),
        text: annotationText(annotation),
      }))
      .filter((item) => item.text && item.rect && item.rect.w > 4 && item.rect.h > 4);
  }, [annotationPage, displayMode, viewer, version]);

  if (!viewer || displayMode !== 'text') return null;

  return ReactDOM.createPortal(
    <div
      style={{
        height: '100%',
        left: 0,
        pointerEvents: 'none',
        position: 'absolute',
        top: 0,
        width: '100%',
      }}
    >
      {labels.map((label) => (
        <div
          key={label.id}
          style={{
            background: label.id === selectedAnnotationId ? 'rgba(251, 191, 36, 0.88)' : 'rgba(15, 23, 42, 0.78)',
            border: label.id === selectedAnnotationId ? '1px solid rgba(245, 158, 11, 0.95)' : '1px solid rgba(148, 163, 184, 0.45)',
            borderRadius: 4,
            color: '#f8fafc',
            fontSize: Math.max(11, Math.min(18, label.rect.h * 0.72)),
            fontWeight: 600,
            left: label.rect.x,
            lineHeight: 1.1,
            maxWidth: label.rect.w,
            overflow: 'hidden',
            padding: '1px 4px',
            position: 'absolute',
            textOverflow: 'ellipsis',
            top: label.rect.y,
            whiteSpace: 'nowrap',
          }}
        >
          {label.text}
        </div>
      ))}
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
