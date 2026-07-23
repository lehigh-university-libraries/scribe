/**
 * @typedef {import('../types/scribe').EditorSession} EditorSession
 * @typedef {import('../types/scribe').EditorSessionAction} EditorSessionAction
 * @typedef {import('../types/scribe').EditorSessionStatus} EditorSessionStatus
 * @typedef {import('../types/scribe').IIIFAnnotation} IIIFAnnotation
 * @typedef {import('../types/scribe').IIIFAnnotationPage} IIIFAnnotationPage
 * @typedef {import('../types/scribe').PagePatch} PagePatch
 * @typedef {import('../types/scribe').RawIIIFProperties} RawIIIFProperties
 */

const HISTORY_LIMIT = 100;

/** @template T @param {T} value @returns {T} */
function clone(value) {
  return value == null ? value : structuredClone(value);
}

/** @param {IIIFAnnotationPage | null} page @returns {IIIFAnnotationPage | null} */
function copyPage(page) {
  if (!page) return page;
  return {
    ...page,
    items: Array.isArray(page.items) ? [...page.items] : [],
  };
}

/** @param {IIIFAnnotationPage | null | undefined} page @returns {Map<string, IIIFAnnotation>} */
function itemsById(page) {
  /** @type {Array<[string, IIIFAnnotation]>} */
  const entries = [];
  for (const item of Array.isArray(page?.items) ? page.items : []) {
    if (typeof item.id === 'string' && item.id) entries.push([item.id, item]);
  }
  return new Map(entries);
}

/** @param {unknown} left @param {unknown} right @returns {boolean} */
function same(left, right) {
  if (Object.is(left, right)) return true;
  if (left instanceof String || right instanceof String) {
    return left instanceof String && right instanceof String && left.valueOf() === right.valueOf();
  }
  if (left === null || right === null || typeof left !== 'object' || typeof right !== 'object') {
    return false;
  }
  if (Array.isArray(left) || Array.isArray(right)) {
    return Array.isArray(left)
      && Array.isArray(right)
      && left.length === right.length
      && left.every((value, index) => same(value, right[index]));
  }
  const leftKeys = Object.keys(left);
  const rightKeys = Object.keys(right);
  const leftRecord = /** @type {Record<string, unknown>} */ (left);
  const rightRecord = /** @type {Record<string, unknown>} */ (right);
  return leftKeys.length === rightKeys.length
    && leftKeys.every((key) => Object.hasOwn(right, key) && same(leftRecord[key], rightRecord[key]));
}

/** @param {IIIFAnnotationPage | null | undefined} page @returns {RawIIIFProperties | null} */
function pageMetadata(page) {
  if (!page) return null;
  const { items: _items, ...metadata } = page;
  return metadata;
}

/** @param {IIIFAnnotationPage | null | undefined} page @returns {string[]} */
function itemIds(page) {
  return (Array.isArray(page?.items) ? page.items : []).map((item) => String(item?.id || ''));
}

/** @param {string[]} left @param {string[]} right @returns {boolean} */
function sameStringArray(left, right) {
  return left.length === right.length && left.every((value, index) => value === right[index]);
}

/**
 * A history entry stores only changed annotations. Text edits on a 10,000 word
 * page therefore retain one annotation instead of cloning the entire page.
 */
