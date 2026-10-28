import { startTransition } from 'react';
import PropTypes from 'prop-types';
import AutoFixHighIcon from '@mui/icons-material/AutoFixHigh';
import AddCircleOutlineIcon from '@mui/icons-material/AddCircleOutlineOutlined';
import AddBoxOutlinedIcon from '@mui/icons-material/AddBoxOutlined';
import BorderColorOutlinedIcon from '@mui/icons-material/BorderColorOutlined';
import CallSplitOutlinedIcon from '@mui/icons-material/CallSplitOutlined';
import DeleteOutlineIcon from '@mui/icons-material/DeleteOutlineOutlined';
import HorizontalSplitOutlinedIcon from '@mui/icons-material/HorizontalSplitOutlined';
import MergeTypeOutlinedIcon from '@mui/icons-material/MergeTypeOutlined';
import PublishOutlinedIcon from '@mui/icons-material/PublishOutlined';
import RedoOutlinedIcon from '@mui/icons-material/RedoOutlined';
import SaveOutlinedIcon from '@mui/icons-material/SaveOutlined';
import SplitscreenOutlinedIcon from '@mui/icons-material/SplitscreenOutlined';
import UndoOutlinedIcon from '@mui/icons-material/UndoOutlined';
import Alert from '@mui/material/Alert';
import Box from '@mui/material/Box';
import Button from '@mui/material/Button';
import CircularProgress from '@mui/material/CircularProgress';
import Divider from '@mui/material/Divider';
import Chip from '@mui/material/Chip';
import Stack from '@mui/material/Stack';
import ToggleButton from '@mui/material/ToggleButton';
import ToggleButtonGroup from '@mui/material/ToggleButtonGroup';
import Tooltip from '@mui/material/Tooltip';
import Typography from '@mui/material/Typography';
import { useTranslation } from 'react-i18next';
import { ConnectedCompanionWindow as CompanionWindow } from 'mirador';
import { scribeTheme } from '../theme';
import StructuralEditDialogs from './StructuralEditDialogs';

const compactEditorMedia = '@media (max-width: 480px), (max-height: 500px)';

export const actionPanelRootSx = {
  alignItems: 'center',
  background: `linear-gradient(180deg, ${scribeTheme.background} 0%, ${scribeTheme.surfaceMuted} 100%)`,
  boxSizing: 'border-box',
  display: 'flex',
  flex: '1 1 auto',
  flexDirection: 'column',
  height: '100%',
  justifyContent: 'flex-start',
  minHeight: 0,
  overflow: 'auto',
  p: 1,
  width: '100%',
  [compactEditorMedia]: {
    p: 0.5,
  },
};

export const actionPanelToolbarLayoutSx = {
  alignItems: 'stretch',
  display: 'flex',
  flexWrap: 'wrap',
  gap: 1.5,
  justifyContent: 'center',
  minWidth: 0,
  width: '100%',
  [compactEditorMedia]: {
    gap: 0.5,
  },
};

export const shortcutLegendSx = {
  alignSelf: 'flex-start',
  display: 'grid',
  flex: '0 1 320px',
  gap: 0.5,
  gridTemplateColumns: 'repeat(auto-fit, minmax(148px, 1fr))',
  listStyle: 'none',
  m: 0,
  maxWidth: 320,
  minWidth: 0,
  p: 0,
  width: '100%',
  '@media (max-height: 500px)': {
    display: 'none',
  },
};

export const compactToolbarActionSx = {
  [compactEditorMedia]: {
    lineHeight: 1.5,
    minHeight: 30,
    minWidth: 34,
    px: 0.5,
    py: 0.25,
    '& .MuiButton-startIcon': {
      m: 0,
    },
  },
};

export const toolbarActionLabelSx = {
  [compactEditorMedia]: {
    display: 'none',
  },
};

/**
 * Overlay modes in the order they appear in the mode switch. Each entry is a
 * distinct way of looking at the same draft, so they are presented as one
 * exclusive choice rather than a cycling button.
 * @type {ReadonlyArray<{ description: string, label: string, mode: import('../types/scribe').ScribeOverlayMode, shortcut?: string }>}
 */
