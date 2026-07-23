/**
 * Shared structural types for the plugin's raw-preserving IIIF boundary.
 *
 * IIIF resources intentionally keep an unknown-property index signature: the
 * editor owns only a small set of Presentation 3 and Text Granularity fields
 * and must round-trip every extension property it does not understand.
 */
export type RawIIIFProperties = Record<string, unknown>;

export interface IIIFTextualBody extends RawIIIFProperties {
  id?: string;
  type?: string;
  purpose?: string;
  value?: string;
  format?: string;
  language?: string;
}

export interface IIIFSelector extends RawIIIFProperties {
  id?: string;
  type?: string;
  value?: string;
  conformsTo?: string;
  refinedBy?: IIIFSelector | IIIFSelector[];
}

export interface IIIFTarget extends RawIIIFProperties {
  id?: string;
  type?: string;
  source?: string | (RawIIIFProperties & { id?: string });
  selector?: IIIFSelector | IIIFSelector[];
}

export interface IIIFAnnotation extends RawIIIFProperties {
  id?: string;
  '@id'?: string;
  type?: string;
  motivation?: string;
  body?: string | IIIFTextualBody | IIIFTextualBody[];
  target?: string | IIIFTarget;
  textGranularity?: 'line' | 'word' | string;
}

export interface IdentifiedIIIFAnnotation extends IIIFAnnotation {
  id: string;
}

export interface IIIFAnnotationPage extends RawIIIFProperties {
  id?: string;
  '@context'?: unknown;
  type?: string;
  items: IIIFAnnotation[];
}

export interface CanonicalIIIFAnnotationPage extends IIIFAnnotationPage {
  id: string;
  type: 'AnnotationPage';
  items: IdentifiedIIIFAnnotation[];
}

export interface AnnotationPageSnapshot {
  page: CanonicalIIIFAnnotationPage;
  revision: string;
  updatedAt?: string;
}

export interface ImageBBox {
  x: number;
  y: number;
  w: number;
  h: number;
}

export interface Point2D {
  x: number;
  y: number;
}

export interface AnnotationResource extends RawIIIFProperties {
  json?: IIIFAnnotationPage;
}

export interface MiradorWindowState extends RawIIIFProperties {
  canvasId?: string;
  canvasIds?: string[];
  visibleCanvases?: string[];
  selectedCanvasId?: string;
  selectedAnnotation?: string | IIIFAnnotation;
  selectedAnnotationId?: string;
}

export interface MiradorCompanionWindowState extends RawIIIFProperties {
  content?: string;
  windowId?: string;
}

export interface MiradorState extends RawIIIFProperties {
  annotations?: Record<string, Record<string, AnnotationResource>>;
  companionWindows?: Record<string, MiradorCompanionWindowState>;
  config?: RawIIIFProperties & {
    annotation?: RawIIIFProperties & { adapter?: ScribeAdapterFactory };
  };
  workspace?: RawIIIFProperties & { focusedWindowId?: string };
  windows?: Record<string, MiradorWindowState>;
}

export interface ScribeAnnotationClient {
  getAnnotationPage(itemImageId: string): Promise<AnnotationPageSnapshot | CanonicalIIIFAnnotationPage>;
  saveAnnotationPage(
    itemImageId: string,
    annotationPageJson: string,
    expectedRevision: string,
  ): Promise<AnnotationPageSnapshot | CanonicalIIIFAnnotationPage>;
  enrichAnnotation(
    itemImageId: string,
    scope: 'line' | 'page',
    annotationJson: string,
    contextId: string,
  ): Promise<unknown>;
  splitLineIntoWords(
    itemImageId: string,
    annotationPageJson: string,
    selectedAnnotationId: string,
    words?: string[],
  ): Promise<CanonicalIIIFAnnotationPage>;
  splitLineIntoTwoLines(
    itemImageId: string,
    annotationPageJson: string,
    selectedAnnotationId: string,
    splitAtWord?: number,
  ): Promise<CanonicalIIIFAnnotationPage>;
  joinLines(
    itemImageId: string,
    annotationPageJson: string,
    selectedAnnotationIds: string[],
  ): Promise<CanonicalIIIFAnnotationPage>;
  joinWordsIntoLine(
    itemImageId: string,
    annotationPageJson: string,
    selectedAnnotationIds: string[],
  ): Promise<CanonicalIIIFAnnotationPage>;
}

export interface ScribeAdapterRuntime {
  client?: ScribeAnnotationClient;
  contextId?: string | number | bigint;
  itemImageId?: string | number | bigint;
  windowId?: string;
  resolveContextId?: () => Promise<string | number | bigint> | string | number | bigint;
}

export interface AnnotationMutation {
  annotation?: IIIFAnnotation;
  annotationId?: string;
  operation: 'create' | 'update' | 'delete';
}

