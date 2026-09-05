import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { readFile } from "node:fs/promises";
import test from "node:test";

import { exactStructuralSnapshot } from "./deployed-readiness-structure.mjs";

const wordTexts = ["browser", "readiness", "alpha", "beta", "gamma", "epsilon"];
const lineText = wordTexts.join(" ");
const expected = { lineText, wordTexts };
const publishedManifestSHA = "e871f532c845bd90f983f3e13282ded1442de29b";
const publishedAssetSHA = "8202da4a50fef7256b77d60685f2f0e08e14f3c9";
const publishedManifestURL = `https://raw.githubusercontent.com/lehigh-university-libraries/scribe/${publishedManifestSHA}/internal/server/testdata/deployed-readiness/manifest.json`;
const publishedImageURL = `https://raw.githubusercontent.com/lehigh-university-libraries/scribe/${publishedAssetSHA}/web/e2e/canvas-a.svg`;
const publishedHOCRURL = `https://raw.githubusercontent.com/lehigh-university-libraries/scribe/${publishedAssetSHA}/internal/server/testdata/crosswalk/expected_hocr.html`;

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

test("deployed readiness pins the published fixture and its transitive assets", async () => {
  const [runner, manifestRaw, image] = await Promise.all([
    readFile(new URL("./deployed-readiness.mjs", import.meta.url), "utf8"),
    readFile(new URL("../../internal/server/testdata/deployed-readiness/manifest.json", import.meta.url), "utf8"),
    readFile(new URL("./canvas-a.svg", import.meta.url)),
  ]);
  const manifest = JSON.parse(manifestRaw);
  assert.equal(
    runner.includes(`const manifestURL = ${JSON.stringify(publishedManifestURL)};`),
    true,
  );
  assert.match(runner, /const legacyManifestURL = "https:\/\/preserve\.lehigh\.edu\/node\/38817\/book-manifest"/u);
  assert.equal(manifest.items.length, 6);
  assert.deepEqual(
    new Set(manifest.items.map((canvas) => canvas.items[0].items[0].body.id)),
    new Set([publishedImageURL]),
  );
  assert.deepEqual(
    new Set(manifest.items.map((canvas) => canvas.seeAlso[0].id)),
    new Set([publishedHOCRURL]),
  );
  const imageSHA256 = createHash("sha256").update(image).digest("hex");
  assert.equal(
    runner.includes(`const readinessManifestImageSHA256 = ${JSON.stringify(imageSHA256)};`),
    true,
  );
  for (const tuple of [
    "line:Foo bar baz",
    "line:Hello world",
    "word:Foo",
    "word:Hello",
    "word:bar",
    "word:baz",
    "word:world",
  ]) {
    assert.equal(runner.includes(JSON.stringify(tuple)), true);
  }
});

test("publication diagnostics remain compatible with protected single-file staging", async () => {
  const runner = await readFile(new URL("./deployed-readiness.mjs", import.meta.url), "utf8");
  assert.match(
    runner,
    /manifestFailureExitCode as sharedManifestFailureExitCode/u,
  );
  for (const [substage, exitCode] of [
    ["first-publication-request", 84],
    ["first-publication-confirmation", 126],
    ["first-publication-resource", 127],
    ["first-publication-contract", 128],
    ["second-publication-request", 89],
    ["second-publication-resource", 129],
    ["second-publication-contract", 130],
  ]) {
    assert.equal(
      runner.includes(`[${JSON.stringify(substage)}, ${exitCode}]`),
      true,
      substage,
    );
  }
  assert.match(
    runner,
    /protectedPreviewPublicationExitCodes\.get\(substage\)[\s\S]*sharedManifestFailureExitCode\(substage\)/u,
  );
});
