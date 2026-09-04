import assert from "node:assert/strict";
import test from "node:test";

import {
  classifyDurableUploadFailure,
  classifyRetryableUploadResponse,
  classifyUploadFailure,
  cleanupCommitHorizonMs,
  initialIngressRetryDelayMs,
  manifestFailureExitCode,
  remainingUploadSequenceTimeoutMs,
  uploadDurableFailureMarker,
  uploadFailureMarker,
  uploadRequestTimeoutMs,
  uploadRetryableResponseMarker,
} from "./deployed-readiness-budget.mjs";

test("manifest substages have distinct task-status exit codes", () => {
  const substages = [
    "library-navigation", "import-form", "import-request", "import-contract",
    "editor-navigation", "editor-mount", "first-canvas", "first-image",
    "first-annotations", "first-publication", "second-image", "second-canvas",
    "second-annotations", "second-overlay", "second-publication",
  ];
  assert.deepEqual(substages.map(manifestFailureExitCode), [
    75, 76, 77, 78, 79, 80, 81, 82, 83, 84, 85, 86, 87, 88, 89,
  ]);
  assert.throws(() => manifestFailureExitCode("raw-error"), /invalid manifest failure substage/);
});

test("initial ingress retries transient failures only inside the existing deadline", () => {
  assert.equal(initialIngressRetryDelayMs(10_000, 2_000, 7_999), 2_000);
  assert.equal(initialIngressRetryDelayMs(10_000, 2_000, 8_000), 0);
  assert.equal(initialIngressRetryDelayMs(10_000, 2_000, 10_001), 0);
  assert.throws(
    () => initialIngressRetryDelayMs(Number.NaN, 2_000, 1_000),
    /retry budget must be finite and positive/,
  );
  assert.throws(
    () => initialIngressRetryDelayMs(10_000, 0, 1_000),
    /retry budget must be finite and positive/,
  );
});

test("upload sequence uses the remaining scenario deadline without widening request cleanup", () => {
  const scenarioStartedAt = 1_000_000;
  const now = scenarioStartedAt + 420_000;
  const mainScenarioDeadline = scenarioStartedAt + 1_620_000;

  assert.equal(uploadRequestTimeoutMs, 300_000);
  assert.equal(cleanupCommitHorizonMs, uploadRequestTimeoutMs);
  assert.equal(
    remainingUploadSequenceTimeoutMs(mainScenarioDeadline, now),
    1_200_000,
  );
  assert.ok(
    remainingUploadSequenceTimeoutMs(mainScenarioDeadline, now) > uploadRequestTimeoutMs,
  );
  assert.throws(
    () => remainingUploadSequenceTimeoutMs(mainScenarioDeadline, mainScenarioDeadline),
    /upload sequence deadline exceeded/,
  );
});

test("durable upload classifier accepts only exact safe status messages", () => {
  const fixtureName = "browser-readiness-00000000-0000-4000-8000-000000000000.png";
  const status = (failure) => `Upload failed: upload failed for ${fixtureName}: ${failure}`;
  const cases = [
    ["segmentation provider request canceled", "segmentation-canceled"],
    ["segmentation provider request timed out", "segmentation-timeout"],
    ["segmentation provider request failed", "segmentation-failed"],
    ["provider authentication failed", "provider-authentication"],
    ["provider request failed with HTTP status 403", "provider-authentication"],
    ["provider request failed with HTTP status 503", "provider-failed"],
    ["provider request was rate limited", "provider-failed"],
    ["admission failed", "admission-failed"],
    ["upload storage failed", "upload-storage-failed"],
    ["segmentation output failed", "segmentation-output-failed"],
    ["quota resize failed", "quota-resize-failed"],
    ["lease renewal failed", "lease-renewal-failed"],
    ["image commit failed", "image-commit-failed"],
    ["ocr run commit failed", "ocr-run-commit-failed"],
    ["annotation commit failed", "annotation-commit-failed"],
    ["transcription enqueue failed", "transcription-enqueue-failed"],
    ["item reload failed", "item-reload-failed"],
    ["batch commit failed", "batch-commit-failed"],
    ["canonical commit failed", "unknown"],
    ["processing failed", "unknown"],
    ["private upstream response", "unknown"],
  ];

  for (const [failure, expected] of cases) {
    assert.equal(classifyDurableUploadFailure(status(failure), fixtureName), expected);
    assert.equal(
      uploadDurableFailureMarker(expected),
      `browser readiness upload durable failure: ${expected}`,
    );
  }
  assert.equal(
    classifyDurableUploadFailure(status("quota resize failed"), "other.png"),
    "unknown",
  );
  assert.throws(
    () => uploadDurableFailureMarker("private upstream response"),
    /invalid upload durable failure category/,
  );
});

