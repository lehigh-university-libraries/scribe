import { createHash, randomUUID } from "node:crypto";
import { readFile, unlink } from "node:fs/promises";
import { chromium } from "@playwright/test";

import {
  productionSessionUserIsNonAdmin,
  protoJSONRepeatedField,
} from "./deployed-readiness-protojson.mjs";
import { waitForActionByValue } from "./deployed-readiness-dom.mjs";
import { exactStructuralSnapshot } from "./deployed-readiness-structure.mjs";
import {
  classifyDurableUploadFailure,
  classifyManifestResponseFailure,
  classifyManifestSourceFailure,
  classifyRetryableUploadResponse,
  classifyUploadFailure,
  cleanupCommitHorizonMs,
  initialIngressRetryDelayMs,
  manifestFailureExitCode,
  manifestSourceFailureMarker,
  remainingUploadSequenceTimeoutMs,
  uploadDurableFailureMarker,
  uploadFailureMarker,
  uploadRequestTimeoutMs,
  uploadRetryableResponseMarker,
} from "./deployed-readiness-budget.mjs";
import { configureCanonicalIPv6Routing } from "./deployed-readiness-routing.mjs";

const scriptStartedAt = Date.now();
const readinessSmokeFixtureBase64 = "iVBORw0KGgoAAAANSUhEUgAAAoAAAACgAQAAAAC0heCRAAAEJElEQVRo3u3ZTW7jNhQH8CdwMMyiCGeZRWBO0QtkmUVhoTdJb+DuvDAsqVpkOVeiposeoyx6gHJJoILY/yNpx2oKS2mnm4IGDMRO9LOp90E8hsIXflABC1jAAhawgAUs4P8abJtQTaT9lrSjOowyTGLkH6jGa1d7Cu6grCdt9SiWwYmUpyNJgJJBh+tpJAVQ4bXVkDLYCkfNIhiv25IAKBi01Fg6kMQvZAaVO0gGSdh1oL0EDdXmAjQA5ZtArz+Z41YBVL3Dn3dOt+NhA3Aj8doovxXuIKyvJ/mbGVeAdWjDpHqAHYPC6yqMWvIzgXWVQRWGFVEG2GUwfkMxKnEB/giwc4fqDaAO/SUoJyUvwF76esigDGYNWEUwB6XJYL6H1DBo3OEDgrKlxqhlEPkrkIych8ifZpLTLUc452EjX8Aa+VAtg0hsEbM7JvYZ1PxksI/g3RPAdYkd2gjqvOQTmEtvBlqU5ArQyz4tOaVNuod5yQ1Hi8H7j8iGqQqmXgYnBm8ZHF7Ae8VPgFTFKN8TQOSXWwEG2aUoJzDl4alSRgaRh5sIDpy1S+AfQZ4qZXhdKSNRrBQGpzD8sgL0R8m13J3uYaplzdUiEsi1rBnU3U8rluxjt1EDoly9dBvdA+wdPWobuw1A2pAwa7pN7IfKzPuh7gB2AGu7536o2zeA6HhHdORZx9YDwIFBt+eOnUBpXsf0b97hPUXZ/WlPEbynaAPQOHpgEHuKNvhgNNsVe8q/fRSwgAUsYAELWMACFrCABfwvwQGzAyZ/HudrTGWTCG0QmNUqDJ6Y7PIBUDDC4G0b/BYzEAbV6yDmCAZlwHQhMDj3DGI6vgTJrgW96gFWPEZFEENyPwF0GZQZfDrKCIoF0AL0CcQaaRQY7LpJvQZ3W7UerAImTtVG0G3FgFk32GMC+QAIC3H7bxOISVeHdaBpAu2FbcQwMhjCDHy80+tA96l330HaKpvBzmCuPYNVBg83dQRpAXS7Z+meIojhm3bChsFGsJmDR3oT+O4RYB3ePfQM+roVViYwHgAhKEfMrAze7A6kr4E/PEtrL0DDAYgnPjOw4utWgd//LK1r3t9F8H11Bk+JHQ+A8A1P4FcPC+DXn9UrcNe0qJg52HTrQA9wcI28SaDge+gfmh5X6wjGEyV8SJ3B2w/Xg+K/+awM1ZJilOUF6DOYDoD8GaQFUFjVAmxjHjLYdP5jADgmcPMXUC2DuiXNICpFPgNEfwD4+z8ExwyaWMsAUcsR/DUvWdP8Hqp2GRQjg9xtZOjRbcYqtJWNsU0nStxtIsgF1V7vNqN0Wo5K2tgPAaIfTjOwTWBzAs0KcIqgjCA6dgabGRhWgpN0WwZd3FMA8v8usLFUWHcCTdpTzqC9Dn6BRwELWMACFrCABSxgAQtYwAIWsIAFLCAefwL3udqwYaAJQwAAAABJRU5ErkJggg==";
const readinessSmokeFixtureSHA256 = "e3f3bb2b5ade3c15af262a76ad58b720e7eb3b3d079802df04f1dd50be917b2d";
const stageTimeoutMs = 180_000;
// Direct VPC egress can need more than one minute to establish a first
// connection. Keep the initial PPB readiness allowance bounded while leaving
// the rest of the scenario fail closed.
const initialIngressWarmupBudgetMs = 300_000;
const initialIngressAttemptTimeoutMs = 10_000;
const initialIngressRetryIntervalMs = 2_000;
const transcriptionTimeoutMs = 360_000;
const wandVisualProofGraceMs = 5_000;
const browserTaskBudgetMs = 2_400_000;
const mainScenarioBudgetMs = 1_620_000;
const cleanupReserveMs = 780_000;
const sessionRevocationBudgetMs = 180_000;
const browserCloseBudgetMs = 30_000;
const cleanupPlatformHeadroomMs = 90_000;
const mainScenarioDeadline = scriptStartedAt + mainScenarioBudgetMs;
// The managed task is capped at 2,400 seconds. Reconciliation stops early
// enough to retain the full logout budget, a bounded browser close, and final
// platform shutdown headroom inside that absolute cap.
const browserTaskDeadline = scriptStartedAt + browserTaskBudgetMs;
const browserShutdownDeadline = browserTaskDeadline - cleanupPlatformHeadroomMs;
const sessionRevocationDeadline = browserShutdownDeadline - browserCloseBudgetMs;
const globalCleanupDeadline = sessionRevocationDeadline - sessionRevocationBudgetMs;
const cleanupPollIntervalMs = 5_000;
const cleanupStablePasses = 2;
const cleanupRecoveryTailMs = stageTimeoutMs;
const cleanupMaxItemPages = 100;
const cleanupMaxItems = 10_000;
const productionJanitorWorkspaceID = "1";
const productionJanitorMaxItemPages = 10;
const productionJanitorMaxItems = 1_000;
const productionJanitorMaxItemVerifications = 100;
const productionJanitorMaxAPIKeys = 1_000;
const productionJanitorMaxDeletes = 100;
const maxObservedImageResponses = 100;
const maxReadinessImageBytes = 64 * 1024 * 1024;
const manifestURL = "https://raw.githubusercontent.com/lehigh-university-libraries/scribe/e871f532c845bd90f983f3e13282ded1442de29b/internal/server/testdata/deployed-readiness/manifest.json";
const legacyManifestURL = "https://preserve.lehigh.edu/node/38817/book-manifest";
const productionReadinessManifestURLs = new Set([manifestURL, legacyManifestURL]);
const readinessManifestImageSHA256 = "0443cf4f28c60debf3237300d3357539b3b309f8c950af489491c686a13e0e16";
const productionStorageStatePath = "/tmp/scribe-browser-session-state.json";
const centeredLineAccessibleName = "Add a line at the viewport center and focus its keyboard resize handle";
const deterministicLineText = "browser readiness alpha beta gamma";
const deterministicWordTexts = Object.freeze([...deterministicLineText.split(" "), "epsilon"]);
const deterministicJoinedLineText = deterministicWordTexts.join(" ");
const readinessUploadNamePattern = /^browser-readiness-[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}\.png$/;
const readinessAPIKeyNamePattern = /^browser-readiness-[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;
const readinessManifestReferencePattern = /^browser-readiness-manifest-[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;
const presentationAnnotationPathPattern = /^\/presentation\/v3\/item-image-[1-9][0-9]*\/canvas\/page-1\/annotations(?:\/.*)?$/;
const maxManifestImportRequestBytes = 65_536;
// Cloud Logging can be temporarily unavailable to the protected deploy
// runner even though the Cloud Run task status is readable. Keep every
// allowlisted failure category recoverable from the task's bounded exit code
// without persisting raw browser output.
const readinessFailureExitCodes = new Map([
  ["home", 21],
  ["context", 22],
  ["upload", 23],
  ["upload-multi", 74],
  ["handoff", 24],
  ["transcription", 25],
  ["annotations", 26],
  ["editor", 27],
  ["overlay", 28],
  ["retranscribe", 29],
  ["structure", 30],
  ["save", 31],
  ["publish", 32],
  ["responsive", 33],
  ["token", 34],
  ["manifest", 35],
  ["cleanup", 36],
  ["network", 37],
  ["csp", 38],
  ["rate", 39],
  ["network-document-client", 40],
  ["network-document-server", 41],
  ["network-auth-client", 42],
  ["network-auth-server", 43],
  ["network-workspace-client", 44],
  ["network-workspace-server", 45],
  ["network-item-client", 46],
  ["network-item-server", 47],
  ["network-context-client", 48],
  ["network-context-server", 49],
  ["network-annotation-client", 50],
  ["network-annotation-server", 51],
  ["network-processing-client", 52],
  ["network-processing-server", 53],
  ["network-transcription-client", 54],
  ["network-transcription-server", 55],
  ["network-events-client", 56],
  ["network-events-server", 57],
  ["network-presentation-client", 58],
  ["network-presentation-server", 59],
  ["network-iiif-client", 60],
  ["network-iiif-server", 61],
  ["network-asset-client", 62],
  ["network-asset-server", 63],
  ["network-other-client", 64],
  ["network-other-server", 65],
  ["network-document-transport", 66],
  ["network-api-transport", 67],
  ["network-events-transport", 68],
  ["network-image-transport", 69],
  ["network-asset-transport", 70],
  ["network-other-transport", 71],
  ["initial-ingress-forbidden", 72],
  ["initial-ingress-not-found", 73],
]);

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

function configuredBrowserMode() {
  const mode = String(process.env.SCRIBE_BROWSER_MODE ?? "").trim();
  if (mode === "" || mode === "preview") return "preview";
  if (mode === "production") return "production";
  return undefined;
}

function configuredBaseURL(mode) {
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
    || (mode === "preview" && !/^scribe-pr-[1-9][0-9]*-[0-9]+\.[a-z]+-[a-z]+[0-9]+\.run\.app$/.test(parsed.hostname))
    || (mode === "production" && !/^scribe-[1-9][0-9]*\.[a-z]+-[a-z]+[0-9]+\.run\.app$/.test(parsed.hostname))
  ) {
    return undefined;
  }
  return parsed;
}

function isTargetHTTPSURL(candidate, target) {
  return candidate.protocol === "https:" && candidate.origin === target.origin;
}

const networkServiceFamilies = new Map([
  ["AuthService", "auth"],
  ["WorkspaceService", "workspace"],
  ["ItemService", "item"],
  ["ContextService", "context"],
  ["AnnotationService", "annotation"],
  ["ImageProcessingService", "processing"],
  ["TranscriptionService", "transcription"],
]);

function networkPathFamily(pathname, resourceType) {
  if (resourceType === "document") return "document";
  if (pathname === "/v1/events") return "events";
  const connectService = /^\/scribe\.v1\.([A-Za-z]+Service)\//.exec(pathname)?.[1];
  const serviceFamily = networkServiceFamilies.get(connectService);
  if (serviceFamily) return serviceFamily;
  if (pathname === "/presentation" || pathname.startsWith("/presentation/")) {
    return "presentation";
  }
  if (pathname === "/iiif" || pathname.startsWith("/iiif/")) return "iiif";
  if (
    pathname === "/static/uploads"
    || pathname.startsWith("/static/uploads/")
    || pathname === "/assets"
    || pathname.startsWith("/assets/")
    || ["font", "image", "media", "script", "stylesheet"].includes(resourceType)
  ) {
    return "asset";
  }
  return "other";
}

function responseNetworkFaultCategory(responseURL, response, target) {
  const status = response.status();
  if (!isTargetHTTPSURL(responseURL, target) || status < 400) return undefined;
  const family = networkPathFamily(
    responseURL.pathname,
    response.request().resourceType(),
  );
  const statusClass = status < 500 ? "client" : "server";
  return `network-${family}-${statusClass}`;
}

function rateLimitFamily(responseURL, resourceType, target) {
  if (!isTargetHTTPSURL(responseURL, target)) return undefined;
  return networkPathFamily(responseURL.pathname, resourceType);
}

function requestNetworkFaultCategory(requestURL, request, target) {
  if (!isTargetHTTPSURL(requestURL, target)) return undefined;
  const family = networkPathFamily(requestURL.pathname, request.resourceType());
  if (family === "document") return "network-document-transport";
  if (family === "events") return "network-events-transport";
  if (family === "presentation" || family === "iiif") return "network-image-transport";
  if (family === "asset") return "network-asset-transport";
  if (networkServiceFamilies.has(/^\/scribe\.v1\.([A-Za-z]+Service)\//.exec(requestURL.pathname)?.[1])) {
    return "network-api-transport";
  }
  return "network-other-transport";
}

function initialIngressResponseIsRetryable(responseURL, status, target) {
  return isTargetHTTPSURL(responseURL, target)
    && responseURL.pathname === "/"
    && responseURL.search === ""
    && responseURL.hash === ""
    && (status === 403 || status === 404);
}

function assertInitialIngressRetryClassifier() {
  const target = new URL("https://readiness.invalid/");
  if (
    !initialIngressResponseIsRetryable(new URL("/", target), 403, target)
    || !initialIngressResponseIsRetryable(new URL("/", target), 404, target)
    || [400, 401, 429, 500].some((status) => (
      initialIngressResponseIsRetryable(new URL("/", target), status, target)
    ))
    || initialIngressResponseIsRetryable(new URL("/editor", target), 403, target)
    || initialIngressResponseIsRetryable(new URL("/?retry=1", target), 403, target)
    || initialIngressResponseIsRetryable(new URL("/#retry", target), 403, target)
    || initialIngressResponseIsRetryable(new URL("https://other.invalid/"), 403, target)
  ) {
    throw new Error("initial ingress retry classifier failed");
  }
}

function assertNetworkFaultClassifiers() {
  const target = new URL("https://readiness.invalid/");
  const response = (path, status, resourceType = "fetch") => responseNetworkFaultCategory(
    new URL(path, target),
    {
      status: () => status,
      request: () => ({ resourceType: () => resourceType }),
    },
    target,
  );
  const request = (path, resourceType = "fetch") => requestNetworkFaultCategory(
    new URL(path, target),
    { resourceType: () => resourceType },
    target,
  );
  const responseCases = [
    ["/", "document", "document"],
    ["/scribe.v1.AuthService/GetAuthMe", "fetch", "auth"],
    ["/scribe.v1.WorkspaceService/ListWorkspaces", "fetch", "workspace"],
    ["/scribe.v1.ItemService/ListItems", "fetch", "item"],
    ["/scribe.v1.ContextService/ListContexts", "fetch", "context"],
    ["/scribe.v1.AnnotationService/GetAnnotationPage", "fetch", "annotation"],
    ["/scribe.v1.ImageProcessingService/GetOCRRun", "fetch", "processing"],
    ["/scribe.v1.TranscriptionService/GetTranscriptionJob", "fetch", "transcription"],
    ["/v1/events", "eventsource", "events"],
    ["/presentation/v3/item-image-1", "fetch", "presentation"],
    ["/iiif/3/image/info.json", "image", "iiif"],
    ["/assets/app.js", "script", "asset"],
    ["/healthz", "fetch", "other"],
  ];
  if (
    responseCases.some(([path, resourceType, family]) => (
      response(path, 404, resourceType) !== `network-${family}-client`
      || response(path, 503, resourceType) !== `network-${family}-server`
    ))
    || response("/healthz", 200) !== undefined
    || responseNetworkFaultCategory(
      new URL("https://other.invalid/healthz"),
      { status: () => 500, request: () => ({ resourceType: () => "fetch" }) },
      target,
    ) !== undefined
    || responseNetworkFaultCategory(
      new URL("blob:https://readiness.invalid/private-manifest"),
      { status: () => 500, request: () => ({ resourceType: () => "fetch" }) },
      target,
    ) !== undefined
    || responseCases.some(([path, resourceType, family]) => (
      rateLimitFamily(new URL(path, target), resourceType, target) !== family
    ))
    || rateLimitFamily(new URL("https://other.invalid/v1/events"), "eventsource", target) !== undefined
    || request("/", "document") !== "network-document-transport"
    || request("/scribe.v1.ItemService/ListItems") !== "network-api-transport"
    || request("/v1/events") !== "network-events-transport"
    || request("/iiif/3/image/info.json", "image") !== "network-image-transport"
    || request("/assets/app.js", "script") !== "network-asset-transport"
    || request("/healthz") !== "network-other-transport"
    || requestNetworkFaultCategory(
      new URL("https://other.invalid/healthz"),
      { resourceType: () => "fetch" },
      target,
    ) !== undefined
    || requestNetworkFaultCategory(
      new URL("blob:https://readiness.invalid/private-manifest"),
      { resourceType: () => "fetch" },
      target,
    ) !== undefined
  ) {
    throw new Error("network fault classifier failed");
  }
}

function assertTargetHTTPSURLClassifier() {
  const target = new URL("https://readiness.invalid/");
  if (
    !isTargetHTTPSURL(new URL("https://readiness.invalid/healthz"), target)
    || isTargetHTTPSURL(new URL("https://other.invalid/healthz"), target)
    || isTargetHTTPSURL(new URL("blob:https://readiness.invalid/private-manifest"), target)
  ) {
    throw new Error("target HTTPS URL classifier failed");
  }
}

assertTargetHTTPSURLClassifier();
assertNetworkFaultClassifiers();
assertInitialIngressRetryClassifier();

async function consumeProductionStorageState(targetURL) {
  const expectedVersion = String(process.env.SCRIBE_BROWSER_EXPECTED_SECRET_VERSION ?? "").trim();
  const expectedDigest = String(process.env.SCRIBE_BROWSER_EXPECTED_STORAGE_STATE_SHA256 ?? "").trim();
  if (!/^([2-9]|[1-9][0-9]{1,19})$/.test(expectedVersion) || !/^[0-9a-f]{64}$/.test(expectedDigest)) {
    throw new Error("invalid production browser session metadata");
  }

  let encoded;
  try {
    encoded = await readFile(productionStorageStatePath);
  } finally {
    try {
      await unlink(productionStorageStatePath);
      productionStorageStateRemoved = true;
    } catch (error) {
      if (error?.code !== "ENOENT") throw error;
      productionStorageStateRemoved = true;
    }
  }
  if (encoded.length === 0 || encoded.length > 65_536) {
    throw new Error("production browser session payload failed its digest contract");
  }
  const digestMatches = createHash("sha256").update(encoded).digest("hex") === expectedDigest;

  let state;
  try {
    state = JSON.parse(encoded.toString("utf8"));
  } catch {
    throw new Error("production browser session payload was not JSON");
  }
  const stateKeys = state && typeof state === "object" ? Object.keys(state).sort() : [];
  const cookie = Array.isArray(state?.cookies) && state.cookies.length === 1
    ? state.cookies[0]
    : undefined;
  const cookieKeys = cookie && typeof cookie === "object" ? Object.keys(cookie).sort() : [];
  const minimumRequiredExpiry = scriptStartedAt
    + mainScenarioBudgetMs
    + cleanupReserveMs
    + 60_000;
  if (
    stateKeys.join(",") !== "cookies,origins"
    || !Array.isArray(state.origins)
    || state.origins.length !== 0
    || cookieKeys.join(",") !== "domain,expires,httpOnly,name,path,sameSite,secure,value"
    || cookie.name !== "scribe_session"
    || !/^[A-Za-z0-9_-]{64}$/.test(String(cookie.value ?? ""))
    || cookie.domain !== targetURL.hostname
    || cookie.path !== "/"
    || !Number.isSafeInteger(cookie.expires)
    || cookie.expires * 1_000 < minimumRequiredExpiry
    || cookie.httpOnly !== true
    || cookie.secure !== true
    || cookie.sameSite !== "Lax"
  ) {
    throw new Error("production browser session payload failed its cookie contract");
  }
  // Retain only the validated cookie needed for fail-closed revocation. This
  // lives independently of Chromium so a launch/newContext failure after the
  // one-time state file is consumed can still revoke the database session.
  productionSessionCookie = { name: cookie.name, value: cookie.value };
  if (!digestMatches) {
    throw new Error("production browser session payload failed its digest contract");
  }
  return state;
}

function annotationHasText(annotation) {
  return annotationTextValue(annotation) !== "";
}

function annotationTextValue(annotation) {
  const bodies = Array.isArray(annotation?.body) ? annotation.body : [annotation?.body];
  for (const body of bodies) {
    const value = typeof body === "string" ? body : body?.value;
    if (typeof value === "string" && value.trim() !== "") return value.trim();
  }
  return "";
}

function exactReadinessSmokeFixture() {
  const fixture = Buffer.from(readinessSmokeFixtureBase64, "base64");
  const digest = createHash("sha256").update(fixture).digest("hex");
  if (fixture.length === 0 || digest !== readinessSmokeFixtureSHA256) {
    throw new Error("embedded readiness fixture failed its digest contract");
  }
  return fixture;
}

function newReadinessManifestIdentity() {
  const externalReferenceID = `browser-readiness-manifest-${randomUUID()}`;
  const idempotencyKey = createHash("sha256")
    .update(JSON.stringify(["browser-readiness-manifest", manifestURL, externalReferenceID]))
    .digest("hex");
  if (
    !readinessManifestReferencePattern.test(externalReferenceID)
    || !/^[0-9a-f]{64}$/.test(idempotencyKey)
  ) {
    throw new Error("manifest import identity generation failed");
  }
  return { externalReferenceID, idempotencyKey };
}

function exactManifestImportPostData(request) {
  if (
    !readinessManifestReferencePattern.test(String(manifestExternalReferenceID ?? ""))
    || !/^[0-9a-f]{64}$/.test(String(manifestIdempotencyKey ?? ""))
  ) {
    throw new Error("manifest import identity was not initialized");
  }
  const encoded = request.postDataBuffer();
  if (
    !encoded
    || encoded.byteLength === 0
    || encoded.byteLength > maxManifestImportRequestBytes
  ) {
    throw new Error("manifest import request body failed its size contract");
  }
  let payload;
  try {
    payload = JSON.parse(encoded.toString("utf8"));
  } catch {
    throw new Error("manifest import request body was not JSON");
  }
  if (
    !payload
    || typeof payload !== "object"
    || Array.isArray(payload)
    || payload.manifestUrl !== manifestURL
    || !/^[0-9a-f]{64}$/.test(String(payload.idempotencyKey ?? ""))
    || String(payload.externalReferenceId ?? "") !== ""
  ) {
    throw new Error("manifest import request identity failed");
  }
  manifestRequestIdentityInjected = true;
  return JSON.stringify({
    ...payload,
    externalReferenceId: manifestExternalReferenceID,
    idempotencyKey: manifestIdempotencyKey,
  });
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

function assertReadinessFixtureAnnotations(annotationPage) {
  const expected = [
    "line:Foo bar baz",
    "line:Hello world",
    "word:Foo",
    "word:Hello",
    "word:bar",
    "word:baz",
    "word:world",
  ];
  const actual = annotationPage.items.map((annotation) => (
    `${String(annotation?.textGranularity ?? "").toLowerCase()}:${annotationTextValue(annotation)}`
  )).sort();
  if (stableJSONValue(actual) !== stableJSONValue(expected)) {
    throw new Error("readiness fixture annotations changed");
  }
}

function stableJSONValue(value) {
  if (value === undefined) return "undefined";
  if (value === null || typeof value !== "object") return JSON.stringify(value);
  if (Array.isArray(value)) return `[${value.map(stableJSONValue).join(",")}]`;
  return `{${Object.keys(value).sort().map((key) => (
    `${JSON.stringify(key)}:${stableJSONValue(value[key])}`
  )).join(",")}}`;
}

function annotationTargetGeometry(annotation) {
  const target = annotation?.target;
  let canvasID = "";
  let selectorValues = [];
  if (typeof target === "string") {
    const hashIndex = target.indexOf("#");
    canvasID = hashIndex < 0 ? target : target.slice(0, hashIndex);
    selectorValues = hashIndex < 0 ? [] : [target.slice(hashIndex + 1)];
  } else if (target && typeof target === "object") {
    canvasID = typeof target.source === "string"
      ? target.source
      : String(target.source?.id ?? "").trim();
    const selectors = Array.isArray(target.selector) ? target.selector : [target.selector];
    selectorValues = selectors.filter((selector) => (
      String(selector?.type ?? "").trim().toLowerCase() === "fragmentselector"
    )).map((selector) => String(selector?.value ?? "").trim());
  }
  const xywhValues = selectorValues.flatMap((value) => value.replace(/^#/u, "").split("&"))
    .filter((parameter) => parameter.startsWith("xywh="))
    .map((parameter) => parameter.slice("xywh=".length));
  const match = xywhValues.length === 1
    ? xywhValues[0].match(/^(?:pixel:)?(-?[0-9]+),(-?[0-9]+),([1-9][0-9]*),([1-9][0-9]*)$/u)
    : null;
  if (!canvasID || !match) throw new Error("saved structural annotation geometry was invalid");
  const [x, y, w, h] = match.slice(1).map(Number);
  return { canvasID, x, y, w, h };
}

function assertSavedStructuralPage(annotationPage, expectedStructure) {
  const expectedLine = expectedStructure?.line;
  const expectedWords = Array.isArray(expectedStructure?.words) ? expectedStructure.words : [];
  const lines = annotationPage.items.filter((annotation) => (
    String(annotation?.textGranularity ?? "").toLowerCase() === "line"
  ));
  const words = annotationPage.items.filter((annotation) => (
    String(annotation?.textGranularity ?? "").toLowerCase() === "word"
  ));
  const savedLine = lines.find((annotation) => annotation?.id === expectedLine?.id);
  const expectedOrder = new Map(expectedWords.map((annotation, index) => [annotation?.id, index]));
  const savedWordsByID = new Map(words.map((annotation) => [annotation?.id, annotation]));
  const expectedTargetsMatch = expectedWords.every((annotation) => {
    const saved = savedWordsByID.get(annotation?.id);
    return saved
      && annotationTextValue(saved) === annotationTextValue(annotation)
      && stableJSONValue(saved.target) === stableJSONValue(annotation.target);
  });
  const lineGeometry = savedLine ? annotationTargetGeometry(savedLine) : null;
  const orderedWords = expectedWords.map((annotation) => savedWordsByID.get(annotation?.id));
  const wordsAreOwned = lineGeometry && orderedWords.every((annotation) => {
    if (!annotation) return false;
    const geometry = annotationTargetGeometry(annotation);
    const centerX = geometry.x + Math.floor(geometry.w / 2);
    const centerY = geometry.y + Math.floor(geometry.h / 2);
    return geometry.canvasID === lineGeometry.canvasID
      && centerX >= lineGeometry.x
      && centerX <= lineGeometry.x + lineGeometry.w
      && centerY >= lineGeometry.y
      && centerY <= lineGeometry.y + lineGeometry.h;
  });
  if (orderedWords.every(Boolean)) {
    orderedWords.sort((left, right) => {
      const leftGeometry = annotationTargetGeometry(left);
      const rightGeometry = annotationTargetGeometry(right);
      if (leftGeometry.x !== rightGeometry.x) return leftGeometry.x - rightGeometry.x;
      if (leftGeometry.y !== rightGeometry.y) return leftGeometry.y - rightGeometry.y;
      return expectedOrder.get(left?.id) - expectedOrder.get(right?.id);
    });
  }
  if (
    !expectedLine?.id
    || annotationTextValue(expectedLine) !== deterministicJoinedLineText
    || expectedWords.length !== deterministicWordTexts.length
    || new Set(expectedWords.map((annotation) => annotation?.id)).size !== expectedWords.length
    || !savedLine
    || annotationTextValue(savedLine) !== deterministicJoinedLineText
    || stableJSONValue(savedLine.target) !== stableJSONValue(expectedLine.target)
    || words.length !== deterministicWordTexts.length
    || savedWordsByID.size !== words.length
    || !expectedTargetsMatch
    || !wordsAreOwned
    || orderedWords.some((annotation, index) => (
      annotationTextValue(annotation) !== deterministicWordTexts[index]
    ))
  ) {
    throw new Error("saved structural edits were not canonical");
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
  return remainingDeadlineTimeMs(recoveryDeadline, "cleanup reconciliation deadline exceeded");
}

function remainingDeadlineTimeMs(deadline, failureMessage) {
  const remaining = Math.floor(deadline - Date.now());
  if (remaining <= 0) throw new Error(failureMessage);
  return remaining;
}

async function warmInitialBrowserIngress() {
  const deadline = Math.min(
    mainScenarioDeadline,
    Date.now() + initialIngressWarmupBudgetMs,
  );
  while (true) {
    let response;
    try {
      response = await browserContext.request.get(baseURL.href, {
        failOnStatusCode: false,
        maxRedirects: 0,
        maxRetries: 0,
        timeout: Math.min(
          initialIngressAttemptTimeoutMs,
          remainingDeadlineTimeMs(deadline, "initial ingress warm-up deadline exceeded"),
        ),
      });
    } catch {
      const retryDelayMs = initialIngressRetryDelayMs(
        deadline,
        initialIngressRetryIntervalMs,
      );
      if (retryDelayMs > 0) {
        await new Promise((resolve) => setTimeout(resolve, retryDelayMs));
        continue;
      }
      recordBrowserFault("network-document-transport");
      throw new Error("initial ingress warm-up transport failed");
    }

    let responseURL;
    try {
      responseURL = new URL(response.url());
    } catch {
      await response.dispose();
      recordBrowserFault("network-document-client");
      throw new Error("initial ingress warm-up returned an invalid URL");
    }
    const status = response.status();
    const contentType = String(response.headers()["content-type"] ?? "").trim();
    await response.dispose();
    if (
      isTargetHTTPSURL(responseURL, baseURL)
      && responseURL.pathname === "/"
      && responseURL.search === ""
      && responseURL.hash === ""
      && status === 200
      && /^text\/html(?:\s*;|$)/iu.test(contentType)
    ) return;

    if (initialIngressResponseIsRetryable(responseURL, status, baseURL)) {
      const retryDelayMs = initialIngressRetryDelayMs(
        deadline,
        initialIngressRetryIntervalMs,
      );
      if (retryDelayMs > 0) {
        await new Promise((resolve) => setTimeout(resolve, retryDelayMs));
        continue;
      }
    }
    if (initialIngressResponseIsRetryable(responseURL, status, baseURL)) {
      if (status === 403) recordBrowserFault("initial-ingress-forbidden");
      else if (status === 404) recordBrowserFault("initial-ingress-not-found");
    } else if (status === 429) recordBrowserRateFault(responseURL, "document");
    else if (isTargetHTTPSURL(responseURL, baseURL) && status >= 500) {
      recordBrowserFault("network-document-server");
    } else {
      recordBrowserFault("network-document-client");
    }
    throw new Error("initial ingress warm-up failed");
  }
}

async function waitForOperationBeforeDeadline(operation, deadline, failureMessage) {
  const timeoutMs = remainingDeadlineTimeMs(deadline, failureMessage);
  let timeout;
  try {
    await Promise.race([
      operation,
      new Promise((_, reject) => {
        timeout = setTimeout(() => reject(new Error(failureMessage)), timeoutMs);
      }),
    ]);
  } finally {
    if (timeout !== undefined) clearTimeout(timeout);
  }
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

async function requireProductionSession() {
  if (browserMode !== "production") return;
  const response = await browserContext.request.post(
    new URL("/scribe.v1.AuthService/GetAuthMe", baseURL).href,
    {
      data: {},
      headers: workspaceHeaders("1"),
      timeout: stageTimeoutMs,
    },
  );
  if (!response.ok()) throw new Error("production browser session was rejected");
  const payload = await responseJSON(response, "invalid production browser session response");
  if (
    payload?.authenticated !== true
    || payload?.authType !== "session"
    || positiveID(payload?.user?.id) !== "1"
    || positiveID(payload?.user?.defaultWorkspaceId) !== "1"
    || !productionSessionUserIsNonAdmin(payload?.user)
    || positiveID(payload?.workspace?.id) !== "1"
    || payload?.workspace?.role !== "admin"
  ) {
    throw new Error("production browser session identity failed");
  }
}

async function productionSessionFetch(path, options, revocationDeadline) {
  if (!productionSessionCookie) {
    throw new Error("production browser session revocation state is unavailable");
  }
  const timeoutMs = Math.min(
    30_000,
    remainingDeadlineTimeMs(
      revocationDeadline,
      "production browser session revocation deadline exceeded",
    ),
  );
  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(), timeoutMs);
  try {
    return await fetch(new URL(path, baseURL), {
      ...options,
      redirect: "manual",
      signal: controller.signal,
      headers: {
        ...options.headers,
        Cookie: `${productionSessionCookie.name}=${productionSessionCookie.value}`,
      },
    });
  } finally {
    clearTimeout(timeout);
  }
}

async function productionSessionIsRevoked(revocationDeadline) {
  const response = await productionSessionFetch(
    "/scribe.v1.WorkspaceService/ListWorkspaces",
    {
      method: "POST",
      body: "{}",
      headers: {
        Accept: "application/json",
        "Connect-Protocol-Version": "1",
        "Content-Type": "application/json",
        Origin: baseURL.origin,
        "X-Scribe-Workspace-ID": "1",
      },
    },
    revocationDeadline,
  );
  const rejected = response.status === 401;
  await response.body?.cancel();
  return rejected;
}

async function revokeProductionSession(revocationDeadline) {
  if (browserMode !== "production" || !productionSessionCookie) return;
  while (Date.now() < revocationDeadline) {
    try {
      const response = await productionSessionFetch(
        "/logout",
        {
          method: "POST",
          headers: {
            Accept: "application/json",
            Origin: baseURL.origin,
          },
        },
        revocationDeadline,
      );
      const logoutAccepted = response.ok;
      await response.body?.cancel();
      if (logoutAccepted && await productionSessionIsRevoked(revocationDeadline)) {
        productionSessionCookie = undefined;
        return;
      }
    } catch {
      // Retry only inside the fixed revocation budget. The final categorical
      // failure remains red if deletion or its original-cookie proof never
      // succeeds.
    }
    const retryDelayMs = Math.min(2_000, revocationDeadline - Date.now());
    if (retryDelayMs > 0) {
      await new Promise((resolve) => setTimeout(resolve, retryDelayMs));
    }
  }
  throw new Error("production browser session revocation was not verified");
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

async function listItemSummaries(
  workspaceID,
  query = "",
  recoveryDeadline,
  {
    maxPages = cleanupMaxItemPages,
    maxItems = cleanupMaxItems,
  } = {},
) {
  const items = [];
  const seenTokens = new Set();
  let pageToken = "";
  let pageCount = 0;
  do {
    pageCount += 1;
    if (pageCount > maxPages) throw new Error("item reconciliation page bound exceeded");
    const response = await connectPOST(
      "/scribe.v1.ItemService/ListItems",
      { pageSize: 100, pageToken, query },
      workspaceID,
      recoveryDeadline,
    );
    if (!response.ok()) throw new Error("item reconciliation request failed");
    const payload = await responseJSON(response, "invalid item reconciliation response");
    const pageItems = protoJSONRepeatedField(payload, "items");
    items.push(...pageItems);
    if (items.length > maxItems) throw new Error("item reconciliation result bound exceeded");
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

async function exactManifestItems(workspaceID, externalReferenceID, recoveryDeadline) {
  if (!readinessManifestReferencePattern.test(String(externalReferenceID ?? ""))) {
    throw new Error("manifest cleanup marker failed its identity contract");
  }
  // The run-unique external reference survives a lost ImportManifest response.
  // Query by that marker, then verify the complete persisted source tuple.
  const summaries = await listItemSummaries(
    workspaceID,
    externalReferenceID,
    recoveryDeadline,
  );
  const matches = [];
  for (const summary of summaries) {
    const summaryID = String(summary?.id ?? "").trim();
    if (
      summary?.sourceType !== "manifest"
      || !summaryID
      || summary?.externalReferenceId !== externalReferenceID
    ) continue;
    // Deliberately sequential: this bounded cleanup reconciliation verifies
    // the exact persisted marker and source URL before any deletion.
    // eslint-disable-next-line no-await-in-loop
    const item = await getItemForCleanup(summaryID, workspaceID, recoveryDeadline);
    if (!item) continue;
    if (
      String(item?.id ?? "").trim() !== summaryID
      || item?.sourceType !== "manifest"
      || item?.sourceUrl !== manifestURL
      || item?.externalReferenceId !== externalReferenceID
    ) {
      throw new Error("manifest cleanup source tuple changed");
    }
    matches.push(item);
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
  externalReferenceID,
  knownItemID,
  observation,
  cleanupDeadline,
) {
  await reconcileExactResources(observation, cleanupDeadline, async (recoveryDeadline) => {
    const matches = await exactManifestItems(workspaceID, externalReferenceID, recoveryDeadline);
    const candidates = new Map(matches.map((item) => [String(item.id ?? ""), item]));
    if (knownItemID && !candidates.has(knownItemID)) {
      const item = await getItemForCleanup(knownItemID, workspaceID, recoveryDeadline);
      if (item) candidates.set(knownItemID, item);
    }
    return [...candidates.values()];
  }, async (item, recoveryDeadline) => {
    const itemID = String(item?.id ?? "");
    if (
      !itemID
      || item?.sourceType !== "manifest"
      || item?.sourceUrl !== manifestURL
      || item?.externalReferenceId !== externalReferenceID
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
  return protoJSONRepeatedField(payload, "apiKeys").filter((key) => key?.name === keyName);
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

function productionReadinessItemKind(fullItem) {
  if (
    fullItem?.sourceType === "upload"
    && readinessUploadNamePattern.test(String(fullItem?.name ?? ""))
  ) return "upload";
  if (
    fullItem?.sourceType === "manifest"
    && productionReadinessManifestURLs.has(fullItem?.sourceUrl)
    && readinessManifestReferencePattern.test(String(fullItem?.externalReferenceId ?? ""))
  ) return "manifest";
  return "";
}

function assertProductionManifestCleanupClassifier() {
  const markedReferenceID = "browser-readiness-manifest-00000000-0000-4000-8000-000000000000";
  for (const sourceUrl of productionReadinessManifestURLs) {
    const ordinarySameURLManifest = {
      externalReferenceId: "ordinary-library-manifest",
      sourceType: "manifest",
      sourceUrl,
    };
    if (productionReadinessItemKind(ordinarySameURLManifest) !== "") {
      throw new Error("ordinary same-URL manifest matched the readiness janitor");
    }
    if (productionReadinessItemKind({
      ...ordinarySameURLManifest,
      externalReferenceId: markedReferenceID,
    }) !== "manifest") {
      throw new Error("marked readiness manifest missed the readiness janitor");
    }
  }
  if (productionReadinessItemKind({
    externalReferenceId: markedReferenceID,
    sourceType: "manifest",
    sourceUrl: `${manifestURL}?untrusted=1`,
  }) !== "") {
    throw new Error("unallowlisted readiness manifest matched the readiness janitor");
  }
}

assertProductionManifestCleanupClassifier();

async function findProductionReadinessOrphans(workspaceID, recoveryDeadline) {
  if (workspaceID !== productionJanitorWorkspaceID) {
    throw new Error("production janitor workspace identity failed");
  }
  const summaries = await listItemSummaries(
    workspaceID,
    "",
    recoveryDeadline,
    {
      maxPages: productionJanitorMaxItemPages,
      maxItems: productionJanitorMaxItems,
    },
  );
  const candidateSummaries = summaries.filter((summary) => (
    (summary?.sourceType === "upload" && readinessUploadNamePattern.test(String(summary?.name ?? "")))
    || (
      summary?.sourceType === "manifest"
      && readinessManifestReferencePattern.test(String(summary?.externalReferenceId ?? ""))
    )
  ));
  if (candidateSummaries.length > productionJanitorMaxItemVerifications) {
    throw new Error("production janitor item verification bound exceeded");
  }

  const orphans = [];
  const seenItemIDs = new Set();
  for (const summary of candidateSummaries) {
    const summaryID = String(summary?.id ?? "").trim();
    if (!summaryID || seenItemIDs.has(summaryID)) {
      throw new Error("production janitor item inventory identity failed");
    }
    seenItemIDs.add(summaryID);
    // A ListItems summary intentionally omits source_url. Always load and
    // verify the complete Item source tuple before considering deletion.
    // eslint-disable-next-line no-await-in-loop
    const fullItem = await getItemForCleanup(summaryID, workspaceID, recoveryDeadline);
    if (!fullItem) continue;
    if (
      String(fullItem?.id ?? "").trim() !== summaryID
      || fullItem?.name !== summary?.name
      || fullItem?.sourceType !== summary?.sourceType
      || fullItem?.externalReferenceId !== summary?.externalReferenceId
    ) {
      throw new Error("production janitor item source tuple changed");
    }
    const kind = productionReadinessItemKind(fullItem);
    if (summary?.sourceType === "upload" && kind !== "upload") {
      throw new Error("production janitor upload identity failed");
    }
    if (kind) orphans.push({ kind, item: fullItem });
  }

  const keyResponse = await connectPOST(
    "/scribe.v1.AuthService/ListAPIKeys",
    {},
    workspaceID,
    recoveryDeadline,
  );
  if (!keyResponse.ok()) throw new Error("production janitor token inventory failed");
  const keyPayload = await responseJSON(
    keyResponse,
    "invalid production janitor token inventory",
  );
  const apiKeys = protoJSONRepeatedField(keyPayload, "apiKeys");
  if (apiKeys.length > productionJanitorMaxAPIKeys) {
    throw new Error("production janitor token inventory bound exceeded");
  }
  const seenKeyIDs = new Set();
  for (const key of apiKeys) {
    if (
      !readinessAPIKeyNamePattern.test(String(key?.name ?? ""))
      || positiveID(key?.workspaceId) !== productionJanitorWorkspaceID
    ) continue;
    const keyID = positiveID(key?.id);
    if (!keyID || seenKeyIDs.has(keyID)) {
      throw new Error("production janitor token identity failed");
    }
    seenKeyIDs.add(keyID);
    orphans.push({ key, kind: "api-key" });
  }
  return orphans;
}

async function reconcileProductionReadinessOrphans() {
  if (browserMode !== "production") {
    throw new Error("production janitor cannot run outside production");
  }
  const workspaceID = productionJanitorWorkspaceID;
  const observation = newMutationObservation();
  observation.validated = true;
  const janitorDeadline = Math.min(mainScenarioDeadline, Date.now() + stageTimeoutMs);
  let deleteCount = 0;
  await reconcileExactResources(
    observation,
    janitorDeadline,
    async (recoveryDeadline) => {
      const orphans = await findProductionReadinessOrphans(workspaceID, recoveryDeadline);
      if (orphans.length > productionJanitorMaxDeletes - deleteCount) {
        throw new Error("production janitor delete bound exceeded");
      }
      return orphans;
    },
    async (orphan, recoveryDeadline) => {
      if (orphan?.kind === "api-key") {
        const keyID = positiveID(orphan?.key?.id);
        if (
          !keyID
          || !readinessAPIKeyNamePattern.test(String(orphan?.key?.name ?? ""))
          || positiveID(orphan?.key?.workspaceId) !== workspaceID
        ) {
          throw new Error("production janitor token identity changed");
        }
        await deleteAPIKeyDirect(keyID, workspaceID, recoveryDeadline);
        deleteCount += 1;
        return;
      }

      const expectedItem = orphan?.item;
      const itemID = String(expectedItem?.id ?? "").trim();
      if (!itemID || !["upload", "manifest"].includes(orphan?.kind)) {
        throw new Error("production janitor item identity failed");
      }
      const fullItem = await getItemForCleanup(itemID, workspaceID, recoveryDeadline);
      if (!fullItem) return;
      if (
        String(fullItem?.id ?? "").trim() !== itemID
        || fullItem?.name !== expectedItem?.name
        || fullItem?.sourceType !== expectedItem?.sourceType
        || fullItem?.sourceUrl !== expectedItem?.sourceUrl
        || fullItem?.externalReferenceId !== expectedItem?.externalReferenceId
        || productionReadinessItemKind(fullItem) !== orphan.kind
      ) {
        throw new Error("production janitor item source tuple changed");
      }
      await deleteItemDirect(itemID, workspaceID, recoveryDeadline);
      deleteCount += 1;
    },
  );
}

async function uploadAttemptRetryableResponseKind(attempt) {
  const outcome = attempt?.outcome;
  if (outcome?.kind !== "response" || outcome.response?.ok()) return undefined;
  let connectCode;
  let snapshotValid = false;
  const snapshot = responseJSONSnapshots.get(outcome.response);
  if (snapshot) {
    try {
      const result = await snapshot;
      if (result.ok) {
        snapshotValid = true;
        connectCode = result.payload?.code;
      }
    } catch {
      // The fixed HTTP status remains usable when the capped JSON snapshot is
      // absent, malformed, oversized, or otherwise unreadable.
    }
  }
  return classifyRetryableUploadResponse({ connectCode, snapshotValid, status: outcome.status });
}

async function manifestResponseFailureSubstage(response) {
  let connectCode;
  let connectMessage;
  let snapshotValid = false;
  const snapshot = responseJSONSnapshots.get(response);
  if (snapshot) {
    try {
      const result = await snapshot;
      if (result.ok) {
        snapshotValid = true;
        connectCode = result.payload?.code;
        connectMessage = result.payload?.message;
      }
    } catch {
      // The fixed HTTP status remains usable when the capped JSON snapshot is
      // absent, malformed, oversized, or otherwise unreadable.
    }
  }
  const responseKind = classifyManifestResponseFailure({
    connectCode,
    snapshotValid,
    status: response.status(),
  });
  manifestFailureSourceCategory = classifyManifestSourceFailure({
    connectCode,
    connectMessage,
    snapshotValid,
  });
  return responseKind ? `import-response-${responseKind}` : "import-response-status";
}

async function uploadAttemptIsRetryable(attempt) {
  const outcome = attempt?.outcome;
  if (!outcome || outcome.kind === "transport") return outcome?.status === 0;
  return Boolean(await uploadAttemptRetryableResponseKind(attempt));
}

async function requireUploadAttemptEvidence() {
  if (uploadImageAttempts.length < 1 || uploadImageAttempts.length > 5) {
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

async function loadContext(contextID, workspaceID) {
  const response = await connectPOST(
    "/scribe.v1.ContextService/GetContext",
    { contextId: contextID },
    workspaceID,
  );
  if (!response.ok()) throw new Error("processing context request failed");
  const payload = await responseJSON(response, "invalid processing context response");
  return payload?.context;
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
    || createHash("sha256").update(imageBody).digest("hex") !== readinessManifestImageSHA256
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

async function assertItemDeletePresentation(rootSelector, itemID, itemName) {
  const root = page.locator(rootSelector);
  const itemDelete = await waitForActionByValue(root, "data-item-delete", itemID, {
    timeoutMs: stageTimeoutMs,
    wait: (delayMs) => page.waitForTimeout(delayMs),
  });
  if (!itemDelete) throw new Error("missing item delete action");
  const presentation = await itemDelete.evaluate((button, expected) => {
    const svg = button.querySelector("svg");
    return {
      ariaLabel: button.getAttribute("aria-label") ?? "",
      deleteIdentity: button.getAttribute("data-item-delete") ?? "",
      destructive: button.classList.contains("bg-destructive"),
      exactText: button.textContent?.trim() === "Delete",
      finalAction: button.parentElement?.lastElementChild === button,
      trashIcon: button.querySelectorAll('svg[aria-hidden="true"]').length === 1
        && button.querySelectorAll("svg").length === 1
        && svg?.querySelector("path")?.getAttribute("d")
          === "M3 6h18M8 6V4h8v2m3 0-1 14H6L5 6m4 4v6m6-6v6",
      expected,
    };
  }, { deleteIdentity: itemID, label: `Delete item ${itemName}` });
  if (
    presentation.ariaLabel !== presentation.expected.label
    || presentation.deleteIdentity !== presentation.expected.deleteIdentity
    || !presentation.destructive
    || !presentation.exactText
    || !presentation.finalAction
    || !presentation.trashIcon
  ) {
    throw new Error("item delete action presentation failed");
  }
  return itemDelete;
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
  const itemDelete = await assertItemDeletePresentation(rootSelector, itemID, itemName);
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

async function currentEditorSelectedAnnotationID() {
  await page.waitForFunction(() => (
    typeof globalThis.__scribeReadinessEditorState?.selectedAnnotationId === "string"
  ));
  return page.evaluate(() => globalThis.__scribeReadinessEditorState.selectedAnnotationId);
}

async function selectedEditorAnnotationIDAtCount(expectedCount, previousAnnotationID) {
  const handle = await page.waitForFunction(({ annotationCount, priorAnnotationID }) => {
    const state = globalThis.__scribeReadinessEditorState;
    const selectedAnnotationID = String(state?.selectedAnnotationId ?? "");
    if (
      state?.annotationCount !== annotationCount
      || !selectedAnnotationID
      || selectedAnnotationID === priorAnnotationID
    ) return undefined;
    return selectedAnnotationID;
  }, { annotationCount: expectedCount, priorAnnotationID: previousAnnotationID });
  return handle.jsonValue();
}

async function waitForEditorSelection(annotationCount, annotationID) {
  if (!annotationID) throw new Error("missing editor selection identity");
  await page.waitForFunction(({ expectedAnnotationCount, expectedAnnotationID }) => {
    const state = globalThis.__scribeReadinessEditorState;
    return state?.annotationCount === expectedAnnotationCount
      && state?.selectedAnnotationId === expectedAnnotationID;
  }, {
    expectedAnnotationCount: annotationCount,
    expectedAnnotationID: annotationID,
  });
}

async function waitForEditorAnnotationState(expected) {
  await page.waitForFunction(({ annotationCount, statusMessage, wordAnnotationIds }) => {
    const state = globalThis.__scribeReadinessEditorState;
    return state?.annotationCount === annotationCount
      && state?.statusMessage === statusMessage
      && Array.isArray(state?.wordAnnotationIds)
      && state.wordAnnotationIds.length === wordAnnotationIds.length
      && state.wordAnnotationIds.every((annotationId, index) => (
        annotationId === wordAnnotationIds[index]
      ));
  }, expected);
}

async function currentEditorWordAnnotationIds() {
  await page.waitForFunction(() => (
    Array.isArray(globalThis.__scribeReadinessEditorState?.wordAnnotationIds)
  ));
  return page.evaluate(() => globalThis.__scribeReadinessEditorState.wordAnnotationIds);
}

async function currentEditorStructuralSnapshot() {
  const annotations = await page.evaluate(() => (
    globalThis.__scribeReadinessEditorState?.annotations
  ));
  return exactStructuralSnapshot(annotations, {
    lineText: deterministicJoinedLineText,
    wordTexts: deterministicWordTexts,
  });
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

async function waitForOverlayLineMarkers() {
  await page.locator('[data-scribe-granularity="line"]').first().waitFor({ state: "visible" });
}

async function waitForOverlayMarkersDisabled() {
  await page.waitForFunction(() => document.querySelector("[data-scribe-granularity]") === null);
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
let browserFaultUploadSubstage;
let browserFaultRateFamily;
let uploadFailureSubstage = "response-contract";
let uploadDurableFailureCategory;
let uploadRetryableResponseCategory;
let structureFailureSubstage = "draw-mode";
let tokenFailureSubstage = "post-home-presentation";
let manifestFailureSubstage = "library-navigation";
let manifestFailureSourceCategory;
let browserFaultMonitoringActive = true;
let browser;
let browserContext;
let page;
let baseURL;
let browserMode;
let productionStorageStateRemoved = false;
let productionSessionCookie;
let createdItemID;
let createdManifestItemID;
let createdManifestItemName;
let manifestExternalReferenceID;
let manifestIdempotencyKey;
let createdWorkspaceID;
let fixtureName;
let multiUploadItemID;
let multiUploadItemName;
let createdAPIKeyName;
let createdAPIKeyID;
let manifestImportAttempted = false;
let manifestRequestIdentityInjected = false;
let manifestReprocessRequestCount = 0;
let enrichAnnotationRequestCount = 0;
let editorAssetDelayObserved = false;
let editorAssetDelayReachedCompletion = false;
let editorAssetDelayFailed = false;
let mainScenarioWatchdogTimer;
let mainScenarioTimedOut = false;
let watchdogPageClose;
let expectedItemDeleteDialog;
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

function recordBrowserFault(faultCategory, uploadSubstage) {
  if (browserFaultMonitoringActive && !browserFaultCategory) {
    browserFaultCategory = faultCategory;
    if (faultCategory === "upload") browserFaultUploadSubstage = uploadSubstage;
  }
}

function recordBrowserRateFault(responseURL, resourceType) {
  if (browserFaultMonitoringActive && !browserFaultCategory) {
    browserFaultRateFamily = rateLimitFamily(responseURL, resourceType, baseURL);
    recordBrowserFault("rate");
  }
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
      browserFaultMonitoringActive = false;
      if (page && !page.isClosed()) {
        watchdogPageClose = page.close({ runBeforeUnload: false }).catch(() => undefined);
      }
      reject(new Error("main scenario deadline exceeded"));
    }, watchdogDelayMs);
  });
  const mainScenario = (async () => {
  browserMode = configuredBrowserMode();
  baseURL = configuredBaseURL(browserMode);
  if (!browserMode || !baseURL) throw new Error("invalid target");

  let productionState;
  if (browserMode === "production") {
    category = "token";
    productionState = await consumeProductionStorageState(baseURL);
  }

  category = "network";
  const chromiumIPv6Argument = await configureCanonicalIPv6Routing(baseURL.hostname);
  category = "home";
  browser = await chromium.launch({
    args: [chromiumIPv6Argument],
    headless: true,
  });
  if (mainScenarioTimedOut) {
    throw new Error("main scenario deadline exceeded");
  }
  let contextOptions = {
    baseURL: baseURL.href,
    acceptDownloads: false,
  };
  if (browserMode === "production") {
    contextOptions.storageState = productionState;
  }
  browserContext = await browser.newContext(contextOptions);
  contextOptions = undefined;
  productionState = undefined;
  category = "home";
  await warmInitialBrowserIngress();
  category = "token";
  await requireProductionSession();
  if (browserMode === "production") {
    category = "cleanup";
    await reconcileProductionReadinessOrphans();
  }
  category = "home";
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
      streamReady: undefined,
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
    document.addEventListener("scribe:transcription-stream-ready", (event) => {
      const detail = event.detail ?? {};
      if (globalThis.__scribeReadinessAutomaticTranscription) {
        globalThis.__scribeReadinessAutomaticTranscription.streamReady = {
          canvasId: String(detail.canvasId ?? ""),
          itemImageId: String(detail.itemImageId ?? ""),
          windowId: String(detail.windowId ?? ""),
        };
      }
    });
    document.addEventListener("scribe:editor-state", (event) => {
      const annotationPage = event.detail?.annotationPage;
      globalThis.__scribeReadinessEditorState = {
        annotations: Array.isArray(annotationPage?.items)
          ? structuredClone(annotationPage.items)
          : [],
        annotationCount: Array.isArray(annotationPage?.items) ? annotationPage.items.length : -1,
        focusedWordAnnotationId: String(event.detail?.focusedWordAnnotationId ?? ""),
        selectedAnnotationId: String(event.detail?.selectedAnnotationId ?? ""),
        statusMessage: String(event.detail?.statusMessage ?? ""),
        wordAnnotationIds: Array.isArray(annotationPage?.items)
          ? annotationPage.items
            .filter((annotation) => String(annotation?.textGranularity ?? "").toLowerCase() === "word")
            .map((annotation) => String(annotation?.id ?? "").trim())
            .filter(Boolean)
            .sort()
          : [],
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
      const fetchOptions = {
        maxRedirects: 0,
        maxRetries: 0,
        timeout: uploadRequestTimeoutMs,
      };
      if (requestURL.pathname === "/scribe.v1.ItemService/ImportManifest") {
        manifestFailureSubstage = "import-request-body";
        const headers = { ...request.headers() };
        delete headers["content-length"];
        fetchOptions.headers = headers;
        fetchOptions.postData = exactManifestImportPostData(request);
        manifestFailureSubstage = "import-upstream-request";
      }
      const upstreamResponse = await route.fetch(fetchOptions);
      if (requestURL.pathname === "/scribe.v1.ItemService/ImportManifest") {
        manifestFailureSubstage = "import-upstream-response";
      }
      const snapshot = await snapshotNavigationResponseJSON(upstreamResponse);
      navigationResponseJSONSnapshots.set(request, Promise.resolve(snapshot));
      if (requestURL.pathname === "/scribe.v1.ItemService/ImportManifest") {
        manifestFailureSubstage = "import-response-delivery";
      }
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
    const isStartUploadBatchResponse = sameOriginPOST
      && responseURL.pathname === "/scribe.v1.ItemService/StartUploadBatch";
    const isManifestImportResponse = sameOriginPOST
      && responseURL.pathname === "/scribe.v1.ItemService/ImportManifest";
    if (isUploadImageResponse) {
      const attempt = uploadImageAttemptByRequest.get(response.request());
      if (attempt) {
        attempt.outcome = { kind: "response", response, status: response.status() };
      } else {
        recordBrowserFault("upload", "response-contract");
      }
      return;
    }
    if (isStartUploadBatchResponse) {
      startUploadResponses.push(response);
      if (response.status() >= 400) recordBrowserFault("upload", "start-response");
      return;
    }
    if (isManifestImportResponse && response.status() >= 400) {
      recordBrowserFault("manifest");
      return;
    }
    if (isTargetHTTPSURL(responseURL, baseURL) && response.status() === 429) {
      recordBrowserRateFault(responseURL, response.request().resourceType());
      return;
    }
    if (
      isTargetHTTPSURL(responseURL, baseURL)
      && response.status() === 404
      && presentationAnnotationPathPattern.test(responseURL.pathname)
    ) {
      recordBrowserFault("annotations");
      return;
    }
    if (isTargetHTTPSURL(responseURL, baseURL) && response.status() >= 400) {
      const networkFaultCategory = responseNetworkFaultCategory(responseURL, response, baseURL);
      if (networkFaultCategory) recordBrowserFault(networkFaultCategory);
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
        recordBrowserFault("upload", "response-contract");
      }
      return;
    }
    if (
      sameOriginPOST
      && requestURL.pathname === "/scribe.v1.ItemService/StartUploadBatch"
    ) {
      recordBrowserFault("upload", "start-transport");
      return;
    }
    if (
      sameOriginPOST
      && requestURL.pathname === "/scribe.v1.ItemService/ImportManifest"
    ) {
      recordBrowserFault("manifest");
      return;
    }
    if (isTargetHTTPSURL(requestURL, baseURL) && !clientCancellation) {
      const networkFaultCategory = requestNetworkFaultCategory(requestURL, request, baseURL);
      if (networkFaultCategory) recordBrowserFault(networkFaultCategory);
    }
  });
  page.on("console", (message) => {
    if (
      message.type() === "error"
      && /content security policy|violates.*(?:csp|policy)|refused to (?:connect|load).*policy/i.test(message.text())
    ) {
      recordBrowserFault("csp");
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
        recordBrowserFault("token");
        expectedDialog.reject(error);
      });
      return;
    }
    if (expectedItemDeleteDialog) {
      const expectedDialog = expectedItemDeleteDialog;
      expectedItemDeleteDialog = undefined;
      expectedDialog.reject(new Error("unexpected item delete confirmation"));
    }
    recordBrowserFault("token");
    void dialog.dismiss().catch(() => {
      recordBrowserFault("token");
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
  if (browserMode === "production" && createdWorkspaceID !== "1") {
    throw new Error("production workspace identity failed");
  }
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
  let uploadOutcome;
  try {
    uploadOutcome = await page.waitForFunction(() => {
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
    }, undefined, {
      timeout: remainingUploadSequenceTimeoutMs(mainScenarioDeadline),
    });
  } catch (error) {
    uploadFailureSubstage = classifyUploadFailure({
      handoffTimedOut: mainScenarioTimedOut
        || Date.now() >= mainScenarioDeadline
        || error?.name === "TimeoutError",
    });
    throw error;
  }
  if (await uploadOutcome.jsonValue() !== "handoff") {
    uploadDurableFailureCategory = classifyDurableUploadFailure(
      await page.locator("#shell-upload-status").textContent(),
      fixtureName,
    );
    const finalAttemptOutcome = uploadImageAttempts.at(-1)?.outcome;
    const attemptSucceeded = finalAttemptOutcome?.kind === "response"
      && finalAttemptOutcome.response?.ok() === true;
    uploadRetryableResponseCategory = finalAttemptOutcome?.kind === "response"
      ? await uploadAttemptRetryableResponseKind({ outcome: finalAttemptOutcome })
      : undefined;
    let attemptRetryable = Boolean(uploadRetryableResponseCategory);
    if (finalAttemptOutcome?.kind !== "response" && finalAttemptOutcome) {
      attemptRetryable = await uploadAttemptIsRetryable({ outcome: finalAttemptOutcome });
    }
    uploadFailureSubstage = classifyUploadFailure({
      terminal: true,
      attemptKind: finalAttemptOutcome?.kind,
      attemptSucceeded,
      attemptRetryable,
    });
    throw new Error("upload did not reach editor handoff");
  }

  uploadFailureSubstage = "response-contract";
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

  category = "handoff";
  const durableJob = await loadTranscriptionJob(jobID, workspaceID);
  const durableContextID = positiveID(durableJob?.contextId);
  if (
    positiveID(durableJob?.id) !== jobID
    || positiveID(durableJob?.itemImageId) !== itemImageID
    || !durableContextID
  ) {
    throw new Error("default context did not resolve durably");
  }
  const durableContext = await loadContext(durableContextID, workspaceID);
  if (
    positiveID(durableContext?.id) !== durableContextID
    || String(durableContext?.userId ?? "0") !== "0"
    || durableContext?.name !== "Tesseract OCR"
    || durableContext?.isDefault !== true
    || durableContext?.segmentationModel !== "scribe"
    || durableContext?.transcriptionProvider !== "tesseract"
    || durableContext?.transcriptionModel !== "tesseract"
  ) {
    throw new Error("durable default context did not match Scribe segmentation with Tesseract transcription");
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
    const statusComplete = status === "Batch transcription complete. Updated text is now available in the editor.";
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
      && statusComplete
    ) return "proved";
    if (Date.now() < expected.visualDeadline) return "";
    if (!statusComplete) return "completed-without-terminal-status";
    if (!progressesExactlyInOrder(segments)) return "completed-without-segments";
    if (!progressesExactlyInOrder(results)) return "completed-without-results";
    if (badges.length === 0) return "completed-without-visible-badges";
    return "completed-without-badge-order";
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
  assertBrowserHealthy();

  // Mount a fresh editor without pinning it to the completed upload job, then
  // enqueue a second durable job. This makes the overlay and SSE bridge ready
  // before any task progress, exercising the real in-flight line-by-line path
  // separately from the deliberately late-loaded catch-up path above.
  const liveEditorPath = `/editor?itemId=${encodeURIComponent(createdItemID)}&itemImageId=${itemImageID}&workspace_id=${workspaceID}`;
  const liveEditorURL = new URL(liveEditorPath, baseURL);
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
      && response.request().headers()["referer"] === liveEditorURL.href
      && url.searchParams.get("item_image_id") === itemImageID;
  }, { timeout: stageTimeoutMs });
  await navigate(liveEditorPath);
  await page.locator("#editor-transcription-status").waitFor({ state: "attached" });
  await page.getByRole("heading", { name: "Editor", exact: true }).waitFor({ state: "visible" });
  // The server captures the outbox high-water mark before flushing this SSE
  // response. The correlated application marker below is emitted only after
  // that stream-ready signal has reconciled the durable job snapshot.
  await liveEventStreamReady;
  await page.waitForFunction(() => {
    const evidence = globalThis.__scribeReadinessAutomaticTranscription;
    const activeCanvas = globalThis.__scribeReadinessActiveCanvas;
    const routeItemImageID = new URL(window.location.href).searchParams.get("itemImageId");
    return globalThis.__scribeReadinessAutomaticTranscription?.overlayReady === true
      && Boolean(routeItemImageID)
      && activeCanvas?.itemImageId === routeItemImageID
      && Boolean(activeCanvas?.canvasId)
      && Boolean(activeCanvas?.windowId)
      && evidence?.streamReady?.itemImageId === routeItemImageID
      && evidence?.streamReady?.canvasId === activeCanvas.canvasId
      && evidence?.streamReady?.windowId === activeCanvas.windowId
      && document.getElementById("editor-transcription-status")?.textContent?.trim()
        === "Batch transcription complete. Updated text is now available in the editor.";
  }, undefined, { timeout: transcriptionTimeoutMs });
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

  const liveCompletedJob = await waitForTerminalTranscriptionJob(liveJobID, workspaceID);
  if (
    !transcriptionJobCompleted(liveCompletedJob)
    || Number(liveCompletedJob?.attemptCount ?? -1) !== 1
    || Number(liveCompletedJob?.failedSegments ?? 0) !== 0
    || Number(liveCompletedJob?.completedSegments ?? -1) !== 2
    || Number(liveCompletedJob?.totalSegments ?? -1) !== 2
  ) {
    throw new Error("live transcription job did not complete all segments in one attempt");
  }
  const liveAutomaticTranscriptionProof = await page.waitForFunction((expected) => {
    const evidence = globalThis.__scribeReadinessAutomaticTranscription;
    const status = document.getElementById("editor-transcription-status")?.textContent?.trim() ?? "";
    const statusComplete = status === "Batch transcription complete. Updated text is now available in the editor.";
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
      && badgeMovesExactlyInOrder
      && statusComplete) return "proved";
    if (Date.now() < expected.visualDeadline) return "";
    if (!statusComplete) return "completed-without-terminal-status";
    if (!progressesExactlyInOrder(segments)) return "completed-without-segments";
    if (!progressesExactlyInOrder(results)) return "completed-without-results";
    if (badges.length === 0) return "completed-without-visible-badges";
    return "completed-without-badge-order";
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
  await waitForOverlayLineMarkers();
  await page.getByRole("button", { name: "Edit overlay", exact: true }).click();
  await page.getByRole("button", { name: "Read overlay", exact: true }).click();
  await page.getByRole("button", { name: "Outline overlay", exact: true }).click();
  await page.getByRole("button", { name: "Overlay off", exact: true }).waitFor({ state: "visible" });
  await waitForOverlayMarkersDisabled();
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
  structureFailureSubstage = "draw-mode";
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

  structureFailureSubstage = "centered-line";
  const selectedAnnotationIDBeforeCenteredLine = await currentEditorSelectedAnnotationID();
  const centeredLineButton = page.getByRole("button", {
    name: centeredLineAccessibleName,
    exact: true,
  });
  await centeredLineButton.click();
  await page.getByRole("status").filter({ hasText: "Draft line created." }).waitFor({ state: "visible" });
  const centeredLineAnnotationID = await selectedEditorAnnotationIDAtCount(
    initialDraftCount + 1,
    selectedAnnotationIDBeforeCenteredLine,
  );
  await waitForSaveEnabled();

  structureFailureSubstage = "undo-redo";
  await page.getByRole("button", { name: "Undo", exact: true }).click();
  await waitForEditorAnnotationCount(initialDraftCount);
  await page.getByRole("button", { name: "Redo", exact: true }).click();
  await waitForEditorSelection(initialDraftCount + 1, centeredLineAnnotationID);

  structureFailureSubstage = "delete-line";
  await editorDelete.click();
  await waitForEditorAnnotationCount(initialDraftCount);

  structureFailureSubstage = "line-edit";
  const lineTokenInputs = page.getByRole("textbox", { name: /^Edit line token [1-9][0-9]*$/ });
  await lineTokenInputs.first().waitFor({ state: "visible" });
  while (await lineTokenInputs.count() > 1) {
    const previousLineTokenCount = await lineTokenInputs.count();
    await lineTokenInputs.last().fill("");
    await page.waitForFunction((previousCount) => (
      document.querySelectorAll('input[aria-label^="Edit line token "]').length < previousCount
    ), previousLineTokenCount);
  }
  const lineToken = lineTokenInputs.first();
  await lineToken.fill(deterministicLineText);
  const expectedTokens = deterministicLineText.split(" ");
  await page.waitForFunction((tokens) => {
    const inputs = Array.from(document.querySelectorAll('input[aria-label^="Edit line token "]'));
    return inputs.length === tokens.length
      && inputs.every((input, index) => input.value === tokens[index]);
  }, expectedTokens);

  structureFailureSubstage = "split-words";
  const beforeSplitWordsCount = await currentEditorAnnotationCount();
  await requireConnectAction(
    "/scribe.v1.AnnotationService/SplitLineIntoWords",
    () => page.getByRole("button", { name: "Split Words", exact: true }).click(),
  );
  await page.getByRole("status").filter({ hasText: "Words created." }).waitFor({ state: "visible" });
  const splitWordsCount = await waitForEditorAnnotationCountDirection(beforeSplitWordsCount, "increase");

  structureFailureSubstage = "add-word";
  await page.getByRole("textbox", { name: "Edit word gamma", exact: true }).focus();
  await page.waitForFunction(() => {
    const state = globalThis.__scribeReadinessEditorState;
    return Boolean(state?.focusedWordAnnotationId)
      && state.focusedWordAnnotationId === state.selectedAnnotationId;
  });
  await page.getByRole("button", {
    name: "Add a word annotation beside the selection",
    exact: true,
  }).click();
  await page.getByRole("status").filter({ hasText: "Draft word created." }).waitFor({ state: "visible" });
  await waitForEditorAnnotationCount(splitWordsCount + 1);

  structureFailureSubstage = "word-history";
  await page.getByRole("button", { name: "Undo", exact: true }).click();
  await waitForEditorAnnotationCount(splitWordsCount);
  await page.getByRole("button", { name: "Redo", exact: true }).click();
  await waitForEditorAnnotationCount(splitWordsCount + 1);
  const addedWord = page.getByRole("textbox", { name: "Edit word with empty text", exact: true });
  await addedWord.waitFor({ state: "visible" });
  await addedWord.fill("epsilon");
  await page.getByRole("textbox", { name: "Edit word epsilon", exact: true }).waitFor({ state: "visible" });

  structureFailureSubstage = "join-words";
  const beforeJoinWordsCount = await currentEditorAnnotationCount();
  const beforeJoinWordAnnotationIds = await currentEditorWordAnnotationIds();
  await page.getByRole("button", { name: "Join Words", exact: true }).click();
  const joinWordsDialog = page.getByRole("dialog", { name: "Choose words to join", exact: true });
  await joinWordsDialog.waitFor({ state: "visible" });
  await selectAllAdditionalCandidates(joinWordsDialog, /^Word [0-9]+:/);
  await requireConnectAction(
    "/scribe.v1.AnnotationService/JoinWordsIntoLine",
    () => joinWordsDialog.getByRole("button", { name: "Join selected words", exact: true }).click(),
  );
  await page.getByRole("status").filter({ hasText: "Words joined." }).waitFor({ state: "visible" });
  await waitForEditorAnnotationState({
    annotationCount: beforeJoinWordsCount, statusMessage: "Words joined.",
    wordAnnotationIds: beforeJoinWordAnnotationIds,
  });
  const joinedWordsCount = beforeJoinWordsCount;

  structureFailureSubstage = "split-line";
  await page.getByRole("button", { name: "Split Line", exact: true }).click();
  const splitLineDialog = page.getByRole("dialog", { name: "Choose a split boundary", exact: true });
  await splitLineDialog.waitFor({ state: "visible" });
  await splitLineDialog.getByText("beta gamma epsilon", { exact: true }).waitFor({ state: "visible" });
  await requireConnectAction(
    "/scribe.v1.AnnotationService/SplitLineIntoTwoLines",
    () => splitLineDialog.getByRole("button", { name: "Split at boundary", exact: true }).click(),
  );
  await page.getByRole("status").filter({ hasText: "Line split." }).waitFor({ state: "visible" });
  await waitForEditorAnnotationCount(joinedWordsCount + 1);

  structureFailureSubstage = "join-lines";
  await page.getByRole("button", { name: "Join Lines", exact: true }).click();
  const joinLinesDialog = page.getByRole("dialog", { name: "Choose lines to join", exact: true });
  await joinLinesDialog.waitFor({ state: "visible" });
  await selectFirstAdditionalCandidate(
    joinLinesDialog,
    /^Line [0-9]+: (?:browser readiness alpha|beta gamma epsilon)$/,
  );
  const beforeJoinLinesCount = await currentEditorAnnotationCount();
  await requireConnectAction(
    "/scribe.v1.AnnotationService/JoinLines",
    () => joinLinesDialog.getByRole("button", { name: "Join selected lines", exact: true }).click(),
  );
  await page.getByRole("status").filter({ hasText: "Lines joined." }).waitFor({ state: "visible" });
  await waitForEditorAnnotationCount(beforeJoinLinesCount - 1);

  structureFailureSubstage = "snapshot";
  const expectedSavedStructure = await currentEditorStructuralSnapshot();
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
  assertSavedStructuralPage(savedAnnotationSnapshot.page, expectedSavedStructure);
  assertBrowserHealthy();

  category = "publish";
  await page.getByRole("button", { name: "Publish edits", exact: true }).click();
  await page.getByText("Edits published.", { exact: true }).waitFor({ state: "visible" });
  const publishedAnnotationPath = `/presentation/v3/item-image-${itemImageID}/canvas/page-1/annotations`;
  const publishedAnnotationPage = await waitForPublishedAnnotationPage(publishedAnnotationPath);
  assertTextualAnnotationPage(publishedAnnotationPage);
  assertSavedStructuralPage(publishedAnnotationPage, expectedSavedStructure);
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

  category = "home";
  await navigate("/");
  await assertItemDeletePresentation("#shell-content", createdItemID, fixtureName);
  await assertItemDeletePresentation("#shell-sidebar", createdItemID, fixtureName);
  category = "token";
  tokenFailureSubstage = "post-home-presentation";
  await page.locator("#shell-account-button").click();
  tokenFailureSubstage = "settings-open";
  await page.getByRole("heading", { name: "Workspace and account settings", exact: true }).waitFor({ state: "visible" });
  await page.locator("#settings-api-key-form").waitFor({ state: "visible" });
  tokenFailureSubstage = "key-creation";
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

  tokenFailureSubstage = "key-display";
  const tokenDialog = page.getByRole("dialog", { name: "Copy workspace token", exact: true });
  await tokenDialog.waitFor({ state: "visible" });
  const tokenField = tokenDialog.getByRole("textbox", { name: "Workspace token", exact: true });
  if (await tokenField.getAttribute("readonly") === null) throw new Error("token is editable");
  const tokenValue = await tokenField.inputValue();
  if (!tokenValue.startsWith("scribe_") || tokenValue.length < 24) throw new Error("invalid token");
  tokenFailureSubstage = "key-display-copy";
  // navigator.clipboard.writeText() throws NotAllowedError in Chromium unless
  // the document has focus; a headless single-page execution does not
  // guarantee that on its own.
  await page.bringToFront();
  await tokenDialog.getByRole("button", { name: "Copy token", exact: true }).click();
  await tokenDialog.getByText("Copied to clipboard.", { exact: true }).waitFor({ state: "visible" });
  const copiedValue = await page.evaluate(() => navigator.clipboard.readText());
  if (copiedValue !== tokenValue) throw new Error("token copy failed");
  tokenFailureSubstage = "key-display-done";
  await tokenDialog.getByRole("button", { name: "Done", exact: true }).click();
  await tokenDialog.waitFor({ state: "hidden" });
  if (await page.locator("#shell-api-key-value").inputValue() !== "") {
    throw new Error("token remained in document");
  }
  tokenFailureSubstage = "key-display-clear";
  await page.bringToFront();
  await page.evaluate(() => navigator.clipboard.writeText(""));
  assertBrowserHealthy();

  category = "manifest";
  manifestFailureSubstage = "library-navigation";
  successfulImageResponses.length = 0;
  await navigate("/");
  manifestFailureSubstage = "import-form";
  await page.locator('[data-library-tab="manifest"]').click();
  if (await page.locator("#library-context-select").inputValue() !== "0") {
    throw new Error("manifest import did not retain default context");
  }
  const importMode = page.locator('input[name="library-manifest-mode"][value="import"]');
  const reprocessMode = page.locator('input[name="library-manifest-mode"][value="reprocess"]');
  if (!await importMode.isChecked() || await reprocessMode.isChecked()) {
    throw new Error("manifest import mode was not the default");
  }
  const manifestIdentity = newReadinessManifestIdentity();
  manifestExternalReferenceID = manifestIdentity.externalReferenceID;
  manifestIdempotencyKey = manifestIdentity.idempotencyKey;
  assertMainScenarioActive();
  await page.getByLabel("IIIF manifest URL", { exact: true }).fill(manifestURL);
  manifestImportAttempted = true;
  manifestFailureSubstage = "import-request";
  const manifestResponsePromise = page.waitForResponse((response) => (
    sameOriginConnectResponse(response, "/scribe.v1.ItemService/ImportManifest")
  ), { timeout: uploadRequestTimeoutMs });
  await page.getByRole("button", { name: "Ingest manifest", exact: true }).click();
  const manifestResponse = await manifestResponsePromise;
  manifestFailureSubstage = "import-response-status";
  if (!manifestResponse.ok()) {
    manifestFailureSubstage = await manifestResponseFailureSubstage(manifestResponse);
    throw new Error("manifest import failed");
  }
  manifestFailureSubstage = "import-response-settlement";
  await waitForMutationResponsesToSettle(manifestMutation);
  requireTerminalRequestSuccess(manifestResponse, "manifest import request failed");
  manifestFailureSubstage = "import-contract";
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
    || !manifestRequestIdentityInjected
    || manifestReprocessRequestCount !== 0
    || !createdManifestItemID
    || !createdManifestItemName
    || manifestItem?.sourceType !== "manifest"
    || manifestItem?.sourceUrl !== manifestURL
    || manifestItem?.externalReferenceId !== manifestExternalReferenceID
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
  manifestFailureSubstage = "editor-navigation";
  await page.waitForURL((url) => (
    url.pathname === "/editor"
    && url.searchParams.get("itemId") === createdManifestItemID
    && url.searchParams.get("itemImageId") === manifestFirstImageID
    && url.searchParams.get("workspace_id") === workspaceID
  ), { timeout: uploadRequestTimeoutMs });
  manifestFailureSubstage = "editor-mount";
  await page.locator("#mirador-viewer").waitFor({ state: "visible" });
  const manifestActionPanel = page.locator('[data-scribe-action-panel="true"]');
  await manifestActionPanel.waitFor({ state: "visible" });
  if (await manifestActionPanel.count() !== 1) {
    throw new Error("manifest editor action panel did not mount exactly once");
  }
  manifestFailureSubstage = "first-canvas";
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
  manifestFailureSubstage = "first-image";
  await requireLoadedOpenSeadragonImage(manifestFirstImageResource);

  manifestFailureSubstage = "first-annotations";
  const manifestFirstAnnotationPath = `/presentation/v3/item-image-${manifestFirstImageID}/canvas/page-1/annotations`;
  const manifestAnnotationSnapshot = await loadCanonicalAnnotationSnapshot(
    manifestFirstImageID,
    workspaceID,
  );
  const manifestAnnotationPage = manifestAnnotationSnapshot.page;
  assertTextualAnnotationPage(manifestAnnotationPage);
  assertReadinessFixtureAnnotations(manifestAnnotationPage);
  assertExactPresentationAnnotationPage(manifestAnnotationPage, manifestFirstAnnotationPath);
  if (
    manifestAnnotationSnapshot.itemImageID !== manifestFirstImageID
    || manifestAnnotationSnapshot.canvasURI !== manifestFirstCanvasID
  ) {
    throw new Error("manifest canonical page identity mismatch");
  }
  await waitForEditorAnnotationCount(manifestAnnotationPage.items.length);

  // Publication is scoped to this disposable imported item and is removed by
  // the exact manifest cleanup below. It proves the canonical page reaches the
  // sole public Presentation surface without modifying the external source.
  manifestFailureSubstage = "first-publication-request";
  await requireConnectAction(
    "/scribe.v1.AnnotationService/PublishAnnotationPage",
    () => page.getByRole("button", { name: "Publish edits", exact: true }).click(),
  );
  manifestFailureSubstage = "first-publication-confirmation";
  await page.getByText("Edits published.", { exact: true }).waitFor({ state: "visible" });
  manifestFailureSubstage = "first-publication-resource";
  const manifestPublishedAnnotationPage = await waitForPublishedAnnotationPage(
    manifestFirstAnnotationPath,
  );
  manifestFailureSubstage = "first-publication-contract";
  assertExactPresentationAnnotationPage(
    manifestPublishedAnnotationPage,
    manifestFirstAnnotationPath,
  );

  const nextCanvas = page.getByRole("button", { name: "Next item", exact: true });
  await nextCanvas.waitFor({ state: "visible" });
  if (!await nextCanvas.isEnabled()) throw new Error("next manifest Canvas action was disabled");
  manifestFailureSubstage = "second-image";
  await requireLoadedOpenSeadragonImage(
    manifestSecondImageResource,
    () => nextCanvas.click(),
  );
  manifestFailureSubstage = "second-canvas";
  await waitForActiveCanvasIdentity({
    canvasID: manifestSecondCanvasID,
    itemID: createdManifestItemID,
    itemImageID: manifestSecondImageID,
    workspaceID,
  });
  await assertOpenSeadragonCanvas();

  manifestFailureSubstage = "second-annotations";
  const manifestSecondAnnotationPath = `/presentation/v3/item-image-${manifestSecondImageID}/canvas/page-1/annotations`;
  const manifestSecondAnnotationSnapshot = await loadCanonicalAnnotationSnapshot(
    manifestSecondImageID,
    workspaceID,
  );
  assertTextualAnnotationPage(manifestSecondAnnotationSnapshot.page);
  assertReadinessFixtureAnnotations(manifestSecondAnnotationSnapshot.page);
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

  manifestFailureSubstage = "second-overlay";
  const manifestRetranscribe = page.getByRole("button", { name: "Retranscribe", exact: true });
  await manifestRetranscribe.waitFor({ state: "visible" });
  await page.waitForFunction(() => {
    const retranscribe = document.querySelector('button[aria-label="Retranscribe"]');
    return retranscribe instanceof HTMLButtonElement && !retranscribe.disabled;
  });
  await page.getByRole("button", { name: "Overlay off", exact: true }).click();
  await page.getByRole("button", { name: "Edit overlay", exact: true }).waitFor({ state: "visible" });
  await page.locator(".scribe-text-overlay").waitFor({ state: "visible" });
  await waitForOverlayLineMarkers();
  await page.getByRole("button", { name: "Edit overlay", exact: true }).click();
  await page.getByRole("button", { name: "Read overlay", exact: true }).click();
  await page.getByRole("button", { name: "Outline overlay", exact: true }).click();
  await page.getByRole("button", { name: "Overlay off", exact: true }).waitFor({ state: "visible" });
  await waitForOverlayMarkersDisabled();
  if (!await manifestRetranscribe.isEnabled()) {
    throw new Error("manifest editor action was unusable after overlay cycling");
  }

  manifestFailureSubstage = "second-publication-request";
  await requireConnectAction(
    "/scribe.v1.AnnotationService/PublishAnnotationPage",
    () => page.getByRole("button", { name: "Publish edits", exact: true }).click(),
  );
  manifestFailureSubstage = "second-publication-resource";
  const manifestSecondPublishedAnnotationPage = await waitForPublishedAnnotationPage(
    manifestSecondAnnotationPath,
  );
  manifestFailureSubstage = "second-publication-contract";
  assertExactPresentationAnnotationPage(
    manifestSecondPublishedAnnotationPage,
    manifestSecondAnnotationPath,
  );
  assertBrowserHealthy();

  category = "upload-multi";
  await navigate("/");
  await page.locator('[data-library-tab="multi"]').click();
  const multiUploadFixture = exactReadinessSmokeFixture();
  const multiUploadFixtureNames = [
    `browser-readiness-${randomUUID()}.png`,
    `browser-readiness-${randomUUID()}.png`,
  ];
  multiUploadItemName = multiUploadFixtureNames[0];
  assertMainScenarioActive();
  await page.locator("#library-multi-files").setInputFiles(multiUploadFixtureNames.map((name) => ({
    name,
    mimeType: "image/png",
    buffer: multiUploadFixture,
  })));
  const multiUploadStartResponsePromise = page.waitForResponse((response) => (
    sameOriginConnectResponse(response, "/scribe.v1.ItemService/StartUploadBatch")
  ), { timeout: uploadRequestTimeoutMs });
  await page.getByRole("button", { name: "Upload or resume batch", exact: true }).click();
  const multiUploadStartResponse = await multiUploadStartResponsePromise;
  if (!multiUploadStartResponse.ok()) throw new Error("multi upload start failed");
  const multiUploadStartPayload = await responseJSON(multiUploadStartResponse, "invalid multi upload start response");
  multiUploadItemID = String(multiUploadStartPayload?.item?.id ?? "").trim();
  if (
    !multiUploadItemID
    || multiUploadStartPayload?.item?.name !== multiUploadItemName
    || multiUploadStartPayload?.item?.sourceType !== "upload"
  ) {
    throw new Error("multi upload item identity mismatch");
  }
  await page.waitForFunction((expectedFilename) => {
    const text = document.getElementById("library-multi-status")?.textContent?.trim() ?? "";
    return text === `Uploaded 2/2: ${expectedFilename}`;
  }, multiUploadFixtureNames[1], { timeout: uploadRequestTimeoutMs });
  assertMainScenarioActive();
  const multiUploadedItem = await getItemForCleanup(multiUploadItemID, createdWorkspaceID, mainScenarioDeadline);
  const multiUploadedImages = Array.isArray(multiUploadedItem?.images) ? multiUploadedItem.images : [];
  const multiUploadedImageIDs = new Set(multiUploadedImages.map((image) => positiveID(image?.id)));
  if (
    multiUploadedItem?.name !== multiUploadItemName
    || multiUploadedItem?.sourceType !== "upload"
    || multiUploadedImages.length !== 2
    || multiUploadedImageIDs.size !== 2
    || [...multiUploadedImageIDs].some((imageID) => !imageID)
  ) {
    throw new Error("multi upload item did not retain the exact declared image set");
  }
  assertBrowserHealthy();

  category = "cleanup";
  await navigate("/");
  await deleteItemThroughLibrary("#shell-content", createdItemID, fixtureName);
  createdItemID = undefined;
  await deleteItemThroughLibrary("#shell-sidebar", createdManifestItemID, createdManifestItemName);
  createdManifestItemID = undefined;
  await deleteItemThroughLibrary("#shell-content", multiUploadItemID, multiUploadItemName);
  multiUploadItemID = undefined;
  assertBrowserHealthy();
  })();
  await Promise.race([mainScenario, mainScenarioWatchdog]);
} catch {
  if (mainScenarioTimedOut && category === "upload") {
    uploadFailureSubstage = "handoff-timeout";
  }
  failureCategory = browserFaultCategory ?? category;
} finally {
  failureCategory ??= browserFaultCategory;
  browserFaultMonitoringActive = false;
  if (browserMode === "production" && !productionStorageStateRemoved) {
    try {
      await unlink(productionStorageStatePath);
      productionStorageStateRemoved = true;
    } catch (error) {
      if (error?.code !== "ENOENT") {
        tokenFailureSubstage = "final-cleanup";
        failureCategory ??= "token";
      }
    }
  }
  if (mainScenarioWatchdogTimer !== undefined) {
    clearTimeout(mainScenarioWatchdogTimer);
    mainScenarioWatchdogTimer = undefined;
  }
  if (watchdogPageClose) {
    try {
      await waitForOperationBeforeDeadline(
        watchdogPageClose,
        globalCleanupDeadline,
        "watchdog page close deadline exceeded",
      );
    } catch {
      failureCategory ??= "cleanup";
    }
  }
  if (
    browserContext
    && createdWorkspaceID
    && (createdAPIKeyName || createdAPIKeyID || fixtureName || multiUploadItemName || manifestImportAttempted)
  ) {
    category = "cleanup";
    let cleanupFailed = false;

    // Stop browser-side retry timers before calculating the latest observed
    // mutation horizon. Direct API reconciliation keeps the session cookie.
    if (page && !page.isClosed()) {
      try {
        await waitForOperationBeforeDeadline(
          page.close({ runBeforeUnload: false }),
          Math.min(globalCleanupDeadline, Date.now() + browserCloseBudgetMs),
          "cleanup page close deadline exceeded",
        );
      } catch {
        cleanupFailed = true;
      }
    }

    if (manifestImportAttempted) {
      try {
        await cleanupExactManifestItems(
          createdWorkspaceID,
          manifestExternalReferenceID,
          createdManifestItemID,
          manifestMutation,
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
        tokenFailureSubstage = "key-deletion";
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

    if (multiUploadItemName) {
      try {
        await cleanupExactUploadItems(
          multiUploadItemName,
          createdWorkspaceID,
          multiUploadItemID,
          uploadMutation,
          globalCleanupDeadline,
        );
      } catch {
        cleanupFailed = true;
      }
    }

    if (cleanupFailed) failureCategory ??= category;
  }
  if (browserMode === "production" && productionSessionCookie) {
    try {
      await revokeProductionSession(sessionRevocationDeadline);
    } catch {
      tokenFailureSubstage = "logout-proof";
      failureCategory ??= "token";
    }
  }
  if (browser) {
    try {
      await waitForOperationBeforeDeadline(
        browser.close(),
        browserShutdownDeadline,
        "browser close deadline exceeded",
      );
    } catch {
      failureCategory ??= "cleanup";
    }
  }
}

if (failureCategory) {
  process.stderr.write(`browser readiness failed: ${failureCategory}\n`);
  if (failureCategory === "upload") {
    process.stderr.write(`${uploadFailureMarker(
      browserFaultUploadSubstage ?? uploadFailureSubstage,
    )}\n`);
    if (uploadRetryableResponseCategory) {
      process.stderr.write(`${uploadRetryableResponseMarker(uploadRetryableResponseCategory)}\n`);
    }
    if (uploadDurableFailureCategory) {
      process.stderr.write(`${uploadDurableFailureMarker(uploadDurableFailureCategory)}\n`);
    }
  }
  if (failureCategory === "structure") {
    process.stderr.write(`browser readiness structure substage: ${structureFailureSubstage}\n`);
  }
  if (failureCategory === "token") {
    process.stderr.write(`browser readiness token substage: ${tokenFailureSubstage}\n`);
  }
  if (failureCategory === "manifest") {
    process.stderr.write(`browser readiness manifest substage: ${manifestFailureSubstage}\n`);
    if (manifestFailureSourceCategory) {
      process.stderr.write(`${manifestSourceFailureMarker(manifestFailureSourceCategory)}\n`);
    }
  }
  if (failureCategory === "rate" && browserFaultRateFamily) {
    process.stderr.write(`browser readiness rate limit: ${browserFaultRateFamily}\n`);
  }
  process.exitCode = failureCategory === "manifest"
    ? manifestFailureExitCode(manifestFailureSubstage)
    : readinessFailureExitCodes.get(failureCategory) ?? 1;
} else {
  process.stdout.write("browser readiness passed\n");
}
