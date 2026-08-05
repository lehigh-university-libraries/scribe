import "../src/styles.css";
import { annotationClient } from "../src/api/annotations";
import { parseIIIFJSON } from "../src/lib/iiif-json";
import { commonViewerOptions } from "../src/pages/editor/mirador";
import { renderEditorLayout } from "../src/pages/editor/layout";

const status = document.getElementById("harness-status");
const app = document.getElementById("app");
const mode = new URLSearchParams(window.location.search).get("mode") || "session";

let OpenSeadragon;
let ScribeAnnotationAdapter;
let acceptSavedSession;
let clientPointToImage;
let createEditorSession;
let editSession;
let imagePointToViewerElement;
let normalizeImageBBox;
let rebaseSession;
let sessionIsDirty;
let viewerElementPointToImage;
let createCanvasImageRegistry;
let annotationCanvasId;
let createDraftLineAnnotation;
let updateAnnotationText;
let pluginStore = null;
let pluginSetCanvas = null;
let pluginState = null;
const pluginLoadItemImageIds = [];
const pluginSaveItemImageIds = [];
const pluginActiveCanvasEvents = [];
const pluginTranscriptionCalls = [];
let pluginLastEditorState = null;
let pluginPendingSplit = null;
let pluginStructuralCalls = {
  joinLineIds: [],
  joinWordIds: [],
  splitAtWord: 0,
};
let backgroundLastEditorState = null;
const backgroundReloadEvents = [];

function backgroundSnapshot() {
  const annotationPage = backgroundLastEditorState?.annotationPage;
  const firstAnnotation = annotationPage?.items?.[0];
  return {
    annotationId: firstAnnotation?.id || "",
    annotationText: firstAnnotation?.body?.[0]?.value || "",
    dirty: backgroundLastEditorState?.saveDisabled === false,
    pendingRemoteIds: [...(backgroundLastEditorState?.pendingRemoteIds || [])],
    reloadCount: backgroundReloadEvents.length,
    sessionStatus: backgroundLastEditorState?.sessionStatus || "",
  };
}

async function triggerBackgroundCompletion() {
  const response = await fetch("/v1/__browser-fixture/complete-background-transcription", {
    method: "POST",
  });
  if (!response.ok) {
    throw new Error(`background completion fixture returned ${response.status}`);
  }
  return response.json();
}

async function initializeBackgroundDelivery() {
  const itemImageId = import.meta.env.VITE_SCRIBE_BROWSER_ITEM_IMAGE_ID || "";
  if (!/^[1-9][0-9]*$/.test(itemImageId)) {
    throw new Error("background delivery requires the browser Connect fixture");
  }
  const route = new URL(window.location.href);
  route.searchParams.set("itemImageId", itemImageId);
  window.history.replaceState(window.history.state, "", route);
  const resetResponse = await fetch("/v1/__browser-fixture/reset-background-transcription", {
    method: "POST",
  });
  if (!resetResponse.ok) {
    throw new Error(`background reset fixture returned ${resetResponse.status}`);
  }
  document.addEventListener("scribe:editor-state", (event) => {
    if (event.detail?.windowId === "scribe-editor-window") {
      backgroundLastEditorState = structuredClone(event.detail);
    }
  });
  document.addEventListener("scribe:reload-annotations", (event) => {
    if (event.detail?.windowId === "scribe-editor-window") {
      backgroundReloadEvents.push(structuredClone(event.detail));
    }
  });
  const { renderEditor } = await import("../src/pages/editor");
  await renderEditor(app);
}

async function initializeSessionModules() {
  const [adapterModule, sessionModule, registryModule, iiifModule] = await Promise.all([
    import("../../mirador-scribe/src/annotationAdapter/ScribeAnnotationAdapter"),
    import("../../mirador-scribe/src/editor/session"),
    import("../src/pages/editor/canvas-image-registry"),
    import("../../mirador-scribe/src/utils/iiif"),
  ]);
  ScribeAnnotationAdapter = adapterModule.default;
  ({ createCanvasImageRegistry } = registryModule);
  ({ annotationCanvasId, createDraftLineAnnotation, updateAnnotationText } = iiifModule);
  ({
    acceptSavedSession,
    createEditorSession,
    editSession,
    rebaseSession,
    sessionIsDirty,
  } = sessionModule);
}

async function initializeGeometryModules() {
  const [openSeadragonModule, geometryModule] = await Promise.all([
    import("openseadragon"),
    import("../../mirador-scribe/src/editor/geometry"),
  ]);
  OpenSeadragon = openSeadragonModule.default;
  ({
    clientPointToImage,
    imagePointToViewerElement,
    normalizeImageBBox,
    viewerElementPointToImage,
  } = geometryModule);
}

