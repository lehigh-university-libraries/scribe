// @vitest-environment happy-dom

import { act } from 'react';
import { createRoot } from 'react-dom/client';
import { afterEach, describe, expect, it, vi } from 'vitest';
import ScribeActionPanel, {
  actionPanelRootSx,
  actionPanelToolbarLayoutSx,
  compactToolbarActionSx,
  shortcutLegendSx,
  toolbarActionLabelSx,
} from './ScribeActionPanel';

globalThis.IS_REACT_ACT_ENVIRONMENT = true;

vi.mock('mirador', () => ({
  ConnectedCompanionWindow: ({ children }) => <section>{children}</section>,
}));

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key) => ({
      scribeEditorTranscribe: 'Retranscribe',
      scribeEditorTranscribeSelected: 'Retranscribe selected',
    })[key] || key,
  }),
}));

let root;
let container;

function line(id = 'line-1') {
  return {
    body: [{ purpose: 'supplementing', type: 'TextualBody', value: 'one two three' }],
    id,
    target: 'https://example.test/canvas/1#xywh=pixel:0,0,100,20',
    textGranularity: 'line',
    type: 'Annotation',
  };
}

function word(id = 'word-1') {
  return {
    ...line(id),
    textGranularity: 'word',
  };
}

function props(overrides = {}) {
  const annotation = line();
  const noop = vi.fn();
  return {
    annotations: [annotation],
    batchTranscriptionActive: false,
    visibleAnnotations: [annotation],
    canSplitToWords: true,
    drawMode: false,
    id: 'companion-1',
    isBusy: false,
    onAddWord: noop,
    onCreateCenteredLine: noop,
    onCreateLine: noop,
    onCycleOverlayMode: noop,
    onDelete: noop,
    onExplode: noop,
    onPublish: noop,
    onRedo: noop,
    onReload: noop,
    onSave: noop,
    onTranscribe: noop,
    onTranscribeDialogClose: noop,
    onTranscribeDialogOpen: noop,
    onTranscribeSelectionChange: noop,
    onUndo: noop,
    overlayMode: 'none',
    pendingRemoteIds: [],
    revisionConflict: false,
    saveDisabled: false,
    selectedAnnotation: annotation,
    selectedGranularity: 'line',
    statusMessage: '',
    structuralEdits: {
      canChooseLines: true,
      canChooseSplit: true,
      canChooseWords: true,
      closeDialog: noop,
      dialog: null,
      joinLines: noop,
      joinWords: noop,
      lineCandidates: [annotation, line('line-2')],
      openJoinLines: noop,
      openJoinWords: noop,
      openSplit: noop,
      selectedLineId: annotation.id,
      selectedWordId: '',
      splitAtWord: noop,
      splitTokens: ['one', 'two', 'three'],
      wordCandidates: [],
    },
    transcribeDialogOpen: false,
    transcribeSelection: [],
    windowId: 'window-1',
    ...overrides,
  };
}

afterEach(async () => {
  if (root) await act(async () => root.unmount());
  container?.remove();
  root = undefined;
  container = undefined;
  vi.restoreAllMocks();
});

