// @vitest-environment happy-dom

import { act } from 'react';
import { createRoot } from 'react-dom/client';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { ScribeCompanionWindow } from './ScribeCompanionWindow';

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
  root = undefined;
  container = undefined;
  remoteRebaseListener = undefined;
  vi.restoreAllMocks();
});

describe('ScribeCompanionWindow durable transcription guard', () => {
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
});