export interface DraftMutationResponse {
  annotation?: IIIFAnnotation;
  error?: unknown;
  page?: IIIFAnnotationPage;
  revision?: string | number | bigint;
  updatedAt?: string;
}

export interface ScribeAdapterLike {
  all(): Promise<IIIFAnnotationPage>;
  canvasId?: string;
  itemImageId?: string;
  loadSnapshot(): Promise<AnnotationPageSnapshot>;
  savePage(page: IIIFAnnotationPage, expectedRevision?: string): Promise<AnnotationPageSnapshot>;
  splitLineIntoWords(
    page: IIIFAnnotationPage,
    annotationId: string,
    words?: string[],
  ): Promise<IIIFAnnotationPage>;
  splitLineIntoTwoLines(
    page: IIIFAnnotationPage,
    annotationId: string,
    splitAtWord?: number,
  ): Promise<IIIFAnnotationPage>;
  joinLinesIntoLine(page: IIIFAnnotationPage, annotationIds: string[]): Promise<IIIFAnnotationPage>;
  joinWordsIntoLine(page: IIIFAnnotationPage, annotationIds: string[]): Promise<IIIFAnnotationPage>;
  transcribeAnnotation(annotation: IIIFAnnotation): Promise<IdentifiedIIIFAnnotation>;
  transcribeAnnotationPage(page: IIIFAnnotationPage): Promise<unknown>;
}

export type ScribeAdapterFactory = (canvasId: string) => ScribeAdapterLike | null | undefined;

export interface ScribeAnnotationAdapterInstance {
  readonly canvasId: string;
  contextId: string;
  readonly itemImageId: string;
  readonly annotationPageId: string;
  all(): Promise<CanonicalIIIFAnnotationPage>;
  loadSnapshot(): Promise<AnnotationPageSnapshot>;
  savePage(
    page: CanonicalIIIFAnnotationPage,
    expectedRevision?: string,
  ): Promise<AnnotationPageSnapshot>;
  get(annotationId: string): Promise<IdentifiedIIIFAnnotation>;
  create(annotation: IdentifiedIIIFAnnotation): Promise<CanonicalIIIFAnnotationPage>;
  createOne(annotation: IdentifiedIIIFAnnotation): Promise<IdentifiedIIIFAnnotation>;
  update(annotation: IdentifiedIIIFAnnotation): Promise<CanonicalIIIFAnnotationPage>;
  updateOne(annotation: IdentifiedIIIFAnnotation): Promise<IdentifiedIIIFAnnotation>;
  delete(annotationId: string): Promise<CanonicalIIIFAnnotationPage>;
  deleteOne(annotationId: string): Promise<void>;
  splitLineIntoWords(
    annotationPage: CanonicalIIIFAnnotationPage,
    selectedAnnotationId: string,
    words?: string[],
  ): Promise<CanonicalIIIFAnnotationPage>;
  splitAnnotationIntoWords(
    annotationPage: CanonicalIIIFAnnotationPage,
    selectedAnnotationId: string,
    words?: string[],
  ): Promise<CanonicalIIIFAnnotationPage>;
  splitLineIntoTwoLines(
    annotationPage: CanonicalIIIFAnnotationPage,
    selectedAnnotationId: string,
    splitAtWord?: number,
  ): Promise<CanonicalIIIFAnnotationPage>;
  splitAnnotationIntoTwoLines(
    annotationPage: CanonicalIIIFAnnotationPage,
    selectedAnnotationId: string,
    splitAtWord?: number,
  ): Promise<CanonicalIIIFAnnotationPage>;
  joinLinesIntoLine(
    annotationPage: CanonicalIIIFAnnotationPage,
    selectedAnnotationIds: string[],
  ): Promise<CanonicalIIIFAnnotationPage>;
  mergeAnnotationsIntoLine(
    annotationPage: CanonicalIIIFAnnotationPage,
    selectedAnnotationIds: string[],
  ): Promise<CanonicalIIIFAnnotationPage>;
  joinWordsIntoLine(
    annotationPage: CanonicalIIIFAnnotationPage,
    selectedAnnotationIds: string[],
  ): Promise<CanonicalIIIFAnnotationPage>;
  mergeWordsIntoLineAnnotation(
    annotationPage: CanonicalIIIFAnnotationPage,
    selectedAnnotationIds: string[],
  ): Promise<CanonicalIIIFAnnotationPage>;
  transcribeAnnotation(annotation: IIIFAnnotation): Promise<IdentifiedIIIFAnnotation>;
  transcribeAnnotationPage(
    annotationPage: CanonicalIIIFAnnotationPage,
  ): Promise<CanonicalIIIFAnnotationPage>;
}

