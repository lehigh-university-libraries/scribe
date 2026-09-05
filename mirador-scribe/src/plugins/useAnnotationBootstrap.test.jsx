// @vitest-environment happy-dom

import { act } from 'react';
import { createRoot } from 'react-dom/client';
import { afterEach, expect, it, vi } from 'vitest';
import { useAnnotationBootstrap } from './useAnnotationBootstrap';

globalThis.IS_REACT_ACT_ENVIRONMENT = true;

const canvasId = 'https://example.test/canvas/1';
const canonicalPage = {
  id: 'https://scribe.test/presentation/v3/item-image-41/canvas/page-1/annotations',
  items: [],
  type: 'AnnotationPage',
};

let container;
let root;

function Harness({ adapterFactory, options }) {
  useAnnotationBootstrap({
    ...options,
    adapterFactory,
    requireAdapter: (targetCanvasId) => adapterFactory(targetCanvasId),
  });
  return null;
}

afterEach(() => {
  if (root) act(() => root.unmount());
  container?.remove();
  root = undefined;
  container = undefined;
  vi.restoreAllMocks();
});

it('restarts a same-Canvas load when the prior effect is cancelled before settlement', async () => {
  let resolveFirstLoad;
  const firstAdapter = {
    loadSnapshot: vi.fn(() => new Promise((resolve) => {
      resolveFirstLoad = resolve;
    })),
  };
  const secondAdapter = {
    loadSnapshot: vi.fn(async () => ({ page: canonicalPage, revision: '7' })),
  };
  const dispatchSessionForCanvas = vi.fn();
  const loadedCanvasRef = { current: '' };
  const receiveAnnotation = vi.fn();
  const options = {
    canvasId,
    didInitialSnapRef: { current: true },
    dispatchSessionForCanvas,
    loadedCanvasRef,
    receiveAnnotation,
    setFocusedWordAnnotationId: vi.fn(),
    setStatusMessage: vi.fn(),
  };

  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
  const firstFactory = vi.fn(() => firstAdapter);
  const secondFactory = vi.fn(() => secondAdapter);

  await act(async () => {
    root.render(<Harness adapterFactory={firstFactory} options={options} />);
  });
  await vi.waitFor(() => expect(firstAdapter.loadSnapshot).toHaveBeenCalledOnce());

  await act(async () => {
    root.render(<Harness adapterFactory={secondFactory} options={options} />);
  });
  await vi.waitFor(() => expect(secondAdapter.loadSnapshot).toHaveBeenCalledOnce());
  await vi.waitFor(() => expect(receiveAnnotation).toHaveBeenCalledWith(
    canvasId,
    canonicalPage.id,
    canonicalPage,
  ));

  await act(async () => {
    resolveFirstLoad({ page: canonicalPage, revision: '6' });
  });
  expect(dispatchSessionForCanvas.mock.calls.filter(([, action]) => action.type === 'loaded')).toEqual([
    [canvasId, { page: canonicalPage, revision: '7', type: 'loaded' }],
  ]);
  expect(loadedCanvasRef.current).toBe(canvasId);
});
