import { readFile } from "node:fs/promises";
import { chromium } from "@playwright/test";

const fixturePath = "/app/readiness-smoke.png.base64";
const stageTimeoutMs = 180_000;
const transcriptionTimeoutMs = 360_000;

function previewBaseURL() {
  const raw = process.env.SCRIBE_BROWSER_BASE_URL ?? "";
  let parsed;
  try {
    parsed = new URL(raw);
  } catch {
    return undefined;
  }
  if (
    parsed.protocol !== "https:"
    || parsed.username !== ""
    || parsed.password !== ""
    || parsed.pathname !== "/"
    || parsed.search !== ""
    || parsed.hash !== ""
    || !/^scribe-pr-[1-9][0-9]*-[0-9]+\.[a-z]+-[a-z]+[0-9]+\.run\.app$/.test(parsed.hostname)
  ) {
    return undefined;
  }
  return parsed;
}

function annotationHasText(annotation) {
  const bodies = Array.isArray(annotation?.body) ? annotation.body : [annotation?.body];
  return bodies.some((body) => (
    typeof body === "string"
      ? body.trim() !== ""
      : typeof body?.value === "string" && body.value.trim() !== ""
  ));
}

function assertTextualAnnotationPage(annotationPage) {
  if (
    annotationPage?.type !== "AnnotationPage"
    || !Array.isArray(annotationPage.items)
    || annotationPage.items.length === 0
    || !annotationPage.items.some(annotationHasText)
  ) {
    throw new Error("missing annotations");
  }
}

async function loadCanonicalAnnotationPage(itemImageID, workspaceID) {
  const response = await browserContext.request.post(
    new URL("/scribe.v1.AnnotationService/GetAnnotationPage", baseURL).href,
    {
      data: { itemImageId: itemImageID },
      headers: {
        "Connect-Protocol-Version": "1",
        "Content-Type": "application/json",
        "X-Scribe-Workspace-ID": workspaceID,
      },
    },
  );
  if (!response.ok()) throw new Error("canonical annotation request failed");
  let payload;
  try {
    payload = await response.json();
  } catch {
    throw new Error("invalid canonical annotation response");
  }
  if (typeof payload?.annotationPageJson !== "string") {
    throw new Error("canonical annotation response omitted the page");
  }
  try {
    return JSON.parse(payload.annotationPageJson);
  } catch {
    throw new Error("invalid canonical annotation page");
  }
}

async function waitForPublishedAnnotationPage(annotationPath) {
  const deadline = Date.now() + stageTimeoutMs;
  while (Date.now() < deadline) {
    const response = await browserContext.request.get(new URL(annotationPath, baseURL).href);
    if (response.ok()) {
      try {
        return await response.json();
      } catch {
        throw new Error("invalid published annotation response");
      }
    }
    if (response.status() !== 404) throw new Error("published annotation request failed");
    await new Promise((resolve) => setTimeout(resolve, 1_000));
  }
  throw new Error("published annotation page did not become available");
}

async function findActionByValue(page, attribute, value) {
  const candidates = page.locator(`[${attribute}]`);
  const count = await candidates.count();
  for (let index = 0; index < count; index += 1) {
    const candidate = candidates.nth(index);
    if (await candidate.getAttribute(attribute) === value) return candidate;
  }
  return undefined;
}

async function findAPIKeyDeleteByName(page, name) {
  const candidates = page.locator("[data-api-key-delete]");
  const count = await candidates.count();
  for (let index = 0; index < count; index += 1) {
    const candidate = candidates.nth(index);
    const parentText = await candidate.locator("xpath=..").textContent();
    if (parentText?.includes(name)) return candidate;
  }
  return undefined;
}

async function waitForActionToDisappear(attribute, value) {
  if (!/^[a-z0-9-]+$/.test(attribute) || !value) throw new Error("invalid cleanup identity");
  await page.waitForFunction(({ attributeName, expectedValue }) => (
    Array.from(document.querySelectorAll(`[${attributeName}]`))
      .every((element) => element.getAttribute(attributeName) !== expectedValue)
  ), { attributeName: attribute, expectedValue: value });
}

let category = "home";
let failureCategory;
let browserFaultCategory;
let browser;
let browserContext;
let page;
let baseURL;
let createdItemID;
let createdAPIKeyName;
let createdAPIKeyID;
let expectedConfirmation = false;

