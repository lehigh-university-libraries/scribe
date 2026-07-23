/**
 * @typedef {import('../types/scribe').AnnotationResource} AnnotationResource
 * @typedef {import('../types/scribe').EditorRow} EditorRow
 * @typedef {import('../types/scribe').IIIFAnnotation} IIIFAnnotation
 * @typedef {import('../types/scribe').IdentifiedIIIFAnnotation} IdentifiedIIIFAnnotation
 * @typedef {import('../types/scribe').IIIFAnnotationPage} IIIFAnnotationPage
 * @typedef {import('../types/scribe').IIIFSelector} IIIFSelector
 * @typedef {import('../types/scribe').IIIFTextualBody} IIIFTextualBody
 * @typedef {import('../types/scribe').ImageBBox} ImageBBox
 * @typedef {import('../types/scribe').MiradorState} MiradorState
 * @typedef {import('../types/scribe').Point2D} Point2D
 * @typedef {{ annotation: IIIFAnnotation, center: number, height: number, order: number }} VerticalEntry
 * @typedef {VerticalEntry & { start: number, end: number }} IntervalPoint
 * @typedef {{ add(point: IntervalPoint): void, firstIntersecting(queryStart: number, queryEnd: number): number }} IntervalOrderIndex
 */

/** @param {IIIFAnnotation | null | undefined} annotation @returns {IIIFTextualBody | null} */
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

/** @param {IIIFAnnotation | null | undefined} annotation @returns {string} */
export function annotationText(annotation) {
  return annotationTextBody(annotation)?.value || '';
}

/** @param {IIIFAnnotation | null | undefined} annotation @returns {string} */
export function annotationCanvasId(annotation) {
  const target = annotation?.target;
  if (!target) return '';
  if (typeof target === 'string') {
    const hashIndex = target.indexOf('#');
    return hashIndex >= 0 ? target.slice(0, hashIndex) : target;
  }
  return typeof target.source === 'string' ? target.source : target.source?.id || '';
}

/** @param {MiradorState | null | undefined} state @param {string} canvasId @returns {IIIFAnnotationPage | null} */
export function annotationPageForCanvas(state, canvasId) {
  const resources = state?.annotations?.[canvasId];
  if (!resources || typeof resources !== 'object') return null;
  return Object.values(resources)
    .map((resource) => resource?.json)
    .find((page) => page?.type === 'AnnotationPage') || null;
}

/** @param {MiradorState | null | undefined} state @param {string} windowId @returns {string} */
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

/** @param {MiradorState | null | undefined} state @param {string} windowId @param {string} content @returns {boolean} */
export function hasCompanionWindowContent(state, windowId, content) {
  return Object.values(state?.companionWindows || {}).some((entry) => {
    if (!entry || entry.content !== content) return false;
    return entry.windowId === windowId;
  });
}

/** @param {MiradorState | null | undefined} state @returns {Array<[string, AnnotationResource]>} */
export function annotationEntries(state) {
  return Object.entries(state?.annotations || {}).flatMap(([canvasId, resources]) => (
    Object.values(resources || {})
      .filter((resource) => resource?.json?.type === 'AnnotationPage')
      .map((resource) => /** @type {[string, AnnotationResource]} */ ([canvasId, resource]))
  ));
}

/** @param {MiradorState | null | undefined} state @param {string} annotationId @returns {IIIFAnnotationPage | null} */
export function findAnnotationPageByAnnotationId(state, annotationId) {
  if (!annotationId) return null;
  for (const [, value] of annotationEntries(state)) {
    const page = value?.json;
    const items = Array.isArray(page?.items) ? page.items : [];
    if (items.some((item) => item?.id === annotationId)) {
      return page || null;
    }
  }
  return null;
}

/** @param {MiradorState | null | undefined} state @param {string} annotationId @returns {string} */
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

/** @param {MiradorState | null | undefined} state @returns {string} */
export function firstAnnotationCanvasId(state) {
  return annotationEntries(state)[0]?.[0] || '';
}

/** @param {MiradorState | null | undefined} state @returns {IIIFAnnotationPage | null} */
export function firstAnnotationPage(state) {
  return annotationEntries(state)[0]?.[1]?.json || null;
}

/** @param {MiradorState | null | undefined} state @param {string} windowId @returns {string} */
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

const FRAGMENT_SELECTOR = 'fragmentselector';
const MEDIA_FRAGMENTS_PROFILE = 'http://www.w3.org/TR/media-frags/';

/** @param {unknown} value @returns {value is IIIFSelector} */
function isFragmentSelector(value) {
  return value !== null
    && typeof value === 'object'
    && String(/** @type {Record<string, unknown>} */ (value).type || '').trim().toLowerCase() === FRAGMENT_SELECTOR;
}

