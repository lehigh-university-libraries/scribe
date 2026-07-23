import {
  createEditorSession,
  editorSessionReducer,
  sessionIsDirty,
} from './session';
import type {
  EditorSession,
  EditorSessionAction,
  EditorSessionCache,
  IIIFAnnotationPage,
} from '../types/scribe';

export const maxCleanEditorSessions = 12;

function canvasKey(canvasId: string | null | undefined): string {
  return typeof canvasId === 'string' ? canvasId.trim() : '';
}

export function createEditorSessionCache(
  canvasId = '',
  page: IIIFAnnotationPage | null = null,
  revision: string | number | bigint = '',
): EditorSessionCache {
  const sessions = new Map<string, EditorSession>();
  const key = canvasKey(canvasId);
  if (key) sessions.set(key, createEditorSession(page, revision));
  return { accessOrder: key ? [key] : [], sessions };
}

export function editorSessionForCanvas(
  cache: EditorSessionCache | null | undefined,
  canvasId: string,
): EditorSession {
  const key = canvasKey(canvasId);
  return cache?.sessions.get(key) || createEditorSession();
}

/**
 * Applies an editor action only to its named Canvas. Keeping the Canvas ID on
 * every action prevents late network responses from being routed into whichever
 * Canvas happens to be focused when the response arrives.
 */
export function editorSessionCacheReducer(
  cache: EditorSessionCache,
  action: EditorSessionAction,
): EditorSessionCache {
  const key = canvasKey(action.canvasId);
  if (!key) return cache;

  const sessions = cache.sessions || new Map<string, EditorSession>();
  const hasSession = sessions.has(key);
  const current = sessions.get(key) || createEditorSession();
  const next = editorSessionReducer(current, action);
  const currentOrder = Array.isArray(cache.accessOrder) ? cache.accessOrder : [...sessions.keys()];
  const accessOrder = [...currentOrder.filter((canvasId) => canvasId !== key), key];
  if (hasSession && next === current
      && currentOrder.length === accessOrder.length
      && currentOrder.every((canvasId, index) => canvasId === accessOrder[index])) return cache;

  const nextSessions = new Map(sessions);
  nextSessions.set(key, next);

  // Dirty sessions are never evicted. Bound only clean cached Canvases so a
  // long document can be paged through without retaining every page forever.
  let cleanCount = [...nextSessions.values()].filter((session) => !sessionIsDirty(session)).length;
  const retainedOrder = [...accessOrder];
  while (cleanCount > maxCleanEditorSessions) {
    const evictionIndex = retainedOrder.findIndex((canvasId) => (
      canvasId !== key && !sessionIsDirty(nextSessions.get(canvasId))
    ));
    if (evictionIndex < 0) break;
    const [evictedCanvasId] = retainedOrder.splice(evictionIndex, 1);
    nextSessions.delete(evictedCanvasId);
    cleanCount -= 1;
  }
  return { accessOrder: retainedOrder, sessions: nextSessions };
}

export function dirtyEditorSessions(
  cache: EditorSessionCache | null | undefined,
): Array<{ canvasId: string; session: EditorSession }> {
  return [...(cache?.sessions || new Map<string, EditorSession>()).entries()]
    .filter(([, session]) => sessionIsDirty(session))
    .map(([canvasId, session]) => ({ canvasId, session }));
}

export function editorSessionCacheIsDirty(cache: EditorSessionCache | null | undefined): boolean {
  return dirtyEditorSessions(cache).length > 0;
}
