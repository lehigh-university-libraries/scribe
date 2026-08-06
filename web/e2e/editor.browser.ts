import { expect, test } from "@playwright/test";

type BrowserHarness = {
  backgroundSnapshot(): {
    annotationId: string;
    annotationText: string;
    dirty: boolean;
    pendingRemoteIds: string[];
    reloadCount: number;
    sessionStatus: string;
  };
  finishPluginSplit(): void;
  geometryRoundTrip(x: number, y: number): {
    fromClient: { x: number; y: number };
    fromElement: { x: number; y: number };
    homeZoom: number;
    normalizedCrossedBox: { x: number; y: number; w: number; h: number };
    rect: { left: number; top: number };
    scroll: { x: number; y: number };
    zoom: number;
  };
  runBackgroundRebaseScenario(): {
    baseLineOne: string;
    dirty: boolean;
    draftLineOne: string;
    draftLineTwo: string;
    label: string;
    pendingRemoteIds: string[];
    revision: string;
  };
  runMultiCanvasPersistenceScenario(): Promise<{
    initialPageAText: string;
    initialPageBText: string;
    loadItemImageIds: string[];
    pageARevision: string;
    pageAText: string;
    pageBRevision: string;
    pageBTarget: string;
    pageBText: string;
    reprocessItemImageId: string;
    saveItemImageIds: string[];
    savedPageId: string;
  }>;
  runPersistenceScenario(): Promise<{
    backend: "connect-mariadb" | "memory";
    conflictMessage: string;
    conflictName: string;
    conflictedDraftDirty: boolean;
    conflictedDraftText: string;
    primaryAnnotationId: string;
    pendingRemoteIds: string[];
    reloadDirty: boolean;
    reloadLargeInteger: string;
    reloadPageCounter: string;
    reloadPreciseDecimal: string;
    reloadRevision: string;
    reloadText: string;
    saveDirty: boolean;
    saveRevision: string;
    initialRevision: string;
    structuralCalls: number;
    structuralResultCanonical: boolean;
    structuralResultCount: number;
    structuralResultPageId: string;
    structuralResultPreservedExisting: boolean;
    structuralSourceCanonical: boolean;
    structuralSourceWasUnsaved: boolean;
  }>;
  triggerBackgroundCompletion(): Promise<{
    itemImageId: string;
    remoteText: string;
    revision: string;
  }>;
  pluginSnapshot(): {
    activeCanvasEvents: Array<{ canvasId: string; itemImageId: string; windowId: string }>;
    activeCanvasId: string;
    loadItemImageIds: string[];
    saveItemImageIds: string[];
    isBusy: boolean;
    overlayMode: string;
    selectedAnnotationId: string;
    selectedDraftTarget: unknown;
    statusMessage: string;
    splitPending: boolean;
    structural: {
      calls: {
        joinLineIds: string[];
        joinWordIds: string[];
        splitAtWord: number;
      };
      draft: Array<{ granularity: string; id: string; text: string }>;
    };
    sessionStatus: string;
    pageA: { count: number; revision: string; target: unknown; text: string };
    pageB: {
      count: number;
      draftCount: number;
      draftTarget: unknown;
      revision: string;
      target: unknown;
      text: string;
    };
  };
  turnPluginCanvas(canvasId: string): void;
};

declare global {
  interface Window {
    __scribeBrowserHarness: BrowserHarness;
  }
}

async function waitForHarness(page: import("@playwright/test").Page, timeout = 60_000) {
  const state = await page.waitForFunction(() => {
    const { harnessError, harnessReady } = document.documentElement.dataset;
    if (!harnessError && harnessReady !== "true") return null;
    return { error: harnessError || "", ready: harnessReady === "true" };
  }, undefined, { timeout }).then((handle) => handle.jsonValue());

  if (!state) throw new Error("browser harness did not report an initialization state");
  expect(state.error, `browser harness failed to initialize: ${state.error}`).toBe("");
  expect(state.ready).toBe(true);
}

