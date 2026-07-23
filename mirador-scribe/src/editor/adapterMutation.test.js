import { describe, expect, it } from 'vitest';

import { applyAdapterMutationToPage } from './adapterMutation';
import { createEditorSession, editSession, sessionIsDirty } from './session';

function annotation(id, value) {
  return {
    id,
    type: 'Annotation',
    textGranularity: 'line',
    target: 'https://source.test/canvas/1#xywh=pixel:1,2,30,10',
    body: [{ type: 'TextualBody', value }],
  };
}

describe('Mirador local mutation bridge', () => {
  it('changes only the local page draft until explicit persistence', () => {
    const base = {
      id: 'https://scribe.test/presentation/v3/item-image-1/canvas/page-1/annotations',
      type: 'AnnotationPage',
      items: [annotation('line-1', 'one')],
    };
    const created = annotation('line-2', 'two');
    let session = createEditorSession(base, '8');
    session = editSession(session, applyAdapterMutationToPage(session.draftPage, {
      annotation: created,
      operation: 'create',
    }).page);
    session = editSession(session, applyAdapterMutationToPage(session.draftPage, {
      annotation: annotation('line-2', 'updated'),
      operation: 'update',
    }).page);
    session = editSession(session, applyAdapterMutationToPage(session.draftPage, {
      annotationId: 'line-1',
      operation: 'delete',
    }).page);

    expect(session.revision).toBe('8');
    expect(session.basePage.items.map(({ id }) => id)).toEqual(['line-1']);
    expect(session.draftPage.items).toEqual([expect.objectContaining({
      id: 'line-2',
      body: [expect.objectContaining({ value: 'updated' })],
    })]);
    expect(sessionIsDirty(session)).toBe(true);
  });
});
