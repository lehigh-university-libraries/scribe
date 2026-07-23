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
});
