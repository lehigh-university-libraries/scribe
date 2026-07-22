// @vitest-environment happy-dom

import { describe, expect, it, vi } from 'vitest';

import ScribeAnnotationAdapter from './ScribeAnnotationAdapter';

function annotation(id, text, x = 10) {
  return {
    id,
    type: 'Annotation',
    motivation: 'supplementing',
    textGranularity: 'word',
    target: `https://example.test/canvas/1#xywh=${x},20,30,40`,
    body: [{
      id: `${id}/body`,
      type: 'TextualBody',
      value: text,
      language: 'en',
      format: 'text/plain',
      purpose: 'supplementing',
    }],
  };
}

function memoryClient(initialItems = []) {
  let revision = 4n;
  let page = {
    '@context': ['http://iiif.io/api/extension/text-granularity/context.json', 'http://iiif.io/api/presentation/3/context.json'],
    id: 'https://example.test/presentation/v3/item-image-91/canvas/page-1/annotations',
    type: 'AnnotationPage',
    label: { en: ['OCR corrections'] },
    partOf: [{ id: 'https://example.test/manifest/1', type: 'Manifest' }],
    items: structuredClone(initialItems),
  };
  return {
    enrichAnnotation: vi.fn(async (_itemImageId, _scope, payload) => JSON.parse(payload)),
    getAnnotationPage: vi.fn(async () => ({ page: structuredClone(page), revision: revision.toString() })),
    joinLines: vi.fn(async (_itemImageId, payload) => ({
      ...JSON.parse(payload),
      transformedBy: 'join-lines',
    })),
    joinWordsIntoLine: vi.fn(async (_itemImageId, payload) => ({
      ...JSON.parse(payload),
      transformedBy: 'join-words',
    })),
    saveAnnotationPage: vi.fn(async (_itemImageId, payload, expectedRevision) => {
      if (expectedRevision !== revision.toString()) throw new Error('revision conflict');
      page = JSON.parse(payload);
      revision += 1n;
      return { page: structuredClone(page), revision: revision.toString() };
    }),
    splitLineIntoTwoLines: vi.fn(async (_itemImageId, payload) => ({
      ...JSON.parse(payload),
      transformedBy: 'split-lines',
    })),
    splitLineIntoWords: vi.fn(async (_itemImageId, payload) => ({
      ...JSON.parse(payload),
      transformedBy: 'split-words',
    })),
  };
}

function adapter(client, contextId = '23') {
  return new ScribeAnnotationAdapter(
    '/annotations',
    3,
    'https://example.test/canvas/1',
    'Editor',
    { client, contextId, itemImageId: '91', windowId: 'window-1' },
  );
}

