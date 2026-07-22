import { describe, expect, it } from 'vitest';

import {
  acceptSavedSession,
  annotationLocallyChanged,
  applyPageTransformSession,
  applyRemoteAnnotation,
  createEditorSession,
  editSession,
  editorSessionReducer,
  rebaseSession,
  redoSession,
  sessionIsDirty,
  undoSession,
} from './session';
import { createDraftLineAnnotation, updateAnnotationText } from '../utils/iiif';

function annotation(id, value) {
  return { id, type: 'Annotation', body: [{ type: 'TextualBody', value }] };
}

function page(items, label = 'Page', id = 'https://example.test/presentation/v3/item-image-1/canvas/page-1/annotations') {
  return { id, type: 'AnnotationPage', label, items };
}

describe('editor session', () => {
  it('keeps an unsaved local annotation while adopting unrelated remote updates', () => {
    const base = page([annotation('line-1', 'local base'), annotation('line-2', 'old')]);
    let session = createEditorSession(base, '7');
    session = editSession(session, page([annotation('line-1', 'my draft'), annotation('line-2', 'old')]));

    session = rebaseSession(
      session,
      page([annotation('line-1', 'worker text'), annotation('line-2', 'new')], 'Remote label'),
      '8',
    );

    expect(session.draftPage.label).toBe('Remote label');
    expect(session.draftPage.items.map((item) => item.body[0].value)).toEqual(['my draft', 'new']);
    expect(session.basePage.items.map((item) => item.body[0].value)).toEqual(['worker text', 'new']);
    expect(session.pendingRemoteIds).toEqual(['line-1']);
    expect(annotationLocallyChanged(session, 'line-1')).toBe(true);
    expect(sessionIsDirty(session)).toBe(true);
    expect(undoSession(session).draftPage.items.map((item) => item.body[0].value))
      .toEqual(['worker text', 'new']);
  });

  it('ignores a reload that finishes after a newer revision', () => {
    const current = createEditorSession(page([annotation('line-1', 'new')]), '9');
    const stale = rebaseSession(current, page([annotation('line-1', 'old')]), '8');
    expect(stale).toBe(current);
  });

  it('starts a clean session instead of rebasing across AnnotationPage IDs', () => {
    const pageA = page([annotation('shared-line-id', 'canvas A')], 'Page A', 'https://example.test/presentation/v3/item-image-1/canvas/page-1/annotations');
    const pageB = page([annotation('shared-line-id', 'canvas B')], 'Page B', 'https://example.test/presentation/v3/item-image-2/canvas/page-1/annotations');
    let session = createEditorSession(pageA, '40');
    session = editSession(session, page(
      [annotation('shared-line-id', 'unsaved A')],
      'Page A',
      'https://example.test/presentation/v3/item-image-1/canvas/page-1/annotations',
    ));

    session = rebaseSession(session, pageB, '5');

    expect(session.basePage).toEqual(pageB);
    expect(session.draftPage).toEqual(pageB);
    expect(session.draftPage.items[0].body[0].value).toBe('canvas B');
    expect(session.revision).toBe('5');
    expect(sessionIsDirty(session)).toBe(false);
  });

  it('uses remote reading order while retaining a local-only annotation position', () => {
    const base = page([
      annotation('line-1', 'one'),
      annotation('line-2', 'two'),
      annotation('line-3', 'three'),
    ]);
    let session = createEditorSession(base, '2');
    session = editSession(session, page([
      annotation('line-1', 'one'),
      annotation('draft-line', 'draft'),
      annotation('line-2', 'two'),
      annotation('line-3', 'three'),
    ]));

    session = rebaseSession(session, page([
      annotation('line-3', 'three'),
      annotation('line-1', 'one'),
      annotation('line-2', 'two'),
    ]), '3');

    expect(session.draftPage.items.map((item) => item.id))
      .toEqual(['line-3', 'line-1', 'draft-line', 'line-2']);
    expect(sessionIsDirty(session)).toBe(true);
  });

  it('preserves a local reading-order edit across a background rebase', () => {
    const base = page([
      annotation('line-1', 'one'),
      annotation('line-2', 'two'),
      annotation('line-3', 'three'),
    ]);
    let session = createEditorSession(base, '2');
    session = editSession(session, page([
      annotation('line-2', 'two'),
      annotation('line-1', 'one'),
      annotation('line-3', 'three'),
    ]));

    session = rebaseSession(session, page([
      annotation('line-1', 'one'),
      annotation('line-2', 'two'),
      annotation('line-3', 'remote three'),
    ]), '3');

    expect(session.draftPage.items.map(({ id }) => id)).toEqual(['line-2', 'line-1', 'line-3']);
    expect(session.draftPage.items[2].body[0].value).toBe('remote three');
    expect(session.pendingRemoteIds).toEqual([]);
    expect(sessionIsDirty(session)).toBe(true);
  });

  it('flags overlapping local and remote reading-order edits without dropping the local order', () => {
    const base = page([
      annotation('line-1', 'one'),
      annotation('line-2', 'two'),
      annotation('line-3', 'three'),
    ]);
    let session = editSession(createEditorSession(base, '2'), page([
      annotation('line-2', 'two'),
      annotation('line-1', 'one'),
      annotation('line-3', 'three'),
    ]));

    session = rebaseSession(session, page([
      annotation('line-1', 'one'),
      annotation('line-3', 'three'),
      annotation('line-2', 'two'),
    ]), '3');

    expect(session.draftPage.items.map(({ id }) => id)).toEqual(['line-2', 'line-1', 'line-3']);
    expect(session.pendingRemoteIds).toEqual(expect.arrayContaining(['line-1', 'line-2']));
    expect(sessionIsDirty(session)).toBe(true);
  });

  it('accepts an identical concurrent reading-order edit without a false conflict', () => {
    const base = page([
      annotation('line-1', 'one'),
      annotation('line-2', 'two'),
      annotation('line-3', 'three'),
    ]);
    const reordered = page([
      annotation('line-2', 'two'),
      annotation('line-1', 'one'),
      annotation('line-3', 'three'),
    ]);
    let session = editSession(createEditorSession(base, '2'), reordered);

    session = rebaseSession(session, reordered, '3');

    expect(session.draftPage.items.map(({ id }) => id)).toEqual(['line-2', 'line-1', 'line-3']);
    expect(session.pendingRemoteIds).toEqual([]);
    expect(sessionIsDirty(session)).toBe(false);
  });

  it('clears a prior reading-order conflict when a later remote order matches the draft', () => {
    const base = page([
      annotation('line-1', 'one'),
      annotation('line-2', 'two'),
      annotation('line-3', 'three'),
    ]);
    const local = page([
      annotation('line-2', 'two'),
      annotation('line-1', 'one'),
      annotation('line-3', 'three'),
    ]);
    let session = editSession(createEditorSession(base, '2'), local);
    session = rebaseSession(session, page([
      annotation('line-1', 'one'),
      annotation('line-3', 'three'),
      annotation('line-2', 'two'),
    ]), '3');
    expect(session.pendingRemoteIds.length).toBeGreaterThan(0);

    session = rebaseSession(session, local, '4');

    expect(session.pendingRemoteIds).toEqual([]);
    expect(sessionIsDirty(session)).toBe(false);
  });

  it('treats reordered IIIF object properties as the same JSON value across rebase and save', () => {
    const originalAnnotation = {
      id: 'line-1',
      type: 'Annotation',
      body: [{
        type: 'TextualBody',
        purpose: 'supplementing',
        value: 'base',
        language: 'en',
      }],
      target: {
        source: { id: 'https://example.test/canvas/1', type: 'Canvas' },
        selector: { type: 'FragmentSelector', value: 'xywh=1,2,3,4' },
      },
    };
    const reorderedBaseAnnotation = {
      target: {
        selector: { value: 'xywh=1,2,3,4', type: 'FragmentSelector' },
        source: { type: 'Canvas', id: 'https://example.test/canvas/1' },
      },
      body: [{ language: 'en', value: 'base', purpose: 'supplementing', type: 'TextualBody' }],
      type: 'Annotation',
      id: 'line-1',
    };
    const base = page([originalAnnotation], { en: ['Page'] });
    const reorderedBase = {
      items: [reorderedBaseAnnotation],
      label: { en: ['Page'] },
      type: 'AnnotationPage',
      id: base.id,
    };
    expect(editSession(createEditorSession(base, '4'), reorderedBase).dirty).toBe(false);

    let session = editSession(createEditorSession(base, '4'), page([
      { ...originalAnnotation, body: [{ ...originalAnnotation.body[0], value: 'local' }] },
    ], { en: ['Page'] }));
    session = rebaseSession(session, reorderedBase, '5');
    expect(session.draftPage.items[0].body[0].value).toBe('local');
    expect(session.pendingRemoteIds).toEqual([]);

    const submitted = session.draftPage;
    const reorderedSaved = {
      items: [{
        target: submitted.items[0].target,
        body: [{ language: 'en', value: 'local', purpose: 'supplementing', type: 'TextualBody' }],
        type: 'Annotation',
        id: 'line-1',
      }],
      label: { en: ['Page'] },
      type: 'AnnotationPage',
      id: base.id,
    };
    session = acceptSavedSession(session, reorderedSaved, '6', submitted, '5');
    expect(sessionIsDirty(session)).toBe(false);
    expect(session.pendingRemoteIds).toEqual([]);
  });

  it('compares clone-safe lossless number wrappers by their source token', () => {
    const original = page([{
      ...annotation('line-1', 'base'),
      'ex:preciseDecimal': new String('0.123456789012345678901'),
    }]);
    const unchanged = structuredClone(original);
    const changed = structuredClone(original);
    changed.items[0]['ex:preciseDecimal'] = new String('0.123456789012345678902');

    expect(editSession(createEditorSession(original, '4'), unchanged).dirty).toBe(false);
    expect(editSession(createEditorSession(original, '4'), changed).dirty).toBe(true);
  });

  it('does not clear dirty state when a background result arrives', () => {
    const base = page([annotation('line-1', 'old'), annotation('line-2', 'other')]);
    let session = createEditorSession(base, '2');
    session = editSession(session, page([annotation('line-1', 'draft'), annotation('line-2', 'other')]));
    session = applyRemoteAnnotation(session, annotation('line-1', 'automatic'));

    expect(session.draftPage.items[0].body[0].value).toBe('draft');
    expect(session.basePage.items[0].body[0].value).toBe('automatic');
    expect(sessionIsDirty(session)).toBe(true);
  });

  it('settles a transform conflict when a background annotation matches the draft', () => {
    const base = page([annotation('line-1', 'base')]);
    const submitted = page([annotation('line-1', 'base')]);
    let session = editSession(
      createEditorSession(base, '2'),
      page([annotation('line-1', 'local')]),
    );
    session = applyPageTransformSession(
      session,
      submitted,
      page([annotation('line-1', 'automatic')]),
    );
    expect(session).toEqual(expect.objectContaining({
      conflictKind: 'transform',
      pendingRemoteIds: ['line-1'],
      status: 'conflict',
    }));

    session = applyRemoteAnnotation(session, annotation('line-1', 'local'));

    expect(session).toEqual(expect.objectContaining({
      conflictKind: null,
      dirty: false,
      error: null,
      pendingRemoteIds: [],
      status: 'ready',
    }));
  });

  it('retains an unresolved remote conflict across repeated and stale rebases', () => {
    const base = page([annotation('line-1', 'base')]);
    let session = editSession(
      createEditorSession(base, '5'),
      page([annotation('line-1', 'local')]),
    );

    session = rebaseSession(session, page([annotation('line-1', 'remote')]), '6');
    expect(session.pendingRemoteIds).toEqual(['line-1']);

    session = rebaseSession(session, page([annotation('line-1', 'remote')]), '6');
    expect(session.pendingRemoteIds).toEqual(['line-1']);
    expect(session.draftPage.items[0].body[0].value).toBe('local');

    const stale = rebaseSession(session, page([annotation('line-1', 'stale')]), '4');
    expect(stale).toBe(session);
  });

  it('clears a pending remote conflict when a later remote page matches the draft', () => {
    const base = page([annotation('line-1', 'base')]);
    let session = editSession(createEditorSession(base, '5'), page([annotation('line-1', 'local')]));
    session = rebaseSession(session, page([annotation('line-1', 'remote')]), '6');
    expect(session.pendingRemoteIds).toEqual(['line-1']);

    session = rebaseSession(session, page([annotation('line-1', 'local')]), '7');

    expect(session.pendingRemoteIds).toEqual([]);
    expect(sessionIsDirty(session)).toBe(false);
    expect(session.status).toBe('ready');
  });

  it('recomputes dirty and conflict state when an exact edit is reverted', () => {
    const base = page([annotation('line-1', 'base')]);
    let session = editSession(createEditorSession(base, '5'), page([annotation('line-1', 'local')]));
    session = rebaseSession(session, page([annotation('line-1', 'remote')]), '6');
    session = editorSessionReducer(session, { error: 'Revision conflict', type: 'save-conflict' });

    session = editSession(session, session.basePage);

    expect(sessionIsDirty(session)).toBe(false);
    expect(session.pendingRemoteIds).toEqual([]);
    expect(session.status).toBe('ready');
    expect(session.error).toBeNull();
  });

  it('reconciles pending conflicts through undo and redo', () => {
    const base = page([annotation('line-1', 'base')]);
    let session = editSession(createEditorSession(base, '1'), page([annotation('line-1', 'local')]));
    session = rebaseSession(session, page([annotation('line-1', 'remote')]), '2');
    session = editSession(session, session.basePage);
    expect(session.pendingRemoteIds).toEqual([]);

    session = undoSession(session);
    expect(session.pendingRemoteIds).toEqual([]);
    expect(sessionIsDirty(session)).toBe(true);
    session = redoSession(session);
    expect(session.pendingRemoteIds).toEqual([]);
    expect(sessionIsDirty(session)).toBe(false);
  });

  it('records text edits in undo history and accepts an atomic save revision', () => {
    const base = page([annotation('line-1', 'before')]);
    let session = createEditorSession(base, '10');
    session = editSession(session, page([annotation('line-1', 'after')]));
    expect(undoSession(session).draftPage.items[0].body[0].value).toBe('before');

    session = acceptSavedSession(session, session.draftPage, '11');
    expect(session.revision).toBe('11');
    expect(sessionIsDirty(session)).toBe(false);
    expect(session.undoStack).toEqual([]);
  });

  it('keeps edits made while a page save is in flight', () => {
    const base = page([annotation('line-1', 'before')]);
    let session = createEditorSession(base, '10');
    session = editSession(session, page([annotation('line-1', 'submitted')], 'Submitted metadata'));
    const submittedPage = session.draftPage;
    const submittedRevision = session.revision;

    // This edit occurs after SaveAnnotationPage has captured its payload but
    // before the response for that payload reaches the reducer.
    session = editSession(session, page([annotation('line-1', 'typed during save')], 'Local metadata'));
    session = editorSessionReducer(session, {
      page: page([annotation('line-1', 'submitted')], 'Server metadata'),
      revision: '11',
      submittedPage,
      submittedRevision,
      type: 'saved',
    });

    expect(session.basePage.items[0].body[0].value).toBe('submitted');
    expect(session.basePage.label).toBe('Server metadata');
    expect(session.draftPage.items[0].body[0].value).toBe('typed during save');
    expect(session.draftPage.label).toBe('Server metadata');
    expect(session.revision).toBe('11');
    expect(sessionIsDirty(session)).toBe(true);
    expect(undoSession(session).draftPage.items[0].body[0].value).toBe('submitted');
  });

  it('keeps one canonical draft annotation when it is edited during its first save', () => {
    const base = page([]);
    let session = createEditorSession(base, '1');
    const draft = createDraftLineAnnotation(
      'https://example.test/canvas/1',
      { x: 1, y: 2, w: 30, h: 10 },
      base.id,
    );
    expect(draft.id).toMatch(/^https:\/\/example\.test\/presentation\/v3\/item-image-1\/canvas\/page-1\/annotations\/items\/[0-9a-f]{32}$/);
    session = editSession(session, page([draft]));
    const submittedPage = session.draftPage;
    const submittedRevision = session.revision;

    session = editorSessionReducer(session, { type: 'save-start' });
    session = editSession(session, page([updateAnnotationText(draft, 'typed during save')]));
    session = editorSessionReducer(session, {
      page: submittedPage,
      revision: '2',
      submittedPage,
      submittedRevision,
      type: 'saved',
    });

    expect(session.draftPage.items).toHaveLength(1);
    expect(session.draftPage.items[0].id).toBe(draft.id);
    expect(session.draftPage.items[0].body[0].value).toBe('typed during save');
    expect(session.dirty).toBe(true);
    expect(session.status).toBe('ready');
  });

  it('settles a stale save response after a newer rebase without losing the rebased draft', () => {
    const base = page([annotation('line-1', 'base'), annotation('line-2', 'two')]);
    let session = editSession(createEditorSession(base, '10'), page([
      annotation('line-1', 'submitted'),
      annotation('line-2', 'two'),
    ]));
    const submittedPage = session.draftPage;
    session = editorSessionReducer(session, { type: 'save-start' });
    session = rebaseSession(session, page([
      annotation('line-1', 'remote'),
      annotation('line-2', 'remote two'),
    ]), '11');
    expect(session.status).toBe('saving');

    session = acceptSavedSession(session, submittedPage, '11', submittedPage, '10');

    expect(session.status).toBe('ready');
    expect(session.revision).toBe('11');
    expect(session.draftPage.items.map((item) => item.body[0].value))
      .toEqual(['submitted', 'remote two']);
    expect(session.pendingRemoteIds).toEqual(['line-1']);
  });

  it('does not enter the saving lifecycle for a clean page', () => {
    const session = editorSessionReducer(createEditorSession(page([]), '3'), { type: 'save-start' });
    expect(session.status).toBe('ready');
    expect(sessionIsDirty(session)).toBe(false);
  });

  it('merges a delayed full-page transform onto typing that happened after submission', () => {
    const base = page([annotation('line-1', 'one'), annotation('line-2', 'two')]);
    const submitted = page([annotation('line-1', 'one'), annotation('line-2', 'two')]);
    const transformed = page([
      annotation('line-1a', 'one a'),
      annotation('line-1b', 'one b'),
      annotation('line-2', 'two'),
    ]);
    let session = editSession(createEditorSession(base, '1'), page([
      annotation('line-1', 'one'),
      annotation('line-2', 'typed later'),
    ]));

    session = applyPageTransformSession(session, submitted, transformed);

    expect(session.draftPage.items.map((item) => item.id)).toEqual(['line-1a', 'line-1b', 'line-2']);
    expect(session.draftPage.items.map((item) => item.body[0].value))
      .toEqual(['one a', 'one b', 'typed later']);
    expect(session.pendingRemoteIds).toEqual([]);
    expect(sessionIsDirty(session)).toBe(true);
  });

  it('preserves a newer remote rebase under a delayed transform', () => {
    const submitted = page([annotation('line-1', 'one'), annotation('line-2', 'two')]);
    const transformed = page([
      annotation('line-1a', 'one a'),
      annotation('line-1b', 'one b'),
      annotation('line-2', 'two'),
    ]);
    let session = rebaseSession(
      createEditorSession(submitted, '1'),
      page([annotation('line-1', 'one'), annotation('line-2', 'remote two')], 'Remote page'),
      '2',
    );

    session = applyPageTransformSession(session, submitted, transformed);

    expect(session.revision).toBe('2');
    expect(session.draftPage.label).toBe('Remote page');
    expect(session.draftPage.items.map((item) => item.body[0].value))
      .toEqual(['one a', 'one b', 'remote two']);
    expect(session.pendingRemoteIds).toEqual([]);
  });

  it('keeps the latest edit and reports an overlap from a delayed transform', () => {
    const submitted = page([annotation('line-1', 'base')]);
    const transformed = page([annotation('line-1', 'transformed')]);
    let session = editSession(
      createEditorSession(submitted, '1'),
      page([annotation('line-1', 'typed later')]),
    );

    session = applyPageTransformSession(session, submitted, transformed);

    expect(session.draftPage.items[0].body[0].value).toBe('typed later');
    expect(session.pendingRemoteIds).toEqual(['line-1']);
    expect(sessionIsDirty(session)).toBe(true);
    expect(session.status).toBe('conflict');
    expect(session.conflictKind).toBe('transform');

    session = editSession(session, submitted);
    expect(session.pendingRemoteIds).toEqual([]);
    expect(session.status).toBe('ready');
    expect(session.conflictKind).toBeNull();
  });

  it('retains an existing remote conflict through an unrelated delayed transform', () => {
    const base = page([annotation('line-1', 'base'), annotation('line-2', 'two')]);
    let session = editSession(createEditorSession(base, '1'), page([
      annotation('line-1', 'local'),
      annotation('line-2', 'two'),
    ]));
    session = rebaseSession(session, page([
      annotation('line-1', 'remote'),
      annotation('line-2', 'two'),
    ]), '2');
    const submitted = session.draftPage;
    session = editSession(session, page([
      annotation('line-1', 'local'),
      annotation('line-2', 'typed later'),
    ]));
    const transformed = page([
      annotation('line-1', 'local'),
      annotation('line-2a', 'two a'),
      annotation('line-2b', 'two b'),
    ]);

    session = applyPageTransformSession(session, submitted, transformed, {
      affectedIds: ['line-2'],
      atomic: true,
    });

    expect(session.pendingRemoteIds).toEqual(expect.arrayContaining(['line-1', 'line-2']));
    expect(session.draftPage.items.map((item) => item.body[0].value))
      .toEqual(['local', 'typed later']);
    expect(session.status).toBe('conflict');
  });

  it('retains a save conflict through an unrelated delayed transform', () => {
    const base = page([annotation('line-1', 'base'), annotation('line-2', 'two')]);
    let session = editSession(createEditorSession(base, '1'), page([
      annotation('line-1', 'local'),
      annotation('line-2', 'two'),
    ]));
    session = editorSessionReducer(session, {
      error: 'Revision conflict',
      type: 'save-conflict',
    });
    const submitted = session.draftPage;
    session = applyRemoteAnnotation(session, annotation('line-3', 'remote addition'));

    session = applyPageTransformSession(session, submitted, page([
      annotation('line-1', 'local'),
      annotation('line-2', 'automatic two'),
    ]));

    expect(session.draftPage.items.map((item) => item.body[0].value))
      .toEqual(['local', 'automatic two', 'remote addition']);
    expect(session).toEqual(expect.objectContaining({
      conflictKind: 'save',
      error: 'Revision conflict',
      status: 'conflict',
    }));
  });

  it('rejects an atomic structural response that overlaps a newer remote rebase', () => {
    const submitted = page([annotation('line-1', 'base')]);
    const transformed = page([annotation('line-1a', 'one'), annotation('line-1b', 'two')]);
    let session = rebaseSession(
      createEditorSession(submitted, '1'),
      page([annotation('line-1', 'remote')]),
      '2',
    );

    session = applyPageTransformSession(session, submitted, transformed, {
      affectedIds: ['line-1'],
      atomic: true,
    });

    expect(session.draftPage.items.map((item) => item.id)).toEqual(['line-1']);
    expect(session.draftPage.items[0].body[0].value).toBe('remote');
    expect(session.revision).toBe('2');
    expect(session.status).toBe('conflict');
    expect(session.conflictKind).toBe('transform');
  });

  it('rejects an atomic structural response when a related transformed annotation changed', () => {
    const line = { ...annotation('line-1', 'one two'), textGranularity: 'line' };
    const word = { ...annotation('word-1', 'one'), textGranularity: 'word' };
    const submitted = page([line, word]);
    const transformed = page([
      { ...annotation('line-1a', 'one'), textGranularity: 'line' },
      { ...annotation('line-1b', 'two'), textGranularity: 'line' },
    ]);
    let session = editSession(createEditorSession(submitted, '1'), page([
      line,
      { ...annotation('word-1', 'typed later'), textGranularity: 'word' },
    ]));

    session = applyPageTransformSession(session, submitted, transformed, {
      affectedIds: ['line-1'],
      atomic: true,
    });

    expect(session.draftPage.items.map(({ id }) => id)).toEqual(['line-1', 'word-1']);
    expect(session.draftPage.items[1].body[0].value).toBe('typed later');
    expect(session.status).toBe('conflict');
    expect(session.conflictKind).toBe('transform');
  });

  it('models loading, saving, conflict, and recovery in the reducer', () => {
    let session = createEditorSession(page([]), '1');
    session = editorSessionReducer(session, { type: 'load-start' });
    expect(session).toEqual(expect.objectContaining({ status: 'loading', error: null }));
    session = editorSessionReducer(session, { page: page([]), revision: '2', type: 'loaded' });
    expect(session.status).toBe('ready');
    session = editorSessionReducer(session, { page: page([annotation('line-1', 'draft')]), type: 'edit' });
    session = editorSessionReducer(session, { type: 'save-start' });
    expect(session.status).toBe('saving');
    session = editorSessionReducer(session, { error: 'Revision conflict', type: 'save-conflict' });
    expect(session).toEqual(expect.objectContaining({ status: 'conflict', error: 'Revision conflict' }));
    session = editorSessionReducer(session, { type: 'dismiss-error' });
    expect(session).toEqual(expect.objectContaining({ status: 'ready', error: null }));
  });

  it('fails closed for an unknown reducer action instead of silently ignoring it', () => {
    expect(() => editorSessionReducer(createEditorSession(page([]), '1'), {
      type: 'misspelled-save',
    })).toThrow('Unsupported editor session action.');
  });

  it('stores compact history for a 10,000-annotation text edit', () => {
    const items = Array.from({ length: 10_000 }, (_, index) => annotation(`word-${index}`, `value-${index}`));
    const base = page(items);
    const changed = [...items];
    changed[5_000] = annotation('word-5000', 'changed');
    const started = performance.now();
    const session = editSession(createEditorSession(base, '1'), page(changed));

    expect(session.undoStack).toHaveLength(1);
    expect(session.undoStack[0]).toEqual(expect.objectContaining({
      changes: [expect.objectContaining({ id: 'word-5000' })],
      orderBefore: null,
      orderAfter: null,
    }));
    expect(undoSession(session).draftPage.items[5_000].body[0].value).toBe('value-5000');
    expect(performance.now() - started).toBeLessThan(5_000);
  });

  it('does not acknowledge a save response for another AnnotationPage', () => {
    const pageA = page([annotation('line-1', 'base')], 'Page A', 'https://example.test/presentation/v3/item-image-1/canvas/page-1/annotations');
    let session = createEditorSession(pageA, '12');
    session = editSession(session, page(
      [annotation('line-1', 'unsaved')],
      'Page A',
      'https://example.test/presentation/v3/item-image-1/canvas/page-1/annotations',
    ));
    const submittedPage = session.draftPage;

    const result = acceptSavedSession(
      session,
      page([annotation('line-1', 'wrong page')], 'Page B', 'https://example.test/presentation/v3/item-image-2/canvas/page-1/annotations'),
      '13',
      submittedPage,
      '12',
    );

    expect(result).toBe(session);
    expect(sessionIsDirty(result)).toBe(true);
    expect(result.draftPage.id).toBe('https://example.test/presentation/v3/item-image-1/canvas/page-1/annotations');
  });
});