test("a raw editor deep link opens the requested item without prior navigation", async ({ page }) => {
  const itemImageId = process.env.VITE_SCRIBE_BROWSER_ITEM_IMAGE_ID ?? "";
  test.skip(!/^[1-9][0-9]*$/.test(itemImageId), "requires the browser Connect fixture");

  await page.goto(`/editor?itemImageId=${itemImageId}`);

  await expect(page).toHaveURL(new RegExp(`/editor\\?itemImageId=${itemImageId}(?:&|$)`));
  await expect(page.locator("#editor-meta")).toContainText(
    `image ${itemImageId}`,
    { timeout: 60_000 },
  );
  await expect(page.locator("#mirador-viewer")).toContainText("View and modes", { timeout: 60_000 });
});

test("dirty-editor leave dialog traps keyboard focus and restores its trigger", async ({ page }) => {
  await page.goto("/e2e/harness.html?mode=dialog");
  await waitForHarness(page);

  const home = page.getByRole("button", { name: "Home" });
  await home.focus();
  await page.evaluate(() => {
    document.dispatchEvent(new CustomEvent("scribe:dirty-state", {
      detail: { dirty: true, windowId: "browser-window" },
    }));
  });
  await home.click();

  const dialog = page.getByRole("dialog", { name: "Leave editor?" });
  const cancel = page.getByRole("button", { name: "Cancel" });
  const discard = page.getByRole("button", { name: "Discard" });
  const save = page.getByRole("button", { name: "Save" });
  await expect(dialog).toBeVisible();
  await expect(cancel).toBeFocused();

  await page.keyboard.press("Tab");
  await expect(discard).toBeFocused();
  await page.keyboard.press("Tab");
  await expect(save).toBeFocused();
  await page.keyboard.press("Tab");
  await expect(cancel).toBeFocused();
  await page.keyboard.press("Shift+Tab");
  await expect(save).toBeFocused();

  await page.keyboard.press("Escape");
  await expect(dialog).toBeHidden();
  await expect(home).toBeFocused();
});

test("production OpenSeadragon geometry round-trips offsets, scrolling, pan, and zoom", async ({ page }) => {
  await page.goto("/e2e/harness.html?mode=geometry");
  await waitForHarness(page);

  const result = await page.evaluate(() => window.__scribeBrowserHarness.geometryRoundTrip(620, 310));
  expect(result.rect.left).toBeGreaterThan(100);
  expect(result.rect.top).not.toBe(0);
  expect(result.scroll.y).toBeGreaterThan(0);
  expect(result.zoom).toBeGreaterThan(result.homeZoom);
  expect(result.fromClient.x).toBeCloseTo(620, 3);
  expect(result.fromClient.y).toBeCloseTo(310, 3);
  expect(result.fromElement.x).toBeCloseTo(620, 3);
  expect(result.fromElement.y).toBeCloseTo(310, 3);
  expect(result.normalizedCrossedBox).toEqual({ x: 0, y: 0, w: 20, h: 1 });
});

test("dirty drafts survive a background rebase while untouched content advances", async ({ page }) => {
  await page.goto("/e2e/harness.html?mode=session");
  await waitForHarness(page);

  const result = await page.evaluate(() => window.__scribeBrowserHarness.runBackgroundRebaseScenario());
  expect(result).toEqual({
    baseLineOne: "worker result",
    dirty: true,
    draftLineOne: "local draft",
    draftLineTwo: "remote untouched update",
    label: "Remote metadata",
    pendingRemoteIds: ["line-1"],
    revision: "8",
  });
});

