import { useEffect, useState } from 'react';
import PropTypes from 'prop-types';
import Box from '@mui/material/Box';
import Button from '@mui/material/Button';
import Checkbox from '@mui/material/Checkbox';
import Chip from '@mui/material/Chip';
import Dialog from '@mui/material/Dialog';
import DialogActions from '@mui/material/DialogActions';
import DialogContent from '@mui/material/DialogContent';
import DialogTitle from '@mui/material/DialogTitle';
import List from '@mui/material/List';
import ListItemButton from '@mui/material/ListItemButton';
import ListItemText from '@mui/material/ListItemText';
import Stack from '@mui/material/Stack';
import Typography from '@mui/material/Typography';
import { annotationText } from '../utils/iiif';
import { scribeTheme } from '../theme';

/** @typedef {import('../types/scribe').IdentifiedIIIFAnnotation} IdentifiedAnnotation */
/** @typedef {'split' | 'join-lines' | 'join-words' | null} StructuralDialog */
/**
 * @typedef {Object} StructuralEditModel
 * @property {() => void} closeDialog
 * @property {StructuralDialog} dialog
 * @property {(ids: string[]) => void | Promise<unknown>} joinLines
 * @property {(ids: string[]) => void | Promise<unknown>} joinWords
 * @property {IdentifiedAnnotation[]} lineCandidates
 * @property {string} selectedLineId
 * @property {string} selectedWordId
 * @property {(splitAtWord: number) => void | Promise<unknown>} splitAtWord
 * @property {string[]} splitTokens
 * @property {IdentifiedAnnotation[]} wordCandidates
 */

/** @param {string[]} current @param {string} id @returns {string[]} */
function toggleId(current, id) {
  return current.includes(id)
    ? current.filter((candidate) => candidate !== id)
    : [...current, id];
}

