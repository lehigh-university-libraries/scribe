import { createHash, randomUUID } from "node:crypto";
import { chromium } from "@playwright/test";

import { configureCanonicalIPv6Routing } from "./deployed-readiness-routing.mjs";

const scriptStartedAt = Date.now();
const readinessSmokeFixtureBase64 = "iVBORw0KGgoAAAANSUhEUgAAAoAAAACgAQAAAAC0heCRAAAEJElEQVRo3u3ZTW7jNhQH8CdwMMyiCGeZRWBO0QtkmUVhoTdJb+DuvDAsqVpkOVeiposeoyx6gHJJoILY/yNpx2oKS2mnm4IGDMRO9LOp90E8hsIXflABC1jAAhawgAUs4P8abJtQTaT9lrSjOowyTGLkH6jGa1d7Cu6grCdt9SiWwYmUpyNJgJJBh+tpJAVQ4bXVkDLYCkfNIhiv25IAKBi01Fg6kMQvZAaVO0gGSdh1oL0EDdXmAjQA5ZtArz+Z41YBVL3Dn3dOt+NhA3Aj8doovxXuIKyvJ/mbGVeAdWjDpHqAHYPC6yqMWvIzgXWVQRWGFVEG2GUwfkMxKnEB/giwc4fqDaAO/SUoJyUvwF76esigDGYNWEUwB6XJYL6H1DBo3OEDgrKlxqhlEPkrkIych8ifZpLTLUc452EjX8Aa+VAtg0hsEbM7JvYZ1PxksI/g3RPAdYkd2gjqvOQTmEtvBlqU5ArQyz4tOaVNuod5yQ1Hi8H7j8iGqQqmXgYnBm8ZHF7Ae8VPgFTFKN8TQOSXWwEG2aUoJzDl4alSRgaRh5sIDpy1S+AfQZ4qZXhdKSNRrBQGpzD8sgL0R8m13J3uYaplzdUiEsi1rBnU3U8rluxjt1EDoly9dBvdA+wdPWobuw1A2pAwa7pN7IfKzPuh7gB2AGu7536o2zeA6HhHdORZx9YDwIFBt+eOnUBpXsf0b97hPUXZ/WlPEbynaAPQOHpgEHuKNvhgNNsVe8q/fRSwgAUsYAELWMACFrCABfwvwQGzAyZ/HudrTGWTCG0QmNUqDJ6Y7PIBUDDC4G0b/BYzEAbV6yDmCAZlwHQhMDj3DGI6vgTJrgW96gFWPEZFEENyPwF0GZQZfDrKCIoF0AL0CcQaaRQY7LpJvQZ3W7UerAImTtVG0G3FgFk32GMC+QAIC3H7bxOISVeHdaBpAu2FbcQwMhjCDHy80+tA96l330HaKpvBzmCuPYNVBg83dQRpAXS7Z+meIojhm3bChsFGsJmDR3oT+O4RYB3ePfQM+roVViYwHgAhKEfMrAze7A6kr4E/PEtrL0DDAYgnPjOw4utWgd//LK1r3t9F8H11Bk+JHQ+A8A1P4FcPC+DXn9UrcNe0qJg52HTrQA9wcI28SaDge+gfmh5X6wjGEyV8SJ3B2w/Xg+K/+awM1ZJilOUF6DOYDoD8GaQFUFjVAmxjHjLYdP5jADgmcPMXUC2DuiXNICpFPgNEfwD4+z8ExwyaWMsAUcsR/DUvWdP8Hqp2GRQjg9xtZOjRbcYqtJWNsU0nStxtIsgF1V7vNqN0Wo5K2tgPAaIfTjOwTWBzAs0KcIqgjCA6dgabGRhWgpN0WwZd3FMA8v8usLFUWHcCTdpTzqC9Dn6BRwELWMACFrCABSxgAQtYwAIWsIAFLCAefwL3udqwYaAJQwAAAABJRU5ErkJggg==";
const readinessSmokeFixtureSHA256 = "e3f3bb2b5ade3c15af262a76ad58b720e7eb3b3d079802df04f1dd50be917b2d";
const stageTimeoutMs = 180_000;
// Upload segmentation may consume the application's full 240-second
// scale-to-zero inference budget plus the backend's commit tail.
const uploadTimeoutMs = 300_000;
const transcriptionTimeoutMs = 360_000;
const wandVisualProofGraceMs = 5_000;
const mainScenarioBudgetMs = 1_800_000;
const cleanupReserveMs = 600_000;
const cleanupPlatformHeadroomMs = 120_000;
const mainScenarioDeadline = scriptStartedAt + mainScenarioBudgetMs;
// The managed task is capped at 2,400 seconds. Stop reconciliation at
// start+2,280 seconds so process shutdown retains two minutes of headroom.
const globalCleanupDeadline = mainScenarioDeadline + cleanupReserveMs - cleanupPlatformHeadroomMs;
const cleanupCommitHorizonMs = uploadTimeoutMs;
const cleanupPollIntervalMs = 5_000;
const cleanupStablePasses = 2;
const cleanupRecoveryTailMs = stageTimeoutMs;
const cleanupMaxItemPages = 100;
const cleanupMaxItems = 10_000;
const maxObservedImageResponses = 100;
const maxReadinessImageBytes = 64 * 1024 * 1024;
const manifestURL = "https://preserve.lehigh.edu/node/38817/book-manifest";
const centeredLineAccessibleName = "Add a line at the viewport center and focus its keyboard resize handle";
const deterministicLineText = "browser readiness alpha beta gamma";

function bottomPaneHeightForViewport({ height, width }) {
  const viewportHeight = Number.isFinite(height) ? Math.max(0, Math.floor(height)) : 0;
  const viewportWidth = Number.isFinite(width) ? Math.max(0, Math.floor(width)) : 0;
  const shortViewport = viewportHeight < 420;
  const desiredPaneHeight = shortViewport ? 300 : viewportWidth <= 900 ? 420 : 320;
  const minimumCanvasHeight = shortViewport ? 72 : viewportWidth <= 480 ? 170 : 220;
  return Math.min(
    desiredPaneHeight,
    Math.max(0, viewportHeight - minimumCanvasHeight - 60),
  );
}

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

