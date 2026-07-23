import { startTransition, useEffect, useRef } from 'react';
import PropTypes from 'prop-types';
import AutoFixHighIcon from '@mui/icons-material/AutoFixHigh';
import BackspaceOutlinedIcon from '@mui/icons-material/BackspaceOutlined';
import AddCircleOutlineIcon from '@mui/icons-material/AddCircleOutline';
import AddBoxOutlinedIcon from '@mui/icons-material/AddBoxOutlined';
import BorderColorOutlinedIcon from '@mui/icons-material/BorderColorOutlined';
import CallSplitOutlinedIcon from '@mui/icons-material/CallSplitOutlined';
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
 * @property {string} label
 * @property {VoidAction} onClick
 * @property {boolean} [selected]
 * @property {string} title
 * @property {ToolbarVariant} [variant]
 * @typedef {Object} ScribeActionPanelProps
 * @property {IdentifiedAnnotation[]} annotations
 * @property {boolean} canJoinLines
 * @property {boolean} canJoinWords
 * @property {boolean} canSplitLine
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
 * @property {VoidAction} onJoinLines
 * @property {VoidAction} onJoinWords
 * @property {VoidAction} onRedo
 * @property {VoidAction} onPublish
 * @property {VoidAction} onReload
 * @property {VoidAction} onSave
 * @property {VoidAction} onSplit
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
 * @property {boolean} transcribeDialogOpen
 * @property {string[]} transcribeSelection
 * @property {string} windowId
 */