export const overlayModeOptions = Object.freeze([
  { description: 'Image only. Click any word or line to start editing it.', label: 'Off', mode: 'none', shortcut: 'Esc' },
  { description: 'Correct the selected line in place, and move or resize its boxes.', label: 'Edit', mode: 'edit', shortcut: 'E' },
  { description: 'Show the transcription on top of the image.', label: 'Read', mode: 'read', shortcut: 'R' },
  { description: 'Show only line boundaries.', label: 'Outline', mode: 'outline' },
  { description: 'Image on the left, scrollable transcript in reading order on the right.', label: 'Transcript', mode: 'transcript', shortcut: 'T' },
]);

/**
 * @typedef {import('react').ElementType<{ fontSize?: 'small' | 'inherit' | 'large' | 'medium' }>} ToolbarIcon
 * @typedef {import('../types/scribe').IdentifiedIIIFAnnotation} IdentifiedAnnotation
 * @typedef {import('../types/scribe').ScribeOverlayMode} OverlayMode
 * @typedef {'inherit' | 'primary' | 'secondary' | 'error' | 'info' | 'success' | 'warning'} ToolbarColor
 * @typedef {'contained' | 'outlined' | 'text'} ToolbarVariant
 * @typedef {() => unknown} VoidAction
 * @typedef {Object} ToolbarActionProps
 * @property {ToolbarColor} [color]
 * @property {boolean} disabled
 * @property {ToolbarIcon} icon
 * @property {string} [keyShortcuts]
 * @property {string} label
 * @property {VoidAction} onClick
 * @property {boolean} [selected]
 * @property {string} title
 * @property {ToolbarVariant} [variant]
 * @typedef {Object} ScribeActionPanelProps
 * @property {IdentifiedAnnotation[]} annotations
 * @property {boolean} batchTranscriptionActive
 * @property {boolean} canSplitToWords
 * @property {{ granularity: 'line' | 'word', id: string, text: string } | null} deleteTarget
 * @property {boolean} drawMode
 * @property {string} id
 * @property {boolean} isBusy
 * @property {OverlayMode} overlayMode
 * @property {VoidAction} onCreateLine
 * @property {VoidAction} onCreateCenteredLine
 * @property {VoidAction} onAddWord
 * @property {(annotationId: string) => void | Promise<void>} onDelete
 * @property {VoidAction} onExplode
 * @property {VoidAction} onRedo
 * @property {VoidAction} onPublish
 * @property {VoidAction} onReload
 * @property {VoidAction} onReprocess
 * @property {VoidAction} onSave
 * @property {(mode: OverlayMode) => void} onSelectOverlayMode
 * @property {VoidAction} onUndo
 * @property {(id: string, type: string) => void} [onRegionType]
 * @property {string[]} pendingRemoteIds
 * @property {boolean} saveDisabled
 * @property {boolean} revisionConflict
 * @property {IdentifiedAnnotation | null} selectedAnnotation
 * @property {'line' | 'word' | null} selectedGranularity
 * @property {string | null | undefined} statusMessage
 * @property {{
 *   canChooseLines: boolean,
 *   canChooseSplit: boolean,
 *   canChooseWords: boolean,
 *   closeDialog: VoidAction,
 *   dialog: 'split' | 'join-lines' | 'join-words' | null,
 *   joinLines: (ids: string[]) => void | Promise<unknown>,
 *   joinWords: (ids: string[]) => void | Promise<unknown>,
 *   lineCandidates: IdentifiedAnnotation[],
 *   openJoinLines: VoidAction,
 *   openJoinWords: VoidAction,
 *   openSplit: VoidAction,
 *   selectedLineId: string,
 *   selectedWordId: string,
 *   splitAtWord: (splitAtWord: number) => void | Promise<unknown>,
 *   splitTokens: string[],
 *   wordCandidates: IdentifiedAnnotation[],
 * }} structuralEdits
 * @property {string} windowId
 */

