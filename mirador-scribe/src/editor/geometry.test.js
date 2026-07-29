// @vitest-environment happy-dom

import { describe, expect, it } from 'vitest';

import { initialLineBBoxForViewport } from './geometry';

describe('OpenSeadragon geometry boundaries', () => {
  it('centers a keyboard-created line inside the visible image intersection', () => {
    expect(initialLineBBoxForViewport(
      { x: -100, y: 100, w: 1_000, h: 500 },
      { width: 800, height: 600 },
    )).toEqual({ x: 112, y: 320, w: 576, h: 60 });
    expect(initialLineBBoxForViewport(
      { x: 900, y: 100, w: 20, h: 20 },
      { width: 800, height: 600 },
    )).toBeNull();
  });
});