/** @param {IIIFAnnotationPage | null | undefined} beforePage @param {IIIFAnnotationPage | null | undefined} afterPage @returns {PagePatch} */
function createPagePatch(beforePage, afterPage) {
  const beforeItems = itemsById(beforePage);
  const afterItems = itemsById(afterPage);
  const changes = [];
  const ids = new Set([...beforeItems.keys(), ...afterItems.keys()]);
  for (const id of ids) {
    const before = beforeItems.get(id) || null;
    const after = afterItems.get(id) || null;
    if (!same(before, after)) {
      changes.push({
        after: clone(after),
        before: clone(before),
        id,
      });
    }
  }

  const beforeMetadata = pageMetadata(beforePage);
  const afterMetadata = pageMetadata(afterPage);
  const metadataChanged = !same(beforeMetadata, afterMetadata);
  const beforeOrder = itemIds(beforePage);
  const afterOrder = itemIds(afterPage);
  const orderChanged = !sameStringArray(beforeOrder, afterOrder);
  return {
    changes,
    metadataAfter: metadataChanged ? clone(afterMetadata) : null,
    metadataBefore: metadataChanged ? clone(beforeMetadata) : null,
    orderAfter: orderChanged ? afterOrder : null,
    orderBefore: orderChanged ? beforeOrder : null,
  };
}

/** @param {PagePatch} patch @returns {boolean} */
function patchIsEmpty(patch) {
  return patch.changes.length === 0
    && patch.metadataBefore === null
    && patch.orderBefore === null;
}

/** @param {IIIFAnnotationPage} page @param {PagePatch} patch @param {'forward' | 'backward'} direction @returns {IIIFAnnotationPage} */
function applyPagePatch(page, patch, direction) {
  const useAfter = direction === 'forward';
  const items = itemsById(page);
  for (const change of patch.changes) {
    const value = useAfter ? change.after : change.before;
    if (value) items.set(change.id, clone(value));
    else items.delete(change.id);
  }

  const wantedOrder = useAfter ? patch.orderAfter : patch.orderBefore;
  let nextItems;
  if (wantedOrder) {
    const included = new Set(wantedOrder);
    nextItems = /** @type {IIIFAnnotation[]} */ (wantedOrder.map((id) => items.get(id)).filter(Boolean));
    // Be defensive if the page received an unrelated remote annotation after
    // this local patch was recorded.
    for (const [id, item] of items) {
      if (!included.has(id)) nextItems.push(item);
    }
  } else {
    const currentIds = new Set();
    nextItems = (Array.isArray(page?.items) ? page.items : [])
      .map((item) => (item.id ? items.get(item.id) : undefined))
      .filter((item) => {
        if (!item || currentIds.has(item.id)) return false;
        currentIds.add(item.id);
        return true;
      });
    nextItems = /** @type {IIIFAnnotation[]} */ (nextItems);
    for (const [id, item] of items) {
      if (!currentIds.has(id)) nextItems.push(item);
    }
  }

  const metadata = useAfter ? patch.metadataAfter : patch.metadataBefore;
  return {
    ...(metadata === null ? pageMetadata(page) : clone(metadata)),
    items: nextItems,
  };
}

/** @param {IIIFAnnotationPage | null | undefined} left @param {IIIFAnnotationPage | null | undefined} right @returns {boolean} */
function pagesEqual(left, right) {
  if (left === right) return true;
  return patchIsEmpty(createPagePatch(left, right));
}

/** @param {IIIFAnnotationPage | null | undefined} page @returns {string} */
function pageId(page) {
  return typeof page?.id === 'string' ? page.id.trim() : '';
}

/** @param {IIIFAnnotationPage | null | undefined} basePage @param {IIIFAnnotationPage | null | undefined} draftPage @returns {Set<string>} */
function pageLocallyTouchedIds(basePage, draftPage) {
  const baseItems = itemsById(basePage);
  const draftItems = itemsById(draftPage);
  const touched = new Set();
  for (const id of new Set([...baseItems.keys(), ...draftItems.keys()])) {
    if (!same(baseItems.get(id), draftItems.get(id))) touched.add(id);
  }
  for (const id of orderTouchedIds(itemIds(basePage), itemIds(draftPage))) touched.add(id);
  if (!same(pageMetadata(basePage), pageMetadata(draftPage))) {
    const id = pageId(basePage) || pageId(draftPage);
    if (id) touched.add(id);
  }
  return touched;
}

