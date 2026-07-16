import { describe, expect, it } from 'vitest';

import ScribeAnnotationAdapter from './ScribeAnnotationAdapter';

function annotation(id, text, x = 10) {
  return {
    id,
    type: 'Annotation',
    motivation: 'supplementing',
    target: `https://example.test/canvas/1#xywh=${x},20,30,40`,
    body: {
      type: 'TextualBody',
      value: text,
      format: 'text/plain',
      purpose: 'transcribing',
    },
  };
}

function memoryClient(initialItems = []) {
  const items = new Map(initialItems.map((item) => [item.id, item]));
  return {
    async createAnnotation(payload) {
      const item = JSON.parse(payload);
      items.set(item.id, item);
      return item;
    },
    async updateAnnotation(payload) {
      const item = JSON.parse(payload);
      items.set(item.id, item);
      return item;
    },
    async deleteAnnotation(id) {
      items.delete(id);
    },
    async getAnnotation(id) {
      const item = items.get(id);
      if (!item) throw new Error('not found');
      return item;
    },
    async searchAnnotations() {
      return {
        '@context': 'http://iiif.io/api/presentation/3/context.json',
        id: 'https://example.test/page/1',
        type: 'AnnotationPage',
        items: Array.from(items.values()),
      };
    },
  };
}

describe('ScribeAnnotationAdapter', () => {
  it('reloads an edited word annotation from the backing annotation client', async () => {
    const client = memoryClient([annotation('word-1', 'olde')]);
    const adapter = new ScribeAnnotationAdapter('/annotations', 3, 'https://example.test/canvas/1', 'Editor', client);

    await adapter.updateOne(annotation('word-1', 'old', 14));

    const reloadedAdapter = new ScribeAnnotationAdapter('/annotations', 3, 'https://example.test/canvas/1', 'Editor', client);
    const page = await reloadedAdapter.all();

    expect(page).toEqual(expect.objectContaining({
      type: 'AnnotationPage',
      items: [
        expect.objectContaining({
          id: 'word-1',
          target: 'https://example.test/canvas/1#xywh=14,20,30,40',
          body: expect.objectContaining({ value: 'old' }),
        }),
      ],
    }));
  });
});
