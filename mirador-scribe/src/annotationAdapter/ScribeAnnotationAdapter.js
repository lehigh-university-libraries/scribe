/**
 * @typedef {import('../types/scribe').AnnotationMutation} AnnotationMutation
 * @typedef {import('../types/scribe').AnnotationPageSnapshot} AnnotationPageSnapshot
 * @typedef {import('../types/scribe').CanonicalIIIFAnnotationPage} CanonicalIIIFAnnotationPage
 * @typedef {import('../types/scribe').DraftMutationResponse} DraftMutationResponse
 * @typedef {import('../types/scribe').IdentifiedIIIFAnnotation} IdentifiedIIIFAnnotation
 * @typedef {import('../types/scribe').IIIFAnnotation} IIIFAnnotation
 * @typedef {import('../types/scribe').IIIFAnnotationPage} IIIFAnnotationPage
 * @typedef {import('../types/scribe').RawIIIFProperties} RawIIIFProperties
 * @typedef {import('../types/scribe').ScribeAdapterRuntime} ScribeAdapterRuntime
 * @typedef {import('../types/scribe').ScribeAnnotationClient} ScribeAnnotationClient
 */

import { stringifyIIIFJSON } from '../utils/json';

/** @template T @param {T} value @returns {T} */
function clone(value) {
  return value == null ? value : structuredClone(value);
}

/** @param {unknown} snapshot @returns {AnnotationPageSnapshot} */
function normalizeSnapshot(snapshot) {
  if (!snapshot || typeof snapshot !== 'object') {
    throw new Error('The annotation service returned an invalid IIIF AnnotationPage');
  }
  const candidate = /** @type {RawIIIFProperties} */ (snapshot);
  const page = candidate.page || candidate.annotationPage || candidate;
  if (!page || typeof page !== 'object') {
    throw new Error('The annotation service returned an invalid IIIF AnnotationPage');
  }
  const pageCandidate = /** @type {RawIIIFProperties} */ (page);
  if (pageCandidate.type !== 'AnnotationPage'
    || typeof pageCandidate.id !== 'string'
    || !pageCandidate.id.trim()
    || !Array.isArray(pageCandidate.items)
    || !pageCandidate.items.every((item) => (
      item !== null
      && typeof item === 'object'
      && !Array.isArray(item)
      && typeof /** @type {RawIIIFProperties} */ (item).id === 'string'
    ))) {
    throw new Error('The annotation service returned an invalid IIIF AnnotationPage');
  }
  return {
    page: clone(/** @type {CanonicalIIIFAnnotationPage} */ (page)),
    revision: String(candidate.revision ?? ''),
    updatedAt: String(candidate.updatedAt ?? ''),
  };
}

/** @param {unknown} annotation @returns {IdentifiedIIIFAnnotation} */
function normalizeAnnotation(annotation) {
  if (!annotation || typeof annotation !== 'object' || Array.isArray(annotation)) {
    throw new Error('The annotation service returned an invalid IIIF Annotation');
  }
  const candidate = /** @type {RawIIIFProperties} */ (annotation);
  if (candidate.type !== 'Annotation' || typeof candidate.id !== 'string' || !candidate.id.trim()) {
    throw new Error('The annotation service returned an invalid IIIF Annotation');
  }
  return clone(/** @type {IdentifiedIIIFAnnotation} */ (candidate));
}

/**
 * Mirador annotation adapter backed by Scribe's page-level optimistic-
 * concurrency API. The page is the unit of persistence. Mirador's native
 * create/update/delete methods publish synchronous local-draft events; only
 * the editor's explicit Save action calls SaveAnnotationPage.
 */
export default class ScribeAnnotationAdapter {
  /**
   * @param {string} endpointUrl
   * @param {3} iiifPresentationVersion
   * @param {string} canvasId
   * @param {string} user
   * @param {ScribeAdapterRuntime} [runtime]
   */
  constructor(endpointUrl, iiifPresentationVersion, canvasId, user, runtime = {}) {
    if (iiifPresentationVersion !== 3) {
      throw new Error(`ScribeAnnotationAdapter expects IIIF Presentation 3, got '${iiifPresentationVersion}'`);
    }
    this.user = user || 'Scribe User';
    this.canvasId = canvasId;
    this.endpointUrl = endpointUrl;
    /** @type {ScribeAnnotationClient | null} */
    this.client = runtime.client || null;
    this.contextId = String(runtime?.contextId || '0');
    this.contextIdResolver = typeof runtime.resolveContextId === 'function' ? runtime.resolveContextId : null;
    this.contextIdResolved = !this.contextIdResolver;
    this.itemImageId = String(runtime?.itemImageId || '');
    this.windowId = String(runtime?.windowId || '').trim();
    /** @type {AnnotationPageSnapshot | null} */
    this.snapshot = null;
  }

