async function successOrThrow(response) {
  if (!response.ok) {
    throw new Error(`Fetch error: response ${response.status} on url: ${response.url}`);
  }
}

export default class ScribeAnnotationAdapter {
  constructor(endpointUrl, iiifPresentationVersion, canvasId, user) {
    if (iiifPresentationVersion !== 3) {
      throw new Error(`ScribeAnnotationAdapter expects IIIF Presentation 3, got '${iiifPresentationVersion}'`);
    }
    this.user = user || 'Scribe User';
    this.canvasId = canvasId;
    this.endpointUrl = endpointUrl;
    this.endpointUrlAnnotations = `${endpointUrl}/annotations/${iiifPresentationVersion}`;
  }

  getStorageAdapterUser() {
    return this.user;
  }

  get annotationPageId() {
    return `${this.endpointUrlAnnotations}/search?canvasUri=${encodeURIComponent(this.canvasId)}`;
  }

  async create(annotation) {
    await this.createOne(annotation);
    return this.all();
  }

  async createOne(annotation) {
    const response = await fetch(`${this.endpointUrlAnnotations}/create`, {
      method: 'POST',
      body: JSON.stringify(annotation),
      headers: {
        Accept: 'application/json',
        'Content-Type': 'application/json',
      },
    });
    await successOrThrow(response);
    return response.json();
  }

  async update(annotation) {
    await this.updateOne(annotation);
    return this.all();
  }

  async updateOne(annotation) {
    const response = await fetch(`${this.endpointUrlAnnotations}/update`, {
      method: 'POST',
      body: JSON.stringify(annotation),
      headers: {
        Accept: 'application/json',
        'Content-Type': 'application/json',
      },
    });
    await successOrThrow(response);
    return response.json();
  }

  async delete(annotationId) {
    await this.deleteOne(annotationId);
    return this.all();
  }

  async deleteOne(annotationId) {
    const response = await fetch(`${this.endpointUrlAnnotations}/delete?uri=${encodeURIComponent(annotationId)}`, {
      method: 'DELETE',
    });
    await successOrThrow(response);
    return response.text();
  }

  async get(annotationId) {
    const response = await fetch(annotationId);
    await successOrThrow(response);
    return response.json();
  }

  async splitAnnotationIntoWords(annotation, words = []) {
    const response = await fetch(`${this.endpointUrl}/scribe.v1.AnnotationService/SplitAnnotationIntoWords`, {
      method: 'POST',
      body: JSON.stringify({
        annotation_json: JSON.stringify(annotation),
        words,
      }),
      headers: {
        Accept: 'application/json',
        'Content-Type': 'application/json',
      },
    });
    await successOrThrow(response);
    return response.json();
  }

  async splitLineIntoWords(annotation, words = []) {
    return this.splitAnnotationIntoWords(annotation, words);
  }

  async splitAnnotationIntoTwoLines(annotation, splitAtWord = 0) {
    const response = await fetch(`${this.endpointUrl}/scribe.v1.AnnotationService/SplitAnnotationIntoTwoLines`, {
      method: 'POST',
      body: JSON.stringify({
        annotation_json: JSON.stringify(annotation),
        split_at_word: splitAtWord,
      }),
      headers: {
        Accept: 'application/json',
        'Content-Type': 'application/json',
      },
    });
    await successOrThrow(response);
    return response.json();
  }

  async splitLineIntoTwoLines(annotation, splitAtWord = 0) {
    return this.splitAnnotationIntoTwoLines(annotation, splitAtWord);
  }

  async mergeAnnotationsIntoLine(annotations) {
    const response = await fetch(`${this.endpointUrl}/scribe.v1.AnnotationService/MergeAnnotationsIntoLine`, {
      method: 'POST',
      body: JSON.stringify({
        annotation_jsons: annotations.map((annotation) => JSON.stringify(annotation)),
      }),
      headers: {
        Accept: 'application/json',
        'Content-Type': 'application/json',
      },
    });
    await successOrThrow(response);
    return response.json();
  }

  async joinLinesIntoLine(annotations) {
    return this.mergeAnnotationsIntoLine(annotations);
  }

  async mergeWordsIntoLineAnnotation(annotations) {
    const response = await fetch(`${this.endpointUrl}/scribe.v1.AnnotationService/MergeWordsIntoLineAnnotation`, {
      method: 'POST',
      body: JSON.stringify({
        annotation_jsons: annotations.map((annotation) => JSON.stringify(annotation)),
      }),
      headers: {
        Accept: 'application/json',
        'Content-Type': 'application/json',
      },
    });
    await successOrThrow(response);
    return response.json();
  }

  async joinWordsIntoLine(annotations) {
    return this.mergeWordsIntoLineAnnotation(annotations);
  }

  async transcribeAnnotation(annotation, contextId = 0) {
    const response = await fetch(`${this.endpointUrl}/scribe.v1.AnnotationService/TranscribeAnnotation`, {
      method: 'POST',
      body: JSON.stringify({
        annotation_json: JSON.stringify(annotation),
        context_id: contextId,
      }),
      headers: {
        Accept: 'application/json',
        'Content-Type': 'application/json',
      },
    });
    await successOrThrow(response);
    return response.json();
  }

  async transcribeAnnotationPage(annotationPage, contextId = 0) {
    const response = await fetch(`${this.endpointUrl}/scribe.v1.AnnotationService/TranscribeAnnotationPage`, {
      method: 'POST',
      body: JSON.stringify({
        annotation_page_json: JSON.stringify(annotationPage),
        context_id: contextId,
      }),
      headers: {
        Accept: 'application/json',
        'Content-Type': 'application/json',
      },
    });
    await successOrThrow(response);
    return response.json();
  }

  async all() {
    const items = [];
    let nextPage = this.annotationPageId;
    let page;

    while (nextPage) {
      const response = await fetch(nextPage);
      await successOrThrow(response);
      page = await response.json();
      items.push(...(Array.isArray(page?.items) ? page.items : []));
      nextPage = page?.next || '';
    }

    return {
      '@context': page?.['@context'] || 'http://iiif.io/api/presentation/3/context.json',
      id: page?.id || this.annotationPageId,
      items,
      type: 'AnnotationPage',
    };
  }
}