describe('ScribeAnnotationAdapter', () => {
  it('loads all granularities and preserves the complete AnnotationPage', async () => {
    const word = annotation('https://example.test/annotations/word-1', 'olde');
    const client = memoryClient([word]);

    const loaded = await adapter(client).all();

    expect(client.getAnnotationPage).toHaveBeenCalledWith('91');
    expect(loaded).toEqual(expect.objectContaining({
      label: { en: ['OCR corrections'] },
      partOf: [{ id: 'https://example.test/manifest/1', type: 'Manifest' }],
      items: [expect.objectContaining({ id: word.id, textGranularity: 'word' })],
    }));
  });

  it('atomically saves a page with the loaded revision', async () => {
    const client = memoryClient([annotation('https://example.test/annotations/word-1', 'olde')]);
    const instance = adapter(client);
    const snapshot = await instance.loadSnapshot();
    snapshot.page.items[0] = annotation('https://example.test/annotations/word-1', 'old', 14);

    const saved = await instance.savePage(snapshot.page, snapshot.revision);

    expect(client.saveAnnotationPage).toHaveBeenCalledWith('91', expect.any(String), '4');
    expect(saved.revision).toBe('5');
    expect(saved.page.items[0]).toEqual(expect.objectContaining({
      target: 'https://example.test/canvas/1#xywh=14,20,30,40',
      body: [expect.objectContaining({ id: 'https://example.test/annotations/word-1/body', language: 'en', value: 'old' })],
    }));
  });

  it('saves unknown numeric properties without JavaScript rounding', async () => {
    const client = memoryClient([annotation('https://example.test/annotations/word-1', 'olde')]);
    const instance = adapter(client);
    const snapshot = await instance.loadSnapshot();
    snapshot.page['ex:largeInteger'] = new String('9007199254740993');
    snapshot.page['ex:preciseDecimal'] = new String('0.123456789012345678901');
    client.saveAnnotationPage.mockResolvedValueOnce({ page: snapshot.page, revision: '5' });

    await instance.savePage(snapshot.page, snapshot.revision);

    const payload = client.saveAnnotationPage.mock.calls[0][1];
    expect(payload).toContain('"ex:largeInteger":9007199254740993');
    expect(payload).toContain('"ex:preciseDecimal":0.123456789012345678901');
  });

  it('uses only the loaded canonical page identity even when the Canvas has a query', async () => {
    const client = memoryClient();
    const instance = new ScribeAnnotationAdapter(
      '/annotations',
      3,
      'https://source.test/canvas/1?region=full&mode=choice',
      'Editor',
      { client, contextId: '23', itemImageId: '91', windowId: 'window-1' },
    );

    expect(() => instance.annotationPageId).toThrow('until Scribe has loaded');
    await instance.loadSnapshot();

    expect(instance.canvasId).toBe('https://source.test/canvas/1?region=full&mode=choice');
    expect(instance.annotationPageId).toBe('https://example.test/presentation/v3/item-image-91/canvas/page-1/annotations');
  });

  it('routes native Mirador mutations through the local draft bridge without saving', async () => {
    const original = annotation('https://example.test/annotations/word-1', 'olde');
    const created = annotation('https://example.test/annotations/word-2', 'new', 50);
    const client = memoryClient([original]);
    const instance = adapter(client);
    let draft = {
      id: 'https://example.test/presentation/v3/item-image-91/canvas/page-1/annotations',
      type: 'AnnotationPage',
      items: [original],
    };
    const operations = [];
    const handleMutation = (event) => {
      operations.push(structuredClone({
        annotationId: event.detail.annotationId,
        canvasId: event.detail.canvasId,
        itemImageId: event.detail.itemImageId,
        operation: event.detail.operation,
        windowId: event.detail.windowId,
      }));
      const next = structuredClone(draft);
      if (event.detail.operation === 'delete') {
        next.items = next.items.filter((item) => item.id !== event.detail.annotationId);
      } else {
        const index = next.items.findIndex((item) => item.id === event.detail.annotation.id);
        if (index < 0) next.items.push(structuredClone(event.detail.annotation));
        else next.items[index] = structuredClone(event.detail.annotation);
      }
      draft = next;
      event.detail.respond({
        annotation: event.detail.annotation,
        page: next,
        revision: '4',
      });
    };
    document.addEventListener('scribe:annotation-mutation', handleMutation);

    try {
      const createdPage = await instance.create(created);
      const updated = { ...created, body: [{ ...created.body[0], value: 'updated' }] };
      const updatedAnnotation = await instance.updateOne(updated);
      const deletedPage = await instance.delete(original.id);
      const readAfterWrite = await instance.get(created.id);
      const allAfterWrite = await instance.all();

      expect(createdPage.items.map((item) => item.id)).toEqual([original.id, created.id]);
      expect(updatedAnnotation.body[0].value).toBe('updated');
      expect(deletedPage.items.map((item) => item.id)).toEqual([created.id]);
      expect(readAfterWrite.body[0].value).toBe('updated');
      expect(allAfterWrite.items).toEqual([expect.objectContaining({
        id: created.id,
        body: [expect.objectContaining({ value: 'updated' })],
      })]);
      expect(operations).toEqual([
        { annotationId: undefined, canvasId: 'https://example.test/canvas/1', itemImageId: '91', operation: 'create', windowId: 'window-1' },
        { annotationId: undefined, canvasId: 'https://example.test/canvas/1', itemImageId: '91', operation: 'update', windowId: 'window-1' },
        { annotationId: original.id, canvasId: 'https://example.test/canvas/1', itemImageId: '91', operation: 'delete', windowId: 'window-1' },
      ]);
      expect(client.getAnnotationPage).not.toHaveBeenCalled();
      expect(client.saveAnnotationPage).not.toHaveBeenCalled();
    } finally {
      document.removeEventListener('scribe:annotation-mutation', handleMutation);
    }
  });

  it('fails closed when native mutation methods have no mounted editor bridge', async () => {
    const client = memoryClient();
    await expect(adapter(client).create(annotation('word-1', 'new')))
      .rejects.toThrow('mounted Scribe editor event bridge');
    expect(client.saveAnnotationPage).not.toHaveBeenCalled();
  });

  it('uses the persisted processing context for retranscription', async () => {
    const line = annotation('https://example.test/annotations/line-1', 'old');
    const client = memoryClient([line]);

    await adapter(client, '77').transcribeAnnotation(line);

    expect(client.enrichAnnotation).toHaveBeenCalledWith('91', 'line', JSON.stringify(line), '77');
  });

  it('resolves and caches the canvas processing context lazily', async () => {
    const line = annotation('https://example.test/annotations/line-1', 'old');
    const client = memoryClient([line]);
    const resolveContextId = vi.fn(async () => '88');
    const instance = new ScribeAnnotationAdapter(
      '/annotations',
      3,
      'https://example.test/canvas/1',
      'Editor',
      { client, contextId: '0', itemImageId: '91', resolveContextId, windowId: 'window-1' },
    );

    await instance.transcribeAnnotation(line);

    expect(resolveContextId).toHaveBeenCalledOnce();
    expect(client.enrichAnnotation).toHaveBeenCalledWith('91', 'line', JSON.stringify(line), '88');
    expect(instance.contextId).toBe('88');
  });

  it('sends complete draft pages and selected IDs to every structural transform', async () => {
    const lineA = annotation('https://example.test/annotations/line-a', 'alpha');
    const lineB = annotation('https://example.test/annotations/line-b', 'beta', 50);
    const client = memoryClient([lineA, lineB]);
    const instance = adapter(client);
    const draftPage = await instance.all();
    draftPage.workflow = { localDraft: true };
    const serializedDraft = JSON.stringify(draftPage);

    const splitWords = await instance.splitLineIntoWords(draftPage, lineA.id, ['al', 'pha']);
    const splitLines = await instance.splitLineIntoTwoLines(draftPage, lineA.id, 1);
    const joinedLines = await instance.joinLinesIntoLine(draftPage, [lineA.id, lineB.id]);
    const joinedWords = await instance.joinWordsIntoLine(draftPage, [lineA.id, lineB.id]);

    expect(client.splitLineIntoWords).toHaveBeenCalledWith('91', serializedDraft, lineA.id, ['al', 'pha']);
    expect(client.splitLineIntoTwoLines).toHaveBeenCalledWith('91', serializedDraft, lineA.id, 1);
    expect(client.joinLines).toHaveBeenCalledWith('91', serializedDraft, [lineA.id, lineB.id]);
    expect(client.joinWordsIntoLine).toHaveBeenCalledWith('91', serializedDraft, [lineA.id, lineB.id]);
    expect(splitWords).toEqual(expect.objectContaining({ transformedBy: 'split-words', workflow: { localDraft: true } }));
    expect(splitLines).toEqual(expect.objectContaining({ transformedBy: 'split-lines', workflow: { localDraft: true } }));
    expect(joinedLines).toEqual(expect.objectContaining({ transformedBy: 'join-lines', workflow: { localDraft: true } }));
    expect(joinedWords).toEqual(expect.objectContaining({ transformedBy: 'join-words', workflow: { localDraft: true } }));
  });

  it('rejects structural requests without a complete page or sufficient selected IDs', async () => {
    const client = memoryClient();
    const instance = adapter(client);

    await expect(instance.splitLineIntoWords({ type: 'Annotation' }, 'line-1')).rejects.toThrow(
      'splitLineIntoWords requires a complete IIIF AnnotationPage',
    );
    await expect(instance.joinLinesIntoLine({ id: 'page', type: 'AnnotationPage', items: [] }, ['line-1'])).rejects.toThrow(
      'joinLinesIntoLine requires at least two selected annotation IDs',
    );
    await expect(instance.joinWordsIntoLine(
      { id: 'page', type: 'AnnotationPage', items: [] },
      ['word-1', 'word-1'],
    )).rejects.toThrow('joinWordsIntoLine requires distinct selected annotation IDs');
    expect(client.splitLineIntoWords).not.toHaveBeenCalled();
    expect(client.joinLines).not.toHaveBeenCalled();
    expect(client.joinWordsIntoLine).not.toHaveBeenCalled();
  });
});
