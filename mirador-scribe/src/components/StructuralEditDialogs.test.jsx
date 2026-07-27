// @vitest-environment happy-dom

import { act } from 'react';
import { createRoot } from 'react-dom/client';
import { afterEach, describe, expect, it, vi } from 'vitest';
import StructuralEditDialogs from './StructuralEditDialogs';

globalThis.IS_REACT_ACT_ENVIRONMENT = true;

const mountedRoots = [];

function annotation(id, text, granularity = 'line') {
  return {
    body: [{ purpose: 'supplementing', type: 'TextualBody', value: text }],
    id,
    target: `https://example.test/canvas/1#xywh=pixel:0,0,100,20`,
    textGranularity: granularity,
    type: 'Annotation',
  };
}

function model(overrides = {}) {
  return {
    closeDialog: vi.fn(),
    dialog: 'split',
    joinLines: vi.fn(),
    joinWords: vi.fn(),
    lineCandidates: [],
    selectedLineId: 'line-1',
    selectedWordId: 'word-1',
    splitAtWord: vi.fn(),
    splitTokens: ['alpha', 'beta', 'gamma', 'delta'],
    wordCandidates: [],
    ...overrides,
  };
}

async function renderDialog(structuralEdits) {
  const container = document.createElement('div');
  document.body.appendChild(container);
  const root = createRoot(container);
  mountedRoots.push({ container, root });
  await act(async () => {
    root.render(<StructuralEditDialogs structuralEdits={structuralEdits} />);
  });
}

function button(name) {
  const match = [...document.querySelectorAll('button, [role="button"]')].find((candidate) => (
    candidate.getAttribute('aria-label') === name || candidate.textContent?.trim() === name
  ));
  if (!(match instanceof HTMLElement)) throw new Error(`missing button: ${name}`);
  return match;
}

afterEach(async () => {
  while (mountedRoots.length > 0) {
    const mounted = mountedRoots.pop();
    await act(async () => mounted.root.unmount());
    mounted.container.remove();
  }
  document.body.innerHTML = '';
  vi.restoreAllMocks();
});

describe('StructuralEditDialogs', () => {
  it('passes a reviewer-selected non-midpoint word boundary to split', async () => {
    const structuralEdits = model();
    await renderDialog(structuralEdits);

    await act(async () => button('Split after gamma, word 3').click());
    expect(document.body.textContent).toContain('alpha beta gamma');
    expect(document.body.textContent).toContain('delta');
    await act(async () => button('Split at boundary').click());

    expect(structuralEdits.splitAtWord).toHaveBeenCalledWith(3);
  });

  it('joins an explicit non-adjacent line selection', async () => {
    const lines = Array.from({ length: 5 }, (_, index) => annotation(`line-${index + 1}`, `Line ${index + 1}`));
    const structuralEdits = model({
      dialog: 'join-lines',
      lineCandidates: lines,
      selectedLineId: 'line-2',
    });
    await renderDialog(structuralEdits);

    await act(async () => button('Line 5: Line 5').click());
    await act(async () => button('Join selected lines').click());

    expect(structuralEdits.joinLines).toHaveBeenCalledWith(['line-2', 'line-5']);
  });

  it('joins only the chosen subset of words in a row', async () => {
    const words = ['one', 'two', 'three', 'four'].map((text, index) => (
      annotation(`word-${index + 1}`, text, 'word')
    ));
    const structuralEdits = model({
      dialog: 'join-words',
      selectedWordId: 'word-1',
      wordCandidates: words,
    });
    await renderDialog(structuralEdits);

    await act(async () => button('Word 3: three').click());
    await act(async () => button('Join selected words').click());

    expect(structuralEdits.joinWords).toHaveBeenCalledWith(['word-1', 'word-3']);
  });
});