/** @param {EditorSession} session @param {IIIFAnnotationPage} draftPage @returns {string[]} */
function pendingIdsForDraft(session, draftPage) {
  const touched = pageLocallyTouchedIds(session?.basePage, draftPage);
  return (session?.pendingRemoteIds || []).filter((id) => touched.has(id));
}

/**
 * @param {EditorSession} session
 * @param {IIIFAnnotationPage} draftPage
 * @returns {Pick<EditorSession, 'conflictKind' | 'dirty' | 'error' | 'pendingRemoteIds' | 'status'>}
 */
function draftLifecycle(session, draftPage) {
  const dirty = !pagesEqual(session?.basePage, draftPage);
  const pendingRemoteIds = pendingIdsForDraft(session, draftPage);
  const resolvedConflict = session?.status === 'conflict'
    && (!dirty || (session?.conflictKind === 'transform' && pendingRemoteIds.length === 0));
  return {
    conflictKind: resolvedConflict ? null : session?.conflictKind || null,
    dirty,
    error: resolvedConflict ? null : session?.error || null,
    pendingRemoteIds,
    status: resolvedConflict ? 'ready' : session?.status || 'ready',
  };
}

/** @param {IIIFAnnotationPage} page @param {Map<string, IIIFAnnotation>} items @returns {IIIFAnnotationPage} */
function pageWithItems(page, items) {
  return {
    ...(clone(pageMetadata(page)) || { type: 'AnnotationPage' }),
    items: Array.from(items.values()).map(clone),
  };
}

/** @param {string[]} referenceIds @param {string[]} candidateIds @returns {string[]} */
function projectedSharedOrder(referenceIds, candidateIds) {
  const shared = new Set(candidateIds);
  return referenceIds.filter((id) => shared.has(id));
}

/** @param {string[]} ids @returns {Map<string, { next: string, previous: string }>} */
function orderNeighbours(ids) {
  return new Map(ids.map((id, index) => [id, {
    next: ids[index + 1] || '',
    previous: ids[index - 1] || '',
  }]));
}

/** @param {string[]} baseIds @param {string[]} candidateIds @returns {Set<string>} */
function orderTouchedIds(baseIds, candidateIds) {
  const baseOrder = projectedSharedOrder(baseIds, candidateIds);
  const candidateOrder = projectedSharedOrder(candidateIds, baseIds);
  if (sameStringArray(baseOrder, candidateOrder)) return new Set();

  const baseNeighbours = orderNeighbours(baseOrder);
  const candidateNeighbours = orderNeighbours(candidateOrder);
  return new Set(baseOrder.filter((id) => {
    const before = baseNeighbours.get(id);
    const after = candidateNeighbours.get(id);
    return before?.next !== after?.next || before?.previous !== after?.previous;
  }));
}

/** @param {string[]} currentIds @param {string[]} draftIds @param {Set<string>} idsToPlace @returns {string[]} */
function placeIdsAtDraftAnchors(currentIds, draftIds, idsToPlace) {
  if (idsToPlace.size === 0) return currentIds;
  const retained = currentIds.filter((id) => !idsToPlace.has(id));
  const retainedIds = new Set(retained);
  const beforeAnchor = new Map();
  /** @type {string[]} */
  const atEnd = [];
  let nextAnchor = '';

  for (let index = draftIds.length - 1; index >= 0; index -= 1) {
    const id = draftIds[index];
    if (retainedIds.has(id)) {
      nextAnchor = id;
    } else if (idsToPlace.has(id)) {
      const bucket = nextAnchor
        ? beforeAnchor.get(nextAnchor) || []
        : atEnd;
      bucket.push(id);
      if (nextAnchor) beforeAnchor.set(nextAnchor, bucket);
    }
  }

  /** @type {string[]} */
  const result = [];
  for (const id of retained) {
    const additions = beforeAnchor.get(id);
    if (additions) result.push(...additions.reverse());
    result.push(id);
  }
  result.push(...atEnd.reverse());
  return result;
}

