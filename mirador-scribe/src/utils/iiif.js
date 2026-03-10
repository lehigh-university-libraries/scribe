export function annotationTextBody(annotation) {
  const body = annotation?.body;
  if (Array.isArray(body)) {
    return body.find((item) => item?.type === 'TextualBody' && (item?.purpose === 'describing' || item?.purpose === 'supplementing'))
      || body.find((item) => item?.type === 'TextualBody')
      || null;
  }
  if (body && typeof body === 'object' && body.type === 'TextualBody') {
    return body;
  }
  if (typeof body === 'string') {
    return { type: 'TextualBody', purpose: 'supplementing', value: body };
  }
  return null;
}

export function annotationText(annotation) {
  return annotationTextBody(annotation)?.value || '';
}

export function annotationCanvasId(annotation) {
  const target = annotation?.target;
  if (!target) return '';
  if (typeof target === 'string') {
    const hashIndex = target.indexOf('#');
    return hashIndex >= 0 ? target.slice(0, hashIndex) : target;
  }
  return target?.source?.id || target?.source || '';
}

export function annotationPageForCanvas(state, canvasId) {
  return state?.annotations?.[canvasId]?.json || null;
}

export function selectedAnnotationIdForWindow(state, windowId) {
  const windowState = state?.windows?.[windowId];
  if (typeof windowState?.selectedAnnotationId === 'string') {
    return windowState.selectedAnnotationId;
  }
  if (typeof windowState?.selectedAnnotation === 'string') {
    return windowState.selectedAnnotation;
  }
  if (typeof windowState?.selectedAnnotation?.id === 'string') {
    return windowState.selectedAnnotation.id;
  }
  return '';
}

export function hasCompanionWindowContent(state, windowId, content) {
  return Object.values(state?.companionWindows || {}).some((entry) => {
    if (!entry || entry.content !== content) return false;
    return !entry.windowId || entry.windowId === windowId;
  });
}

export function annotationEntries(state) {
  return Object.entries(state?.annotations || {}).filter(([, value]) => value?.json);
}

export function findAnnotationPageByAnnotationId(state, annotationId) {
  if (!annotationId) return null;
  for (const [, value] of annotationEntries(state)) {
    const page = value?.json;
    const items = Array.isArray(page?.items) ? page.items : [];
    if (items.some((item) => item?.id === annotationId)) {
      return page;
    }
  }
  return null;
}

export function findCanvasIdByAnnotationId(state, annotationId) {
  if (!annotationId) return '';
  for (const [canvasId, value] of annotationEntries(state)) {
    const page = value?.json;
    const items = Array.isArray(page?.items) ? page.items : [];
    if (items.some((item) => item?.id === annotationId)) {
      return canvasId;
    }
  }
  return '';
}

export function firstAnnotationCanvasId(state) {
  return annotationEntries(state)[0]?.[0] || '';
}

export function firstAnnotationPage(state) {
  return annotationEntries(state)[0]?.[1]?.json || null;
}

export function canvasIdForWindow(state, windowId) {
  const windowState = state?.windows?.[windowId];
  if (!windowState) return '';

  if (typeof windowState.canvasId === 'string' && windowState.canvasId) {
    return windowState.canvasId;
  }

  if (Array.isArray(windowState.canvasIds) && typeof windowState.canvasIds[0] === 'string') {
    return windowState.canvasIds[0];
  }

  if (Array.isArray(windowState.visibleCanvases) && typeof windowState.visibleCanvases[0] === 'string') {
    return windowState.visibleCanvases[0];
  }

  if (typeof windowState.selectedCanvasId === 'string' && windowState.selectedCanvasId) {
    return windowState.selectedCanvasId;
  }

  return '';
}

export function annotationBBox(annotation) {
  const selector = annotation?.target?.selector;
  const fragment = Array.isArray(selector)
    ? selector.find((item) => item?.type === 'FragmentSelector')
    : selector;
  const rawValue = typeof fragment?.value === 'string' ? fragment.value : '';
  const value = rawValue.replace(/^xywh=/, '');
  const [x, y, w, h] = value.split(',').map((part) => Number.parseInt(part, 10));
  return {
    h: Number.isFinite(h) ? h : 0,
    w: Number.isFinite(w) ? w : 0,
    x: Number.isFinite(x) ? x : 0,
    y: Number.isFinite(y) ? y : 0,
  };
}