test("retryable upload response classifier uses exact Connect codes before fixed HTTP status", () => {
  const connectCases = [
    ["aborted", "connect-aborted"],
    ["already_exists", "connect-already-exists"],
    ["deadline_exceeded", "connect-deadline-exceeded"],
    ["internal", "connect-internal"],
    ["resource_exhausted", "connect-resource-exhausted"],
    ["unavailable", "connect-unavailable"],
    ["unknown", "connect-unknown"],
  ];
  const httpCases = [408, 409, 425, 429, 500, 502, 503, 504];

  for (const [connectCode, expected] of connectCases) {
    assert.equal(
      classifyRetryableUploadResponse({ connectCode, snapshotValid: true, status: 503 }),
      expected,
    );
    assert.equal(
      uploadRetryableResponseMarker(expected),
      `browser readiness upload retryable response: ${expected}`,
    );
  }
  for (const status of httpCases) {
    const expected = `http-${status}`;
    assert.equal(classifyRetryableUploadResponse({ status }), expected);
    assert.equal(
      uploadRetryableResponseMarker(expected),
      `browser readiness upload retryable response: ${expected}`,
    );
  }

  assert.equal(
    classifyRetryableUploadResponse({ connectCode: "internal", snapshotValid: true, status: 503 }),
    "connect-internal",
  );
  assert.equal(
    classifyRetryableUploadResponse({ connectCode: "permission_denied", snapshotValid: true, status: 503 }),
    undefined,
  );
  assert.equal(
    classifyRetryableUploadResponse({ snapshotValid: true, status: 500 }),
    undefined,
  );
  assert.equal(
    classifyRetryableUploadResponse({ connectCode: 13, snapshotValid: true, status: 500 }),
    undefined,
  );
  assert.equal(
    classifyRetryableUploadResponse({ connectCode: "INTERNAL", snapshotValid: true, status: 500 }),
    undefined,
  );
  assert.equal(classifyRetryableUploadResponse({ status: 418 }), undefined);
  assert.equal(
    classifyRetryableUploadResponse({ status: 502, message: "private response body" }),
    "http-502",
  );
  assert.throws(
    () => uploadRetryableResponseMarker("gateway-private"),
    /invalid upload retryable response kind/,
  );
});

test("upload failure classifier emits only fixed request and handoff substages", () => {
  const cases = [
    [{ handoffTimedOut: true }, "handoff-timeout"],
    [{ handoffTimedOut: true, terminal: true }, "response-contract"],
    [{ terminal: true, attemptKind: "transport" }, "image-transport"],
    [{ terminal: true, attemptKind: "response", attemptRetryable: true }, "image-retry"],
    [{ terminal: true, attemptKind: "response" }, "image-terminal"],
    [{ terminal: true, attemptKind: "response", attemptSucceeded: true }, "handoff-terminal"],
    [{ terminal: true }, "response-contract"],
    [{}, "response-contract"],
  ];

  for (const [input, expected] of cases) {
    assert.equal(classifyUploadFailure(input), expected);
    assert.equal(
      uploadFailureMarker(expected),
      `browser readiness upload substage: ${expected}`,
    );
  }
  assert.throws(() => uploadFailureMarker("raw-error"), /invalid upload failure substage/);
});
