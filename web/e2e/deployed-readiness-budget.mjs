// Upload segmentation may consume the application's full 240-second
// scale-to-zero inference budget plus the backend's commit tail.
export const uploadRequestTimeoutMs = 300_000;
export const cleanupCommitHorizonMs = uploadRequestTimeoutMs;

const manifestFailureExitCodes = new Map([
  ["library-navigation", 75],
  ["import-form", 76],
  ["import-request", 77],
  ["import-contract", 78],
  ["editor-navigation", 79],
  ["editor-mount", 80],
  ["first-canvas", 81],
  ["first-image", 82],
  ["first-annotations", 83],
  ["first-publication", 84],
  ["second-image", 85],
  ["second-canvas", 86],
  ["second-annotations", 87],
  ["second-overlay", 88],
  ["second-publication", 89],
  ["import-request-body", 90],
  ["import-upstream-request", 91],
  ["import-upstream-response", 92],
  ["import-response-delivery", 93],
  ["import-response-status", 94],
  ["import-response-settlement", 95],
]);

export function manifestFailureExitCode(substage) {
  const exitCode = manifestFailureExitCodes.get(substage);
  if (exitCode === undefined) throw new TypeError("invalid manifest failure substage");
  return exitCode;
}

export function initialIngressRetryDelayMs(deadline, retryIntervalMs, now = Date.now()) {
  if (![deadline, retryIntervalMs, now].every(Number.isFinite) || retryIntervalMs <= 0) {
    throw new TypeError("initial ingress retry budget must be finite and positive");
  }
  return deadline - now > retryIntervalMs ? retryIntervalMs : 0;
}

const uploadFailureSubstages = new Set([
  "start-response",
  "start-transport",
  "image-terminal",
  "image-retry",
  "image-transport",
  "handoff-timeout",
  "handoff-terminal",
  "response-contract",
]);
const uploadDurableFailureCategories = new Set([
  "segmentation-canceled",
  "segmentation-timeout",
  "segmentation-failed",
  "provider-authentication",
  "provider-failed",
  "admission-failed",
  "upload-storage-failed",
  "segmentation-output-failed",
  "quota-resize-failed",
  "lease-renewal-failed",
  "image-commit-failed",
  "ocr-run-commit-failed",
  "annotation-commit-failed",
  "transcription-enqueue-failed",
  "item-reload-failed",
  "batch-commit-failed",
  "unknown",
]);
const retryableUploadConnectResponseKinds = new Map([
  ["aborted", "connect-aborted"],
  ["already_exists", "connect-already-exists"],
  ["deadline_exceeded", "connect-deadline-exceeded"],
  ["internal", "connect-internal"],
  ["resource_exhausted", "connect-resource-exhausted"],
  ["unavailable", "connect-unavailable"],
  ["unknown", "connect-unknown"],
]);
const retryableUploadHTTPResponseKinds = new Map([
  [408, "http-408"],
  [409, "http-409"],
  [425, "http-425"],
  [429, "http-429"],
  [500, "http-500"],
  [502, "http-502"],
  [503, "http-503"],
  [504, "http-504"],
]);
const retryableUploadResponseKinds = new Set([
  ...retryableUploadConnectResponseKinds.values(),
  ...retryableUploadHTTPResponseKinds.values(),
]);
const genericProviderFailures = new Set([
  "provider request canceled",
  "provider request timed out",
  "provider request failed",
  "provider request was rejected",
  "provider response exceeded configured limit",
  "provider request was rate limited",
  "provider returned an invalid response",
]);

export function remainingUploadSequenceTimeoutMs(mainScenarioDeadline, now = Date.now()) {
  if (!Number.isFinite(mainScenarioDeadline) || !Number.isFinite(now)) {
    throw new TypeError("upload sequence deadline must be finite");
  }
  const remaining = Math.floor(mainScenarioDeadline - now);
  if (remaining <= 0) throw new Error("upload sequence deadline exceeded");
  return remaining;
}

export function classifyUploadFailure({
  handoffTimedOut = false,
  terminal = false,
  attemptKind = "",
  attemptSucceeded = false,
  attemptRetryable = false,
} = {}) {
  if (handoffTimedOut) {
    return terminal ? "response-contract" : "handoff-timeout";
  }
  if (!terminal) return "response-contract";
  if (attemptKind === "transport") return "image-transport";
  if (attemptKind === "response" && !attemptSucceeded) {
    return attemptRetryable ? "image-retry" : "image-terminal";
  }
  if (attemptKind === "response" && attemptSucceeded) return "handoff-terminal";
  return "response-contract";
}

export function uploadFailureMarker(substage) {
  if (!uploadFailureSubstages.has(substage)) {
    throw new TypeError("invalid upload failure substage");
  }
  return `browser readiness upload substage: ${substage}`;
}

export function classifyDurableUploadFailure(statusText, fixtureName) {
  if (typeof statusText !== "string" || typeof fixtureName !== "string" || fixtureName === "") {
    return "unknown";
  }
  const prefix = `Upload failed: upload failed for ${fixtureName}: `;
  if (!statusText.startsWith(prefix)) return "unknown";
  const failure = statusText.slice(prefix.length);
  switch (failure) {
    case "segmentation provider request canceled":
      return "segmentation-canceled";
    case "segmentation provider request timed out":
      return "segmentation-timeout";
    case "segmentation provider request failed":
      return "segmentation-failed";
    case "provider authentication failed":
      return "provider-authentication";
    case "admission failed":
      return "admission-failed";
    case "upload storage failed":
      return "upload-storage-failed";
    case "segmentation output failed":
      return "segmentation-output-failed";
    case "quota resize failed":
      return "quota-resize-failed";
    case "lease renewal failed":
      return "lease-renewal-failed";
    case "image commit failed":
      return "image-commit-failed";
    case "ocr run commit failed":
      return "ocr-run-commit-failed";
    case "annotation commit failed":
      return "annotation-commit-failed";
    case "transcription enqueue failed":
      return "transcription-enqueue-failed";
    case "item reload failed":
      return "item-reload-failed";
    case "batch commit failed":
      return "batch-commit-failed";
    default:
      if (/^provider request failed with HTTP status (?:401|403)$/.test(failure)) {
        return "provider-authentication";
      }
      if (
        genericProviderFailures.has(failure)
        || /^provider request failed with HTTP status [3-5][0-9]{2}$/.test(failure)
      ) return "provider-failed";
      return "unknown";
  }
}

export function uploadDurableFailureMarker(category) {
  if (!uploadDurableFailureCategories.has(category)) {
    throw new TypeError("invalid upload durable failure category");
  }
  return `browser readiness upload durable failure: ${category}`;
}

export function classifyRetryableUploadResponse({ connectCode, snapshotValid = false, status = 0 } = {}) {
  if (snapshotValid) {
    if (typeof connectCode !== "string") return undefined;
    return retryableUploadConnectResponseKinds.get(connectCode);
  }
  return retryableUploadHTTPResponseKinds.get(status);
}

export function uploadRetryableResponseMarker(kind) {
  if (!retryableUploadResponseKinds.has(kind)) {
    throw new TypeError("invalid upload retryable response kind");
  }
  return `browser readiness upload retryable response: ${kind}`;
}