/** @param {unknown} value @returns {string[]} */
function mediaFragmentParameters(value) {
  const raw = String(value || '').trim().replace(/^#/, '');
  return raw === '' ? [] : raw.split('&');
}

/**
 * Parse exactly one xywh dimension from a media fragment. The result mirrors
 * the backend's canonical selector rule: two xywh dimensions are ambiguous,
 * even when they appear in the same FragmentSelector.
 *
 * @param {unknown} value
 * @param {{ width: number, height: number } | null | undefined} dimensions
 */
function mediaFragmentGeometry(value, dimensions) {
  const xywhValues = [];
  for (const parameter of mediaFragmentParameters(value)) {
    const separator = parameter.indexOf('=');
    if (separator < 0 || parameter.slice(0, separator).trim() !== 'xywh') continue;
    xywhValues.push(parameter.slice(separator + 1).trim());
  }
  if (xywhValues.length > 1) {
    return { bbox: null, error: new Error('media fragment contains multiple xywh parameters'), present: false };
  }
  if (xywhValues.length === 0) return { bbox: null, error: null, present: false };

  const match = xywhValues[0].match(
    /^(?:(pixel|percent):)?(-?\d+(?:\.\d+)?),(-?\d+(?:\.\d+)?),(\d+(?:\.\d+)?),(\d+(?:\.\d+)?)$/,
  );
  if (!match) {
    return { bbox: null, error: new Error('media fragment contains invalid xywh geometry'), present: true };
  }
  let [x, y, w, h] = match.slice(2).map(Number);
  if (match[1] === 'percent'
    && dimensions
    && dimensions.width > 0
    && dimensions.height > 0) {
    x = x * dimensions.width / 100;
    w = w * dimensions.width / 100;
    y = y * dimensions.height / 100;
    h = h * dimensions.height / 100;
  }
  return { bbox: { h, w, x, y }, error: null, present: true };
}

/** @param {unknown} selector @returns {IIIFSelector[]} */
function fragmentSelectors(selector) {
  const selectors = Array.isArray(selector) ? selector : [selector];
  return selectors.filter(isFragmentSelector);
}

/**
 * @param {unknown} selector
 * @param {{ width: number, height: number } | null | undefined} dimensions
 */
function spatialFragmentSelector(selector, dimensions = null) {
  const fragments = fragmentSelectors(selector);
  let spatial = null;
  let bbox = null;
  for (const fragment of fragments) {
    const geometry = mediaFragmentGeometry(fragment.value, dimensions);
    if (geometry.error) return { bbox: null, error: geometry.error, fragments, spatial: null };
    if (!geometry.present) continue;
    if (spatial) {
      return {
        bbox: null,
        error: new Error('target contains multiple xywh FragmentSelectors'),
        fragments,
        spatial: null,
      };
    }
    spatial = fragment;
    bbox = geometry.bbox;
  }
  return { bbox, error: null, fragments, spatial };
}

/** @param {unknown} fragment @param {string} value @returns {string} */
function replaceMediaFragmentGeometry(fragment, value) {
  const parameters = mediaFragmentParameters(fragment);
  let replaced = false;
  for (let index = 0; index < parameters.length; index += 1) {
    const parameter = parameters[index];
    const separator = parameter.indexOf('=');
    if (separator < 0 || parameter.slice(0, separator).trim() !== 'xywh') continue;
    if (replaced) throw new Error('media fragment contains multiple xywh parameters');
    parameters[index] = `${parameter.slice(0, separator)}=${value}`;
    replaced = true;
  }
  if (!replaced) parameters.push(`xywh=${value}`);
  return parameters.join('&');
}

/**
 * @param {IIIFAnnotation | null | undefined} annotation
 * @param {{ width: number, height: number } | null} [dimensions]
 * @returns {ImageBBox}
 */
export function annotationBBox(annotation, dimensions = null) {
  if (typeof annotation?.target === 'string') {
    const hashIndex = annotation.target.indexOf('#');
    const geometry = mediaFragmentGeometry(
      hashIndex >= 0 ? annotation.target.slice(hashIndex + 1) : '',
      dimensions,
    );
    if (!geometry.error && geometry.bbox) return geometry.bbox;
  }
  const target = annotation?.target;
  const geometry = spatialFragmentSelector(
    target && typeof target === 'object' ? target.selector : null,
    dimensions,
  );
  return (!geometry.error && geometry.bbox) || { h: 0, w: 0, x: 0, y: 0 };
}

/** @param {IIIFAnnotation | null | undefined} annotation @returns {string} */
export function annotationGranularity(annotation) {
  return annotation?.textGranularity || 'line';
}

/** @param {IIIFAnnotation | null | undefined} annotation @returns {boolean} */
export function isWordAnnotation(annotation) {
  return annotationGranularity(annotation) === 'word';
}

/** @param {IIIFAnnotation | null | undefined} annotation @returns {boolean} */
export function isLineAnnotation(annotation) {
  return annotationGranularity(annotation) === 'line';
}

/** @param {IIIFAnnotation | null | undefined} left @param {IIIFAnnotation | null | undefined} right @returns {boolean} */
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

/** @param {IIIFAnnotation} annotation @param {ImageBBox | null | undefined} rect @returns {boolean} */
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

/** @param {IIIFAnnotationPage | null | undefined} page @returns {IdentifiedIIIFAnnotation[]} */
export function sortedAnnotations(page) {
  const items = /** @type {IdentifiedIIIFAnnotation[]} */ ((Array.isArray(page?.items) ? [...page.items] : [])
    .filter((annotation) => typeof annotation.id === 'string' && annotation.id));
  return items.sort((left, right) => {
    const a = annotationBBox(left);
    const b = annotationBBox(right);
    if (a.y !== b.y) return a.y - b.y;
    if (a.x !== b.x) return a.x - b.x;
    return String(left?.id || '').localeCompare(String(right?.id || ''));
  });
}

/** @param {IIIFAnnotation[]} annotations @returns {ImageBBox | null} */
function bboxUnion(annotations) {
  if (!annotations.length) return null;
  const boxes = annotations.map((annotation) => annotationBBox(annotation));
  const left = Math.min(...boxes.map(({ x }) => x));
  const top = Math.min(...boxes.map(({ y }) => y));
  const right = Math.max(...boxes.map(({ x, w }) => x + w));
  const bottom = Math.max(...boxes.map(({ y, h }) => y + h));
  return { h: bottom - top, w: right - left, x: left, y: top };
}

/** @param {ImageBBox} reference @param {ImageBBox} candidate @returns {[number, number]} */
function bboxDistance(reference, candidate) {
  const horizontalGap = Math.max(0, reference.x - (candidate.x + candidate.w), candidate.x - (reference.x + reference.w));
  const verticalGap = Math.max(0, reference.y - (candidate.y + candidate.h), candidate.y - (reference.y + reference.h));
  const referenceCenterX = reference.x + reference.w / 2;
  const referenceCenterY = reference.y + reference.h / 2;
  const candidateCenterX = candidate.x + candidate.w / 2;
  const candidateCenterY = candidate.y + candidate.h / 2;
  return [
    horizontalGap * horizontalGap + verticalGap * verticalGap,
    (referenceCenterX - candidateCenterX) ** 2 + (referenceCenterY - candidateCenterY) ** 2,
  ];
}

/** @param {unknown} left @param {unknown} right @returns {boolean} */
function jsonValuesEqual(left, right) {
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
      && left.every((value, index) => jsonValuesEqual(value, right[index]));
  }
  const leftKeys = Object.keys(left);
  const rightKeys = Object.keys(right);
  const leftRecord = /** @type {Record<string, unknown>} */ (left);
  const rightRecord = /** @type {Record<string, unknown>} */ (right);
  return leftKeys.length === rightKeys.length
    && leftKeys.every((key) => (
      Object.hasOwn(right, key) && jsonValuesEqual(leftRecord[key], rightRecord[key])
    ));
}