async function initializePlugin(structural = false) {
  const [miradorModule, pluginModule, adapterModule, registryModule] = await Promise.all([
    import("mirador"),
    import("../../mirador-scribe/src/index"),
    import("../../mirador-scribe/src/annotationAdapter/ScribeAnnotationAdapter"),
    import("../src/pages/editor/canvas-image-registry"),
  ]);
  const Mirador = miradorModule.default;
  pluginSetCanvas = miradorModule.setCanvas;
  const PluginAdapter = adapterModule.default;
  const canvasA = `${window.location.origin}/e2e/canvas/a`;
  const canvasB = `${window.location.origin}/e2e/canvas/b`;
  const registry = registryModule.createCanvasImageRegistry([
    { canvasUri: canvasA, id: 1001n },
    { canvasUri: canvasB, id: 2002n },
  ]);
  pluginStructuralCalls = { joinLineIds: [], joinWordIds: [], splitAtWord: 0 };
  pluginTranscriptionCalls.length = 0;
  pluginState = new Map([
    ["1001", {
      page: page([{
        ...annotation("https://scribe.test/presentation/v3/item-image-1001/canvas/page-1/annotations/items/00000000000000000000000000000001", "page A original"),
        target: `${canvasA}#xywh=pixel:100,100,500,80`,
      }], "Page A"),
      revision: "11",
    }],
    ["2002", structural ? {
      page: structuralPluginPage(canvasB),
      revision: "21",
    } : {
      page: {
        ...page([{
          ...annotation("https://scribe.test/presentation/v3/item-image-2002/canvas/page-1/annotations/items/00000000000000000000000000000002", "page B original"),
          target: {
            type: "SpecificResource",
            source: { id: canvasB, type: "Canvas" },
            selector: [
              {
                type: "FragmentSelector",
                value: "t=2,4&track=ocr",
                extension: { retained: "nonspatial" },
              },
              {
                type: "FragmentSelector",
                conformsTo: "http://www.w3.org/TR/media-frags/",
                value: "xywh=pixel:120,160,500,80",
                extension: { retained: "spatial" },
              },
            ],
          },
        }], "Page B"),
        id: "https://scribe.test/presentation/v3/item-image-2002/canvas/page-1/annotations",
      },
      revision: "21",
    }],
  ]);
  pluginState.get("1001").page.id = "https://scribe.test/presentation/v3/item-image-1001/canvas/page-1/annotations";
  const client = {
    async enrichAnnotation(itemImageId, scope, annotationJson, contextId) {
      const submitted = parseIIIFJSON(annotationJson);
      pluginTranscriptionCalls.push({
        annotationId: submitted?.id || "",
        contextId: String(contextId),
        itemImageId: String(itemImageId),
        scope,
      });
      if (scope !== "line") throw new Error(`unexpected browser transcription scope ${scope}`);
      return withAnnotationValue(submitted, `retranscribed ${annotationValue(submitted)}`);
    },
    async getAnnotationPage(itemImageId) {
      pluginLoadItemImageIds.push(String(itemImageId));
      const snapshot = pluginState.get(String(itemImageId));
      if (!snapshot) throw new Error(`missing plugin fixture ${itemImageId}`);
      return structuredClone(snapshot);
    },
    async saveAnnotationPage(itemImageId, annotationPageJson, expectedRevision) {
      const key = String(itemImageId);
      pluginSaveItemImageIds.push(key);
      const snapshot = pluginState.get(key);
      if (!snapshot || snapshot.revision !== String(expectedRevision)) {
        const error = new Error("revision conflict");
        error.name = "RevisionConflict";
        throw error;
      }
      const next = {
        page: parseIIIFJSON(annotationPageJson),
        revision: (BigInt(snapshot.revision) + 1n).toString(),
      };
      pluginState.set(key, next);
      return structuredClone(next);
    },
    async splitLineIntoTwoLines(_itemImageId, annotationPageJson, selectedAnnotationId, splitAtWord) {
      pluginStructuralCalls.splitAtWord = Number(splitAtWord || 0);
      if (structural) {
        return splitPluginLine(parseIIIFJSON(annotationPageJson), selectedAnnotationId, splitAtWord);
      }
      if (pluginPendingSplit) throw new Error("a split is already pending");
      const submittedPage = parseIIIFJSON(annotationPageJson);
      return new Promise((resolve) => {
        pluginPendingSplit = () => {
          pluginPendingSplit = null;
          resolve(structuredClone(submittedPage));
        };
      });
    },
    async joinLines(_itemImageId, annotationPageJson, selectedAnnotationIds) {
      pluginStructuralCalls.joinLineIds = [...selectedAnnotationIds];
      return joinPluginAnnotations(
        parseIIIFJSON(annotationPageJson),
        selectedAnnotationIds,
        "line",
      );
    },
    async joinWordsIntoLine(_itemImageId, annotationPageJson, selectedAnnotationIds) {
      pluginStructuralCalls.joinWordIds = [...selectedAnnotationIds];
      return joinPluginAnnotations(
        parseIIIFJSON(annotationPageJson),
        selectedAnnotationIds,
        "line",
      );
    },
  };
  const viewerOptions = commonViewerOptions(
    window.location.origin,
    PluginAdapter,
    client,
    (canvasId) => ({
      contextId: "1",
      itemImageId: registry.itemImageIdForCanvas(canvasId),
      windowId: "plugin-window",
    }),
    {
      ajaxWithCredentials: false,
      animationTime: 0,
      blendTime: 0,
      crossOriginPolicy: "Anonymous",
    },
  );
  document.addEventListener("scribe:active-canvas", (event) => {
    pluginActiveCanvasEvents.push(structuredClone(event.detail));
  });
  document.addEventListener("scribe:editor-state", (event) => {
    if (event.detail?.windowId === "plugin-window") {
      pluginLastEditorState = structuredClone(event.detail);
    }
  });
  document.addEventListener("scribe:request-publish", (event) => {
    document.dispatchEvent(new CustomEvent("scribe:publish-result", {
      detail: {
        canvasId: event.detail.canvasId,
        ok: true,
        publishedRevision: event.detail.expectedRevision,
        requestId: event.detail.requestId,
        windowId: event.detail.windowId,
      },
    }));
  });
  const responsive = new URLSearchParams(window.location.search).get("responsive") === "1";
  const viewerId = responsive ? "mirador-viewer" : "plugin-mirador";
  if (responsive) {
    status.hidden = true;
    renderEditorLayout(app);
  } else {
    app.style.height = "900px";
    app.style.width = "1200px";
    app.id = viewerId;
  }
  pluginStore = Mirador.viewer({
    ...viewerOptions,
    id: viewerId,
    windows: [{
      canvasId: canvasA,
      id: "plugin-window",
      manifestId: `${window.location.origin}/e2e/two-canvas-manifest.json`,
    }],
    window: {
      allowClose: false,
      allowFullscreen: false,
      allowMaximize: false,
      allowTopMenuButton: false,
      hideWindowTitle: true,
    },
    workspaceControlPanel: { enabled: false },
  }, [...pluginModule.default]);

  const deadline = Date.now() + 60_000;
  while (Date.now() < deadline) {
    if (pluginLoadItemImageIds.includes("1001") && document.body.textContent.includes("View and modes")) return;
    await new Promise((resolve) => setTimeout(resolve, 50));
  }
  throw new Error("mounted Mirador/Scribe plugin did not become ready");
}