function exactReadinessSmokeFixture() {
  const fixture = Buffer.from(readinessSmokeFixtureBase64, "base64");
  const digest = createHash("sha256").update(fixture).digest("hex");
  if (fixture.length === 0 || digest !== readinessSmokeFixtureSHA256) {
    throw new Error("embedded readiness fixture failed its digest contract");
  }
  return fixture;
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

function assertExactPresentationAnnotationPage(page, annotationPath) {
  const expectedID = new URL(annotationPath, baseURL).href;
  if (
    page?.type !== "AnnotationPage"
    || page.id !== expectedID
    || !Array.isArray(page.items)
    || page.items.length === 0
    || !page.items.every((item) => (
      item?.type === "Annotation"
      && typeof item.id === "string"
      && item.id.startsWith(`${expectedID}/items/`)
    ))
  ) {
    throw new Error("Presentation annotation page identity failed");
  }
  assertTextualAnnotationPage(page);
}

function positiveID(value) {
  const normalized = String(value ?? "").trim();
  return /^[1-9][0-9]*$/.test(normalized) ? normalized : "";
}

function workspaceHeaders(workspaceID) {
  if (!positiveID(workspaceID)) throw new Error("invalid workspace identity");
  return {
    "Connect-Protocol-Version": "1",
    "Content-Type": "application/json",
    "X-Scribe-Workspace-ID": workspaceID,
  };
}

function remainingCleanupTimeMs(recoveryDeadline) {
  const remaining = Math.floor(recoveryDeadline - Date.now());
  if (remaining <= 0) throw new Error("cleanup reconciliation deadline exceeded");
  return remaining;
}

async function connectPOST(path, data, workspaceID, recoveryDeadline) {
  const options = {
    data,
    headers: workspaceHeaders(workspaceID),
    timeout: stageTimeoutMs,
  };
  if (recoveryDeadline !== undefined) {
    options.timeout = remainingCleanupTimeMs(recoveryDeadline);
  }
  return browserContext.request.post(new URL(path, baseURL).href, options);
}

const responseJSONSnapshots = new WeakMap();
const navigationResponseJSONSnapshots = new WeakMap();
const navigationResponseJSONPaths = new Set(["/scribe.v1.ItemService/StartUploadBatch", "/scribe.v1.ItemService/UploadItemImage", "/scribe.v1.ItemService/ImportManifest"]);
const maxNavigationResponseJSONBytes = 1024 * 1024;

async function snapshotNavigationResponseJSON(response) {
  let contentType = "";
  let declaredLengthHeader = "";
  try {
    contentType = String(response.headers()["content-type"] ?? "").trim();
    declaredLengthHeader = String(response.headers()["content-length"] ?? "").trim();
  } catch {
    return { ok: false };
  }
  let declaredLength = 0;
  if (declaredLengthHeader) {
    declaredLength = Number(declaredLengthHeader);
  }
  if (
    !/^application\/json(?:\s*;|$)/iu.test(contentType)
    || (declaredLengthHeader && (
      !/^[0-9]+$/u.test(declaredLengthHeader)
      || !Number.isSafeInteger(declaredLength)
      || declaredLength > maxNavigationResponseJSONBytes
    ))
  ) {
    return { ok: false };
  }
  let body;
  try {
    body = await response.body();
  } catch {
    return { ok: false };
  }
  if (
    body.byteLength === 0
    || body.byteLength > maxNavigationResponseJSONBytes
  ) return { ok: false };
  try {
    return { ok: true, payload: JSON.parse(body.toString("utf8")) };
  } catch {
    return { ok: false };
  }
}

async function responseJSON(response, failureMessage) {
  const snapshot = responseJSONSnapshots.get(response);
  if (snapshot) {
    const result = await snapshot;
    if (!result.ok) throw new Error(failureMessage);
    return result.payload;
  }
  try {
    return await response.json();
  } catch {
    throw new Error(failureMessage);
  }
}

async function listItemSummaries(workspaceID, query = "", recoveryDeadline) {
  const items = [];
  const seenTokens = new Set();
  let pageToken = "";
  let pageCount = 0;
  do {
    pageCount += 1;
    if (pageCount > cleanupMaxItemPages) throw new Error("item reconciliation page bound exceeded");
    const response = await connectPOST(
      "/scribe.v1.ItemService/ListItems",
      { pageSize: 100, pageToken, query },
      workspaceID,
      recoveryDeadline,
    );
    if (!response.ok()) throw new Error("item reconciliation request failed");
    const payload = await responseJSON(response, "invalid item reconciliation response");
    if (!Array.isArray(payload?.items)) throw new Error("invalid item reconciliation response");
    items.push(...payload.items);
    if (items.length > cleanupMaxItems) throw new Error("item reconciliation result bound exceeded");
    const nextPageToken = String(payload.nextPageToken ?? "");
    if (nextPageToken && seenTokens.has(nextPageToken)) {
      throw new Error("item reconciliation pagination repeated");
    }
    if (nextPageToken) seenTokens.add(nextPageToken);
    pageToken = nextPageToken;
  } while (pageToken);
  return items;
}

async function getItemForCleanup(itemID, workspaceID, recoveryDeadline) {
  const response = await connectPOST(
    "/scribe.v1.ItemService/GetItem",
    { itemId: itemID },
    workspaceID,
    recoveryDeadline,
  );
  if (response.status() === 404) return undefined;
  if (!response.ok()) throw new Error("item cleanup verification failed");
  const payload = await responseJSON(response, "invalid item cleanup verification response");
  return payload?.item;
}

async function deleteItemDirect(itemID, workspaceID, recoveryDeadline) {
  const response = await connectPOST(
    "/scribe.v1.ItemService/DeleteItem",
    { itemId: itemID },
    workspaceID,
    recoveryDeadline,
  );
  if (!response.ok() && response.status() !== 404) throw new Error("item cleanup request failed");
}

async function exactUploadItems(fixtureName, workspaceID, recoveryDeadline) {
  const summaries = await listItemSummaries(workspaceID, fixtureName, recoveryDeadline);
  return summaries.filter((item) => item?.name === fixtureName);
}

async function exactManifestItems(workspaceID, protectedItemIDs = new Set(), recoveryDeadline) {
  // A lost ImportManifest response may leave no usable display-name clue.
  // Scan the capped workspace inventory, then verify the full source tuple.
  const summaries = await listItemSummaries(workspaceID, "", recoveryDeadline);
  const matches = [];
  for (const summary of summaries) {
    const summaryID = String(summary?.id ?? "").trim();
    if (
      summary?.sourceType !== "manifest"
      || !summaryID
      || protectedItemIDs.has(summaryID)
    ) continue;
    // Deliberately sequential: this bounded cleanup reconciliation must verify
    // each full Item's source URL before any destructive request is issued.
    // eslint-disable-next-line no-await-in-loop
    const item = await getItemForCleanup(summaryID, workspaceID, recoveryDeadline);
    if (item?.sourceType === "manifest" && item?.sourceUrl === manifestURL) matches.push(item);
  }
  return matches;
}

function newMutationObservation() {
  return {
    latestRequestAt: 0,
    requestCount: 0,
    responseSettled: true,
    validated: false,
    pendingRequests: new Set(),
  };
}

function observeMutationRequest(observation, request) {
  observation.latestRequestAt = Date.now();
  observation.requestCount += 1;
  observation.responseSettled = false;
  observation.validated = false;
  observation.pendingRequests.add(request);
}

function settleMutationRequest(observation, request) {
  observation.pendingRequests.delete(request);
  observation.responseSettled = observation.pendingRequests.size === 0;
}

async function waitForMutationResponsesToSettle(observation) {
  const deadline = Date.now() + stageTimeoutMs;
  while (!observation.responseSettled && Date.now() < deadline) {
    // eslint-disable-next-line no-await-in-loop
    await new Promise((resolve) => setTimeout(resolve, 25));
  }
  if (!observation.responseSettled) throw new Error("mutation response did not settle");
}

function requireTerminalRequestSuccess(response, failureMessage) {
  if (response.request().failure() !== null) throw new Error(failureMessage);
}

async function reconcileExactResources(
  observation,
  cleanupDeadline,
  findMatches,
  validateAndDelete,
) {
  const uncertainOutcome = observation.latestRequestAt > 0
    && (!observation.responseSettled || !observation.validated);
  const commitHorizon = uncertainOutcome
    ? observation.latestRequestAt + cleanupCommitHorizonMs
    : Date.now();
  const resourceRecoveryDeadline = Math.max(commitHorizon, Date.now()) + cleanupRecoveryTailMs;
  const recoveryDeadline = Math.min(resourceRecoveryDeadline, cleanupDeadline);
  let stablePasses = 0;

  while (Date.now() < recoveryDeadline) {
    // Deliberately sequential: every exact match is verified before deletion,
    // and uncertainty keeps reconciliation active through the commit horizon.
    // eslint-disable-next-line no-await-in-loop
    const matches = await findMatches(recoveryDeadline);
    remainingCleanupTimeMs(recoveryDeadline);
    for (const match of matches) {
      // eslint-disable-next-line no-await-in-loop
      await validateAndDelete(match, recoveryDeadline);
      remainingCleanupTimeMs(recoveryDeadline);
    }

    if (Date.now() >= commitHorizon && matches.length === 0) {
      stablePasses += 1;
      if (stablePasses >= cleanupStablePasses) return;
    } else {
      stablePasses = 0;
    }

    const remainingToHorizon = Math.max(0, commitHorizon - Date.now());
    const delayMs = remainingToHorizon > 0
      ? Math.min(cleanupPollIntervalMs, remainingToHorizon)
      : cleanupPollIntervalMs;
    const boundedDelayMs = Math.min(delayMs, remainingCleanupTimeMs(recoveryDeadline));
    // eslint-disable-next-line no-await-in-loop
    await new Promise((resolve) => setTimeout(resolve, boundedDelayMs));
  }
  throw new Error("cleanup reconciliation did not stabilize");
}

async function cleanupExactUploadItems(
  fixtureName,
  workspaceID,
  knownItemID,
  observation,
  cleanupDeadline,
) {
  await reconcileExactResources(observation, cleanupDeadline, async (recoveryDeadline) => {
    const matches = await exactUploadItems(fixtureName, workspaceID, recoveryDeadline);
    const candidates = new Map(matches.map((item) => [String(item.id ?? ""), item]));
    if (knownItemID && !candidates.has(knownItemID)) {
      const item = await getItemForCleanup(knownItemID, workspaceID, recoveryDeadline);
      if (item) candidates.set(knownItemID, item);
    }
    return [...candidates.values()];
  }, async (item, recoveryDeadline) => {
    const itemID = String(item?.id ?? "");
    if (!itemID || item?.name !== fixtureName || item?.sourceType !== "upload") {
      throw new Error("upload cleanup identity mismatch");
    }
    await deleteItemDirect(itemID, workspaceID, recoveryDeadline);
  });
}

async function cleanupExactManifestItems(
  workspaceID,
  knownItemID,
  observation,
  protectedItemIDs,
  cleanupDeadline,
) {
  await reconcileExactResources(observation, cleanupDeadline, async (recoveryDeadline) => {
    const matches = await exactManifestItems(workspaceID, protectedItemIDs, recoveryDeadline);
    const candidates = new Map(matches.map((item) => [String(item.id ?? ""), item]));
    if (
      knownItemID
      && !protectedItemIDs.has(knownItemID)
      && !candidates.has(knownItemID)
    ) {
      const item = await getItemForCleanup(knownItemID, workspaceID, recoveryDeadline);
      if (item) candidates.set(knownItemID, item);
    }
    return [...candidates.values()];
  }, async (item, recoveryDeadline) => {
    const itemID = String(item?.id ?? "");
    if (
      !itemID
      || protectedItemIDs.has(itemID)
      || item?.sourceType !== "manifest"
      || item?.sourceUrl !== manifestURL
    ) {
      throw new Error("manifest cleanup identity mismatch");
    }
    await deleteItemDirect(itemID, workspaceID, recoveryDeadline);
  });
}

async function exactAPIKeys(keyName, workspaceID, recoveryDeadline) {
  const response = await connectPOST(
    "/scribe.v1.AuthService/ListAPIKeys",
    {},
    workspaceID,
    recoveryDeadline,
  );
  if (!response.ok()) throw new Error("token reconciliation request failed");
  const payload = await responseJSON(response, "invalid token reconciliation response");
  if (!Array.isArray(payload?.apiKeys)) throw new Error("invalid token reconciliation response");
  return payload.apiKeys.filter((key) => key?.name === keyName);
}

async function deleteAPIKeyDirect(keyID, workspaceID, recoveryDeadline) {
  const response = await connectPOST(
    "/scribe.v1.AuthService/DeleteAPIKey",
    { keyId: keyID },
    workspaceID,
    recoveryDeadline,
  );
  if (!response.ok() && response.status() !== 404) throw new Error("token cleanup request failed");
}

async function cleanupExactAPIKeys(keyName, workspaceID, observation, cleanupDeadline) {
  await reconcileExactResources(
    observation,
    cleanupDeadline,
    (recoveryDeadline) => exactAPIKeys(keyName, workspaceID, recoveryDeadline),
    async (key, recoveryDeadline) => {
      const keyID = positiveID(key?.id);
      if (!keyID || key?.name !== keyName || positiveID(key?.workspaceId) !== workspaceID) {
        throw new Error("token cleanup identity mismatch");
      }
      await deleteAPIKeyDirect(keyID, workspaceID, recoveryDeadline);
    },
  );
}

const retryableUploadConnectCodes = new Set([
  "aborted",
  "already_exists",
  "deadline_exceeded",
  "internal",
  "resource_exhausted",
  "unavailable",
  "unknown",
]);
const retryableUploadGatewayStatuses = new Set([0, 408, 409, 425, 429, 500, 502, 503, 504]);

async function uploadAttemptIsRetryable(attempt) {
  const outcome = attempt?.outcome;
  if (!outcome || outcome.kind === "transport") return outcome?.status === 0;
  if (outcome.response?.ok()) return false;
  let code = "";
  try {
    const payload = await responseJSON(outcome.response, "invalid retryable upload response");
    code = String(payload?.code ?? "").toLowerCase();
  } catch {
    // A proxy-generated retryable status may not carry a Connect error body.
  }
  return retryableUploadConnectCodes.has(code)
    || (code === "" && retryableUploadGatewayStatuses.has(outcome.status));
}

async function requireUploadAttemptEvidence() {
  if (uploadImageAttempts.length < 1 || uploadImageAttempts.length > 3) {
    throw new Error("upload retry bound failed");
  }
  for (const attempt of uploadImageAttempts.slice(0, -1)) {
    // eslint-disable-next-line no-await-in-loop
    if (!await uploadAttemptIsRetryable(attempt)) throw new Error("upload retried a terminal failure");
  }
  const finalAttempt = uploadImageAttempts.at(-1);
  if (finalAttempt?.outcome?.kind !== "response" || !finalAttempt.outcome.response?.ok()) {
    throw new Error("upload image failed");
  }
  return finalAttempt.outcome.response;
}

async function loadTranscriptionJob(jobID, workspaceID) {
  const response = await connectPOST(
    "/scribe.v1.TranscriptionService/GetTranscriptionJob",
    { jobId: jobID },
    workspaceID,
  );
  if (!response.ok()) throw new Error("transcription job request failed");
  const payload = await responseJSON(response, "invalid transcription job response");
  return payload?.job;
}

async function createTranscriptionJob(itemImageID, contextID, workspaceID) {
  const response = await connectPOST(
    "/scribe.v1.TranscriptionService/CreateTranscriptionJob",
    { contextId: contextID, itemImageId: itemImageID },
    workspaceID,
  );
  if (!response.ok()) throw new Error("live transcription job request failed");
  const payload = await responseJSON(response, "invalid live transcription job response");
  const jobID = positiveID(payload?.jobId);
  if (!jobID) throw new Error("live transcription job response omitted its identity");
  return jobID;
}

function transcriptionJobStatus(job) {
  return String(job?.status ?? "").trim().toLowerCase();
}

function transcriptionJobCompleted(job) {
  const status = transcriptionJobStatus(job);
  return status === "3"
    || status === "completed"
    || status === "transcription_job_status_completed";
}

function transcriptionJobAttemptCompleted(attempt) {
  const outcome = String(attempt?.outcome ?? "").trim().toLowerCase();
  return outcome === "2"
    || outcome === "completed"
    || outcome === "transcription_job_attempt_outcome_completed";
}

function exactCompletedAttemptResultRevision(job, jobID) {
  const completedAttempts = Array.isArray(job?.attempts)
    ? job.attempts.filter((attempt) => (
      positiveID(attempt?.jobId) === jobID
      && positiveID(attempt?.attemptNumber)
      && transcriptionJobAttemptCompleted(attempt)
      && positiveID(attempt?.resultRevision)
    ))
    : [];
  return completedAttempts.length === 1
    ? positiveID(completedAttempts[0].resultRevision)
    : "";
}

function transcriptionJobTerminal(job) {
  const status = transcriptionJobStatus(job);
  return transcriptionJobCompleted(job)
    || status === "4"
    || status === "5"
    || status === "6"
    || status === "failed"
    || status === "canceled"
    || status === "superseded"
    || status === "transcription_job_status_failed"
    || status === "transcription_job_status_canceled"
    || status === "transcription_job_status_superseded";
}

async function waitForTerminalTranscriptionJob(jobID, workspaceID) {
  const deadline = Date.now() + transcriptionTimeoutMs;
  let pollDelayMs = 500;
  while (Date.now() < deadline) {
    // Deliberately sequential: the editor asset remains paused until the
    // durable worker has committed or terminated this exact job.
    // eslint-disable-next-line no-await-in-loop
    const job = await loadTranscriptionJob(jobID, workspaceID);
    if (transcriptionJobTerminal(job)) return job;
    // eslint-disable-next-line no-await-in-loop
    await new Promise((resolve) => setTimeout(resolve, pollDelayMs));
    pollDelayMs = Math.min(pollDelayMs * 2, 2_000);
  }
  throw new Error("transcription job did not become terminal before editor load");
}

async function loadCanonicalAnnotationSnapshot(itemImageID, workspaceID) {
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
  const responseItemImageID = positiveID(payload?.itemImageId);
  const revision = positiveID(payload?.revision);
  const canvasURI = String(payload?.canvasUri ?? "").trim();
  if (
    responseItemImageID !== itemImageID
    || !revision
    || !canvasURI
    || typeof payload?.annotationPageJson !== "string"
  ) {
    throw new Error("canonical annotation response omitted the page");
  }
  try {
    return {
      canvasURI,
      itemImageID: responseItemImageID,
      page: JSON.parse(payload.annotationPageJson),
      revision,
    };
  } catch {
    throw new Error("invalid canonical annotation page");
  }
}

async function loadEditorManifest(itemImageID, workspaceID) {
  const response = await connectPOST(
    "/scribe.v1.ItemService/GetEditorManifest",
    { itemImageId: itemImageID },
    workspaceID,
  );
  if (!response.ok()) throw new Error("editor Manifest request failed");
  const payload = await responseJSON(response, "invalid editor Manifest response");
  const selectedCanvasID = String(payload?.selectedCanvasId ?? "").trim();
  if (
    !payload?.item
    || !selectedCanvasID
    || typeof payload.manifestJson !== "string"
    || payload.manifestJson.length === 0
  ) {
    throw new Error("editor Manifest response omitted its identity");
  }
  let manifest;
  try {
    manifest = JSON.parse(payload.manifestJson);
  } catch {
    throw new Error("invalid editor Manifest JSON");
  }
  if (manifest?.type !== "Manifest" || !Array.isArray(manifest.items)) {
    throw new Error("editor Manifest omitted its Canvases");
  }
  return { item: payload.item, manifest, selectedCanvasID };
}

function httpResourceID(resource) {
  const raw = String(resource?.id ?? resource?.["@id"] ?? "").trim();
  try {
    const parsed = new URL(raw);
    return parsed.protocol === "http:" || parsed.protocol === "https:" ? parsed.href : "";
  } catch {
    return "";
  }
}

function imageBodies(value, depth = 0) {
  if (depth > 4) return [];
  if (Array.isArray(value)) {
    return value.slice(0, 16).flatMap((entry) => imageBodies(entry, depth + 1));
  }
  if (!value || typeof value !== "object") return [];
  const nested = value.type === "Choice" || value["@type"] === "oa:Choice"
    ? imageBodies(value.items ?? value.item ?? value.default, depth + 1)
    : [];
  return httpResourceID(value) ? [value, ...nested] : nested;
}

function editorCanvasImageResource(manifest, canvasID) {
  const canvas = manifest.items.find((candidate) => (
    String(candidate?.id ?? candidate?.["@id"] ?? "").trim() === canvasID
  ));
  if (!canvas || !Array.isArray(canvas.items)) {
    throw new Error("editor Manifest omitted the selected Canvas");
  }
  const paintingAnnotations = canvas.items
    .slice(0, 16)
    .flatMap((annotationPage) => (
      Array.isArray(annotationPage?.items) ? annotationPage.items.slice(0, 16) : []
    ))
    .filter((annotation) => {
      const motivation = Array.isArray(annotation?.motivation)
        ? annotation.motivation
        : [annotation?.motivation];
      return motivation.includes("painting");
    });
  const bodies = paintingAnnotations.flatMap((annotation) => imageBodies(annotation?.body));
  const bodyIDs = [...new Set(bodies.map(httpResourceID).filter(Boolean))];
  const serviceIDs = [...new Set(bodies.flatMap((body) => {
    const services = Array.isArray(body?.service) ? body.service : [body?.service];
    return services.slice(0, 16).map(httpResourceID).filter(Boolean);
  }))];
  if (bodyIDs.length === 0) throw new Error("editor Canvas omitted its painting image");
  return { bodyIDs, canvasID, serviceIDs };
}

function responseMatchesImageResource(response, resource) {
  if (!response.ok() || response.request().resourceType() !== "image") return false;
  let responseURL;
  try {
    responseURL = new URL(response.url()).href;
  } catch {
    return false;
  }
  return resource.bodyIDs.some((bodyID) => (
    responseURL === bodyID || responseURL.startsWith(`${bodyID}?`)
  )) || resource.serviceIDs.some((serviceID) => (
    responseURL.startsWith(`${serviceID.replace(/\/$/, "")}/`)
  ));
}

async function assertOpenSeadragonCanvas() {
  await page.locator(".openseadragon-canvas").waitFor({ state: "visible" });
  await page.waitForFunction(() => {
    const viewer = document.getElementById("mirador-viewer");
    const osd = viewer?.querySelector(".openseadragon-canvas");
    if (!(osd instanceof HTMLElement)) return false;
    const bounds = osd.getBoundingClientRect();
    const canvases = Array.from(osd.querySelectorAll("canvas"));
    return bounds.width > 0
      && bounds.height > 0
      && canvases.some((canvas) => canvas.width > 0 && canvas.height > 0);
  });
}

async function requireLoadedOpenSeadragonImage(resource, action) {
  const observed = successfulImageResponses.find((response) => (
    responseMatchesImageResource(response, resource)
  ));
  const pendingResponse = observed
    ? undefined
    : page.waitForResponse(
      (response) => responseMatchesImageResource(response, resource),
      { timeout: stageTimeoutMs },
    );
  if (action) await action();
  const imageResponse = observed ?? await pendingResponse;
  await imageResponse.finished();
  const responseHeaders = imageResponse.headers();
  const declaredLength = Number(responseHeaders["content-length"] ?? 0);
  if (Number.isFinite(declaredLength) && declaredLength > maxReadinessImageBytes) {
    throw new Error("OpenSeadragon image response exceeded the readiness bound");
  }
  const imageBody = await imageResponse.body();
  const contentType = String(responseHeaders["content-type"] ?? "").toLowerCase();
  if (
    imageBody.byteLength === 0
    || imageBody.byteLength > maxReadinessImageBytes
    || !contentType.startsWith("image/")
  ) {
    throw new Error("OpenSeadragon image response was empty or invalid");
  }
  await assertOpenSeadragonCanvas();
}

async function waitForActiveCanvasIdentity(expected) {
  await page.waitForFunction((identity) => {
    const activeCanvas = globalThis.__scribeReadinessActiveCanvas;
    const url = new URL(window.location.href);
    return url.pathname === "/editor"
      && url.searchParams.get("itemId") === identity.itemID
      && url.searchParams.get("itemImageId") === identity.itemImageID
      && url.searchParams.get("workspace_id") === identity.workspaceID
      && activeCanvas?.canvasId === identity.canvasID
      && activeCanvas?.itemImageId === identity.itemImageID
      && activeCanvas?.windowId === "scribe-editor-window";
  }, expected, { timeout: stageTimeoutMs });
}

async function waitForPublishedAnnotationPage(annotationPath) {
  const deadline = Date.now() + stageTimeoutMs;
  while (Date.now() < deadline) {
    const response = await browserContext.request.get(new URL(annotationPath, baseURL).href, {
      timeout: Math.max(1, deadline - Date.now()),
    });
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

async function assertItemDeletePresentation(rootSelector, itemID, itemName) {
  const root = page.locator(rootSelector);
  const itemDelete = await findActionByValue(root, "data-item-delete", itemID);
  if (!itemDelete) throw new Error("missing item delete action");
  const presentation = await itemDelete.evaluate((button, expectedLabel) => {
    const svg = button.querySelector("svg");
    return {
      ariaLabel: button.getAttribute("aria-label") ?? "",
      destructive: button.classList.contains("bg-destructive"),
      exactText: button.textContent?.trim() === "Delete",
      finalAction: button.parentElement?.lastElementChild === button,
      trashIcon: button.querySelectorAll('svg[aria-hidden="true"]').length === 1
        && button.querySelectorAll("svg").length === 1
        && svg?.querySelector("path")?.getAttribute("d")
          === "M3 6h18M8 6V4h8v2m3 0-1 14H6L5 6m4 4v6m6-6v6",
      expectedLabel,
    };
  }, `Delete item ${itemName}`);
  if (
    presentation.ariaLabel !== presentation.expectedLabel
    || !presentation.destructive
    || !presentation.exactText
    || !presentation.finalAction
    || !presentation.trashIcon
  ) {
    throw new Error("item delete action presentation failed");
  }
}

function armItemDeleteDialog(itemID) {
  if (expectedItemDeleteDialog) throw new Error("item delete confirmation already armed");
  let resolve;
  let reject;
  const accepted = new Promise((promiseResolve, promiseReject) => {
    resolve = promiseResolve;
    reject = promiseReject;
  });
  expectedItemDeleteDialog = {
    message: `Delete item "${itemID}"?`,
    reject,
    resolve,
  };
  return accepted;
}

async function deleteItemThroughLibrary(rootSelector, itemID, itemName) {
  if (!itemID || !itemName) throw new Error("missing item delete identity");
  await page.waitForFunction(({ attributeValue, root }) => (
    Array.from(document.querySelectorAll(`${root} [data-item-delete]`)).some((button) => (
      button.getAttribute("data-item-delete") === attributeValue
    ))
  ), { attributeValue: itemID, root: rootSelector }, { timeout: stageTimeoutMs });
  await assertItemDeletePresentation(rootSelector, itemID, itemName);
  const itemDelete = await findActionByValue(page.locator(rootSelector), "data-item-delete", itemID);
  if (!itemDelete) throw new Error("missing item delete action");
  const dialogAccepted = armItemDeleteDialog(itemID);
  const deleteResponse = page.waitForResponse((response) => {
    if (!sameOriginConnectResponse(response, "/scribe.v1.ItemService/DeleteItem")) return false;
    try {
      return String(response.request().postDataJSON()?.itemId ?? "") === itemID;
    } catch {
      return false;
    }
  }, { timeout: stageTimeoutMs });
  const [response] = await Promise.all([
    deleteResponse,
    dialogAccepted,
    itemDelete.click(),
  ]);
  if (!response.ok()) throw new Error("item delete request failed");
  await page.waitForFunction((deletedItemID) => (
    !Array.from(document.querySelectorAll("[data-item-delete]")).some((button) => (
      button.getAttribute("data-item-delete") === deletedItemID
    ))
  ), itemID, { timeout: stageTimeoutMs });
}

function sameOriginConnectResponse(response, path) {
  let responseURL;
  try {
    responseURL = new URL(response.url());
  } catch {
    return false;
  }
  return responseURL.origin === baseURL.origin
    && responseURL.pathname === path
    && response.request().method() === "POST";
}

async function requireConnectAction(path, action) {
  const responsePromise = page.waitForResponse((response) => sameOriginConnectResponse(response, path));
  await action();
  const response = await responsePromise;
  if (!response.ok()) throw new Error("editor transform request failed");
}

async function waitForSaveEnabled() {
  await page.waitForFunction(() => {
    const buttons = Array.from(document.querySelectorAll('button[aria-label="Save"]'));
    return buttons.length === 1
      && buttons[0] instanceof HTMLButtonElement
      && !buttons[0].disabled;
  });
}

async function currentEditorAnnotationCount() {
  await page.waitForFunction(() => (
    Number.isInteger(globalThis.__scribeReadinessEditorState?.annotationCount)
  ));
  return page.evaluate(() => globalThis.__scribeReadinessEditorState.annotationCount);
}

async function waitForEditorAnnotationCount(expected) {
  await page.waitForFunction((count) => (
    globalThis.__scribeReadinessEditorState?.annotationCount === count
  ), expected);
}

async function waitForEditorAnnotationCountDirection(previous, direction) {
  const handle = await page.waitForFunction(({ count, expectedDirection }) => {
    const current = globalThis.__scribeReadinessEditorState?.annotationCount;
    if (!Number.isInteger(current)) return undefined;
    if (expectedDirection === "increase" && current > count) return current;
    if (expectedDirection === "decrease" && current < count) return current;
    return undefined;
  }, { count: previous, expectedDirection: direction });
  return handle.jsonValue();
}

async function selectAllAdditionalCandidates(dialog, candidatePattern) {
  const candidates = dialog.getByRole("button", { name: candidatePattern });
  const count = await candidates.count();
  let selected = 0;
  for (let index = 0; index < count; index += 1) {
    const candidate = candidates.nth(index);
    const checkbox = candidate.locator('input[type="checkbox"]');
    if (await checkbox.count() !== 1 || await checkbox.isDisabled() || await checkbox.isChecked()) continue;
    // eslint-disable-next-line no-await-in-loop
    await candidate.click();
    selected += 1;
  }
  if (selected === 0) throw new Error("structural dialog omitted an additional candidate");
}

async function selectFirstAdditionalCandidate(dialog, candidatePattern) {
  const candidates = dialog.getByRole("button", { name: candidatePattern });
  const count = await candidates.count();
  for (let index = 0; index < count; index += 1) {
    const candidate = candidates.nth(index);
    const checkbox = candidate.locator('input[type="checkbox"]');
    if (await checkbox.count() !== 1 || await checkbox.isDisabled() || await checkbox.isChecked()) continue;
    await candidate.click();
    return;
  }
  throw new Error("structural dialog omitted an additional candidate");
}

let category = "home";
let failureCategory;
let browserFaultCategory;
let browser;
let browserContext;
let page;
let baseURL;
let createdItemID;
let createdManifestItemID;
let createdManifestItemName;
let createdWorkspaceID;
let fixtureName;
let createdAPIKeyName;
let createdAPIKeyID;
let manifestImportAttempted = false;
let manifestReprocessRequestCount = 0;
let enrichAnnotationRequestCount = 0;
let editorAssetDelayObserved = false;
let editorAssetDelayReachedCompletion = false;
let editorAssetDelayFailed = false;
let mainScenarioWatchdogTimer;
let mainScenarioTimedOut = false;
let watchdogPageClose;
let expectedItemDeleteDialog;
const manifestBaselineItemIDs = new Set();
const successfulImageResponses = [];
const startUploadResponses = [];
const uploadImageAttempts = [];
const uploadImageAttemptByRequest = new WeakMap();
const uploadMutation = newMutationObservation();
const manifestMutation = newMutationObservation();
const tokenMutation = newMutationObservation();

function assertBrowserHealthy() {
  if (browserFaultCategory) throw new Error("browser fault");
}

function assertMainScenarioActive() {
  if (mainScenarioTimedOut) throw new Error("main scenario deadline exceeded");
}

function mutationObservationForPath(pathname) {
  if (
    pathname === "/scribe.v1.ItemService/StartUploadBatch"
    || pathname === "/scribe.v1.ItemService/UploadItemImage"
  ) return uploadMutation;
  if (pathname === "/scribe.v1.ItemService/ImportManifest") return manifestMutation;
  if (pathname === "/scribe.v1.AuthService/CreateAPIKey") return tokenMutation;
  return undefined;
}

async function navigate(path, requireHealthy = true) {
  await page.goto(path, { waitUntil: "domcontentloaded" });
  if (requireHealthy) assertBrowserHealthy();
}

async function assertResponsiveEditorGeometry(width, height, minimumImageHeight) {
  const deadline = Date.now() + 10_000;
  do {
    const geometry = await page.locator('[data-scribe-action-panel="true"]').evaluate((panel) => {
      const parent = panel.parentElement;
      const companion = parent?.parentElement;
      const viewer = document.getElementById("mirador-viewer");
      const osd = viewer?.querySelector(".openseadragon-canvas");
      const panelBounds = panel.getBoundingClientRect();
      const parentBounds = parent?.getBoundingClientRect();
      const viewerBounds = viewer?.getBoundingClientRect();
      const osdBounds = osd?.getBoundingClientRect();
      const osdCanvases = osd instanceof HTMLCanvasElement
        ? [osd]
        : Array.from(osd?.querySelectorAll("canvas") ?? []);
      const actionGroups = [
        panel.querySelector('[role="group"][aria-label="View and modes"]'),
        panel.querySelector('[role="group"][aria-label="Text and page actions"]'),
      ];
      const primaryActions = actionGroups.flatMap((group) => (
        Array.from(group?.querySelectorAll("button[aria-label]") ?? [])
      ));
      const primaryActionsVisible = primaryActions.every((button) => {
        const bounds = button.getBoundingClientRect();
        const style = getComputedStyle(button);
        return bounds.width > 0
          && bounds.height > 0
          && bounds.left >= Math.max(0, panelBounds.left) - 1
          && bounds.right <= Math.min(window.innerWidth, panelBounds.right) + 1
          && bounds.top >= Math.max(0, panelBounds.top) - 1
          && bounds.bottom <= Math.min(window.innerHeight, panelBounds.bottom) + 1
          && style.display !== "none"
          && style.visibility !== "hidden";
      });
      return {
        companionHeight: companion?.getBoundingClientRect().height ?? 0,
        osdHasPixels: osdCanvases.some((canvas) => canvas.width > 0 && canvas.height > 0),
        osdImageHeight: osdBounds?.height ?? 0,
        pageOverflow: document.documentElement.scrollWidth > window.innerWidth
          || document.documentElement.scrollHeight > window.innerHeight,
        panelClientHeight: panel.clientHeight,
        panelClientWidth: panel.clientWidth,
        panelScrollTop: panel.scrollTop,
        panelScrollWidth: panel.scrollWidth,
        parentClientHeight: parent?.clientHeight ?? 0,
        parentClientWidth: parent?.clientWidth ?? 0,
        parentScrollTop: parent?.scrollTop ?? 0,
        parentScrollWidth: parent?.scrollWidth ?? 0,
        panelWithinParent: Boolean(parentBounds)
          && panelBounds.top >= parentBounds.top - 1
          && panelBounds.bottom <= parentBounds.bottom + 1,
        panelWithinViewer: Boolean(viewerBounds)
          && panelBounds.top >= viewerBounds.top - 1
          && panelBounds.bottom <= viewerBounds.bottom + 1,
        primaryActionCount: primaryActions.length,
        primaryActionsVisible,
        viewerClientHeight: viewer?.clientHeight ?? 0,
        viewerClientWidth: viewer?.clientWidth ?? 0,
        viewportHeight: window.innerHeight,
        viewportWidth: window.innerWidth,
      };
    });
    const expectedPaneHeight = bottomPaneHeightForViewport({
      height: geometry.viewerClientHeight,
      width: geometry.viewerClientWidth,
    });
    if (
      !geometry.pageOverflow
      && geometry.viewportWidth === width
      && geometry.viewportHeight === height
      && geometry.viewerClientHeight >= Math.min(500, height - 180)
      && geometry.panelClientHeight > 0
      && geometry.parentClientHeight > 0
      && Math.abs(geometry.panelClientHeight - geometry.parentClientHeight) <= 2
      && geometry.panelClientWidth > 0
      && geometry.parentClientWidth > 0
      && geometry.panelScrollTop === 0
      && geometry.parentScrollTop === 0
      && geometry.panelScrollWidth <= geometry.panelClientWidth + 1
      && geometry.parentScrollWidth <= geometry.parentClientWidth + 1
      && geometry.panelWithinParent
      && geometry.panelWithinViewer
      && geometry.primaryActionCount === 14
      && geometry.primaryActionsVisible
      && geometry.osdHasPixels
      && geometry.osdImageHeight >= minimumImageHeight
      && Math.abs(geometry.companionHeight - expectedPaneHeight) <= 1
    ) return;
    await page.waitForTimeout(100);
  } while (Date.now() < deadline);
  throw new Error("editor action panel geometry failed");
}

try {
  const mainScenarioWatchdog = new Promise((_, reject) => {
    const watchdogDelayMs = Math.max(0, mainScenarioDeadline - Date.now());
    mainScenarioWatchdogTimer = setTimeout(() => {
      mainScenarioTimedOut = true;
      if (page && !page.isClosed()) {
        watchdogPageClose = page.close({ runBeforeUnload: false }).catch(() => undefined);
      }
      reject(new Error("main scenario deadline exceeded"));
    }, watchdogDelayMs);
  });
  const mainScenario = (async () => {
  baseURL = previewBaseURL();
  if (!baseURL) throw new Error("invalid target");

  const chromiumIPv6Argument = await configureCanonicalIPv6Routing(baseURL.hostname);
  browser = await chromium.launch({
    args: [chromiumIPv6Argument],
    headless: true,
  });
  if (mainScenarioTimedOut) {
    await browser.close().catch(() => undefined);
    throw new Error("main scenario deadline exceeded");
  }
  browserContext = await browser.newContext({
    baseURL: baseURL.href,
    acceptDownloads: false,
  });
  assertMainScenarioActive();
  await browserContext.grantPermissions(
    ["clipboard-read", "clipboard-write"],
    { origin: baseURL.origin },
  );
  page = await browserContext.newPage();
  assertMainScenarioActive();
  await page.addInitScript(() => {
    globalThis.__scribeReadinessActiveCanvas = undefined;
    globalThis.__scribeReadinessEditorState = undefined;
    globalThis.__scribeReadinessAutomaticTranscription = {
      badges: [],
      overlayReady: false,
      results: [],
      segments: [],
    };
    const recordTranscriptionEvent = (kind, event) => {
      const detail = event.detail ?? {};
      if (!detail.annotation) return;
      const target = globalThis.__scribeReadinessAutomaticTranscription?.[kind];
      if (!Array.isArray(target) || target.length >= 100) return;
      target.push({
        annotationId: String(detail.annotation.id ?? ""),
        attemptNumber: Number(detail.attemptNumber ?? 0),
        canvasId: String(detail.canvasId ?? ""),
        catchUp: detail.catchUp === true,
        done: Number(detail.done ?? 0),
        jobId: String(detail.jobId ?? ""),
        total: Number(detail.total ?? 0),
      });
    };
    document.addEventListener("scribe:transcription-segment", (event) => {
      recordTranscriptionEvent("segments", event);
    });
    document.addEventListener("scribe:transcription-result", (event) => {
      recordTranscriptionEvent("results", event);
    });
    const observeTranscriptionBadge = () => {
      if (!document.documentElement) {
        setTimeout(observeTranscriptionBadge, 0);
        return;
      }
      let pendingFrame = 0;
      const record = () => {
        const badge = document.querySelector('[data-scribe-transcription-active="true"]');
        const target = globalThis.__scribeReadinessAutomaticTranscription?.badges;
        if (!(badge instanceof HTMLElement) || !Array.isArray(target) || target.length >= 100) return;
        const bounds = badge.getBoundingClientRect();
        const viewerBounds = document.getElementById("mirador-viewer")?.getBoundingClientRect();
        const style = getComputedStyle(badge);
        const sample = {
          annotationId: badge.dataset.scribeTranscriptionAnnotation ?? "",
          attemptNumber: Number(badge.dataset.scribeTranscriptionAttempt ?? 0),
          hasWand: badge.querySelector("svg") !== null,
          jobId: badge.dataset.scribeTranscriptionJob ?? "",
          line: Number(badge.dataset.scribeTranscriptionLine ?? -1),
          total: Number(badge.dataset.scribeTranscriptionTotal ?? -1),
          visible: bounds.width > 0
            && bounds.height > 0
            && bounds.right > 0
            && bounds.bottom > 0
            && bounds.left < window.innerWidth
            && bounds.top < window.innerHeight
            && style.display !== "none"
            && style.visibility !== "hidden"
            && Number(style.opacity || "1") > 0
            && Boolean(viewerBounds)
            && bounds.left >= viewerBounds.left - 1
            && bounds.right <= viewerBounds.right + 1
            && bounds.top >= viewerBounds.top - 1
            && bounds.bottom <= viewerBounds.bottom + 1,
        };
        const previous = target.at(-1);
        if (
          previous?.annotationId === sample.annotationId
          && previous?.attemptNumber === sample.attemptNumber
          && previous?.hasWand === sample.hasWand
          && previous?.jobId === sample.jobId
          && previous?.line === sample.line
          && previous?.total === sample.total
          && previous?.visible === sample.visible
        ) return;
        target.push(sample);
      };
      const scheduleRecord = () => {
        if (pendingFrame) return;
        pendingFrame = requestAnimationFrame(() => {
          pendingFrame = 0;
          record();
        });
      };
      new MutationObserver(scheduleRecord).observe(document.documentElement, {
        attributes: true,
        childList: true,
        subtree: true,
      });
      scheduleRecord();
    };
    observeTranscriptionBadge();
    document.addEventListener("scribe:transcription-overlay-state", (event) => {
      if (globalThis.__scribeReadinessAutomaticTranscription) {
        globalThis.__scribeReadinessAutomaticTranscription.overlayReady = event.detail?.ready === true;
      }
    });
    document.addEventListener("scribe:editor-state", (event) => {
      const annotationPage = event.detail?.annotationPage;
      globalThis.__scribeReadinessEditorState = {
        annotationCount: Array.isArray(annotationPage?.items) ? annotationPage.items.length : -1,
      };
    });
    document.addEventListener("scribe:active-canvas", (event) => {
      const detail = event.detail ?? {};
      globalThis.__scribeReadinessActiveCanvas = {
        canvasId: String(detail.canvasId ?? ""),
        itemImageId: String(detail.itemImageId ?? ""),
        windowId: String(detail.windowId ?? ""),
      };
    });
  });
  page.setDefaultTimeout(stageTimeoutMs);
  page.setDefaultNavigationTimeout(stageTimeoutMs);

  const navigationResponseJSONRoutePattern = /\/scribe\.v1\.ItemService\/(?:StartUploadBatch|UploadItemImage|ImportManifest)$/;
  const interceptNavigationResponseJSON = async (route) => {
    const request = route.request();
    let requestURL;
    try {
      requestURL = new URL(request.url());
    } catch {
      await route.continue();
      return;
    }
    if (
      requestURL.origin !== baseURL.origin
      || request.method() !== "POST"
      || !navigationResponseJSONPaths.has(requestURL.pathname)
    ) {
      await route.continue();
      return;
    }
    try {
      const upstreamResponse = await route.fetch({
        maxRedirects: 0,
        maxRetries: 0,
        timeout: uploadTimeoutMs,
      });
      const snapshot = await snapshotNavigationResponseJSON(upstreamResponse);
      navigationResponseJSONSnapshots.set(request, Promise.resolve(snapshot));
      await route.fulfill({ response: upstreamResponse });
    } catch {
      navigationResponseJSONSnapshots.set(
        request,
        Promise.resolve({ ok: false }),
      );
      await route.abort("failed").catch(() => undefined);
    }
  };
  await page.route(
    navigationResponseJSONRoutePattern,
    interceptNavigationResponseJSON,
  );

  const editorAssetPattern = /\/assets\/editor-[^/?]+\.js(?:\?.*)?$/;
  const delayEditorAssetUntilJobCompletes = async (route) => {
    try {
      const assetURL = new URL(route.request().url());
      const referer = route.request().headers().referer ?? page.url();
      const editorRoute = new URL(referer);
      const delayedJobID = positiveID(editorRoute.searchParams.get("jobId"));
      const delayedWorkspaceID = positiveID(editorRoute.searchParams.get("workspace_id"));
      if (
        assetURL.origin !== baseURL.origin
        || editorRoute.origin !== baseURL.origin
        || editorRoute.pathname !== "/editor"
        || !delayedJobID
        || !delayedWorkspaceID
      ) {
        throw new Error("editor asset request omitted the exact job identity");
      }
      editorAssetDelayObserved = true;
      const delayedJob = await waitForTerminalTranscriptionJob(
        delayedJobID,
        delayedWorkspaceID,
      );
      editorAssetDelayReachedCompletion = transcriptionJobCompleted(delayedJob);
    } catch {
      editorAssetDelayFailed = true;
    } finally {
      await route.continue();
    }
  };
  await page.route(editorAssetPattern, delayEditorAssetUntilJobCompletes);

  page.on("request", (request) => {
    let requestURL;
    try {
      requestURL = new URL(request.url());
    } catch {
      return;
    }
    if (requestURL.origin !== baseURL.origin || request.method() !== "POST") return;
    const observation = mutationObservationForPath(requestURL.pathname);
    if (observation) observeMutationRequest(observation, request);
    if (requestURL.pathname === "/scribe.v1.ItemService/UploadItemImage") {
      const attempt = { observedAt: Date.now(), outcome: undefined };
      uploadImageAttempts.push(attempt);
      uploadImageAttemptByRequest.set(request, attempt);
    }
    if (requestURL.pathname === "/scribe.v1.ImageProcessingService/ReprocessItemImage") {
      manifestReprocessRequestCount += 1;
    }
    if (requestURL.pathname === "/scribe.v1.AnnotationService/EnrichAnnotation") {
      enrichAnnotationRequestCount += 1;
    }
  });
  page.on("response", (response) => {
    let responseURL;
    try {
      responseURL = new URL(response.url());
    } catch {
      return;
    }
    if (
      category === "manifest"
      && response.ok()
      && response.request().resourceType() === "image"
    ) {
      successfulImageResponses.push(response);
      if (successfulImageResponses.length > maxObservedImageResponses) {
        successfulImageResponses.shift();
      }
    }
    const sameOriginPOST = responseURL.origin === baseURL.origin
      && response.request().method() === "POST";
    if (sameOriginPOST && navigationResponseJSONPaths.has(responseURL.pathname)) {
      const snapshot = navigationResponseJSONSnapshots.get(response.request())
        ?? Promise.resolve({ ok: false });
      responseJSONSnapshots.set(response, snapshot);
    }
    const isUploadImageResponse = sameOriginPOST
      && responseURL.pathname === "/scribe.v1.ItemService/UploadItemImage";
    if (isUploadImageResponse) {
      const attempt = uploadImageAttemptByRequest.get(response.request());
      if (attempt) {
        attempt.outcome = { kind: "response", response, status: response.status() };
      } else {
        browserFaultCategory ??= "upload";
      }
      return;
    }
    if (
      sameOriginPOST
      && responseURL.pathname === "/scribe.v1.ItemService/StartUploadBatch"
    ) {
      startUploadResponses.push(response);
    }
    if (responseURL.origin === baseURL.origin && response.status() >= 400) {
      browserFaultCategory ??= "network";
    }
  });
  page.on("requestfinished", (request) => {
    let requestURL;
    try {
      requestURL = new URL(request.url());
    } catch {
      return;
    }
    if (requestURL.origin !== baseURL.origin || request.method() !== "POST") return;
    const observation = mutationObservationForPath(requestURL.pathname);
    if (observation) settleMutationRequest(observation, request);
  });
  page.on("requestfailed", (request) => {
    let requestURL;
    try {
      requestURL = new URL(request.url());
    } catch {
      return;
    }
    const clientCancellation = /ERR_ABORTED|cancell?ed/i.test(request.failure()?.errorText ?? "");
    const sameOriginPOST = requestURL.origin === baseURL.origin && request.method() === "POST";
    const observation = sameOriginPOST
      ? mutationObservationForPath(requestURL.pathname)
      : undefined;
    if (observation) {
      observation.validated = false;
      settleMutationRequest(observation, request);
    }
    if (
      sameOriginPOST
      && requestURL.pathname === "/scribe.v1.ItemService/UploadItemImage"
    ) {
      const attempt = uploadImageAttemptByRequest.get(request);
      if (attempt) {
        attempt.outcome = { kind: "transport", status: 0 };
      } else {
        browserFaultCategory ??= "upload";
      }
      return;
    }
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
    if (
      expectedItemDeleteDialog
      && dialog.type() === "confirm"
      && dialog.message() === expectedItemDeleteDialog.message
    ) {
      const expectedDialog = expectedItemDeleteDialog;
      expectedItemDeleteDialog = undefined;
      void dialog.accept().then(expectedDialog.resolve).catch((error) => {
        browserFaultCategory ??= "token";
        expectedDialog.reject(error);
      });
      return;
    }
    if (expectedItemDeleteDialog) {
      const expectedDialog = expectedItemDeleteDialog;
      expectedItemDeleteDialog = undefined;
      expectedDialog.reject(new Error("unexpected item delete confirmation"));
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
  if (await contextSelect.inputValue() !== "0") throw new Error("default context was not selected");
  createdWorkspaceID = positiveID(await page.locator("#sidebar-workspace-select").inputValue());
  if (!createdWorkspaceID) throw new Error("missing workspace identity");
  assertBrowserHealthy();

  category = "upload";
  const fixture = exactReadinessSmokeFixture();
  fixtureName = `browser-readiness-${randomUUID()}.png`;
  await page.locator("#library-single-file").setInputFiles({
    name: fixtureName,
    mimeType: "image/png",
    buffer: fixture,
  });
  await page.locator("#shell-upload-dialog").waitFor({ state: "visible" });
  const uploadOutcome = await page.waitForFunction(() => {
    const currentURL = new URL(window.location.href);
    if (
      currentURL.pathname === "/editor"
      && /^[1-9][0-9]*$/.test(currentURL.searchParams.get("itemImageId") ?? "")
      && /^[1-9][0-9]*$/.test(currentURL.searchParams.get("jobId") ?? "")
    ) return "handoff";
    const dialog = document.getElementById("shell-upload-dialog");
    const closeAction = document.getElementById("shell-upload-cancel");
    if (
      dialog?.getAttribute("aria-hidden") === "false"
      && closeAction?.textContent?.trim() === "Close"
    ) return "terminal";
    return "";
  }, undefined, { timeout: uploadTimeoutMs });
  if (await uploadOutcome.jsonValue() !== "handoff") throw new Error("upload did not reach editor handoff");

  category = "handoff";
  const editorURL = new URL(page.url());
  const itemImageID = positiveID(editorURL.searchParams.get("itemImageId"));
  const jobID = positiveID(editorURL.searchParams.get("jobId"));
  const workspaceID = positiveID(editorURL.searchParams.get("workspace_id"));
  const handoffItemID = String(editorURL.searchParams.get("itemId") ?? "").trim();
  if (!itemImageID || !jobID || !handoffItemID) throw new Error("missing editor identity");
  if (!workspaceID || workspaceID !== createdWorkspaceID) throw new Error("mismatched workspace identity");

  if (startUploadResponses.length !== 1) throw new Error("upload start request count failed");
  const finalStartUploadResponse = startUploadResponses[0];
  if (!finalStartUploadResponse?.ok()) throw new Error("upload start failed");
  await waitForMutationResponsesToSettle(uploadMutation);
  if (!uploadMutation.responseSettled) throw new Error("upload response did not settle");
  requireTerminalRequestSuccess(finalStartUploadResponse, "upload start request failed");
  const startUploadPayload = await responseJSON(finalStartUploadResponse, "invalid upload start response");
  createdItemID = String(startUploadPayload?.item?.id ?? "").trim() || handoffItemID;
  if (
    createdItemID !== handoffItemID
    || startUploadPayload?.item?.name !== fixtureName
    || startUploadPayload?.item?.sourceType !== "upload"
  ) {
    throw new Error("mismatched editor identity");
  }

  const finalUploadImageResponse = await requireUploadAttemptEvidence();
  requireTerminalRequestSuccess(finalUploadImageResponse, "upload image request failed");
  const uploadImagePayload = await responseJSON(finalUploadImageResponse, "invalid upload image response");
  if (
    String(uploadImagePayload?.item?.id ?? "") !== createdItemID
    || positiveID(uploadImagePayload?.image?.id) !== itemImageID
    || positiveID(uploadImagePayload?.transcriptionJobId) !== jobID
  ) {
    throw new Error("upload response identity mismatch");
  }

  const durableJob = await loadTranscriptionJob(jobID, workspaceID);
  if (
    positiveID(durableJob?.id) !== jobID
    || positiveID(durableJob?.itemImageId) !== itemImageID
    || !positiveID(durableJob?.contextId)
  ) {
    throw new Error("default context did not resolve durably");
  }
  assertMainScenarioActive();
  uploadMutation.validated = true;
  assertBrowserHealthy();

  category = "transcription";
  await page.locator("#editor-transcription-status").waitFor({
    state: "attached",
    timeout: transcriptionTimeoutMs + stageTimeoutMs,
  });
  if (
    editorAssetDelayFailed
    || !editorAssetDelayObserved
    || !editorAssetDelayReachedCompletion
  ) {
    throw new Error("editor did not exercise completed-job transcription catch-up");
  }
  await page.unroute(editorAssetPattern, delayEditorAssetUntilJobCompletes);

  const completedDurableJob = await loadTranscriptionJob(jobID, workspaceID);
  const completedResultRevision = exactCompletedAttemptResultRevision(completedDurableJob, jobID);
  const canonicalSnapshot = await loadCanonicalAnnotationSnapshot(itemImageID, workspaceID);
  const annotationPage = canonicalSnapshot.page;
  assertTextualAnnotationPage(annotationPage);
  const canonicalLines = annotationPage.items.filter((annotation) => (
    String(annotation?.textGranularity ?? "").toLowerCase() === "line"
  ));
  const canonicalLineIDs = canonicalLines.map((annotation) => String(annotation?.id ?? "").trim());
  if (
    positiveID(completedDurableJob?.id) !== jobID
    || positiveID(completedDurableJob?.itemImageId) !== itemImageID
    || !transcriptionJobCompleted(completedDurableJob)
    || Number(completedDurableJob?.failedSegments ?? 0) !== 0
    || Number(completedDurableJob?.attemptCount ?? -1) !== 1
    || Number(completedDurableJob?.completedSegments ?? -1) !== 2
    || Number(completedDurableJob?.totalSegments ?? -1) !== 2
    || !completedResultRevision
    || canonicalSnapshot.revision !== completedResultRevision
    || canonicalSnapshot.itemImageID !== itemImageID
    || canonicalLines.length !== 2
    || canonicalLineIDs.some((annotationID) => !annotationID)
    || new Set(canonicalLineIDs).size !== 2
    || !canonicalLines.every(annotationHasText)
  ) {
    throw new Error("canonical transcription omitted exact completed line results");
  }

  const automaticTranscriptionProof = await page.waitForFunction((expected) => {
    const evidence = globalThis.__scribeReadinessAutomaticTranscription;
    const status = document.getElementById("editor-transcription-status")?.textContent?.trim() ?? "";
    if (status.startsWith("Batch transcription failed")) return "failed";
    if (status.startsWith("The completed transcription could not")) return "blocked";
    if (status === "Automatic transcription was canceled.") return "canceled";
    const segments = Array.isArray(evidence?.segments)
      ? evidence.segments.filter((segment) => segment.jobId === expected.jobID)
      : [];
    const results = Array.isArray(evidence?.results)
      ? evidence.results.filter((result) => result.jobId === expected.jobID)
      : [];
    const badges = Array.isArray(evidence?.badges)
      ? evidence.badges.filter((badge) => (
        badge.jobId === expected.jobID && badge.visible
      ))
      : [];
    const exactEvent = (sample) => (
      sample.canvasId === expected.canvasID
      && sample.attemptNumber === expected.attemptNumber
      && Number.isInteger(sample.done)
      && sample.done >= 1
      && sample.done <= 2
      && sample.total === 2
      && sample.catchUp
      && sample.annotationId === expected.lineIDs[sample.done - 1]
    );
    const progressesExactlyInOrder = (samples) => {
      if (samples.length < 2 || !samples.every(exactEvent)) return false;
      const seen = new Set();
      let previous = 0;
      for (const sample of samples) {
        if (sample.done < previous) return false;
        previous = sample.done;
        seen.add(sample.done);
      }
      return seen.size === 2 && seen.has(1) && seen.has(2);
    };
    const exactBadge = (badge) => (
      badge.hasWand
      && badge.attemptNumber === expected.attemptNumber
      && Number.isInteger(badge.line)
      && badge.line >= 1
      && badge.line <= 2
      && badge.total === 2
      && badge.annotationId === expected.lineIDs[badge.line - 1]
    );
    const badgeMovesExactlyInOrder = badges.length >= 2
      && badges.every(exactBadge)
      && badges.every((badge, index) => index === 0 || badge.line >= badges[index - 1].line)
      && badges.some((first, firstIndex) => (
        first.line === 1
        && first.annotationId === expected.lineIDs[0]
        && badges.slice(firstIndex + 1).some((second) => (
          second.line === 2
          && second.annotationId === expected.lineIDs[1]
        ))
      ));
    if (
      progressesExactlyInOrder(segments)
      && progressesExactlyInOrder(results)
      && badgeMovesExactlyInOrder
    ) return "proved";
    if (status === "Batch transcription complete. Updated text is now available in the editor.") {
      if (Date.now() < expected.visualDeadline) return "";
      if (!progressesExactlyInOrder(segments)) return "completed-without-segments";
      if (!progressesExactlyInOrder(results)) return "completed-without-results";
      if (badges.length === 0) return "completed-without-visible-badges";
      return "completed-without-badge-order";
    }
    return "";
  }, {
    canvasID: canonicalSnapshot.canvasURI,
    attemptNumber: Number(completedDurableJob.attemptCount),
    jobID,
    lineIDs: canonicalLineIDs,
    visualDeadline: Date.now() + wandVisualProofGraceMs,
  }, { timeout: transcriptionTimeoutMs });
  if (await automaticTranscriptionProof.jsonValue() !== "proved") {
    throw new Error("automatic transcription omitted visible line-by-line wand progress");
  }
  const transcriptionOutcome = await page.waitForFunction(() => {
    const text = document.getElementById("editor-transcription-status")?.textContent?.trim() ?? "";
    if (text === "Batch transcription complete. Updated text is now available in the editor.") return "complete";
    if (text.startsWith("Batch transcription failed")) return "failed";
    if (text === "Automatic transcription was canceled.") return "canceled";
    return "";
  }, undefined, { timeout: transcriptionTimeoutMs });
  if (await transcriptionOutcome.jsonValue() !== "complete") throw new Error("transcription failed");
  assertBrowserHealthy();

  // Mount a fresh editor without pinning it to the completed upload job, then
  // enqueue a second durable job. This makes the overlay and SSE bridge ready
  // before any task progress, exercising the real in-flight line-by-line path
  // separately from the deliberately late-loaded catch-up path above.
  const liveEventStreamReady = page.waitForResponse((response) => {
    let url;
    try {
      url = new URL(response.url());
    } catch {
      return false;
    }
    return response.ok()
      && response.request().method() === "GET"
      && url.origin === baseURL.origin
      && url.pathname === "/v1/events"
      && url.searchParams.get("item_image_id") === itemImageID;
  }, { timeout: stageTimeoutMs });
  await navigate(`/editor?itemId=${encodeURIComponent(createdItemID)}&itemImageId=${itemImageID}&workspace_id=${workspaceID}`);
  await page.locator("#editor-transcription-status").waitFor({ state: "attached" });
  await page.getByRole("heading", { name: "Editor", exact: true }).waitFor({ state: "visible" });
  await page.waitForFunction(() => (
    globalThis.__scribeReadinessAutomaticTranscription?.overlayReady === true
    && document.getElementById("editor-transcription-status")?.textContent?.trim()
      === "Batch transcription complete. Updated text is now available in the editor."
  ), undefined, { timeout: transcriptionTimeoutMs });
  // The server captures the outbox high-water mark before flushing this SSE
  // response. Waiting for it guarantees the live job cannot start before the
  // new editor subscription is established.
  await liveEventStreamReady;
  await page.evaluate(() => {
    const evidence = globalThis.__scribeReadinessAutomaticTranscription;
    if (!evidence) return;
    evidence.badges.length = 0;
    evidence.results.length = 0;
    evidence.segments.length = 0;
  });
  const liveJobID = await createTranscriptionJob(
    itemImageID,
    positiveID(durableJob.contextId),
    workspaceID,
  );
  if (liveJobID === jobID) throw new Error("live transcription job reused the completed upload job");

  const liveAutomaticTranscriptionProof = await page.waitForFunction((expected) => {
    const evidence = globalThis.__scribeReadinessAutomaticTranscription;
    const status = document.getElementById("editor-transcription-status")?.textContent?.trim() ?? "";
    if (status.startsWith("Batch transcription failed")) return "failed";
    if (status.startsWith("The completed transcription could not")) return "blocked";
    if (status === "Automatic transcription was canceled.") return "canceled";
    const segments = Array.isArray(evidence?.segments)
      ? evidence.segments.filter((segment) => segment.jobId === expected.jobID)
      : [];
    const results = Array.isArray(evidence?.results)
      ? evidence.results.filter((result) => result.jobId === expected.jobID)
      : [];
    const badges = Array.isArray(evidence?.badges)
      ? evidence.badges.filter((badge) => badge.jobId === expected.jobID && badge.visible)
      : [];
    const exactLiveEvent = (sample) => (
      sample.canvasId === expected.canvasID
      && sample.attemptNumber === 1
      && sample.catchUp === false
      && Number.isInteger(sample.done)
      && sample.done >= 1
      && sample.done <= 2
      && sample.total === 2
      && sample.annotationId === expected.lineIDs[sample.done - 1]
    );
    const progressesExactlyInOrder = (samples) => {
      if (samples.length < 2 || !samples.every(exactLiveEvent)) return false;
      const seen = new Set();
      let previous = 0;
      for (const sample of samples) {
        if (sample.done < previous) return false;
        previous = sample.done;
        seen.add(sample.done);
      }
      return seen.size === 2 && seen.has(1) && seen.has(2);
    };
    const exactBadge = (badge) => (
      badge.hasWand
      && badge.attemptNumber === 1
      && Number.isInteger(badge.line)
      && badge.line >= 1
      && badge.line <= 2
      && badge.total === 2
      && badge.annotationId === expected.lineIDs[badge.line - 1]
    );
    const badgeMovesExactlyInOrder = badges.length >= 2
      && badges.every(exactBadge)
      && badges.every((badge, index) => index === 0 || badge.line >= badges[index - 1].line)
      && badges.some((first, firstIndex) => (
        first.line === 1
        && first.annotationId === expected.lineIDs[0]
        && badges.slice(firstIndex + 1).some((second) => (
          second.line === 2
          && second.annotationId === expected.lineIDs[1]
        ))
      ));
    if (progressesExactlyInOrder(segments)
      && progressesExactlyInOrder(results)
      && badgeMovesExactlyInOrder) return "proved";
    if (status === "Batch transcription complete. Updated text is now available in the editor.") {
      if (Date.now() < expected.visualDeadline) return "";
      if (!progressesExactlyInOrder(segments)) return "completed-without-segments";
      if (!progressesExactlyInOrder(results)) return "completed-without-results";
      if (badges.length === 0) return "completed-without-visible-badges";
      return "completed-without-badge-order";
    }
    return "";
  }, {
    canvasID: canonicalSnapshot.canvasURI,
    jobID: liveJobID,
    lineIDs: canonicalLineIDs,
    visualDeadline: Date.now() + wandVisualProofGraceMs,
  }, { timeout: transcriptionTimeoutMs });
  const liveAutomaticTranscriptionOutcome = await liveAutomaticTranscriptionProof.jsonValue();
  if (liveAutomaticTranscriptionOutcome !== "proved") {
    throw new Error(`in-flight automatic transcription omitted live wand progress: ${liveAutomaticTranscriptionOutcome}`);
  }
  const liveCompletedJob = await waitForTerminalTranscriptionJob(liveJobID, workspaceID);
  const liveCompletedRevision = exactCompletedAttemptResultRevision(liveCompletedJob, liveJobID);
  const liveCanonicalSnapshot = await loadCanonicalAnnotationSnapshot(itemImageID, workspaceID);
  assertTextualAnnotationPage(liveCanonicalSnapshot.page);
  const liveCanonicalLines = liveCanonicalSnapshot.page.items.filter((annotation) => (
    String(annotation?.textGranularity ?? "").toLowerCase() === "line"
  ));
  const liveCanonicalLineIDs = liveCanonicalLines.map((annotation) => String(annotation?.id ?? "").trim());
  if (
    !transcriptionJobCompleted(liveCompletedJob)
    || Number(liveCompletedJob?.attemptCount ?? -1) !== 1
    || Number(liveCompletedJob?.failedSegments ?? 0) !== 0
    || Number(liveCompletedJob?.completedSegments ?? -1) !== 2
    || Number(liveCompletedJob?.totalSegments ?? -1) !== 2
    || !liveCompletedRevision
    || liveCanonicalSnapshot.revision !== liveCompletedRevision
    || liveCanonicalSnapshot.itemImageID !== itemImageID
    || liveCanonicalSnapshot.canvasURI !== canonicalSnapshot.canvasURI
    || liveCanonicalLineIDs.length !== canonicalLineIDs.length
    || liveCanonicalLineIDs.some((annotationID, index) => annotationID !== canonicalLineIDs[index])
    || !liveCanonicalLines.every(annotationHasText)
    || BigInt(liveCompletedRevision) <= BigInt(completedResultRevision)
  ) {
    throw new Error("in-flight automatic transcription did not commit its exact successful attempt");
  }
  await page.waitForFunction(() => (
    document.getElementById("editor-transcription-status")?.textContent?.trim()
      === "Batch transcription complete. Updated text is now available in the editor."
  ), undefined, { timeout: transcriptionTimeoutMs });
  if (enrichAnnotationRequestCount !== 0) {
    throw new Error("automatic transcription used the foreground enrichment path");
  }
  assertBrowserHealthy();

  category = "annotations";
  assertBrowserHealthy();

  category = "editor";
  await page.locator("#mirador-viewer").waitFor({ state: "visible" });
  await page.getByRole("heading", { name: "Editor", exact: true }).waitFor({ state: "visible" });
  for (const name of ["Overlay off", "Retranscribe", "Save", "Publish edits"]) {
    await page.getByRole("button", { name, exact: true }).waitFor({ state: "visible" });
  }
  const textActions = page.getByRole("group", { name: "Text and page actions", exact: true });
  const editorActionButtons = textActions.locator("button");
  const editorDelete = page.getByRole("button", { name: "Delete", exact: true });
  const editorDeleteState = {
    className: await editorDelete.getAttribute("class") ?? "",
    iconCount: await editorDelete.locator("svg").count(),
    lastLabel: await editorActionButtons.last().getAttribute("aria-label") ?? "",
  };
  if (
    editorDeleteState.lastLabel !== "Delete"
    || !editorDeleteState.className.includes("MuiButton-containedError")
    || editorDeleteState.iconCount !== 1
  ) {
    throw new Error("editor delete action is not the final destructive trash action");
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
  if (enrichAnnotationRequestCount !== 0) {
    throw new Error("automatic transcription used the foreground enrichment path");
  }
  await page.getByRole("button", { name: "Retranscribe", exact: true }).click();
  const transcribeDialog = page.getByRole("dialog").filter({ hasText: "entire page" });
  await transcribeDialog.waitFor({ state: "visible" });
  await transcribeDialog.getByRole("button", { name: /entire page/i }).click();
  await page.getByText("Document retranscribed. Save to persist this draft.", { exact: true }).waitFor({ state: "visible" });
  if (enrichAnnotationRequestCount < 1) throw new Error("manual retranscription omitted foreground enrichment");
  assertBrowserHealthy();

  category = "structure";
  const initialDraftCount = await currentEditorAnnotationCount();
  const drawLineButton = page.getByRole("button", { name: "Draw New Line", exact: true });
  if (await drawLineButton.getAttribute("aria-pressed") === "true") {
    throw new Error("draw line unexpectedly active");
  }
  await drawLineButton.click();
  await page.waitForFunction(() => (
    document.querySelector('button[aria-label="Draw New Line"]')?.getAttribute("aria-pressed") === "true"
  ));
  await drawLineButton.click();
  await page.waitForFunction(() => (
    document.querySelector('button[aria-label="Draw New Line"]')?.getAttribute("aria-pressed") !== "true"
  ));

  const centeredLineButton = page.getByRole("button", {
    name: centeredLineAccessibleName,
    exact: true,
  });
  await centeredLineButton.click();
  await page.getByRole("status").filter({ hasText: "Draft line created." }).waitFor({ state: "visible" });
  await waitForEditorAnnotationCount(initialDraftCount + 1);
  await waitForSaveEnabled();

  await page.getByRole("button", { name: "Undo", exact: true }).click();
  await waitForEditorAnnotationCount(initialDraftCount);
  await page.getByRole("button", { name: "Redo", exact: true }).click();
  await waitForEditorAnnotationCount(initialDraftCount + 1);

  const emptyCenteredLine = page.getByRole("button", {
    name: "Edit line: empty text",
    exact: true,
  }).last();
  await emptyCenteredLine.click();
  const lineToken = page.getByRole("textbox", { name: "Edit line token 1", exact: true });
  await lineToken.waitFor({ state: "visible" });
  if (await lineToken.inputValue() !== "") throw new Error("centered line was not selected");
  await lineToken.fill(deterministicLineText);
  const expectedTokens = deterministicLineText.split(" ");
  await page.waitForFunction((tokens) => {
    const inputs = Array.from(document.querySelectorAll('input[aria-label^="Edit line token "]'));
    return inputs.length === tokens.length
      && inputs.every((input, index) => input.value === tokens[index]);
  }, expectedTokens);

  const beforeSplitWordsCount = await currentEditorAnnotationCount();
  await requireConnectAction(
    "/scribe.v1.AnnotationService/SplitLineIntoWords",
    () => page.getByRole("button", { name: "Split Words", exact: true }).click(),
  );
  await page.getByRole("status").filter({ hasText: "Words created." }).waitFor({ state: "visible" });
  const splitWordsCount = await waitForEditorAnnotationCountDirection(beforeSplitWordsCount, "increase");

  await page.getByRole("button", {
    name: "Add a word annotation beside the selection",
    exact: true,
  }).click();
  await page.getByRole("status").filter({ hasText: "Draft word created." }).waitFor({ state: "visible" });
  await waitForEditorAnnotationCount(splitWordsCount + 1);
  await page.getByRole("button", { name: "Undo", exact: true }).click();
  await waitForEditorAnnotationCount(splitWordsCount);
  await page.getByRole("button", { name: "Redo", exact: true }).click();
  await waitForEditorAnnotationCount(splitWordsCount + 1);
  const addedWord = page.getByRole("textbox", { name: "Edit word with empty text", exact: true });
  await addedWord.waitFor({ state: "visible" });
  await addedWord.fill("epsilon");
  await page.getByRole("textbox", { name: "Edit word epsilon", exact: true }).waitFor({ state: "visible" });

  const beforeJoinWordsCount = await currentEditorAnnotationCount();
  await page.getByRole("button", { name: "Join Words", exact: true }).click();
  const joinWordsDialog = page.getByRole("dialog", { name: "Choose words to join", exact: true });
  await joinWordsDialog.waitFor({ state: "visible" });
  await selectAllAdditionalCandidates(joinWordsDialog, /^Word [0-9]+:/);
  await requireConnectAction(
    "/scribe.v1.AnnotationService/JoinWordsIntoLine",
    () => joinWordsDialog.getByRole("button", { name: "Join selected words", exact: true }).click(),
  );
  await page.getByRole("status").filter({ hasText: "Words joined." }).waitFor({ state: "visible" });
  const joinedWordsCount = await waitForEditorAnnotationCountDirection(beforeJoinWordsCount, "decrease");

  await page.getByRole("button", { name: "Split Line", exact: true }).click();
  const splitLineDialog = page.getByRole("dialog", { name: "Choose a split boundary", exact: true });
  await splitLineDialog.waitFor({ state: "visible" });
  await requireConnectAction(
    "/scribe.v1.AnnotationService/SplitLineIntoTwoLines",
    () => splitLineDialog.getByRole("button", { name: "Split at boundary", exact: true }).click(),
  );
  await page.getByRole("status").filter({ hasText: "Line split." }).waitFor({ state: "visible" });
  await waitForEditorAnnotationCount(joinedWordsCount + 1);

  await page.getByRole("button", { name: "Join Lines", exact: true }).click();
  const joinLinesDialog = page.getByRole("dialog", { name: "Choose lines to join", exact: true });
  await joinLinesDialog.waitFor({ state: "visible" });
  await selectFirstAdditionalCandidate(joinLinesDialog, /^Line [0-9]+:/);
  const beforeJoinLinesCount = await currentEditorAnnotationCount();
  await requireConnectAction(
    "/scribe.v1.AnnotationService/JoinLines",
    () => joinLinesDialog.getByRole("button", { name: "Join selected lines", exact: true }).click(),
  );
  await page.getByRole("status").filter({ hasText: "Lines joined." }).waitFor({ state: "visible" });
  await waitForEditorAnnotationCount(beforeJoinLinesCount - 1);

  const beforeDeleteCount = await currentEditorAnnotationCount();
  await page.getByRole("button", { name: "Delete", exact: true }).click();
  await waitForEditorAnnotationCount(beforeDeleteCount - 1);
  assertBrowserHealthy();

  category = "save";
  const saveButton = page.getByRole("button", { name: "Save", exact: true });
  await waitForSaveEnabled();
  await requireConnectAction(
    "/scribe.v1.AnnotationService/SaveAnnotationPage",
    () => saveButton.click(),
  );
  await page.getByText("Saved page.", { exact: true }).waitFor({ state: "visible" });
  const savedAnnotationSnapshot = await loadCanonicalAnnotationSnapshot(itemImageID, workspaceID);
  assertTextualAnnotationPage(savedAnnotationSnapshot.page);
  assertBrowserHealthy();

  category = "publish";
  await page.getByRole("button", { name: "Publish edits", exact: true }).click();
  await page.getByText("Edits published.", { exact: true }).waitFor({ state: "visible" });
  const publishedAnnotationPath = `/presentation/v3/item-image-${itemImageID}/canvas/page-1/annotations`;
  const publishedAnnotationPage = await waitForPublishedAnnotationPage(publishedAnnotationPath);
  assertTextualAnnotationPage(publishedAnnotationPage);
  assertBrowserHealthy();

  category = "responsive";
  const responsiveEditorPath = `/editor?itemId=${encodeURIComponent(createdItemID)}&itemImageId=${itemImageID}&workspace_id=${workspaceID}`;
  const responsiveCanonicalSnapshot = await loadCanonicalAnnotationSnapshot(
    itemImageID,
    workspaceID,
  );
  const responsiveViewports = [
    { width: 360, height: 800, minimumImageHeight: 160 },
    { width: 667, height: 375, minimumImageHeight: 60 },
    { width: 768, height: 1024, minimumImageHeight: 220 },
    { width: 1440, height: 900, minimumImageHeight: 220 },
  ];
  await page.setViewportSize(responsiveViewports[0]);
  await navigate(responsiveEditorPath);
  await page.locator("#mirador-viewer").waitFor({ state: "visible" });
  await page.locator('[data-scribe-action-panel="true"]').waitFor({ state: "visible" });
  await waitForActiveCanvasIdentity({
    canvasID: responsiveCanonicalSnapshot.canvasURI,
    itemID: createdItemID,
    itemImageID,
    workspaceID,
  });
  for (const viewport of responsiveViewports) {
    assertMainScenarioActive();
    await page.setViewportSize({ width: viewport.width, height: viewport.height });
    await assertOpenSeadragonCanvas();
    await assertResponsiveEditorGeometry(
      viewport.width,
      viewport.height,
      viewport.minimumImageHeight,
    );
  }
  const responsiveCanonicalAfter = await loadCanonicalAnnotationSnapshot(itemImageID, workspaceID);
  if (
    responsiveCanonicalAfter.itemImageID !== responsiveCanonicalSnapshot.itemImageID
    || responsiveCanonicalAfter.canvasURI !== responsiveCanonicalSnapshot.canvasURI
    || responsiveCanonicalAfter.revision !== responsiveCanonicalSnapshot.revision
    || JSON.stringify(responsiveCanonicalAfter.page)
      !== JSON.stringify(responsiveCanonicalSnapshot.page)
  ) {
    throw new Error("responsive editor resize changed the saved canonical page");
  }
  assertBrowserHealthy();

  category = "token";
  await navigate("/");
  await assertItemDeletePresentation("#shell-content", createdItemID, fixtureName);
  await assertItemDeletePresentation("#shell-sidebar", createdItemID, fixtureName);
  await page.locator("#shell-account-button").click();
  await page.getByRole("heading", { name: "Workspace and account settings", exact: true }).waitFor({ state: "visible" });
  await page.locator("#settings-api-key-form").waitFor({ state: "visible" });
  createdAPIKeyName = `browser-readiness-${randomUUID()}`;
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
  await waitForMutationResponsesToSettle(tokenMutation);
  requireTerminalRequestSuccess(createKeyResponse, "token creation request failed");
  const createKeyPayload = await responseJSON(createKeyResponse, "invalid token creation response");
  const createdAPIKey = createKeyPayload?.apiKey;
  createdAPIKeyID = positiveID(createdAPIKey?.id) || undefined;
  if (
    tokenMutation.requestCount !== 1
    || !tokenMutation.responseSettled
    || !createdAPIKeyID
    || createdAPIKey?.name !== createdAPIKeyName
    || createdAPIKey?.role !== "read"
    || positiveID(createdAPIKey?.workspaceId) !== workspaceID
  ) {
    throw new Error("missing token identity");
  }
  assertMainScenarioActive();
  tokenMutation.validated = true;

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

  category = "manifest";
  successfulImageResponses.length = 0;
  await navigate("/");
  await page.locator('[data-library-tab="manifest"]').click();
  if (await page.locator("#library-context-select").inputValue() !== "0") {
    throw new Error("manifest import did not retain default context");
  }
  const importMode = page.locator('input[name="library-manifest-mode"][value="import"]');
  const reprocessMode = page.locator('input[name="library-manifest-mode"][value="reprocess"]');
  if (!await importMode.isChecked() || await reprocessMode.isChecked()) {
    throw new Error("manifest import mode was not the default");
  }
  const baselineManifestItems = await exactManifestItems(workspaceID);
  for (const baselineItem of baselineManifestItems) {
    const baselineItemID = String(baselineItem?.id ?? "").trim();
    if (!baselineItemID) throw new Error("manifest baseline identity failed");
    manifestBaselineItemIDs.add(baselineItemID);
  }
  assertMainScenarioActive();
  await page.getByLabel("IIIF manifest URL", { exact: true }).fill(manifestURL);
  manifestImportAttempted = true;
  const manifestResponsePromise = page.waitForResponse((response) => (
    sameOriginConnectResponse(response, "/scribe.v1.ItemService/ImportManifest")
  ), { timeout: uploadTimeoutMs });
  await page.getByRole("button", { name: "Ingest manifest", exact: true }).click();
  const manifestResponse = await manifestResponsePromise;
  if (!manifestResponse.ok()) throw new Error("manifest import failed");
  await waitForMutationResponsesToSettle(manifestMutation);
  requireTerminalRequestSuccess(manifestResponse, "manifest import request failed");
  const manifestPayload = await responseJSON(manifestResponse, "invalid manifest import response");
  const manifestItem = manifestPayload?.item;
  createdManifestItemID = String(manifestItem?.id ?? "").trim() || undefined;
  createdManifestItemName = String(manifestItem?.name ?? "").trim() || createdManifestItemID;
  const manifestImages = Array.isArray(manifestItem?.images) ? manifestItem.images : [];
  const manifestImageIDs = manifestImages.map((image) => positiveID(image?.id));
  const manifestCanvasIDs = manifestImages.map((image) => String(image?.canvasUri ?? "").trim());
  const manifestFirstImageID = manifestImageIDs[0];
  const manifestSecondImageID = manifestImageIDs[1];
  const manifestFirstCanvasID = manifestCanvasIDs[0];
  const manifestSecondCanvasID = manifestCanvasIDs[1];
  if (
    manifestMutation.requestCount !== 1
    || !manifestMutation.responseSettled
    || manifestReprocessRequestCount !== 0
    || !createdManifestItemID
    || !createdManifestItemName
    || manifestBaselineItemIDs.has(createdManifestItemID)
    || manifestItem?.sourceType !== "manifest"
    || manifestItem?.sourceUrl !== manifestURL
    || manifestImageIDs.length !== 6
    || manifestImageIDs.some((imageID) => !imageID)
    || new Set(manifestImageIDs).size !== 6
    || manifestCanvasIDs.some((canvasID) => !canvasID)
    || new Set(manifestCanvasIDs).size !== 6
  ) {
    throw new Error("manifest import contract failed");
  }
  assertMainScenarioActive();
  manifestMutation.validated = true;
  await page.waitForURL((url) => (
    url.pathname === "/editor"
    && url.searchParams.get("itemId") === createdManifestItemID
    && url.searchParams.get("itemImageId") === manifestFirstImageID
    && url.searchParams.get("workspace_id") === workspaceID
  ), { timeout: uploadTimeoutMs });
  await page.locator("#mirador-viewer").waitFor({ state: "visible" });
  const manifestActionPanel = page.locator('[data-scribe-action-panel="true"]');
  await manifestActionPanel.waitFor({ state: "visible" });
  if (await manifestActionPanel.count() !== 1) {
    throw new Error("manifest editor action panel did not mount exactly once");
  }
  await waitForActiveCanvasIdentity({
    canvasID: manifestFirstCanvasID,
    itemID: createdManifestItemID,
    itemImageID: manifestFirstImageID,
    workspaceID,
  });

  const manifestEditorManifest = await loadEditorManifest(manifestFirstImageID, workspaceID);
  const editorManifestImageIDs = Array.isArray(manifestEditorManifest.item?.images)
    ? manifestEditorManifest.item.images.map((image) => positiveID(image?.id))
    : [];
  if (
    String(manifestEditorManifest.item?.id ?? "") !== createdManifestItemID
    || manifestEditorManifest.selectedCanvasID !== manifestFirstCanvasID
    || manifestEditorManifest.manifest.items.length !== 6
    || editorManifestImageIDs.length !== 6
    || editorManifestImageIDs.some((imageID, index) => imageID !== manifestImageIDs[index])
  ) {
    throw new Error("manifest editor projection identity failed");
  }
  const manifestFirstImageResource = editorCanvasImageResource(
    manifestEditorManifest.manifest,
    manifestFirstCanvasID,
  );
  const manifestSecondImageResource = editorCanvasImageResource(
    manifestEditorManifest.manifest,
    manifestSecondCanvasID,
  );
  await requireLoadedOpenSeadragonImage(manifestFirstImageResource);

  const manifestFirstAnnotationPath = `/presentation/v3/item-image-${manifestFirstImageID}/canvas/page-1/annotations`;
  const manifestAnnotationSnapshot = await loadCanonicalAnnotationSnapshot(
    manifestFirstImageID,
    workspaceID,
  );
  const manifestAnnotationPage = manifestAnnotationSnapshot.page;
  assertTextualAnnotationPage(manifestAnnotationPage);
  assertExactPresentationAnnotationPage(manifestAnnotationPage, manifestFirstAnnotationPath);
  if (
    manifestAnnotationSnapshot.itemImageID !== manifestFirstImageID
    || manifestAnnotationSnapshot.canvasURI !== manifestFirstCanvasID
  ) {
    throw new Error("manifest canonical page identity mismatch");
  }

  // Publication is scoped to this disposable imported item and is removed by
  // the exact manifest cleanup below. It proves the canonical page reaches the
  // sole public Presentation surface without modifying the external source.
  await requireConnectAction(
    "/scribe.v1.AnnotationService/PublishAnnotationPage",
    () => page.getByRole("button", { name: "Publish edits", exact: true }).click(),
  );
  await page.getByText("Edits published.", { exact: true }).waitFor({ state: "visible" });
  const manifestPublishedAnnotationPage = await waitForPublishedAnnotationPage(
    manifestFirstAnnotationPath,
  );
  assertExactPresentationAnnotationPage(
    manifestPublishedAnnotationPage,
    manifestFirstAnnotationPath,
  );

  const nextCanvas = page.getByRole("button", { name: "Next item", exact: true });
  await nextCanvas.waitFor({ state: "visible" });
  if (!await nextCanvas.isEnabled()) throw new Error("next manifest Canvas action was disabled");
  await requireLoadedOpenSeadragonImage(
    manifestSecondImageResource,
    () => nextCanvas.click(),
  );
  await waitForActiveCanvasIdentity({
    canvasID: manifestSecondCanvasID,
    itemID: createdManifestItemID,
    itemImageID: manifestSecondImageID,
    workspaceID,
  });
  await assertOpenSeadragonCanvas();

  const manifestSecondAnnotationPath = `/presentation/v3/item-image-${manifestSecondImageID}/canvas/page-1/annotations`;
  const manifestSecondAnnotationSnapshot = await loadCanonicalAnnotationSnapshot(
    manifestSecondImageID,
    workspaceID,
  );
  assertTextualAnnotationPage(manifestSecondAnnotationSnapshot.page);
  assertExactPresentationAnnotationPage(
    manifestSecondAnnotationSnapshot.page,
    manifestSecondAnnotationPath,
  );
  if (
    manifestSecondAnnotationSnapshot.itemImageID !== manifestSecondImageID
    || manifestSecondAnnotationSnapshot.canvasURI !== manifestSecondCanvasID
  ) {
    throw new Error("second manifest canonical page identity mismatch");
  }

  const manifestRetranscribe = page.getByRole("button", { name: "Retranscribe", exact: true });
  await manifestRetranscribe.waitFor({ state: "visible" });
  await page.waitForFunction(() => {
    const retranscribe = document.querySelector('button[aria-label="Retranscribe"]');
    return retranscribe instanceof HTMLButtonElement && !retranscribe.disabled;
  });
  await page.getByRole("button", { name: "Overlay off", exact: true }).click();
  await page.getByRole("button", { name: "Edit overlay", exact: true }).waitFor({ state: "visible" });
  await page.locator(".scribe-text-overlay").waitFor({ state: "visible" });
  if (await page.locator('[data-scribe-granularity="line"]').count() < 1) {
    throw new Error("second manifest Canvas overlay omitted line markers");
  }
  await page.getByRole("button", { name: "Edit overlay", exact: true }).click();
  await page.getByRole("button", { name: "Read overlay", exact: true }).click();
  await page.getByRole("button", { name: "Outline overlay", exact: true }).click();
  await page.getByRole("button", { name: "Overlay off", exact: true }).waitFor({ state: "visible" });
  if (!await manifestRetranscribe.isEnabled()) {
    throw new Error("manifest editor action was unusable after overlay cycling");
  }

  await requireConnectAction(
    "/scribe.v1.AnnotationService/PublishAnnotationPage",
    () => page.getByRole("button", { name: "Publish edits", exact: true }).click(),
  );
  const manifestSecondPublishedAnnotationPage = await waitForPublishedAnnotationPage(
    manifestSecondAnnotationPath,
  );
  assertExactPresentationAnnotationPage(
    manifestSecondPublishedAnnotationPage,
    manifestSecondAnnotationPath,
  );
  assertBrowserHealthy();

  category = "cleanup";
  await navigate("/");
  await deleteItemThroughLibrary("#shell-content", createdItemID, fixtureName);
  createdItemID = undefined;
  await deleteItemThroughLibrary("#shell-sidebar", createdManifestItemID, createdManifestItemName);
  createdManifestItemID = undefined;
  assertBrowserHealthy();
  })();
  await Promise.race([mainScenario, mainScenarioWatchdog]);
} catch {
  failureCategory = browserFaultCategory ?? category;
} finally {
  if (mainScenarioWatchdogTimer !== undefined) {
    clearTimeout(mainScenarioWatchdogTimer);
    mainScenarioWatchdogTimer = undefined;
  }
  if (watchdogPageClose) await watchdogPageClose;
  if (
    browserContext
    && createdWorkspaceID
    && (createdAPIKeyName || createdAPIKeyID || fixtureName || manifestImportAttempted)
  ) {
    category = "cleanup";
    let cleanupFailed = false;

    // Stop browser-side retry timers before calculating the latest observed
    // mutation horizon. Direct API reconciliation keeps the session cookie.
    if (page && !page.isClosed()) {
      try {
        await page.close({ runBeforeUnload: false });
      } catch {
        cleanupFailed = true;
      }
    }

    if (manifestImportAttempted) {
      try {
        await cleanupExactManifestItems(
          createdWorkspaceID,
          createdManifestItemID,
          manifestMutation,
          manifestBaselineItemIDs,
          globalCleanupDeadline,
        );
      } catch {
        cleanupFailed = true;
      }
    }

    if (createdAPIKeyName) {
      try {
        await cleanupExactAPIKeys(
          createdAPIKeyName,
          createdWorkspaceID,
          tokenMutation,
          globalCleanupDeadline,
        );
      } catch {
        cleanupFailed = true;
      }
    }

    if (fixtureName) {
      try {
        await cleanupExactUploadItems(
          fixtureName,
          createdWorkspaceID,
          createdItemID,
          uploadMutation,
          globalCleanupDeadline,
        );
      } catch {
        cleanupFailed = true;
      }
    }

    if (cleanupFailed) failureCategory ??= browserFaultCategory ?? category;
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