function assertBrowserHealthy() {
  if (browserFaultCategory) throw new Error("browser fault");
}

async function navigate(path, requireHealthy = true) {
  await page.goto(path, { waitUntil: "domcontentloaded" });
  if (requireHealthy) assertBrowserHealthy();
}

async function deleteWithConfirmation(button, attribute) {
  const value = (await button.getAttribute(attribute) ?? "").trim();
  if (!value) throw new Error("missing cleanup identity");
  expectedConfirmation = true;
  try {
    await button.click();
    await waitForActionToDisappear(attribute, value);
  } finally {
    expectedConfirmation = false;
  }
}

async function deleteAPIKeyWithConfirmation(button, apiKeyID) {
  const deleteResponsePromise = page.waitForResponse((response) => {
    const responseURL = new URL(response.url());
    return responseURL.origin === baseURL.origin
      && responseURL.pathname === "/scribe.v1.AuthService/DeleteAPIKey"
      && response.request().method() === "POST";
  });
  const listResponsePromise = page.waitForResponse((response) => {
    const responseURL = new URL(response.url());
    return responseURL.origin === baseURL.origin
      && responseURL.pathname === "/scribe.v1.AuthService/ListAPIKeys"
      && response.request().method() === "POST";
  });
  expectedConfirmation = true;
  try {
    await button.click();
    const deleteResponse = await deleteResponsePromise;
    if (!deleteResponse.ok()) throw new Error("token cleanup request failed");
    const listResponse = await listResponsePromise;
    if (!listResponse.ok()) throw new Error("token cleanup verification failed");
    const listPayload = await listResponse.json();
    if (
      !Array.isArray(listPayload?.apiKeys)
      || listPayload.apiKeys.some((key) => String(key?.id ?? "") === apiKeyID)
    ) {
      throw new Error("token remained after cleanup");
    }
    await waitForActionToDisappear("data-api-key-delete", apiKeyID);
    if (await findActionByValue(page, "data-api-key-delete", apiKeyID)) {
      throw new Error("token cleanup rerender retained action");
    }
  } finally {
    expectedConfirmation = false;
  }
}

async function assertResponsiveEditorGeometry(width, height) {
  await page.setViewportSize({ width, height });
  const geometry = await page.locator('[data-scribe-action-panel="true"]').evaluate((panel) => {
    const parent = panel.parentElement;
    const viewer = document.getElementById("mirador-viewer");
    const panelBounds = panel.getBoundingClientRect();
    const parentBounds = parent?.getBoundingClientRect();
    const viewerBounds = viewer?.getBoundingClientRect();
    return {
      pageOverflow: document.documentElement.scrollWidth > window.innerWidth,
      panelClientHeight: panel.clientHeight,
      panelClientWidth: panel.clientWidth,
      panelScrollWidth: panel.scrollWidth,
      parentClientHeight: parent?.clientHeight ?? 0,
      parentClientWidth: parent?.clientWidth ?? 0,
      parentScrollWidth: parent?.scrollWidth ?? 0,
      panelWithinParent: Boolean(parentBounds)
        && panelBounds.top >= parentBounds.top - 1
        && panelBounds.bottom <= parentBounds.bottom + 1,
      panelWithinViewer: Boolean(viewerBounds)
        && panelBounds.top >= viewerBounds.top - 1
        && panelBounds.bottom <= viewerBounds.bottom + 1,
      viewerClientHeight: viewer?.clientHeight ?? 0,
    };
  });
  if (
    geometry.pageOverflow
    || geometry.viewerClientHeight < Math.min(500, height - 180)
    || geometry.panelClientHeight <= 0
    || geometry.parentClientHeight <= 0
    || Math.abs(geometry.panelClientHeight - geometry.parentClientHeight) > 2
    || geometry.panelClientWidth <= 0
    || geometry.parentClientWidth <= 0
    || geometry.panelScrollWidth > geometry.panelClientWidth + 1
    || geometry.parentScrollWidth > geometry.parentClientWidth + 1
    || !geometry.panelWithinParent
    || !geometry.panelWithinViewer
  ) {
    throw new Error("editor action panel geometry failed");
  }
  const retranscribe = page.getByRole("button", { name: "Retranscribe", exact: true });
  await retranscribe.scrollIntoViewIfNeeded();
  const actionBounds = await retranscribe.boundingBox();
  if (!actionBounds || actionBounds.x < 0 || actionBounds.x + actionBounds.width > width) {
    throw new Error("editor control outside viewport");
  }
}

