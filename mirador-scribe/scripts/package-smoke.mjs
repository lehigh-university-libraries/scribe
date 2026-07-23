import { createRequire } from 'node:module';
import { strict as assert } from 'node:assert';
import { Window } from 'happy-dom';

// Mirador resolves several browser globals at module load time. A lightweight
// DOM lets this test exercise both published entry points as a browser-aware
// consumer would, without mounting a viewer.
const browserWindow = new Window({ url: 'https://consumer.example/' });
for (const [name, value] of Object.entries({
  CustomEvent: browserWindow.CustomEvent,
  document: browserWindow.document,
  DOMParser: browserWindow.DOMParser,
  Element: browserWindow.Element,
  Event: browserWindow.Event,
  HTMLElement: browserWindow.HTMLElement,
  MutationObserver: browserWindow.MutationObserver,
  navigator: browserWindow.navigator,
  Node: browserWindow.Node,
  self: browserWindow,
  window: browserWindow,
})) {
  Object.defineProperty(globalThis, name, { configurable: true, value });
}

const esm = await import('../dist/mirador-scribe.mjs');
const require = createRequire(import.meta.url);
const cjs = require('../dist/mirador-scribe.cjs');

for (const entry of [esm, cjs]) {
  assert.ok(Array.isArray(entry.default), 'default export must be the Mirador plugin array');
  assert.equal(typeof entry.ScribeAnnotationAdapter, 'function');
  assert.equal(typeof entry.annotationAdapters?.ScribeAnnotationAdapter, 'function');
}