function annotation(id, value) {
  return {
    id,
    type: "Annotation",
    motivation: "supplementing",
    textGranularity: "line",
    body: [{ type: "TextualBody", purpose: "supplementing", value }],
  };
}

function page(items, label = "Base label") {
  return {
    id: "https://scribe.test/presentation/v3/item-image-1/canvas/page-1/annotations",
    type: "AnnotationPage",
    label,
    items,
  };
}

function structuralPluginPage(canvasId) {
  const pageId = "https://scribe.test/presentation/v3/item-image-2002/canvas/page-1/annotations";
  const located = (hexId, value, granularity, x, y, width, height) => ({
    ...annotation(`${pageId}/items/${hexId}`, value),
    target: `${canvasId}#xywh=pixel:${x},${y},${width},${height}`,
    textGranularity: granularity,
  });
  return {
    ...page([
      located("00000000000000000000000000000001", "alpha beta gamma delta", "line", 100, 80, 520, 80),
      located("00000000000000000000000000000002", "second line", "line", 100, 210, 420, 50),
      located("00000000000000000000000000000011", "red", "word", 100, 320, 90, 45),
      located("00000000000000000000000000000012", "green", "word", 205, 320, 110, 45),
      located("00000000000000000000000000000013", "blue", "word", 330, 320, 100, 45),
      located("00000000000000000000000000000014", "gold", "word", 445, 320, 95, 45),
      located("00000000000000000000000000000004", "fourth line", "line", 100, 420, 420, 50),
      located("00000000000000000000000000000005", "fifth line", "line", 100, 520, 420, 50),
    ], "Structural edits"),
    id: pageId,
  };
}

function annotationValue(annotationValue) {
  const bodies = Array.isArray(annotationValue?.body) ? annotationValue.body : [annotationValue?.body];
  return bodies.find((body) => body?.type === "TextualBody")?.value || "";
}

