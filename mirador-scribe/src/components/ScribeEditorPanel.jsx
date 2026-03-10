import { startTransition, useEffect, useMemo, useRef } from 'react';
import PropTypes from 'prop-types';
import AutoFixHighIcon from '@mui/icons-material/AutoFixHigh';
import BackspaceOutlinedIcon from '@mui/icons-material/BackspaceOutlined';
import BorderColorOutlinedIcon from '@mui/icons-material/BorderColorOutlined';
import CallSplitOutlinedIcon from '@mui/icons-material/CallSplitOutlined';
import HorizontalSplitOutlinedIcon from '@mui/icons-material/HorizontalSplitOutlined';
import MergeTypeOutlinedIcon from '@mui/icons-material/MergeTypeOutlined';
import RedoOutlinedIcon from '@mui/icons-material/RedoOutlined';
import SaveOutlinedIcon from '@mui/icons-material/SaveOutlined';
import SplitscreenOutlinedIcon from '@mui/icons-material/SplitscreenOutlined';
import UndoOutlinedIcon from '@mui/icons-material/UndoOutlined';
import Alert from '@mui/material/Alert';
import Box from '@mui/material/Box';
import Checkbox from '@mui/material/Checkbox';
import Chip from '@mui/material/Chip';
import Dialog from '@mui/material/Dialog';
import DialogActions from '@mui/material/DialogActions';
import DialogContent from '@mui/material/DialogContent';
import DialogTitle from '@mui/material/DialogTitle';
import Divider from '@mui/material/Divider';
import IconButton from '@mui/material/IconButton';
import List from '@mui/material/List';
import ListItemButton from '@mui/material/ListItemButton';
import ListItemText from '@mui/material/ListItemText';
import Stack from '@mui/material/Stack';
import TextField from '@mui/material/TextField';
import Tooltip from '@mui/material/Tooltip';
import Typography from '@mui/material/Typography';
import { useTranslation } from 'react-i18next';
import { ConnectedCompanionWindow as CompanionWindow } from 'mirador';
import {
  annotationBBox,
  annotationGranularity,
  annotationText,
  groupAnnotationsForEditor,
  sortedAnnotations,
} from '../utils/iiif';