/** @param {ToolbarActionProps} props */
function ToolbarAction({
  color = 'inherit',
  disabled,
  icon: Icon,
  label,
  onClick,
  selected = false,
  title,
  variant = 'outlined',
}) {
  return (
    <Tooltip title={title} placement="top">
      <span>
        <Button
          aria-label={title}
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
              ? 'rgba(226,232,240,0.38)'
              : selected
                ? 'rgba(254,243,199,0.96)'
                : 'rgba(255,255,255,0.9)',
            border: '1px solid rgba(148,163,184,0.18)',
            borderRadius: 2,
            boxShadow: disabled ? 'none' : (selected ? '0 12px 24px rgba(217,119,6,0.16)' : '0 8px 20px rgba(15,23,42,0.08)'),
            color: selected ? 'warning.dark' : 'text.primary',
            minHeight: 34,
            px: 1.25,
            textTransform: 'none',
            transition: 'transform 120ms ease, box-shadow 120ms ease, background-color 120ms ease',
            '&:hover': {
              backgroundColor: disabled ? 'rgba(226,232,240,0.38)' : (selected ? 'rgba(254,243,199,0.96)' : 'rgba(255,251,235,0.96)'),
              boxShadow: disabled ? 'none' : '0 12px 24px rgba(15,23,42,0.12)',
              transform: disabled ? 'none' : 'translateY(-1px)',
            },
          }}
        >
          {label}
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
  ];

  return (
    <Box
      component="ul"
      sx={{
        alignSelf: 'center',
        listStyle: 'none',
        m: 0,
        p: 0,
        pl: 0.5,
      }}
    >
      {shortcuts.map((shortcut) => (
        <Box
          key={shortcut.key}
          component="li"
          sx={{
            alignItems: 'center',
            display: 'flex',
            gap: 0.75,
            mb: 0.3,
          }}
        >
          <Typography component="span" sx={{ color: 'text.disabled', fontSize: 10, lineHeight: 1 }}>
            •
          </Typography>
          <Chip
            label={shortcut.key}
            size="small"
            variant="outlined"
            sx={{
              backgroundColor: 'rgba(255,255,255,0.78)',
              borderColor: 'rgba(148,163,184,0.24)',
              fontSize: 10,
              height: 18,
            }}
          />
          <Typography component="span" sx={{ color: 'text.secondary', fontSize: 11, lineHeight: 1 }}>
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
  label: PropTypes.string.isRequired,
  onClick: PropTypes.func.isRequired,
  selected: PropTypes.bool,
  title: PropTypes.string.isRequired,
  variant: PropTypes.oneOf(['contained', 'outlined', 'text']),
};

/** @param {ScribeActionPanelProps} props */
export default function ScribeActionPanel({
  annotations,
  canJoinLines,
  canJoinWords,
  canSplitLine,
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
  onJoinLines,
  onJoinWords,
  onRedo,
  onPublish,
  onReload,
  onSave,
  onSplit,
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
  transcribeDialogOpen,
  transcribeSelection,
  windowId,
}) {
  const { t } = useTranslation();
  const orderedAnnotations = annotations;
  const hasSelection = Boolean(selectedAnnotation?.id);

  const panelRef = useRef(/** @type {HTMLElement | null} */ (null));
  const overlayModeLabel = overlayMode === 'edit' ? 'Edit overlay'
    : overlayMode === 'read' ? 'Read overlay'
    : overlayMode === 'outline' ? 'Outline overlay'
    : 'Overlay off';

  useEffect(() => {
    const container = panelRef.current;
    if (!(container instanceof HTMLElement)) return undefined;
    const drawer = container.closest('.MuiDrawer-paper, .MuiPaper-root');
    const drawerRoot = container.closest('.MuiDrawer-root');
    const targets = [drawerRoot, drawer, drawer?.parentElement].filter((element) => element instanceof HTMLElement);
    if (targets.length === 0) return undefined;

    const previousStyles = targets.map((element) => ({
      element,
      flexBasis: element.style.flexBasis,
      height: element.style.height,
      maxWidth: element.style.maxWidth,
      minWidth: element.style.minWidth,
      width: element.style.width,
    }));

    for (const element of targets) {
      element.style.setProperty('width', '100%', 'important');
      element.style.setProperty('min-width', '100%', 'important');
      element.style.setProperty('max-width', '100%', 'important');
      element.style.setProperty('flex-basis', '100%', 'important');
      element.style.setProperty('height', '176px', 'important');
    }

    return () => {
      for (const previous of previousStyles) {
        previous.element.style.width = previous.width;
        previous.element.style.minWidth = previous.minWidth;
        previous.element.style.maxWidth = previous.maxWidth;
        previous.element.style.flexBasis = previous.flexBasis;
        previous.element.style.height = previous.height;
      }
    };
  }, []);

  return (
    <CompanionWindow title="" id={id} windowId={windowId}>
      <Box
        ref={panelRef}
        sx={{
          alignItems: 'center',
          background: 'linear-gradient(180deg, rgba(248,250,252,0.98) 0%, rgba(241,245,249,0.98) 100%)',
          boxSizing: 'border-box',
          display: 'flex',
          flexDirection: 'column',
          height: '100%',
          justifyContent: 'center',
          minHeight: 0,
          overflow: 'hidden',
          p: 1,
          width: '100%',
        }}
      >
        <Box
          sx={{
            alignItems: 'stretch',
            display: 'flex',
            gap: 1.5,
            justifyContent: 'center',
            width: '100%',
          }}
        >
          <Box
            sx={{
              backgroundColor: 'rgba(255,255,255,0.68)',
              border: '1px solid rgba(148,163,184,0.18)',
              borderRadius: 3,
              boxShadow: '0 10px 30px rgba(15,23,42,0.08)',
              display: 'flex',
              flexDirection: 'column',
              maxWidth: 680,
              p: 1,
            }}
          >
            <Stack spacing={1}>
              <Box>
                <Typography
                  variant="caption"
                  sx={{ color: 'text.secondary', display: 'block', mb: 0.75, px: 0.25, textTransform: 'uppercase' }}
                >
                  View and modes
                </Typography>
                <Stack direction="row" flexWrap="wrap" useFlexGap spacing={0.75}>
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

              <Divider />

              <Box>
                <Typography
                  variant="caption"
                  sx={{ color: 'text.secondary', display: 'block', mb: 0.75, px: 0.25, textTransform: 'uppercase' }}
                >
                  Text and page actions
                </Typography>
                <Stack direction="row" flexWrap="wrap" useFlexGap spacing={0.75}>
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
                    disabled={isBusy || !canJoinWords}
                    onClick={onJoinWords}
                  />
                  <ToolbarAction
                    title={t('scribeEditorSplitLine')}
                    label="Split line"
                    icon={SplitscreenOutlinedIcon}
                    disabled={isBusy || !hasSelection || !canSplitLine}
                    onClick={onSplit}
                  />
                  <ToolbarAction
                    title={t('scribeEditorJoinLines')}
                    label="Join lines"
                    icon={MergeTypeOutlinedIcon}
                    disabled={isBusy || !canJoinLines}
                    onClick={onJoinLines}
                  />
                  <ToolbarAction
                    title={t('scribeEditorTranscribe')}
                    label="Retranscribe"
                    icon={AutoFixHighIcon}
                    color="secondary"
                    disabled={isBusy || orderedAnnotations.length === 0}
                    onClick={onTranscribeDialogOpen}
                  />
                  <ToolbarAction
                    title={t('scribeEditorDelete')}
                    label="Delete"
                    icon={BackspaceOutlinedIcon}
                    color="error"
                    disabled={isBusy || !hasSelection}
                    onClick={() => {
                      const annotationId = selectedAnnotation?.id;
                      if (!annotationId) return;
                      startTransition(() => {
                        void onDelete(annotationId);
                      });
                    }}
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
                    disabled={isBusy}
                    onClick={() => {
                      startTransition(() => {
                        void onPublish();
                      });
                    }}
                  />
                </Stack>
              </Box>
            </Stack>
            {selectedAnnotation ? (
              <Typography
                variant="caption"
                sx={{
                  color: 'text.secondary',
                  display: 'block',
                  lineHeight: 1.3,
                  mt: 0.5,
                  textAlign: 'center',
                }}
              >
                {`${selectedGranularity || 'line'} selected`}
              </Typography>
            ) : null}
          </Box>

          {/* Keyboard shortcuts — bulleted list to the right */}
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

      <Dialog open={transcribeDialogOpen} onClose={onTranscribeDialogClose} fullWidth maxWidth="sm">
        <DialogTitle>{t('scribeEditorTranscribeDialogTitle')}</DialogTitle>
        <DialogContent dividers>
          <Stack spacing={2}>
            <Button
              fullWidth
              size="large"
              variant="contained"
              disabled={isBusy || orderedAnnotations.length === 0}
              startIcon={<AutoFixHighIcon />}
              onClick={() => {
                onTranscribeDialogClose();
                void onTranscribe({ all: true });
              }}
              sx={{
                background: 'linear-gradient(135deg, #6d28d9 0%, #7c3aed 100%)',
                borderRadius: 2,
                boxShadow: '0 4px 14px rgba(109,40,217,0.4)',
                fontWeight: 700,
                letterSpacing: '0.02em',
                py: 1.25,
                textTransform: 'none',
                '&:hover': {
                  background: 'linear-gradient(135deg, #5b21b6 0%, #6d28d9 100%)',
                  boxShadow: '0 6px 20px rgba(109,40,217,0.5)',
                },
              }}
            >
            &nbsp; entire page
            </Button>

            <Divider>
              <Typography variant="caption" sx={{ color: 'text.disabled', px: 1 }}>
                or select lines
              </Typography>
            </Divider>

            <List dense disablePadding sx={{ maxHeight: 280, overflowY: 'auto' }}>
              {(() => {
                const lineAnnotations = orderedAnnotations.filter(isLineAnnotation);
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
          <Tooltip title={t('scribeEditorTranscribeSelected')}>
            <span>
              <Button
                variant="outlined"
                color="secondary"
                size="small"
                startIcon={<AutoFixHighIcon />}
                disabled={isBusy || transcribeSelection.length === 0}
                onClick={() => {
                  onTranscribeDialogClose();
                  void onTranscribe({ all: false, annotationIds: transcribeSelection });
                }}
                sx={{ textTransform: 'none' }}
              >
                Transcribe selected
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
  canJoinLines: PropTypes.bool.isRequired,
  canJoinWords: PropTypes.bool.isRequired,
  canSplitLine: PropTypes.bool.isRequired,
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
  onJoinLines: PropTypes.func.isRequired,
  onJoinWords: PropTypes.func.isRequired,
  onRedo: PropTypes.func.isRequired,
  onPublish: PropTypes.func.isRequired,
  onReload: PropTypes.func.isRequired,
  onSave: PropTypes.func.isRequired,
  onSplit: PropTypes.func.isRequired,
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
  transcribeDialogOpen: PropTypes.bool.isRequired,
  transcribeSelection: PropTypes.arrayOf(PropTypes.string).isRequired,
  windowId: PropTypes.string.isRequired,
};
