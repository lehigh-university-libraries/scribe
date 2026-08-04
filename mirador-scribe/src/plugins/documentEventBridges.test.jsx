// @vitest-environment happy-dom

import { act } from 'react';
import { createRoot } from 'react-dom/client';
import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  createEditorSessionCache,
  editorSessionCacheReducer,
  editorSessionForCanvas,
} from '../editor/sessionCache';
import { useAnnotationCreationBridge } from './useAnnotationCreationBridge';
import { useAnnotationMutationBridge } from './useAnnotationMutationBridge';
import { useDocumentEvent } from './useDocumentEvent';
import { useEditorRequestBridge, useViewportBridge } from './useEditorRequestBridge';
import { useInlineEditorBridge } from './useInlineEditorBridge';
import { useRemoteAnnotationRebase } from './useRemoteAnnotationRebase';

globalThis.IS_REACT_ACT_ENVIRONMENT = true;

const canvasId = 'https://example.test/canvas/1';
const windowId = 'window-1';

let container;
let root;

function annotation(id, text, textGranularity = 'line', x = 10, y = 10) {
  return {
    body: [{ purpose: 'supplementing', type: 'TextualBody', value: text }],
    id,
    target: `${canvasId}#xywh=pixel:${x},${y},80,20`,
    textGranularity,
    type: 'Annotation',
  };
}

function page(items) {
  return {
    id: 'https://triplet.test/presentation/item-image-1/canvas/page-1/annotations',
    items,
    type: 'AnnotationPage',
  };
}

function mount(element) {
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
  act(() => root.render(element));
}

function dispatch(name, detail) {
  act(() => document.dispatchEvent(new CustomEvent(name, { detail })));
}

function DocumentEventHarness({ handler }) {
  useDocumentEvent('scribe:test-document-listener', handler);
  return null;
}

function CreationBridgeHarness({ options }) {
  useAnnotationCreationBridge(options);
  return null;
}

function InlineBridgeHarness({ options }) {
  useInlineEditorBridge(options);
  return null;
}

function MutationBridgeHarness({ options }) {
  useAnnotationMutationBridge(options);
  return null;
}

function RequestBridgeHarness({ options }) {
  useEditorRequestBridge(options);
  return null;
}

function RemoteRebaseHarness({ options }) {
  useRemoteAnnotationRebase(options);
  return null;
}

function ViewportBridgeHarness({ options }) {
  useViewportBridge(options);
  return null;
}

afterEach(() => {
  if (root) act(() => root.unmount());
  container?.remove();
  root = undefined;
  container = undefined;
  vi.restoreAllMocks();
});

