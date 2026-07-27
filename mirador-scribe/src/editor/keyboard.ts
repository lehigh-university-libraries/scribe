export type EditorKeyboardCommand =
  | 'save'
  | 'dismiss-overlay'
  | 'undo'
  | 'redo'
  | 'delete'
  | 'edit-overlay'
  | 'split-line'
  | 'join-lines'
  | 'join-words'
  | 'retranscribe'
  | 'publish';

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
  if (!command && event.altKey && key.toLowerCase() === 's') return 'split-line';
  if (!command && event.altKey && key.toLowerCase() === 'l') return 'join-lines';
  if (!command && event.altKey && key.toLowerCase() === 'w') return 'join-words';
  if (!command && event.altKey && key.toLowerCase() === 'r') return 'retranscribe';
  if (!command && event.altKey && key.toLowerCase() === 'p') return 'publish';
  if (!command && !event.altKey && key.toLowerCase() === 'e') return 'edit-overlay';
  return null;
}
