// @vitest-environment happy-dom

import { describe, expect, it } from 'vitest';

import {
  clientPointToImage,
  imagePointToViewerElement,
  imageBBoxContainsCenter,
  initialLineBBoxForViewport,
  viewerElementPointToImage,
  wordBBoxBesideSelection,
} from './geometry';

describe('OpenSeadragon geometry boundaries', () => {
  it('uses points owned by the mounted viewer across package boundaries', () => {
    class ViewerPoint {
      constructor(x, y) {
        this.x = x;
        this.y = y;
      }
    }
    const isViewerPoint = (point) => point instanceof ViewerPoint;
    const image = {
      imageToViewerElementCoordinates: (point) => (
        isViewerPoint(point) ? new ViewerPoint(point.x / 2, point.y / 3) : new ViewerPoint(NaN, NaN)
      ),
      viewerElementToImageCoordinates: (point) => (
        isViewerPoint(point) ? new ViewerPoint(point.x * 2, point.y * 3) : new ViewerPoint(0, 0)
      ),
      windowToImageCoordinates: (point) => (
        isViewerPoint(point) ? new ViewerPoint(point.x * 4, point.y * 5) : new ViewerPoint(0, 0)
      ),
    };
    const viewer = {
      viewport: { getCenter: () => new ViewerPoint(0, 0) },
      world: { getItemAt: () => image, getItemCount: () => 1 },
    };

    expect(viewerElementPointToImage(viewer, { x: 4, y: 5 })).toMatchObject({ x: 8, y: 15 });
    expect(imagePointToViewerElement(viewer, 8, 15)).toMatchObject({ x: 4, y: 5 });
    expect(clientPointToImage(viewer, 7, 11, { x: 0, y: 0 })).toMatchObject({ x: 28, y: 55 });
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

  it('keeps an adjacent draft word inside its containing line at the image edge', () => {
    expect(wordBBoxBesideSelection(
      { x: 80, y: 20, w: 10, h: 12 },
      { x: 10, y: 18, w: 100, h: 16 },
    )).toEqual({ x: 90, y: 20, w: 10, h: 12 });
    expect(wordBBoxBesideSelection(
      { x: 100, y: 20, w: 10, h: 12 },
      { x: 10, y: 18, w: 100, h: 16 },
    )).toEqual({ x: 109, y: 20, w: 1, h: 12 });
  });

  it('requires the integer word center to be inside both line axes', () => {
    const lineBBox = { x: 10, y: 10, w: 100, h: 20 };
    expect(imageBBoxContainsCenter(lineBBox, { x: 100, y: 20, w: 20, h: 1 })).toBe(true);
    expect(imageBBoxContainsCenter(lineBBox, { x: 110, y: 20, w: 2, h: 1 })).toBe(false);
    expect(imageBBoxContainsCenter(lineBBox, { x: 20, y: 30, w: 1, h: 2 })).toBe(false);
  });
});
