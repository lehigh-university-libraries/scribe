import PropTypes from 'prop-types';
import Typography from '@mui/material/Typography';

function ScribeSidebarButton({ size = 'caption' }) {
  return (
    <Typography
      component="span"
      sx={{
        fontSize: 10,
        fontWeight: 700,
        letterSpacing: '0.08em',
        lineHeight: 1,
      }}
      variant={size}
    >
      OCR
    </Typography>
  );
}

ScribeSidebarButton.propTypes = {
  size: PropTypes.string,
};

ScribeSidebarButton.value = 'scribeEditor';

const scribeSidebarButtonPlugin = {
  component: ScribeSidebarButton,
  config: {
    translations: {
      en: {
        scribeEditorCanvasLabel: 'Canvas',
        scribeEditorCreateLine: 'Draw New Line',
        scribeEditorDelete: 'Delete',
        scribeEditorEmpty: 'Select a text annotation in the annotation list or image overlay to edit it here.',
        scribeEditorJoinLines: 'Join Lines',
        scribeEditorJoinWords: 'Join Words',
        scribeEditorOverlayNone: 'No Overlay',
        scribeEditorOverlaySegments: 'Segments',
        scribeEditorOverlayText: 'Text',
        scribeEditorRedo: 'Redo',
        scribeEditorSave: 'Save',
        scribeEditorSelectedLabel: 'Selected annotation',
        scribeEditorSplitLine: 'Split Line',
        scribeEditorSplitWords: 'Split Words',
        scribeEditorTextLabel: 'Transcription',
        scribeEditorTranscribe: 'Transcribe',
        scribeEditorTranscribeDialogDescription: 'Choose visible annotations to retranscribe, or send the full document page.',
        scribeEditorTranscribeDialogTitle: 'Transcribe Text',
        scribeEditorTranscribeDocument: 'Transcribe Document',
        scribeEditorTranscribeSelectVisible: 'Select all visible annotations',
        scribeEditorTranscribeSelected: 'Transcribe Selected',
        scribeEditorTitle: 'Scribe Editor',
        scribeEditorUndo: 'Undo',
        scribeEditorUntitled: 'Untitled annotation',
        openCompanionWindow_scribeEditor: 'Scribe Editor',
      },
    },
  },
  mode: 'add',
  target: 'WindowSideBarButtons',
};

export default scribeSidebarButtonPlugin;
