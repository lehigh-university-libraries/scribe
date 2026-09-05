// @vitest-environment happy-dom

import { act, useCallback, useState } from 'react';
import { createRoot } from 'react-dom/client';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { ScribeCompanionWindow } from './ScribeCompanionWindow';
import { annotationBBox } from '../utils/iiif';

globalThis.IS_REACT_ACT_ENVIRONMENT = true;

vi.mock('mirador', () => ({
  ConnectedCompanionWindow: ({ children }) => <section>{children}</section>,
  receiveAnnotation: vi.fn(),
  selectAnnotation: vi.fn(),
}));

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key) => ({
      scribeEditorTranscribe: 'Retranscribe',
      scribeEditorTranscribeDialogTitle: 'Retranscribe text',
      scribeEditorTranscribeSelected: 'Retranscribe selected',
    })[key] || key,
  }),
}));

const canvasId = 'https://example.test/canvas/1';
const windowId = 'window-1';
let container;
let editorStateListener;
let remoteRebaseListener;
let root;

function line() {
  return {
    body: [{ purpose: 'supplementing', type: 'TextualBody', value: 'one line' }],
    id: 'line-1',
    target: `${canvasId}#xywh=pixel:10,10,100,20`,
    textGranularity: 'line',
    type: 'Annotation',
  };
}

function page() {
  return {
    id: 'https://scribe.test/presentation/v3/item-image-41/canvas/page-1/annotations',
    items: [line()],
    type: 'AnnotationPage',
  };
}

function pressRetranscribeShortcut() {
  window.dispatchEvent(new KeyboardEvent('keydown', {
    altKey: true,
    bubbles: true,
    key: 'r',
  }));
}

afterEach(async () => {
  if (root) await act(async () => root.unmount());
  container?.remove();
  if (remoteRebaseListener) {
    document.removeEventListener('scribe:remote-rebase-ready', remoteRebaseListener);
  }
  if (editorStateListener) {
    document.removeEventListener('scribe:editor-state', editorStateListener);
  }
  root = undefined;
  container = undefined;
  editorStateListener = undefined;
  remoteRebaseListener = undefined;
  vi.restoreAllMocks();
});