  getStorageAdapterUser() {
    return this.user;
  }

  get annotationPageId() {
    if (this.snapshot?.page?.id) return this.snapshot.page.id;
    throw new Error('annotationPageId is unavailable until Scribe has loaded the canonical AnnotationPage');
  }

  /** @param {string} methodName @returns {ScribeAnnotationClient} */
  requireClient(methodName) {
    if (!this.client) {
      throw new Error(`${methodName} requires an injected Scribe Connect client`);
    }
    return this.client;
  }

  /** @param {string} methodName @returns {string} */
  requireItemImageId(methodName) {
    if (!this.itemImageId || this.itemImageId === '0') {
      throw new Error(`${methodName} requires an itemImageId`);
    }
    return this.itemImageId;
  }

  /** @param {string} methodName @param {string | null | undefined} annotationId @returns {string} */
  requireAnnotationId(methodName, annotationId) {
    const normalized = String(annotationId || '').trim();
    if (!normalized) throw new Error(`${methodName} requires a selected annotation ID`);
    return normalized;
  }

  /** @param {string} methodName @param {string[]} annotationIds @returns {string[]} */
  requireAnnotationIds(methodName, annotationIds) {
    if (!Array.isArray(annotationIds) || annotationIds.length < 2) {
      throw new Error(`${methodName} requires at least two selected annotation IDs`);
    }
    const normalized = annotationIds.map((annotationId) => this.requireAnnotationId(methodName, annotationId));
    if (new Set(normalized).size !== normalized.length) {
      throw new Error(`${methodName} requires distinct selected annotation IDs`);
    }
    return normalized;
  }

  /** @param {string} methodName @param {unknown} annotationPage @returns {string} */
  serializeDraftPage(methodName, annotationPage) {
    try {
      return stringifyIIIFJSON(normalizeSnapshot(annotationPage).page);
    } catch (error) {
      throw new Error(`${methodName} requires a complete IIIF AnnotationPage`, { cause: error });
    }
  }

  /** @param {string} methodName @param {unknown} annotationPage @returns {CanonicalIIIFAnnotationPage} */
  normalizeTransformedPage(methodName, annotationPage) {
    try {
      return normalizeSnapshot(annotationPage).page;
    } catch (error) {
      throw new Error(`${methodName} returned an invalid IIIF AnnotationPage`, { cause: error });
    }
  }

  /** @param {string} methodName @returns {Promise<string>} */
  async requireContextId(methodName) {
    const resolved = !this.contextIdResolved && this.contextIdResolver
      ? await this.contextIdResolver()
      : this.contextId;
    const contextId = String(resolved || '').trim();
    if (!/^[0-9]+$/.test(contextId)) {
      throw new Error(`${methodName} requires a valid processing context`);
    }
    this.contextId = contextId;
    this.contextIdResolved = true;
    return contextId;
  }

  /** @returns {Promise<AnnotationPageSnapshot>} */
  async loadSnapshot() {
    const client = this.requireClient('loadSnapshot');
    const itemImageId = this.requireItemImageId('loadSnapshot');
    this.snapshot = normalizeSnapshot(await client.getAnnotationPage(itemImageId));
    return clone(this.snapshot);
  }

  /**
   * @param {CanonicalIIIFAnnotationPage} page
   * @param {string} [expectedRevision]
   * @returns {Promise<AnnotationPageSnapshot>}
   */
  async savePage(page, expectedRevision = this.snapshot?.revision || '') {
    const client = this.requireClient('savePage');
    const itemImageId = this.requireItemImageId('savePage');
    this.snapshot = normalizeSnapshot(await client.saveAnnotationPage(
      itemImageId,
      stringifyIIIFJSON(page),
      String(expectedRevision || ''),
    ));
    return clone(this.snapshot);
  }

  /** @returns {Promise<CanonicalIIIFAnnotationPage>} */
  async all() {
    const snapshot = this.snapshot || await this.loadSnapshot();
    return clone(snapshot.page);
  }

