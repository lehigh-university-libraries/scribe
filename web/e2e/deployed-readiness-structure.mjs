function structuralSnapshotFailure() {
  throw new Error("editor structural snapshot failed its exact contract");
}

function annotationTextValue(annotation) {
  const bodies = Array.isArray(annotation?.body) ? annotation.body : [annotation?.body];
  for (const body of bodies) {
    const value = typeof body === "string" ? body : body?.value;
    if (typeof value === "string" && value.trim() !== "") return value.trim();
  }
  return "";
}

function annotationGranularity(annotation) {
  return String(annotation?.textGranularity ?? "").trim().toLowerCase();
}

export function exactStructuralSnapshot(annotations, expected) {
  const expectedLineText = expected?.lineText;
  const expectedWordTexts = expected?.wordTexts;
  if (
    !Array.isArray(annotations)
    || typeof expectedLineText !== "string"
    || expectedLineText === ""
    || !Array.isArray(expectedWordTexts)
    || expectedWordTexts.length === 0
    || expectedWordTexts.some((text) => typeof text !== "string" || text === "")
    || new Set(expectedWordTexts).size !== expectedWordTexts.length
  ) {
    structuralSnapshotFailure();
  }

  const lines = annotations.filter((annotation) => (
    annotationGranularity(annotation) === "line"
    && annotationTextValue(annotation) === expectedLineText
  ));
  const words = annotations.filter((annotation) => (
    annotationGranularity(annotation) === "word"
  ));
  const expectedWordCounts = new Map(expectedWordTexts.map((text) => [text, 1]));
  for (const word of words) {
    const text = annotationTextValue(word);
    const remaining = expectedWordCounts.get(text) ?? 0;
    expectedWordCounts.set(text, remaining - 1);
  }
  const ids = lines.length === 1
    ? [lines[0], ...words].map((annotation) => annotation?.id)
    : [];

  if (
    lines.length !== 1
    || words.length !== expectedWordTexts.length
    || [...expectedWordCounts.values()].some((remaining) => remaining !== 0)
    || ids.some((id) => typeof id !== "string" || id.trim() === "")
    || new Set(ids).size !== ids.length
  ) {
    structuralSnapshotFailure();
  }

  return { line: lines[0], words };
}