test("a production SSE completion rebases the mounted dirty editor through Connect", async ({ page }) => {
  test.skip(
    process.env.VITE_SCRIBE_BROWSER_BACKEND !== "true",
    "requires the MariaDB-backed browser fixture",
  );
  test.setTimeout(180_000);

  const annotationLoads: string[] = [];
  page.on("request", (request) => {
    if (request.url().includes("/scribe.v1.AnnotationService/GetAnnotationPage")) {
      annotationLoads.push(request.url());
    }
  });
  const streamReady = page.waitForResponse(
    (response) => response.url().includes("/v1/events?") && response.status() === 200,
    { timeout: 60_000 },
  );

  await page.goto("/e2e/harness.html?mode=background");
  await waitForHarness(page, 120_000);
  await streamReady;
  await expect.poll(
    async () => page.evaluate(() => window.__scribeBrowserHarness.backgroundSnapshot()),
    { timeout: 60_000 },
  ).toMatchObject({
    annotationText: "server base",
    dirty: false,
    sessionStatus: "ready",
  });

  await page.getByRole("button", { name: "Overlay off" }).click();
  const inputs = page.getByRole("textbox", { name: /Edit line token/ });
  const inputValues = () => inputs.evaluateAll(
    (elements) => elements.map((element) => (element as HTMLInputElement).value),
  );
  await expect(inputs).toHaveCount(2);
  await inputs.nth(0).fill("local draft");
  await expect.poll(
    async () => page.evaluate(() => window.__scribeBrowserHarness.backgroundSnapshot()),
    { timeout: 30_000 },
  ).toMatchObject({ dirty: true, sessionStatus: "ready" });

  const dirtyDraft = await page.evaluate(() => window.__scribeBrowserHarness.backgroundSnapshot());
  const dirtyInputValues = await inputValues();
  expect(dirtyDraft.annotationText).not.toBe("server base");
  const loadsBeforeCompletion = annotationLoads.length;
  const completion = await page.evaluate(
    () => window.__scribeBrowserHarness.triggerBackgroundCompletion(),
  );
  expect(completion).toMatchObject({
    remoteText: "worker result",
  });
  expect(completion.revision).toMatch(/^[1-9][0-9]*$/);

  await expect.poll(
    async () => ({
      connectReloaded: annotationLoads.length > loadsBeforeCompletion,
      snapshot: await page.evaluate(() => window.__scribeBrowserHarness.backgroundSnapshot()),
    }),
    { timeout: 30_000 },
  ).toMatchObject({
    connectReloaded: true,
    snapshot: {
      annotationId: dirtyDraft.annotationId,
      annotationText: dirtyDraft.annotationText,
      dirty: true,
      pendingRemoteIds: [dirtyDraft.annotationId],
      reloadCount: 1,
      sessionStatus: "ready",
    },
  });
  expect(await inputValues()).toEqual(dirtyInputValues);
});

test("page save reloads cleanly and a stale editor retains its draft on conflict", async ({ page }) => {
  await page.goto("/e2e/harness.html?mode=session");
  await waitForHarness(page);

  const result = await page.evaluate(() => window.__scribeBrowserHarness.runPersistenceScenario());
  if (process.env.VITE_SCRIBE_BROWSER_BACKEND === "true") {
    expect(result.backend).toBe("connect-mariadb");
  }
  expect(BigInt(result.saveRevision)).toBe(BigInt(result.initialRevision) + 1n);
  expect(result.saveDirty).toBe(false);
  expect(result.reloadRevision).toBe(result.saveRevision);
  expect(result.reloadText).toBe("saved by editor A");
  expect(result.reloadDirty).toBe(false);
  expect(result.reloadLargeInteger).toBe("9007199254740993");
  expect(result.reloadPageCounter).toBe("9007199254740995");
  expect(result.reloadPreciseDecimal).toBe("0.123456789012345678901");
  expect(result.conflictName).toBe("RevisionConflict");
  expect(result.conflictMessage).toContain("revision conflict");
  expect(result.conflictedDraftText).toBe("unsaved editor B draft");
  expect(result.conflictedDraftDirty).toBe(true);
  expect(result.pendingRemoteIds).toEqual([result.primaryAnnotationId]);
  expect(result.structuralCalls).toBe(1);
  expect(result.structuralSourceWasUnsaved).toBe(true);
  expect(result.structuralSourceCanonical).toBe(true);
  expect(result.structuralResultCount).toBe(3);
  expect(result.structuralResultCanonical).toBe(true);
  expect(result.structuralResultPageId).toMatch(
    /\/presentation\/v3\/item-image-[1-9][0-9]*\/canvas\/page-1\/annotations$/,
  );
  expect(result.structuralResultPreservedExisting).toBe(true);
});

test("a multi-Canvas adapter saves and reprocesses only the active Canvas item image", async ({ page }) => {
  await page.goto("/e2e/harness.html?mode=session");
  await waitForHarness(page);

  const result = await page.evaluate(() => window.__scribeBrowserHarness.runMultiCanvasPersistenceScenario());
  expect(result).toEqual({
    initialPageAText: "page A original",
    initialPageBText: "page B original",
    loadItemImageIds: ["1001", "2002"],
    pageARevision: "11",
    pageAText: "page A original",
    pageBRevision: "22",
    pageBTarget: "https://source.test/manifest/canvas/b#xywh=20,20,200,30",
    pageBText: "page B corrected",
    reprocessItemImageId: "2002",
    saveItemImageIds: ["2002"],
    savedPageId: "https://scribe.test/presentation/v3/item-image-2002/canvas/page-1/annotations",
  });
});

