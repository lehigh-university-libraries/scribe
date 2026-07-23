import {
  annotationPageForCanvas,
  canvasIdForWindow,
  findAnnotationPageByAnnotationId,
  findCanvasIdByAnnotationId,
} from '../utils/iiif';
import type {
  IIIFAnnotationPage,
  MiradorState,
  ScribeAdapterFactory,
} from '../types/scribe';

export interface ActiveCanvasEventDetail {
  canvasId: string;
  itemImageId: string;
  windowId: string;
}

export interface ActiveCanvasState {
  canvasId: string;
  serverPage: IIIFAnnotationPage | null;
}

function adapterItemImageId(
  adapterFactory: ScribeAdapterFactory | null | undefined,
  canvasId: string,
): string {
  if (typeof adapterFactory !== 'function') return '';
  try {
    const raw = String(adapterFactory(canvasId)?.itemImageId || '').trim();
    return /^[1-9][0-9]*$/.test(raw) ? BigInt(raw).toString() : '';
  } catch {
    return '';
  }
}

export function activeCanvasEventDetail(
  adapterFactory: ScribeAdapterFactory | null | undefined,
  canvasId: string,
  windowId: string,
): ActiveCanvasEventDetail | null {
  if (!canvasId || !windowId) return null;
  return {
    canvasId,
    itemImageId: adapterItemImageId(adapterFactory, canvasId),
    windowId,
  };
}

export function dispatchActiveCanvasEvent(detail: ActiveCanvasEventDetail | null | undefined): boolean {
  if (!detail?.canvasId || !detail.windowId) return false;
  document.dispatchEvent(new CustomEvent('scribe:active-canvas', { detail }));
  return true;
}

/**
 * Mirador's current window Canvas is authoritative. The selected annotation
 * can briefly refer to the prior page during a page turn, so it is only a
 * compatibility fallback when the window has no Canvas identity yet.
 */
export function resolveActiveCanvasId(
  state: MiradorState | null | undefined,
  windowId: string,
  selectedAnnotationId: string,
): string {
  return canvasIdForWindow(state, windowId)
    || findCanvasIdByAnnotationId(state, selectedAnnotationId);
}

export function resolveActiveCanvasState(
  state: MiradorState | null | undefined,
  windowId: string,
  selectedAnnotationId: string,
): ActiveCanvasState {
  const canvasId = resolveActiveCanvasId(state, windowId, selectedAnnotationId);
  return {
    canvasId,
    serverPage: canvasId
      ? (annotationPageForCanvas(state, canvasId) || null)
      : findAnnotationPageByAnnotationId(state, selectedAnnotationId),
  };
}
