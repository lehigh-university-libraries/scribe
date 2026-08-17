import type OpenSeadragon from 'openseadragon';
import type { ImageBBox, Point2D } from '../types/scribe';

interface ImageSize {
  height: number;
  width: number;
}

function tiledImage(viewer: OpenSeadragon.Viewer | null | undefined): OpenSeadragon.TiledImage | null {
  if (!viewer?.viewport || !viewer.world?.getItemCount()) return null;
  return viewer.world.getItemAt(0) || null;
}

/**
 * Build a point with the same OpenSeadragon class identity as the mounted
 * viewer. Mirador and this plugin may bundle separate OpenSeadragon copies;
 * conversion APIs reject a structurally identical Point from the other copy.
 */
function viewerOwnedPoint(
  viewer: OpenSeadragon.Viewer | null | undefined,
  x: number,
  y: number,
): OpenSeadragon.Point | null {
  const center = viewer?.viewport?.getCenter?.(true);
  if (!center) return null;
  const point = typeof center.clone === 'function' ? center.clone() : center;
  point.x = x;
  point.y = y;
  return point;
}

function finitePoint(point: OpenSeadragon.Point | null | undefined): OpenSeadragon.Point | null {
  return point && Number.isFinite(point.x) && Number.isFinite(point.y) ? point : null;
}

/** Convert browser client coordinates to image coordinates exactly once. */
export function clientPointToImage(
  viewer: OpenSeadragon.Viewer | null | undefined,
  clientX: number,
  clientY: number,
  scroll: Point2D | null = null,
): OpenSeadragon.Point | null {
  const image = tiledImage(viewer);
  if (!image?.windowToImageCoordinates) return null;
  const pageScroll = scroll || {
    x: globalThis.window?.scrollX || 0,
    y: globalThis.window?.scrollY || 0,
  };
  const point = viewerOwnedPoint(
    viewer,
    clientX + pageScroll.x,
    clientY + pageScroll.y,
  );
  return point ? finitePoint(image.windowToImageCoordinates(point)) : null;
}

/** Convert an OpenSeadragon MouseTracker element-relative point to image space. */
export function viewerElementPointToImage(
  viewer: OpenSeadragon.Viewer | null | undefined,
  point: Point2D | null | undefined,
): OpenSeadragon.Point | null {
  const image = tiledImage(viewer);
  if (!image?.viewerElementToImageCoordinates || !point) return null;
  const viewerPoint = viewerOwnedPoint(viewer, point.x, point.y);
  return viewerPoint ? finitePoint(image.viewerElementToImageCoordinates(viewerPoint)) : null;
}

/** Convert image coordinates to coordinates relative to the viewer element. */
export function imagePointToViewerElement(
  viewer: OpenSeadragon.Viewer | null | undefined,
  x: number,
  y: number,
): OpenSeadragon.Point | null {
  const image = tiledImage(viewer);
  if (!image?.imageToViewerElementCoordinates) return null;
  const imagePoint = viewerOwnedPoint(viewer, x, y);
  return imagePoint ? finitePoint(image.imageToViewerElementCoordinates(imagePoint)) : null;
}

export function normalizeImageBBox({ x, y, w, h }: ImageBBox): ImageBBox {
  let left = Number(x) || 0;
  let top = Number(y) || 0;
  let width = Number(w) || 0;
  let height = Number(h) || 0;
  if (width < 0) {
    left += width;
    width = Math.abs(width);
  }
  if (height < 0) {
    top += height;
    height = Math.abs(height);
  }
  return {
    x: Math.max(0, Math.round(left)),
    y: Math.max(0, Math.round(top)),
    w: Math.max(1, Math.round(width)),
    h: Math.max(1, Math.round(height)),
  };
}

/**
 * Places a new word after the selected word without leaving its containing
 * line. When the selection already reaches the right edge, retain a visible
 * one-pixel box at that edge so the draft stays inside the canonical image.
 */
export function wordBBoxBesideSelection(
  selection: ImageBBox,
  containingLine: ImageBBox,
): ImageBBox {
  const selected = normalizeImageBBox(selection);
  const container = normalizeImageBBox(containingLine);
  const containerRight = container.x + container.w;
  const containerBottom = container.y + container.h;
  const y = Math.max(container.y, Math.min(selected.y, containerBottom - 1));
  const h = Math.max(1, Math.min(selected.h, containerBottom - y));
  const adjacentX = Math.max(container.x, selected.x + selected.w);
  if (adjacentX < containerRight) {
    return {
      x: adjacentX,
      y,
      w: Math.max(1, Math.min(selected.w, containerRight - adjacentX)),
      h,
    };
  }
  return {
    x: Math.max(container.x, containerRight - 1),
    y,
    w: 1,
    h,
  };
}

/** Match the backend's integer word-center containment rule. */
export function imageBBoxContainsCenter(containerBBox: ImageBBox, itemBBox: ImageBBox): boolean {
  const container = normalizeImageBBox(containerBBox);
  const item = normalizeImageBBox(itemBBox);
  const centerX = item.x + Math.floor(item.w / 2);
  const centerY = item.y + Math.floor(item.h / 2);
  return centerX >= container.x
    && centerX <= container.x + container.w
    && centerY >= container.y
    && centerY <= container.y + container.h;
}

/**
 * Build a visible, line-shaped rectangle in the center of the current image
 * viewport. This is the keyboard alternative to pointer drag creation.
 */
export function initialLineBBoxForViewport(
  bounds: ImageBBox | null | undefined,
  imageSize: ImageSize | null | undefined,
): ImageBBox | null {
  if (!bounds) return null;
  const values = [bounds.x, bounds.y, bounds.w, bounds.h];
  if (!values.every(Number.isFinite) || bounds.w <= 0 || bounds.h <= 0) return null;

  const imageWidth = Number(imageSize?.width);
  const imageHeight = Number(imageSize?.height);
  if (!Number.isFinite(imageWidth) || imageWidth <= 0
    || !Number.isFinite(imageHeight) || imageHeight <= 0) return null;

  const left = Math.max(0, Math.min(imageWidth, bounds.x));
  const top = Math.max(0, Math.min(imageHeight, bounds.y));
  const right = Math.max(left, Math.min(imageWidth, bounds.x + bounds.w));
  const bottom = Math.max(top, Math.min(imageHeight, bounds.y + bounds.h));
  const visibleWidth = right - left;
  const visibleHeight = bottom - top;
  if (visibleWidth < 12 || visibleHeight < 12) return null;

  const width = Math.max(12, visibleWidth * 0.72);
  const height = Math.max(12, Math.min(80, visibleHeight * 0.12));
  return normalizeImageBBox({
    h: Math.min(visibleHeight, height),
    w: Math.min(visibleWidth, width),
    x: left + ((visibleWidth - Math.min(visibleWidth, width)) / 2),
    y: top + ((visibleHeight - Math.min(visibleHeight, height)) / 2),
  });
}