function withAnnotationValue(annotationValue, value) {
  const next = structuredClone(annotationValue);
  const bodies = Array.isArray(next.body) ? next.body : [next.body];
  const body = bodies.find((candidate) => candidate?.type === "TextualBody");
  if (!body) throw new Error(`annotation ${next.id} has no TextualBody`);
  body.value = value;
  next.body = bodies;
  return next;
}

function annotationTargetBox(annotationValue) {
  const target = String(annotationValue?.target || "");
  const match = target.match(/xywh=(?:pixel:)?([0-9]+),([0-9]+),([0-9]+),([0-9]+)/);
  if (!match) throw new Error(`annotation ${annotationValue?.id} has no fixture geometry`);
  return {
    h: Number(match[4]),
    w: Number(match[3]),
    x: Number(match[1]),
    y: Number(match[2]),
  };
}

function withAnnotationTargetBox(annotationValue, box) {
  const next = structuredClone(annotationValue);
  const canvasId = String(next.target).split("#", 1)[0];
  next.target = `${canvasId}#xywh=pixel:${box.x},${box.y},${box.w},${box.h}`;
  return next;
}

function splitPluginLine(pageValue, selectedAnnotationId, requestedSplit) {
  const next = structuredClone(pageValue);
  const index = next.items.findIndex(({ id }) => id === selectedAnnotationId);
  if (index < 0) throw new Error("selected split line is missing");
  const selected = next.items[index];
  const tokens = annotationValue(selected).trim().split(/\s+/).filter(Boolean);
  const splitAt = Number(requestedSplit);
  if (!Number.isInteger(splitAt) || splitAt < 1 || splitAt >= tokens.length) {
    throw new Error("invalid split boundary");
  }
  const box = annotationTargetBox(selected);
  const firstHeight = Math.floor(box.h / 2);
  const baseId = next.id;
  const first = withAnnotationTargetBox(
    withAnnotationValue(selected, tokens.slice(0, splitAt).join(" ")),
    { ...box, h: firstHeight },
  );
  first.id = `${baseId}/items/00000000000000000000000000000091`;
  const second = withAnnotationTargetBox(
    withAnnotationValue(selected, tokens.slice(splitAt).join(" ")),
    { ...box, h: box.h - firstHeight, y: box.y + firstHeight },
  );
  second.id = `${baseId}/items/00000000000000000000000000000092`;
  next.items.splice(index, 1, first, second);
  return next;
}

function joinPluginAnnotations(pageValue, selectedAnnotationIds, granularity) {
  const next = structuredClone(pageValue);
  const selectedIdSet = new Set(selectedAnnotationIds);
  const selected = next.items.filter(({ id }) => selectedIdSet.has(id));
  if (selected.length < 2) throw new Error("at least two annotations are required");
  const selectedIndexes = selected.map(({ id }) => next.items.findIndex((item) => item.id === id));
  const boxes = selected.map(annotationTargetBox);
  const left = Math.min(...boxes.map(({ x }) => x));
  const top = Math.min(...boxes.map(({ y }) => y));
  const right = Math.max(...boxes.map(({ x, w }) => x + w));
  const bottom = Math.max(...boxes.map(({ y, h }) => y + h));
  let merged = withAnnotationValue(selected[0], selected.map(annotationValue).join(" "));
  merged = withAnnotationTargetBox(merged, { h: bottom - top, w: right - left, x: left, y: top });
  merged.textGranularity = granularity;
  const insertAt = Math.min(...selectedIndexes);
  next.items = next.items.filter(({ id }) => !selectedIdSet.has(id));
  next.items.splice(insertAt, 0, merged);
  return next;
}

function textFor(pageValue, id) {
  return pageValue.items.find((item) => item.id === id)?.body?.[0]?.value || "";
}

function editedPage(pageValue, id, text) {
  const next = structuredClone(pageValue);
  const item = next.items.find((candidate) => candidate.id === id);
  if (!item) throw new Error(`missing annotation ${id}`);
  item.body[0].value = text;
  return next;
}

function runBackgroundRebaseScenario() {
  const base = page([
    annotation("line-1", "original one"),
    annotation("line-2", "original two"),
  ]);
  let session = createEditorSession(base, "7");
  session = editSession(session, editedPage(session.draftPage, "line-1", "local draft"));

  const remote = page([
    annotation("line-1", "worker result"),
    annotation("line-2", "remote untouched update"),
  ], "Remote metadata");
  session = rebaseSession(session, remote, "8");

  return {
    baseLineOne: textFor(session.basePage, "line-1"),
    dirty: sessionIsDirty(session),
    draftLineOne: textFor(session.draftPage, "line-1"),
    draftLineTwo: textFor(session.draftPage, "line-2"),
    label: session.draftPage.label,
    pendingRemoteIds: session.pendingRemoteIds,
    revision: session.revision,
  };
}

