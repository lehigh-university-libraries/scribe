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
    getBounds: () => ({ x: 0, y: 0, width: 1, height: contentHeight / 800 }),
    imageToViewportCoordinates: (x, y) => ({ x, y }),
    viewportToImageRectangle: () => ({ height: 600, width: 800, x: 0, y: 0 }),
  };
  return {
    addHandler: vi.fn(),
    canvas: viewerCanvas,
    removeHandler: vi.fn(),
    setMouseNavEnabled: vi.fn(),
    viewport: {
      fitBounds: vi.fn(),
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

function word(id = 'word-1', bbox = '230,32,28,20', text = 'alpha') {
  return {
    body: [{ purpose: 'supplementing', type: 'TextualBody', value: text }],
    id,
    target: `${canvasId}#xywh=pixel:${bbox}`,
    textGranularity: 'word',
    type: 'Annotation',
  };
}

function textLine(id, bbox, text) {
  return { ...line(id, bbox), body: [{ purpose: 'supplementing', type: 'TextualBody', value: text }] };
}

function pointer(type, init) {
  return new PointerEvent(type, { bubbles: true, isPrimary: true, pointerId: 1, ...init });
}

async function renderOverlay(items, mountedViewer) {
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
  await act(async () => root.render(<ScribeTextOverlayPlugin
    annotationPage={{ id: 'page-1', items, type: 'AnnotationPage' }}
    canvasId={canvasId}
    selectedAnnotationId=""
    viewer={mountedViewer}
    windowId={windowId}
  />));
}

describe('ScribeTextOverlayPlugin interaction', () => {
  it('selects a word from a click while the overlay is off and ignores a drag', async () => {
    const selections = [];
    const handleSelect = (event) => selections.push(event.detail);
    document.addEventListener('scribe:select-annotation', handleSelect);
    await renderOverlay([textLine('line-1', '20,30,240,24', 'alpha beta'), word()], viewer());

    const hit = viewerCanvas.querySelector('[data-scribe-hit="word"]');
    expect(hit).not.toBeNull();
    expect(hit?.getAttribute('aria-hidden')).toBe('true');
    expect(viewerCanvas.querySelector('[data-scribe-hit="line"]')).not.toBeNull();

    await act(async () => hit?.dispatchEvent(pointer('pointerdown', { clientX: 240, clientY: 40 })));
    await act(async () => window.dispatchEvent(pointer('pointerup', { clientX: 300, clientY: 40 })));
    expect(selections).toHaveLength(0);

    await act(async () => hit?.dispatchEvent(pointer('pointerdown', { clientX: 240, clientY: 40 })));
    await act(async () => window.dispatchEvent(pointer('pointerup', { clientX: 242, clientY: 41 })));
    expect(selections).toEqual([{
      annotationId: 'word-1',
      canvasId,
      focusAnnotationId: 'word-1',
      overlayMode: 'edit',
      windowId,
    }]);
    document.removeEventListener('scribe:select-annotation', handleSelect);
  });

  it('moves the focused word with the keyboard while keeping it inside its line', async () => {
    const geometry = [];
    const handleGeometry = (event) => geometry.push(event.detail);
    document.addEventListener('scribe:resize-annotation', handleGeometry);
    const items = [textLine('line-1', '20,30,240,24', 'alpha'), word()];
    await renderOverlay(items, viewer());
    await act(async () => {
      document.dispatchEvent(new CustomEvent('scribe:editor-state', {
        detail: {
          annotationPage: { id: 'page-1', items, type: 'AnnotationPage' },
          canvasId,
          focusedWordAnnotationId: 'word-1',
          overlayMode: 'edit',
          selectedAnnotationId: 'line-1',
          windowId,
        },
      }));
    });

    const moveHandle = viewerCanvas.querySelector('[data-scribe-move-handle="word"]');
    expect(moveHandle?.getAttribute('aria-label')).toBe('Move word: alpha');
    expect(viewerCanvas.querySelector('[data-scribe-geometry-target="word"]')).not.toBeNull();
    await act(async () => moveHandle?.dispatchEvent(new KeyboardEvent('keydown', { bubbles: true, key: 'ArrowRight', shiftKey: true })));
    expect(geometry).toEqual([{
      annotationId: 'word-1',
      bbox: { h: 20, w: 28, x: 232, y: 32 },
      canvasId,
      operation: 'move',
      windowId,
    }]);

    const corner = viewerCanvas.querySelector('[data-scribe-resize-handle="se"]');
    await act(async () => corner?.dispatchEvent(new KeyboardEvent('keydown', { bubbles: true, key: 'ArrowDown' })));
    expect(geometry.at(-1)).toEqual(expect.objectContaining({
      bbox: { h: 21, w: 28, x: 230, y: 32 },
      operation: 'resize',
    }));
    document.removeEventListener('scribe:resize-annotation', handleGeometry);
  });

  it('keeps mouse navigation enabled while editing', async () => {
    const mountedViewer = viewer();
    const items = [textLine('line-1', '20,30,240,24', 'alpha')];
    await renderOverlay(items, mountedViewer);
    await act(async () => {
      document.dispatchEvent(new CustomEvent('scribe:editor-state', {
        detail: {
          annotationPage: { id: 'page-1', items, type: 'AnnotationPage' },
          canvasId,
          overlayMode: 'edit',
          selectedAnnotationId: 'line-1',
          windowId,
        },
      }));
    });
    expect(viewerCanvas.querySelector('[data-scribe-inline-editor]')?.getAttribute('data-scribe-interactive')).toBe('true');
    expect(mountedViewer.setMouseNavEnabled).not.toHaveBeenCalledWith(false);
    expect(document.activeElement).toBe(viewerCanvas.querySelector('input'));
    const shellSelect = document.createElement('select');
    container.appendChild(shellSelect);
    shellSelect.focus();
    const animate = mountedViewer.addHandler.mock.calls.find(([name]) => name === 'animation')?.[1];
    await act(async () => animate?.());
    expect(document.activeElement).toBe(shellSelect);
    for (const isBusy of [true, false]) {
      await act(async () => document.dispatchEvent(new CustomEvent('scribe:editor-state', {
        detail: { canvasId, isBusy, overlayMode: 'edit', selectedAnnotationId: 'line-1', windowId },
      })));
    }
    expect(document.activeElement).toBe(viewerCanvas.querySelector('input'));
  });

  it('flows transcript rows without overlap at every zoom and reserves the image fit margin', async () => {
    const mountedViewer = viewer();
    mountedViewer.viewport.setMargins = vi.fn();
    const items = [
      textLine('line-1', '20,30,240,24', 'first line'),
      textLine('line-2', '20,90,240,30', 'second line'),
    ];
    const selections = [];
    const handleSelect = (event) => selections.push(event.detail);
    document.addEventListener('scribe:select-annotation', handleSelect);
    const edits = [];
    const handleEdit = (event) => edits.push(event.detail);
    document.addEventListener('scribe:inline-change-text', handleEdit);
    await renderOverlay(items, mountedViewer);
    await act(async () => {
      document.dispatchEvent(new CustomEvent('scribe:editor-state', {
        detail: {
          annotationPage: { id: 'page-1', items, type: 'AnnotationPage' },
          canvasId,
          overlayMode: 'transcript',
          selectedAnnotationId: 'line-1',
          windowId,
        },
      }));
    });

    expect(mountedViewer.setMouseNavEnabled).not.toHaveBeenCalledWith(false);
    expect(mountedViewer.viewport.setMargins).toHaveBeenLastCalledWith({ right: 360 });
    expect(mountedViewer.viewport.fitBounds).toHaveBeenLastCalledWith({ x: 0, y: 0, width: 1, height: 0.75 }, true);
    const pane = viewerCanvas.querySelector('[data-scribe-transcript-pane]');
    expect(pane?.getAttribute('aria-label')).toBe('Transcript');
    const rows = [...viewerCanvas.querySelectorAll('input[data-scribe-transcript-row]')];
    expect(rows.map((row) => row.value)).toEqual(['first line', 'second line']);
    expect(rows.map((row) => row.style.top)).toEqual(['', '']);
    expect(rows.map((row) => row.style.height)).toEqual(['32px', '32px']);
    expect(rows[0].getAttribute('aria-label')).toBe('Transcript line 1: first line');

    vi.useFakeTimers({ toFake: ['requestAnimationFrame', 'cancelAnimationFrame'] });
    const updateViewport = mountedViewer.addHandler.mock.calls.find(([name]) => name === 'update-viewport')[1];
    for (const [scale, tops, heights] of [
      [1, ['', ''], ['32px', '32px']],
      [0.5, ['', ''], ['32px', '32px']],
      [2, ['', ''], ['32px', '32px']],
    ]) {
      mountedViewer.viewport.pixelFromPoint = ({ x, y }) => ({ x: x * scale, y: y * scale });
      await act(async () => {
        updateViewport();
        vi.advanceTimersToNextFrame();
      });
      const zoomedRows = [...viewerCanvas.querySelectorAll('input[data-scribe-transcript-row]')];
      expect(zoomedRows.map((row) => row.style.top)).toEqual(tops);
      expect(zoomedRows.map((row) => row.style.height)).toEqual(heights);
      expect(zoomedRows.map((row) => row.style.fontSize)).toEqual(['15px', '15px']);
      expect(zoomedRows.map((row) => row.style.fontFamily)).toEqual([
        '"IBM Plex Sans", "Helvetica Neue", sans-serif',
        '"IBM Plex Sans", "Helvetica Neue", sans-serif',
      ]);
    }

    const valueSetter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')?.set;
    await act(async () => {
      valueSetter?.call(rows[1], 'second line fixed');
      rows[1].dispatchEvent(new Event('input', { bubbles: true }));
    });
    expect(edits).toEqual([expect.objectContaining({
      annotationId: 'line-2',
      canvasId,
      text: 'second line fixed',
      windowId,
    })]);

    await act(async () => rows[0].dispatchEvent(new KeyboardEvent('keydown', { bubbles: true, key: 'Enter' })));
    expect(selections.at(-1)).toEqual({
      annotationId: 'line-2',
      canvasId,
      focusAnnotationId: '',
      overlayMode: 'transcript',
      windowId,
    });

    await act(async () => {
      document.dispatchEvent(new CustomEvent('scribe:editor-state', {
        detail: { annotationPage: { id: 'page-1', items, type: 'AnnotationPage' }, canvasId, overlayMode: 'none', windowId },
      }));
    });
    expect(mountedViewer.viewport.setMargins).toHaveBeenLastCalledWith({});
    document.removeEventListener('scribe:select-annotation', handleSelect);
    document.removeEventListener('scribe:inline-change-text', handleEdit);
  });

  it('keeps off-screen words in a visible transcript line', async () => {
    const mountedViewer = viewer();
    mountedViewer.world.getItemAt(0).viewportToImageRectangle = () => ({
      height: 600, width: 100, x: 0, y: 0,
    });
    const items = [
      textLine('line-1', '20,30,600,24', 'first last'),
      word('word-1', '20,30,50,24', 'first'),
      word('word-2', '500,30,50,24', 'last'),
      textLine('line-2', '500,90,100,24', 'hidden'),
    ];
    await renderOverlay(items, mountedViewer);
    await act(async () => document.dispatchEvent(new CustomEvent('scribe:editor-state', {
      detail: { canvasId, overlayMode: 'transcript', windowId },
    })));
    const rows = [...viewerCanvas.querySelectorAll('input[data-scribe-transcript-row]')];
    expect(rows.map((row) => row.value)).toEqual(['first last', 'hidden']);
  });
});