/** @param {ToolbarActionProps} props */
function ToolbarAction({
  color = 'inherit',
  disabled,
  icon: Icon,
  keyShortcuts,
  label,
  onClick,
  selected = false,
  title,
  variant = 'outlined',
}) {
  const destructive = color === 'error' && variant === 'contained';
  return (
    <Tooltip title={title} placement="top">
      <span>
        <Button
          aria-label={title}
          aria-keyshortcuts={keyShortcuts}
          aria-pressed={selected || undefined}
          size="small"
          color={color}
          disabled={disabled}
          onClick={onClick}
          startIcon={<Icon fontSize="small" />}
          variant={variant}
          sx={{
            backdropFilter: 'blur(10px)',
            backgroundColor: disabled
              ? scribeTheme.surfaceMuted
              : destructive
                ? 'error.main'
                : selected
                  ? scribeTheme.selected
                  : scribeTheme.surface,
            border: '1px solid',
            borderColor: destructive && !disabled ? 'error.dark' : scribeTheme.border,
            borderRadius: 2,
            boxShadow: disabled ? 'none' : `0 8px 20px ${scribeTheme.shadowSoft}`,
            color: destructive && !disabled
              ? 'error.contrastText'
              : selected ? scribeTheme.selectedForeground : scribeTheme.foreground,
            minHeight: 34,
            px: 1.25,
            textTransform: 'none',
            transition: 'transform 120ms ease, box-shadow 120ms ease, background-color 120ms ease',
            '&:hover': {
              backgroundColor: disabled
                ? scribeTheme.surfaceMuted
                : destructive ? 'error.dark' : (selected ? scribeTheme.selected : scribeTheme.accent),
              boxShadow: disabled ? 'none' : `0 12px 24px ${scribeTheme.shadow}`,
              transform: disabled ? 'none' : 'translateY(-1px)',
            },
            ...compactToolbarActionSx,
          }}
        >
          <Box component="span" sx={toolbarActionLabelSx}>{label}</Box>
        </Button>
      </span>
    </Tooltip>
  );
}

const isMac = typeof navigator !== 'undefined' && /Mac|iPhone|iPad|iPod/.test(navigator.platform);
const mod = isMac ? 'Cmd' : 'Ctrl';

export const shortcutLegendEntries = Object.freeze([
  { key: 'Esc', label: 'Overlay off' },
  { key: 'E', label: 'Edit overlay' },
  { key: 'R', label: 'Read overlay' },
  { key: 'T', label: 'Transcript pane' },
  { key: 'N', label: 'New line' },
  { key: 'W', label: 'New word' },
  { key: 'Tab', label: 'Next row' },
  { key: 'Shift+Tab', label: 'Prev row' },
  { key: 'Arrows', label: 'Nudge box' },
  { key: 'Delete', label: 'Delete selection' },
  { key: `${mod}+S`, label: 'Save' },
  { key: `${mod}+Z`, label: 'Undo' },
  { key: `${mod}+Shift+Z`, label: 'Redo' },
  { key: 'Alt+S', label: 'Split line' },
  { key: 'Alt+L', label: 'Join lines' },
  { key: 'Alt+W', label: 'Join words' },
  { key: 'Alt+R', label: 'Reprocess page' },
  { key: 'Alt+P', label: 'Publish' },
]);

function ShortcutLegend() {
  return (
    <Box
      aria-label="Keyboard shortcuts"
      component="ul"
      sx={shortcutLegendSx}
    >
      {shortcutLegendEntries.map((shortcut) => (
        <Box
          key={shortcut.key}
          component="li"
          sx={{
            alignItems: 'center',
            display: 'grid',
            gap: 0.75,
            gridTemplateColumns: 'minmax(52px, max-content) minmax(0, 1fr)',
            minWidth: 0,
          }}
        >
          <Box
            component="kbd"
            sx={{
              alignItems: 'center',
              backgroundColor: scribeTheme.surface,
              border: '1px solid',
              borderBottomWidth: 2,
              borderColor: scribeTheme.border,
              borderRadius: 1,
              boxShadow: `0 1px 2px ${scribeTheme.shadowSoft}`,
              color: scribeTheme.foreground,
              display: 'inline-flex',
              fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace',
              fontSize: 12,
              fontWeight: 700,
              justifyContent: 'center',
              lineHeight: 1,
              minHeight: 22,
              px: 0.75,
              whiteSpace: 'nowrap',
            }}
          >
            {shortcut.key}
          </Box>
          <Typography
            component="span"
            sx={{
              color: 'text.secondary',
              fontSize: 12,
              lineHeight: 1.25,
              minWidth: 0,
            }}
          >
            {shortcut.label}
          </Typography>
        </Box>
      ))}
    </Box>
  );
}

