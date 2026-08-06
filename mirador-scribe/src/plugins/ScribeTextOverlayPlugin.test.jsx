// @vitest-environment happy-dom

import { act } from 'react';
import { createRoot } from 'react-dom/client';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { ScribeTextOverlayPlugin } from './ScribeTextOverlayPlugin';

vi.mock('openseadragon', () => ({
  default: {
    Point: class Point {
      constructor(x, y) {
        this.x = x;
        this.y = y;
      }
    },
  },
}));

globalThis.IS_REACT_ACT_ENVIRONMENT = true;

const canvasId = 'https://example.test/canvas/1';
const windowId = 'window-1';
let container;
let root;
let viewerCanvas;

function viewer() {
  viewerCanvas = document.createElement('div');
  Object.defineProperty(viewerCanvas, 'clientWidth', { value: 800 });
  document.body.appendChild(viewerCanvas);
  const tiledImage = {
    getContentSize: () => ({ x: 800, y: 600 }),
    imageToViewportCoordinates: (x, y) => ({ x, y }),
    viewportToImageRectangle: () => ({ height: 600, width: 800, x: 0, y: 0 }),
  };
  return {
    addHandler: vi.fn(),
    canvas: viewerCanvas,
    removeHandler: vi.fn(),
    setMouseNavEnabled: vi.fn(),
    viewport: {
      getBounds: () => ({}),
      pixelFromPoint: (point) => point,
    },
    world: {
      getItemAt: () => tiledImage,
      getItemCount: () => 1,
    },
  };
}

afterEach(async () => {
  if (root) await act(async () => root.unmount());
  container?.remove();
  viewerCanvas?.remove();
  container = undefined;
  root = undefined;
  viewerCanvas = undefined;
});

describe('ScribeTextOverlayPlugin', () => {
  it('removes every granularity marker when the overlay is off', async () => {
    const annotation = {
      body: [{ purpose: 'supplementing', type: 'TextualBody', value: 'Line' }],
      id: 'line-1',
      target: `${canvasId}#xywh=pixel:20,30,240,24`,
      textGranularity: 'line',
      type: 'Annotation',
    };
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);

    await act(async () => root.render(<ScribeTextOverlayPlugin
      annotationPage={{ id: 'page-1', items: [annotation], type: 'AnnotationPage' }}
      canvasId={canvasId}
      selectedAnnotationId=""
      viewer={viewer()}
      windowId={windowId}
    />));
    expect(viewerCanvas.querySelector('[data-scribe-granularity]')).toBeNull();

    await act(async () => {
      document.dispatchEvent(new CustomEvent('scribe:editor-state', {
        detail: { canvasId, overlayMode: 'outline', windowId },
      }));
    });
    expect(viewerCanvas.querySelector('[data-scribe-granularity="line"]')).not.toBeNull();

    await act(async () => {
      document.dispatchEvent(new CustomEvent('scribe:editor-state', {
        detail: { canvasId, overlayMode: 'none', windowId },
      }));
    });
    expect(viewerCanvas.querySelector('[data-scribe-granularity]')).toBeNull();
  });
});