describe('ScribeActionPanel', () => {
  it('fills the companion content allocation and scrolls inside it', () => {
    expect(actionPanelRootSx).toMatchObject({
      flex: '1 1 auto',
      height: '100%',
      minHeight: 0,
      overflow: 'auto',
    });
    expect(actionPanelToolbarLayoutSx).toMatchObject({
      flexWrap: 'wrap',
      minWidth: 0,
      width: '100%',
    });
    expect(shortcutLegendSx).toMatchObject({
      display: 'grid',
      gridTemplateColumns: 'repeat(auto-fit, minmax(164px, 1fr))',
      width: '100%',
    });
    expect(actionPanelRootSx['@media (max-width: 480px), (max-height: 500px)']).toMatchObject({
      p: 0.5,
    });
    expect(actionPanelToolbarLayoutSx['@media (max-width: 480px), (max-height: 500px)']).toMatchObject({
      gap: 0.5,
    });
    expect(compactToolbarActionSx['@media (max-width: 480px), (max-height: 500px)']).toMatchObject({
      minHeight: 30,
      minWidth: 34,
      px: 0.5,
    });
    expect(toolbarActionLabelSx['@media (max-width: 480px), (max-height: 500px)']).toEqual({
      display: 'none',
    });
    expect(shortcutLegendSx['@media (max-height: 500px)']).toEqual({ display: 'none' });
    expect(shortcutLegendSx['@media (max-width: 480px)']).toMatchObject({
      gridTemplateColumns: 'repeat(auto-fit, minmax(148px, 1fr))',
    });
  });

  it('keeps granularity visible and exposes structural shortcuts on their controls', async () => {
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
    await act(async () => root.render(<ScribeActionPanel {...props()} />));

    const granularityLegend = document.querySelector('[aria-label="Text granularity legend"]');
    expect(granularityLegend?.textContent).toContain('Line boundaries');
    expect(granularityLegend?.textContent).toContain('Word boundaries');
    expect(document.querySelector('button[aria-label="scribeEditorSplitLine"]')?.getAttribute('aria-keyshortcuts')).toBe('Alt+S');
    expect(document.querySelector('button[aria-label="scribeEditorJoinLines"]')?.getAttribute('aria-keyshortcuts')).toBe('Alt+L');
    expect(document.querySelector('button[aria-label="scribeEditorJoinWords"]')?.getAttribute('aria-keyshortcuts')).toBe('Alt+W');
    expect(document.querySelector('button[aria-label="Retranscribe"]')?.getAttribute('aria-keyshortcuts')).toBe('Alt+R');
    expect(document.querySelector('button[aria-label="Publish edits"]')?.getAttribute('aria-keyshortcuts')).toBe('Alt+P');
  });

  it('renders shortcut keys as readable semantic keycaps without decorative bullets', async () => {
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
    await act(async () => root.render(<ScribeActionPanel {...props()} />));

    const legend = document.querySelector('[aria-label="Keyboard shortcuts"]');
    expect(legend?.querySelectorAll('li')).toHaveLength(11);
    expect(legend?.querySelectorAll('kbd')).toHaveLength(11);
    expect(legend?.textContent).not.toContain('•');
    expect(legend?.textContent).toContain('Shift+TabPrev row');
    expect(legend?.textContent).toContain('Alt+PPublish');
  });

  it('disables foreground retranscription while the durable job is active', async () => {
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
    await act(async () => root.render(<ScribeActionPanel {...props({
      batchTranscriptionActive: true,
    })} />));

    expect(document.querySelector('button[aria-label="Retranscribe"]')?.disabled).toBe(true);
    expect(document.querySelector('button[aria-label="Publish edits"]')?.disabled).toBe(false);
  });

  it('puts the destructive trash action at the end of the sidebar toolbar', async () => {
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
    await act(async () => root.render(<ScribeActionPanel {...props()} />));

    const textActions = document.querySelector('[role="group"][aria-label="Text and page actions"]');
    const toolbarActions = [...textActions.querySelectorAll('button[aria-label]')];
    const deleteAction = toolbarActions.at(-1);
    expect(deleteAction?.getAttribute('aria-label')).toBe('scribeEditorDelete');
    expect(deleteAction?.className).toContain('MuiButton-containedError');
    expect(deleteAction?.querySelector('[data-testid="DeleteOutlineIcon"]')).not.toBeNull();
  });

  it('offers an explicit cancel action and never enables a word-only retranscription selection', async () => {
    const onTranscribeDialogClose = vi.fn();
    const selectedWord = word();
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
    await act(async () => root.render(<ScribeActionPanel {...props({
      annotations: [line(), selectedWord],
      onTranscribeDialogClose,
      transcribeDialogOpen: true,
      transcribeSelection: [selectedWord.id],
      visibleAnnotations: [line(), selectedWord],
    })} />));

    const buttons = [...document.querySelectorAll('button')];
    const transcribeSelected = buttons.find((button) => button.textContent?.includes('Retranscribe selected'));
    expect(transcribeSelected?.disabled).toBe(true);

    const cancel = buttons.find((button) => button.textContent === 'Cancel');
    expect(cancel).toBeDefined();
    await act(async () => cancel?.click());
    expect(onTranscribeDialogClose).toHaveBeenCalledOnce();
  });

  it('keeps whole-page retranscription available when no line is inside the viewport', async () => {
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
    await act(async () => root.render(<ScribeActionPanel {...props({
      transcribeDialogOpen: true,
      visibleAnnotations: [],
    })} />));

    const buttons = [...document.querySelectorAll('button')];
    expect(buttons.find((button) => button.getAttribute('aria-label') === 'Retranscribe')?.disabled).toBe(false);
    expect(buttons.find((button) => button.textContent?.includes('Retranscribe entire page'))?.disabled).toBe(false);
    expect(buttons.find((button) => button.textContent?.includes('Retranscribe selected'))?.disabled).toBe(true);
  });
});
