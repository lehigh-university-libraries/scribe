import { startTransition } from 'react';
import PropTypes from 'prop-types';
import AutoFixHighIcon from '@mui/icons-material/AutoFixHigh';
import AddCircleOutlineIcon from '@mui/icons-material/AddCircleOutline';
import AddBoxOutlinedIcon from '@mui/icons-material/AddBoxOutlined';
import BorderColorOutlinedIcon from '@mui/icons-material/BorderColorOutlined';
import CallSplitOutlinedIcon from '@mui/icons-material/CallSplitOutlined';
import DeleteOutlineIcon from '@mui/icons-material/DeleteOutline';
import HorizontalSplitOutlinedIcon from '@mui/icons-material/HorizontalSplitOutlined';
import LayersOutlinedIcon from '@mui/icons-material/LayersOutlined';
import MergeTypeOutlinedIcon from '@mui/icons-material/MergeTypeOutlined';
import PublishOutlinedIcon from '@mui/icons-material/PublishOutlined';
import RedoOutlinedIcon from '@mui/icons-material/RedoOutlined';
import SaveOutlinedIcon from '@mui/icons-material/SaveOutlined';
import SplitscreenOutlinedIcon from '@mui/icons-material/SplitscreenOutlined';
import UndoOutlinedIcon from '@mui/icons-material/UndoOutlined';
import Alert from '@mui/material/Alert';
import Box from '@mui/material/Box';
import Button from '@mui/material/Button';
import Checkbox from '@mui/material/Checkbox';
import CircularProgress from '@mui/material/CircularProgress';
import Divider from '@mui/material/Divider';
import Chip from '@mui/material/Chip';
import Dialog from '@mui/material/Dialog';
import DialogActions from '@mui/material/DialogActions';
import DialogContent from '@mui/material/DialogContent';
import DialogTitle from '@mui/material/DialogTitle';
import List from '@mui/material/List';
import ListItemButton from '@mui/material/ListItemButton';
import ListItemText from '@mui/material/ListItemText';
import Stack from '@mui/material/Stack';
import Tooltip from '@mui/material/Tooltip';
import Typography from '@mui/material/Typography';
import { useTranslation } from 'react-i18next';
import { ConnectedCompanionWindow as CompanionWindow } from 'mirador';
import { annotationGranularity, annotationText, isLineAnnotation } from '../utils/iiif';
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
  alignSelf: 'stretch',
  display: 'grid',
  flex: '1 1 280px',
  gap: 0.75,
  gridTemplateColumns: 'repeat(auto-fit, minmax(164px, 1fr))',
  listStyle: 'none',
  m: 0,
  maxWidth: 560,
  minWidth: 0,
  p: 0,
  width: '100%',
  '@media (max-height: 500px)': {
    display: 'none',
  },
  '@media (max-width: 480px)': {
    gridTemplateColumns: 'repeat(auto-fit, minmax(148px, 1fr))',
  },
};