async function runPersistenceScenario() {
  const useBackend = import.meta.env.VITE_SCRIBE_BROWSER_BACKEND === "true";
  const itemImageId = import.meta.env.VITE_SCRIBE_BROWSER_ITEM_IMAGE_ID || "1";
  const memoryCanvasId = "https://source.test/canvas/1";
  const memoryPageId = "https://scribe.test/presentation/v3/item-image-1/canvas/page-1/annotations";
  const memoryLineId = `${memoryPageId}/items/00000000000000000000000000000001`;
  let canonicalPage = {
    ...page([{
      ...annotation(memoryLineId, "server base"),
      "ex:largeInteger": new String("9007199254740993"),
      "ex:preciseDecimal": new String("0.123456789012345678901"),
      target: `${memoryCanvasId}#xywh=pixel:10,10,300,40`,
    }]),
    "@context": [
      "http://iiif.io/api/extension/text-granularity/context.json",
      "https://example.org/scribe-browser-extension/context.json",
      "http://iiif.io/api/presentation/3/context.json",
    ],
    "ex:pageCounter": new String("9007199254740995"),
  };
  let canonicalRevision = "10";
  let structuralCalls = 0;
  const memoryClient = {
    async getAnnotationPage() {
      return {
        page: structuredClone(canonicalPage),
        revision: canonicalRevision,
        updatedAt: "2026-07-20T00:00:00Z",
      };
    },
    async saveAnnotationPage(_itemImageId, annotationPageJson, expectedRevision) {
      if (String(expectedRevision) !== canonicalRevision) {
        const error = new Error(`revision conflict: expected ${expectedRevision}, current ${canonicalRevision}`);
        error.name = "RevisionConflict";
        throw error;
      }
      canonicalPage = parseIIIFJSON(annotationPageJson);
      canonicalRevision = (BigInt(canonicalRevision) + 1n).toString();
      return {
        page: structuredClone(canonicalPage),
        revision: canonicalRevision,
        updatedAt: "2026-07-20T00:00:01Z",
      };
    },
    async splitLineIntoWords(_itemImageId, annotationPageJson, selectedAnnotationId, words) {
      structuralCalls += 1;
      const draftPage = parseIIIFJSON(annotationPageJson);
      const source = draftPage.items.find(({ id }) => id === selectedAnnotationId);
      if (!source) throw new Error(`selected annotation ${selectedAnnotationId} was not found`);
      return {
        ...draftPage,
        items: draftPage.items.flatMap((item) => (
          item.id !== selectedAnnotationId
            ? [item]
            : words.map((word, index) => ({
              ...structuredClone(source),
              id: `${draftPage.id}/items/${(index + 101).toString(16).padStart(32, "0")}`,
              textGranularity: "word",
              body: [{
                type: "TextualBody",
                purpose: "supplementing",
                format: "text/plain",
                value: word,
              }],
            }))
        )),
      };
    },
  };
  const persistenceClient = useBackend ? annotationClient : memoryClient;

  const adapter = () => new ScribeAnnotationAdapter(
    "https://scribe.test",
    3,
    "https://source.test/canvas/1",
    "Browser test",
    { client: persistenceClient, contextId: "1", itemImageId, windowId: "session-window" },
  );
  const editorA = adapter();
  const editorB = adapter();
  const snapshotA = await editorA.loadSnapshot();
  const snapshotB = await editorB.loadSnapshot();
  const primaryAnnotationId = snapshotA.page.items[0]?.id;
  const sourceCanvasId = annotationCanvasId(snapshotA.page.items[0]);
  if (!primaryAnnotationId || !sourceCanvasId) {
    throw new Error("persistence fixture must contain one targeted annotation");
  }

  const drawnLine = updateAnnotationText(createDraftLineAnnotation(
    sourceCanvasId,
    { x: 25, y: 75, w: 240, h: 36 },
    snapshotA.page.id,
  ), "newly drawn words");
  const structuralSourceWasUnsaved = !snapshotA.page.items.some(({ id }) => id === drawnLine.id);
  const structuralDraftPage = {
    ...structuredClone(snapshotA.page),
    items: [...structuredClone(snapshotA.page.items), drawnLine],
  };
  const splitPage = await editorA.splitLineIntoWords(
    structuralDraftPage,
    drawnLine.id,
    ["newly", "drawn", "words"],
  );
  if (useBackend) structuralCalls += 1;
  const splitItems = Array.isArray(splitPage?.items) ? splitPage.items : [];
  const splitWordItems = splitItems.filter(({ textGranularity }) => textGranularity === "word");

  let sessionA = createEditorSession(snapshotA.page, snapshotA.revision);
  let sessionB = createEditorSession(snapshotB.page, snapshotB.revision);

  sessionA = editSession(sessionA, editedPage(sessionA.draftPage, primaryAnnotationId, "saved by editor A"));
  const submittedA = structuredClone(sessionA.draftPage);
  const savedA = await editorA.savePage(submittedA, sessionA.revision);
  sessionA = acceptSavedSession(sessionA, savedA.page, savedA.revision, submittedA, snapshotA.revision);

  const reloadedA = await editorA.loadSnapshot();
  const reloadSession = createEditorSession(reloadedA.page, reloadedA.revision);
  const reloadedPrimary = reloadedA.page.items.find(({ id }) => id === primaryAnnotationId);

  sessionB = editSession(sessionB, editedPage(sessionB.draftPage, primaryAnnotationId, "unsaved editor B draft"));
  let conflictName = "";
  let conflictMessage = "";
  try {
    await editorB.savePage(sessionB.draftPage, sessionB.revision);
  } catch (error) {
    conflictName = error instanceof Error ? error.name : "unknown";
    conflictMessage = error instanceof Error ? error.message : String(error);
  }

  const latest = await editorB.loadSnapshot();
  sessionB = rebaseSession(sessionB, latest.page, latest.revision);

  return {
    backend: useBackend ? "connect-mariadb" : "memory",
    conflictMessage,
    conflictName,
    conflictedDraftDirty: sessionIsDirty(sessionB),
    conflictedDraftText: textFor(sessionB.draftPage, primaryAnnotationId),
    primaryAnnotationId,
    pendingRemoteIds: sessionB.pendingRemoteIds,
    reloadDirty: sessionIsDirty(reloadSession),
    reloadLargeInteger: reloadedPrimary?.["ex:largeInteger"]?.valueOf?.() || "",
    reloadPageCounter: reloadedA.page["ex:pageCounter"]?.valueOf?.() || "",
    reloadPreciseDecimal: reloadedPrimary?.["ex:preciseDecimal"]?.valueOf?.() || "",
    reloadRevision: reloadSession.revision,
    reloadText: textFor(reloadSession.draftPage, primaryAnnotationId),
    saveDirty: sessionIsDirty(sessionA),
    saveRevision: sessionA.revision,
    initialRevision: snapshotA.revision,
    structuralCalls,
    structuralResultCanonical: splitWordItems.length === 3 && splitWordItems.every(({ id }) => (
      typeof id === "string"
      && id.startsWith(`${snapshotA.page.id}/items/`)
      && /^[0-9a-f]{32}$/.test(id.slice(`${snapshotA.page.id}/items/`.length))
    )),
    structuralResultCount: splitWordItems.length,
    structuralResultPageId: splitPage?.id || "",
    structuralResultPreservedExisting: splitItems.some(({ id }) => id === primaryAnnotationId),
    structuralSourceCanonical: drawnLine.id.startsWith(`${snapshotA.page.id}/items/`)
      && /^[0-9a-f]{32}$/.test(drawnLine.id.slice(`${snapshotA.page.id}/items/`.length)),
    structuralSourceWasUnsaved,
  };
}

