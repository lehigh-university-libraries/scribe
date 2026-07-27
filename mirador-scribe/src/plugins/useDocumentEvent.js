import { useEffect, useRef } from 'react';

/**
 * Registers one stable document listener while routing events to the latest
 * render callback. Bridge hooks can therefore keep listener lifecycle separate
 * from the editor state they consume.
 *
 * @param {string} eventName
 * @param {(event: Event) => void} handler
 */
export function useDocumentEvent(eventName, handler) {
  const handlerRef = useRef(handler);
  handlerRef.current = handler;

  useEffect(() => {
    /** @param {Event} event */
    const listener = (event) => handlerRef.current(event);
    document.addEventListener(eventName, listener);
    return () => document.removeEventListener(eventName, listener);
  }, [eventName]);
}

/**
 * @typedef {Object} EditorBridgeEventDetail
 * @property {boolean} [active]
 * @property {import('../types/scribe').IIIFAnnotation} [annotation]
 * @property {string} [annotationId]
 * @property {import('../types/scribe').ImageBBox} [bbox]
 * @property {import('../types/scribe').ImageBBox} [bounds]
 * @property {number} [direction]
 * @property {string} [focusAnnotationId]
 * @property {'nw' | 'ne' | 'sw' | 'se'} [focusResizeHandle]
 * @property {string | number | bigint} [itemImageId]
 * @property {string} [message]
 * @property {'create' | 'update' | 'delete'} [operation]
 * @property {boolean} [persisted]
 * @property {string} [requestId]
 * @property {(result: import('../types/scribe').DraftMutationResponse) => void} [respond]
 * @property {number | null} [selectionStart]
 * @property {string} [text]
 * @property {string} [canvasId]
 * @property {string} [windowId]
 */

/** @param {Event} event @returns {EditorBridgeEventDetail} */
export function editorBridgeEventDetail(event) {
  return /** @type {CustomEvent<EditorBridgeEventDetail>} */ (event).detail || {};
}