/**
 * Chooses a stable post-operation selection. A retained selected Annotation is
 * preferred; otherwise the nearest changed/new Annotation of the same text
 * granularity is used before falling back to the nearest page Annotation.
 */
/** @param {IIIFAnnotationPage | null | undefined} beforePage @param {IIIFAnnotationPage | null | undefined} afterPage @param {string[]} [selectedIds] @returns {string} */
export function selectionAfterPageTransform(beforePage, afterPage, selectedIds = []) {
  const beforeItems = Array.isArray(beforePage?.items) ? beforePage.items : [];
  const afterItems = Array.isArray(afterPage?.items) ? afterPage.items : [];
  const normalizedIds = [...new Set((selectedIds || []).filter(Boolean))];
  const retained = normalizedIds.find((id) => afterItems.some((annotation) => annotation?.id === id));
  if (retained) return retained;
  if (afterItems.length === 0) return '';

  const selected = /** @type {IIIFAnnotation[]} */ (normalizedIds
    .map((id) => beforeItems.find((annotation) => annotation?.id === id))
    .filter(Boolean));
  const reference = bboxUnion(selected);
  if (!reference) return sortedAnnotations(afterPage)[0]?.id || '';
  const preferredGranularity = annotationGranularity(selected[0]);
  const beforeById = new Map(beforeItems.map((annotation) => [annotation?.id, annotation]));
  const changed = afterItems.filter((annotation) => {
    const previous = beforeById.get(annotation?.id);
    return !previous || !jsonValuesEqual(previous, annotation);
  });
  const sameGranularity = changed.filter((annotation) => annotationGranularity(annotation) === preferredGranularity);
  const candidates = sameGranularity.length > 0
    ? sameGranularity
    : (changed.length > 0 ? changed : afterItems);

  return [...candidates].sort((left, right) => {
    const leftDistance = bboxDistance(reference, annotationBBox(left));
    const rightDistance = bboxDistance(reference, annotationBBox(right));
    if (leftDistance[0] !== rightDistance[0]) return leftDistance[0] - rightDistance[0];
    if (leftDistance[1] !== rightDistance[1]) return leftDistance[1] - rightDistance[1];
    return String(left?.id || '').localeCompare(String(right?.id || ''));
  })[0]?.id || '';
}

