// @vitest-environment happy-dom

import { describe, expect, it } from 'vitest';

import { editorKeyboardCommand, isEditableEventTarget } from './keyboard';

describe('editor keyboard routing', () => {
  it('lets Escape close the overlay while an input owns focus', () => {
    const input = document.createElement('input');
    expect(isEditableEventTarget(input)).toBe(true);
    expect(editorKeyboardCommand({ key: 'Escape', target: input })).toBe('dismiss-overlay');
  });

  it('keeps native deletion and undo in editable targets while allowing explicit save', () => {
    const textarea = document.createElement('textarea');
    expect(editorKeyboardCommand({ ctrlKey: true, key: 'Backspace', target: textarea })).toBeNull();
    expect(editorKeyboardCommand({ ctrlKey: true, key: 'z', target: textarea })).toBeNull();
    expect(editorKeyboardCommand({ ctrlKey: true, key: 's', target: textarea })).toBe('save');
  });

  it('routes non-editable undo, redo, delete, and edit-overlay commands', () => {
    const button = document.createElement('button');
    expect(editorKeyboardCommand({ ctrlKey: true, key: 'z', target: button })).toBe('undo');
    expect(editorKeyboardCommand({ ctrlKey: true, key: 'z', shiftKey: true, target: button })).toBe('redo');
    expect(editorKeyboardCommand({ ctrlKey: true, key: 'Delete', target: button })).toBe('delete');
    expect(editorKeyboardCommand({ key: 'e', target: button })).toBe('edit-overlay');
  });

  it('routes structural, retranscribe, and publish shortcuts outside text fields', () => {
    const button = document.createElement('button');
    expect(editorKeyboardCommand({ altKey: true, key: 's', target: button })).toBe('split-line');
    expect(editorKeyboardCommand({ altKey: true, key: 'l', target: button })).toBe('join-lines');
    expect(editorKeyboardCommand({ altKey: true, key: 'w', target: button })).toBe('join-words');
    expect(editorKeyboardCommand({ altKey: true, key: 'r', target: button })).toBe('retranscribe');
    expect(editorKeyboardCommand({ altKey: true, key: 'p', target: button })).toBe('publish');
  });

  it('does not steal structural shortcuts from an editor input', () => {
    const input = document.createElement('input');
    expect(editorKeyboardCommand({ altKey: true, key: 's', target: input })).toBeNull();
    expect(editorKeyboardCommand({ altKey: true, key: 'w', target: input })).toBeNull();
  });
});