async function runMultiCanvasPersistenceScenario() {
  const canvasA = "https://source.test/manifest/canvas/a";
  const canvasB = "https://source.test/manifest/canvas/b";
  const imageA = "1001";
  const imageB = "2002";
  const registry = createCanvasImageRegistry([
    { canvasUri: canvasA, id: BigInt(imageA) },
    { canvasUri: canvasB, id: BigInt(imageB) },
  ]);
  const state = new Map([
    [imageA, {
      page: {
        id: "https://scribe.test/presentation/v3/item-image-1001/canvas/page-1/annotations",
        type: "AnnotationPage",
        items: [{
          ...annotation("line-a", "page A original"),
          target: `${canvasA}#xywh=10,10,100,20`,
        }],
      },
      revision: "11",
    }],
    [imageB, {
      page: {
        id: "https://scribe.test/presentation/v3/item-image-2002/canvas/page-1/annotations",
        type: "AnnotationPage",
        items: [{
          ...annotation("line-b", "page B original"),
          target: `${canvasB}#xywh=20,20,200,30`,
        }],
      },
      revision: "21",
    }],
  ]);
  const loadItemImageIds = [];
  const saveItemImageIds = [];
  const client = {
    async getAnnotationPage(itemImageId) {
      loadItemImageIds.push(String(itemImageId));
      const snapshot = state.get(String(itemImageId));
      if (!snapshot) throw new Error(`unknown item image ${itemImageId}`);
      return structuredClone(snapshot);
    },
    async saveAnnotationPage(itemImageId, annotationPageJson, expectedRevision) {
      const key = String(itemImageId);
      saveItemImageIds.push(key);
      const snapshot = state.get(key);
      if (!snapshot) throw new Error(`unknown item image ${itemImageId}`);
      if (String(expectedRevision) !== snapshot.revision) throw new Error("revision conflict");
      const next = {
        page: parseIIIFJSON(annotationPageJson),
        revision: (BigInt(snapshot.revision) + 1n).toString(),
      };
      state.set(key, next);
      return structuredClone(next);
    },
  };
  const adapterForCanvas = (canvasId) => new ScribeAnnotationAdapter(
    "https://scribe.test",
    3,
    canvasId,
    "Browser test",
    {
      client,
      contextId: "1",
      itemImageId: registry.itemImageIdForCanvas(canvasId),
      windowId: "session-window",
    },
  );

  const snapshotA = await adapterForCanvas(canvasA).loadSnapshot();
  const adapterB = adapterForCanvas(canvasB);
  const snapshotB = await adapterB.loadSnapshot();
  const changedB = editedPage(snapshotB.page, "line-b", "page B corrected");
  const savedB = await adapterB.savePage(changedB, snapshotB.revision);

  return {
    initialPageAText: textFor(snapshotA.page, "line-a"),
    initialPageBText: textFor(snapshotB.page, "line-b"),
    loadItemImageIds,
    pageARevision: state.get(imageA).revision,
    pageAText: textFor(state.get(imageA).page, "line-a"),
    pageBRevision: state.get(imageB).revision,
    pageBTarget: state.get(imageB).page.items[0].target,
    pageBText: textFor(state.get(imageB).page, "line-b"),
    reprocessItemImageId: registry.itemImageIdForCanvas(canvasB),
    saveItemImageIds,
    savedPageId: savedB.page.id,
  };
}

