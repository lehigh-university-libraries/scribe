import {
  annotationText,
  findEditorRowByAnnotationId,
  isWordAnnotation,
  removeAnnotationsFromPage,
  synchronizeLineTextFromWords,
  updateAnnotationText,
  upsertAnnotationInPage,
} from '../utils/iiif';
import type {
  AnnotationMutation,
  IIIFAnnotation,
  IIIFAnnotationPage,
} from '../types/scribe';

export interface AdapterMutationResult {
  annotation: IIIFAnnotation;
  page: IIIFAnnotationPage;
}

/**
 * Applies a Mirador adapter mutation to the in-memory canonical page. This is
 * deliberately pure: persistence remains an explicit page-level Save.
 */
export function applyAdapterMutationToPage(
  page: IIIFAnnotationPage | null | undefined,
  mutation: AnnotationMutation,
): AdapterMutationResult {
  if (!page || !Array.isArray(page.items)) throw new Error('The editor has no AnnotationPage draft');
  const operation = mutation.operation;
  if (operation === 'create' || operation === 'update') {
    const annotation = mutation.annotation;
    if (!annotation?.id) throw new Error(`${operation} requires an Annotation ID`);
    const exists = page.items.some((item) => item.id === annotation.id);
    if (operation === 'create' && exists) throw new Error(`Annotation '${annotation.id}' already exists`);
    if (operation === 'update' && !exists) throw new Error(`Annotation '${annotation.id}' was not found`);
    let nextPage = upsertAnnotationInPage(page, annotation);
    if (isWordAnnotation(annotation)) {
      nextPage = synchronizeLineTextFromWords(nextPage, annotation);
    }
    return { annotation, page: nextPage };
  }

  if (operation === 'delete') {
    const annotationId = String(mutation.annotationId || '').trim();
    const deleted = page.items.find((annotation) => annotation.id === annotationId);
    if (!deleted) throw new Error(`Annotation '${annotationId}' was not found`);
    const row = findEditorRowByAnnotationId(page, annotationId);
    let nextPage = removeAnnotationsFromPage(page, [annotationId]);
    if (isWordAnnotation(deleted) && row?.lead && !isWordAnnotation(row.lead)) {
      const remainingText = (row.fields || [])
        .filter((word) => word.id !== annotationId)
        .map((word) => nextPage.items.find((item) => item.id === word.id))
        .filter((word): word is IIIFAnnotation => Boolean(word))
        .map(annotationText)
        .filter(Boolean)
        .join(' ');
      nextPage = upsertAnnotationInPage(nextPage, updateAnnotationText(row.lead, remainingText));
    }
    return { annotation: deleted, page: nextPage };
  }

  throw new Error(`Unsupported annotation mutation '${operation || ''}'`);
}