/**
 * @param {IIIFAnnotationPage} basePage
 * @param {IIIFAnnotationPage} draftPage
 * @param {IIIFAnnotationPage} remotePage
 * @returns {{ conflictIds: string[], disagreementIds: Set<string>, ids: string[], localTouched: Set<string> }}
 */
function mergeReadingOrder(basePage, draftPage, remotePage) {
  const baseIds = itemIds(basePage);
  const draftIds = itemIds(draftPage);
  const remoteIds = itemIds(remotePage);
  const localTouched = orderTouchedIds(baseIds, draftIds);
  const remoteTouched = orderTouchedIds(baseIds, remoteIds);
  const remoteSet = new Set(remoteIds);
  const draftSet = new Set(draftIds);
  const commonBaseIds = new Set(baseIds.filter((id) => draftSet.has(id) && remoteSet.has(id)));
  const draftComparableOrder = draftIds.filter((id) => commonBaseIds.has(id));
  const remoteComparableOrder = remoteIds.filter((id) => commonBaseIds.has(id));
  const draftNeighbours = orderNeighbours(draftComparableOrder);
  const remoteNeighbours = orderNeighbours(remoteComparableOrder);
  const disagreementIds = new Set([...commonBaseIds].filter((id) => (
    !same(draftNeighbours.get(id), remoteNeighbours.get(id))
  )));

  let ids = [...remoteIds];
  const locallyReordered = new Set([...localTouched].filter((id) => remoteSet.has(id)));
  ids = placeIdsAtDraftAnchors(ids, draftIds, locallyReordered);

  const knownIds = new Set(ids);
  const localOnly = new Set(draftIds.filter((id) => !knownIds.has(id)));
  ids = placeIdsAtDraftAnchors(ids, draftIds, localOnly);

  return {
    conflictIds: [...localTouched].filter((id) => (
      remoteTouched.has(id) && disagreementIds.has(id)
    )),
    disagreementIds,
    ids,
    localTouched,
  };
}

/**
 * @param {IIIFAnnotationPage} basePage
 * @param {IIIFAnnotationPage} draftPage
 * @param {IIIFAnnotationPage} remotePage
 * @param {string[]} [priorPendingIds]
 * @returns {{ conflictIds: string[], page: IIIFAnnotationPage, pendingRemoteIds: string[] }}
 */
function mergeDraftOntoRemote(basePage, draftPage, remotePage, priorPendingIds = []) {
  const baseItems = itemsById(basePage);
  const draftItems = itemsById(draftPage);
  const remoteItems = itemsById(remotePage);
  const mergedItems = new Map();
  const pendingRemoteIds = new Set();
  const conflictIds = new Set();
  const priorPending = new Set(priorPendingIds);
  const orderMerge = mergeReadingOrder(basePage, draftPage, remotePage);
  const orderConflicts = new Set(orderMerge.conflictIds);
  orderMerge.conflictIds.forEach((id) => conflictIds.add(id));
  const ids = orderMerge.ids;

  for (const id of ids) {
    const base = baseItems.get(id);
    const draft = draftItems.get(id);
    const remote = remoteItems.get(id);
    const locallyChanged = !same(base, draft);

    if (locallyChanged) {
      if (draft) mergedItems.set(id, clone(draft));
      // A repeated poll of the same remote revision must not make an existing
      // unresolved conflict disappear.
      if (!same(draft, remote) && (!same(base, remote) || priorPending.has(id))) {
        pendingRemoteIds.add(id);
      }
      if (!same(draft, remote) && !same(base, remote)) conflictIds.add(id);
    } else if (remote) {
      mergedItems.set(id, clone(remote));
    }
    if ((orderConflicts.has(id) || (priorPending.has(id) && orderMerge.disagreementIds.has(id)))
      && !pendingRemoteIds.has(id)) pendingRemoteIds.add(id);
  }

  return {
    conflictIds: [...conflictIds],
    page: pageWithItems(remotePage, mergedItems),
    pendingRemoteIds: [...pendingRemoteIds],
  };
}