test("mounted Mirador/Scribe keeps edits and events scoped across two real Canvases", async ({ page }) => {
  test.setTimeout(300_000);
  const pluginPollOptions = { timeout: 60_000 };
  await page.goto("/e2e/harness.html?mode=plugin");
  await waitForHarness(page, 120_000);

  await expect(page.getByText("View and modes", { exact: true })).toBeVisible();
  const canvasB = "http://127.0.0.1:4173/e2e/canvas/b";
  await page.evaluate((canvasId) => window.__scribeBrowserHarness.turnPluginCanvas(canvasId), canvasB);
  await expect.poll(async () => page.evaluate(() => {
    const snapshot = window.__scribeBrowserHarness.pluginSnapshot();
    return snapshot.activeCanvasId === "http://127.0.0.1:4173/e2e/canvas/b"
      && snapshot.loadItemImageIds.includes("1001")
      && snapshot.loadItemImageIds.includes("2002");
  }), pluginPollOptions).toBe(true);

  const addCenteredLine = page.getByRole("button", {
    name: "Add a line at the viewport center and focus its keyboard resize handle",
  });
  await addCenteredLine.focus();
  await page.keyboard.press("Enter");
  await expect.poll(async () => page.evaluate(() => {
    const snapshot = window.__scribeBrowserHarness.pluginSnapshot();
    return {
      draftCount: snapshot.pageB.draftCount,
      overlayMode: snapshot.overlayMode,
      statusMessage: snapshot.statusMessage,
    };
  }), pluginPollOptions).toEqual({
    draftCount: 2,
    overlayMode: "edit",
    statusMessage: "Draft line created. Its southeast resize handle is focused; use Arrow keys to resize, or Shift+Arrow for larger steps.",
  });
  const creationStatus = page.getByRole("status").filter({ hasText: "Draft line created." });
  await expect(creationStatus).toHaveAttribute("aria-live", "polite");
  const southeastResize = page.getByRole("button", {
    name: "Resize annotation from the se corner",
  });
  await expect(southeastResize).toBeFocused();
  await expect(southeastResize).toHaveAttribute(
    "aria-keyshortcuts",
    "ArrowUp ArrowDown ArrowLeft ArrowRight",
  );
  const initialKeyboardTarget = await page.evaluate(() => (
    window.__scribeBrowserHarness.pluginSnapshot().selectedDraftTarget
  ));
  expect(initialKeyboardTarget).toMatchObject({
    selector: { value: expect.stringMatching(/^xywh=[0-9]+,[0-9]+,[0-9]+,[0-9]+$/) },
  });
  await page.keyboard.press("ArrowRight");
  await expect.poll(async () => JSON.stringify(await page.evaluate(() => (
    window.__scribeBrowserHarness.pluginSnapshot().selectedDraftTarget
  ))), pluginPollOptions).not.toBe(JSON.stringify(initialKeyboardTarget));
  await page.keyboard.press("Control+Backspace");
  await expect.poll(async () => page.evaluate(() => (
    window.__scribeBrowserHarness.pluginSnapshot().pageB.draftCount
  )), pluginPollOptions).toBe(1);
  await page.keyboard.press("Escape");

  await page.getByRole("button", { name: "Overlay off" }).click();
  const inputs = page.getByRole("textbox", { name: /Edit line token/ });
  await expect(inputs).toHaveCount(3);
  await expect(inputs.nth(0)).toHaveValue("page");
  await expect(inputs.nth(1)).toHaveValue("B");
  await expect(inputs.nth(2)).toHaveValue("original");
  await expect(inputs.nth(0)).toBeFocused();

  await expect(southeastResize).toBeVisible();
  await southeastResize.focus();
  await page.keyboard.press("ArrowRight");
  await expect.poll(async () => page.evaluate(() => (
    window.__scribeBrowserHarness.pluginSnapshot().pageB.draftTarget
  )), pluginPollOptions).toMatchObject({
    selector: [
      {
        extension: { retained: "nonspatial" },
        type: "FragmentSelector",
        value: "t=2,4&track=ocr",
      },
      {
        extension: { retained: "spatial" },
        type: "FragmentSelector",
        value: "xywh=pixel:120,160,501,80",
      },
    ],
  });

  await page.keyboard.press("Alt+s");
  await expect(page.getByRole("dialog", { name: "Choose a split boundary" })).toBeVisible();
  await page.getByRole("button", { name: "Split after B, word 2" }).click();
  await page.getByRole("button", { name: "Split at boundary" }).click();
  await expect.poll(async () => page.evaluate(() => window.__scribeBrowserHarness.pluginSnapshot()), pluginPollOptions)
    .toMatchObject({ isBusy: true, saveItemImageIds: [], splitPending: true, structural: { calls: { splitAtWord: 2 } } });
  await page.keyboard.press("Control+s");
  await expect.poll(async () => page.evaluate(() => window.__scribeBrowserHarness.pluginSnapshot()), pluginPollOptions)
    .toMatchObject({ isBusy: true, saveItemImageIds: [], splitPending: true });
  await page.evaluate(() => window.__scribeBrowserHarness.finishPluginSplit());
  await expect.poll(async () => page.evaluate(() => window.__scribeBrowserHarness.pluginSnapshot().isBusy), pluginPollOptions)
    .toBe(false);
  await expect(inputs.nth(0)).toBeFocused();

  await page.keyboard.press("Tab");
  await expect(inputs.nth(0)).toBeFocused();
  await page.keyboard.press("Shift+Tab");
  await expect(inputs.nth(0)).toBeFocused();
  await page.keyboard.press("Escape");
  await expect(inputs).toHaveCount(0);
  await page.getByRole("button", { name: "Overlay off" }).click();
  await expect(inputs).toHaveCount(3);
  await inputs.nth(1).focus();
  await page.keyboard.press("Control+Backspace");
  await expect(inputs).toHaveCount(2);
  await expect.poll(async () => page.evaluate(() => window.__scribeBrowserHarness.pluginSnapshot().pageB.count), pluginPollOptions)
    .toBe(1);
  await inputs.nth(1).fill("B corrected");
  await expect(inputs).toHaveCount(3);
  await page.getByRole("button", { name: "Save" }).click();

  await expect.poll(async () => page.evaluate(() => window.__scribeBrowserHarness.pluginSnapshot()), pluginPollOptions)
    .toMatchObject({
      pageA: { count: 1, revision: "11", text: "page A original" },
      pageB: {
        count: 1,
        revision: "22",
        target: {
          selector: [
            {
              extension: { retained: "nonspatial" },
              type: "FragmentSelector",
              value: "t=2,4&track=ocr",
            },
            {
              extension: { retained: "spatial" },
              type: "FragmentSelector",
              value: "xywh=pixel:120,160,501,80",
            },
          ],
        },
        text: "page B corrected",
      },
    });
  await page.evaluate((canvasId) => {
    document.dispatchEvent(new CustomEvent("scribe:reload-annotations", {
      detail: { canvasId, itemImageId: "2002", windowId: "plugin-window" },
    }));
  }, canvasB);
  await expect.poll(async () => page.evaluate(() => {
    const snapshot = window.__scribeBrowserHarness.pluginSnapshot();
    return {
      draftTarget: snapshot.pageB.draftTarget,
      reloads: snapshot.loadItemImageIds.filter((itemImageId) => itemImageId === "2002").length,
      sessionStatus: snapshot.sessionStatus,
    };
  }), pluginPollOptions).toMatchObject({
    draftTarget: {
      selector: [
        {
          extension: { retained: "nonspatial" },
          type: "FragmentSelector",
          value: "t=2,4&track=ocr",
        },
        {
          extension: { retained: "spatial" },
          type: "FragmentSelector",
          value: "xywh=pixel:120,160,501,80",
        },
      ],
    },
    reloads: 2,
    sessionStatus: "ready",
  });
  await page.keyboard.press("Control+s");
  await expect.poll(async () => page.evaluate(() => window.__scribeBrowserHarness.pluginSnapshot()), pluginPollOptions)
    .toMatchObject({ saveItemImageIds: ["2002"], sessionStatus: "ready" });
  await page.getByRole("button", { name: "Publish edits" }).click();
  await expect(page.getByText("Edits published.", { exact: true })).toBeVisible();
  await expect.poll(async () => page.evaluate(() => window.__scribeBrowserHarness.pluginSnapshot()), pluginPollOptions)
    .toMatchObject({ saveItemImageIds: ["2002"], sessionStatus: "ready" });
  const snapshot = await page.evaluate(() => window.__scribeBrowserHarness.pluginSnapshot());
  expect(snapshot.activeCanvasEvents).toContainEqual({
    canvasId: canvasB,
    itemImageId: "2002",
    windowId: "plugin-window",
  });
});

