import { describe, expect, it, vi } from 'vitest';

import { saveCachedEditorSessions } from './sessionPersistence';
import {
  createEditorSessionCache,
  dirtyEditorSessions,
  editorSessionCacheReducer,
  editorSessionForCanvas,
} from './sessionCache';

function annotation(value) {
  return { id: 'line-1', type: 'Annotation', body: [{ type: 'TextualBody', value }] };
}

function page(id, value) {
  const itemImageId = [...String(id)].reduce((valueSoFar, character) => (
    valueSoFar * 31 + character.charCodeAt(0)
  ), 1);
  return {
    id: `https://example.test/presentation/v3/item-image-${itemImageId}/canvas/page-1/annotations`,
    type: 'AnnotationPage',
    items: [annotation(value)],
  };
}

function dirtyTwoCanvases() {
  let cache = createEditorSessionCache('canvas-a', page('a', 'base A'), '1');
  cache = editorSessionCacheReducer(cache, {
    canvasId: 'canvas-a', page: page('a', 'draft A'), type: 'edit',
  });
  cache = editorSessionCacheReducer(cache, {
    canvasId: 'canvas-b', page: page('b', 'base B'), revision: '4', type: 'rebase',
  });
  return editorSessionCacheReducer(cache, {
    canvasId: 'canvas-b', page: page('b', 'draft B'), type: 'edit',
  });
}

