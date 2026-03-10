export default class ScribeAnnotationAdapter {
  constructor(endpointUrl, iiifPresentationVersion, canvasId, user, client = null) {
    if (iiifPresentationVersion !== 3) {
      throw new Error(`ScribeAnnotationAdapter expects IIIF Presentation 3, got '${iiifPresentationVersion}'`);
    }
    this.user = user || 'Scribe User';
    this.canvasId = canvasId;
    this.endpointUrl = endpointUrl;
    this.client = client;
  }

  getStorageAdapterUser() {
    return this.user;
  }

  get annotationPageId() {
    return `urn:scribe:annotation-page:${encodeURIComponent(this.canvasId)}`;
  }

  requireClient(methodName) {
    if (!this.client) {
      throw new Error(`${methodName} requires an injected Scribe Connect client`);
    }
    return this.client;
  }

  async create(annotation) {
    await this.createOne(annotation);
    return this.all();
  }

  async createOne(annotation) {
    return this.requireClient('createOne').createAnnotation(JSON.stringify(annotation));
  }

  async update(annotation) {
    await this.updateOne(annotation);
    return this.all();
  }

  async updateOne(annotation) {
    return this.requireClient('updateOne').updateAnnotation(JSON.stringify(annotation));
  }

  async delete(annotationId) {
    await this.deleteOne(annotationId);
    return this.all();
  }

  async deleteOne(annotationId) {
    return this.requireClient('deleteOne').deleteAnnotation(annotationId);
  }

  async get(annotationId) {
    return this.requireClient('get').getAnnotation(annotationId);
  }

  async all() {
    const page = await this.requireClient('all').searchAnnotations(this.canvasId);
    return {
      '@context': page?.['@context'] || 'http://iiif.io/api/presentation/3/context.json',
      id: page?.id || this.annotationPageId,
      items: Array.isArray(page?.items) ? page.items : [],
      type: 'AnnotationPage',
    };
  }

  async splitLineIntoWords(annotation, words = []) {
    return this.requireClient('splitLineIntoWords').splitLineIntoWords(JSON.stringify(annotation), words);
  }

  async splitAnnotationIntoWords(annotation, words = []) {
    return this.splitLineIntoWords(annotation, words);
  }

  async splitLineIntoTwoLines(annotation, splitAtWord = 0) {
    return this.requireClient('splitLineIntoTwoLines').splitLineIntoTwoLines(JSON.stringify(annotation), splitAtWord);
  }

  async splitAnnotationIntoTwoLines(annotation, splitAtWord = 0) {
    return this.splitLineIntoTwoLines(annotation, splitAtWord);
  }

  async joinLinesIntoLine(annotations) {
    return this.requireClient('joinLinesIntoLine').joinLines(annotations.map((a) => JSON.stringify(a)));
  }

  async mergeAnnotationsIntoLine(annotations) {
    return this.joinLinesIntoLine(annotations);
  }

  async joinWordsIntoLine(annotations) {
    return this.requireClient('joinWordsIntoLine').joinWordsIntoLine(annotations.map((a) => JSON.stringify(a)));
  }

  async mergeWordsIntoLineAnnotation(annotations) {
    return this.joinWordsIntoLine(annotations);
  }

  async transcribeAnnotation(annotation, contextId = 0) {
    return this.requireClient('transcribeAnnotation').enrichAnnotation('line', JSON.stringify(annotation), contextId);
  }

  async transcribeAnnotationPage(annotationPage, contextId = 0) {
    return this.requireClient('transcribeAnnotationPage').enrichAnnotation('page', JSON.stringify(annotationPage), contextId);
  }
}