function ToolbarAction({ color = 'default', disabled, icon: Icon, onClick, selected = false, title }) {
  return (
    <Tooltip title={title}>
      <span>
        <IconButton
          size="small"
          color={color}
          disabled={disabled}
          onClick={onClick}
          sx={{
            backdropFilter: 'blur(10px)',
            backgroundColor: disabled ? 'rgba(226,232,240,0.38)' : 'rgba(255,255,255,0.88)',
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

ToolbarAction.propTypes = {
  color: PropTypes.oneOf(['default', 'inherit', 'primary', 'secondary', 'error', 'info', 'success', 'warning']),
  disabled: PropTypes.bool.isRequired,
  icon: PropTypes.elementType.isRequired,
  onClick: PropTypes.func.isRequired,
  selected: PropTypes.bool,
  title: PropTypes.string.isRequired,
};

export default function ScribeEditorPanel({
  annotations,
  canvasId,
  canJoinLines,
  canJoinWords,
  id,
  isBusy,
  onChangeText,
  onCreateLine,
  onDelete,
  onExplode,
  onJoinLines,
  onJoinWords,
  onRedo,
  onSave,
  onSelect,
  onSplit,
  onTranscribe,
  onTranscribeDialogClose,
  onTranscribeDialogOpen,
  onTranscribeSelectionChange,
  onUndo,
  saveDisabled,
  selectedAnnotationId,
  selectedGranularity,
  statusMessage,
  drawMode,
  transcribeDialogOpen,
  transcribeSelection,
  windowId,
}) {
  const { t } = useTranslation();
  const textInputRefs = useRef({});
  const panelRef = useRef(null);
  const orderedAnnotations = useMemo(() => sortedAnnotations({ items: annotations }), [annotations]);
  const editorRows = useMemo(() => groupAnnotationsForEditor({ items: annotations }), [annotations]);
  const selectedAnnotation = useMemo(
    () => orderedAnnotations.find((annotation) => annotation?.id === selectedAnnotationId) || null,
    [orderedAnnotations, selectedAnnotationId],
  );
  const hasSelection = Boolean(selectedAnnotation?.id);
  const allTranscribeSelected = orderedAnnotations.length > 0 && transcribeSelection.length === orderedAnnotations.length;
  const selectedBBox = useMemo(
    () => (selectedAnnotation ? annotationBBox(selectedAnnotation) : { x: 0, y: 0, w: 0, h: 0 }),
    [selectedAnnotation],
  );

  useEffect(() => {
    if (!selectedAnnotation?.id) return;
    const field = textInputRefs.current[selectedAnnotation.id];
    field?.focus?.();
    field?.select?.();
  }, [selectedAnnotation?.id]);

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
      minHeight: element.style.minHeight,
      maxWidth: element.style.maxWidth,
      minWidth: element.style.minWidth,
      overflow: element.style.overflow,
      width: element.style.width,
    }));

    for (const element of targets) {
      element.style.setProperty('width', '50vw', 'important');
      element.style.setProperty('min-width', '50vw', 'important');
      element.style.setProperty('max-width', '50vw', 'important');
      element.style.setProperty('flex-basis', '50vw', 'important');
      element.style.setProperty('height', 'calc(100dvh - 56px)', 'important');
      element.style.setProperty('min-height', 'calc(100dvh - 56px)', 'important');
      element.style.setProperty('overflow', 'hidden', 'important');
    }

    return () => {
      for (const previous of previousStyles) {
        previous.element.style.width = previous.width;
        previous.element.style.minWidth = previous.minWidth;
        previous.element.style.maxWidth = previous.maxWidth;
        previous.element.style.flexBasis = previous.flexBasis;
        previous.element.style.height = previous.height;
        previous.element.style.minHeight = previous.minHeight;
        previous.element.style.overflow = previous.overflow;
      }
    };
  }, []);

  useEffect(() => {
    function handleKeyDown(event) {
      const target = event.target;
      const isEditableTarget = target instanceof HTMLElement
        && (target.tagName === 'INPUT' || target.tagName === 'TEXTAREA' || target.isContentEditable);
      const currentIndex = orderedAnnotations.findIndex((annotation) => annotation?.id === selectedAnnotationId);

      if (event.key === 'Tab' && orderedAnnotations.length > 0) {
        event.preventDefault();
        const direction = event.shiftKey ? -1 : 1;
        const safeIndex = currentIndex >= 0 ? currentIndex : 0;
        const nextIndex = (safeIndex + direction + orderedAnnotations.length) % orderedAnnotations.length;
        onSelect(orderedAnnotations[nextIndex].id);
        return;
      }

      if ((event.metaKey || event.ctrlKey) && !event.shiftKey && event.key.toLowerCase() === 'z') {
        event.preventDefault();
        onUndo();
        return;
      }

      if (((event.metaKey || event.ctrlKey) && event.shiftKey && event.key.toLowerCase() === 'z')
        || ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'y')) {
        event.preventDefault();
        onRedo();
        return;
      }

      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'enter' && hasSelection) {
        event.preventDefault();
        void onTranscribe({ all: false, annotationIds: [selectedAnnotation.id] });
        return;
      }

      if (isEditableTarget) {
        if (event.key === 'Enter' && hasSelection && !event.shiftKey && !event.metaKey && !event.ctrlKey) {
          event.preventDefault();
          void onSave();
        }
        return;
      }

      if (!hasSelection) return;

      switch (event.key.toLowerCase()) {
        case 'backspace':
        case 'delete':
          event.preventDefault();
          void onDelete(selectedAnnotation.id);
          break;
        case 'e':
          if (selectedGranularity === 'line') {
            event.preventDefault();
            void onExplode();
          }
          break;
        case 'j':
          event.preventDefault();
          if (selectedGranularity === 'word') void onJoinWords();
          else void onJoinLines();
          break;
        case 's':
          if (selectedGranularity === 'line') {
            event.preventDefault();
            void onSplit();
          }
          break;
        default:
          break;
      }
    }

    window.addEventListener('keydown', handleKeyDown);
    return () => window.removeEventListener('keydown', handleKeyDown);
  }, [
    hasSelection,
    onDelete,
    onExplode,
    onJoinLines,
    onJoinWords,
    onRedo,
    onSave,
    onSelect,
    onSplit,
    onTranscribe,
    onUndo,
    orderedAnnotations,
    selectedAnnotation,
    selectedAnnotationId,
    selectedGranularity,
  ]);

  return (
    <CompanionWindow title={t('scribeEditorTitle')} id={id} windowId={windowId}>
      <Stack
        ref={panelRef}
        spacing={2}
        sx={{
          background: 'linear-gradient(180deg, rgba(248,250,252,0.98) 0%, rgba(241,245,249,0.98) 100%)',
          boxSizing: 'border-box',
          height: 'calc(100dvh - 96px)',
          maxWidth: '50vw',
          minHeight: 0,
          minWidth: '50vw',
          overflow: 'hidden',
          p: 2.25,
          width: '50vw',
        }}
      >
        <Stack
          direction="row"
          spacing={0.75}
          useFlexGap
          flexWrap="wrap"
          sx={{
            backgroundColor: 'rgba(255,255,255,0.68)',
            border: '1px solid rgba(148,163,184,0.18)',
            borderRadius: 3,
            boxShadow: '0 10px 30px rgba(15,23,42,0.08)',
            p: 0.75,
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
        </Stack>

        <Divider sx={{ borderColor: 'rgba(148,163,184,0.18)' }} />

        {orderedAnnotations.length === 0 ? (
          <Alert severity="info">{t('scribeEditorEmpty')}</Alert>
        ) : (
          <Stack spacing={1} sx={{ flex: 1, minHeight: 0, overflowY: 'auto', pr: 0.5 }}>
            {editorRows.map((row) => (
              <Stack
                key={row.id}
                direction="row"
                spacing={0.5}
                useFlexGap
                flexWrap="nowrap"
                sx={{
                  alignItems: 'stretch',
                  backgroundColor: row.fields.some((annotation) => annotation.id === selectedAnnotationId)
                    ? 'rgba(254,249,195,0.55)'
                    : 'rgba(255,255,255,0.72)',
                  border: row.fields.some((annotation) => annotation.id === selectedAnnotationId)
                    ? '1px solid rgba(217,119,6,0.24)'
                    : '1px solid rgba(148,163,184,0.16)',
                  borderRadius: 2.5,
                  boxShadow: row.fields.some((annotation) => annotation.id === selectedAnnotationId)
                    ? '0 12px 26px rgba(217,119,6,0.10)'
                    : '0 8px 22px rgba(15,23,42,0.06)',
                  overflowX: 'auto',
                  p: 0.5,
                }}
              >
                {row.fields.map((annotation) => {
                  const isSelected = annotation.id === selectedAnnotationId;
                  if (isSelected) {
                    return (
                      <TextField
                        key={annotation.id}
                        fullWidth
                        size="small"
                        value={annotationText(annotation)}
                        inputRef={(field) => {
                          if (field) textInputRefs.current[annotation.id] = field;
                        }}
                        onClick={() => onSelect(annotation.id)}
                        onFocus={() => onSelect(annotation.id)}
                        onChange={(event) => onChangeText(annotation.id, event.target.value)}
                        variant="outlined"
                        sx={{
                          '& .MuiOutlinedInput-root': {
                            backgroundColor: 'rgba(255,255,255,0.96)',
                            borderRadius: 2,
                            fontFamily: '"IBM Plex Sans", "Helvetica Neue", sans-serif',
                            fontSize: 13,
                            lineHeight: 1.35,
                          },
                          '& .MuiOutlinedInput-notchedOutline': {
                            borderColor: 'rgba(217,119,6,0.45)',
                          },
                        }}
                      />
                    );
                  }

                  return (
                    <Box
                      key={annotation.id}
                      onClick={() => onSelect(annotation.id)}
                      sx={{
                        alignItems: 'center',
                        backgroundColor: 'rgba(255,255,255,0.36)',
                        borderRadius: 2,
                        cursor: 'text',
                        display: 'flex',
                        flex: '0 0 auto',
                        minHeight: 32,
                        px: 0.75,
                        py: 0.5,
                        transition: 'background-color 120ms ease',
                        whiteSpace: 'nowrap',
                        '&:hover': {
                          backgroundColor: 'rgba(255,255,255,0.6)',
                        },
                      }}
                    >
                      <Typography
                        sx={{
                          color: 'text.primary',
                          fontFamily: '"IBM Plex Sans", "Helvetica Neue", sans-serif',
                          fontSize: 12.5,
                          fontWeight: 500,
                          letterSpacing: '-0.01em',
                          lineHeight: 1.2,
                          whiteSpace: 'nowrap',
                        }}
                      >
                        {annotationText(annotation) || t('scribeEditorUntitled')}
                      </Typography>
                    </Box>
                  );
                })}
              </Stack>
            ))}
            {statusMessage ? (
              <Alert
                severity="info"
                sx={{
                  backgroundColor: 'rgba(224,242,254,0.75)',
                  border: '1px solid rgba(14,165,233,0.2)',
                  borderRadius: 2.5,
                }}
              >
                {statusMessage}
              </Alert>
            ) : null}
          </Stack>
        )}

        <Box
          sx={{
            alignItems: 'center',
            backgroundColor: 'rgba(255,255,255,0.7)',
            border: '1px solid rgba(148,163,184,0.16)',
            borderRadius: 999,
            color: 'text.secondary',
            display: 'flex',
            fontSize: 11,
            gap: 1.5,
            justifyContent: 'flex-end',
            minHeight: 28,
            mt: 'auto',
            px: 1.25,
            py: 0.5,
            width: 'fit-content',
            alignSelf: 'flex-end',
          }}
        >
          <Typography variant="caption" sx={{ fontSize: 11 }}>
            {selectedGranularity || 'line'}
          </Typography>
          <Typography variant="caption" sx={{ fontSize: 11 }}>
            x:{selectedBBox.x}
          </Typography>
          <Typography variant="caption" sx={{ fontSize: 11 }}>
            y:{selectedBBox.y}
          </Typography>
          <Typography variant="caption" sx={{ fontSize: 11 }}>
            w:{selectedBBox.w}
          </Typography>
          <Typography variant="caption" sx={{ fontSize: 11 }}>
            h:{selectedBBox.h}
          </Typography>
        </Box>
      </Stack>

      <Dialog open={transcribeDialogOpen} onClose={onTranscribeDialogClose} fullWidth maxWidth="sm">
        <DialogTitle>{t('scribeEditorTranscribeDialogTitle')}</DialogTitle>
        <DialogContent dividers>
          <Stack spacing={1.25}>
            <Typography variant="body2" color="text.secondary">
              {t('scribeEditorTranscribeDialogDescription')}
            </Typography>
            <List dense disablePadding sx={{ maxHeight: 320, overflowY: 'auto' }}>
              <ListItemButton
                selected={allTranscribeSelected}
                onClick={() => {
                  onTranscribeSelectionChange(
                    allTranscribeSelected ? [] : orderedAnnotations.map((annotation) => annotation.id),
                  );
                }}
                sx={{ borderRadius: 1, mb: 0.5 }}
              >
                <Checkbox edge="start" tabIndex={-1} disableRipple checked={allTranscribeSelected} />
                <ListItemText primary={t('scribeEditorTranscribeSelectVisible')} />
              </ListItemButton>
              {orderedAnnotations.map((annotation) => {
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
            </List>
          </Stack>
        </DialogContent>
        <DialogActions>
          <Tooltip title={t('scribeEditorTranscribeSelected')}>
            <span>
              <IconButton
                color="secondary"
                disabled={isBusy || transcribeSelection.length === 0}
                onClick={() => void onTranscribe({ all: false, annotationIds: transcribeSelection })}
              >
                <AutoFixHighIcon />
              </IconButton>
            </span>
          </Tooltip>
          <Tooltip title={t('scribeEditorTranscribeDocument')}>
            <span>
              <IconButton
                color="secondary"
                disabled={isBusy || orderedAnnotations.length === 0}
                onClick={() => void onTranscribe({ all: true })}
              >
                <AutoFixHighIcon />
              </IconButton>
            </span>
          </Tooltip>
        </DialogActions>
      </Dialog>
    </CompanionWindow>
  );
}

ScribeEditorPanel.propTypes = {
  annotations: PropTypes.arrayOf(PropTypes.shape({
    body: PropTypes.oneOfType([PropTypes.array, PropTypes.object, PropTypes.string]),
    id: PropTypes.string,
    target: PropTypes.oneOfType([PropTypes.object, PropTypes.string]),
    textGranularity: PropTypes.string,
  })).isRequired,
  canvasId: PropTypes.string,
  canJoinLines: PropTypes.bool.isRequired,
  canJoinWords: PropTypes.bool.isRequired,
  id: PropTypes.string.isRequired,
  isBusy: PropTypes.bool.isRequired,
  onChangeText: PropTypes.func.isRequired,
  onCreateLine: PropTypes.func.isRequired,
  onDelete: PropTypes.func.isRequired,
  onExplode: PropTypes.func.isRequired,
  onJoinLines: PropTypes.func.isRequired,
  onJoinWords: PropTypes.func.isRequired,
  onRedo: PropTypes.func.isRequired,
  onSave: PropTypes.func.isRequired,
  onSelect: PropTypes.func.isRequired,
  onSplit: PropTypes.func.isRequired,
  onTranscribe: PropTypes.func.isRequired,
  onTranscribeDialogClose: PropTypes.func.isRequired,
  onTranscribeDialogOpen: PropTypes.func.isRequired,
  onTranscribeSelectionChange: PropTypes.func.isRequired,
  onUndo: PropTypes.func.isRequired,
  saveDisabled: PropTypes.bool.isRequired,
  selectedAnnotationId: PropTypes.string,
  selectedGranularity: PropTypes.oneOf(['line', 'word', null]),
  statusMessage: PropTypes.string,
  drawMode: PropTypes.bool.isRequired,
  transcribeDialogOpen: PropTypes.bool.isRequired,
  transcribeSelection: PropTypes.arrayOf(PropTypes.string).isRequired,
  windowId: PropTypes.string.isRequired,
};