export function annotationGranularity(annotation) {
  return annotation?.textGranularity || 'line';
}

export function isWordAnnotation(annotation) {
  return annotationGranularity(annotation) === 'word';
}

export function isLineAnnotation(annotation) {
  return annotationGranularity(annotation) === 'line';
}

export function annotationsShareLine(left, right) {
  if (!left || !right) return false;
  const a = annotationBBox(left);
  const b = annotationBBox(right);
  const overlapTop = Math.max(a.y, b.y);
  const overlapBottom = Math.min(a.y + a.h, b.y + b.h);
  const overlap = Math.max(0, overlapBottom - overlapTop);
  const minHeight = Math.max(1, Math.min(a.h, b.h));
  const aCenter = a.y + a.h / 2;
  const bCenter = b.y + b.h / 2;
  return overlap / minHeight >= 0.45 || Math.abs(aCenter - bCenter) <= Math.max(a.h, b.h) * 0.45;
}

export function annotationIntersectsImageRect(annotation, rect) {
  if (!rect) return true;
  const bbox = annotationBBox(annotation);
  const left = bbox.x;
  const right = bbox.x + bbox.w;
  const top = bbox.y;
  const bottom = bbox.y + bbox.h;
  const rectLeft = rect.x;
  const rectRight = rect.x + rect.w;
  const rectTop = rect.y;
  const rectBottom = rect.y + rect.h;

  return left <= rectRight
    && right >= rectLeft
    && top <= rectBottom
    && bottom >= rectTop;
}

export function sortedAnnotations(page) {
  const items = Array.isArray(page?.items) ? [...page.items] : [];
  return items.sort((left, right) => {
    const a = annotationBBox(left);
    const b = annotationBBox(right);
    if (a.y !== b.y) return a.y - b.y;
    if (a.x !== b.x) return a.x - b.x;
    return String(left?.id || '').localeCompare(String(right?.id || ''));
  });
}

export function sortAnnotationsWithinLine(annotations) {
  return [...(annotations || [])].sort((left, right) => {
    const a = annotationBBox(left);
    const b = annotationBBox(right);
    if (a.x !== b.x) return a.x - b.x;
    if (a.y !== b.y) return a.y - b.y;
    return String(left?.id || '').localeCompare(String(right?.id || ''));
  });
}

export function groupAnnotationsForEditor(page) {
  const annotations = sortedAnnotations(page);
  const lines = annotations.filter((annotation) => isLineAnnotation(annotation));
  const words = annotations.filter((annotation) => isWordAnnotation(annotation));

  if (words.length === 0) {
    return lines.map((annotation) => ({
      id: annotation.id,
      granularity: 'line',
      lead: annotation,
      fields: [annotation],
    }));
  }

  const assignedWordIds = new Set();
  const rows = lines.map((line) => {
    const rowWords = sortAnnotationsWithinLine(words.filter((word) => {
      if (assignedWordIds.has(word.id)) return false;
      return annotationsShareLine(line, word);
    }));
    rowWords.forEach((word) => assignedWordIds.add(word.id));
    return {
      id: line.id,
      granularity: rowWords.length > 0 ? 'word' : 'line',
      lead: line,
      fields: rowWords.length > 0 ? rowWords : [line],
    };
  });

  const looseWords = words
    .filter((word) => !assignedWordIds.has(word.id))
    .map((word) => ({
      id: word.id,
      granularity: 'word',
      lead: word,
      fields: [word],
    }));

  return [...rows, ...looseWords].sort((left, right) => {
    const a = annotationBBox(left.lead);
    const b = annotationBBox(right.lead);
    if (a.y !== b.y) return a.y - b.y;
    if (a.x !== b.x) return a.x - b.x;
    return String(left.id).localeCompare(String(right.id));
  });
}

export function joinWordCandidates(selectedAnnotation, annotations) {
  if (!isWordAnnotation(selectedAnnotation)) return [];
  const candidates = sortedAnnotations({
    items: (annotations || []).filter((annotation) => isWordAnnotation(annotation) && annotationsShareLine(annotation, selectedAnnotation)),
  });
  return candidates.length > 1 ? candidates : [];
}