/**
 * Keeps action targets tied to the page selection even when panning has moved
 * that annotation outside the viewport. Visibility may filter lists, but it
 * must never silently retarget keyboard or toolbar mutations.
 * @param {IdentifiedIIIFAnnotation[]} annotations
 * @param {IdentifiedIIIFAnnotation[]} visibleAnnotations
 * @param {string} selectedAnnotationId
 * @param {string} preferredAnnotationId
 * @returns {IdentifiedIIIFAnnotation | null}
 */
export function editorSelectedAnnotation(
  annotations,
  visibleAnnotations,
  selectedAnnotationId,
  preferredAnnotationId,
) {
  const pageItems = Array.isArray(annotations) ? annotations : [];
  const visibleItems = Array.isArray(visibleAnnotations) ? visibleAnnotations : [];
  return pageItems.find((annotation) => annotation?.id === selectedAnnotationId)
    || pageItems.find((annotation) => annotation?.id === preferredAnnotationId)
    || visibleItems[0]
    || pageItems[0]
    || null;
}

/** @param {IIIFAnnotation[]} annotations @returns {IIIFAnnotation[]} */
export function sortAnnotationsWithinLine(annotations) {
  return [...(annotations || [])].sort((left, right) => {
    const a = annotationBBox(left);
    const b = annotationBBox(right);
    if (a.x !== b.x) return a.x - b.x;
    if (a.y !== b.y) return a.y - b.y;
    return String(left?.id || '').localeCompare(String(right?.id || ''));
  });
}

/** @param {number[]} values @param {number} target @returns {number} */
function ascendingLowerBound(values, target) {
  let low = 0;
  let high = values.length;
  while (low < high) {
    const middle = Math.floor((low + high) / 2);
    if (values[middle] < target) low = middle + 1;
    else high = middle;
  }
  return low;
}

/** @param {number[]} values @param {number} target @returns {number} */
function ascendingUpperBound(values, target) {
  let low = 0;
  let high = values.length;
  while (low < high) {
    const middle = Math.floor((low + high) / 2);
    if (values[middle] <= target) low = middle + 1;
    else high = middle;
  }
  return low;
}

/** @param {number[]} values @param {number} target @returns {number} */
function descendingLowerBound(values, target) {
  let low = 0;
  let high = values.length;
  while (low < high) {
    const middle = Math.floor((low + high) / 2);
    if (values[middle] > target) low = middle + 1;
    else high = middle;
  }
  return low;
}

/** @param {number[]} values @param {number} minimum @returns {number} */
function descendingPrefixLength(values, minimum) {
  let low = 0;
  let high = values.length;
  while (low < high) {
    const middle = Math.floor((low + high) / 2);
    if (values[middle] >= minimum) low = middle + 1;
    else high = middle;
  }
  return low;
}

/**
 * Dynamic two-dimensional dominance index. A query returns the lowest reading
 * order among added intervals whose start <= queryEnd and end >= queryStart.
 * The nested Fenwick trees keep both updates and queries O(log² n).
 * @param {IntervalPoint[]} points
 * @returns {IntervalOrderIndex}
 */
