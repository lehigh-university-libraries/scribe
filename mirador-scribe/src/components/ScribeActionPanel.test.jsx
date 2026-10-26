// @vitest-environment happy-dom

import { act } from 'react';
import { createRoot } from 'react-dom/client';
import { afterEach, describe, expect, it, vi } from 'vitest';
import ScribeActionPanel, {
  actionPanelRootSx,
  actionPanelToolbarLayoutSx,
  compactToolbarActionSx,
  shortcutLegendEntries,
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
      scribeEditorReprocess: 'Reprocess Page',
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
    deleteTarget: { granularity: 'line', id: annotation.id, text: 'one two three' },
    drawMode: false,
    id: 'companion-1',
    isBusy: false,
    onAddWord: noop,
    onCreateCenteredLine: noop,
    onCreateLine: noop,
    onDelete: noop,
    onExplode: noop,
    onPublish: noop,
    onRedo: noop,
    onReload: noop,
    onReprocess: noop,
    onSave: noop,
    onSelectOverlayMode: noop,
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
      gridTemplateColumns: 'repeat(auto-fit, minmax(148px, 1fr))',
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
    expect(document.querySelector('button[aria-label^="Re-segment and retranscribe"]')?.getAttribute('aria-keyshortcuts')).toBe('Alt+R');
    expect(document.querySelector('button[aria-label="Publish edits"]')?.getAttribute('aria-keyshortcuts')).toBe('Alt+P');
    expect(document.querySelector('button[aria-label="Add a word annotation beside the selection"]')?.getAttribute('aria-keyshortcuts')).toBe('W');
  });

  it('renders shortcut keys as readable semantic keycaps without decorative bullets', async () => {
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
    await act(async () => root.render(<ScribeActionPanel {...props()} />));

    const legend = document.querySelector('[aria-label="Keyboard shortcuts"]');
    expect(legend?.querySelectorAll('li')).toHaveLength(shortcutLegendEntries.length);
    expect(legend?.querySelectorAll('kbd')).toHaveLength(shortcutLegendEntries.length);
    expect(legend?.textContent).not.toContain('•');
    expect(legend?.textContent).toContain('Shift+TabPrev row');
    expect(legend?.textContent).toContain('Alt+PPublish');
    expect(legend?.textContent).toContain('TTranscript pane');
    expect(legend?.textContent).toContain('ArrowsNudge box');
  });

  it('presents every overlay mode as one exclusive switch and reports the active mode', async () => {
    const onSelectOverlayMode = vi.fn();
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
    await act(async () => root.render(<ScribeActionPanel {...props({
      onSelectOverlayMode,
      overlayMode: 'read',
    })} />));

    const modeSwitch = document.querySelector('[role="group"][aria-label="Overlay mode"]');
    const modeButtons = [...(modeSwitch?.querySelectorAll('button') || [])];
    expect(modeButtons.map((button) => button.getAttribute('aria-label'))).toEqual([
      'Off overlay', 'Edit overlay', 'Read overlay', 'Outline overlay', 'Transcript overlay',
    ]);
    expect(modeButtons.map((button) => button.getAttribute('aria-pressed'))).toEqual([
      'false', 'false', 'true', 'false', 'false',
    ]);
    expect(modeButtons[4]?.getAttribute('aria-keyshortcuts')).toBe('T');

    await act(async () => modeButtons[4]?.click());
    expect(onSelectOverlayMode).toHaveBeenCalledWith('transcript');
    // Re-selecting a mode still records an explicit preference before OCR arrives.
    await act(async () => modeButtons[2]?.click());
    expect(onSelectOverlayMode).toHaveBeenLastCalledWith('read');
  });

  it('disables whole-page reprocessing while the durable job is active and while the page is empty', async () => {
    const onReprocess = vi.fn();
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
    await act(async () => root.render(<ScribeActionPanel {...props({
      batchTranscriptionActive: true,
      onReprocess,
    })} />));

    const reprocess = () => document.querySelector('button[aria-label^="Re-segment and retranscribe"]');
    expect(reprocess()?.disabled).toBe(true);
    expect(document.querySelector('button[aria-label="Publish edits"]')?.disabled).toBe(false);

    await act(async () => root.render(<ScribeActionPanel {...props({ annotations: [], onReprocess })} />));
    expect(reprocess()?.disabled).toBe(true);

    await act(async () => root.render(<ScribeActionPanel {...props({ onReprocess })} />));
    expect(reprocess()?.disabled).toBe(false);
    await act(async () => reprocess()?.click());
    expect(onReprocess).toHaveBeenCalledOnce();
    expect(document.body.textContent).not.toContain('Retranscribe selected');
  });

  it('puts the destructive trash action at the end of the sidebar toolbar', async () => {
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
    await act(async () => root.render(<ScribeActionPanel {...props()} />));

    const textActions = document.querySelector('[role="group"][aria-label="Text and page actions"]');
    const toolbarActions = [...textActions.querySelectorAll('button[aria-label]')];
    const deleteAction = toolbarActions.at(-1);
    expect(deleteAction?.getAttribute('aria-label')).toBe('Delete the line "one two three"');
    expect(deleteAction?.className).toContain('MuiButton-contained');
    expect(deleteAction?.className).toContain('MuiButton-colorError');
    expect(deleteAction?.querySelector('[data-testid="DeleteOutlineOutlinedIcon"]')).not.toBeNull();
  });

  it('deletes the focused word rather than its line and disables deletion without a target', async () => {
    const onDelete = vi.fn();
    const selectedWord = word();
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
    await act(async () => root.render(<ScribeActionPanel {...props({
      annotations: [line(), selectedWord],
      deleteTarget: { granularity: 'word', id: selectedWord.id, text: 'one' },
      onDelete,
    })} />));

    const deleteAction = document.querySelector("button[aria-label='Delete the word \"one\"']");
    expect(deleteAction?.textContent).toContain('Delete word');
    await act(async () => deleteAction?.click());
    expect(onDelete).toHaveBeenCalledWith(selectedWord.id);

    await act(async () => root.render(<ScribeActionPanel {...props({ deleteTarget: null, selectedAnnotation: null })} />));
    expect(document.querySelector('button[aria-label="scribeEditorDelete"]')?.disabled).toBe(true);
  });
});