  /**
   * @param {string} methodName
   * @param {AnnotationMutation} mutation
   * @returns {{ annotation: IIIFAnnotation | undefined, page: CanonicalIIIFAnnotationPage }}
   */
  dispatchDraftMutation(methodName, mutation) {
    if (typeof document === 'undefined' || typeof document.dispatchEvent !== 'function') {
      throw new Error(`${methodName} requires the mounted Scribe editor event bridge`);
    }
    if (!this.windowId) throw new Error(`${methodName} requires a windowId for the Scribe editor event bridge`);
    /** @type {DraftMutationResponse | undefined} */
    let response;
    let handled = false;
    /** @param {DraftMutationResponse} value */
    const respond = (value) => {
      if (handled) return;
      handled = true;
      response = value;
    };
    document.dispatchEvent(new CustomEvent('scribe:annotation-mutation', {
      detail: {
        ...clone(mutation),
        canvasId: this.canvasId,
        itemImageId: this.itemImageId,
        respond,
        windowId: this.windowId,
      },
    }));
    if (!handled) throw new Error(`${methodName} requires the mounted Scribe editor event bridge`);
    const bridgeResponse = /** @type {DraftMutationResponse | undefined} */ (response);
    if (bridgeResponse?.error) {
      throw bridgeResponse.error instanceof Error ? bridgeResponse.error : new Error(String(bridgeResponse.error));
    }
    const page = normalizeSnapshot(bridgeResponse?.page).page;
    this.snapshot = {
      page: clone(page),
      revision: String(bridgeResponse?.revision ?? this.snapshot?.revision ?? ''),
      updatedAt: String(bridgeResponse?.updatedAt ?? this.snapshot?.updatedAt ?? ''),
    };
    return { annotation: clone(bridgeResponse?.annotation), page: clone(page) };
  }

  /** @param {IdentifiedIIIFAnnotation} annotation @returns {Promise<CanonicalIIIFAnnotationPage>} */
  async create(annotation) {
    return this.dispatchDraftMutation('create', { annotation, operation: 'create' }).page;
  }

  /** @param {IdentifiedIIIFAnnotation} annotation @returns {Promise<IdentifiedIIIFAnnotation>} */
  async createOne(annotation) {
    const result = this.dispatchDraftMutation('createOne', { annotation, operation: 'create' });
    return (result.annotation?.id
      ? /** @type {IdentifiedIIIFAnnotation} */ (result.annotation)
      : result.page.items.find((item) => item.id === annotation.id)) || clone(annotation);
  }

  /** @param {IdentifiedIIIFAnnotation} annotation @returns {Promise<CanonicalIIIFAnnotationPage>} */
  async update(annotation) {
    return this.dispatchDraftMutation('update', { annotation, operation: 'update' }).page;
  }

  /** @param {IdentifiedIIIFAnnotation} annotation @returns {Promise<IdentifiedIIIFAnnotation>} */
  async updateOne(annotation) {
    const result = this.dispatchDraftMutation('updateOne', { annotation, operation: 'update' });
    return (result.annotation?.id
      ? /** @type {IdentifiedIIIFAnnotation} */ (result.annotation)
      : result.page.items.find((item) => item.id === annotation.id)) || clone(annotation);
  }

  /** @param {string} annotationId @returns {Promise<CanonicalIIIFAnnotationPage>} */
  async delete(annotationId) {
    return this.dispatchDraftMutation('delete', { annotationId, operation: 'delete' }).page;
  }

  /** @param {string} annotationId @returns {Promise<void>} */
  async deleteOne(annotationId) {
    this.dispatchDraftMutation('deleteOne', { annotationId, operation: 'delete' });
  }

  /** @param {string} annotationId @returns {Promise<IdentifiedIIIFAnnotation>} */
  async get(annotationId) {
    const page = (this.snapshot || await this.loadSnapshot()).page;
    const match = page.items.find((annotation) => annotation?.id === annotationId || annotation?.['@id'] === annotationId);
    if (!match) throw new Error(`Annotation '${annotationId}' was not found`);
    return clone(match);
  }

  /** @param {CanonicalIIIFAnnotationPage} annotationPage @param {string} selectedAnnotationId @param {string[]} [words] */
  async splitLineIntoWords(annotationPage, selectedAnnotationId, words = []) {
    const methodName = 'splitLineIntoWords';
    const transformedPage = await this.requireClient(methodName).splitLineIntoWords(
      this.requireItemImageId(methodName),
      this.serializeDraftPage(methodName, annotationPage),
      this.requireAnnotationId(methodName, selectedAnnotationId),
      words,
    );
    return this.normalizeTransformedPage(methodName, transformedPage);
  }