function createIntervalOrderIndex(points) {
  const starts = [...new Set(points.map(({ start }) => start))].sort((a, b) => a - b);
  /** @type {number[][]} */
  const endCoordinates = Array.from({ length: starts.length + 1 }, () => []);
  for (const point of points) {
    const startRank = ascendingLowerBound(starts, point.start) + 1;
    for (let index = startRank; index <= starts.length; index += index & -index) {
      endCoordinates[index].push(point.end);
    }
  }
  for (let index = 1; index < endCoordinates.length; index += 1) {
    endCoordinates[index] = [...new Set(endCoordinates[index])].sort((a, b) => b - a);
  }
  const minimumOrders = endCoordinates.map((coordinates) => (
    new Float64Array(coordinates.length + 1).fill(Number.POSITIVE_INFINITY)
  ));

  return {
    add(point) {
      const startRank = ascendingLowerBound(starts, point.start) + 1;
      for (let outer = startRank; outer <= starts.length; outer += outer & -outer) {
        const endRank = descendingLowerBound(endCoordinates[outer], point.end) + 1;
        for (let inner = endRank; inner < minimumOrders[outer].length; inner += inner & -inner) {
          minimumOrders[outer][inner] = Math.min(minimumOrders[outer][inner], point.order);
        }
      }
    },
    firstIntersecting(queryStart, queryEnd) {
      let result = Number.POSITIVE_INFINITY;
      for (let outer = ascendingUpperBound(starts, queryEnd); outer > 0; outer -= outer & -outer) {
        const prefix = descendingPrefixLength(endCoordinates[outer], queryStart);
        for (let inner = prefix; inner > 0; inner -= inner & -inner) {
          result = Math.min(result, minimumOrders[outer][inner]);
        }
      }
      return result;
    },
  };
}

/** @param {IIIFAnnotation} annotation @param {number} order @returns {VerticalEntry} */
function verticalEntry(annotation, order) {
  const bbox = annotationBBox(annotation);
  const height = Number.isFinite(bbox.h) && bbox.h > 0 ? bbox.h : 1;
  return {
    annotation,
    center: bbox.y + height / 2,
    height,
    order,
  };
}

/** @param {VerticalEntry[]} lineEntries @param {VerticalEntry[]} wordEntries @param {'tall' | 'short'} kind @returns {Float64Array} */
function earliestLineOrders(lineEntries, wordEntries, kind) {
  const tall = kind === 'tall';
  const points = lineEntries.map((entry) => {
    const radius = entry.height * (tall ? 0.5 : 0.05);
    return {
      ...entry,
      end: entry.center + radius,
      start: entry.center - radius,
    };
  });
  const index = createIntervalOrderIndex(points);
  const pendingLines = [...points].sort((left, right) => (
    tall
      ? right.height - left.height || left.order - right.order
      : left.height - right.height || left.order - right.order
  ));
  const pendingWords = [...wordEntries].sort((left, right) => (
    tall
      ? right.height - left.height || left.order - right.order
      : left.height - right.height || left.order - right.order
  ));
  const result = new Float64Array(wordEntries.length).fill(Number.POSITIVE_INFINITY);
  let lineIndex = 0;

  for (const word of pendingWords) {
    while (lineIndex < pendingLines.length && (tall
      ? pendingLines[lineIndex].height >= word.height
      : pendingLines[lineIndex].height < word.height)) {
      index.add(pendingLines[lineIndex]);
      lineIndex += 1;
    }
    const radius = word.height * (tall ? 0.05 : 0.5);
    result[word.order] = index.firstIntersecting(word.center - radius, word.center + radius);
  }
  return result;
}

/** @param {IIIFAnnotationPage | null | undefined} page @returns {EditorRow[]} */
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

  const lineEntries = lines.map(verticalEntry);
  const wordEntries = words.map(verticalEntry);
  const tallLineOrders = earliestLineOrders(lineEntries, wordEntries, 'tall');
  const shortLineOrders = earliestLineOrders(lineEntries, wordEntries, 'short');
  /** @type {IIIFAnnotation[][]} */
  const fieldsByLine = Array.from({ length: lines.length }, () => []);
  /** @type {IIIFAnnotation[]} */
  const looseWords = [];
  for (const word of wordEntries) {
    const lineOrder = Math.min(tallLineOrders[word.order], shortLineOrders[word.order]);
    if (Number.isFinite(lineOrder)) fieldsByLine[lineOrder].push(word.annotation);
    else looseWords.push(word.annotation);
  }

  /** @type {EditorRow[]} */
  const rows = lines.map((line, order) => {
    const rowWords = sortAnnotationsWithinLine(fieldsByLine[order]);
    return {
      id: line.id,
      granularity: rowWords.length > 0 ? 'word' : 'line',
      lead: line,
      fields: rowWords.length > 0 ? rowWords : [line],
    };
  });

  /** @type {EditorRow[]} */
  const looseWordRows = [];
  for (const word of looseWords) {
    const row = looseWordRows.at(-1);
    if (row) {
      if (annotationsShareLine(row.lead, word)) {
        row.fields.push(word);
        continue;
      }
    }
    looseWordRows.push({
      id: word.id,
      granularity: 'word',
      lead: word,
      fields: [word],
    });
  }
  looseWordRows.forEach((row) => { row.fields = sortAnnotationsWithinLine(row.fields); });

  return [...rows, ...looseWordRows].sort((left, right) => {
    const a = annotationBBox(left.lead);
    const b = annotationBBox(right.lead);
    if (a.y !== b.y) return a.y - b.y;
    if (a.x !== b.x) return a.x - b.x;
    return String(left.id).localeCompare(String(right.id));
  });
}

