import assert from "node:assert/strict";
import test from "node:test";

import { exactStructuralSnapshot } from "./deployed-readiness-structure.mjs";

const wordTexts = ["browser", "readiness", "alpha", "beta", "gamma", "epsilon"];
const lineText = wordTexts.join(" ");
const expected = { lineText, wordTexts };

function annotation(id, textGranularity, value) {
  return {
    id,
    type: "Annotation",
    textGranularity,
    body: { type: "TextualBody", value },
  };
}

function validAnnotations() {
  return [
    annotation("line-other", "line", "unrelated line"),
    annotation("line-joined", "line", lineText),
    ...wordTexts.map((text) => annotation(`word-${text}`, "word", text)),
  ];
}

test("exact structural snapshot ignores the selected retained word", () => {
  const editorState = {
    annotations: validAnnotations(),
    selectedAnnotationId: "word-epsilon",
  };

  const snapshot = exactStructuralSnapshot(editorState.annotations, expected);

  assert.equal(snapshot.line.id, "line-joined");
  assert.deepEqual(
    snapshot.words.map((word) => word.body.value),
    wordTexts,
  );
});

test("exact structural snapshot fails closed on ambiguous or malformed structure", () => {
  const cases = [
    ["duplicate joined line", (annotations) => [
      ...annotations,
      annotation("line-joined-copy", "line", lineText),
    ]],
    ["missing word", (annotations) => annotations.filter(({ id }) => id !== "word-epsilon")],
    ["extra word", (annotations) => [
      ...annotations,
      annotation("word-extra", "word", "extra"),
    ]],
    ["wrong word text", (annotations) => annotations.map((entry) => (
      entry.id === "word-epsilon"
        ? annotation(entry.id, "word", "zeta")
        : entry
    ))],
    ["duplicate word ID", (annotations) => annotations.map((entry) => (
      entry.id === "word-epsilon"
        ? { ...entry, id: "word-gamma" }
        : entry
    ))],
    ["line and word share an ID", (annotations) => annotations.map((entry) => (
      entry.id === "line-joined"
        ? { ...entry, id: "word-browser" }
        : entry
    ))],
    ["empty word ID", (annotations) => annotations.map((entry) => (
      entry.id === "word-epsilon"
        ? { ...entry, id: "" }
        : entry
    ))],
  ];

  for (const [name, mutate] of cases) {
    assert.throws(
      () => exactStructuralSnapshot(mutate(validAnnotations()), expected),
      /editor structural snapshot failed its exact contract/,
      name,
    );
  }
});
