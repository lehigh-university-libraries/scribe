import { describe, expect, it } from 'vitest';

import {
  createEditorSessionCache,
  dirtyEditorSessions,
  editorSessionCacheIsDirty,
  editorSessionCacheReducer,
  editorSessionForCanvas,
  maxCleanEditorSessions,
} from './sessionCache';

function annotation(id, value) {
  return { id, type: 'Annotation', body: [{ type: 'TextualBody', value }] };
}

function page(id, value) {
  const itemImageId = [...String(id)].reduce((valueSoFar, character) => (
    valueSoFar * 31 + character.charCodeAt(0)
  ), 1);
  return {
    id: `https://example.test/presentation/v3/item-image-${itemImageId}/canvas/page-1/annotations`,
    type: 'AnnotationPage',
    items: [annotation('same-annotation-id', value)],
  };
}

describe('per-Canvas editor session cache', () => {
  it('restores dirty A after visiting B without contaminating B', () => {
    let cache = createEditorSessionCache('canvas-a', page('a', 'server A'), '1');
    cache = editorSessionCacheReducer(cache, {
      canvasId: 'canvas-a',
      page: page('a', 'unsaved A'),
      type: 'edit',
    });
    cache = editorSessionCacheReducer(cache, {
      canvasId: 'canvas-b',
      page: page('b', 'server B'),
      revision: '8',
      type: 'rebase',
    });

    const canvasB = editorSessionForCanvas(cache, 'canvas-b');
    expect(canvasB.draftPage.items[0].body[0].value).toBe('server B');
    expect(canvasB.dirty).toBe(false);

    const canvasA = editorSessionForCanvas(cache, 'canvas-a');
    expect(canvasA.draftPage.items[0].body[0].value).toBe('unsaved A');
    expect(canvasA.basePage.items[0].body[0].value).toBe('server A');
    expect(canvasA.dirty).toBe(true);
  });

  it('aggregates dirty state and clears only the saved Canvas', () => {
    let cache = createEditorSessionCache('canvas-a', page('a', 'server A'), '1');
    cache = editorSessionCacheReducer(cache, {
      canvasId: 'canvas-a', page: page('a', 'draft A'), type: 'edit',
    });
    cache = editorSessionCacheReducer(cache, {
      canvasId: 'canvas-b', page: page('b', 'server B'), revision: '3', type: 'rebase',
    });
    cache = editorSessionCacheReducer(cache, {
      canvasId: 'canvas-b', page: page('b', 'draft B'), type: 'edit',
    });

    expect(editorSessionCacheIsDirty(cache)).toBe(true);
    expect(dirtyEditorSessions(cache).map(({ canvasId }) => canvasId))
      .toEqual(['canvas-a', 'canvas-b']);

    const submitted = editorSessionForCanvas(cache, 'canvas-a');
    cache = editorSessionCacheReducer(cache, {
      canvasId: 'canvas-a',
      page: submitted.draftPage,
      revision: '2',
      submittedPage: submitted.draftPage,
      submittedRevision: submitted.revision,
      type: 'saved',
    });

    expect(editorSessionForCanvas(cache, 'canvas-a').dirty).toBe(false);
    expect(editorSessionForCanvas(cache, 'canvas-b').dirty).toBe(true);
    expect(dirtyEditorSessions(cache).map(({ canvasId }) => canvasId))
      .toEqual(['canvas-b']);
  });

  it('evicts least-recently-used clean sessions but never dirty drafts', () => {
    let cache = createEditorSessionCache('dirty-canvas', page('dirty', 'base'), '1');
    cache = editorSessionCacheReducer(cache, {
      canvasId: 'dirty-canvas', page: page('dirty', 'unsaved'), type: 'edit',
    });
    for (let index = 0; index < maxCleanEditorSessions + 3; index += 1) {
      cache = editorSessionCacheReducer(cache, {
        canvasId: `clean-${index}`,
        page: page(`clean-${index}`, `value-${index}`),
        revision: '1',
        type: 'rebase',
      });
    }

    expect(cache.sessions.has('dirty-canvas')).toBe(true);
    expect(cache.sessions.has('clean-0')).toBe(false);
    expect(cache.sessions.has(`clean-${maxCleanEditorSessions + 2}`)).toBe(true);
    expect([...cache.sessions.values()].filter((session) => !session.dirty))
      .toHaveLength(maxCleanEditorSessions);
  });
});