/** @param {EditorRow | null | undefined} row @returns {{ canSplitLine: boolean, canSplitToWords: boolean }} */
export function editorRowTransformCapabilities(row) {
  const hasLine = Boolean(row?.lead && isLineAnnotation(row.lead));
  return {
    canSplitLine: hasLine,
    canSplitToWords: hasLine && row?.granularity === 'line',
  };
}

/** @param {EditorRow | null | undefined} row @returns {string} */
export function rowText(row) {
  const fields = Array.isArray(row?.fields) ? row.fields : [];
  if (fields.length === 0) return '';
  if (row?.granularity === 'word' && row?.lead && isLineAnnotation(row.lead)) {
    return annotationText(row.lead);
  }
  if (row?.granularity === 'word') {
    return fields.map((annotation) => annotationText(annotation)).join(' ').trim();
  }
  return annotationText(row?.lead || fields[0]);
}

/** @param {EditorRow | null | undefined} row @returns {ImageBBox} */
export function rowBBox(row) {
  const fields = Array.isArray(row?.fields) ? row.fields : [];
  const annotations = [row?.lead, ...fields].filter(Boolean);
  if (annotations.length === 0) {
    return {
      h: 0,
      w: 0,
      x: 0,
      y: 0,
    };
  }

  const boxes = annotations.map((annotation) => annotationBBox(annotation));
  const left = Math.min(...boxes.map((box) => box.x));
  const top = Math.min(...boxes.map((box) => box.y));
  const right = Math.max(...boxes.map((box) => box.x + box.w));
  const bottom = Math.max(...boxes.map((box) => box.y + box.h));

  return {
    h: Math.max(0, bottom - top),
    w: Math.max(0, right - left),
    x: left,
    y: top,
  };
}

/** @param {IIIFAnnotationPage | null | undefined} page @param {IIIFAnnotation | null | undefined} annotation @returns {IIIFAnnotation | null} */
export function lineAnnotationForSelection(page, annotation) {
  if (!page || !annotation) return null;
  if (isLineAnnotation(annotation)) return annotation;
  const items = Array.isArray(page?.items) ? page.items : [];
  return items.find((candidate) => (
    isLineAnnotation(candidate) && annotationsShareLine(candidate, annotation)
  )) || null;
}

/** @param {EditorRow | null | undefined} row @returns {string} */
export function rowSelectionId(row) {
  return row?.lead?.id || row?.fields?.[0]?.id || '';
}

/** @param {IIIFAnnotationPage | null | undefined} page @param {string} annotationId @returns {EditorRow | null} */
export function findEditorRowByAnnotationId(page, annotationId) {
  if (!annotationId) return null;
  return groupAnnotationsForEditor(page).find((row) => (
    row?.lead?.id === annotationId
      || (Array.isArray(row?.fields) && row.fields.some((annotation) => annotation.id === annotationId))
  )) || null;
}

/** @param {EditorRow | null | undefined} row @param {string} value @param {number | null | undefined} selectionStart @returns {string} */
export function wordAnnotationIdForCaret(row, value, selectionStart) {
  if (row?.granularity !== 'word' || !Array.isArray(row?.fields) || row.fields.length === 0) {
    return rowSelectionId(row);
  }
  const text = String(value || '');
  const caret = Math.max(0, Math.min(selectionStart ?? text.length, text.length));
  const beforeCaret = text.slice(0, caret);
  const tokensBeforeCaret = beforeCaret.trim().length === 0 ? 0 : beforeCaret.trim().split(/\s+/).length - 1;
  const clampedIndex = Math.max(0, Math.min(tokensBeforeCaret, row.fields.length - 1));
  return row.fields[clampedIndex]?.id || rowSelectionId(row);
}

