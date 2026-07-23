// @vitest-environment happy-dom

import { describe, expect, it, vi } from 'vitest';

import {
  clientPointToImage,
  imagePointToViewerElement,
  initialLineBBoxForViewport,
  normalizeImageBBox,
  viewerElementPointToImage,
} from './geometry';

function viewerWith(image) {
  return {
    viewport: {},
    world: {
      getItemCount: () => 1,
      getItemAt: () => image,
    },
  };
}

describe('OpenSeadragon geometry boundaries', () => {
  it('passes page coordinates to windowToImageCoordinates without subtracting the viewer twice', () => {
    const windowToImageCoordinates = vi.fn((point) => point);
    const point = clientPointToImage(viewerWith({ windowToImageCoordinates }), 140, 90, { x: 12, y: 8 });

    expect(windowToImageCoordinates).toHaveBeenCalledWith(expect.objectContaining({ x: 152, y: 98 }));
    expect(point).toEqual(expect.objectContaining({ x: 152, y: 98 }));
  });

  it('uses viewer-element conversion for MouseTracker positions and preview overlays', () => {
    const viewerElementToImageCoordinates = vi.fn((point) => ({ x: point.x * 2, y: point.y * 2 }));
    const imageToViewerElementCoordinates = vi.fn((point) => ({ x: point.x / 2, y: point.y / 2 }));
    const viewer = viewerWith({ viewerElementToImageCoordinates, imageToViewerElementCoordinates });

    expect(viewerElementPointToImage(viewer, { x: 25, y: 40 })).toEqual({ x: 50, y: 80 });
    expect(imagePointToViewerElement(viewer, 50, 80)).toEqual({ x: 25, y: 40 });
  });

  it('normalizes resize handles that cross and clamps coordinates to the image origin', () => {
    expect(normalizeImageBBox({ x: 8, y: -4, w: -12, h: 0 })).toEqual({
      x: 0,
      y: 0,
      w: 12,
      h: 1,
    });
  });

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
