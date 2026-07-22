export type EditorKeyboardCommand =
  | 'save'
  | 'dismiss-overlay'
  | 'undo'
  | 'redo'
  | 'delete'
  | 'edit-overlay';

export function isEditableEventTarget(target: EventTarget | null | undefined): boolean {
  return target instanceof HTMLElement
    && (target.tagName === 'INPUT' || target.tagName === 'TEXTAREA' || target.isContentEditable);
}

/**
 * Resolves window-level editor shortcuts without stealing native text-editing
 * keys. Escape and Save are intentionally available from inside an input.
 */
export function editorKeyboardCommand(
  event: Pick<KeyboardEvent, 'altKey' | 'ctrlKey' | 'key' | 'metaKey' | 'shiftKey' | 'target'>,
  editableTarget = isEditableEventTarget(event.target),
): EditorKeyboardCommand | null {
  const key = String(event.key || '');
  const command = Boolean(event.metaKey || event.ctrlKey);
  if (command && key.toLowerCase() === 's') return 'save';
  if (!command && !event.altKey && key === 'Escape') return 'dismiss-overlay';
  if (editableTarget) return null;
  if (command && key.toLowerCase() === 'z') return event.shiftKey ? 'redo' : 'undo';
  if (command && (key === 'Backspace' || key === 'Delete')) return 'delete';
  if (!command && !event.altKey && key.toLowerCase() === 'e') return 'edit-overlay';
  return null;
}
