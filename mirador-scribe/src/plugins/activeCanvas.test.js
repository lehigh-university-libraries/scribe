// @vitest-environment happy-dom

import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  activeCanvasEventDetail,
  dispatchActiveCanvasEvent,
  resolveActiveCanvasId,
  resolveActiveCanvasState,
} from './activeCanvas';

describe('active Canvas event bridge', () => {
  afterEach(() => vi.restoreAllMocks());

  it('emits the adapter item-image identity with the focused Canvas', () => {
    const adapterFactory = vi.fn(() => ({ itemImageId: 2002n }));
    const listener = vi.fn();
    document.addEventListener('scribe:active-canvas', listener, { once: true });
    const detail = activeCanvasEventDetail(
      adapterFactory,
      'https://source.test/manifest/canvas/b',
      'window-b',
    );

    expect(dispatchActiveCanvasEvent(detail)).toBe(true);
    expect(adapterFactory).toHaveBeenCalledWith('https://source.test/manifest/canvas/b');
    expect(listener).toHaveBeenCalledOnce();
    expect(listener.mock.calls[0][0].detail).toEqual({
      canvasId: 'https://source.test/manifest/canvas/b',
      itemImageId: '2002',
      windowId: 'window-b',
    });
  });

  it('keeps the Canvas event usable when an external adapter has no item-image identity', () => {
    expect(activeCanvasEventDetail(
      () => {
        throw new Error('external adapter');
      },
      'https://source.test/manifest/canvas/a',
      'window-a',
    )).toEqual({
      canvasId: 'https://source.test/manifest/canvas/a',
      itemImageId: '',
      windowId: 'window-a',
    });
    expect(dispatchActiveCanvasEvent(null)).toBe(false);
    expect(activeCanvasEventDetail(
      () => ({ itemImageId: 1n }),
      'https://source.test/manifest/canvas/a',
      '',
    )).toBeNull();
    expect(dispatchActiveCanvasEvent({
      canvasId: 'https://source.test/manifest/canvas/a',
      itemImageId: '1',
    })).toBe(false);
  });

  it('prefers the current window Canvas over a stale prior-page annotation selection', () => {
    const state = {
      annotations: {
        'https://source.test/manifest/canvas/a': {
          'https://scribe.test/presentation/v3/item-image-1/canvas/page-1/annotations': {
            json: { items: [{ id: 'annotation-from-a' }], type: 'AnnotationPage' },
          },
        },
        'https://source.test/manifest/canvas/b': {
          'https://scribe.test/presentation/v3/item-image-2/canvas/page-1/annotations': {
            json: { items: [{ id: 'annotation-from-b' }], type: 'AnnotationPage' },
          },
        },
      },
      windows: {
        'window-1': {
          canvasId: 'https://source.test/manifest/canvas/b',
          selectedAnnotationId: 'annotation-from-a',
        },
      },
    };

    expect(resolveActiveCanvasId(state, 'window-1', 'annotation-from-a'))
      .toBe('https://source.test/manifest/canvas/b');
    expect(resolveActiveCanvasState(state, 'window-1', 'annotation-from-a')).toEqual({
      canvasId: 'https://source.test/manifest/canvas/b',
      serverPage: state.annotations['https://source.test/manifest/canvas/b']
        ['https://scribe.test/presentation/v3/item-image-2/canvas/page-1/annotations'].json,
    });
    const beforePageBLoads = structuredClone(state);
    delete beforePageBLoads.annotations['https://source.test/manifest/canvas/b'];
    expect(resolveActiveCanvasState(beforePageBLoads, 'window-1', 'annotation-from-a')).toEqual({
      canvasId: 'https://source.test/manifest/canvas/b',
      serverPage: null,
    });
    expect(resolveActiveCanvasId({ annotations: state.annotations }, 'window-1', 'annotation-from-a'))
      .toBe('https://source.test/manifest/canvas/a');
  });
});