ToolbarAction.propTypes = {
  color: PropTypes.oneOf(['inherit', 'primary', 'secondary', 'error', 'info', 'success', 'warning']),
  disabled: PropTypes.bool.isRequired,
  icon: PropTypes.elementType.isRequired,
  keyShortcuts: PropTypes.string,
  label: PropTypes.string.isRequired,
  onClick: PropTypes.func.isRequired,
  selected: PropTypes.bool,
  title: PropTypes.string.isRequired,
  variant: PropTypes.oneOf(['contained', 'outlined', 'text']),
};

/** @param {{ disabled: boolean, onSelect: (mode: OverlayMode) => void, value: OverlayMode }} props */
function OverlayModeSwitch({ disabled, onSelect, value }) {
  return (
    <ToggleButtonGroup
      aria-label="Overlay mode"
      color="primary"
      exclusive
      onChange={(_event, next) => {
        if (next) onSelect(next);
      }}
      size="small"
      value={value}
      sx={{
        backgroundColor: scribeTheme.surface,
        borderRadius: 2,
        boxShadow: `0 8px 20px ${scribeTheme.shadowSoft}`,
        '& .MuiToggleButton-root': {
          borderColor: scribeTheme.border,
          color: scribeTheme.foreground,
          minHeight: 34,
          px: 1.25,
          textTransform: 'none',
          ...compactToolbarActionSx,
        },
        '& .MuiToggleButton-root.Mui-selected': {
          backgroundColor: scribeTheme.selected,
          color: scribeTheme.selectedForeground,
        },
      }}
    >
      {overlayModeOptions.map((option) => (
        <Tooltip key={option.mode} placement="top" title={option.description}>
          <ToggleButton
            aria-label={`${option.label} overlay`}
            aria-keyshortcuts={option.shortcut}
            disabled={disabled}
            // Clicking the already active Off mode is still an explicit choice
            // that must suppress automatic Read mode when OCR arrives later.
            onClick={() => { if (value === option.mode) onSelect(option.mode); }}
            value={option.mode}
          >
            {option.label}
          </ToggleButton>
        </Tooltip>
      ))}
    </ToggleButtonGroup>
  );
}

OverlayModeSwitch.propTypes = {
  disabled: PropTypes.bool.isRequired,
  onSelect: PropTypes.func.isRequired,
  value: PropTypes.oneOf(['none', 'read', 'edit', 'outline', 'transcript']).isRequired,
};