  /** @param {CanonicalIIIFAnnotationPage} annotationPage @param {string} selectedAnnotationId @param {string[]} [words] */
  async splitAnnotationIntoWords(annotationPage, selectedAnnotationId, words = []) {
    return this.splitLineIntoWords(annotationPage, selectedAnnotationId, words);
  }

  /** @param {CanonicalIIIFAnnotationPage} annotationPage @param {string} selectedAnnotationId @param {number} [splitAtWord] */
  async splitLineIntoTwoLines(annotationPage, selectedAnnotationId, splitAtWord = 0) {
    const methodName = 'splitLineIntoTwoLines';
    const transformedPage = await this.requireClient(methodName).splitLineIntoTwoLines(
      this.requireItemImageId(methodName),
      this.serializeDraftPage(methodName, annotationPage),
      this.requireAnnotationId(methodName, selectedAnnotationId),
      splitAtWord,
    );
    return this.normalizeTransformedPage(methodName, transformedPage);
  }

  /** @param {CanonicalIIIFAnnotationPage} annotationPage @param {string} selectedAnnotationId @param {number} [splitAtWord] */
  async splitAnnotationIntoTwoLines(annotationPage, selectedAnnotationId, splitAtWord = 0) {
    return this.splitLineIntoTwoLines(annotationPage, selectedAnnotationId, splitAtWord);
  }

  /** @param {CanonicalIIIFAnnotationPage} annotationPage @param {string[]} selectedAnnotationIds */
  async joinLinesIntoLine(annotationPage, selectedAnnotationIds) {
    const methodName = 'joinLinesIntoLine';
    const transformedPage = await this.requireClient(methodName).joinLines(
      this.requireItemImageId(methodName),
      this.serializeDraftPage(methodName, annotationPage),
      this.requireAnnotationIds(methodName, selectedAnnotationIds),
    );
    return this.normalizeTransformedPage(methodName, transformedPage);
  }

  /** @param {CanonicalIIIFAnnotationPage} annotationPage @param {string[]} selectedAnnotationIds */
  async mergeAnnotationsIntoLine(annotationPage, selectedAnnotationIds) {
    return this.joinLinesIntoLine(annotationPage, selectedAnnotationIds);
  }

  /** @param {CanonicalIIIFAnnotationPage} annotationPage @param {string[]} selectedAnnotationIds */
  async joinWordsIntoLine(annotationPage, selectedAnnotationIds) {
    const methodName = 'joinWordsIntoLine';
    const transformedPage = await this.requireClient(methodName).joinWordsIntoLine(
      this.requireItemImageId(methodName),
      this.serializeDraftPage(methodName, annotationPage),
      this.requireAnnotationIds(methodName, selectedAnnotationIds),
    );
    return this.normalizeTransformedPage(methodName, transformedPage);
  }

  /** @param {CanonicalIIIFAnnotationPage} annotationPage @param {string[]} selectedAnnotationIds */
  async mergeWordsIntoLineAnnotation(annotationPage, selectedAnnotationIds) {
    return this.joinWordsIntoLine(annotationPage, selectedAnnotationIds);
  }

  /** @param {IIIFAnnotation} annotation @returns {Promise<IdentifiedIIIFAnnotation>} */
  async transcribeAnnotation(annotation) {
    const contextId = await this.requireContextId('transcribeAnnotation');
    const transformed = await this.requireClient('transcribeAnnotation').enrichAnnotation(
      this.requireItemImageId('transcribeAnnotation'),
      'line',
      stringifyIIIFJSON(annotation),
      contextId,
    );
    return normalizeAnnotation(transformed);
  }

  /** @param {CanonicalIIIFAnnotationPage} annotationPage @returns {Promise<CanonicalIIIFAnnotationPage>} */
  async transcribeAnnotationPage(annotationPage) {
    const contextId = await this.requireContextId('transcribeAnnotationPage');
    const transformed = await this.requireClient('transcribeAnnotationPage').enrichAnnotation(
      this.requireItemImageId('transcribeAnnotationPage'),
      'page',
      stringifyIIIFJSON(annotationPage),
      contextId,
    );
    return this.normalizeTransformedPage('transcribeAnnotationPage', transformed);
  }
}