try {
  baseURL = previewBaseURL();
  if (!baseURL) throw new Error("invalid target");

  browser = await chromium.launch({ headless: true });
  browserContext = await browser.newContext({
    baseURL: baseURL.href,
    acceptDownloads: false,
  });
  await browserContext.grantPermissions(
    ["clipboard-read", "clipboard-write"],
    { origin: baseURL.origin },
  );
  page = await browserContext.newPage();
  page.setDefaultTimeout(stageTimeoutMs);
  page.setDefaultNavigationTimeout(stageTimeoutMs);

  page.on("response", (response) => {
    let responseURL;
    try {
      responseURL = new URL(response.url());
    } catch {
      return;
    }
    if (responseURL.origin === baseURL.origin && response.status() >= 400) {
      browserFaultCategory ??= "network";
    }
  });
  page.on("requestfailed", (request) => {
    let requestURL;
    try {
      requestURL = new URL(request.url());
    } catch {
      return;
    }
    const clientCancellation = /ERR_ABORTED|cancell?ed/i.test(request.failure()?.errorText ?? "");
    if (requestURL.origin === baseURL.origin && !clientCancellation) {
      browserFaultCategory ??= "network";
    }
  });
  page.on("console", (message) => {
    if (
      message.type() === "error"
      && /content security policy|violates.*(?:csp|policy)|refused to (?:connect|load).*policy/i.test(message.text())
    ) {
      browserFaultCategory ??= "csp";
    }
  });
  page.on("dialog", (dialog) => {
    if (dialog.type() === "confirm" && expectedConfirmation) {
      void dialog.accept().catch(() => {
        browserFaultCategory ??= "cleanup";
      });
      return;
    }
    browserFaultCategory ??= "token";
    void dialog.dismiss().catch(() => {
      browserFaultCategory ??= "token";
    });
  });

  await navigate("/");
  await page.locator("#library-single-file").waitFor({ state: "attached" });

  category = "context";
  await page.locator('[data-library-tab="single"]').click();
  const contextSelect = page.locator("#library-context-select");
  await contextSelect.selectOption({ label: "Tesseract OCR" });
  const contextID = await contextSelect.inputValue();
  if (!/^[1-9][0-9]*$/.test(contextID)) throw new Error("invalid context");
  assertBrowserHealthy();

  category = "upload";
  const fixture = Buffer.from((await readFile(fixturePath, "utf8")).trim(), "base64");
  const fixtureName = `browser-readiness-${Date.now()}.png`;
  const startUploadResponsePromise = page.waitForResponse((response) => {
    const responseURL = new URL(response.url());
    return responseURL.origin === baseURL.origin
      && responseURL.pathname === "/scribe.v1.ItemService/StartUploadBatch"
      && response.request().method() === "POST";
  });
  await page.locator("#library-single-file").setInputFiles({
    name: fixtureName,
    mimeType: "image/png",
    buffer: fixture,
  });
  await page.locator("#shell-upload-dialog").waitFor({ state: "visible" });
  const startUploadResponse = await startUploadResponsePromise;
  if (!startUploadResponse.ok()) throw new Error("upload start failed");
  const startUploadPayload = await startUploadResponse.json();
  createdItemID = String(startUploadPayload?.item?.id ?? "").trim() || undefined;
  if (!createdItemID) throw new Error("upload start omitted item identity");

  category = "handoff";
  await page.waitForURL((url) => (
    url.pathname === "/editor"
    && /^[1-9][0-9]*$/.test(url.searchParams.get("itemImageId") ?? "")
    && /^[1-9][0-9]*$/.test(url.searchParams.get("jobId") ?? "")
  ));
  const editorURL = new URL(page.url());
  const itemImageID = editorURL.searchParams.get("itemImageId");
  const workspaceID = editorURL.searchParams.get("workspace_id");
  if (editorURL.searchParams.get("itemId") !== createdItemID) throw new Error("mismatched editor identity");
  if (!itemImageID) throw new Error("missing editor identity");
  if (!workspaceID || !/^[1-9][0-9]*$/.test(workspaceID)) throw new Error("missing workspace identity");
  assertBrowserHealthy();

  category = "transcription";
  await page.locator("#editor-transcription-status").waitFor({ state: "attached" });
  const transcriptionOutcome = await page.waitForFunction(() => {
    const text = document.getElementById("editor-transcription-status")?.textContent?.trim() ?? "";
    if (text === "Batch transcription complete. Updated text is now available in the editor.") return "complete";
    if (text.startsWith("Batch transcription failed")) return "failed";
    return "";
  }, undefined, { timeout: transcriptionTimeoutMs });
  if (await transcriptionOutcome.jsonValue() !== "complete") throw new Error("transcription failed");
  assertBrowserHealthy();

  category = "annotations";
  const annotationPage = await loadCanonicalAnnotationPage(itemImageID, workspaceID);
  assertTextualAnnotationPage(annotationPage);
  assertBrowserHealthy();

  category = "editor";
  await page.locator("#mirador-viewer").waitFor({ state: "visible" });
  await page.getByRole("heading", { name: "Editor", exact: true }).waitFor({ state: "visible" });
  for (const name of ["Overlay off", "Retranscribe", "Save", "Publish edits"]) {
    await page.getByRole("button", { name, exact: true }).waitFor({ state: "visible" });
  }

  category = "overlay";
  await page.getByRole("button", { name: "Overlay off", exact: true }).click();
  await page.getByRole("button", { name: "Edit overlay", exact: true }).waitFor({ state: "visible" });
  await page.locator(".scribe-text-overlay").waitFor({ state: "visible" });
  if (await page.locator('[data-scribe-granularity="line"]').count() < 1) {
    throw new Error("overlay omitted line markers");
  }
  await page.getByRole("button", { name: "Edit overlay", exact: true }).click();
  await page.getByRole("button", { name: "Read overlay", exact: true }).click();
  await page.getByRole("button", { name: "Outline overlay", exact: true }).click();
  await page.getByRole("button", { name: "Overlay off", exact: true }).waitFor({ state: "visible" });
  if (await page.locator("[data-scribe-granularity]").count() !== 0) {
    throw new Error("overlay markers remained enabled");
  }
  assertBrowserHealthy();

  category = "retranscribe";
  await page.getByRole("button", { name: "Retranscribe", exact: true }).click();
  const transcribeDialog = page.getByRole("dialog").filter({ hasText: "entire page" });
  await transcribeDialog.waitFor({ state: "visible" });
  await transcribeDialog.getByRole("button", { name: /entire page/i }).click();
  await page.getByText("Document retranscribed. Save to persist this draft.", { exact: true }).waitFor({ state: "visible" });
  assertBrowserHealthy();

  category = "save";
  const saveButton = page.getByRole("button", { name: "Save", exact: true });
  if (await saveButton.isDisabled()) {
    await page.getByRole("button", { name: "Add centered line", exact: true }).click();
    await saveButton.waitFor({ state: "visible" });
  }
  await saveButton.click();
  await page.getByText("Saved page.", { exact: true }).waitFor({ state: "visible" });
  assertBrowserHealthy();

  category = "publish";
  await page.getByRole("button", { name: "Publish edits", exact: true }).click();
  await page.getByText("Edits published.", { exact: true }).waitFor({ state: "visible" });
  const publishedAnnotationPath = `/presentation/v3/item-image-${itemImageID}/canvas/page-1/annotations`;
  const publishedAnnotationPage = await waitForPublishedAnnotationPage(publishedAnnotationPath);
  assertTextualAnnotationPage(publishedAnnotationPage);
  assertBrowserHealthy();

  category = "responsive";
  for (const viewport of [
    { width: 360, height: 800 },
    { width: 768, height: 1024 },
    { width: 1440, height: 900 },
  ]) {
    await assertResponsiveEditorGeometry(viewport.width, viewport.height);
  }
  assertBrowserHealthy();

  category = "token";
  await navigate("/");
  await page.locator("#shell-account-button").click();
  await page.getByRole("heading", { name: "Workspace and account settings", exact: true }).waitFor({ state: "visible" });
  await page.locator("#settings-api-key-form").waitFor({ state: "visible" });
  createdAPIKeyName = `browser-readiness-${Date.now()}`;
  await page.getByLabel("Workspace token name").fill(createdAPIKeyName);
  await page.getByLabel("Workspace token role").selectOption("read");
  const createKeyResponsePromise = page.waitForResponse((response) => {
    const responseURL = new URL(response.url());
    return responseURL.origin === baseURL.origin
      && responseURL.pathname === "/scribe.v1.AuthService/CreateAPIKey"
      && response.request().method() === "POST";
  });
  await page.getByRole("button", { name: "Create key", exact: true }).click();
  const createKeyResponse = await createKeyResponsePromise;
  if (!createKeyResponse.ok()) throw new Error("token creation failed");
  const createKeyPayload = await createKeyResponse.json();
  createdAPIKeyID = String(createKeyPayload?.apiKey?.id ?? "");
  if (!/^[1-9][0-9]*$/.test(createdAPIKeyID)) throw new Error("missing token identity");

  const tokenDialog = page.getByRole("dialog", { name: "Copy workspace token", exact: true });
  await tokenDialog.waitFor({ state: "visible" });
  const tokenField = tokenDialog.getByRole("textbox", { name: "Workspace token", exact: true });
  if (await tokenField.getAttribute("readonly") === null) throw new Error("token is editable");
  const tokenValue = await tokenField.inputValue();
  if (!tokenValue.startsWith("scribe_") || tokenValue.length < 24) throw new Error("invalid token");
  await tokenDialog.getByRole("button", { name: "Copy token", exact: true }).click();
  await tokenDialog.getByText("Copied to clipboard.", { exact: true }).waitFor({ state: "visible" });
  const copiedValue = await page.evaluate(() => navigator.clipboard.readText());
  if (copiedValue !== tokenValue) throw new Error("token copy failed");
  await tokenDialog.getByRole("button", { name: "Done", exact: true }).click();
  await tokenDialog.waitFor({ state: "hidden" });
  if (await tokenField.inputValue() !== "") throw new Error("token remained in document");
  await page.evaluate(() => navigator.clipboard.writeText(""));
  assertBrowserHealthy();
} catch {
  failureCategory = browserFaultCategory ?? category;
} finally {
  if (page && (createdAPIKeyName || createdAPIKeyID || createdItemID)) {
    try {
      category = "cleanup";
      await navigate("/", false);
      let cleanupFailed = false;

      if (createdAPIKeyName || createdAPIKeyID) {
        try {
          await page.locator("#shell-account-button").click();
          await page.getByRole("heading", { name: "Workspace and account settings", exact: true }).waitFor({ state: "visible" });
          const apiKeyDelete = createdAPIKeyID
            ? await findActionByValue(page, "data-api-key-delete", createdAPIKeyID)
            : await findAPIKeyDeleteByName(page, createdAPIKeyName);
          if (!apiKeyDelete) throw new Error("missing token cleanup action");
          const cleanupAPIKeyID = createdAPIKeyID
            || (await apiKeyDelete.getAttribute("data-api-key-delete") ?? "").trim();
          if (!/^[1-9][0-9]*$/.test(cleanupAPIKeyID)) throw new Error("missing token cleanup identity");
          await deleteAPIKeyWithConfirmation(apiKeyDelete, cleanupAPIKeyID);
        } catch {
          cleanupFailed = true;
        }
      }

      if (createdItemID) {
        try {
          await navigate("/", false);
          const itemDelete = await findActionByValue(page, "data-item-delete", createdItemID);
          if (!itemDelete) throw new Error("missing item cleanup action");
          await deleteWithConfirmation(itemDelete, "data-item-delete");
          if (await findActionByValue(page, "data-item-delete", createdItemID)) {
            throw new Error("item remained after cleanup");
          }
        } catch {
          cleanupFailed = true;
        }
      }
      if (cleanupFailed) throw new Error("cleanup failed");
    } catch {
      failureCategory ??= browserFaultCategory ?? category;
    }
  }
  if (browser) await browser.close().catch(() => {});
}

failureCategory = browserFaultCategory ?? failureCategory;
if (failureCategory) {
  process.stderr.write(`browser readiness failed: ${failureCategory}\n`);
  process.exitCode = 1;
} else {
  process.stdout.write("browser readiness passed\n");
}