describe('document event bridges', () => {
  it('announces remote-rebase readiness only after its reload listener is mounted', () => {
    const emptyPage = page([]);
    const cache = createEditorSessionCache(canvasId, emptyPage);
    const adapter = { itemImageId: '41', loadSnapshot: vi.fn() };
    const reloadAnnotations = vi.fn(async () => {});
    const ready = vi.fn((event) => {
      document.dispatchEvent(new CustomEvent('scribe:reload-annotations', {
        detail: event.detail,
      }));
    });
    document.addEventListener('scribe:remote-rebase-ready', ready);

    mount(<RemoteRebaseHarness options={{
      adapterFactory: () => adapter,
      canvasId,
      dispatchSession: () => cache,
      reloadAnnotations,
      session: editorSessionForCanvas(cache, canvasId),
      setStatusMessage: vi.fn(),
      syncPage: vi.fn(async () => {}),
      windowId,
    }} />);

    expect(ready).toHaveBeenCalledOnce();
    expect(ready.mock.calls[0][0].detail).toEqual({
      canvasId,
      itemImageId: '41',
      windowId,
    });
    expect(reloadAnnotations).toHaveBeenCalledWith(adapter, canvasId);
    document.removeEventListener('scribe:remote-rebase-ready', ready);
  });

  it('registers once, routes through the latest callback, and removes the exact listener', () => {
    const addListener = vi.spyOn(document, 'addEventListener');
    const removeListener = vi.spyOn(document, 'removeEventListener');
    const firstHandler = vi.fn();
    const latestHandler = vi.fn();

    mount(<DocumentEventHarness handler={firstHandler} />);
    const registrations = addListener.mock.calls.filter(([name]) => name === 'scribe:test-document-listener');
    expect(registrations).toHaveLength(1);
    const registeredListener = registrations[0][1];

    act(() => root.render(<DocumentEventHarness handler={latestHandler} />));
    expect(addListener.mock.calls.filter(([name]) => name === 'scribe:test-document-listener')).toHaveLength(1);
    dispatch('scribe:test-document-listener', { value: 'latest' });
    expect(firstHandler).not.toHaveBeenCalled();
    expect(latestHandler).toHaveBeenCalledOnce();

    act(() => root.unmount());
    root = undefined;
    expect(removeListener).toHaveBeenCalledWith('scribe:test-document-listener', registeredListener);
    dispatch('scribe:test-document-listener', { value: 'after-unmount' });
    expect(latestHandler).toHaveBeenCalledOnce();
  });

  it('registers and cleans up every companion-window document bridge listener', () => {
    const addListener = vi.spyOn(document, 'addEventListener');
    const removeListener = vi.spyOn(document, 'removeEventListener');
    const emptyPage = page([]);
    const noop = vi.fn();
    const creationOptions = {
      canvasId,
      editingIsBlocked: () => false,
      localPage: emptyPage,
      pushHistory: noop,
      selectAnnotation: noop,
      setDrawMode: noop,
      setOverlayMode: noop,
      setStatusMessage: noop,
      windowId,
    };
    const inlineOptions = {
      canvasId,
      effectiveSelectedAnnotationId: '',
      editingIsBlocked: () => false,
      handleSave: noop,
      localPage: emptyPage,
      pushHistory: noop,
      selectedAnnotation: null,
      selectAnnotation: noop,
      serverPage: null,
      setDrawMode: noop,
      setFocusedWordAnnotationId: noop,
      setOverlayMode: noop,
      setStatusMessage: noop,
      visibleRows: [],
      windowId,
    };
    const sessionCacheRef = { current: createEditorSessionCache(canvasId, emptyPage) };
    const mutationOptions = {
      adapterFactory: () => ({ itemImageId: '1' }),
      canvasId,
      dispatchSessionForCanvas: () => sessionCacheRef.current,
      editingIsBlocked: () => false,
      isFocusedWindow: true,
      preferredSelectionRef: { current: '' },
      receiveAnnotation: noop,
      selectAnnotation: noop,
      sessionCacheRef,
      setStatusMessage: noop,
      windowId,
    };
    const requestOptions = {
      canvasId,
      handleTranscribe: noop,
      performSaveAllDirty: async () => ({ ok: true, remainingCanvasIds: [] }),
      windowId,
    };
    mount(<>
      <CreationBridgeHarness options={creationOptions} />
      <InlineBridgeHarness options={inlineOptions} />
      <MutationBridgeHarness options={mutationOptions} />
      <RequestBridgeHarness options={requestOptions} />
      <ViewportBridgeHarness options={{ canvasId, setViewportBounds: noop, windowId }} />
    </>);

    const eventNames = [
      'scribe:create-annotation',
      'scribe:inline-change-text',
      'scribe:inline-change-word',
      'scribe:inline-step-selection',
      'scribe:inline-save',
      'scribe:select-annotation',
      'scribe:resize-annotation',
      'scribe:annotation-mutation',
      'scribe:request-save',
      'scribe:request-transcribe-all',
      'scribe:viewport-change',
    ];
    const registrations = new Map(eventNames.map((eventName) => [
      eventName,
      addListener.mock.calls.find(([registeredName]) => registeredName === eventName)?.[1],
    ]));
    registrations.forEach((listener) => expect(listener).toEqual(expect.any(Function)));

    act(() => root.unmount());
    root = undefined;
    registrations.forEach((listener, eventName) => {
      expect(removeListener).toHaveBeenCalledWith(eventName, listener);
    });
  });

  it('creates a draft only for the exact editable window and Canvas payload', () => {
    let blocked = false;
    const pushHistory = vi.fn();
    const selectAnnotation = vi.fn();
    const setDrawMode = vi.fn();
    const setOverlayMode = vi.fn();
    const setStatusMessage = vi.fn();
    const focusResizeHandle = vi.fn();
    document.addEventListener('scribe:focus-resize-handle', focusResizeHandle);
    const options = {
      canvasId,
      editingIsBlocked: () => blocked,
      localPage: page([]),
      pushHistory,
      selectAnnotation,
      setDrawMode,
      setOverlayMode,
      setStatusMessage,
      windowId,
    };
    mount(<CreationBridgeHarness options={options} />);

    dispatch('scribe:create-annotation', {
      bbox: { h: 20, w: 100, x: 3, y: 4 },
      canvasId: 'https://example.test/canvas/other',
      windowId,
    });
    dispatch('scribe:create-annotation', {
      bbox: { h: 20, w: 100, x: 3, y: 4 },
      canvasId,
      windowId: 'window-other',
    });
    blocked = true;
    dispatch('scribe:create-annotation', {
      bbox: { h: 20, w: 100, x: 3, y: 4 },
      canvasId,
      windowId,
    });
    expect(pushHistory).not.toHaveBeenCalled();

    blocked = false;
    dispatch('scribe:create-annotation', {
      bbox: { h: 20, w: 100, x: 3, y: 4 },
      canvasId,
      focusResizeHandle: 'se',
      windowId,
    });

    expect(pushHistory).toHaveBeenCalledOnce();
    const created = pushHistory.mock.calls[0][0].items[0];
    expect(created).toEqual(expect.objectContaining({ textGranularity: 'line' }));
    expect(created.target).toEqual(expect.objectContaining({
      selector: expect.objectContaining({ value: 'xywh=3,4,100,20' }),
      source: expect.objectContaining({ id: canvasId }),
    }));
    expect(selectAnnotation).toHaveBeenCalledWith(windowId, created.id);
    expect(setDrawMode).toHaveBeenCalledWith(false);
    expect(setOverlayMode).toHaveBeenCalledWith('edit');
    expect(focusResizeHandle).toHaveBeenCalledWith(expect.objectContaining({
      detail: {
        annotationId: created.id,
        canvasId,
        handle: 'se',
        windowId,
      },
    }));
    expect(setStatusMessage).toHaveBeenCalledWith(expect.stringContaining('resize handle is focused'));
    document.removeEventListener('scribe:focus-resize-handle', focusResizeHandle);
  });

  it('routes inline text and save payloads only to their exact Canvas scope', () => {
    const line = annotation('line-1', 'before');
    const pushHistory = vi.fn();
    const handleSave = vi.fn();
    const options = {
      canvasId,
      effectiveSelectedAnnotationId: line.id,
      editingIsBlocked: () => false,
      handleSave,
      localPage: page([line]),
      pushHistory,
      selectedAnnotation: line,
      selectAnnotation: vi.fn(),
      serverPage: null,
      setDrawMode: vi.fn(),
      setFocusedWordAnnotationId: vi.fn(),
      setOverlayMode: vi.fn(),
      setStatusMessage: vi.fn(),
      visibleRows: [{ fields: [line], granularity: 'line', id: line.id, lead: line }],
      windowId,
    };
    mount(<InlineBridgeHarness options={options} />);

    dispatch('scribe:inline-change-text', {
      canvasId: 'https://example.test/canvas/other',
      text: 'wrong Canvas',
      windowId,
    });
    dispatch('scribe:inline-change-text', {
      canvasId,
      text: 'wrong window',
      windowId: 'window-other',
    });
    expect(pushHistory).not.toHaveBeenCalled();

    dispatch('scribe:inline-change-text', { canvasId, text: 'updated line', windowId });
    expect(pushHistory).toHaveBeenCalledOnce();
    expect(pushHistory.mock.calls[0][0].items[0].body[0].value).toBe('updated line');

    dispatch('scribe:inline-save', { canvasId, windowId: 'window-other' });
    expect(handleSave).not.toHaveBeenCalled();
    dispatch('scribe:inline-save', { canvasId, windowId });
    expect(handleSave).toHaveBeenCalledOnce();
  });

  it('fences selection, stepping, and geometry mutation while editing is blocked', () => {
    let blocked = true;
    const lineOne = annotation('line-1', 'one', 'line', 10, 10);
    const wordOne = annotation('word-1', 'one', 'word', 10, 10);
    const lineTwo = annotation('line-2', 'two', 'line', 10, 50);
    const pushHistory = vi.fn();
    const selectAnnotation = vi.fn();
    const setDrawMode = vi.fn();
    const setFocusedWordAnnotationId = vi.fn();
    const setOverlayMode = vi.fn();
    const options = {
      canvasId,
      effectiveSelectedAnnotationId: lineOne.id,
      editingIsBlocked: () => blocked,
      handleSave: vi.fn(),
      localPage: page([lineOne, wordOne, lineTwo]),
      pushHistory,
      selectedAnnotation: lineOne,
      selectAnnotation,
      serverPage: null,
      setDrawMode,
      setFocusedWordAnnotationId,
      setOverlayMode,
      setStatusMessage: vi.fn(),
      visibleRows: [
        { fields: [wordOne], granularity: 'word', id: lineOne.id, lead: lineOne },
        { fields: [lineTwo], granularity: 'line', id: lineTwo.id, lead: lineTwo },
      ],
      windowId,
    };
    mount(<InlineBridgeHarness options={options} />);

    dispatch('scribe:select-annotation', { annotationId: wordOne.id, canvasId, windowId });
    dispatch('scribe:inline-step-selection', { canvasId, direction: 1, windowId });
    dispatch('scribe:resize-annotation', {
      annotationId: lineOne.id,
      bbox: { h: 30, w: 120, x: 20, y: 25 },
      canvasId,
      windowId,
    });
    expect(selectAnnotation).not.toHaveBeenCalled();
    expect(pushHistory).not.toHaveBeenCalled();
    expect(setDrawMode).not.toHaveBeenCalled();

    blocked = false;
    dispatch('scribe:select-annotation', {
      annotationId: wordOne.id,
      canvasId: 'https://example.test/canvas/other',
      windowId,
    });
    expect(selectAnnotation).not.toHaveBeenCalled();

    dispatch('scribe:select-annotation', { annotationId: wordOne.id, canvasId, windowId });
    expect(selectAnnotation).toHaveBeenCalledWith(windowId, wordOne.id);
    expect(setFocusedWordAnnotationId).toHaveBeenCalledWith(wordOne.id);
    expect(setDrawMode).toHaveBeenCalledWith(false);
    expect(setOverlayMode).toHaveBeenCalledWith('edit');

    dispatch('scribe:inline-step-selection', { canvasId, direction: 1, windowId });
    expect(selectAnnotation).toHaveBeenLastCalledWith(windowId, lineTwo.id);

    dispatch('scribe:resize-annotation', {
      annotationId: lineOne.id,
      bbox: { h: 30, w: 120, x: 20, y: 25 },
      canvasId,
      windowId,
    });
    expect(pushHistory).toHaveBeenCalledOnce();
    expect(pushHistory.mock.calls[0][0].items[0].target).toContain('pixel:20,25,120,30');
  });

  it('routes adapter mutations only for the focused window, Canvas, and item image', () => {
    let blocked = false;
    const original = annotation('line-1', 'before');
    const updated = annotation('line-1', 'after');
    const sessionCacheRef = {
      current: createEditorSessionCache(canvasId, page([original]), '12'),
    };
    const dispatchSessionForCanvas = vi.fn((targetCanvasId, action) => {
      sessionCacheRef.current = editorSessionCacheReducer(sessionCacheRef.current, {
        ...action,
        canvasId: targetCanvasId,
      });
      return sessionCacheRef.current;
    });
    const preferredSelectionRef = { current: '' };
    const receiveAnnotation = vi.fn();
    const selectAnnotation = vi.fn();
    const setStatusMessage = vi.fn();
    const options = {
      adapterFactory: () => ({ itemImageId: '77' }),
      canvasId,
      dispatchSessionForCanvas,
      editingIsBlocked: () => blocked,
      isFocusedWindow: true,
      preferredSelectionRef,
      receiveAnnotation,
      selectAnnotation,
      sessionCacheRef,
      setStatusMessage,
      windowId,
    };
    mount(<MutationBridgeHarness options={options} />);

    const wrongScopeRespond = vi.fn();
    dispatch('scribe:annotation-mutation', {
      annotation: updated,
      canvasId: 'https://example.test/canvas/other',
      itemImageId: '77',
      operation: 'update',
      respond: wrongScopeRespond,
      windowId,
    });
    dispatch('scribe:annotation-mutation', {
      annotation: updated,
      canvasId,
      itemImageId: '78',
      operation: 'update',
      respond: wrongScopeRespond,
      windowId,
    });
    expect(wrongScopeRespond).not.toHaveBeenCalled();
    expect(dispatchSessionForCanvas).not.toHaveBeenCalled();

    blocked = true;
    const blockedRespond = vi.fn();
    dispatch('scribe:annotation-mutation', {
      annotation: updated,
      canvasId,
      itemImageId: '77',
      operation: 'update',
      respond: blockedRespond,
      windowId,
    });
    expect(blockedRespond).toHaveBeenCalledWith({
      error: expect.objectContaining({ message: expect.stringContaining('operation is in progress') }),
    });
    expect(dispatchSessionForCanvas).not.toHaveBeenCalled();

    blocked = false;
    const successRespond = vi.fn();
    dispatch('scribe:annotation-mutation', {
      annotation: updated,
      canvasId,
      itemImageId: '77',
      operation: 'update',
      respond: successRespond,
      windowId,
    });

    expect(dispatchSessionForCanvas).toHaveBeenCalledWith(canvasId, {
      page: expect.objectContaining({
        items: [expect.objectContaining({ body: [expect.objectContaining({ value: 'after' })] })],
      }),
      type: 'edit',
    });
    expect(receiveAnnotation).toHaveBeenCalledWith(
      canvasId,
      page([]).id,
      expect.objectContaining({ items: [expect.objectContaining({ id: original.id })] }),
    );
    expect(selectAnnotation).toHaveBeenCalledWith(windowId, updated.id);
    expect(preferredSelectionRef.current).toBe(updated.id);
    expect(successRespond).toHaveBeenCalledWith(expect.objectContaining({
      annotation: updated,
      page: expect.objectContaining({ items: [expect.objectContaining({ id: updated.id })] }),
      revision: '12',
    }));
    expect(setStatusMessage).toHaveBeenCalledWith('Draft updated. Save to persist it.');
  });

  it('correlates scoped save requests and routes transcribe-all commands', async () => {
    const performSaveAllDirty = vi.fn().mockResolvedValue({
      ok: true,
      remainingCanvasIds: ['https://example.test/canvas/2'],
    });
    const handleTranscribe = vi.fn();
    const saveResult = vi.fn();
    document.addEventListener('scribe:save-result', saveResult);
    mount(<RequestBridgeHarness options={{
      canvasId,
      handleTranscribe,
      performSaveAllDirty,
      windowId,
    }} />);

    dispatch('scribe:request-save', {
      canvasId: 'https://example.test/canvas/other',
      requestId: 'wrong',
      windowId,
    });
    expect(performSaveAllDirty).not.toHaveBeenCalled();

    await act(async () => {
      document.dispatchEvent(new CustomEvent('scribe:request-save', {
        detail: { canvasId, requestId: 'save-42', windowId },
      }));
      await Promise.resolve();
    });
    expect(performSaveAllDirty).toHaveBeenCalledOnce();
    expect(saveResult).toHaveBeenCalledWith(expect.objectContaining({
      detail: {
        canvasId,
        dirtyCanvasIds: ['https://example.test/canvas/2'],
        ok: true,
        requestId: 'save-42',
        windowId,
      },
    }));

    dispatch('scribe:request-transcribe-all', { canvasId, windowId: 'window-other' });
    expect(handleTranscribe).not.toHaveBeenCalled();
    dispatch('scribe:request-transcribe-all', { canvasId, windowId });
    expect(handleTranscribe).toHaveBeenCalledWith({ all: true });
    document.removeEventListener('scribe:save-result', saveResult);
  });

  it('accepts viewport bounds only from the exact window and Canvas', () => {
    const setViewportBounds = vi.fn();
    mount(<ViewportBridgeHarness options={{ canvasId, setViewportBounds, windowId }} />);
    dispatch('scribe:viewport-change', {
      bounds: { h: 40, w: 30, x: 10, y: 20 },
      canvasId: 'https://example.test/canvas/other',
      windowId,
    });
    expect(setViewportBounds).not.toHaveBeenCalled();
    dispatch('scribe:viewport-change', {
      bounds: { h: 40, w: 30, x: 10, y: 20 },
      canvasId,
      windowId,
    });
    expect(setViewportBounds).toHaveBeenCalledWith({ h: 40, w: 30, x: 10, y: 20 });
  });
});