/** @param {ScribeActionPanelProps} props */
export default function ScribeActionPanel({
  annotations,
  batchTranscriptionActive,
  canSplitToWords,
  deleteTarget,
  drawMode,
  id,
  isBusy,
  overlayMode,
  onCreateLine,
  onCreateCenteredLine,
  onAddWord,
  onDelete,
  onExplode,
  onRedo,
  onPublish,
  onReload,
  onReprocess,
  onSave,
  onSelectOverlayMode,
  onUndo,
  onRegionType,
  pendingRemoteIds,
  saveDisabled,
  revisionConflict,
  selectedAnnotation,
  selectedGranularity,
  statusMessage,
  structuralEdits,
  windowId,
}) {
  const { t } = useTranslation();
  const hasSelection = Boolean(selectedAnnotation?.id);
  const hasPageContent = annotations.length > 0;
  const deleteLabel = deleteTarget ? `Delete ${deleteTarget.granularity}` : 'Delete';
  const deleteTitle = deleteTarget
    ? `Delete the ${deleteTarget.granularity}${deleteTarget.text ? ` "${deleteTarget.text}"` : ''}`
    : t('scribeEditorDelete');

  return (
    <CompanionWindow title="" id={id} windowId={windowId}>
      <Box
        data-scribe-action-panel="true"
        sx={actionPanelRootSx}
      >
        <Box
          sx={actionPanelToolbarLayoutSx}
        >
          <Box
            sx={{
              backgroundColor: scribeTheme.surface,
              border: `1px solid ${scribeTheme.border}`,
              borderRadius: 3,
              boxShadow: `0 10px 30px ${scribeTheme.shadowSoft}`,
              display: 'flex',
              flex: '1 1 480px',
              flexDirection: 'column',
              maxWidth: 960,
              minWidth: 0,
              p: 1,
              width: '100%',
              [compactEditorMedia]: { p: 0.5 },
            }}
          >
            <Stack spacing={0.5}>
              <Box>
                <Typography
                  variant="caption"
                  sx={{
                    color: 'text.secondary',
                    display: 'block',
                    mb: 0.5,
                    px: 0.25,
                    textTransform: 'uppercase',
                    [compactEditorMedia]: { display: 'none' },
                  }}
                >
                  View and modes
                </Typography>
                <Stack aria-label="View and modes" direction="row" sx={{ flexWrap: "wrap" }} role="group" useFlexGap spacing={0.5}>
                  <OverlayModeSwitch
                    disabled={isBusy}
                    onSelect={onSelectOverlayMode}
                    value={overlayMode}
                  />
                  <ToolbarAction
                    title={t('scribeEditorCreateLine')}
                    label="Draw line"
                    icon={BorderColorOutlinedIcon}
                    color="warning"
                    disabled={isBusy}
                    onClick={onCreateLine}
                    selected={drawMode}
                  />
                  <ToolbarAction
                    title="Add a line at the viewport center and focus its keyboard resize handle"
                    label="Add line"
                    icon={AddBoxOutlinedIcon}
                    color="warning"
                    keyShortcuts="N"
                    disabled={isBusy}
                    onClick={onCreateCenteredLine}
                  />
                  <ToolbarAction
                    title={t('scribeEditorUndo')}
                    label="Undo"
                    icon={UndoOutlinedIcon}
                    disabled={isBusy}
                    onClick={onUndo}
                  />
                  <ToolbarAction
                    title={t('scribeEditorRedo')}
                    label="Redo"
                    icon={RedoOutlinedIcon}
                    disabled={isBusy}
                    onClick={onRedo}
                  />
                </Stack>
              </Box>

              <Divider sx={{ [compactEditorMedia]: { display: 'none' } }} />

              <Box>
                <Typography
                  variant="caption"
                  sx={{
                    color: 'text.secondary',
                    display: 'block',
                    mb: 0.5,
                    px: 0.25,
                    textTransform: 'uppercase',
                    [compactEditorMedia]: { display: 'none' },
                  }}
                >
                  Text and page actions
                </Typography>
                <Stack aria-label="Text and page actions" direction="row" sx={{ flexWrap: "wrap" }} role="group" useFlexGap spacing={0.5}>
                  <ToolbarAction
                    title={t('scribeEditorSplitWords')}
                    label="Split to words"
                    icon={CallSplitOutlinedIcon}
                    disabled={isBusy || !hasSelection || !canSplitToWords}
                    onClick={onExplode}
                  />
                  <ToolbarAction
                    title="Add a word annotation beside the selection"
                    label="Add word"
                    icon={AddCircleOutlineIcon}
                    keyShortcuts="W"
                    disabled={isBusy || !hasSelection}
                    onClick={onAddWord}
                  />
                  <ToolbarAction
                    title={t('scribeEditorJoinWords')}
                    label="Join words"
                    icon={HorizontalSplitOutlinedIcon}
                    keyShortcuts="Alt+W"
                    disabled={isBusy || !structuralEdits.canChooseWords}
                    onClick={structuralEdits.openJoinWords}
                  />
                  <ToolbarAction
                    title={t('scribeEditorSplitLine')}
                    label="Split line"
                    icon={SplitscreenOutlinedIcon}
                    keyShortcuts="Alt+S"
                    disabled={isBusy || !hasSelection || !structuralEdits.canChooseSplit}
                    onClick={structuralEdits.openSplit}
                  />
                  <ToolbarAction
                    title={t('scribeEditorJoinLines')}
                    label="Join lines"
                    icon={MergeTypeOutlinedIcon}
                    keyShortcuts="Alt+L"
                    disabled={isBusy || !structuralEdits.canChooseLines}
                    onClick={structuralEdits.openJoinLines}
                  />
                  <ToolbarAction
                    title="Re-segment and retranscribe the whole page with the selected processing context"
                    label="Reprocess page"
                    icon={AutoFixHighIcon}
                    color="secondary"
                    keyShortcuts="Alt+R"
                    disabled={batchTranscriptionActive || isBusy || !hasPageContent}
                    onClick={onReprocess}
                  />
                  <ToolbarAction
                    title={t('scribeEditorSave')}
                    label="Save"
                    icon={SaveOutlinedIcon}
                    color="primary"
                    keyShortcuts={`${mod}+S`}
                    disabled={isBusy || saveDisabled}
                    onClick={() => {
                      startTransition(() => {
                        void onSave();
                      });
                    }}
                  />
                  <ToolbarAction
                    title="Publish edits"
                    label="Publish"
                    icon={PublishOutlinedIcon}
                    color="success"
                    keyShortcuts="Alt+P"
                    disabled={isBusy}
                    onClick={() => {
                      startTransition(() => {
                        void onPublish();
                      });
                    }}
                  />
                  <ToolbarAction
                    title={deleteTitle}
                    label={deleteLabel}
                    icon={DeleteOutlineIcon}
                    color="error"
                    keyShortcuts="Delete"
                    disabled={isBusy || !deleteTarget}
                    onClick={() => {
                      const annotationId = deleteTarget?.id;
                      if (!annotationId) return;
                      startTransition(() => {
                        void onDelete(annotationId);
                      });
                    }}
                    variant="contained"
                  />
                </Stack>
              </Box>
            </Stack>
            <Stack
              aria-label="Text granularity legend"
              direction="row"
              role="group"
              spacing={0.75}
              sx={{ alignItems: 'center', justifyContent: 'center', mt: 0.5 }}
            >
              <Chip
                aria-current={selectedGranularity === 'line' ? 'true' : undefined}
                label="Line boundaries"
                size="small"
                sx={{
                  backgroundColor: selectedGranularity === 'line' ? scribeTheme.lineSurface : 'transparent',
                  borderColor: scribeTheme.line,
                  color: scribeTheme.line,
                }}
                variant="outlined"
              />
              <Chip
                aria-current={selectedGranularity === 'word' ? 'true' : undefined}
                label="Word boundaries"
                size="small"
                sx={{
                  backgroundColor: selectedGranularity === 'word' ? scribeTheme.wordSurface : 'transparent',
                  borderColor: scribeTheme.word,
                  color: scribeTheme.word,
                }}
                variant="outlined"
              />
              <Typography sx={{ color: 'text.secondary' }} variant="caption">
                {selectedAnnotation ? `${selectedGranularity || 'line'} selected` : 'No selection'}
              </Typography>
            </Stack>
          </Box>

            {/* Keyboard shortcut keycaps */}
          <ShortcutLegend />
        </Box>

        {isBusy || statusMessage || revisionConflict ? (
          <Alert
            aria-live="polite"
            icon={isBusy ? <CircularProgress aria-label="Editor operation in progress" size={18} /> : undefined}
            role="status"
            severity={revisionConflict || /fail|error|conflict|changed on the server/i.test(String(statusMessage || '')) ? 'error' : 'info'}
            action={revisionConflict ? (
              <Button
                color="inherit"
                disabled={isBusy}
                onClick={() => { void onReload(); }}
                size="small"
              >
                Reload &amp; rebase
              </Button>
            ) : undefined}
            sx={{
              mt: 1,
              p: 0.75,
              width: '100%',
            }}
          >
            {statusMessage || (revisionConflict
              ? 'This page needs to be reloaded before it can be saved.'
              : 'Working…')}
          </Alert>
        ) : null}
        {pendingRemoteIds.length > 0 ? (
          <Alert aria-live="polite" role="status" severity="warning" sx={{ mt: 1, width: '100%' }}>
            {pendingRemoteIds.length === 1
              ? 'One server update is waiting behind your local edit. Save or reload and rebase to resolve it.'
              : `${pendingRemoteIds.length} server updates are waiting behind local edits. Save or reload and rebase to resolve them.`}
          </Alert>
        ) : null}
      </Box>

      {annotations.some((annotation) => annotation.textGranularity === 'block') ? (
        <Box sx={{ p: 1 }}>
          <Typography variant="subtitle2">Region types</Typography>
          {annotations.filter((annotation) => annotation.textGranularity === 'block').map((annotation, index) => {
            const bodies = Array.isArray(annotation.body) ? annotation.body : [annotation.body];
            const type = bodies.find((body) => body && typeof body === 'object' && body.purpose === 'classifying');
            return <label key={annotation.id} style={{ display: 'block' }}>Region {index + 1}{' '}
              <select aria-label={`Region ${index + 1} type`} disabled={isBusy || !onRegionType} value={type && typeof type === 'object' ? type.value : 'unknown'} onChange={(event) => onRegionType?.(annotation.id || '', event.target.value)}>
                {['text','heading','caption','header','footer','page-number','marginalia','footnote','list','drop-capital','signature','table','field','image','math','separator','advert','noise','unknown'].map((value) => <option key={value} value={value}>{value}</option>)}
              </select>
            </label>;
          })}
        </Box>
      ) : null}
      <StructuralEditDialogs structuralEdits={structuralEdits} />
    </CompanionWindow>
  );
}