let geometryViewer = null;

async function initializeGeometry() {
  const stage = document.getElementById("geometry-stage");
  const element = document.getElementById("geometry-viewer");
  stage.hidden = false;
  document.body.style.minHeight = "1500px";
  element.style.position = "absolute";
  element.style.left = "137px";
  element.style.top = "480px";
  element.style.width = "720px";
  element.style.height = "420px";
  element.style.border = "1px solid black";

  geometryViewer = OpenSeadragon({
    animationTime: 0,
    blendTime: 0,
    element,
    immediateRender: true,
    showNavigationControl: false,
  });
  const opened = new Promise((resolve, reject) => {
    geometryViewer.addOnceHandler("open", resolve);
    geometryViewer.addOnceHandler("open-failed", (event) => reject(new Error(event?.message || "image open failed")));
  });
  const svg = `<svg xmlns="http://www.w3.org/2000/svg" width="1200" height="600"><rect width="1200" height="600" fill="white"/><path d="M0 0L1200 600M1200 0L0 600" stroke="black"/></svg>`;
  geometryViewer.open({
    type: "image",
    url: `data:image/svg+xml;charset=utf-8,${encodeURIComponent(svg)}`,
  });
  await opened;

  const homeZoom = geometryViewer.viewport.getHomeZoom();
  geometryViewer.viewport.zoomTo(homeZoom * 2, undefined, true);
  geometryViewer.viewport.panTo(
    geometryViewer.viewport.imageToViewportCoordinates(new OpenSeadragon.Point(620, 310)),
    true,
  );
  geometryViewer.viewport.applyConstraints(true);
  window.scrollTo(0, 240);
  await new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)));
}

function geometryRoundTrip(x, y) {
  if (!geometryViewer) throw new Error("geometry viewer is not ready");
  const element = document.getElementById("geometry-viewer");
  const relative = imagePointToViewerElement(geometryViewer, x, y);
  const rect = element.getBoundingClientRect();
  const fromClient = clientPointToImage(
    geometryViewer,
    rect.left + relative.x,
    rect.top + relative.y,
  );
  const fromElement = viewerElementPointToImage(geometryViewer, relative);
  return {
    fromClient: { x: fromClient.x, y: fromClient.y },
    fromElement: { x: fromElement.x, y: fromElement.y },
    homeZoom: geometryViewer.viewport.getHomeZoom(),
    normalizedCrossedBox: normalizeImageBBox({ x: 12, y: -4, w: -20, h: 0 }),
    rect: { left: rect.left, top: rect.top },
    relative: { x: relative.x, y: relative.y },
    scroll: { x: window.scrollX, y: window.scrollY },
    zoom: geometryViewer.viewport.getZoom(),
  };
}