export const compactToolbarActionSx = {
  [compactEditorMedia]: {
    minHeight: 30,
    minWidth: 34,
    px: 0.5,
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
 * @typedef {import('react').ElementType<{ fontSize?: 'small' | 'inherit' | 'large' | 'medium' }>} ToolbarIcon
 * @typedef {import('../types/scribe').IdentifiedIIIFAnnotation} IdentifiedAnnotation
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
 * @property {boolean} drawMode
 * @property {string} id
 * @property {boolean} isBusy
 * @property {'none' | 'read' | 'edit' | 'outline'} overlayMode
 * @property {VoidAction} onCreateLine
 * @property {VoidAction} onCreateCenteredLine
 * @property {VoidAction} onAddWord
 * @property {(annotationId: string) => void | Promise<void>} onDelete
 * @property {VoidAction} onExplode
 * @property {VoidAction} onRedo
 * @property {VoidAction} onPublish
 * @property {VoidAction} onReload
 * @property {VoidAction} onSave
 * @property {VoidAction} onCycleOverlayMode
 * @property {(options: { all: boolean, annotationIds?: string[] }) => void | Promise<void>} onTranscribe
 * @property {VoidAction} onTranscribeDialogClose
 * @property {VoidAction} onTranscribeDialogOpen
 * @property {(annotationIds: string[]) => void} onTranscribeSelectionChange
 * @property {VoidAction} onUndo
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
 * @property {boolean} transcribeDialogOpen
 * @property {string[]} transcribeSelection
 * @property {IdentifiedAnnotation[]} visibleAnnotations
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

function ShortcutLegend() {
  const shortcuts = [
    { key: 'Esc', label: 'No overlay' },
    { key: 'E', label: 'Edit overlay' },
    { key: 'Tab', label: 'Next row' },
    { key: 'Shift+Tab', label: 'Prev row' },
    { key: `${mod}+Z`, label: 'Undo' },
    { key: `${mod}+Shift+Z`, label: 'Redo' },
    { key: 'Alt+S', label: 'Split line' },
    { key: 'Alt+L', label: 'Join lines' },
    { key: 'Alt+W', label: 'Join words' },
    { key: 'Alt+R', label: 'Retranscribe' },
    { key: 'Alt+P', label: 'Publish' },
  ];

  return (
    <Box
      aria-label="Keyboard shortcuts"
      component="ul"
      sx={shortcutLegendSx}
    >
      {shortcuts.map((shortcut) => (
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

/** @param {ScribeActionPanelProps} props */
export default function ScribeActionPanel({
  annotations,
  batchTranscriptionActive,
  canSplitToWords,
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
  onSave,
  onCycleOverlayMode,
  onTranscribe,
  onTranscribeDialogClose,
  onTranscribeDialogOpen,
  onTranscribeSelectionChange,
  onUndo,
  pendingRemoteIds,
  saveDisabled,
  revisionConflict,
  selectedAnnotation,
  selectedGranularity,
  statusMessage,
  structuralEdits,
  transcribeDialogOpen,
  transcribeSelection,
  visibleAnnotations,
  windowId,
}) {
  const { t } = useTranslation();
  const orderedAnnotations = annotations;
  const pageLineAnnotations = orderedAnnotations.filter(isLineAnnotation);
  const visibleLineAnnotations = visibleAnnotations.filter(isLineAnnotation);
  const validTranscribeSelection = transcribeSelection.filter((id) => (
    visibleLineAnnotations.some((annotation) => annotation.id === id)
  ));
  const hasSelection = Boolean(selectedAnnotation?.id);

  const overlayModeLabel = overlayMode === 'edit' ? 'Edit overlay'
    : overlayMode === 'read' ? 'Read overlay'
    : overlayMode === 'outline' ? 'Outline overlay'
    : 'Overlay off';

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
              maxWidth: 680,
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
                <Stack aria-label="View and modes" direction="row" flexWrap="wrap" role="group" useFlexGap spacing={0.5}>
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
                    label="Add centered line"
                    icon={AddBoxOutlinedIcon}
                    color="warning"
                    disabled={isBusy}
                    onClick={onCreateCenteredLine}
                  />
                  <ToolbarAction
                    title={overlayModeLabel}
                    label={overlayModeLabel}
                    icon={LayersOutlinedIcon}
                    color="info"
                    disabled={isBusy}
                    onClick={onCycleOverlayMode}
                    selected={overlayMode !== 'none'}
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
                <Stack aria-label="Text and page actions" direction="row" flexWrap="wrap" role="group" useFlexGap spacing={0.5}>
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
                    title={t('scribeEditorTranscribe')}
                    label="Retranscribe"
                    icon={AutoFixHighIcon}
                    color="secondary"
                    keyShortcuts="Alt+R"
                    disabled={batchTranscriptionActive || isBusy || pageLineAnnotations.length === 0}
                    onClick={onTranscribeDialogOpen}
                  />
                  <ToolbarAction
                    title={t('scribeEditorSave')}
                    label="Save"
                    icon={SaveOutlinedIcon}
                    color="primary"
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
                    title={t('scribeEditorDelete')}
                    label="Delete"
                    icon={DeleteOutlineIcon}
                    color="error"
                    disabled={isBusy || !hasSelection}
                    onClick={() => {
                      const annotationId = selectedAnnotation?.id;
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

      <StructuralEditDialogs structuralEdits={structuralEdits} />

      <Dialog open={transcribeDialogOpen} onClose={onTranscribeDialogClose} fullWidth maxWidth="sm">
        <DialogTitle>{t('scribeEditorTranscribeDialogTitle')}</DialogTitle>
        <DialogContent dividers>
          <Stack spacing={2}>
            <Button
              fullWidth
              size="large"
              variant="contained"
              disabled={batchTranscriptionActive || isBusy || pageLineAnnotations.length === 0}
              startIcon={<AutoFixHighIcon />}
              onClick={() => {
                onTranscribeDialogClose();
                void onTranscribe({ all: true });
              }}
              sx={{
                background: `linear-gradient(135deg, ${scribeTheme.transcribeStrong} 0%, ${scribeTheme.transcribe} 100%)`,
                borderRadius: 2,
                boxShadow: `0 4px 14px ${scribeTheme.shadow}`,
                fontWeight: 700,
                letterSpacing: '0.02em',
                py: 1.25,
                textTransform: 'none',
                '&:hover': {
                  background: `linear-gradient(135deg, ${scribeTheme.transcribeStrong} 0%, ${scribeTheme.transcribe} 100%)`,
                  boxShadow: `0 6px 20px ${scribeTheme.shadow}`,
                },
              }}
            >
              Retranscribe entire page
            </Button>

            <Divider>
              <Typography variant="caption" sx={{ color: 'text.disabled', px: 1 }}>
                or select lines
              </Typography>
            </Divider>

            <List dense disablePadding sx={{ maxHeight: 280, overflowY: 'auto' }}>
              {(() => {
                const lineAnnotations = visibleLineAnnotations;
                const allLinesSelected = lineAnnotations.length > 0
                  && lineAnnotations.every((a) => transcribeSelection.includes(a.id));
                return (
                  <>
                    <ListItemButton
                      selected={allLinesSelected}
                      onClick={() => {
                        onTranscribeSelectionChange(
                          allLinesSelected ? [] : lineAnnotations.map((a) => a.id),
                        );
                      }}
                      sx={{ borderRadius: 1, mb: 0.5 }}
                    >
                      <Checkbox edge="start" tabIndex={-1} disableRipple checked={allLinesSelected} inputProps={{ 'aria-label': 'Select all visible lines' }} />
                      <ListItemText primary={t('scribeEditorTranscribeSelectVisible')} />
                    </ListItemButton>
                    {lineAnnotations.map((annotation) => {
                      const checked = transcribeSelection.includes(annotation.id);
                      return (
                        <ListItemButton
                          key={annotation.id}
                          selected={checked}
                          onClick={() => {
                            onTranscribeSelectionChange(
                              checked
                                ? transcribeSelection.filter((entry) => entry !== annotation.id)
                                : [...transcribeSelection, annotation.id],
                            );
                          }}
                          sx={{ borderRadius: 1, mb: 0.5 }}
                        >
                          <Checkbox edge="start" tabIndex={-1} disableRipple checked={checked} inputProps={{ 'aria-label': `Select ${annotationText(annotation) || annotation.id}` }} />
                          <ListItemText
                            primary={annotationText(annotation) || t('scribeEditorUntitled')}
                            secondary={annotation.id}
                            primaryTypographyProps={{ noWrap: true }}
                            secondaryTypographyProps={{ noWrap: true }}
                          />
                          <Chip label={annotationGranularity(annotation)} size="small" variant="outlined" />
                        </ListItemButton>
                      );
                    })}
                  </>
                );
              })()}
            </List>
          </Stack>
        </DialogContent>
        <DialogActions>
          <Button
            disabled={isBusy}
            onClick={onTranscribeDialogClose}
            size="small"
          >
            Cancel
          </Button>
          <Tooltip title={t('scribeEditorTranscribeSelected')}>
            <span>
              <Button
                variant="outlined"
                color="secondary"
                size="small"
                startIcon={<AutoFixHighIcon />}
                disabled={batchTranscriptionActive || isBusy || validTranscribeSelection.length === 0}
                onClick={() => {
                  onTranscribeDialogClose();
                  void onTranscribe({ all: false, annotationIds: validTranscribeSelection });
                }}
                sx={{ textTransform: 'none' }}
              >
                Retranscribe selected
              </Button>
            </span>
          </Tooltip>
        </DialogActions>
      </Dialog>
    </CompanionWindow>
  );
}

ScribeActionPanel.propTypes = {
  annotations: PropTypes.arrayOf(PropTypes.shape({
    body: PropTypes.oneOfType([PropTypes.array, PropTypes.object, PropTypes.string]),
    id: PropTypes.string,
    target: PropTypes.oneOfType([PropTypes.object, PropTypes.string]),
    textGranularity: PropTypes.string,
  })).isRequired,
  batchTranscriptionActive: PropTypes.bool.isRequired,
  canSplitToWords: PropTypes.bool.isRequired,
  drawMode: PropTypes.bool.isRequired,
  id: PropTypes.string.isRequired,
  isBusy: PropTypes.bool.isRequired,
  overlayMode: PropTypes.oneOf(['none', 'read', 'edit', 'outline']).isRequired,
  onCreateLine: PropTypes.func.isRequired,
  onCreateCenteredLine: PropTypes.func.isRequired,
  onAddWord: PropTypes.func.isRequired,
  onDelete: PropTypes.func.isRequired,
  onExplode: PropTypes.func.isRequired,
  onRedo: PropTypes.func.isRequired,
  onPublish: PropTypes.func.isRequired,
  onReload: PropTypes.func.isRequired,
  onSave: PropTypes.func.isRequired,
  onCycleOverlayMode: PropTypes.func.isRequired,
  onTranscribe: PropTypes.func.isRequired,
  onTranscribeDialogClose: PropTypes.func.isRequired,
  onTranscribeDialogOpen: PropTypes.func.isRequired,
  onTranscribeSelectionChange: PropTypes.func.isRequired,
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
  transcribeDialogOpen: PropTypes.bool.isRequired,
  transcribeSelection: PropTypes.arrayOf(PropTypes.string).isRequired,
  visibleAnnotations: PropTypes.arrayOf(PropTypes.shape({
    body: PropTypes.oneOfType([PropTypes.array, PropTypes.object, PropTypes.string]),
    id: PropTypes.string,
    target: PropTypes.oneOfType([PropTypes.object, PropTypes.string]),
    textGranularity: PropTypes.string,
  })).isRequired,
  windowId: PropTypes.string.isRequired,
};
