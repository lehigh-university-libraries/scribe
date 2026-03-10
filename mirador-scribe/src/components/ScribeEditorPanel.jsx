import { startTransition, useEffect, useMemo, useRef, useState } from 'react';
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
  rowBBox,
  rowText,
  sortedAnnotations,
} from '../utils/iiif';

const MIN_ROW_HEIGHT_PX = 44;
const ROW_GAP_PX = 8;

function rowSelectionId(row) {
  return row?.lead?.id || row?.fields?.[0]?.id || '';
}

function activeWordIdForCaret(row, value, selectionStart) {
  if (row?.granularity !== 'word' || !Array.isArray(row?.fields) || row.fields.length === 0) return '';
  const text = String(value || '');
  const caret = Math.max(0, Math.min(selectionStart ?? text.length, text.length));
  const beforeCaret = text.slice(0, caret);
  const tokensBeforeCaret = beforeCaret.trim().length === 0 ? 0 : beforeCaret.trim().split(/\s+/).length - 1;
  const clampedIndex = Math.max(0, Math.min(tokensBeforeCaret, row.fields.length - 1));
  return row.fields[clampedIndex]?.id || rowSelectionId(row);
}

function rowIncludesAnnotation(row, annotationId) {
  if (!annotationId) return false;
  if (row?.lead?.id === annotationId) return true;
  return Array.isArray(row?.fields) && row.fields.some((annotation) => annotation.id === annotationId);
}

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
  onChangeRowText,
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
  viewportBounds,
  windowId,
}) {
  const { t } = useTranslation();
  const textInputRefs = useRef({});
  const panelRef = useRef(null);
  const rowsViewportRef = useRef(null);
  const [rowsViewportHeight, setRowsViewportHeight] = useState(0);
  const orderedAnnotations = useMemo(() => sortedAnnotations({ items: annotations }), [annotations]);
  const editorRows = useMemo(() => groupAnnotationsForEditor({ items: annotations }), [annotations]);
  const selectedAnnotation = useMemo(
    () => orderedAnnotations.find((annotation) => annotation?.id === selectedAnnotationId) || null,
    [orderedAnnotations, selectedAnnotationId],
  );
  const selectedRowIndex = useMemo(
    () => editorRows.findIndex((row) => row.fields.some((annotation) => annotation.id === selectedAnnotationId)),
    [editorRows, selectedAnnotationId],
  );
  const hasSelection = Boolean(selectedAnnotation?.id);
  const allTranscribeSelected = orderedAnnotations.length > 0 && transcribeSelection.length === orderedAnnotations.length;
  const selectedBBox = useMemo(
    () => (selectedAnnotation ? annotationBBox(selectedAnnotation) : { x: 0, y: 0, w: 0, h: 0 }),
    [selectedAnnotation],
  );
  const positionedRows = useMemo(() => {
    if (editorRows.length === 0) return [];

    let previousBottom = 0;
    return editorRows.map((row, index) => {
      const bbox = rowBBox(row);
      const proportionalTop = viewportBounds && rowsViewportHeight > 0 && viewportBounds.h > 0
        ? ((bbox.y - viewportBounds.y) / viewportBounds.h) * rowsViewportHeight
        : index * (MIN_ROW_HEIGHT_PX + ROW_GAP_PX);
      const proportionalHeight = viewportBounds && rowsViewportHeight > 0 && viewportBounds.h > 0
        ? Math.max(MIN_ROW_HEIGHT_PX, (bbox.h / viewportBounds.h) * rowsViewportHeight)
        : MIN_ROW_HEIGHT_PX;
      const top = Math.max(0, Math.max(proportionalTop, previousBottom === 0 ? proportionalTop : previousBottom + ROW_GAP_PX));
      previousBottom = top + proportionalHeight;
      return {
        ...row,
        bbox,
        height: proportionalHeight,
        top,
      };
    });
  }, [editorRows, rowsViewportHeight, viewportBounds]);
  const rowsCanvasHeight = useMemo(() => {
    const lastRow = positionedRows[positionedRows.length - 1];
    if (!lastRow) return rowsViewportHeight;
    return Math.max(rowsViewportHeight, lastRow.top + lastRow.height);
  }, [positionedRows, rowsViewportHeight]);

  useEffect(() => {
    if (!selectedAnnotation?.id) return;
    const field = textInputRefs.current[selectedAnnotation.id];
    field?.focus?.();
    field?.select?.();
  }, [selectedAnnotation?.id]);

  useEffect(() => {
    const element = rowsViewportRef.current;
    if (!(element instanceof HTMLElement) || typeof ResizeObserver === 'undefined') return undefined;

    const update = () => setRowsViewportHeight(element.clientHeight);
    update();
    const observer = new ResizeObserver(update);
    observer.observe(element);
    return () => observer.disconnect();
  }, []);

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
      const currentIndex = selectedRowIndex >= 0 ? selectedRowIndex : 0;

      if (event.key === 'Tab' && editorRows.length > 0) {
        event.preventDefault();
        const direction = event.shiftKey ? -1 : 1;
        const nextIndex = (currentIndex + direction + editorRows.length) % editorRows.length;
        onSelect(rowSelectionId(editorRows[nextIndex]));
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
    editorRows,
    orderedAnnotations,
    selectedAnnotation,
    selectedAnnotationId,
    selectedRowIndex,
    selectedGranularity,
  ]);

  return (
    <CompanionWindow title={t('scribeEditorTitle')} id={id} windowId={windowId}>
      <Box
        ref={panelRef}
        sx={{
          background: 'linear-gradient(180deg, rgba(248,250,252,0.98) 0%, rgba(241,245,249,0.98) 100%)',
          boxSizing: 'border-box',
          height: 'calc(100dvh - 96px)',
          minHeight: 0,
          overflow: 'hidden',
          position: 'relative',
          width: '100%',
        }}
      >
        <Stack
          spacing={0.75}
          sx={{
            backgroundColor: 'rgba(255,255,255,0.68)',
            border: '1px solid rgba(148,163,184,0.18)',
            borderRadius: 3,
            boxShadow: '0 10px 30px rgba(15,23,42,0.08)',
            left: 12,
            p: 0.75,
            position: 'absolute',
            top: 12,
            width: 52,
            zIndex: 2,
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
        <Box
          ref={rowsViewportRef}
          sx={{
            height: '100%',
            ml: '76px',
            minHeight: 0,
            overflowY: 'auto',
            pb: 8,
            pr: 1,
          }}
        >
          {orderedAnnotations.length === 0 ? (
            <Alert severity="info" sx={{ m: 2 }}>{t('scribeEditorEmpty')}</Alert>
          ) : (
            <Box
              sx={{
                minHeight: Math.max(rowsCanvasHeight, 0),
                position: 'relative',
              }}
            >
              {positionedRows.map((row) => (
                <Stack
                  key={row.id}
                  direction="row"
                  spacing={0.5}
                  useFlexGap
                  flexWrap="nowrap"
                  sx={{
                    alignItems: 'stretch',
                    backgroundColor: rowIncludesAnnotation(row, selectedAnnotationId)
                      ? 'rgba(254,249,195,0.55)'
                      : 'rgba(255,255,255,0.72)',
                    border: rowIncludesAnnotation(row, selectedAnnotationId)
                      ? '1px solid rgba(217,119,6,0.24)'
                      : '1px solid rgba(148,163,184,0.16)',
                    borderRadius: 2.5,
                    boxShadow: rowIncludesAnnotation(row, selectedAnnotationId)
                      ? '0 12px 26px rgba(217,119,6,0.10)'
                      : '0 8px 22px rgba(15,23,42,0.06)',
                    left: 0,
                    minHeight: row.height,
                    overflowX: 'auto',
                    p: 0.5,
                    position: 'absolute',
                    right: 0,
                    top: row.top,
                  }}
                >
                  <Stack spacing={0.5} sx={{ flex: 1, minWidth: 0, justifyContent: 'center' }}>
                    <TextField
                      fullWidth
                      size="small"
                      value={rowText(row)}
                      inputRef={(field) => {
                        if (!field) return;
                        row.fields.forEach((annotation) => {
                          textInputRefs.current[annotation.id] = field;
                        });
                      }}
                      onClick={() => onSelect(rowSelectionId(row))}
                      onFocus={() => onSelect(rowSelectionId(row))}
                      onKeyUp={(event) => {
                        if (row.granularity !== 'word') return;
                        onSelect(activeWordIdForCaret(row, event.currentTarget.value, event.currentTarget.selectionStart));
                      }}
                      onChange={(event) => {
                        if (row.granularity === 'word') {
                          onSelect(activeWordIdForCaret(row, event.target.value, event.target.selectionStart));
                          onChangeRowText(row, event.target.value);
                          return;
                        }
                        onChangeText(rowSelectionId(row), event.target.value);
                      }}
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
                          borderColor: rowIncludesAnnotation(row, selectedAnnotationId)
                            ? 'rgba(217,119,6,0.45)'
                            : 'rgba(148,163,184,0.24)',
                        },
                      }}
                    />
                  </Stack>
                </Stack>
              ))}
            </Box>
          )}
        </Box>

        {statusMessage ? (
          <Alert
            severity="info"
            sx={{
              backgroundColor: 'rgba(224,242,254,0.92)',
              border: '1px solid rgba(14,165,233,0.2)',
              borderRadius: 2.5,
              bottom: 12,
              left: 88,
              maxWidth: 'calc(100% - 140px)',
              position: 'absolute',
              zIndex: 2,
            }}
          >
            {statusMessage}
          </Alert>
        ) : null}

        <Box
          sx={{
            alignItems: 'center',
            backgroundColor: 'rgba(255,255,255,0.9)',
            border: '1px solid rgba(148,163,184,0.16)',
            borderRadius: 999,
            bottom: 12,
            color: 'text.secondary',
            display: 'flex',
            fontSize: 11,
            gap: 1.5,
            justifyContent: 'flex-end',
            minHeight: 28,
            px: 1.25,
            py: 0.5,
            width: 'fit-content',
            position: 'absolute',
            right: 12,
            zIndex: 2,
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
      </Box>

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
  onChangeRowText: PropTypes.func.isRequired,
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
  viewportBounds: PropTypes.shape({
    h: PropTypes.number,
    w: PropTypes.number,
    x: PropTypes.number,
    y: PropTypes.number,
  }),
  windowId: PropTypes.string.isRequired,
};