function pluginSnapshot() {
  const state = pluginStore?.store?.getState?.() || pluginStore?.getState?.();
  const activeCanvasId = state?.windows?.["plugin-window"]?.canvasId
    || state?.windows?.["plugin-window"]?.canvasIds?.[0]
    || "";
  const fixture = (itemImageId) => {
    const snapshot = pluginState?.get(itemImageId);
    return {
      count: snapshot?.page?.items?.length || 0,
      revision: snapshot?.revision || "",
      target: structuredClone(snapshot?.page?.items?.[0]?.target || null),
      text: snapshot?.page?.items?.[0]?.body?.[0]?.value || "",
    };
  };
  const draftItemsB = activeCanvasId.endsWith("/canvas/b")
    ? pluginLastEditorState?.annotationPage?.items || []
    : [];
  const draftPageB = draftItemsB[0] || null;
  const selectedDraft = draftItemsB.find((annotation) => (
    annotation?.id === pluginLastEditorState?.selectedAnnotationId
  )) || null;
  return {
    activeCanvasEvents: structuredClone(pluginActiveCanvasEvents),
    activeCanvasId,
    loadItemImageIds: [...pluginLoadItemImageIds],
    saveItemImageIds: [...pluginSaveItemImageIds],
    isBusy: Boolean(pluginLastEditorState?.isBusy),
    overlayMode: pluginLastEditorState?.overlayMode || "",
    selectedAnnotationId: pluginLastEditorState?.selectedAnnotationId || "",
    selectedDraftTarget: structuredClone(selectedDraft?.target || null),
    statusMessage: pluginLastEditorState?.statusMessage || "",
    transcriptionCalls: structuredClone(pluginTranscriptionCalls),
    splitPending: Boolean(pluginPendingSplit),
    structural: {
      calls: structuredClone(pluginStructuralCalls),
      draft: draftItemsB.map((draftAnnotation) => ({
        granularity: draftAnnotation.textGranularity || "line",
        id: draftAnnotation.id || "",
        text: annotationValue(draftAnnotation),
      })),
    },
    sessionStatus: pluginLastEditorState?.sessionStatus || "",
    pageA: fixture("1001"),
    pageB: {
      ...fixture("2002"),
      draftCount: draftItemsB.length,
      draftTarget: structuredClone(draftPageB?.target || null),
    },
  };
}

function finishPluginSplit() {
  if (!pluginPendingSplit) throw new Error("no plugin split is pending");
  pluginPendingSplit();
}

function turnPluginCanvas(canvasId) {
  if (!pluginStore || !pluginSetCanvas) throw new Error("plugin viewer is not ready");
  const store = pluginStore.store || pluginStore;
  if (typeof store.dispatch !== "function") throw new Error("plugin Redux store is unavailable");
  store.dispatch(pluginSetCanvas("plugin-window", canvasId));
}

globalThis.__scribeBrowserHarness = {
  backgroundSnapshot,
  finishPluginSplit,
  geometryRoundTrip,
  pluginSnapshot,
  runBackgroundRebaseScenario,
  runMultiCanvasPersistenceScenario,
  runPersistenceScenario,
  triggerBackgroundCompletion,
  turnPluginCanvas,
};

try {
  if (mode === "dialog") {
    const [{ renderEditorLayout }, { createLeaveDialogController }] = await Promise.all([
      import("../src/pages/editor/layout"),
      import("../src/pages/editor/leave-dialog"),
    ]);
    renderEditorLayout(app);
    const dialog = document.getElementById("leave-dialog");
    const cancel = document.getElementById("leave-cancel");
    const discard = document.getElementById("leave-discard");
    const save = document.getElementById("leave-save");
    const home = document.getElementById("home-nav");
    if (!(dialog instanceof HTMLElement)
      || !(cancel instanceof HTMLButtonElement)
      || !(discard instanceof HTMLButtonElement)
      || !(save instanceof HTMLButtonElement)
      || !(home instanceof HTMLButtonElement)) {
      throw new Error("leave dialog layout is incomplete");
    }
    let dirty = false;
    const controller = createLeaveDialogController({
      cancel,
      dialog,
      discard,
      onDiscard: () => controller.close(),
      onSave: async () => controller.close(),
      save,
    });
    document.addEventListener("scribe:dirty-state", (event) => {
      dirty = Boolean(event.detail?.dirty);
    });
    home.addEventListener("click", () => {
      if (dirty) controller.open();
    });
  } else if (mode === "geometry") {
    await initializeGeometryModules();
    await initializeGeometry();
  } else if (mode === "session") {
    await initializeSessionModules();
  } else if (mode === "plugin") {
    await initializePlugin();
  } else if (mode === "structural") {
    await initializePlugin(true);
  } else if (mode === "background") {
    await initializeBackgroundDelivery();
  } else {
    throw new Error(`unknown harness mode: ${mode}`);
  }
  document.documentElement.dataset.harnessReady = "true";
  status.value = "ready";
  status.textContent = "ready";
} catch (error) {
  document.documentElement.dataset.harnessError = error instanceof Error ? error.message : String(error);
  status.value = "failed";
  status.textContent = "failed";
  throw error;
}
