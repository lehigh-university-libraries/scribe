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

function line(id = 'line-1', bbox = '20,30,240,24') {
  return {
    body: [{ purpose: 'supplementing', type: 'TextualBody', value: '' }],
    id,
    target: `${canvasId}#xywh=pixel:${bbox}`,
    textGranularity: 'line',
    type: 'Annotation',
  };
}

function viewer(contentHeight = 600) {
  viewerCanvas = document.createElement('div');
  Object.defineProperty(viewerCanvas, 'clientWidth', { value: 800 });
  document.body.appendChild(viewerCanvas);
  const tiledImage = {
    getContentSize: () => ({ x: 800, y: contentHeight }),
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
  vi.useRealTimers();
});

describe('ScribeTextOverlayPlugin', () => {
  it('removes every granularity marker when the overlay is off', async () => {
    const annotation = line();
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

  it('announces listener readiness before accepting the current-line wand event', async () => {
    const annotation = line();
    const readiness = [];
    const handleOverlayState = (event) => {
      readiness.push(event.detail);
      if (!event.detail.ready) return;
      document.dispatchEvent(new CustomEvent('scribe:transcription-segment', {
        detail: {
          annotation,
          attemptNumber: 2,
          canvasId,
          done: 1,
          jobId: '91',
          total: 7,
          windowId,
        },
      }));
    };
    document.addEventListener('scribe:transcription-overlay-state', handleOverlayState);
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

    const badge = viewerCanvas.querySelector('[data-scribe-transcription-active="true"]');
    expect(readiness).toEqual([{ canvasId, ready: true, windowId }]);
    expect(badge?.getAttribute('data-scribe-transcription-line')).toBe('1');
    expect(badge?.getAttribute('data-scribe-transcription-total')).toBe('7');
    expect(badge?.getAttribute('data-scribe-transcription-annotation')).toBe(annotation.id);
    expect(badge?.getAttribute('data-scribe-transcription-attempt')).toBe('2');
    expect(badge?.getAttribute('data-scribe-transcription-job')).toBe('91');
    expect(badge?.getAttribute('aria-label')).toBe('Automatic transcription: line 1 of 7');
    expect(badge?.querySelector('svg')).not.toBeNull();

    await act(async () => {
      document.dispatchEvent(new CustomEvent('scribe:transcription-segment', {
        detail: {
          annotation: line('foreign-line'),
          canvasId,
          done: 2,
          total: 7,
          windowId: 'window-other',
        },
      }));
    });
    expect(badge?.getAttribute('data-scribe-transcription-line')).toBe('1');

    await act(async () => root.unmount());
    root = undefined;
    expect(readiness.at(-1)).toEqual({ canvasId, ready: false, windowId });
    document.removeEventListener('scribe:transcription-overlay-state', handleOverlayState);
  });

  it('renders every queued automatic-transcription line before clearing the wand', async () => {
    vi.useFakeTimers();
    const first = line('line-1', '20,30,240,24');
    const second = line('line-2', '20,80,240,24');
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);

    await act(async () => root.render(<ScribeTextOverlayPlugin
      annotationPage={{ id: 'page-1', items: [first, second], type: 'AnnotationPage' }}
      canvasId={canvasId}
      selectedAnnotationId=""
      viewer={viewer()}
      windowId={windowId}
    />));

    await act(async () => {
      for (const [index, annotation] of [first, second].entries()) {
        document.dispatchEvent(new CustomEvent('scribe:transcription-segment', {
          detail: {
            annotation,
            attemptNumber: 1,
            canvasId,
            done: index + 1,
            jobId: '91',
            total: 2,
            windowId,
          },
        }));
      }
    });
    const badge = () => viewerCanvas.querySelector('[data-scribe-transcription-active="true"]');
    expect(badge()?.getAttribute('data-scribe-transcription-line')).toBe('1');
    expect(badge()?.getAttribute('data-scribe-transcription-annotation')).toBe(first.id);

    await act(async () => vi.advanceTimersByTimeAsync(400));
    expect(badge()?.getAttribute('data-scribe-transcription-line')).toBe('2');
    expect(badge()?.getAttribute('data-scribe-transcription-annotation')).toBe(second.id);

    await act(async () => {
      document.dispatchEvent(new CustomEvent('scribe:transcription-segment', {
        detail: { annotation: null, canvasId, jobId: '91', windowId },
      }));
    });
    expect(badge()?.getAttribute('data-scribe-transcription-line')).toBe('2');
    await act(async () => vi.advanceTimersByTimeAsync(400));
    expect(badge()).toBeNull();
  });

  it('clears the wand when the shell finishes its bounded completed-job replay', async () => {
    vi.useFakeTimers();
    const total = 500;
    const replayDelay = 10;
    const annotation = line('line-1', '20,30,240,24');
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

    await act(async () => {
      for (let done = 1; done <= total; done += 1) {
        document.dispatchEvent(new CustomEvent('scribe:transcription-segment', {
          detail: {
            annotation: line(`line-${done}`, `20,${30 + done},240,24`),
            canvasId,
            done,
            jobId: '91',
            total,
            windowId,
          },
        }));
        await vi.advanceTimersByTimeAsync(replayDelay);
      }
      document.dispatchEvent(new CustomEvent('scribe:transcription-segment', {
        detail: { annotation: null, canvasId, jobId: '91', windowId },
      }));
    });

    expect(viewerCanvas.querySelector('[data-scribe-transcription-active="true"]')).toBeNull();
  });

  it('focuses a far-away active wand line without moving an already visible line', async () => {
    vi.useFakeTimers();
    const visibleLine = line('line-visible', '20,30,240,24');
    const farLine = line('line-far', '20,2400,240,24');
    const focusEvents = [];
    const handleFocus = (event) => focusEvents.push(event.detail);
    document.addEventListener('scribe:focus-annotation', handleFocus);
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);

    await act(async () => root.render(<ScribeTextOverlayPlugin
      annotationPage={{ id: 'page-1', items: [visibleLine, farLine], type: 'AnnotationPage' }}
      canvasId={canvasId}
      selectedAnnotationId=""
      viewer={viewer(3000)}
      windowId={windowId}
    />));

    await act(async () => {
      document.dispatchEvent(new CustomEvent('scribe:transcription-segment', {
        detail: {
          annotation: visibleLine,
          canvasId,
          done: 1,
          jobId: '91',
          total: 2,
          windowId,
        },
      }));
    });
    expect(focusEvents).toEqual([]);

    await act(async () => {
      document.dispatchEvent(new CustomEvent('scribe:transcription-segment', {
        detail: {
          annotation: farLine,
          canvasId,
          done: 2,
          jobId: '91',
          total: 2,
          windowId,
        },
      }));
    });
    expect(focusEvents).toEqual([]);
    await act(async () => vi.advanceTimersByTimeAsync(400));
    expect(focusEvents).toEqual([{
      annotationId: farLine.id,
      bbox: { h: 24, w: 240, x: 20, y: 2400 },
      canvasId,
      windowId,
    }]);

    document.removeEventListener('scribe:focus-annotation', handleFocus);
  });
});
