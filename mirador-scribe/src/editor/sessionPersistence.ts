import { sessionIsDirty } from './session';
import { dirtyEditorSessions, editorSessionForCanvas } from './sessionCache';
import type {
  AnnotationPageSnapshot,
  EditorSessionCache,
  IIIFAnnotationPage,
  ScribeAdapterFactory,
} from '../types/scribe';

export interface SavedEditorSession extends Omit<AnnotationPageSnapshot, 'page'> {
  page: IIIFAnnotationPage | null;
}

export interface AcceptedSave extends AnnotationPageSnapshot {
  submittedPage: IIIFAnnotationPage;
  submittedRevision: string;
  type: 'saved';
}

export interface SaveCachedEditorSessionsOptions {
  acceptSaved(canvasId: string, result: AcceptedSave): void | Promise<void>;
  adapterFactory: ScribeAdapterFactory;
  beginSave?(canvasId: string): void | Promise<void>;
  canvasIds?: string[] | null;
  getCache(): EditorSessionCache;
  requireAllClean?: boolean;
  syncPage?(page: IIIFAnnotationPage, canvasId: string): void | Promise<void>;
}

export interface SaveCachedEditorSessionsResult {
  error: Error | null;
  failedCanvasId: string;
  ok: boolean;
  remainingCanvasIds: string[];
  snapshots: Map<string, SavedEditorSession>;
}

function uniqueCanvasIds(canvasIds: string[] | null | undefined): string[] {
  return [...new Set((canvasIds || [])
    .map((canvasId) => (typeof canvasId === 'string' ? canvasId.trim() : ''))
    .filter(Boolean))];
}

function pageId(page: IIIFAnnotationPage | null | undefined): string {
  return typeof page?.id === 'string' ? page.id.trim() : '';
}

/**
 * Saves a stable set of cached Canvas sessions in sequence. `getCache` is read
 * before every request and again before reporting success, so edits made while
 * a save is in flight remain dirty and force the caller to block navigation or
 * publication instead of announcing a false success.
 *
 * Omitting `canvasIds` is the global-save mode: every dirty session present at
 * the start is submitted, and success requires the entire cache to be clean at
 * the end (including sessions dirtied during the operation).
 */
export async function saveCachedEditorSessions({
  acceptSaved,
  adapterFactory,
  beginSave,
  canvasIds,
  getCache,
  requireAllClean = canvasIds == null,
  syncPage,
}: SaveCachedEditorSessionsOptions): Promise<SaveCachedEditorSessionsResult> {
  const targetCanvasIds = uniqueCanvasIds(canvasIds == null
    ? dirtyEditorSessions(getCache()).map(({ canvasId }) => canvasId)
    : canvasIds);
  const targetSet = new Set(targetCanvasIds);
  const snapshots = new Map<string, SavedEditorSession>();
  let error: Error | null = null;
  let failedCanvasId = '';

  for (const canvasId of targetCanvasIds) {
    const session = editorSessionForCanvas(getCache(), canvasId);
    if (!session.draftPage) {
      error = new Error(`Canvas ${canvasId} has no AnnotationPage to save.`);
      failedCanvasId = canvasId;
      break;
    }

    if (!sessionIsDirty(session)) {
      snapshots.set(canvasId, { page: session.draftPage, revision: session.revision });
      continue;
    }

    const submittedPage = session.draftPage;
    const submittedRevision = session.revision;
    try {
      await beginSave?.(canvasId);
      const adapter = adapterFactory(canvasId);
      if (!adapter || typeof adapter.savePage !== 'function') {
        throw new Error(`Canvas ${canvasId} has no annotation adapter.`);
      }
      const snapshot = await adapter.savePage(submittedPage, submittedRevision);
      if (!snapshot?.page) throw new Error(`Canvas ${canvasId} save returned no AnnotationPage.`);

      const beforeAcceptance = editorSessionForCanvas(getCache(), canvasId);
      const responseMatchesSubmittedBase = String(beforeAcceptance.revision || '') === String(submittedRevision || '')
        && (!pageId(submittedPage) || pageId(snapshot.page) === pageId(submittedPage));

      await acceptSaved(canvasId, {
        page: snapshot.page,
        revision: snapshot.revision,
        submittedPage,
        submittedRevision,
        type: 'saved',
      });
      const settled = editorSessionForCanvas(getCache(), canvasId);
      if (responseMatchesSubmittedBase) {
        await syncPage?.(settled.draftPage || snapshot.page, canvasId);
      }
      if (responseMatchesSubmittedBase) {
        snapshots.set(canvasId, snapshot);
      } else {
        snapshots.set(canvasId, {
          page: settled.basePage || settled.draftPage,
          revision: settled.revision,
        });
      }
    } catch (cause) {
      error = cause instanceof Error ? cause : new Error('Save failed.');
      failedCanvasId = canvasId;
      break;
    }
  }

  const remainingCanvasIds = dirtyEditorSessions(getCache())
    .map(({ canvasId }) => canvasId)
    .filter((canvasId) => requireAllClean || targetSet.has(canvasId));

  return {
    error,
    failedCanvasId,
    ok: !error && remainingCanvasIds.length === 0,
    remainingCanvasIds,
    snapshots,
  };
}