describe('cached editor-session persistence', () => {
  it('settles an explicit clean save without issuing a persistence request', async () => {
    const cache = createEditorSessionCache('canvas-a', page('a', 'base'), '7');
    const adapter = { savePage: vi.fn() };
    const acceptSaved = vi.fn();

    const result = await saveCachedEditorSessions({
      acceptSaved,
      adapterFactory: () => adapter,
      canvasIds: ['canvas-a'],
      getCache: () => cache,
    });

    expect(result.ok).toBe(true);
    expect(result.snapshots.get('canvas-a')).toEqual({ page: page('a', 'base'), revision: '7' });
    expect(adapter.savePage).not.toHaveBeenCalled();
    expect(acceptSaved).not.toHaveBeenCalled();
    expect(editorSessionForCanvas(cache, 'canvas-a').status).toBe('ready');
  });

  it('saves every dirty Canvas before a global save reports success', async () => {
    let cache = dirtyTwoCanvases();
    const adapters = new Map([
      ['canvas-a', { savePage: vi.fn(async (submitted) => ({ page: submitted, revision: '2' })) }],
      ['canvas-b', { savePage: vi.fn(async (submitted) => ({ page: submitted, revision: '5' })) }],
    ]);
    const syncPage = vi.fn();

    const result = await saveCachedEditorSessions({
      acceptSaved: (canvasId, action) => {
        cache = editorSessionCacheReducer(cache, { ...action, canvasId });
      },
      adapterFactory: (canvasId) => adapters.get(canvasId),
      getCache: () => cache,
      syncPage,
    });

    expect(result.ok).toBe(true);
    expect(adapters.get('canvas-a').savePage).toHaveBeenCalledWith(expect.any(Object), '1');
    expect(adapters.get('canvas-b').savePage).toHaveBeenCalledWith(expect.any(Object), '4');
    expect(syncPage.mock.calls.map(([, canvasId]) => canvasId)).toEqual(['canvas-a', 'canvas-b']);
    expect(dirtyEditorSessions(cache)).toEqual([]);
  });

  it('blocks success when a Canvas is edited while its save is in flight', async () => {
    let cache = createEditorSessionCache('canvas-a', page('a', 'base'), '7');
    cache = editorSessionCacheReducer(cache, {
      canvasId: 'canvas-a', page: page('a', 'submitted'), type: 'edit',
    });
    const adapter = {
      savePage: vi.fn(async (submitted) => {
        cache = editorSessionCacheReducer(cache, {
          canvasId: 'canvas-a', page: page('a', 'typed during save'), type: 'edit',
        });
        return { page: submitted, revision: '8' };
      }),
    };
    const syncPage = vi.fn();

    const result = await saveCachedEditorSessions({
      acceptSaved: (canvasId, action) => {
        cache = editorSessionCacheReducer(cache, { ...action, canvasId });
      },
      adapterFactory: () => adapter,
      getCache: () => cache,
      syncPage,
    });

    expect(result.ok).toBe(false);
    expect(result.error).toBeNull();
    expect(result.remainingCanvasIds).toEqual(['canvas-a']);
    expect(editorSessionForCanvas(cache, 'canvas-a').draftPage.items[0].body[0].value)
      .toBe('typed during save');
    expect(syncPage).toHaveBeenCalledWith(
      expect.objectContaining({
        items: [expect.objectContaining({
          body: [expect.objectContaining({ value: 'typed during save' })],
        })],
      }),
      'canvas-a',
    );
  });

  it('settles but never renders a save response whose submitted base was superseded by rebase', async () => {
    let cache = createEditorSessionCache('canvas-a', page('a', 'base'), '7');
    cache = editorSessionCacheReducer(cache, {
      canvasId: 'canvas-a', page: page('a', 'submitted'), type: 'edit',
    });
    const syncPage = vi.fn();
    const adapter = {
      savePage: vi.fn(async (submitted) => {
        cache = editorSessionCacheReducer(cache, {
          canvasId: 'canvas-a',
          page: page('a', 'remote revision'),
          revision: '8',
          type: 'rebase',
        });
        return { page: submitted, revision: '8' };
      }),
    };
    cache = editorSessionCacheReducer(cache, { canvasId: 'canvas-a', type: 'save-start' });

    const result = await saveCachedEditorSessions({
      acceptSaved: (canvasId, action) => {
        cache = editorSessionCacheReducer(cache, { ...action, canvasId });
      },
      adapterFactory: () => adapter,
      canvasIds: ['canvas-a'],
      getCache: () => cache,
      syncPage,
    });

    expect(result.ok).toBe(false);
    expect(syncPage).not.toHaveBeenCalled();
    const session = editorSessionForCanvas(cache, 'canvas-a');
    expect(session.status).toBe('ready');
    expect(session.basePage.items[0].body[0].value).toBe('remote revision');
    expect(session.draftPage.items[0].body[0].value).toBe('submitted');
  });

  it('blocks the whole request when one dirty Canvas cannot be saved', async () => {
    let cache = dirtyTwoCanvases();
    const adapters = new Map([
      ['canvas-a', { savePage: vi.fn(async (submitted) => ({ page: submitted, revision: '2' })) }],
      ['canvas-b', { savePage: vi.fn(async () => { throw new Error('revision conflict'); }) }],
    ]);

    const result = await saveCachedEditorSessions({
      acceptSaved: (canvasId, action) => {
        cache = editorSessionCacheReducer(cache, { ...action, canvasId });
      },
      adapterFactory: (canvasId) => adapters.get(canvasId),
      getCache: () => cache,
    });

    expect(result.ok).toBe(false);
    expect(result.failedCanvasId).toBe('canvas-b');
    expect(result.error?.message).toBe('revision conflict');
    expect(result.remainingCanvasIds).toEqual(['canvas-b']);
  });

  it('does not leave an unattempted Canvas in the saving lifecycle after an earlier failure', async () => {
    let cache = dirtyTwoCanvases();
    const adapters = new Map([
      ['canvas-a', { savePage: vi.fn(async () => { throw new Error('first save failed'); }) }],
      ['canvas-b', { savePage: vi.fn() }],
    ]);
    const beginSave = vi.fn((canvasId) => {
      cache = editorSessionCacheReducer(cache, { canvasId, type: 'save-start' });
    });

    const result = await saveCachedEditorSessions({
      acceptSaved: (canvasId, action) => {
        cache = editorSessionCacheReducer(cache, { ...action, canvasId });
      },
      adapterFactory: (canvasId) => adapters.get(canvasId),
      beginSave,
      getCache: () => cache,
    });
    cache = editorSessionCacheReducer(cache, {
      canvasId: result.failedCanvasId,
      error: result.error?.message,
      type: 'save-error',
    });

    expect(beginSave).toHaveBeenCalledTimes(1);
    expect(beginSave).toHaveBeenCalledWith('canvas-a');
    expect(adapters.get('canvas-b').savePage).not.toHaveBeenCalled();
    expect(editorSessionForCanvas(cache, 'canvas-a').status).toBe('error');
    expect(editorSessionForCanvas(cache, 'canvas-b').status).toBe('ready');
  });
});