/** @param {{ structuralEdits: StructuralEditModel }} props */
export default function StructuralEditDialogs({ structuralEdits }) {
  const {
    closeDialog,
    dialog,
    joinLines,
    joinWords,
    lineCandidates,
    selectedLineId,
    selectedWordId,
    splitAtWord,
    splitTokens,
    wordCandidates,
  } = structuralEdits;
  const [boundary, setBoundary] = useState(1);
  const [selectedLineIds, setSelectedLineIds] = useState(/** @type {string[]} */ ([]));
  const [selectedWordIds, setSelectedWordIds] = useState(/** @type {string[]} */ ([]));

  useEffect(() => {
    if (dialog === 'split') {
      setBoundary(Math.max(1, Math.floor(splitTokens.length / 2)));
    } else if (dialog === 'join-lines') {
      setSelectedLineIds(selectedLineId ? [selectedLineId] : []);
    } else if (dialog === 'join-words') {
      setSelectedWordIds(selectedWordId ? [selectedWordId] : []);
    }
  }, [dialog, selectedLineId, selectedWordId, splitTokens.length]);

  const title = dialog === 'split' ? 'Choose a split boundary'
    : dialog === 'join-lines' ? 'Choose lines to join'
      : dialog === 'join-words' ? 'Choose words to join'
        : '';
  const description = dialog === 'split'
    ? 'Select the word that should end the first new line.'
    : dialog === 'join-lines'
      ? 'Choose any lines on this page. Reading order is determined from their geometry.'
      : 'Choose a subset of words from this row. Unselected words remain unchanged.';

  return (
    <Dialog
      aria-describedby="scribe-structural-dialog-description"
      fullWidth
      maxWidth="sm"
      onClose={closeDialog}
      open={dialog !== null}
    >
      <DialogTitle>{title}</DialogTitle>
      <DialogContent dividers>
        <Typography
          color="text.secondary"
          id="scribe-structural-dialog-description"
          sx={{ mb: 2 }}
          variant="body2"
        >
          {description}
        </Typography>

        {dialog === 'split' ? (
          <Stack spacing={2}>
            <Stack
              aria-label="Available word boundaries"
              direction="row"
              flexWrap="wrap"
              role="group"
              spacing={0.75}
              useFlexGap
            >
              {splitTokens.slice(0, -1).map((token, index) => {
                const splitIndex = index + 1;
                const selected = boundary === splitIndex;
                return (
                  <Button
                    aria-label={`Split after ${token}, word ${splitIndex}`}
                    aria-pressed={selected}
                    key={`${token}-${splitIndex}`}
                    onClick={() => setBoundary(splitIndex)}
                    size="small"
                    sx={{
                      backgroundColor: selected ? scribeTheme.selected : scribeTheme.surface,
                      borderColor: selected ? scribeTheme.word : scribeTheme.border,
                      color: selected ? scribeTheme.selectedForeground : scribeTheme.foreground,
                      textTransform: 'none',
                    }}
                    variant="outlined"
                  >
                    {token}
                    <Box aria-hidden="true" component="span" sx={{ color: scribeTheme.word, ml: 0.75 }}>
                      |
                    </Box>
                  </Button>
                );
              })}
            </Stack>
            <Box
              aria-live="polite"
              sx={{
                backgroundColor: scribeTheme.surfaceMuted,
                border: `1px solid ${scribeTheme.border}`,
                borderRadius: 2,
                p: 1.5,
              }}
            >
              <Typography sx={{ color: scribeTheme.line }} variant="caption">First line</Typography>
              <Typography>{splitTokens.slice(0, boundary).join(' ')}</Typography>
              <Typography sx={{ color: scribeTheme.line, mt: 1 }} variant="caption">Second line</Typography>
              <Typography>{splitTokens.slice(boundary).join(' ')}</Typography>
            </Box>
          </Stack>
        ) : null}

        {dialog === 'join-lines' ? (
          <List aria-label="Lines available to join" dense disablePadding>
            {lineCandidates.map((annotation, index) => {
              const checked = selectedLineIds.includes(annotation.id);
              const required = annotation.id === selectedLineId;
              const text = annotationText(annotation) || 'Untitled line';
              return (
                <ListItemButton
                  aria-label={`Line ${index + 1}: ${text}`}
                  key={annotation.id}
                  onClick={() => {
                    if (!required) setSelectedLineIds((current) => toggleId(current, annotation.id));
                  }}
                  selected={checked}
                  sx={{ borderRadius: 1, mb: 0.5 }}
                >
                  <Checkbox
                    checked={checked}
                    disabled={required}
                    edge="start"
                    inputProps={{ 'aria-label': `Select line ${index + 1}: ${text}` }}
                    tabIndex={-1}
                  />
                  <ListItemText primary={text} secondary={required ? 'Current line' : annotation.id} />
                  <Chip label="line" size="small" sx={{ borderColor: scribeTheme.line, color: scribeTheme.line }} variant="outlined" />
                </ListItemButton>
              );
            })}
          </List>
        ) : null}

        {dialog === 'join-words' ? (
          <List aria-label="Words available to join" dense disablePadding>
            {wordCandidates.map((annotation, index) => {
              const checked = selectedWordIds.includes(annotation.id);
              const required = annotation.id === selectedWordId;
              const text = annotationText(annotation) || 'Empty word';
              return (
                <ListItemButton
                  aria-label={`Word ${index + 1}: ${text}`}
                  key={annotation.id}
                  onClick={() => {
                    if (!required) setSelectedWordIds((current) => toggleId(current, annotation.id));
                  }}
                  selected={checked}
                  sx={{ borderRadius: 1, mb: 0.5 }}
                >
                  <Checkbox
                    checked={checked}
                    disabled={required}
                    edge="start"
                    inputProps={{ 'aria-label': `Select word ${index + 1}: ${text}` }}
                    tabIndex={-1}
                  />
                  <ListItemText primary={text} secondary={required ? 'Current word' : annotation.id} />
                  <Chip label="word" size="small" sx={{ borderColor: scribeTheme.word, color: scribeTheme.word }} variant="outlined" />
                </ListItemButton>
              );
            })}
          </List>
        ) : null}
      </DialogContent>
      <DialogActions>
        <Button onClick={closeDialog}>Cancel</Button>
        {dialog === 'split' ? (
          <Button
            disabled={boundary < 1 || boundary >= splitTokens.length}
            onClick={() => { void splitAtWord(boundary); }}
            variant="contained"
          >
            Split at boundary
          </Button>
        ) : null}
        {dialog === 'join-lines' ? (
          <Button
            disabled={selectedLineIds.length < 2}
            onClick={() => { void joinLines(selectedLineIds); }}
            variant="contained"
          >
            Join selected lines
          </Button>
        ) : null}
        {dialog === 'join-words' ? (
          <Button
            disabled={selectedWordIds.length < 2}
            onClick={() => { void joinWords(selectedWordIds); }}
            variant="contained"
          >
            Join selected words
          </Button>
        ) : null}
      </DialogActions>
    </Dialog>
  );
}

const annotationShape = PropTypes.shape({
  body: PropTypes.oneOfType([PropTypes.array, PropTypes.object, PropTypes.string]),
  id: PropTypes.string.isRequired,
  target: PropTypes.oneOfType([PropTypes.object, PropTypes.string]),
  textGranularity: PropTypes.string,
});

StructuralEditDialogs.propTypes = {
  structuralEdits: PropTypes.shape({
    closeDialog: PropTypes.func.isRequired,
    dialog: PropTypes.oneOf(['split', 'join-lines', 'join-words', null]),
    joinLines: PropTypes.func.isRequired,
    joinWords: PropTypes.func.isRequired,
    lineCandidates: PropTypes.arrayOf(annotationShape).isRequired,
    selectedLineId: PropTypes.string.isRequired,
    selectedWordId: PropTypes.string.isRequired,
    splitAtWord: PropTypes.func.isRequired,
    splitTokens: PropTypes.arrayOf(PropTypes.string).isRequired,
    wordCandidates: PropTypes.arrayOf(annotationShape).isRequired,
  }).isRequired,
};
