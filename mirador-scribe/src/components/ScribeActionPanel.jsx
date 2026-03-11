import { startTransition, useEffect, useRef } from 'react';
import PropTypes from 'prop-types';
import AutoFixHighIcon from '@mui/icons-material/AutoFixHigh';
import BackspaceOutlinedIcon from '@mui/icons-material/BackspaceOutlined';
import BorderColorOutlinedIcon from '@mui/icons-material/BorderColorOutlined';
import CallSplitOutlinedIcon from '@mui/icons-material/CallSplitOutlined';
import HorizontalSplitOutlinedIcon from '@mui/icons-material/HorizontalSplitOutlined';
import MergeTypeOutlinedIcon from '@mui/icons-material/MergeTypeOutlined';
import PublishOutlinedIcon from '@mui/icons-material/PublishOutlined';
import RedoOutlinedIcon from '@mui/icons-material/RedoOutlined';
import SaveOutlinedIcon from '@mui/icons-material/SaveOutlined';
import SplitscreenOutlinedIcon from '@mui/icons-material/SplitscreenOutlined';
import SubjectOutlinedIcon from '@mui/icons-material/SubjectOutlined';
import UndoOutlinedIcon from '@mui/icons-material/UndoOutlined';
import Alert from '@mui/material/Alert';
import Box from '@mui/material/Box';
import Button from '@mui/material/Button';
import Checkbox from '@mui/material/Checkbox';
import Divider from '@mui/material/Divider';
import Chip from '@mui/material/Chip';
import Dialog from '@mui/material/Dialog';
import DialogActions from '@mui/material/DialogActions';
import DialogContent from '@mui/material/DialogContent';
import DialogTitle from '@mui/material/DialogTitle';
import IconButton from '@mui/material/IconButton';
import List from '@mui/material/List';
import ListItemButton from '@mui/material/ListItemButton';
import ListItemText from '@mui/material/ListItemText';
import Stack from '@mui/material/Stack';
import Tooltip from '@mui/material/Tooltip';
import Typography from '@mui/material/Typography';
import { useTranslation } from 'react-i18next';
import { ConnectedCompanionWindow as CompanionWindow } from 'mirador';
import { annotationGranularity, annotationText, isLineAnnotation } from '../utils/iiif';