/** @param {string | number | bigint | null | undefined} candidate @param {string | number | bigint | null | undefined} current @returns {boolean} */
function revisionIsOlder(candidate, current) {
  if (!candidate || !current) return false;
  try {
    return BigInt(candidate) < BigInt(current);
  } catch {
    // Revisions are currently uint64 strings. If that contract becomes opaque,
    // equality checks still keep responses tied to their submitted base.
    return false;
  }
}

/** @param {IIIFAnnotationPage} remotePage @param {IIIFAnnotationPage} draftPage @returns {PagePatch[]} */
function historyForRebasedDraft(remotePage, draftPage) {
  if (pagesEqual(remotePage, draftPage)) return [];
  return [createPagePatch(remotePage, draftPage)];
}

/** @param {IIIFAnnotationPage | null} [page] @param {string | number | bigint} [revision] @returns {EditorSession} */
export function createEditorSession(page = null, revision = '') {
  return {
    basePage: clone(page),
    conflictKind: null,
    dirty: false,
    draftPage: clone(page),
    error: null,
    pendingRemoteIds: [],
    redoStack: [],
    revision: String(revision || ''),
    status: 'ready',
    undoStack: [],
  };
}

/** @param {EditorSession | null | undefined} session @returns {boolean} */
export function sessionIsDirty(session) {
  return Boolean(session?.dirty);
}

/**
 * Records one user-visible edit. No-op changes do not pollute undo history.
 * @param {EditorSession} session
 * @param {IIIFAnnotationPage | null} nextPage
 * @returns {EditorSession}
 */
export function editSession(session, nextPage) {
  if (!nextPage) return session;
  const previous = session?.draftPage;
  const patch = createPagePatch(previous, nextPage);
  if (patchIsEmpty(patch)) return session;
  const lifecycle = draftLifecycle(session, nextPage);
  return {
    ...session,
    ...lifecycle,
    draftPage: copyPage(nextPage),
    error: lifecycle.status === 'conflict' ? lifecycle.error : null,
    redoStack: [],
    status: session?.status === 'saving'
      ? 'saving'
      : (lifecycle.status === 'conflict' ? 'conflict' : 'ready'),
    undoStack: previous
      ? [...session.undoStack.slice(-(HISTORY_LIMIT - 1)), patch]
      : session.undoStack,
  };
}

/** @param {EditorSession} session @returns {EditorSession} */
export function undoSession(session) {
  if (!session?.draftPage || session.undoStack.length === 0) return session;
  const patch = session.undoStack[session.undoStack.length - 1];
  const previous = applyPagePatch(session.draftPage, patch, 'backward');
  return {
    ...session,
    ...draftLifecycle(session, previous),
    draftPage: previous,
    redoStack: [...session.redoStack, patch],
    undoStack: session.undoStack.slice(0, -1),
  };
}

/** @param {EditorSession} session @returns {EditorSession} */
export function redoSession(session) {
  if (!session?.draftPage || session.redoStack.length === 0) return session;
  const patch = session.redoStack[session.redoStack.length - 1];
  const next = applyPagePatch(session.draftPage, patch, 'forward');
  return {
    ...session,
    ...draftLifecycle(session, next),
    draftPage: next,
    redoStack: session.redoStack.slice(0, -1),
    undoStack: [...session.undoStack.slice(-(HISTORY_LIMIT - 1)), patch],
  };
}

/**
 * Rebases a draft onto a freshly loaded server page. Per annotation, an
 * untouched draft follows the server while a locally changed/deleted item is
 * preserved. Page metadata is refreshed from the server and annotation
 * content is merged by canonical Annotation ID.
 *
 * @param {EditorSession} session
 * @param {IIIFAnnotationPage | null} remotePage
 * @param {string | number | bigint} [revision]
 * @returns {EditorSession}
 */