describe('ScribeCompanionWindow', () => {
  it('keeps a delayed annotation bootstrap alive across its loading rerender', async () => {
    const canonicalPage = page();
    let resolveLoad;
    const load = new Promise((resolve) => {
      resolveLoad = resolve;
    });
    const adapter = {
      itemImageId: '41',
      loadSnapshot: vi.fn(() => load),
      transcribeAnnotation: vi.fn(),
    };
    let latestState;
    editorStateListener = (event) => {
      latestState = event.detail;
    };
    document.addEventListener('scribe:editor-state', editorStateListener);
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);

    await act(async () => root.render(<ScribeCompanionWindow
      adapterFactory={() => adapter}
      canvasId={canvasId}
      id="companion-1"
      isFocusedWindow
      receiveAnnotation={vi.fn()}
      selectAnnotation={vi.fn()}
      selectedAnnotationId="line-1"
      serverPage={canonicalPage}
      windowId={windowId}
    />));
    await vi.waitFor(() => expect(latestState?.sessionStatus).toBe('loading'));
    await act(async () => new Promise((resolve) => setTimeout(resolve, 0)));
    expect(adapter.loadSnapshot).toHaveBeenCalledOnce();

    await act(async () => {
      resolveLoad({ page: canonicalPage, revision: '7' });
      await load;
    });
    await vi.waitFor(() => {
      expect(latestState).toMatchObject({
        hasRevision: true,
        isBusy: false,
        sessionStatus: 'ready',
      });
    });
    expect(adapter.loadSnapshot).toHaveBeenCalledOnce();
    expect(document.querySelector('button[aria-label="Publish edits"]')?.disabled).toBe(false);
  });

  it('keeps loose-word placement adjacent when no line owns the word', async () => {
    const terminalWord = {
      body: [{ purpose: 'supplementing', type: 'TextualBody', value: 'loose' }],
      id: 'word-loose',
      target: `${canvasId}#xywh=pixel:512,10,128,20`,
      textGranularity: 'word',
      type: 'Annotation',
    };
    const canonicalPage = { ...page(), items: [terminalWord] };
    const adapter = {
      itemImageId: '41',
      loadSnapshot: vi.fn(async () => ({ page: canonicalPage, revision: '1' })),
      transcribeAnnotation: vi.fn(),
    };
    let latestPage = canonicalPage;
    editorStateListener = (event) => {
      latestPage = event.detail?.annotationPage || latestPage;
    };
    document.addEventListener('scribe:editor-state', editorStateListener);
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);

    await act(async () => root.render(<ScribeCompanionWindow
      adapterFactory={() => adapter}
      canvasId={canvasId}
      id="companion-1"
      isFocusedWindow
      receiveAnnotation={vi.fn()}
      selectAnnotation={vi.fn()}
      selectedAnnotationId="word-loose"
      serverPage={canonicalPage}
      windowId={windowId}
    />));
    await act(async () => container.querySelector(
      'button[aria-label="Add a word annotation beside the selection"]',
    )?.click());
    await vi.waitFor(() => expect(latestPage.items).toHaveLength(2));
    const addedWord = latestPage.items.find(({ id }) => id !== 'word-loose');
    expect(annotationBBox(addedWord)).toEqual({ x: 640, y: 10, w: 128, h: 20 });
  });

  it('does not treat a horizontally loose same-row word as line-owned', async () => {
    const looseWord = {
      body: [{ purpose: 'supplementing', type: 'TextualBody', value: 'loose' }],
      id: 'word-loose',
      target: `${canvasId}#xywh=pixel:200,10,20,20`,
      textGranularity: 'word',
      type: 'Annotation',
    };
    const canonicalPage = {
      ...page(),
      items: [{ ...line(), target: `${canvasId}#xywh=pixel:0,10,100,20` }, looseWord],
    };
    const adapter = {
      itemImageId: '41',
      loadSnapshot: vi.fn(async () => ({ page: canonicalPage, revision: '1' })),
      transcribeAnnotation: vi.fn(),
    };
    let latestPage = canonicalPage;
    editorStateListener = (event) => {
      latestPage = event.detail?.annotationPage || latestPage;
    };
    document.addEventListener('scribe:editor-state', editorStateListener);
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);

    await act(async () => root.render(<ScribeCompanionWindow
      adapterFactory={() => adapter}
      canvasId={canvasId}
      id="companion-1"
      isFocusedWindow
      receiveAnnotation={vi.fn()}
      selectAnnotation={vi.fn()}
      selectedAnnotationId="word-loose"
      serverPage={canonicalPage}
      windowId={windowId}
    />));
    await act(async () => container.querySelector(
      'button[aria-label="Add a word annotation beside the selection"]',
    )?.click());
    await vi.waitFor(() => expect(latestPage.items).toHaveLength(3));
    const addedWord = latestPage.items.find(({ id }) => !['line-1', 'word-loose'].includes(id));
    expect(annotationBBox(addedWord)).toEqual({ x: 220, y: 10, w: 20, h: 20 });
  });

  it('keeps a word added after the terminal word inside the owning line', async () => {
    const terminalWord = {
      body: [{ purpose: 'supplementing', type: 'TextualBody', value: 'gamma' }],
      id: 'word-gamma',
      target: `${canvasId}#xywh=pixel:512,10,128,20`,
      textGranularity: 'word',
      type: 'Annotation',
    };
    const canonicalPage = {
      ...page(),
      items: [{
        ...line(),
        body: [{ purpose: 'supplementing', type: 'TextualBody', value: 'gamma' }],
        target: `${canvasId}#xywh=pixel:0,10,640,20`,
      }, terminalWord],
    };
    const adapter = {
      itemImageId: '41',
      loadSnapshot: vi.fn(async () => ({ page: canonicalPage, revision: '1' })),
      transcribeAnnotation: vi.fn(),
    };
    let latestPage = canonicalPage;
    editorStateListener = (event) => {
      latestPage = event.detail?.annotationPage || latestPage;
    };
    document.addEventListener('scribe:editor-state', editorStateListener);
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);

    await act(async () => root.render(<ScribeCompanionWindow
      adapterFactory={() => adapter}
      canvasId={canvasId}
      id="companion-1"
      isFocusedWindow
      receiveAnnotation={vi.fn()}
      selectAnnotation={vi.fn()}
      selectedAnnotationId="word-gamma"
      serverPage={canonicalPage}
      windowId={windowId}
    />));
    await act(async () => container.querySelector(
      'button[aria-label="Add a word annotation beside the selection"]',
    )?.click());
    await vi.waitFor(() => expect(latestPage.items).toHaveLength(3));
    const addedWord = latestPage.items.find(({ id }) => !['line-1', 'word-gamma'].includes(id));
    expect(annotationBBox(addedWord)).toEqual({ x: 639, y: 10, w: 1, h: 20 });
  });

  it('blocks Alt+R during the replayed durable batch and releases it after completion', async () => {
    const canonicalPage = page();
    const adapter = {
      itemImageId: '41',
      loadSnapshot: vi.fn(async () => ({ page: canonicalPage, revision: '1' })),
      transcribeAnnotation: vi.fn(),
    };
    let replayActive = true;
    remoteRebaseListener = (event) => {
      document.dispatchEvent(new CustomEvent('scribe:transcription-job-state', {
        detail: {
          ...event.detail,
          active: replayActive,
          message: replayActive
            ? 'Automatic transcription in progress'
            : 'Automatic transcription complete',
        },
      }));
    };
    document.addEventListener('scribe:remote-rebase-ready', remoteRebaseListener);
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);

    await act(async () => root.render(<ScribeCompanionWindow
      adapterFactory={() => adapter}
      canvasId={canvasId}
      id="companion-1"
      isFocusedWindow
      receiveAnnotation={vi.fn()}
      selectAnnotation={vi.fn()}
      selectedAnnotationId="line-1"
      serverPage={canonicalPage}
      windowId={windowId}
    />));

    await act(async () => pressRetranscribeShortcut());
    expect(document.body.textContent).not.toContain('Retranscribe entire page');

    replayActive = false;
    await act(async () => document.dispatchEvent(new CustomEvent('scribe:transcription-job-state', {
      detail: {
        active: false,
        canvasId,
        itemImageId: '41',
        message: 'Automatic transcription complete',
        windowId,
      },
    })));
    await act(async () => pressRetranscribeShortcut());

    expect(document.body.textContent).toContain('Retranscribe entire page');
  });

  it('restores the selected draft when redo brings a created line back', async () => {
    const canonicalPage = page();
    const selectedAnnotationIds = [];
    const adapter = {
      itemImageId: '41',
      loadSnapshot: vi.fn(async () => ({ page: canonicalPage, revision: '1' })),
      transcribeAnnotation: vi.fn(),
    };
    const adapterFactory = () => adapter;
    const receiveAnnotation = vi.fn();
    function ControlledCompanionWindow() {
      const [selectedAnnotationId, setSelectedAnnotationId] = useState('line-1');
      const selectAnnotation = useCallback((
        /** @type {string} */ _selectedWindowId,
        /** @type {string} */ annotationId,
      ) => {
        selectedAnnotationIds.push(annotationId);
        setSelectedAnnotationId(annotationId);
      }, []);
      return <ScribeCompanionWindow
        adapterFactory={adapterFactory}
        canvasId={canvasId}
        id="companion-1"
        isFocusedWindow
        receiveAnnotation={receiveAnnotation}
        selectAnnotation={selectAnnotation}
        selectedAnnotationId={selectedAnnotationId}
        serverPage={canonicalPage}
        windowId={windowId}
      />;
    }
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);

    await act(async () => root.render(<ControlledCompanionWindow />));
    await act(async () => document.dispatchEvent(new CustomEvent('scribe:create-annotation', {
      detail: {
        bbox: { h: 20, w: 100, x: 20, y: 40 },
        canvasId,
        focusResizeHandle: 'se',
        windowId,
      },
    })));
    await vi.waitFor(() => {
      expect(selectedAnnotationIds.at(-1)).toEqual(expect.any(String));
      expect(selectedAnnotationIds.at(-1)).not.toBe('line-1');
    });
    const createdAnnotationId = selectedAnnotationIds.at(-1);

    await act(async () => container.querySelector('button[aria-label="scribeEditorUndo"]')?.click());
    await vi.waitFor(() => expect(selectedAnnotationIds.at(-1)).toBe('line-1'));
    await act(async () => container.querySelector('button[aria-label="scribeEditorRedo"]')?.click());

    await vi.waitFor(() => expect(selectedAnnotationIds.at(-1)).toBe(createdAnnotationId));
  });
});