function ToolbarAction({ color = 'default', disabled, icon: Icon, onClick, selected = false, title }) {
  return (
    <Tooltip title={title} placement="left">
      <span>
        <IconButton
          size="small"
          color={color}
          disabled={disabled}
          onClick={onClick}
          sx={{
            backdropFilter: 'blur(10px)',
            backgroundColor: disabled ? 'rgba(226,232,240,0.38)' : 'rgba(255,255,255,0.9)',
            border: '1px solid rgba(148,163,184,0.18)',
            borderRadius: 2,
            boxShadow: disabled ? 'none' : (selected ? '0 12px 24px rgba(217,119,6,0.16)' : '0 8px 20px rgba(15,23,42,0.08)'),
            color: selected ? 'warning.dark' : 'text.primary',
            transition: 'transform 120ms ease, box-shadow 120ms ease, background-color 120ms ease',
            '&:hover': {
              backgroundColor: disabled ? 'rgba(226,232,240,0.38)' : (selected ? 'rgba(254,243,199,0.96)' : 'rgba(255,251,235,0.96)'),
              boxShadow: disabled ? 'none' : '0 12px 24px rgba(15,23,42,0.12)',
              transform: disabled ? 'none' : 'translateY(-1px)',
            },
          }}
        >
          <Icon fontSize="small" />
        </IconButton>
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
  color: PropTypes.oneOf(['default', 'inherit', 'primary', 'secondary', 'error', 'info', 'success', 'warning']),
  disabled: PropTypes.bool.isRequired,
  icon: PropTypes.elementType.isRequired,
  onClick: PropTypes.func.isRequired,
  selected: PropTypes.bool,
  title: PropTypes.string.isRequired,
};

export default function ScribeActionPanel({
  annotations,
  canJoinLines,
  canJoinWords,
  drawMode,
  id,
  isBusy,
  overlayMode,
  onCreateLine,
  onDelete,
  onExplode,
  onJoinLines,
  onJoinWords,
  onRedo,
  onPublish,
  onSave,
  onSplit,
  onCycleOverlayMode,
  onTranscribe,
  onTranscribeDialogClose,
  onTranscribeDialogOpen,
  onTranscribeSelectionChange,
  onUndo,
  saveDisabled,
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

  const panelRef = useRef(null);
  const overlayModeLabel = overlayMode === 'edit' ? 'Edit Overlay'
    : overlayMode === 'read' ? 'Read Overlay'
    : overlayMode === 'outline' ? 'Outline Overlay'
    : 'No Overlay';

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
      element.style.setProperty('height', '128px', 'important');
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
          {/* Action buttons — centered, max-width, wrappable */}
          <Box
            sx={{
              backgroundColor: 'rgba(255,255,255,0.68)',
              border: '1px solid rgba(148,163,184,0.18)',
              borderRadius: 3,
              boxShadow: '0 10px 30px rgba(15,23,42,0.08)',
              display: 'flex',
              flexDirection: 'column',
              justifyContent: 'center',
              maxWidth: 340,
              p: 0.75,
            }}
          >
            <Stack
              direction="row"
              flexWrap="wrap"
              useFlexGap
              spacing={0.75}
              sx={{
                alignItems: 'center',
                justifyContent: 'center',
              }}
            >
              <ToolbarAction title={t('scribeEditorCreateLine')} icon={BorderColorOutlinedIcon} color="warning" disabled={isBusy} onClick={onCreateLine} selected={drawMode} />
              <ToolbarAction title={t('scribeEditorSplitWords')} icon={CallSplitOutlinedIcon} disabled={isBusy || !hasSelection || selectedGranularity !== 'line'} onClick={onExplode} />
              <ToolbarAction title={t('scribeEditorJoinWords')} icon={HorizontalSplitOutlinedIcon} disabled={isBusy || !canJoinWords} onClick={onJoinWords} />
              <ToolbarAction title={t('scribeEditorSplitLine')} icon={SplitscreenOutlinedIcon} disabled={isBusy || !hasSelection || selectedGranularity !== 'line'} onClick={onSplit} />
              <ToolbarAction title={t('scribeEditorJoinLines')} icon={MergeTypeOutlinedIcon} disabled={isBusy || !canJoinLines} onClick={onJoinLines} />
              <ToolbarAction title={t('scribeEditorTranscribe')} icon={AutoFixHighIcon} color="secondary" disabled={isBusy || orderedAnnotations.length === 0} onClick={onTranscribeDialogOpen} />
              <ToolbarAction title={t('scribeEditorUndo')} icon={UndoOutlinedIcon} disabled={isBusy} onClick={onUndo} />
              <ToolbarAction title={t('scribeEditorRedo')} icon={RedoOutlinedIcon} disabled={isBusy} onClick={onRedo} />
              <ToolbarAction title={overlayModeLabel} icon={SubjectOutlinedIcon} color="info" disabled={isBusy} onClick={onCycleOverlayMode} selected={overlayMode !== 'none'} />
              <ToolbarAction
                title={t('scribeEditorDelete')}
                icon={BackspaceOutlinedIcon}
                color="error"
                disabled={isBusy || !hasSelection}
                onClick={() => {
                  startTransition(() => {
                    void onDelete(selectedAnnotation.id);
                  });
                }}
              />
              <ToolbarAction
                title={t('scribeEditorSave')}
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

        {statusMessage ? (
          <Alert
            severity="info"
            sx={{
              mt: 1,
              p: 0.75,
              width: '100%',
            }}
          >
            {statusMessage}
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
                      <Checkbox edge="start" tabIndex={-1} disableRipple checked={allLinesSelected} />
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
                          <Checkbox edge="start" tabIndex={-1} disableRipple checked={checked} />
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
  drawMode: PropTypes.bool.isRequired,
  id: PropTypes.string.isRequired,
  isBusy: PropTypes.bool.isRequired,
  overlayMode: PropTypes.oneOf(['none', 'read', 'edit', 'outline']).isRequired,
  onCreateLine: PropTypes.func.isRequired,
  onDelete: PropTypes.func.isRequired,
  onExplode: PropTypes.func.isRequired,
  onJoinLines: PropTypes.func.isRequired,
  onJoinWords: PropTypes.func.isRequired,
  onRedo: PropTypes.func.isRequired,
  onPublish: PropTypes.func.isRequired,
  onSave: PropTypes.func.isRequired,
  onSplit: PropTypes.func.isRequired,
  onCycleOverlayMode: PropTypes.func.isRequired,
  onTranscribe: PropTypes.func.isRequired,
  onTranscribeDialogClose: PropTypes.func.isRequired,
  onTranscribeDialogOpen: PropTypes.func.isRequired,
  onTranscribeSelectionChange: PropTypes.func.isRequired,
  onUndo: PropTypes.func.isRequired,
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