export interface ScribeAnnotationAdapterConstructor {
  new (
    endpointUrl: string,
    iiifPresentationVersion: 3,
    canvasId: string,
    user: string,
    runtime?: ScribeAdapterRuntime,
  ): ScribeAnnotationAdapterInstance;
  readonly prototype: ScribeAnnotationAdapterInstance;
}

export interface AnnotationChange {
  id: string;
  before: IIIFAnnotation | null;
  after: IIIFAnnotation | null;
}

export interface PagePatch {
  changes: AnnotationChange[];
  metadataBefore: RawIIIFProperties | null;
  metadataAfter: RawIIIFProperties | null;
  orderBefore: string[] | null;
  orderAfter: string[] | null;
}

export type EditorSessionStatus = 'ready' | 'loading' | 'saving' | 'conflict' | 'error';

export interface EditorSession {
  basePage: IIIFAnnotationPage | null;
  draftPage: IIIFAnnotationPage | null;
  revision: string;
  undoStack: PagePatch[];
  redoStack: PagePatch[];
  pendingRemoteIds: string[];
  dirty: boolean;
  status: EditorSessionStatus;
  conflictKind: 'save' | 'transform' | null;
  error: string | null;
}

export type EditorSessionAction = {
  /** Required by the cache reducer and omitted by the single-session reducer. */
  canvasId?: string;
} & (
  | { type: 'edit'; page: IIIFAnnotationPage }
  | { type: 'undo' }
  | { type: 'redo' }
  | { type: 'load-start' }
  | {
      type: 'loaded' | 'rebase';
      page: IIIFAnnotationPage;
      revision: string | number | bigint;
    }
  | { type: 'remote-annotation'; annotation: IIIFAnnotation }
  | { type: 'save-start' }
  | { type: 'save-conflict'; error: string }
  | { type: 'save-error' | 'load-error'; error: string }
  | { type: 'dismiss-error' }
  | {
      type: 'saved';
      page: IIIFAnnotationPage;
      revision: string | number | bigint;
      submittedPage: IIIFAnnotationPage;
      submittedRevision: string | number | bigint;
    }
  | {
      type: 'transform-result';
      page: IIIFAnnotationPage;
      submittedPage: IIIFAnnotationPage;
      affectedIds: string[];
      atomic: boolean;
    }
  | {
      type: 'reset';
      page: IIIFAnnotationPage | null;
      revision: string | number | bigint;
    }
);

export interface EditorSessionCache {
  sessions: Map<string, EditorSession>;
  accessOrder: string[];
}

export interface EditorRow {
  id?: string;
  granularity: 'line' | 'word';
  lead: IIIFAnnotation;
  fields: IIIFAnnotation[];
}

export interface ScribeActiveCanvasEventDetail {
  canvasId: string;
  itemImageId: string;
  windowId: string;
}

export interface ScribeDirtyStateEventDetail {
  activeCanvasId: string;
  dirty: boolean;
  dirtyCanvasIds: string[];
  windowId: string;
}

export interface ScribeCreateLineAtViewportCenterEventDetail {
  canvasId: string;
  windowId: string;
}

export interface ScribeCreateAnnotationEventDetail {
  bbox: ImageBBox;
  canvasId: string;
  focusResizeHandle?: 'nw' | 'ne' | 'sw' | 'se';
  windowId: string;
}

export interface ScribeFocusResizeHandleEventDetail {
  annotationId: string;
  canvasId: string;
  handle: 'nw' | 'ne' | 'sw' | 'se';
  windowId: string;
}

export interface ScribeAnnotationMutationEventDetail {
  annotation?: IIIFAnnotation;
  annotationId?: string;
  canvasId: string;
  itemImageId: string;
  operation: AnnotationMutation['operation'];
  windowId: string;
  respond(result: DraftMutationResponse): void;
}

export interface ScribeSaveResultEventDetail {
  dirtyCanvasIds: string[];
  ok: boolean;
  requestId: string;
  windowId: string;
}

export interface ScribeSaveRequestEventDetail {
  canvasId: string;
  requestId: string;
  windowId: string;
}

export interface ScribePublishRequestEventDetail {
  canvasId: string;
  expectedRevision: string;
  itemImageId: string;
  requestId: string;
  windowId: string;
}

export interface ScribePublishResultEventDetail {
  canvasId: string;
  ok: boolean;
  publicUrl?: string;
  publishedRevision?: string | bigint;
  requestId: string;
  windowId: string;
}

export interface ScribeReloadAnnotationsEventDetail {
  canvasId: string;
  itemImageId: string;
  windowId: string;
}

export interface ScribeTranscriptionEventDetail {
  annotation: IIIFAnnotation | null;
  canvasId: string;
  done?: number;
  itemImageId?: string;
  persisted?: boolean;
  total?: number;
  windowId: string;
}

export interface ScribeTranscriptionJobStateEventDetail {
  active: boolean;
  canvasId: string;
  itemImageId: string;
  message: string;
  windowId: string;
}
