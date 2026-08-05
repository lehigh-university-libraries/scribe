// @vitest-environment happy-dom

import { act } from 'react';
import { createRoot } from 'react-dom/client';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { ScribeAutoOpenPlugin } from './ScribeAutoOpenPlugin';

(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean })
  .IS_REACT_ACT_ENVIRONMENT = true;

let container: HTMLDivElement | undefined;
let root: ReturnType<typeof createRoot> | undefined;

function render(hasScribeWindow: boolean, openScribeWindow: () => void) {
  if (!container) {
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
  }
  act(() => root?.render(
    <ScribeAutoOpenPlugin
      hasScribeWindow={hasScribeWindow}
      openScribeWindow={openScribeWindow}
    />,
  ));
}

afterEach(() => {
  if (root) act(() => root?.unmount());
  container?.remove();
  container = undefined;
  root = undefined;
});

describe('ScribeAutoOpenPlugin', () => {
  it('opens a missing panel once without reopening it after the user closes it', () => {
    const openScribeWindow = vi.fn();

    render(false, openScribeWindow);
    expect(openScribeWindow).toHaveBeenCalledOnce();

    render(true, openScribeWindow);
    render(false, openScribeWindow);
    expect(openScribeWindow).toHaveBeenCalledOnce();
  });

  it('does not reopen a panel that was already present when the plugin mounted', () => {
    const openScribeWindow = vi.fn();

    render(true, openScribeWindow);
    render(false, openScribeWindow);
    expect(openScribeWindow).not.toHaveBeenCalled();
  });
});
