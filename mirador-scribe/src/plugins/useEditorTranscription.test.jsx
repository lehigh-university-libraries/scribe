// @vitest-environment happy-dom

import { act } from 'react';
import { createRoot } from 'react-dom/client';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { useEditorTranscription } from './useEditorTranscription';

globalThis.IS_REACT_ACT_ENVIRONMENT = true;

const canvasId = 'https://example.test/canvas/1';
let container;
let handleTranscribe;
let root;

function annotation(id, value) {
  return {
    body: [{ purpose: 'supplementing', type: 'TextualBody', value }],
    id,
    target: `${canvasId}#xywh=pixel:0,0,100,20`,
    textGranularity: 'line',
    type: 'Annotation',
  };
}

function page(items) {
  return {
    id: 'https://scribe.test/presentation/v3/item-image-1/canvas/page-1/annotations',
    items,
    type: 'AnnotationPage',
  };
}

function Harness({ options }) {
  ({ handleTranscribe } = useEditorTranscription(options));
  return null;
}

function mount(adapter, overrides = {}) {
  const annotations = [
    annotation('line-1', 'one'),
    annotation('line-2', 'two'),
    annotation('line-3', 'three'),
  ];
  const operationBusyRef = { current: false };
  const setStatusMessage = vi.fn();
  const applyTransformResult = vi.fn(() => ({ overlap: false }));
  const options = {
    activeCanvasRef: { current: canvasId },
    adapterFactory: () => adapter,
    applyTransformResult,
    canvasId,
    editingIsBlocked: () => false,
    localPage: page(annotations),
    mountedRef: { current: true },
    operationBusyRef,
    overlayMode: 'none',
    requireAdapter: () => adapter,
    selectedAnnotation: annotations[0],
    setDialogOpen: vi.fn(),
    setOperationBusy: (busy) => { operationBusyRef.current = busy; },
    setOperationBusyState: vi.fn(),
    setOverlayMode: vi.fn(),
    setStatusMessage,
    transcribeSelection: [],
    windowId: 'window-1',
    ...overrides,
  };
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
  act(() => root.render(<Harness options={options} />));
  return { annotations, applyTransformResult, options, setStatusMessage };
}

afterEach(() => {
  if (root) act(() => root.unmount());
  container?.remove();
  container = undefined;
  handleTranscribe = undefined;
  root = undefined;
  vi.restoreAllMocks();
});

describe('foreground editor transcription', () => {
  it('stops after the first terminal provider failure and reports the attempted result count', async () => {
    const failure = Object.assign(new Error('The transcription provider rejected the selected context.'), {
      scribeBatchDisposition: 'stop',
    });
    const adapter = { transcribeAnnotation: vi.fn().mockRejectedValue(failure) };
    const { applyTransformResult, setStatusMessage } = mount(adapter);

    await act(async () => handleTranscribe({ all: true }));

    expect(adapter.transcribeAnnotation).toHaveBeenCalledOnce();
    expect(applyTransformResult).not.toHaveBeenCalled();
    expect(setStatusMessage).toHaveBeenLastCalledWith(
      'Retranscription stopped after 0/3. The transcription provider rejected the selected context.',
    );
  });

  it('keeps completed draft results when a later provider failure stops the batch', async () => {
    const failure = Object.assign(new Error('The transcription provider is temporarily unavailable.'), {
      scribeBatchDisposition: 'stop',
    });
    const adapter = {
      transcribeAnnotation: vi.fn()
        .mockResolvedValueOnce(annotation('line-1', 'updated one'))
        .mockRejectedValueOnce(failure),
    };
    const { annotations, applyTransformResult, setStatusMessage } = mount(adapter);

    await act(async () => handleTranscribe({ all: true }));

    expect(adapter.transcribeAnnotation).toHaveBeenCalledTimes(2);
    expect(adapter.transcribeAnnotation).not.toHaveBeenCalledWith(annotations[2]);
    expect(applyTransformResult).toHaveBeenCalledWith(
      canvasId,
      expect.any(Object),
      expect.objectContaining({
        items: expect.arrayContaining([
          expect.objectContaining({
            body: [expect.objectContaining({ value: 'updated one' })],
            id: 'line-1',
          }),
        ]),
      }),
      ['line-1'],
    );
    expect(setStatusMessage).toHaveBeenLastCalledWith(
      'Retranscription stopped after 1/3. The transcription provider is temporarily unavailable.',
    );
  });

  it('continues past line-local failures and retains the aggregate failure summary', async () => {
    const adapter = {
      transcribeAnnotation: vi.fn()
        .mockRejectedValueOnce(new Error('line crop failed'))
        .mockResolvedValueOnce(annotation('line-2', 'updated two'))
        .mockResolvedValueOnce(annotation('line-3', 'updated three')),
    };
    const { applyTransformResult, setStatusMessage } = mount(adapter);

    await act(async () => handleTranscribe({ all: true }));

    expect(adapter.transcribeAnnotation).toHaveBeenCalledTimes(3);
    expect(applyTransformResult).toHaveBeenCalledWith(
      canvasId,
      expect.any(Object),
      expect.any(Object),
      ['line-2', 'line-3'],
    );
    expect(setStatusMessage).toHaveBeenLastCalledWith(
      'Retranscribed 2/3. line-1: line crop failed',
    );
  });
});
