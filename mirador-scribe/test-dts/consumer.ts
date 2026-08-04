import scribeMiradorPlugin, {
  ScribeAnnotationAdapter,
  annotationAdapters,
  type CanonicalIIIFAnnotationPage,
  type IdentifiedIIIFAnnotation,
  type EditorSessionAction,
  type ScribeAdapterRuntime,
  type ScribeAnnotationAdapterConstructor,
  type ScribeAnnotationClient,
  type ScribeRemoteRebaseReadyEventDetail,
} from 'mirador-scribe';

const annotation: IdentifiedIIIFAnnotation = {
  id: 'https://consumer.example/annotations/word-1',
  type: 'Annotation',
  textGranularity: 'word',
  body: [{ type: 'TextualBody', value: 'Scribe' }],
  target: 'https://consumer.example/canvas/1#xywh=1,2,3,4',
};

const page: CanonicalIIIFAnnotationPage = {
  '@context': [
    'http://iiif.io/api/extension/text-granularity/context.json',
    'http://iiif.io/api/presentation/3/context.json',
  ],
  id: 'https://consumer.example/presentation/v3/item-image-1/canvas/page-1/annotations',
  items: [annotation],
  type: 'AnnotationPage',
};

const client: ScribeAnnotationClient = {
  async enrichAnnotation(_itemImageId, scope) {
    return scope === 'line' ? annotation : page;
  },
  async getAnnotationPage() {
    return { page, revision: '1' };
  },
  async joinLines() {
    return page;
  },
  async joinWordsIntoLine() {
    return page;
  },
  async saveAnnotationPage() {
    return { page, revision: '2' };
  },
  async splitLineIntoTwoLines() {
    return page;
  },
  async splitLineIntoWords() {
    return page;
  },
};

const runtime: ScribeAdapterRuntime = {
  client,
  contextId: '1',
  itemImageId: '1',
  windowId: 'window-1',
};
const remoteRebaseReady: ScribeRemoteRebaseReadyEventDetail = {
  canvasId: 'https://consumer.example/canvas/1',
  itemImageId: '1',
  windowId: 'window-1',
};
document.addEventListener('scribe:remote-rebase-ready', (event) => {
  const detail: ScribeRemoteRebaseReadyEventDetail = event.detail;
  void detail;
});

const adapter = new ScribeAnnotationAdapter(
  '/api',
  3,
  'https://consumer.example/canvas/1',
  'Consumer',
  runtime,
);

const adapterConstructor: ScribeAnnotationAdapterConstructor = annotationAdapters.ScribeAnnotationAdapter;
const editAction: EditorSessionAction = { page, type: 'edit' };
// @ts-expect-error A saved action must identify both the submitted and accepted revisions.
const malformedSavedAction: EditorSessionAction = { page, revision: '2', type: 'saved' };
const pagePromise: Promise<CanonicalIIIFAnnotationPage> = adapter.all();
const annotationPromise: Promise<IdentifiedIIIFAnnotation> = adapter.transcribeAnnotation(annotation);
const loadedAnnotation: Promise<IdentifiedIIIFAnnotation> = adapter.get(annotation.id);
const createdPage: Promise<CanonicalIIIFAnnotationPage> = adapter.create(annotation);
const createdAnnotation: Promise<IdentifiedIIIFAnnotation> = adapter.createOne(annotation);
const updatedPage: Promise<CanonicalIIIFAnnotationPage> = adapter.update(annotation);
const updatedAnnotation: Promise<IdentifiedIIIFAnnotation> = adapter.updateOne(annotation);
const deletedPage: Promise<CanonicalIIIFAnnotationPage> = adapter.delete(annotation.id);
const savedPage = adapter.savePage(page, '1');
const splitWords = adapter.splitLineIntoWords(page, annotation.id, ['Scribe']);
const splitLines = adapter.splitLineIntoTwoLines(page, annotation.id, 1);
const joinedWords = adapter.joinWordsIntoLine(page, [annotation.id, `${annotation.id}-2`]);
const joinedLines = adapter.joinLinesIntoLine(page, [annotation.id, `${annotation.id}-2`]);

void adapterConstructor;
void editAction;
void malformedSavedAction;
void annotationPromise;
void createdAnnotation;
void createdPage;
void deletedPage;
void joinedLines;
void joinedWords;
void loadedAnnotation;
void pagePromise;
void remoteRebaseReady;
void savedPage;
void scribeMiradorPlugin;
void splitLines;
void splitWords;
void updatedAnnotation;
void updatedPage;