export function rebaseSession(session, remotePage, revision = session?.revision || '') {
  if (!remotePage) return session;
  if (!session?.basePage || !session?.draftPage) {
    return createEditorSession(remotePage, revision);
  }

  const currentPageId = pageId(session.basePage) || pageId(session.draftPage);
  const remotePageId = pageId(remotePage);
  if (currentPageId && remotePageId && currentPageId !== remotePageId) {
    return createEditorSession(remotePage, revision);
  }
  if (revisionIsOlder(revision, session?.revision)) return session;

  const merged = mergeDraftOntoRemote(
    session.basePage,
    session.draftPage,
    remotePage,
    session.pendingRemoteIds,
  );
  const currentTouched = pageLocallyTouchedIds(remotePage, merged.page);

  return {
    ...session,
    basePage: clone(remotePage),
    dirty: !pagesEqual(remotePage, merged.page),
    draftPage: merged.page,
    error: null,
    pendingRemoteIds: merged.pendingRemoteIds.filter((id) => currentTouched.has(id)),
    redoStack: [],
    revision: String(revision || ''),
    status: session.status === 'saving' ? 'saving' : 'ready',
    conflictKind: null,
    undoStack: historyForRebasedDraft(remotePage, merged.page),
  };
}

/** @param {EditorSession} session @param {string} annotationId @returns {boolean} */
export function annotationLocallyChanged(session, annotationId) {
  if (!session?.basePage || !session?.draftPage || !annotationId) return false;
  return !same(itemsById(session.basePage).get(annotationId), itemsById(session.draftPage).get(annotationId));
}

/** @param {EditorSession} session @param {IIIFAnnotation} annotation @returns {EditorSession} */
export function applyRemoteAnnotation(session, annotation) {
  if (!session?.basePage || !session?.draftPage || !annotation?.id) return session;
  const baseItems = itemsById(session.basePage);
  const draftItems = itemsById(session.draftPage);
  const base = baseItems.get(annotation.id);
  const draft = draftItems.get(annotation.id);
  const locallyChanged = !same(base, draft);

  baseItems.set(annotation.id, clone(annotation));
  if (!locallyChanged) draftItems.set(annotation.id, clone(annotation));

  const pending = new Set(session.pendingRemoteIds);
  if (locallyChanged
    && !same(draft, annotation)
    && (!same(base, annotation) || pending.has(annotation.id))) pending.add(annotation.id);
  else pending.delete(annotation.id);

  const nextBasePage = pageWithItems(session.basePage, baseItems);
  const nextDraftPage = pageWithItems(session.draftPage, draftItems);
  const lifecycle = draftLifecycle({
    ...session,
    basePage: nextBasePage,
    pendingRemoteIds: [...pending],
  }, nextDraftPage);

  return {
    ...session,
    ...lifecycle,
    basePage: nextBasePage,
    draftPage: nextDraftPage,
    redoStack: [],
    undoStack: historyForRebasedDraft(nextBasePage, nextDraftPage),
  };
}

/**
 * Accepts the result of an atomic save without discarding edits made after the
 * request began. The submitted page/revision tag identifies exactly which
 * draft the server committed; the current draft is then rebased onto that
 * saved base.
 *
 * @param {EditorSession} session
 * @param {IIIFAnnotationPage | null} savedPage
 * @param {string | number | bigint} revision
 * @param {IIIFAnnotationPage | null} [submittedPage]
 * @param {string | number | bigint} [submittedRevision]
 * @returns {EditorSession}
 */
