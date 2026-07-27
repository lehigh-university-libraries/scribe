import { describe, expect, it } from 'vitest';
import { selectedCandidateIds, splitBoundaryTokens } from './useStructuralEdits';

describe('structural edit selection helpers', () => {
  it('exposes only real word boundaries', () => {
    expect(splitBoundaryTokens('  one   two\nthree  ')).toEqual(['one', 'two', 'three']);
    expect(splitBoundaryTokens('one')).toEqual(['one']);
  });

  it('orders and filters selected IDs against the current page candidates', () => {
    const candidates = [
      { id: 'line-2' },
      { id: 'line-5' },
      { id: 'line-8' },
    ];
    expect(selectedCandidateIds(['forged', 'line-8', 'line-2', 'line-8'], candidates))
      .toEqual(['line-2', 'line-8']);
  });
});