/** @param {IIIFAnnotation | null | undefined} selectedAnnotation @param {IIIFAnnotation[]} annotations @returns {IIIFAnnotation[]} */
export function joinWordCandidates(selectedAnnotation, annotations) {
  if (!selectedAnnotation || !isWordAnnotation(selectedAnnotation)) return [];
  const candidates = sortedAnnotations({
    items: (annotations || []).filter((annotation) => isWordAnnotation(annotation) && annotationsShareLine(annotation, selectedAnnotation)),
  });
  return candidates.length > 1 ? candidates : [];
}

/** @param {IIIFAnnotationPage} page @param {IIIFAnnotation} changedWordAnnotation @returns {IIIFAnnotationPage} */
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

/** @param {IIIFAnnotation[]} words @param {string} text @returns {IIIFAnnotation[]} */
function distributeRowTextAcrossWords(words, text) {
  const normalized = String(text || '').replace(/\s+/g, ' ').trim();
  const tokens = normalized ? normalized.split(' ') : [];
  if (!Array.isArray(words) || words.length === 0) return [];

  return words.map((word, index) => {
    let nextText = '';
    if (tokens.length === words.length) {
      nextText = tokens[index] || '';
    } else if (tokens.length < words.length) {
      nextText = tokens[index] || '';
    } else if (index === words.length - 1) {
      nextText = tokens.slice(index).join(' ');
    } else {
      nextText = tokens[index] || '';
    }
    return updateAnnotationText(word, nextText);
  });
}

/** @param {IIIFAnnotationPage} page @param {EditorRow} row @param {string} text @returns {IIIFAnnotationPage} */
export function updateRowText(page, row, text) {
  if (!page || !row) return page;

  const fields = Array.isArray(row.fields) ? row.fields : [];
  if (row.granularity !== 'word' || fields.length === 0) {
    const target = row.lead || fields[0];
    return target ? upsertAnnotationInPage(page, updateAnnotationText(target, text)) : page;
  }

  const nextWords = distributeRowTextAcrossWords(fields, text);
  const replacements = new Map(nextWords.map((word) => [word.id, word]));
  if (row.lead && isLineAnnotation(row.lead)) {
    const nextLine = updateAnnotationText(row.lead, text);
    replacements.set(nextLine.id, nextLine);
  }
  return {
    ...page,
    items: (Array.isArray(page?.items) ? page.items : []).map((item) => replacements.get(item?.id) || item),
  };
}

/** @param {IIIFAnnotation | null | undefined} selectedAnnotation @param {IIIFAnnotation[]} annotations @returns {IIIFAnnotation[]} */
export function joinLineCandidates(selectedAnnotation, annotations) {
  if (!selectedAnnotation || !isLineAnnotation(selectedAnnotation)) return [];
  const lines = sortedAnnotations({
    items: (annotations || []).filter((annotation) => isLineAnnotation(annotation)),
  });
  const selectedIndex = lines.findIndex((annotation) => annotation?.id === selectedAnnotation?.id);
  if (selectedIndex < 0) return [];
  const sibling = lines[selectedIndex + 1] || lines[selectedIndex - 1];
  return sibling ? [selectedAnnotation, sibling] : [];
}

/** @param {MiradorState | null | undefined} state @param {Array<{ id: string }>} canvases @param {string} annotationId @returns {IIIFAnnotation | null} */
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

/** @param {IIIFAnnotation} annotation @param {ImageBBox} bbox @returns {IIIFAnnotation} */
export function updateAnnotationBBox(annotation, { x, y, w, h }) {
  const next = structuredClone(annotation);
  const value = `pixel:${Math.round(x)},${Math.round(y)},${Math.max(1, Math.round(w))},${Math.max(1, Math.round(h))}`;
  if (typeof next?.target === 'string') {
    const hashIndex = next.target.indexOf('#');
    const base = hashIndex >= 0 ? next.target.slice(0, hashIndex) : next.target;
    const fragmentValue = hashIndex >= 0 ? next.target.slice(hashIndex + 1) : '';
    next.target = `${base}#${replaceMediaFragmentGeometry(fragmentValue, value)}`;
    return next;
  }
  if (!next?.target || typeof next.target !== 'object') return next;

  const selector = next?.target?.selector;
  const selection = spatialFragmentSelector(selector);
  if (selection.error) throw selection.error;
  const fragment = selection.spatial || (selection.fragments.length === 1 ? selection.fragments[0] : null);
  if (fragment) {
    fragment.type = 'FragmentSelector';
    fragment.value = replaceMediaFragmentGeometry(fragment.value, value);
    if (fragment.conformsTo == null) fragment.conformsTo = MEDIA_FRAGMENTS_PROFILE;
    return next;
  }

  const spatial = {
    conformsTo: MEDIA_FRAGMENTS_PROFILE,
    type: 'FragmentSelector',
    value: `xywh=${value}`,
  };
  if (selector == null) next.target.selector = spatial;
  else if (Array.isArray(selector)) next.target.selector = [...selector, spatial];
  else next.target.selector = [selector, spatial];
  return next;
}