export function acceptSavedSession(
  session,
  savedPage,
  revision,
  submittedPage = session?.draftPage,
  submittedRevision = session?.revision || '',
) {
  const page = savedPage || submittedPage || session?.draftPage || null;
  if (!session || !page) return createEditorSession(page, revision);
  const submittedPageId = pageId(submittedPage);
  const savedPageId = pageId(page);
  /** @returns {EditorSession} */
  const settleStaleResponse = () => (session.status === 'saving'
    ? { ...session, error: null, status: 'ready' }
    : session);
  if (submittedPageId && savedPageId && submittedPageId !== savedPageId) return settleStaleResponse();
  if (revisionIsOlder(revision, session.revision)) return settleStaleResponse();
  if (submittedRevision && String(session.revision || '') !== String(submittedRevision)) {
    return settleStaleResponse();
  }
  if (!session.draftPage || !submittedPage || pagesEqual(session.draftPage, submittedPage)) {
    return createEditorSession(page, revision);
  }

  const merged = mergeDraftOntoRemote(
    submittedPage,
    session.draftPage,
    page,
    session.pendingRemoteIds,
  );
  return {
    ...session,
    basePage: clone(page),
    dirty: !pagesEqual(page, merged.page),
    draftPage: merged.page,
    error: null,
    conflictKind: null,
    pendingRemoteIds: merged.pendingRemoteIds,
    redoStack: [],
    revision: String(revision || ''),
    status: 'ready',
    undoStack: historyForRebasedDraft(page, merged.page),
  };
}

/**
 * Applies a pure full-page operation that was computed from `submittedPage`
 * onto the latest draft. Changes made while the request was in flight win;
 * non-overlapping transform changes are retained and overlapping annotation,
 * metadata, or reading-order changes become pending conflicts.
 *
 * @param {EditorSession} session
 * @param {IIIFAnnotationPage | null} submittedPage
 * @param {IIIFAnnotationPage | null} transformedPage
 * @param {{ affectedIds?: string[], atomic?: boolean }} [options]
 * @returns {EditorSession}
 */
export function applyPageTransformSession(
  session,
  submittedPage,
  transformedPage,
  { affectedIds = [], atomic = false } = {},
) {
  if (!session?.draftPage || !submittedPage || !transformedPage) return session;
  const expectedPageId = pageId(submittedPage);
  if ((expectedPageId && pageId(transformedPage) !== expectedPageId)
    || (expectedPageId && pageId(session.draftPage) !== expectedPageId)) return session;

  if (pagesEqual(session.draftPage, submittedPage)) {
    return editSession(session, transformedPage);
  }

  const changedSinceSubmission = pageLocallyTouchedIds(submittedPage, session.draftPage);
  const transformedIds = pageLocallyTouchedIds(submittedPage, transformedPage);
  const operationAffectedIds = new Set([...(affectedIds || []), ...transformedIds]);
  const affectedOverlapIds = [...operationAffectedIds].filter((id) => changedSinceSubmission.has(id));
  if (atomic && affectedOverlapIds.length > 0) {
    const pendingRemoteIds = new Set(session.pendingRemoteIds);
    const currentTouched = pageLocallyTouchedIds(session.basePage, session.draftPage);
    affectedOverlapIds.filter((id) => currentTouched.has(id)).forEach((id) => pendingRemoteIds.add(id));
    const preserveSaveConflict = session.status === 'conflict' && session.conflictKind === 'save';
    return {
      ...session,
      conflictKind: preserveSaveConflict ? 'save' : 'transform',
      error: preserveSaveConflict
        ? session.error
        : 'The draft changed while this structural operation was running. Your newer draft was preserved; retry the operation.',
      pendingRemoteIds: [...pendingRemoteIds],
      status: 'conflict',
    };
  }

  const merged = mergeDraftOntoRemote(
    submittedPage,
    session.draftPage,
    transformedPage,
    session.pendingRemoteIds,
  );
  const submittedMetadata = pageMetadata(submittedPage);
  const currentMetadata = pageMetadata(session.draftPage);
  const transformedMetadata = pageMetadata(transformedPage);
  const currentMetadataChanged = !same(submittedMetadata, currentMetadata);
  const transformedMetadataChanged = !same(submittedMetadata, transformedMetadata);
  const metadataConflict = currentMetadataChanged
    && transformedMetadataChanged
    && !same(currentMetadata, transformedMetadata);
  const metadata = currentMetadataChanged ? currentMetadata : transformedMetadata;
  const nextPage = {
    ...(clone(metadata) || { type: 'AnnotationPage' }),
    items: merged.page.items,
  };
  const pendingRemoteIds = new Set([
    ...session.pendingRemoteIds,
    ...merged.pendingRemoteIds,
  ]);
  if (metadataConflict && expectedPageId) pendingRemoteIds.add(expectedPageId);
  const transformConflictIds = new Set(merged.conflictIds);
  if (metadataConflict && expectedPageId) transformConflictIds.add(expectedPageId);
  const patch = createPagePatch(session.draftPage, nextPage);
  const locallyTouched = pageLocallyTouchedIds(session.basePage, nextPage);
  const retainedPendingRemoteIds = [...pendingRemoteIds].filter((id) => locallyTouched.has(id));
  const lifecycle = draftLifecycle({
    ...session,
    pendingRemoteIds: retainedPendingRemoteIds,
  }, nextPage);
  const preservedConflictKind = lifecycle.status === 'conflict' ? lifecycle.conflictKind : null;
  const conflictKind = preservedConflictKind
    || (transformConflictIds.size > 0 ? 'transform' : null);

  return {
    ...session,
    ...lifecycle,
    draftPage: nextPage,
    error: preservedConflictKind
      ? lifecycle.error
      : (transformConflictIds.size > 0
        ? 'The draft changed while this operation was running. Non-overlapping results were applied and newer overlapping edits were preserved.'
        : null),
    conflictKind,
    pendingRemoteIds: retainedPendingRemoteIds,
    redoStack: [],
    status: session.status === 'saving'
      ? 'saving'
      : (conflictKind ? 'conflict' : 'ready'),
    undoStack: patchIsEmpty(patch)
      ? session.undoStack
      : [...session.undoStack.slice(-(HISTORY_LIMIT - 1)), patch],
  };
}