export function synchronizeLineTextFromWords(page, changedWordAnnotation) {
  if (!page || !changedWordAnnotation || !isWordAnnotation(changedWordAnnotation)) return page;

  const items = Array.isArray(page?.items) ? page.items : [];
  const matchingLine = items.find((annotation) => (
    isLineAnnotation(annotation) && annotationsShareLine(annotation, changedWordAnnotation)
  ));
  if (!matchingLine) return page;

  const rowWords = sortAnnotationsWithinLine(items.filter((annotation) => (
    isWordAnnotation(annotation) && annotationsShareLine(annotation, matchingLine)
  )));
  if (rowWords.length === 0) return page;

  const nextLine = updateAnnotationText(
    matchingLine,
    rowWords.map((annotation) => annotationText(annotation)).filter(Boolean).join(' '),
  );
  return upsertAnnotationInPage(page, nextLine);
}

export function joinLineCandidates(selectedAnnotation, annotations) {
  if (!isLineAnnotation(selectedAnnotation)) return [];
  const lines = sortedAnnotations({
    items: (annotations || []).filter((annotation) => isLineAnnotation(annotation)),
  });
  const selectedIndex = lines.findIndex((annotation) => annotation?.id === selectedAnnotation?.id);
  if (selectedIndex < 0) return [];
  const sibling = lines[selectedIndex + 1] || lines[selectedIndex - 1];
  return sibling ? [selectedAnnotation, sibling] : [];
}

export function findAnnotationForWindow(state, canvases, annotationId) {
  if (!annotationId) return null;
  for (const canvas of canvases || []) {
    const page = annotationPageForCanvas(state, canvas.id);
    const items = Array.isArray(page?.items) ? page.items : [];
    const annotation = items.find((item) => item?.id === annotationId);
    if (annotation) return annotation;
  }
  return null;
}

export function updateAnnotationText(annotation, text) {
  const next = structuredClone(annotation);
  const replacement = {
    format: 'text/plain',
    purpose: 'supplementing',
    type: 'TextualBody',
    value: text,
  };

  if (Array.isArray(next.body)) {
    let replaced = false;
    next.body = next.body.map((body) => {
      if (!replaced && body?.type === 'TextualBody' && (body?.purpose === 'describing' || body?.purpose === 'supplementing')) {
        replaced = true;
        return { ...body, ...replacement };
      }
      return body;
    });
    if (!replaced) next.body.unshift(replacement);
    return next;
  }

  next.body = [replacement];
  return next;
}

export function replaceAnnotationInPage(page, annotationId, replacements) {
  const nextPage = structuredClone(page);
  const items = Array.isArray(nextPage?.items) ? nextPage.items : [];
  const replacementItems = Array.isArray(replacements) ? replacements : [replacements];
  const index = items.findIndex((item) => item?.id === annotationId);
  if (index < 0) return nextPage;
  items.splice(index, 1, ...replacementItems);
  nextPage.items = items;
  return nextPage;
}

export function removeAnnotationsFromPage(page, annotationIds) {
  const ids = new Set(annotationIds);
  const nextPage = structuredClone(page);
  nextPage.items = (Array.isArray(nextPage?.items) ? nextPage.items : []).filter((item) => !ids.has(item?.id));
  return nextPage;
}

export function upsertAnnotationInPage(page, annotation) {
  const nextPage = structuredClone(page);
  const items = Array.isArray(nextPage?.items) ? nextPage.items : [];
  const index = items.findIndex((item) => item?.id === annotation?.id);
  if (index >= 0) items[index] = annotation;
  else items.push(annotation);
  nextPage.items = items;
  return nextPage;
}

export function createDraftLineAnnotation(canvasId, bbox) {
  const x = Math.max(0, Math.round(bbox?.x || 0));
  const y = Math.max(0, Math.round(bbox?.y || 0));
  const w = Math.max(1, Math.round(bbox?.w || 1));
  const h = Math.max(1, Math.round(bbox?.h || 1));

  return {
    type: 'Annotation',
    textGranularity: 'line',
    motivation: 'supplementing',
    body: [{
      type: 'TextualBody',
      purpose: 'supplementing',
      format: 'text/plain',
      value: '',
    }],
    target: {
      source: {
        id: canvasId,
        type: 'Canvas',
      },
      selector: {
        type: 'FragmentSelector',
        conformsTo: 'http://www.w3.org/TR/media-frags/',
        value: `xywh=${x},${y},${w},${h}`,
      },
    },
  };
}