ScribeActionPanel.propTypes = {
  onRegionType: PropTypes.func,
  annotations: PropTypes.arrayOf(PropTypes.shape({
    body: PropTypes.oneOfType([PropTypes.array, PropTypes.object, PropTypes.string]),
    id: PropTypes.string,
    target: PropTypes.oneOfType([PropTypes.object, PropTypes.string]),
    textGranularity: PropTypes.string,
  })).isRequired,
  batchTranscriptionActive: PropTypes.bool.isRequired,
  canSplitToWords: PropTypes.bool.isRequired,
  deleteTarget: PropTypes.shape({
    granularity: PropTypes.oneOf(['line', 'word']).isRequired,
    id: PropTypes.string.isRequired,
    text: PropTypes.string.isRequired,
  }),
  drawMode: PropTypes.bool.isRequired,
  id: PropTypes.string.isRequired,
  isBusy: PropTypes.bool.isRequired,
  overlayMode: PropTypes.oneOf(['none', 'read', 'edit', 'outline', 'transcript']).isRequired,
  onCreateLine: PropTypes.func.isRequired,
  onCreateCenteredLine: PropTypes.func.isRequired,
  onAddWord: PropTypes.func.isRequired,
  onDelete: PropTypes.func.isRequired,
  onExplode: PropTypes.func.isRequired,
  onRedo: PropTypes.func.isRequired,
  onPublish: PropTypes.func.isRequired,
  onReload: PropTypes.func.isRequired,
  onReprocess: PropTypes.func.isRequired,
  onSave: PropTypes.func.isRequired,
  onSelectOverlayMode: PropTypes.func.isRequired,
  onUndo: PropTypes.func.isRequired,
  pendingRemoteIds: PropTypes.arrayOf(PropTypes.string).isRequired,
  revisionConflict: PropTypes.bool.isRequired,
  saveDisabled: PropTypes.bool.isRequired,
  selectedAnnotation: PropTypes.shape({
    id: PropTypes.string,
  }),
  selectedGranularity: PropTypes.oneOf(['line', 'word', null]),
  statusMessage: PropTypes.string,
  structuralEdits: PropTypes.shape({
    canChooseLines: PropTypes.bool.isRequired,
    canChooseSplit: PropTypes.bool.isRequired,
    canChooseWords: PropTypes.bool.isRequired,
    closeDialog: PropTypes.func.isRequired,
    dialog: PropTypes.oneOf(['split', 'join-lines', 'join-words', null]),
    joinLines: PropTypes.func.isRequired,
    joinWords: PropTypes.func.isRequired,
    lineCandidates: PropTypes.array.isRequired,
    openJoinLines: PropTypes.func.isRequired,
    openJoinWords: PropTypes.func.isRequired,
    openSplit: PropTypes.func.isRequired,
    selectedLineId: PropTypes.string.isRequired,
    selectedWordId: PropTypes.string.isRequired,
    splitAtWord: PropTypes.func.isRequired,
    splitTokens: PropTypes.arrayOf(PropTypes.string).isRequired,
    wordCandidates: PropTypes.array.isRequired,
  }).isRequired,
  windowId: PropTypes.string.isRequired,
};