/** @param {EditorSession} session @param {EditorSessionStatus} status @param {string | null} [error] @returns {EditorSession} */
function lifecycleSession(session, status, error = null) {
  return { ...session, error, status };
}

/** @param {never} _action @returns {never} */
function assertNeverEditorSessionAction(_action) {
  throw new TypeError('Unsupported editor session action.');
}

/** @param {EditorSession} session @param {EditorSessionAction} action @returns {EditorSession} */
export function editorSessionReducer(session, action) {
  switch (action.type) {
    case 'edit':
      return editSession(session, action.page);
    case 'undo':
      return undoSession(session);
    case 'redo':
      return redoSession(session);
    case 'load-start':
      return lifecycleSession(session, 'loading');
    case 'rebase':
    case 'loaded':
      return rebaseSession(session, action.page, action.revision);
    case 'remote-annotation':
      return applyRemoteAnnotation(session, action.annotation);
    case 'save-start':
      return sessionIsDirty(session) ? lifecycleSession(session, 'saving') : lifecycleSession(session, 'ready');
    case 'save-conflict':
      return {
        ...lifecycleSession(session, 'conflict', action.error),
        conflictKind: 'save',
      };
    case 'save-error':
    case 'load-error':
      return {
        ...lifecycleSession(session, 'error', action.error),
        conflictKind: null,
      };
    case 'dismiss-error':
      return { ...lifecycleSession(session, 'ready'), conflictKind: null };
    case 'saved':
      return acceptSavedSession(
        session,
        action.page,
        action.revision,
        action.submittedPage,
        action.submittedRevision,
      );
    case 'transform-result':
      return applyPageTransformSession(session, action.submittedPage, action.page, {
        affectedIds: action.affectedIds,
        atomic: action.atomic,
      });
    case 'reset':
      return createEditorSession(action.page, action.revision);
    default:
      return assertNeverEditorSessionAction(action);
  }
}

// Exported for deterministic scale tests and diagnostics; consumers should
// dispatch reducer actions rather than manipulating patches directly.
export const editorHistoryLimit = HISTORY_LIMIT;