/** @param {IIIFAnnotation} annotation @param {string} text @returns {IIIFAnnotation} */
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

  if (next.body && typeof next.body === 'object' && next.body.type === 'TextualBody') {
    next.body = [{ ...next.body, ...replacement }];
    return next;
  }

  next.body = [replacement];
  return next;
}

/** @param {IIIFAnnotationPage} page @param {string} annotationId @param {IIIFAnnotation | IIIFAnnotation[]} replacements @returns {IIIFAnnotationPage} */
export function replaceAnnotationInPage(page, annotationId, replacements) {
  const items = Array.isArray(page?.items) ? [...page.items] : [];
  const replacementItems = Array.isArray(replacements) ? replacements : [replacements];
  const index = items.findIndex((item) => item?.id === annotationId);
  if (index < 0) return page;
  items.splice(index, 1, ...replacementItems);
  return { ...page, items };
}

/** @param {IIIFAnnotationPage} page @param {string[]} annotationIds @returns {IIIFAnnotationPage} */
export function removeAnnotationsFromPage(page, annotationIds) {
  const ids = new Set(annotationIds);
  return {
    ...page,
    items: (Array.isArray(page?.items) ? page.items : []).filter((item) => !item.id || !ids.has(item.id)),
  };
}

/** @param {IIIFAnnotationPage} page @param {IIIFAnnotation} annotation @returns {IIIFAnnotationPage} */
export function upsertAnnotationInPage(page, annotation) {
  const items = Array.isArray(page?.items) ? [...page.items] : [];
  const index = items.findIndex((item) => item?.id === annotation?.id);
  if (index >= 0) items[index] = annotation;
  else items.push(annotation);
  return { ...page, items };
}

/** @param {string} pageId @returns {string} */
function canonicalAnnotationPageId(pageId) {
  const candidate = String(pageId || '').trim();
  let parsed;
  try {
    parsed = new URL(candidate);
  } catch {
    throw new Error('Draft annotations require the loaded canonical HTTP(S) AnnotationPage ID');
  }
  if ((parsed.protocol !== 'http:' && parsed.protocol !== 'https:')
    || !parsed.host
    || parsed.username
    || parsed.password
    || parsed.search
    || parsed.hash) {
    throw new Error('Draft annotations require the loaded canonical HTTP(S) AnnotationPage ID');
  }

  const marker = '/item-image-';
  const markerIndex = parsed.pathname.lastIndexOf(marker);
  const suffix = markerIndex < 0 ? '' : parsed.pathname.slice(markerIndex + marker.length);
  const match = suffix.match(/^([1-9][0-9]*)\/canvas\/page-1\/annotations$/);
  if (!match || BigInt(match[1]) > 18_446_744_073_709_551_615n) {
    throw new Error('Draft annotations require a canonical Triplet AnnotationPage ID');
  }
  return candidate;
}

/** @param {string} pageId @returns {string} */
function draftAnnotationId(pageId) {
  const base = canonicalAnnotationPageId(pageId);
  if (!globalThis.crypto?.getRandomValues) {
    throw new Error('Secure random annotation IDs are unavailable');
  }
  const bytes = new Uint8Array(16);
  globalThis.crypto.getRandomValues(bytes);
  const token = Array.from(bytes, (value) => value.toString(16).padStart(2, '0')).join('');
  return `${base}/items/${token}`;
}

/** @param {string} canvasId @param {ImageBBox} bbox @param {string} [pageId] @returns {IIIFAnnotation} */
export function createDraftLineAnnotation(canvasId, bbox, pageId = '') {
  const x = Math.max(0, Math.round(bbox?.x || 0));
  const y = Math.max(0, Math.round(bbox?.y || 0));
  const w = Math.max(1, Math.round(bbox?.w || 1));
  const h = Math.max(1, Math.round(bbox?.h || 1));

  return {
    id: draftAnnotationId(pageId),
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

/** @param {string} canvasId @param {ImageBBox} bbox @param {string} [pageId] @param {string} [text] @returns {IIIFAnnotation} */
export function createDraftWordAnnotation(canvasId, bbox, pageId = '', text = '') {
  const annotation = createDraftLineAnnotation(canvasId, bbox, pageId);
  return updateAnnotationText({ ...annotation, textGranularity: 'word' }, text);
}