test("structural edit pickers split at the chosen boundary and join explicit subsets", async ({ page }) => {
  test.setTimeout(300_000);
  const pluginPollOptions = { timeout: 60_000 };
  await page.goto("/e2e/harness.html?mode=structural");
  await waitForHarness(page, 120_000);

  const canvasB = "http://127.0.0.1:4173/e2e/canvas/b";
  await page.evaluate((canvasId) => window.__scribeBrowserHarness.turnPluginCanvas(canvasId), canvasB);
  await expect.poll(async () => page.evaluate(() => (
    window.__scribeBrowserHarness.pluginSnapshot().activeCanvasId
  )), pluginPollOptions).toBe(canvasB);

  await expect(page.locator("[data-scribe-granularity]")).toHaveCount(0);
  await page.getByRole("button", { name: /Overlay off/i }).click();
  await page.getByRole("button", { name: /Edit overlay/i }).click();
  await expect(page.getByRole("button", { name: /Read overlay/i })).toBeVisible();
  await expect(page.locator('[data-scribe-granularity="line"]')).not.toHaveCount(0);
  await expect(page.locator('[data-scribe-granularity="word"]')).not.toHaveCount(0);

  await page.keyboard.press("Alt+s");
  await expect(page.getByRole("dialog", { name: "Choose a split boundary" })).toBeVisible();
  await page.getByRole("button", { name: "Split after gamma, word 3" }).click();
  await page.getByRole("button", { name: "Split at boundary" }).click();
  await expect.poll(async () => page.evaluate(() => {
    const { structural } = window.__scribeBrowserHarness.pluginSnapshot();
    return {
      splitAtWord: structural.calls.splitAtWord,
      texts: structural.draft.map(({ text }) => text),
    };
  }), pluginPollOptions).toEqual({
    splitAtWord: 3,
    texts: expect.arrayContaining(["alpha beta gamma", "delta"]),
  });

  await page.getByRole("button", { name: "Edit line: second line" }).click();
  await page.getByRole("button", { name: /Join lines/i }).focus();
  await page.keyboard.press("Alt+l");
  await expect(page.getByRole("dialog", { name: "Choose lines to join" })).toBeVisible();
  await page.getByRole("button", { name: /Line [0-9]+: fifth line/ }).click();
  await page.getByRole("button", { name: "Join selected lines" }).click();
  await expect.poll(async () => page.evaluate(() => {
    const { structural } = window.__scribeBrowserHarness.pluginSnapshot();
    return {
      joined: structural.calls.joinLineIds.length,
      texts: structural.draft.map(({ text }) => text),
    };
  }), pluginPollOptions).toEqual({
    joined: 2,
    texts: expect.arrayContaining(["second line fifth line", "fourth line"]),
  });

  await page.getByRole("button", { name: /Edit overlay/i }).click();
  await page.getByRole("button", { name: "Edit word: red" }).click();
  await page.getByRole("button", { name: /Join words/i }).focus();
  await page.keyboard.press("Alt+w");
  await expect(page.getByRole("dialog", { name: "Choose words to join" })).toBeVisible();
  await page.getByRole("button", { name: "Word 3: blue" }).click();
  await page.getByRole("button", { name: "Join selected words" }).click();
  await expect.poll(async () => page.evaluate(() => {
    const { structural } = window.__scribeBrowserHarness.pluginSnapshot();
    return {
      joined: structural.calls.joinWordIds.length,
      rows: structural.draft.map(({ granularity, text }) => `${granularity}:${text}`),
    };
  }), pluginPollOptions).toEqual({
    joined: 2,
    rows: expect.arrayContaining(["line:red blue", "word:green", "word:gold"]),
  });
});
