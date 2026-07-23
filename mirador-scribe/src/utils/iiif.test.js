import { describe, expect, it, vi } from 'vitest';

import {
  createDraftLineAnnotation,
  createDraftWordAnnotation,
  annotationBBox,
  editorRowTransformCapabilities,
  editorSelectedAnnotation,
  groupAnnotationsForEditor,
  hasCompanionWindowContent,
  selectionAfterPageTransform,
  updateAnnotationBBox,
  updateAnnotationText,
  updateRowText,
} from './iiif';

describe('IIIF editor utilities', () => {
  it('finds companion content only in the requested Mirador window', () => {
    const state = {
      companionWindows: {
        scoped: { content: 'scribeEditor', windowId: 'window-a' },
        legacyUnscoped: { content: 'scribeEditor' },
      },
    };

    expect(hasCompanionWindowContent(state, 'window-a', 'scribeEditor')).toBe(true);
    expect(hasCompanionWindowContent(state, 'window-b', 'scribeEditor')).toBe(false);
  });

  it('creates web-identifiable draft line and word annotations', () => {
    let seed = 0;
    vi.spyOn(globalThis.crypto, 'getRandomValues').mockImplementation((bytes) => {
      bytes.fill(seed);
      seed += 1;
      return bytes;
    });
    const line = createDraftLineAnnotation(
      'https://example.test/canvas/1',
      { x: 1, y: 2, w: 30, h: 10 },
      'https://example.test/presentation/v3/item-image-1/canvas/page-1/annotations',
    );
    const word = createDraftWordAnnotation(
      'https://example.test/canvas/1',
      { x: 2, y: 3, w: 10, h: 8 },
      'https://example.test/presentation/v3/item-image-1/canvas/page-1/annotations',
      'word',
    );

    expect(line.id).toBe('https://example.test/presentation/v3/item-image-1/canvas/page-1/annotations/items/00000000000000000000000000000000');
    expect(word.id).toBe('https://example.test/presentation/v3/item-image-1/canvas/page-1/annotations/items/01010101010101010101010101010101');
    expect(word).toEqual(expect.objectContaining({ textGranularity: 'word' }));
    expect(word.body[0].value).toBe('word');
    vi.restoreAllMocks();
  });

  it('never derives a draft identity from the Canvas before the canonical page loads', () => {
    expect(() => createDraftLineAnnotation(
      'https://example.test/canvas/1',
      { x: 1, y: 2, w: 30, h: 10 },
    )).toThrow('loaded canonical');
  });

  it.each([
    'https://example.test/pages/1',
    'https://example.test/presentation/v3/item-image-0/canvas/page-1/annotations',
    'https://example.test/presentation/v3/item-image-01/canvas/page-1/annotations',
    'https://example.test/presentation/v3/item-image-1/canvas/page-1/annotations/',
    'https://example.test/presentation/v3/item-image-1/canvas/page-1/annotations?workspace=2',
    'https://user@example.test/presentation/v3/item-image-1/canvas/page-1/annotations',
  ])('rejects a non-canonical draft page namespace: %s', (pageId) => {
    expect(() => createDraftLineAnnotation(
      'https://example.test/canvas/1',
      { x: 1, y: 2, w: 30, h: 10 },
      pageId,
    )).toThrow(/canonical|loaded/);
  });

  it('updates text without dropping TextualBody identity, language, or extensions', () => {
    const updated = updateAnnotationText({
      id: 'https://example.test/annotations/1',
      body: {
        id: 'https://example.test/bodies/1',
        type: 'TextualBody',
        language: 'cy',
        purpose: 'supplementing',
        value: 'hen',
        confidence: 0.8,
      },
    }, 'newydd');

    expect(updated.body).toEqual([expect.objectContaining({
      id: 'https://example.test/bodies/1',
      language: 'cy',
      value: 'newydd',
      confidence: 0.8,
    })]);
  });

  it('reads and updates compact string targets without losing the Canvas', () => {
    const annotation = {
      id: 'https://example.test/annotations/1',
      target: 'https://example.test/canvas/1#xywh=10,20,30,40',
    };

    expect(annotationBBox(annotation)).toEqual({ x: 10, y: 20, w: 30, h: 40 });
    expect(updateAnnotationBBox(annotation, { x: 12, y: 22, w: 32, h: 42 }).target)
      .toBe('https://example.test/canvas/1#xywh=pixel:12,22,32,42');
  });

  it('reads pixel and percent selectors and preserves other media-fragment parameters', () => {
    const compact = {
      target: 'https://example.test/canvas/1#t=2,4&xywh=percent:10,20,30,40&track=ocr',
    };
    const objectTarget = {
      target: {
        source: { id: 'https://example.test/canvas/1', type: 'Canvas', service: [{ id: 'https://images.test/1' }] },
        selector: {
          type: 'FragmentSelector',
          value: 't=2,4&xywh=pixel:10.5,20.25,30,40&track=ocr',
          extension: { retained: true },
        },
      },
    };

    expect(annotationBBox(compact, { width: 1000, height: 500 }))
      .toEqual({ x: 100, y: 100, w: 300, h: 200 });
    expect(annotationBBox(objectTarget)).toEqual({ x: 10.5, y: 20.25, w: 30, h: 40 });
    expect(updateAnnotationBBox(compact, { x: 12, y: 22, w: 32, h: 42 }).target)
      .toBe('https://example.test/canvas/1#t=2,4&xywh=pixel:12,22,32,42&track=ocr');
    const updatedObject = updateAnnotationBBox(objectTarget, { x: 1, y: 2, w: 3, h: 4 });
    expect(updatedObject.target.selector).toEqual(expect.objectContaining({
      extension: { retained: true },
      value: 't=2,4&xywh=pixel:1,2,3,4&track=ocr',
    }));
    expect(updatedObject.target.source.service).toEqual([{ id: 'https://images.test/1' }]);
  });

  it('finds and updates the sole spatial FragmentSelector without changing sibling selectors', () => {
    const annotation = {
      target: {
        source: { id: 'https://example.test/canvas/1', type: 'Canvas' },
        selector: [
          {
            type: 'FragmentSelector',
            value: 't=2,4&track=ocr',
            extension: { retained: 'nonspatial' },
          },
          {
            type: 'SvgSelector',
            value: '<svg><path d="M0 0"/></svg>',
          },
          {
            type: 'FragmentSelector',
            conformsTo: 'http://www.w3.org/TR/media-frags/',
            value: 'xywh=pixel:10,20,30,40',
            extension: { retained: 'spatial' },
          },
        ],
      },
    };

    expect(annotationBBox(annotation)).toEqual({ x: 10, y: 20, w: 30, h: 40 });
    const updated = updateAnnotationBBox(annotation, { x: 12, y: 22, w: 32, h: 42 });

    expect(updated.target.selector).toEqual([
      {
        type: 'FragmentSelector',
        value: 't=2,4&track=ocr',
        extension: { retained: 'nonspatial' },
      },
      {
        type: 'SvgSelector',
        value: '<svg><path d="M0 0"/></svg>',
      },
      {
        type: 'FragmentSelector',
        conformsTo: 'http://www.w3.org/TR/media-frags/',
        value: 'xywh=pixel:12,22,32,42',
        extension: { retained: 'spatial' },
      },
    ]);
    expect(annotation.target.selector[2].value).toBe('xywh=pixel:10,20,30,40');
  });

  it('appends a dedicated spatial selector when several nonspatial selectors exist', () => {
    const annotation = {
      target: {
        source: 'https://example.test/canvas/1',
        selector: [
          { type: 'FragmentSelector', value: 't=2,4' },
          { type: 'FragmentSelector', value: 'track=ocr' },
        ],
      },
    };

    const updated = updateAnnotationBBox(annotation, { x: 1, y: 2, w: 3, h: 4 });
    expect(updated.target.selector).toEqual([
      { type: 'FragmentSelector', value: 't=2,4' },
      { type: 'FragmentSelector', value: 'track=ocr' },
      {
        conformsTo: 'http://www.w3.org/TR/media-frags/',
        type: 'FragmentSelector',
        value: 'xywh=pixel:1,2,3,4',
      },
    ]);
  });

  it('refuses to update ambiguous spatial selectors', () => {
    const annotation = {
      target: {
        source: 'https://example.test/canvas/1',
        selector: [
          { type: 'FragmentSelector', value: 'xywh=1,2,3,4' },
          { type: 'FragmentSelector', value: 't=1,2&xywh=5,6,7,8' },
        ],
      },
    };

    expect(annotationBBox(annotation)).toEqual({ x: 0, y: 0, w: 0, h: 0 });
    expect(() => updateAnnotationBBox(annotation, { x: 10, y: 20, w: 30, h: 40 }))
      .toThrow('target contains multiple xywh FragmentSelectors');
  });

  it('keeps split word annotations together after the source line is replaced', () => {
    const words = [
      ['word-1', 10, 'one'],
      ['word-2', 45, 'two'],
      ['word-3', 80, 'three'],
    ].map(([id, x, value]) => ({
      id,
      type: 'Annotation',
      textGranularity: 'word',
      target: `https://example.test/canvas/1#xywh=${x},20,30,12`,
      body: [{ type: 'TextualBody', value }],
    }));

    const rows = groupAnnotationsForEditor({ type: 'AnnotationPage', items: words });

    expect(rows).toHaveLength(1);
    expect(rows[0].fields.map((word) => word.id)).toEqual(['word-1', 'word-2', 'word-3']);
  });

  it('keeps line splitting available for a line backed by word annotations', () => {
    const line = {
      ...createDraftLineAnnotation(
        'https://example.test/canvas/1',
        { x: 10, y: 20, w: 100, h: 20 },
        'https://example.test/presentation/v3/item-image-1/canvas/page-1/annotations',
      ),
      id: 'line-1',
    };
    const word = {
      ...createDraftWordAnnotation(
        'https://example.test/canvas/1',
        { x: 10, y: 20, w: 40, h: 20 },
        'https://example.test/presentation/v3/item-image-1/canvas/page-1/annotations',
      ),
      id: 'word-1',
    };
    const [row] = groupAnnotationsForEditor({ type: 'AnnotationPage', items: [line, word] });

    expect(row).toEqual(expect.objectContaining({ granularity: 'word', lead: line }));
    expect(editorRowTransformCapabilities(row)).toEqual({
      canSplitLine: true,
      canSplitToWords: false,
    });
  });

  it('keeps or geometrically replaces the selection after a page transform', () => {
    const before = {
      type: 'AnnotationPage',
      items: [
        { ...createDraftLineAnnotation('https://example.test/canvas/1', { x: 10, y: 10, w: 100, h: 20 }, 'https://example.test/presentation/v3/item-image-1/canvas/page-1/annotations'), id: 'selected-line' },
        { ...createDraftLineAnnotation('https://example.test/canvas/1', { x: 10, y: 500, w: 100, h: 20 }, 'https://example.test/presentation/v3/item-image-1/canvas/page-1/annotations'), id: 'unrelated-line' },
      ],
    };
    const retained = {
      ...before,
      items: [{ ...before.items[0], body: [{ type: 'TextualBody', value: 'changed' }] }, before.items[1]],
    };
    const replaced = {
      ...before,
      items: [
        before.items[1],
        { ...before.items[0], id: 'near-split-a', target: 'https://example.test/canvas/1#xywh=pixel:10,10,100,9' },
        { ...before.items[0], id: 'near-split-b', target: 'https://example.test/canvas/1#xywh=pixel:10,21,100,9' },
      ],
    };

    expect(selectionAfterPageTransform(before, retained, ['selected-line'])).toBe('selected-line');
    expect(selectionAfterPageTransform(before, replaced, ['selected-line'])).toBe('near-split-a');
  });

  it('does not retarget editor actions when the selected annotation is offscreen', () => {
    const selected = { id: 'selected-offscreen', type: 'Annotation' };
    const visible = { id: 'first-visible', type: 'Annotation' };

    expect(editorSelectedAnnotation(
      [selected, visible],
      [visible],
      selected.id,
      '',
    )).toBe(selected);
  });

  it('ignores object-property order when choosing a replacement selection', () => {
    const selected = {
      id: 'selected-line',
      type: 'Annotation',
      body: [{ type: 'TextualBody', value: 'selected' }],
      target: 'https://example.test/canvas/1#xywh=pixel:10,10,100,20',
    };
    const unrelated = {
      id: 'aaa-unrelated',
      type: 'Annotation',
      body: [{ type: 'TextualBody', value: 'unchanged' }],
      target: 'https://example.test/canvas/1#xywh=pixel:10,10,100,20',
    };
    const reorderedUnrelated = {
      target: unrelated.target,
      body: [{ value: 'unchanged', type: 'TextualBody' }],
      type: 'Annotation',
      id: unrelated.id,
    };
    const replacement = {
      ...selected,
      id: 'zzz-replacement',
    };

    expect(selectionAfterPageTransform(
      { type: 'AnnotationPage', items: [selected, unrelated] },
      { type: 'AnnotationPage', items: [reorderedUnrelated, replacement] },
      [selected.id],
    )).toBe(replacement.id);
  });

  it('groups and edits a 10,000-word page without quadratic scans or page clones', () => {
    const words = Array.from({ length: 10_000 }, (_, index) => ({
      id: `word-${index}`,
      type: 'Annotation',
      textGranularity: 'word',
      target: `https://example.test/canvas/1#xywh=pixel:${(index % 20) * 20},${Math.floor(index / 20) * 14},18,12`,
      body: [{ type: 'TextualBody', value: `w${index}` }],
    }));
    const sourcePage = {
      id: 'https://example.test/presentation/v3/item-image-999/canvas/page-1/annotations',
      type: 'AnnotationPage',
      items: words,
    };
    const started = performance.now();
    const rows = groupAnnotationsForEditor(sourcePage);
    const edited = updateRowText(sourcePage, rows[250], 'replacement words');

    expect(rows).toHaveLength(500);
    expect(edited.items).toHaveLength(10_000);
    expect(edited.items.filter((item, index) => item !== sourcePage.items[index])).toHaveLength(20);
    // A deliberately generous acceptance budget catches accidental O(n^2)
    // regressions while remaining stable in constrained CI containers.
    expect(performance.now() - started).toBeLessThan(5_000);
  });

  it('does not rescan a dense word band for every overlapping line', () => {
    let wordIdReads = 0;
    const words = Array.from({ length: 5_000 }, (_, index) => {
      const word = {
        type: 'Annotation',
        textGranularity: 'word',
        target: `https://example.test/canvas/1#xywh=pixel:${index * 2},20,2,12`,
        body: [{ type: 'TextualBody', value: `w${index}` }],
      };
      Object.defineProperty(word, 'id', {
        enumerable: true,
        get() {
          wordIdReads += 1;
          return `word-${index}`;
        },
      });
      return word;
    });
    const lines = Array.from({ length: 5_000 }, (_, index) => ({
      id: `line-${index}`,
      type: 'Annotation',
      textGranularity: 'line',
      target: 'https://example.test/canvas/1#xywh=pixel:0,20,10000,12',
      body: [{ type: 'TextualBody', value: '' }],
    }));

    const started = performance.now();
    const rows = groupAnnotationsForEditor({
      id: 'https://example.test/presentation/v3/item-image-999/canvas/page-1/annotations',
      type: 'AnnotationPage',
      items: [...lines, ...words],
    });

    expect(rows).toHaveLength(5_000);
    expect(rows[0].fields).toHaveLength(5_000);
    expect(wordIdReads).toBeLessThan(100_000);
    expect(performance.now() - started).toBeLessThan(5_000);
  });

  it('keeps one huge word from widening every normal line query', () => {
    let targetReads = 0;
    const normalWords = Array.from({ length: 4_999 }, (_, index) => {
      const word = {
        id: `normal-word-${index}`,
        type: 'Annotation',
        textGranularity: 'word',
        body: [{ type: 'TextualBody', value: `w${index}` }],
      };
      Object.defineProperty(word, 'target', {
        enumerable: true,
        get() {
          targetReads += 1;
          return `https://example.test/canvas/1#xywh=pixel:${index * 2},10000,2,10`;
        },
      });
      return word;
    });
    const hugeWord = {
      id: 'huge-word',
      type: 'Annotation',
      textGranularity: 'word',
      target: 'https://example.test/canvas/1#xywh=pixel:0,0,10,1000000000',
      body: [{ type: 'TextualBody', value: 'huge' }],
    };
    const lines = Array.from({ length: 5_000 }, (_, index) => ({
      id: `line-${index}`,
      type: 'Annotation',
      textGranularity: 'line',
      target: 'https://example.test/canvas/1#xywh=pixel:0,0,100,10',
      body: [{ type: 'TextualBody', value: '' }],
    }));

    const started = performance.now();
    const rows = groupAnnotationsForEditor({
      id: 'https://example.test/presentation/v3/item-image-999/canvas/page-1/annotations',
      type: 'AnnotationPage',
      items: [...lines, hugeWord, ...normalWords],
    });

    expect(rows).toHaveLength(5_001);
    expect(rows[0].fields.map(({ id }) => id)).toEqual(['huge-word']);
    expect(targetReads).toBeLessThan(250_000);
    expect(performance.now() - started).toBeLessThan(5_000);
  });
});
