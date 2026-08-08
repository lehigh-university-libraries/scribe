#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

fail() {
  echo "browser readiness contract failed: $*" >&2
  exit 1
}

require_fixed() {
  local pattern="$1" file="$2"
  rg -Fq -- "$pattern" "$file" || fail "$file is missing: $pattern"
}

require_pattern() {
  local pattern="$1" file="$2"
  rg -q -- "$pattern" "$file" || fail "$file is missing required pattern: $pattern"
}

forbid_pattern() {
  local pattern="$1" file="$2"
  if rg -qi -- "$pattern" "$file"; then
    fail "$file contains forbidden pattern: $pattern"
  fi
}

assert_before() {
  local file="$1" before_pattern="$2" after_pattern="$3" before_line after_line
  before_line="$(rg -n -m 1 -- "$before_pattern" "$file" | cut -d: -f1)"
  after_line="$(rg -n -m 1 -- "$after_pattern" "$file" | cut -d: -f1)"
  [ -n "$before_line" ] && [ -n "$after_line" ] && [ "$before_line" -lt "$after_line" ] ||
    fail "$file must place $before_pattern before $after_pattern"
}

assert_text_before() {
  local text="$1" before="$2" after="$3" before_line after_line
  before_line="$(rg -n -F -m 1 -- "$before" <<<"$text" | cut -d: -f1 || true)"
  after_line="$(rg -n -F -m 1 -- "$after" <<<"$text" | cut -d: -f1 || true)"
  [ -n "$before_line" ] && [ -n "$after_line" ] && [ "$before_line" -lt "$after_line" ] ||
    fail "text must place $before before $after"
}

playwright_version="$(jq -r '.devDependencies["@playwright/test"]' web/package.json)"
playwright_image="$(sed -nE 's/^PLAYWRIGHT_TEST_IMAGE="\$\{PLAYWRIGHT_TEST_IMAGE:-([^}]*)\}"$/\1/p' ci/test-browser.sh)"
[ -n "$playwright_version" ] && [ -n "$playwright_image" ] || fail "could not resolve the locked Playwright version"
case "$playwright_image" in
  "mcr.microsoft.com/playwright:v${playwright_version}-noble@sha256:"*) ;;
  *) fail "the local browser image does not match web's locked Playwright version" ;;
esac
[ "$(rg -c -F "FROM $playwright_image" Dockerfile.browser-readiness)" -eq 2 ] ||
  fail "the protected runner must pin both image stages to the reviewed Playwright digest"
require_fixed "USER pwuser" Dockerfile.browser-readiness
require_fixed 'ENTRYPOINT ["/app/deployed-readiness-entrypoint.sh"]' Dockerfile.browser-readiness
require_fixed 'COPY web/package.json web/package-lock.json ./' Dockerfile.browser-readiness
require_fixed 'RUN npm ci --ignore-scripts --prefer-offline --no-audit' Dockerfile.browser-readiness
require_fixed 'COPY --chown=pwuser:pwuser web/e2e/deployed-readiness.mjs ./deployed-readiness.mjs' Dockerfile.browser-readiness
require_fixed 'COPY --chown=pwuser:pwuser web/e2e/deployed-readiness-routing.mjs ./deployed-readiness-routing.mjs' Dockerfile.browser-readiness
require_fixed 'COPY --chown=pwuser:pwuser --chmod=0555 web/e2e/deployed-readiness-entrypoint.sh ./deployed-readiness-entrypoint.sh' Dockerfile.browser-readiness
forbid_pattern 'readiness-smoke\.png\.base64' Dockerfile.browser-readiness
require_fixed 'gha-creds-*.json' .dockerignore
forbid_pattern 'latest|curl|wget|apt-get' Dockerfile.browser-readiness
[ "$(rg -c -F 'COPY --chown=pwuser:pwuser web/e2e/deployed-readiness.mjs ./deployed-readiness.mjs' Dockerfile.browser-readiness)" -eq 1 ] ||
  fail "the protected Dockerfile must copy the PR-head script exactly once"
[ "$(rg -c -F 'COPY --chown=pwuser:pwuser web/e2e/deployed-readiness-routing.mjs ./deployed-readiness-routing.mjs' Dockerfile.browser-readiness)" -eq 1 ] ||
  fail "the protected Dockerfile must copy the IPv6 routing helper exactly once"
assert_before Dockerfile.browser-readiness '^RUN npm ci --ignore-scripts' '^COPY --chown=pwuser:pwuser web/e2e/deployed-readiness\.mjs'
final_image_stage="$(awk '/^FROM / { stage += 1 } stage == 2 { print }' Dockerfile.browser-readiness)"
if rg -q '^RUN[[:space:]]' <<<"$final_image_stage"; then
  fail "the final browser image stage must never execute the PR-head script during its credentialed build"
fi

bash ci/prepare-browser-readiness-source_test.sh
forbid_pattern 'readiness-smoke\.png\.base64' ci/prepare-browser-readiness-source.sh

node --check web/e2e/deployed-readiness.mjs
node --test web/e2e/deployed-readiness-routing_test.mjs
require_fixed 'configureCanonicalIPv6Routing(baseURL.hostname)' web/e2e/deployed-readiness.mjs
require_fixed 'args: [chromiumIPv6Argument]' web/e2e/deployed-readiness.mjs
assert_before web/e2e/deployed-readiness.mjs 'productionState = await consumeProductionStorageState\(baseURL\)' 'configureCanonicalIPv6Routing\(baseURL\.hostname\)'
assert_before web/e2e/deployed-readiness.mjs 'category = "network"' 'configureCanonicalIPv6Routing\(baseURL\.hostname\)'
assert_before web/e2e/deployed-readiness.mjs 'configureCanonicalIPv6Routing\(baseURL\.hostname\)' 'chromium\.launch'
assert_before web/e2e/deployed-readiness.mjs 'configureCanonicalIPv6Routing\(baseURL\.hostname\)' 'browser\.newContext\(contextOptions\)'
require_fixed 'options.setResultOrder ?? setDefaultResultOrder' web/e2e/deployed-readiness-routing.mjs
require_fixed 'setResultOrder("ipv6first")' web/e2e/deployed-readiness-routing.mjs
require_fixed 'options.setAutoSelectFamily ?? setDefaultAutoSelectFamily' web/e2e/deployed-readiness-routing.mjs
require_fixed 'setAutoSelectFamily(false)' web/e2e/deployed-readiness-routing.mjs
require_fixed 'Object.freeze(["8.8.8.8", "8.8.4.4"])' web/e2e/deployed-readiness-routing.mjs
require_fixed 'resolver.setServers([...publicDNSResolvers])' web/e2e/deployed-readiness-routing.mjs
require_fixed 'const defaultResolutionTimeoutMs = 120_000;' web/e2e/deployed-readiness-routing.mjs
require_fixed 'const defaultResolutionAttemptTimeoutMs = 10_000;' web/e2e/deployed-readiness-routing.mjs
require_fixed 'const defaultResolutionRetryIntervalMs = 2_000;' web/e2e/deployed-readiness-routing.mjs
require_fixed 'if (!address) throw new Error("canonical IPv6 resolution timed out");' web/e2e/deployed-readiness-routing.mjs
require_fixed 'options.maxAttempts ?? 5' web/src/api/items.ts
require_fixed 'uses the full durable five-attempt budget by default' web/src/api/items.test.ts
require_fixed 'import { createHash, randomUUID } from "node:crypto";' web/e2e/deployed-readiness.mjs
require_fixed 'import { readFile, unlink } from "node:fs/promises";' web/e2e/deployed-readiness.mjs
require_fixed 'const readinessSmokeFixtureSHA256 = "e3f3bb2b5ade3c15af262a76ad58b720e7eb3b3d079802df04f1dd50be917b2d";' web/e2e/deployed-readiness.mjs
require_fixed 'const fixture = Buffer.from(readinessSmokeFixtureBase64, "base64");' web/e2e/deployed-readiness.mjs
require_fixed 'createHash("sha256").update(fixture).digest("hex")' web/e2e/deployed-readiness.mjs
require_fixed 'const fixture = exactReadinessSmokeFixture();' web/e2e/deployed-readiness.mjs
require_pattern '^const readinessSmokeFixtureBase64 = "[A-Za-z0-9+/=]+";$' web/e2e/deployed-readiness.mjs
forbid_pattern 'fixturePath|/app/readiness-smoke\.png\.base64' web/e2e/deployed-readiness.mjs
for forbidden in 'screenshot' 'tracing' 'recordVideo'; do
  forbid_pattern "$forbidden" web/e2e/deployed-readiness.mjs
done
require_fixed 'const productionStorageStatePath = "/tmp/scribe-browser-session-state.json";' web/e2e/deployed-readiness.mjs
require_fixed 'mode === "production"' web/e2e/deployed-readiness.mjs
require_fixed 'mode === "preview"' web/e2e/deployed-readiness.mjs
require_fixed 'const expectedVersion = String(process.env.SCRIBE_BROWSER_EXPECTED_SECRET_VERSION ?? "").trim();' web/e2e/deployed-readiness.mjs
require_fixed 'const expectedDigest = String(process.env.SCRIBE_BROWSER_EXPECTED_STORAGE_STATE_SHA256 ?? "").trim();' web/e2e/deployed-readiness.mjs
require_fixed '!/^([2-9]|[1-9][0-9]{1,19})$/.test(expectedVersion)' web/e2e/deployed-readiness.mjs
require_fixed 'encoded = await readFile(productionStorageStatePath);' web/e2e/deployed-readiness.mjs
[ "$(rg -c -F 'readFile(productionStorageStatePath)' web/e2e/deployed-readiness.mjs)" -eq 1 ] ||
  fail "production storage state must be read into memory exactly once"
require_fixed 'const digestMatches = createHash("sha256").update(encoded).digest("hex") === expectedDigest;' web/e2e/deployed-readiness.mjs
require_fixed 'if (!digestMatches)' web/e2e/deployed-readiness.mjs
require_fixed 'cookie.name !== "scribe_session"' web/e2e/deployed-readiness.mjs
require_fixed 'cookie.domain !== targetURL.hostname' web/e2e/deployed-readiness.mjs
require_fixed 'cookie.expires * 1_000 < minimumRequiredExpiry' web/e2e/deployed-readiness.mjs
require_fixed 'let productionSessionCookie;' web/e2e/deployed-readiness.mjs
require_fixed 'productionSessionCookie = { name: cookie.name, value: cookie.value };' web/e2e/deployed-readiness.mjs
require_fixed 'contextOptions.storageState = productionState;' web/e2e/deployed-readiness.mjs
require_fixed 'await unlink(productionStorageStatePath);' web/e2e/deployed-readiness.mjs
require_fixed 'productionStorageStateRemoved = true;' web/e2e/deployed-readiness.mjs
require_fixed 'error?.code !== "ENOENT"' web/e2e/deployed-readiness.mjs
require_fixed 'await requireProductionSession();' web/e2e/deployed-readiness.mjs
for required in \
  'const initialIngressWarmupBudgetMs = 300_000;' \
  'const initialIngressAttemptTimeoutMs = 10_000;' \
  'function initialIngressResponseIsRetryable(responseURL, status, target)' \
  'status === 403 || status === 404' \
  'initialIngressResponseIsRetryable(new URL("/?retry=1", target), 403, target)' \
  'initialIngressResponseIsRetryable(new URL("/#retry", target), 403, target)' \
  'status === 403) recordBrowserFault("initial-ingress-forbidden")' \
  'status === 404) recordBrowserFault("initial-ingress-not-found")' \
  'assertInitialIngressRetryClassifier();' \
  'async function warmInitialBrowserIngress()' \
  'browserContext.request.get(baseURL.href, {' \
  'failOnStatusCode: false,' \
  'maxRedirects: 0,' \
  'maxRetries: 0,' \
  'await response.dispose();' \
  'await warmInitialBrowserIngress();'; do
  require_fixed "$required" web/e2e/deployed-readiness.mjs
done
assert_before web/e2e/deployed-readiness.mjs 'await warmInitialBrowserIngress\(\)' 'await requireProductionSession\(\)'
assert_before web/e2e/deployed-readiness.mjs 'await warmInitialBrowserIngress\(\)' 'browserContext\.newPage\(\)'
require_fixed 'payload?.authenticated !== true' web/e2e/deployed-readiness.mjs
require_fixed 'payload?.authType !== "session"' web/e2e/deployed-readiness.mjs
require_fixed 'positiveID(payload?.user?.id) !== "1"' web/e2e/deployed-readiness.mjs
require_fixed 'positiveID(payload?.user?.defaultWorkspaceId) !== "1"' web/e2e/deployed-readiness.mjs
require_fixed 'payload?.user?.isAdmin !== false' web/e2e/deployed-readiness.mjs
require_fixed 'positiveID(payload?.workspace?.id) !== "1"' web/e2e/deployed-readiness.mjs
require_fixed 'payload?.workspace?.role !== "admin"' web/e2e/deployed-readiness.mjs
for required in \
  'const productionJanitorWorkspaceID = "1";' \
  'const productionJanitorMaxItemPages = 10;' \
  'const productionJanitorMaxItems = 1_000;' \
  'const productionJanitorMaxItemVerifications = 100;' \
  'const productionJanitorMaxAPIKeys = 1_000;' \
  'const productionJanitorMaxDeletes = 100;' \
  'const readinessUploadNamePattern = /^browser-readiness-[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}\.png$/;' \
  'const readinessAPIKeyNamePattern = /^browser-readiness-[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;' \
  'const readinessManifestReferencePattern = /^browser-readiness-manifest-[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;' \
  'async function reconcileProductionReadinessOrphans()' \
  'browserMode !== "production"' \
  'workspaceID !== productionJanitorWorkspaceID' \
  'summary?.sourceType === "upload" && readinessUploadNamePattern.test(String(summary?.name ?? ""))' \
  'summary?.sourceType === "manifest"' \
  'readinessManifestReferencePattern.test(String(summary?.externalReferenceId ?? ""))' \
  'await getItemForCleanup(summaryID, workspaceID, recoveryDeadline)' \
  'fullItem?.sourceUrl === manifestURL' \
  'readinessAPIKeyNamePattern.test(String(key?.name ?? ""))' \
  'positiveID(key?.workspaceId) !== productionJanitorWorkspaceID' \
  'production janitor delete bound exceeded' \
  'await reconcileExactResources(' \
  'await reconcileProductionReadinessOrphans();'; do
  require_fixed "$required" web/e2e/deployed-readiness.mjs
done
production_janitor="$(sed -n '/^async function findProductionReadinessOrphans/,/^const retryableUploadConnectCodes/p' web/e2e/deployed-readiness.mjs)"
for required in \
  'const fullItem = await getItemForCleanup(summaryID, workspaceID, recoveryDeadline);' \
  'const fullItem = await getItemForCleanup(itemID, workspaceID, recoveryDeadline);' \
  'fullItem?.name !== expectedItem?.name' \
  'fullItem?.sourceType !== expectedItem?.sourceType' \
  'fullItem?.sourceUrl !== expectedItem?.sourceUrl' \
  'fullItem?.externalReferenceId !== expectedItem?.externalReferenceId' \
  'productionReadinessItemKind(fullItem) !== orphan.kind' \
  'await deleteItemDirect(itemID, workspaceID, recoveryDeadline);' \
  'await deleteAPIKeyDirect(keyID, workspaceID, recoveryDeadline);' \
  'const janitorDeadline = Math.min(mainScenarioDeadline, Date.now() + stageTimeoutMs);' \
  'await reconcileExactResources('; do
  rg -Fq "$required" <<<"$production_janitor" ||
    fail "production janitor is missing: $required"
done
production_bootstrap="$(sed -n '/await requireProductionSession();/,/await browserContext.grantPermissions/p' web/e2e/deployed-readiness.mjs)"
for required in \
  'if (browserMode === "production") {' \
  'await reconcileProductionReadinessOrphans();'; do
  rg -Fq "$required" <<<"$production_bootstrap" ||
    fail "production bootstrap is missing: $required"
done
assert_before web/e2e/deployed-readiness.mjs 'await requireProductionSession\(\)' 'await reconcileProductionReadinessOrphans\(\)'
# shellcheck disable=SC2016 # Match a literal JavaScript template expression.
assert_before web/e2e/deployed-readiness.mjs 'await reconcileProductionReadinessOrphans\(\)' 'fixtureName = `browser-readiness-\$\{randomUUID\(\)\}\.png`'
for required in \
  'function assertProductionManifestCleanupClassifier()' \
  'externalReferenceId: "ordinary-library-manifest"' \
  'productionReadinessItemKind(ordinarySameURLManifest) !== ""' \
  'assertProductionManifestCleanupClassifier();'; do
  require_fixed "$required" web/e2e/deployed-readiness.mjs
done
production_revocation="$(sed -n '/^async function productionSessionFetch/,/^const responseJSONSnapshots/p' web/e2e/deployed-readiness.mjs)"
# shellcheck disable=SC2016 # These are literal JavaScript source assertions.
for required in \
  'if (!productionSessionCookie)' \
  'remainingDeadlineTimeMs(' \
  'revocationDeadline,' \
  'const controller = new AbortController();' \
  'const timeout = setTimeout(() => controller.abort(), timeoutMs);' \
  'redirect: "manual"' \
  'Cookie: `${productionSessionCookie.name}=${productionSessionCookie.value}`' \
  '"/scribe.v1.WorkspaceService/ListWorkspaces"' \
  'const rejected = response.status === 401;' \
  'return rejected;' \
  'if (browserMode !== "production" || !productionSessionCookie) return;' \
  'while (Date.now() < revocationDeadline)' \
  '"/logout"' \
  'logoutAccepted && await productionSessionIsRevoked(revocationDeadline)' \
  'productionSessionCookie = undefined;' \
  'const retryDelayMs = Math.min(2_000, revocationDeadline - Date.now());' \
  'throw new Error("production browser session revocation was not verified")'; do
  rg -Fq "$required" <<<"$production_revocation" ||
    fail "production revocation is missing: $required"
done
if rg -Fq 'browserContext' <<<"$production_revocation"; then
  fail "production revocation must remain independent of browserContext"
fi
if rg -Fq '/scribe.v1.AuthService/GetAuthMe' <<<"$production_revocation"; then
  fail "revocation proof must replay the original cookie against a protected endpoint"
fi
production_revocation_proof="$(sed -n '/^async function productionSessionIsRevoked/,/^async function revokeProductionSession/p' web/e2e/deployed-readiness.mjs)"
for required in \
  'const response = await productionSessionFetch(' \
  '"/scribe.v1.WorkspaceService/ListWorkspaces"' \
  'revocationDeadline,' \
  'response.status === 401'; do
  rg -Fq "$required" <<<"$production_revocation_proof" ||
    fail "original-cookie revocation proof is missing: $required"
done
production_logout="$(sed -n '/^async function revokeProductionSession/,/^const responseJSONSnapshots/p' web/e2e/deployed-readiness.mjs)"
for required in \
  'const response = await productionSessionFetch(' \
  '"/logout"' \
  'revocationDeadline,' \
  'await productionSessionIsRevoked(revocationDeadline)'; do
  rg -Fq "$required" <<<"$production_logout" ||
    fail "bounded logout and proof sequence is missing: $required"
done
final_revocation="$(sed -n '/^  if (browserMode === "production" && productionSessionCookie) {/,/^  if (browser) {/p' web/e2e/deployed-readiness.mjs)"
top_level_finalizer="$(sed -n '/^} finally {$/,/^failureCategory =/p' web/e2e/deployed-readiness.mjs)"
for required in \
  'if (browserMode === "production" && productionSessionCookie)' \
  'await revokeProductionSession(sessionRevocationDeadline);'; do
  rg -Fq "$required" <<<"$final_revocation" ||
    fail "top-level finally revocation is missing: $required"
  rg -Fq "$required" <<<"$top_level_finalizer" ||
    fail "top-level finally does not retain revocation: $required"
done
if rg -Fq 'browserContext' <<<"$final_revocation"; then
  fail "top-level finally revocation must not depend on browserContext creation"
fi
require_fixed 'Origin: baseURL.origin' web/e2e/deployed-readiness.mjs
assert_before web/e2e/deployed-readiness.mjs 'readFile\(productionStorageStatePath\)' 'unlink\(productionStorageStatePath\)'
assert_before web/e2e/deployed-readiness.mjs 'unlink\(productionStorageStatePath\)' 'chromium\.launch'
assert_before web/e2e/deployed-readiness.mjs 'unlink\(productionStorageStatePath\)' 'browser\.newContext\(contextOptions\)'
assert_before web/e2e/deployed-readiness.mjs 'productionSessionCookie = \{ name: cookie\.name, value: cookie\.value \}' 'if \(!digestMatches\)'
assert_before web/e2e/deployed-readiness.mjs 'productionSessionCookie = \{ name: cookie\.name, value: cookie\.value \}' 'chromium\.launch'
assert_before web/e2e/deployed-readiness.mjs 'productionState = await consumeProductionStorageState\(baseURL\)' 'browser = await chromium\.launch'
assert_before web/e2e/deployed-readiness.mjs 'browser\.newContext\(contextOptions\)' 'await requireProductionSession\(\)'
assert_before web/e2e/deployed-readiness.mjs 'await cleanupExactUploadItems\(' 'await revokeProductionSession\(sessionRevocationDeadline\)'
assert_before web/e2e/deployed-readiness.mjs 'const logoutAccepted = response\.ok' 'logoutAccepted && await productionSessionIsRevoked\(revocationDeadline\)'
assert_before web/e2e/deployed-readiness.mjs 'logoutAccepted && await productionSessionIsRevoked\(revocationDeadline\)' 'productionSessionCookie = undefined'
[ "$(rg -c -F 'productionSessionCookie = undefined;' web/e2e/deployed-readiness.mjs)" -eq 1 ] ||
  fail "the retained production cookie must be cleared only after revocation proof"
[ "$(rg -c -F 'storageState' web/e2e/deployed-readiness.mjs)" -eq 1 ] ||
  fail "only the production context option may consume Playwright storage state"
# shellcheck disable=SC2016 # These are literal entrypoint source assertions.
for required in \
  'set -euo pipefail' \
  'umask 077' \
  'SCRIBE_BROWSER_MODE:-' \
  'SCRIBE_BROWSER_STORAGE_STATE_JSON' \
  'state_path="/tmp/scribe-browser-session-state.json"' \
  'set -o noclobber' \
  'printf '\''%s'\'' "$SCRIBE_BROWSER_STORAGE_STATE_JSON" >"$state_path"' \
  'unset SCRIBE_BROWSER_STORAGE_STATE_JSON' \
  'exec node /app/deployed-readiness.mjs'; do
  require_fixed "$required" web/e2e/deployed-readiness-entrypoint.sh
done
forbid_pattern 'echo|set -x|printenv|console|storage.*json.*(stdout|stderr)' web/e2e/deployed-readiness-entrypoint.sh
bash ci/deployed-readiness-entrypoint_test.sh
# shellcheck disable=SC2016 # These are literal JavaScript source assertions.
for required in \
  'if (await contextSelect.inputValue() !== "0")' \
  'const durableJob = await loadTranscriptionJob(jobID, workspaceID)' \
  'const durableContextID = positiveID(durableJob?.contextId)' \
  'const durableContext = await loadContext(durableContextID, workspaceID)' \
  '/scribe.v1.ContextService/GetContext' \
  'String(durableContext?.userId ?? "0") !== "0"' \
  'durableContext?.name !== "Tesseract OCR"' \
  'durableContext?.isDefault !== true' \
  'durableContext?.segmentationModel !== "scribe"' \
  'durableContext?.transcriptionProvider !== "tesseract"' \
  'durableContext?.transcriptionModel !== "tesseract"' \
  'globalThis.__scribeReadinessAutomaticTranscription' \
  'overlayReady: false' \
  'streamReady: undefined' \
  'attemptNumber: Number(detail.attemptNumber ?? 0)' \
  'previous?.attemptNumber === sample.attemptNumber' \
  'canvasId: String(detail.canvasId ?? "")' \
  'catchUp: detail.catchUp === true' \
  'data-scribe-transcription-active="true"' \
  'automatic transcription omitted visible line-by-line wand progress' \
  'enrichAnnotationRequestCount !== 0' \
  'automatic transcription used the foreground enrichment path' \
  'enrichAnnotationRequestCount < 1' \
  'const editorAssetPattern = /\/assets\/editor-' \
  'page.route(editorAssetPattern, delayEditorAssetUntilJobCompletes)' \
  'assetURL.origin !== baseURL.origin' \
  'editorRoute.origin !== baseURL.origin' \
  'editorAssetDelayObserved = true' \
  'waitForTerminalTranscriptionJob' \
  'pollDelayMs = Math.min(pollDelayMs * 2, 2_000)' \
  'editorAssetDelayReachedCompletion = transcriptionJobCompleted(delayedJob)' \
  'editorAssetDelayFailed' \
  '|| !editorAssetDelayObserved' \
  '|| !editorAssetDelayReachedCompletion' \
  'page.unroute(editorAssetPattern, delayEditorAssetUntilJobCompletes)' \
  'timeout: transcriptionTimeoutMs + stageTimeoutMs' \
  'exactCompletedAttemptResultRevision(completedDurableJob, jobID)' \
  'positiveID(attempt?.jobId) === jobID' \
  'positiveID(attempt?.attemptNumber)' \
  'transcriptionJobAttemptCompleted(attempt)' \
  'positiveID(attempt?.resultRevision)' \
  'completedAttempts.length === 1' \
  'const revision = positiveID(payload?.revision)' \
  'const canvasURI = String(payload?.canvasUri ?? "").trim()' \
  'responseItemImageID !== itemImageID' \
  'canonicalSnapshot.revision !== completedResultRevision' \
  'canonicalSnapshot.itemImageID !== itemImageID' \
  'const wandVisualProofGraceMs = 5_000;' \
  'canvasID: canonicalSnapshot.canvasURI' \
  'attemptNumber: Number(completedDurableJob.attemptCount)' \
  'lineIDs: canonicalLineIDs' \
  'sample.canvasId === expected.canvasID' \
  'sample.attemptNumber === expected.attemptNumber' \
  'sample.done <= 2' \
  'sample.total === 2' \
  'sample.catchUp' \
  'sample.annotationId === expected.lineIDs[sample.done - 1]' \
  'sample.done < previous' \
  'seen.size === 2 && seen.has(1) && seen.has(2)' \
  'badge.jobId === expected.jobID && badge.visible' \
  'badge.line <= 2' \
  'badge.total === 2' \
  'badge.annotationId === expected.lineIDs[badge.line - 1]' \
  'badge.line >= badges[index - 1].line' \
  'first.line === 1' \
  'second.line === 2' \
  'let pendingFrame = 0;' \
  'new MutationObserver(scheduleRecord).observe(document.documentElement, {' \
  'pendingFrame = requestAnimationFrame(() => {' \
  'first.annotationId === expected.lineIDs[0]' \
  'second.annotationId === expected.lineIDs[1]' \
  'status.startsWith("The completed transcription could not")' \
  'Date.now() < expected.visualDeadline' \
  'visualDeadline: Date.now() + wandVisualProofGraceMs' \
  '/scribe.v1.TranscriptionService/CreateTranscriptionJob' \
  'globalThis.__scribeReadinessAutomaticTranscription?.overlayReady === true' \
  'document.addEventListener("scribe:transcription-stream-ready"' \
  'streamReady?.itemImageId' \
  'streamReady?.canvasId' \
  'streamReady?.windowId' \
  'evidence.badges.length = 0' \
  'const liveEventStreamReady = page.waitForResponse(' \
  'const liveEditorPath = `/editor?itemId=${encodeURIComponent(createdItemID)}&itemImageId=${itemImageID}&workspace_id=${workspaceID}`' \
  'response.request().headers()["referer"] === liveEditorURL.href' \
  'url.searchParams.get("item_image_id") === itemImageID' \
  'await liveEventStreamReady' \
  'if (liveJobID === jobID)' \
  'sample.attemptNumber === 1' \
  'sample.catchUp === false' \
  'in-flight automatic transcription omitted live wand progress' \
  'const previousLineTokenCount = await lineTokenInputs.count()' \
  'Number(liveCompletedJob?.attemptCount ?? -1) !== 1' \
  'liveCanonicalSnapshot.revision !== liveCompletedRevision' \
  'liveCanonicalSnapshot.itemImageID !== itemImageID' \
  'liveCanonicalSnapshot.canvasURI !== canonicalSnapshot.canvasURI' \
  'annotationID !== canonicalLineIDs[index]' \
  'BigInt(liveCompletedRevision) <= BigInt(completedResultRevision)' \
  'Number(completedDurableJob?.failedSegments ?? 0) !== 0' \
  'Number(completedDurableJob?.attemptCount ?? -1) !== 1' \
  'Number(completedDurableJob?.completedSegments ?? -1) !== 2' \
  'Number(completedDurableJob?.totalSegments ?? -1) !== 2' \
  'canonicalLines.length !== 2' \
  'new Set(canonicalLineIDs).size !== 2' \
  'canonicalLines.every(annotationHasText)' \
  'fixtureName = `browser-readiness-${randomUUID()}.png`' \
  'page.on("request"' \
  'page.on("requestfinished"' \
  'const responseJSONSnapshots = new WeakMap();' \
  'const navigationResponseJSONSnapshots = new WeakMap();' \
  'const navigationResponseJSONPaths = new Set(["/scribe.v1.ItemService/StartUploadBatch", "/scribe.v1.ItemService/UploadItemImage", "/scribe.v1.ItemService/ImportManifest"]);' \
  'const maxNavigationResponseJSONBytes = 1024 * 1024;' \
  'const snapshot = responseJSONSnapshots.get(response);' \
  'contentType = String(response.headers()["content-type"] ?? "").trim();' \
  'declaredLengthHeader = String(response.headers()["content-length"] ?? "").trim();' \
  '!/^application\/json(?:\s*;|$)/iu.test(contentType)' \
  '!Number.isSafeInteger(declaredLength)' \
  'declaredLength > maxNavigationResponseJSONBytes' \
  'body = await response.body();' \
  'body.byteLength === 0' \
  'body.byteLength > maxNavigationResponseJSONBytes' \
  'JSON.parse(body.toString("utf8"))' \
  'const navigationResponseJSONRoutePattern = /\/scribe\.v1\.ItemService\/(?:StartUploadBatch|UploadItemImage|ImportManifest)$/;' \
  'const upstreamResponse = await route.fetch(fetchOptions);' \
  'maxRedirects: 0' \
  'maxRetries: 0' \
  'timeout: uploadTimeoutMs' \
  'const snapshot = await snapshotNavigationResponseJSON(upstreamResponse);' \
  'navigationResponseJSONSnapshots.set(request, Promise.resolve(snapshot));' \
  'await route.fulfill({ response: upstreamResponse });' \
  'navigationResponseJSONSnapshots.get(response.request())' \
  'navigationResponseJSONPaths.has(responseURL.pathname)' \
  'responseJSON(outcome.response, "invalid retryable upload response")' \
  'uploadImageAttempts.length < 1 || uploadImageAttempts.length > 5' \
  'uploadImageAttempts.slice(0, -1)' \
  'if (!await uploadAttemptIsRetryable(attempt))' \
  'finalAttempt?.outcome?.kind !== "response"' \
  'if (await uploadOutcome.jsonValue() !== "handoff")' \
  'const finalUploadImageResponse = await requireUploadAttemptEvidence()' \
  'Number(liveCompletedJob?.failedSegments ?? 0) !== 0' \
  'Batch transcription complete. Updated text is now available in the editor.' \
  '/scribe.v1.AnnotationService/GetAnnotationPage' \
  'payload?.annotationPageJson' \
  '/presentation/v3/item-image-${itemImageID}/canvas/page-1/annotations' \
  'assertTextualAnnotationPage(annotationPage)' \
  'assertTextualAnnotationPage(publishedAnnotationPage)' \
  'Document retranscribed. Save to persist this draft.' \
  'name: "Draw New Line", exact: true' \
  'Add a line at the viewport center and focus its keyboard resize handle' \
  'Draft line created.' \
  'async function currentEditorSelectedAnnotationID()' \
  'async function selectedEditorAnnotationIDAtCount(expectedCount, previousAnnotationID)' \
  'async function waitForEditorSelection(annotationCount, annotationID)' \
  'const selectedAnnotationIDBeforeCenteredLine = await currentEditorSelectedAnnotationID()' \
  'const centeredLineAnnotationID = await selectedEditorAnnotationIDAtCount(' \
  'name: "Undo", exact: true' \
  'name: "Redo", exact: true' \
  'await waitForEditorSelection(initialDraftCount + 1, centeredLineAnnotationID)' \
  'await editorDelete.click()' \
  'const lineTokenInputs = page.getByRole("textbox", { name: /^Edit line token [1-9][0-9]*$/ })' \
  'while (await lineTokenInputs.count() > 1)' \
  'await lineTokenInputs.last().fill("")' \
  'name: "Split Words", exact: true' \
  'name: "Edit word gamma", exact: true' \
  'focusedWordAnnotationId: String(event.detail?.focusedWordAnnotationId ?? "")' \
  'Boolean(state?.focusedWordAnnotationId)' \
  'Add a word annotation beside the selection' \
  'name: "Join Words", exact: true' \
  'name: "Choose words to join", exact: true' \
  'statusMessage: String(event.detail?.statusMessage ?? "")' \
  'wordAnnotationIds: Array.isArray(annotationPage?.items)' \
  'async function waitForEditorAnnotationState(expected)' \
  'const beforeJoinWordAnnotationIds = await currentEditorWordAnnotationIds()' \
  'annotationCount: beforeJoinWordsCount, statusMessage: "Words joined."' \
  'wordAnnotationIds: beforeJoinWordAnnotationIds' \
  'getByText("beta gamma epsilon", { exact: true })' \
  '/^Line [0-9]+: (?:browser readiness alpha|beta gamma epsilon)$/' \
  'name: "Choose a split boundary", exact: true' \
  'name: "Choose lines to join", exact: true' \
  '/scribe.v1.AnnotationService/SplitLineIntoWords' \
  '/scribe.v1.AnnotationService/JoinWordsIntoLine' \
  '/scribe.v1.AnnotationService/SplitLineIntoTwoLines' \
  '/scribe.v1.AnnotationService/JoinLines' \
  '/scribe.v1.AnnotationService/SaveAnnotationPage' \
  'async function currentEditorStructuralSnapshot()' \
  'const expectedSavedStructure = await currentEditorStructuralSnapshot()' \
  'stableJSONValue' \
  'annotationTargetGeometry' \
  'assertSavedStructuralPage(savedAnnotationSnapshot.page, expectedSavedStructure)' \
  'assertSavedStructuralPage(publishedAnnotationPage, expectedSavedStructure)' \
  'Saved page.' \
  'Edits published.' \
  'Publish edits' \
  'width: 360, height: 800' \
  'width: 667, height: 375, minimumImageHeight: 60' \
  'width: 768, height: 1024' \
  'width: 1440, height: 900' \
  'const responsiveViewports = [' \
  'for (const viewport of responsiveViewports)' \
  'await page.setViewportSize({ width: viewport.width, height: viewport.height })' \
  'await navigate(responsiveEditorPath)' \
  'const expectedPaneHeight = bottomPaneHeightForViewport({' \
  'Math.abs(geometry.companionHeight - expectedPaneHeight) <= 1' \
  'geometry.primaryActionCount === 14' \
  'geometry.primaryActionsVisible' \
  'geometry.osdImageHeight >= minimumImageHeight' \
  'geometry.panelScrollTop === 0' \
  'responsiveCanonicalAfter.revision !== responsiveCanonicalSnapshot.revision' \
  'JSON.stringify(responsiveCanonicalAfter.page)' \
  'data-scribe-action-panel="true"' \
  'assertItemDeletePresentation("#shell-content", createdItemID, fixtureName)' \
  'assertItemDeletePresentation("#shell-sidebar", createdItemID, fixtureName)' \
  'await deleteItemThroughLibrary("#shell-content", createdItemID, fixtureName)' \
  'await deleteItemThroughLibrary("#shell-sidebar", createdManifestItemID, createdManifestItemName)' \
  'dialog.type() === "confirm"' \
  'dialog.message() === expectedItemDeleteDialog.message' \
  'recordBrowserFault("token")' \
  'button.getAttribute("aria-label")' \
  'button.textContent?.trim() === "Delete"' \
  'button.parentElement?.lastElementChild === button' \
  'svg[aria-hidden="true"]' \
  'Copy workspace token' \
  'Copy token' \
  'navigator.clipboard.writeText("")' \
  'page.on("response"' \
  'page.on("requestfailed"' \
  'page.on("console"' \
  'page.on("dialog"' \
  'candidate.protocol === "https:" && candidate.origin === target.origin' \
  'isTargetHTTPSURL(new URL("https://readiness.invalid/healthz"), target)' \
  'isTargetHTTPSURL(new URL("https://other.invalid/healthz"), target)' \
  'isTargetHTTPSURL(new URL("blob:https://readiness.invalid/private-manifest"), target)' \
  'https://preserve.lehigh.edu/node/38817/book-manifest' \
  'input[name="library-manifest-mode"][value="import"]' \
  'const manifestIdentity = newReadinessManifestIdentity();' \
  'delete headers["content-length"];' \
  'fetchOptions.postData = exactManifestImportPostData(request);' \
  'externalReferenceId: manifestExternalReferenceID' \
  'idempotencyKey: manifestIdempotencyKey' \
  'manifestRequestIdentityInjected' \
  'manifestItem?.sourceType !== "manifest"' \
  'manifestItem?.sourceUrl !== manifestURL' \
  'manifestItem?.externalReferenceId !== manifestExternalReferenceID' \
  'manifestMutation.requestCount !== 1' \
  'manifestReprocessRequestCount !== 0' \
  'manifestImageIDs.length !== 6' \
  'manifestImageIDs.some((imageID) => !imageID)' \
  'new Set(manifestImageIDs).size !== 6' \
  '/scribe.v1.ItemService/GetEditorManifest' \
  'globalThis.__scribeReadinessActiveCanvas' \
  'data-scribe-action-panel="true"' \
  '.openseadragon-canvas' \
  'response.request().resourceType() === "image"' \
  'successfulImageResponses.length > maxObservedImageResponses' \
  'successfulImageResponses.length = 0' \
  'declaredLength > maxReadinessImageBytes' \
  'await imageResponse.body()' \
  'imageBody.byteLength > maxReadinessImageBytes' \
  'name: "Next item", exact: true' \
  'manifestSecondImageID' \
  'url.searchParams.get("itemImageId") === identity.itemImageID' \
  'activeCanvas?.canvasId === identity.canvasID' \
  'activeCanvas?.itemImageId === identity.itemImageID' \
  'activeCanvas?.windowId === "scribe-editor-window"' \
  'assertExactPresentationAnnotationPage(' \
  'page.id !== expectedID' \
  'name: "Overlay off", exact: true' \
  'name: "Edit overlay", exact: true' \
  'name: "Read overlay", exact: true' \
  'name: "Outline overlay", exact: true' \
  'if (!await manifestRetranscribe.isEnabled())' \
  'assertTextualAnnotationPage(manifestAnnotationPage)' \
  '/scribe.v1.ItemService/ListItems' \
  '/scribe.v1.ItemService/DeleteItem' \
  'const scriptStartedAt = Date.now();' \
  'const mainScenarioDeadline = scriptStartedAt + mainScenarioBudgetMs;' \
  'const browserTaskDeadline = scriptStartedAt + browserTaskBudgetMs;' \
  'const browserShutdownDeadline = browserTaskDeadline - cleanupPlatformHeadroomMs;' \
  'const sessionRevocationDeadline = browserShutdownDeadline - browserCloseBudgetMs;' \
  'const globalCleanupDeadline = sessionRevocationDeadline - sessionRevocationBudgetMs;' \
  'const cleanupCommitHorizonMs = uploadTimeoutMs' \
  'observation.latestRequestAt = Date.now()' \
  '!observation.responseSettled || !observation.validated' \
  'observation.latestRequestAt + cleanupCommitHorizonMs' \
  'const recoveryDeadline = Math.min(resourceRecoveryDeadline, cleanupDeadline);' \
  'options.timeout = remainingCleanupTimeMs(recoveryDeadline);' \
  'const watchdogDelayMs = Math.max(0, mainScenarioDeadline - Date.now());' \
  'observation.validated = false' \
  'await waitForMutationResponsesToSettle(uploadMutation)' \
  'await waitForMutationResponsesToSettle(tokenMutation)' \
  'await waitForMutationResponsesToSettle(manifestMutation)' \
  'stablePasses >= cleanupStablePasses' \
  'page.close({ runBeforeUnload: false })' \
  'waitForOperationBeforeDeadline(' \
  'browser.close(),' \
  'browserShutdownDeadline' \
  'await cleanupExactAPIKeys(' \
  'await cleanupExactManifestItems(' \
  'await cleanupExactUploadItems('; do
  require_fixed "$required" web/e2e/deployed-readiness.mjs
done
forbid_pattern 'waitForEditorAnnotationCountDirection\(beforeJoinWordsCount, "decrease"\)' \
  web/e2e/deployed-readiness.mjs
require_fixed 'data-scribe-action-panel="true"' mirador-scribe/src/components/ScribeActionPanel.jsx
require_fixed 'data-scribe-transcription-active="true"' mirador-scribe/src/plugins/ScribeTextOverlayPlugin.jsx
require_fixed "data-scribe-transcription-annotation={transcriptionSegment.annotation?.id || ''}" mirador-scribe/src/plugins/ScribeTextOverlayPlugin.jsx
require_fixed 'data-scribe-transcription-attempt={transcriptionSegment.attemptNumber}' mirador-scribe/src/plugins/ScribeTextOverlayPlugin.jsx
require_fixed 'data-scribe-transcription-job={transcriptionSegment.jobId}' mirador-scribe/src/plugins/ScribeTextOverlayPlugin.jsx
require_fixed 'data-scribe-transcription-line={transcriptionSegment.done}' mirador-scribe/src/plugins/ScribeTextOverlayPlugin.jsx
require_fixed 'data-scribe-transcription-total={transcriptionSegment.total}' mirador-scribe/src/plugins/ScribeTextOverlayPlugin.jsx
require_fixed "scribe:transcription-overlay-state" mirador-scribe/src/plugins/ScribeTextOverlayPlugin.jsx
require_fixed "detail: { canvasId, ready: true, windowId }" mirador-scribe/src/plugins/ScribeTextOverlayPlugin.jsx
require_fixed "detail: { canvasId, ready: false, windowId }" mirador-scribe/src/plugins/ScribeTextOverlayPlugin.jsx
require_fixed "scribe:reload-annotations-result" web/src/pages/editor.ts
require_fixed "requestId: pending.requestId" web/src/pages/editor.ts
require_fixed 'detail?.requestId?.trim() !== pending.requestId' web/src/pages/editor.ts
require_fixed "reloadedCompletedJobs.add(pending.completionKey)" web/src/pages/editor.ts
require_fixed "renderJobStatus(pending.job)" web/src/pages/editor.ts
require_fixed "publishBatchState(message, true)" web/src/pages/editor.ts
require_fixed "ok = await reloadAnnotations(adapter, canvasId) !== false" mirador-scribe/src/plugins/useRemoteAnnotationRebase.js
require_fixed "scribe:reload-annotations-result" mirador-scribe/src/plugins/useRemoteAnnotationRebase.js
require_fixed "ScribeReloadAnnotationsResultEventDetail" mirador-scribe/src/index.d.ts
require_fixed 'startUploadPayload?.item?.id' web/e2e/deployed-readiness.mjs
require_fixed '/scribe.v1.ItemService/UploadItemImage' web/e2e/deployed-readiness.mjs
require_fixed 'String(uploadImagePayload?.item?.id ?? "") !== createdItemID' web/e2e/deployed-readiness.mjs
require_fixed '/scribe.v1.AuthService/DeleteAPIKey' web/e2e/deployed-readiness.mjs
require_fixed '/scribe.v1.AuthService/ListAPIKeys' web/e2e/deployed-readiness.mjs
require_fixed 'payload.apiKeys.filter((key) => key?.name === keyName)' web/e2e/deployed-readiness.mjs
require_fixed 'positiveID(key?.workspaceId) !== workspaceID' web/e2e/deployed-readiness.mjs
require_fixed 'tokenMutation.validated = true' web/e2e/deployed-readiness.mjs
require_fixed 'const summaries = await listItemSummaries(' web/e2e/deployed-readiness.mjs
require_fixed 'externalReferenceID,' web/e2e/deployed-readiness.mjs
require_fixed 'summary?.externalReferenceId !== externalReferenceID' web/e2e/deployed-readiness.mjs
require_fixed 'const cleanupMaxItemPages = 100;' web/e2e/deployed-readiness.mjs
require_fixed 'const cleanupMaxItems = 10_000;' web/e2e/deployed-readiness.mjs
require_fixed 'maxPages = cleanupMaxItemPages' web/e2e/deployed-readiness.mjs
require_fixed 'maxItems = cleanupMaxItems' web/e2e/deployed-readiness.mjs
require_fixed 'pageCount > maxPages' web/e2e/deployed-readiness.mjs
require_fixed 'items.length > maxItems' web/e2e/deployed-readiness.mjs
require_fixed '/scribe.v1.ItemService/ImportManifest' web/e2e/deployed-readiness.mjs
require_fixed '/scribe.v1.ImageProcessingService/ReprocessItemImage' web/e2e/deployed-readiness.mjs
require_fixed 'Math.abs(geometry.panelClientHeight - geometry.parentClientHeight) <= 2' web/e2e/deployed-readiness.mjs
require_fixed 'geometry.panelScrollWidth <= geometry.panelClientWidth + 1' web/e2e/deployed-readiness.mjs
require_fixed 'const clientCancellation = /ERR_ABORTED|cancell?ed/i.test' web/e2e/deployed-readiness.mjs
[ "$(rg -c -F 'failedSegments ?? 0) !== 0' web/e2e/deployed-readiness.mjs)" -eq 3 ] ||
  fail "completed transcription checks must treat omitted proto3 zero-valued failed_segments as zero"
forbid_pattern 'failedSegments[[:space:]]+\?\?[[:space:]]+-1' web/e2e/deployed-readiness.mjs
require_fixed 'if (requireHealthy) assertBrowserHealthy();' web/e2e/deployed-readiness.mjs
for required in \
  'async function waitForOverlayLineMarkers()' \
  'async function waitForOverlayMarkersDisabled()' \
  'await page.locator('\''[data-scribe-granularity="line"]'\'').first().waitFor({ state: "visible" });' \
  'await page.waitForFunction(() => document.querySelector("[data-scribe-granularity]") === null);' \
  'await waitForOverlayLineMarkers();' \
  'await waitForOverlayMarkersDisabled();'; do
  require_fixed "$required" web/e2e/deployed-readiness.mjs
done
[ "$(rg -c -F 'await waitForOverlayLineMarkers();' web/e2e/deployed-readiness.mjs)" -eq 2 ] ||
  fail "upload and manifest overlay checks must both wait for visible line markers"
[ "$(rg -c -F 'await waitForOverlayMarkersDisabled();' web/e2e/deployed-readiness.mjs)" -eq 2 ] ||
  fail "upload and manifest overlay checks must both wait for marker detachment"
forbid_pattern 'locator\("\[data-scribe-granularity\]"\)\.count\(\)' web/e2e/deployed-readiness.mjs
forbid_pattern 'locator\('\''\[data-scribe-granularity="line"\]'\''\)\.count\(\)' web/e2e/deployed-readiness.mjs
require_fixed 'if (await tokenField.inputValue() !== "")' web/e2e/deployed-readiness.mjs
require_fixed 'structure|save|publish|responsive|token|manifest|cleanup|network|network-' ci/run-cloud-run-readiness.sh
require_fixed '|csp|rate)$' ci/run-cloud-run-readiness.sh
assert_before web/e2e/deployed-readiness.mjs '/scribe\.v1\.AnnotationService/GetAnnotationPage' 'category = "publish"'
assert_before web/e2e/deployed-readiness.mjs 'category = "publish"' '/presentation/v3/item-image-\$\{itemImageID\}/canvas/page-1/annotations'
assert_before web/e2e/deployed-readiness.mjs 'contextSelect\.inputValue\(\) !== "0"' 'category = "upload"'
assert_before web/e2e/deployed-readiness.mjs 'editorAssetDelayReachedCompletion = transcriptionJobCompleted\(delayedJob\)' 'await page\.route\(editorAssetPattern, delayEditorAssetUntilJobCompletes\)'
assert_before web/e2e/deployed-readiness.mjs 'await route\.continue\(\)' 'page\.unroute\(editorAssetPattern, delayEditorAssetUntilJobCompletes\)'
assert_before web/e2e/deployed-readiness.mjs 'canonicalSnapshot\.revision !== completedResultRevision' 'const automaticTranscriptionProof'
assert_before web/e2e/deployed-readiness.mjs \
  'const liveCompletedJob = await waitForTerminalTranscriptionJob' \
  'const liveAutomaticTranscriptionProof = await page\.waitForFunction'
assert_before web/e2e/deployed-readiness.mjs \
  'await editorDelete\.click\(\)' \
  'const lineToken = lineTokenInputs\.first'
structure_section="$(sed -n '/category = "structure"/,/category = "save"/p' web/e2e/deployed-readiness.mjs)"
assert_text_before "$structure_section" \
  'const centeredLineAnnotationID = await selectedEditorAnnotationIDAtCount(' \
  'await page.getByRole("button", { name: "Undo", exact: true }).click()'
assert_text_before "$structure_section" \
  'await page.getByRole("button", { name: "Redo", exact: true }).click()' \
  'await waitForEditorSelection(initialDraftCount + 1, centeredLineAnnotationID)'
assert_text_before "$structure_section" \
  'await waitForEditorSelection(initialDraftCount + 1, centeredLineAnnotationID)' \
  'await editorDelete.click()'
[ "$(rg -c -F 'await editorDelete.click()' <<<"$structure_section")" -eq 1 ] ||
  fail "browser readiness must delete only the temporary centered line"
forbid_pattern 'beforeDeleteCount' web/e2e/deployed-readiness.mjs
forbid_pattern '(^|[^[:alnum:]_])words\.some\(\(annotation, index\)' web/e2e/deployed-readiness.mjs
live_transcription_section="$(sed -n '/const liveEditorPath = /,/const liveCompletedRevision = /p' web/e2e/deployed-readiness.mjs)"
assert_live_before() {
  local before="$1" after="$2" before_line after_line
  before_line="$(rg -n -F -m 1 -- "$before" <<<"$live_transcription_section" | cut -d: -f1 || true)"
  after_line="$(rg -n -F -m 1 -- "$after" <<<"$live_transcription_section" | cut -d: -f1 || true)"
  [ -n "$before_line" ] && [ -n "$after_line" ] && [ "$before_line" -lt "$after_line" ] ||
    fail "live transcription must place $before before $after"
}
assert_live_before 'const liveEventStreamReady = page.waitForResponse(' 'await navigate(liveEditorPath)'
assert_live_before 'await navigate(liveEditorPath)' 'await liveEventStreamReady'
assert_live_before 'await liveEventStreamReady' 'const routeItemImageID = new URL(window.location.href).searchParams.get("itemImageId")'
assert_live_before 'const routeItemImageID = new URL(window.location.href).searchParams.get("itemImageId")' 'evidence.badges.length = 0'
assert_live_before 'evidence.badges.length = 0' 'const liveJobID = await createTranscriptionJob('
assert_live_before 'const liveJobID = await createTranscriptionJob(' 'const liveCompletedJob = await waitForTerminalTranscriptionJob'
assert_live_before 'const liveCompletedJob = await waitForTerminalTranscriptionJob' 'const liveAutomaticTranscriptionProof = await page.waitForFunction'
[ "$(rg -c -F 'visualDeadline: Date.now() + wandVisualProofGraceMs' web/e2e/deployed-readiness.mjs)" -eq 2 ] ||
  fail "both automatic transcription proofs must retain a bounded visual queue grace period"
[ "$(rg -c -F 'if (!statusComplete) return "completed-without-terminal-status";' web/e2e/deployed-readiness.mjs)" -eq 2 ] ||
  fail "both automatic transcription proofs must bound terminal UI status drainage"
forbid_pattern 'const transcriptionOutcome = await page\.waitForFunction' web/e2e/deployed-readiness.mjs
assert_before web/e2e/deployed-readiness.mjs 'category = "structure"' 'category = "save"'
assert_before web/e2e/deployed-readiness.mjs 'manifestItem\?\.sourceUrl !== manifestURL' 'const manifestAnnotationSnapshot = await loadCanonicalAnnotationSnapshot'
assert_before web/e2e/deployed-readiness.mjs 'await navigate\(responsiveEditorPath\)' 'for \(const viewport of responsiveViewports\)'
assert_before web/e2e/deployed-readiness.mjs 'for \(const viewport of responsiveViewports\)' 'page\.setViewportSize\(\{ width: viewport\.width, height: viewport\.height \}\)'
assert_before web/e2e/deployed-readiness.mjs 'page\.setViewportSize\(\{ width: viewport\.width, height: viewport\.height \}\)' 'await assertResponsiveEditorGeometry\('
require_fixed '|| item?.sourceType !== "manifest"' web/e2e/deployed-readiness.mjs
require_fixed '|| item?.sourceUrl !== manifestURL' web/e2e/deployed-readiness.mjs
require_fixed '|| item?.externalReferenceId !== externalReferenceID' web/e2e/deployed-readiness.mjs
forbid_pattern 'manifestBaselineItemIDs|baselineManifestItems|protectedItemIDs' web/e2e/deployed-readiness.mjs
forbid_pattern 'fullItem\?\.sourceType === "manifest" && fullItem\?\.sourceUrl === manifestURL\) return "manifest"' web/e2e/deployed-readiness.mjs
manifest_cleanup="$(sed -n '/^async function exactManifestItems/,/^function newMutationObservation/p' web/e2e/deployed-readiness.mjs)"
for required in \
  'if (!readinessManifestReferencePattern.test(String(externalReferenceID ?? "")))' \
  'externalReferenceID,' \
  'summary?.externalReferenceId !== externalReferenceID' \
  'item?.sourceUrl !== manifestURL' \
  'item?.externalReferenceId !== externalReferenceID'; do
  rg -Fq "$required" <<<"$manifest_cleanup" ||
    fail "manifest cleanup is missing exact provenance evidence: $required"
done
assert_before web/e2e/deployed-readiness.mjs 'if \(isUploadImageResponse\)' 'isTargetHTTPSURL\(responseURL, baseURL\) && response\.status\(\) >= 400'
assert_before web/e2e/deployed-readiness.mjs 'manifestExternalReferenceID = manifestIdentity\.externalReferenceID' 'manifestImportAttempted = true'
assert_before web/e2e/deployed-readiness.mjs 'fetchOptions\.postData = exactManifestImportPostData\(request\)' 'route\.fetch\(fetchOptions\)'
assert_before web/e2e/deployed-readiness.mjs 'navigationResponseJSONSnapshots\.set\(request, Promise\.resolve\(snapshot\)\)' 'route\.fulfill\(\{ response: upstreamResponse \}\)'
assert_before web/e2e/deployed-readiness.mjs 'navigationResponseJSONSnapshots\.get\(response\.request\(\)\)' 'if \(isUploadImageResponse\)'
assert_before web/e2e/deployed-readiness.mjs 'page\.on\("request"' 'page\.on\("response"'
assert_before web/e2e/deployed-readiness.mjs 'page\.close\(\{ runBeforeUnload: false \}\)' 'await cleanupExactAPIKeys'
response_handler="$(sed -n '/page.on("response"/,/page.on("requestfinished"/p' web/e2e/deployed-readiness.mjs)"
request_finished_handler="$(sed -n '/page.on("requestfinished"/,/page.on("requestfailed"/p' web/e2e/deployed-readiness.mjs)"
request_failed_handler="$(sed -n '/page.on("requestfailed"/,/page.on("console"/p' web/e2e/deployed-readiness.mjs)"
finally_handler="$(sed -n '/^} finally {/,/^}$/p' web/e2e/deployed-readiness.mjs)"
watchdog_handler="$(sed -n '/const mainScenarioWatchdog =/,/const mainScenario =/p' web/e2e/deployed-readiness.mjs)"
if rg -Fq 'settleMutationRequest' <<<"$response_handler"; then
  fail "response headers must not settle a mutation before request completion"
fi
rg -Fq 'settleMutationRequest(observation, request)' <<<"$request_finished_handler" ||
  fail "finished mutation requests must settle"
for required in 'observation.validated = false' 'attempt.outcome = { kind: "transport", status: 0 }'; do
  rg -Fq "$required" <<<"$request_failed_handler" ||
    fail "failed mutation requests must invalidate response evidence: $required"
done
rg -Fq 'isTargetHTTPSURL(responseURL, baseURL) && response.status() >= 400' <<<"$response_handler" ||
  fail "generic response failures must exclude inherited-origin blob URLs"
for required in \
  'const isManifestImportResponse = sameOriginPOST' \
  'if (isManifestImportResponse && response.status() >= 400)' \
  'recordBrowserFault("manifest")'; do
  rg -Fq "$required" <<<"$response_handler" ||
    fail "manifest import failures must retain the manifest failure category: $required"
done
assert_text_before "$response_handler" \
  'if (isStartUploadBatchResponse)' \
  'if (isTargetHTTPSURL(responseURL, baseURL) && response.status() === 429)'
assert_text_before "$response_handler" \
  'if (isManifestImportResponse && response.status() >= 400)' \
  'if (isTargetHTTPSURL(responseURL, baseURL) && response.status() === 429)'
assert_text_before "$request_failed_handler" \
  'requestURL.pathname === "/scribe.v1.ItemService/StartUploadBatch"' \
  'const networkFaultCategory = requestNetworkFaultCategory(requestURL, request, baseURL);'
assert_text_before "$request_failed_handler" \
  'requestURL.pathname === "/scribe.v1.ItemService/ImportManifest"' \
  'const networkFaultCategory = requestNetworkFaultCategory(requestURL, request, baseURL);'
for required in \
  'const isStartUploadBatchResponse = sameOriginPOST' \
  'if (isStartUploadBatchResponse)' \
  'if (response.status() >= 400) recordBrowserFault("upload")'; do
  rg -Fq "$required" <<<"$response_handler" ||
    fail "upload-batch failures must retain the upload failure category: $required"
done
for required in \
  'requestURL.pathname === "/scribe.v1.ItemService/StartUploadBatch"' \
  'recordBrowserFault("upload")' \
  'requestURL.pathname === "/scribe.v1.ItemService/ImportManifest"' \
  'recordBrowserFault("manifest")'; do
  rg -Fq "$required" <<<"$request_failed_handler" ||
    fail "failed navigation mutations must retain their stage category: $required"
done
for required in \
  'if (isTargetHTTPSURL(responseURL, baseURL) && response.status() === 429)' \
  'recordBrowserFault("rate")' \
  'response.status() === 404' \
  'presentationAnnotationPathPattern.test(responseURL.pathname)' \
  'recordBrowserFault("annotations")'; do
  rg -Fq "$required" <<<"$response_handler" ||
    fail "bounded response attribution is missing: $required"
done
assert_before web/e2e/deployed-readiness.mjs \
  'if \(isManifestImportResponse && response\.status\(\) >= 400\)' \
  'isTargetHTTPSURL\(responseURL, baseURL\) && response\.status\(\) >= 400'
assert_before web/e2e/deployed-readiness.mjs \
  'if \(isTargetHTTPSURL\(responseURL, baseURL\) && response\.status\(\) === 429\)' \
  'isTargetHTTPSURL\(responseURL, baseURL\) && response\.status\(\) >= 400'
assert_before web/e2e/deployed-readiness.mjs \
  'presentationAnnotationPathPattern\.test\(responseURL\.pathname\)' \
  'isTargetHTTPSURL\(responseURL, baseURL\) && response\.status\(\) >= 400'
rg -Fq 'isTargetHTTPSURL(requestURL, baseURL) && !clientCancellation' <<<"$request_failed_handler" ||
  fail "generic request failures must exclude inherited-origin blob URLs"
for required in \
  'function networkPathFamily(pathname, resourceType)' \
  'function responseNetworkFaultCategory(responseURL, response, target)' \
  'function requestNetworkFaultCategory(requestURL, request, target)' \
  'networkFaultCategory = responseNetworkFaultCategory(responseURL, response, baseURL)' \
  'networkFaultCategory = requestNetworkFaultCategory(requestURL, request, baseURL)' \
  'assertNetworkFaultClassifiers();'; do
  require_fixed "$required" web/e2e/deployed-readiness.mjs
done
for required in \
  'let browserFaultMonitoringActive = true;' \
  'function recordBrowserFault(faultCategory)' \
  'if (browserFaultMonitoringActive && !browserFaultCategory)' \
  'failureCategory ??= browserFaultCategory;' \
  'browserFaultMonitoringActive = false;'; do
  require_fixed "$required" web/e2e/deployed-readiness.mjs
done
forbid_pattern 'browserFaultCategory[[:space:]]*\?\?=' web/e2e/deployed-readiness.mjs
forbid_pattern '^failureCategory = browserFaultCategory \?\? failureCategory;' web/e2e/deployed-readiness.mjs
assert_before web/e2e/deployed-readiness.mjs \
  'browserFaultMonitoringActive = false' \
  'Stop browser-side retry timers before calculating'
assert_text_before "$finally_handler" \
  'failureCategory ??= browserFaultCategory;' \
  'browserFaultMonitoringActive = false;'
assert_text_before "$watchdog_handler" \
  'browserFaultMonitoringActive = false;' \
  'watchdogPageClose = page.close({ runBeforeUnload: false })'
for cleanup_boundary in \
  'watchdogPageClose,' \
  'page.close({ runBeforeUnload: false })' \
  'browser.close()'; do
  assert_text_before "$finally_handler" 'browserFaultMonitoringActive = false;' "$cleanup_boundary"
done
for required in \
  'network-document-client' \
  'network-auth-server' \
  'network-events-client' \
  'network-asset-server' \
  'network-document-transport' \
  'network-api-transport' \
  'network-events-transport' \
  'network-image-transport' \
  'network-asset-transport' \
  'network-other-transport'; do
  require_fixed "$required" web/e2e/deployed-readiness.mjs
done
forbid_pattern 'settings-api-key-form"\)\.count\(\) > 0' web/e2e/deployed-readiness.mjs
forbid_pattern 'findAPIKeyDeleteByName|deleteAPIKeyWithConfirmation' web/e2e/deployed-readiness.mjs
forbid_pattern 'response\.json\(\)\.then|outcome\.response\.json\(|snapshotNavigationResponseJSON\(createKeyResponse\)' web/e2e/deployed-readiness.mjs
forbid_pattern 'Math\.abs\(second\.y - first\.y\)|previous\?\.x === sample\.x|previous\?\.y === sample\.y' web/e2e/deployed-readiness.mjs
forbid_pattern 'console\.(log|error|warn)|response\.text\(\)|message\.text\(\).*process\.' web/e2e/deployed-readiness.mjs
require_pattern '\^scribe-pr-\[1-9\]\[0-9\]\*-' web/e2e/deployed-readiness.mjs
require_pattern '\^scribe-\[1-9\]\[0-9\]\*\\\.' web/e2e/deployed-readiness.mjs
[ "$(rg -c -F 'process.stderr.write(`browser readiness failed: ${failureCategory}\n`)' web/e2e/deployed-readiness.mjs)" -eq 1 ] ||
  fail "the runner must emit exactly one bounded failure marker"
readiness_exit_map="$(sed -n '/^const readinessFailureExitCodes = new Map(\[/,/^\]);/p' web/e2e/deployed-readiness.mjs)"
expected_readiness_exit_map='const readinessFailureExitCodes = new Map([
  ["home", 21],
  ["context", 22],
  ["upload", 23],
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
]);'
[ "$readiness_exit_map" = "$expected_readiness_exit_map" ] ||
  fail "every bounded browser failure category must have one exact task exit code"
browser_log_pattern="$(sed -nE "s/^readonly BROWSER_READINESS_LOG_PATTERN='(.*)'$/\1/p" ci/run-cloud-run-readiness.sh)"
[ -n "$browser_log_pattern" ] || fail "could not load the browser readiness marker allowlist"
expected_browser_log_pattern='^browser readiness failed: (home|context|upload|handoff|transcription|annotations|editor|overlay|retranscribe|structure|save|publish|responsive|token|manifest|cleanup|network|network-(document|auth|workspace|item|context|annotation|processing|transcription|events|presentation|iiif|asset|other)-(client|server)|network-(document|api|events|image|asset|other)-transport|initial-ingress-(forbidden|not-found)|csp|rate)$'
[ "$browser_log_pattern" = "$expected_browser_log_pattern" ] ||
  fail "the browser marker allowlist changed without an exact exit-map contract update"
mapfile -t readiness_exit_entries < <(
  sed -n '/^const readinessFailureExitCodes = new Map(\[/,/^\]);/p' web/e2e/deployed-readiness.mjs |
    sed -nE 's/^  \["([a-z0-9-]+)", ([0-9]+)\],$/\1 \2/p'
)
[ "${#readiness_exit_entries[@]}" -eq 53 ] ||
  fail "the exact browser failure map changed without updating its exhaustive contract"
declare -A readiness_exit_values=()
for entry in "${readiness_exit_entries[@]}"; do
  read -r failure_name failure_code <<<"$entry"
  [[ "browser readiness failed: ${failure_name}" =~ $browser_log_pattern ]] ||
    fail "browser failure marker is not allowlisted: $failure_name"
  [ "$failure_code" -ge 1 ] && [ "$failure_code" -le 125 ] ||
    fail "browser failure exit code is outside the portable bounded range: $failure_code"
  [ -z "${readiness_exit_values[$failure_code]:-}" ] ||
    fail "browser failure exit code is duplicated: $failure_code"
  readiness_exit_values[$failure_code]="$failure_name"
done
for invalid_marker in \
  'browser readiness failed: network-cookie-client' \
  'browser readiness failed: network-item-timeout' \
  'browser readiness failed: network-item-client trailing'; do
  if [[ "$invalid_marker" =~ $browser_log_pattern ]]; then
    fail "browser marker allowlist accepted an unknown or suffixed category: $invalid_marker"
  fi
done
require_fixed 'process.exitCode = readinessFailureExitCodes.get(failureCategory) ?? 1;' web/e2e/deployed-readiness.mjs

inference_timeout_seconds="$(
  sed -nE 's/^const InferenceRequestTimeout = ([0-9]+) \* time\.Second$/\1/p' internal/segmentor/client.go
)"
proxy_timeout_ms="$(
  sed -nE 's/^const defaultBackendUpstreamTimeoutMs = ([0-9_]+);$/\1/p' web/server.mjs | tr -d '_'
)"
frontend_request_budget_ms="$(
  sed -nE 's/^const defaultFrontendRequestBudgetMs = ([0-9_]+);$/\1/p' web/server.mjs | tr -d '_'
)"
browser_upload_timeout_ms="$(
  sed -nE 's/^const uploadTimeoutMs = ([0-9_]+);$/\1/p' web/e2e/deployed-readiness.mjs | tr -d '_'
)"
browser_stage_timeout_ms="$(
  sed -nE 's/^const stageTimeoutMs = ([0-9_]+);$/\1/p' web/e2e/deployed-readiness.mjs | tr -d '_'
)"
browser_task_budget_ms="$(
  sed -nE 's/^const browserTaskBudgetMs = ([0-9_]+);$/\1/p' web/e2e/deployed-readiness.mjs | tr -d '_'
)"
browser_main_scenario_budget_ms="$(
  sed -nE 's/^const mainScenarioBudgetMs = ([0-9_]+);$/\1/p' web/e2e/deployed-readiness.mjs | tr -d '_'
)"
browser_cleanup_reserve_ms="$(
  sed -nE 's/^const cleanupReserveMs = ([0-9_]+);$/\1/p' web/e2e/deployed-readiness.mjs | tr -d '_'
)"
browser_cleanup_platform_headroom_ms="$(
  sed -nE 's/^const cleanupPlatformHeadroomMs = ([0-9_]+);$/\1/p' web/e2e/deployed-readiness.mjs | tr -d '_'
)"
browser_session_revocation_budget_ms="$(
  sed -nE 's/^const sessionRevocationBudgetMs = ([0-9_]+);$/\1/p' web/e2e/deployed-readiness.mjs | tr -d '_'
)"
browser_close_budget_ms="$(
  sed -nE 's/^const browserCloseBudgetMs = ([0-9_]+);$/\1/p' web/e2e/deployed-readiness.mjs | tr -d '_'
)"
browser_job_timeout_seconds="$(
  sed -n '/^resource "google_cloud_run_v2_job" "browser_readiness"/,/^}/p' terraform/readiness.tf |
    sed -nE 's/^[[:space:]]*timeout[[:space:]]*=[[:space:]]*"([0-9]+)s"$/\1/p'
)"
[[ "$inference_timeout_seconds" =~ ^[1-9][0-9]*$ \
  && "$proxy_timeout_ms" =~ ^[1-9][0-9]*$ \
  && "$frontend_request_budget_ms" =~ ^[1-9][0-9]*$ \
  && "$browser_upload_timeout_ms" =~ ^[1-9][0-9]*$ \
  && "$browser_stage_timeout_ms" =~ ^[1-9][0-9]*$ \
  && "$browser_task_budget_ms" =~ ^[1-9][0-9]*$ \
  && "$browser_main_scenario_budget_ms" =~ ^[1-9][0-9]*$ \
  && "$browser_cleanup_reserve_ms" =~ ^[1-9][0-9]*$ \
  && "$browser_cleanup_platform_headroom_ms" =~ ^[1-9][0-9]*$ \
  && "$browser_session_revocation_budget_ms" =~ ^[1-9][0-9]*$ \
  && "$browser_close_budget_ms" =~ ^[1-9][0-9]*$ \
  && "$browser_job_timeout_seconds" =~ ^[1-9][0-9]*$ ]] ||
  fail "could not resolve the inference, frontend, and browser timeout chain"
inference_timeout_ms=$((inference_timeout_seconds * 1000))
[ "$inference_timeout_ms" -lt "$proxy_timeout_ms" ] && [ "$proxy_timeout_ms" -lt "$frontend_request_budget_ms" ] && [ "$frontend_request_budget_ms" -lt "$browser_upload_timeout_ms" ] ||
  fail "timeouts must satisfy inference < proxy cap < frontend request < browser upload"
[ "$frontend_request_budget_ms" -lt 300000 ] ||
  fail "the frontend request budget must retain margin below the platform boundary"
[ "$browser_upload_timeout_ms" -ge 300000 ] ||
  fail "the browser mutation cleanup horizon must cover at least 300 seconds"
[ "$browser_main_scenario_budget_ms" -eq 1620000 ] ||
  fail "the browser product scenario must stop after exactly 27 minutes"
[ "$browser_cleanup_reserve_ms" -ge 780000 ] ||
  fail "the browser job must reserve at least 13 minutes for cleanup and shutdown"
[ "$browser_session_revocation_budget_ms" -eq "$browser_stage_timeout_ms" ] ||
  fail "production logout must retain the full stage timeout"
[ "$browser_close_budget_ms" -ge 30000 ] ||
  fail "browser close must retain a bounded 30-second interval"
[ "$((browser_close_budget_ms + browser_cleanup_platform_headroom_ms))" -ge 120000 ] ||
  fail "browser close and platform shutdown must retain two minutes"
[ "$((browser_cleanup_reserve_ms - browser_session_revocation_budget_ms - browser_close_budget_ms - browser_cleanup_platform_headroom_ms))" -ge "$((browser_upload_timeout_ms + browser_stage_timeout_ms))" ] ||
  fail "browser cleanup cannot cover its commit horizon and recovery tail"
[ "$browser_job_timeout_seconds" -eq 2400 ] ||
  fail "the browser Cloud Run job must retain its reviewed 40-minute bound"
[ "$browser_task_budget_ms" -eq "$((browser_job_timeout_seconds * 1000))" ] ||
  fail "the browser script budget must equal the Cloud Run task timeout"
[ "$((browser_main_scenario_budget_ms + browser_cleanup_reserve_ms))" -eq "$browser_task_budget_ms" ] ||
  fail "the browser scenario and cleanup reserve exceed the Cloud Run job timeout"
[ "$(rg -c -F 'globalCleanupDeadline,' web/e2e/deployed-readiness.mjs)" -eq 5 ] ||
  fail "page shutdown and all three disposable-resource cleanup paths must share the global deadline"
require_fixed 'upstreamTimeoutForBudgetMs(' web/server.mjs
require_fixed 'server.requestTimeout = frontendRequestBudgetMs;' web/server.mjs
require_fixed 'COPY web/request-budget.mjs /app/web/request-budget.mjs' Dockerfile.frontend
require_fixed 'caps upstream inactivity by the remaining end-to-end request budget' web/request-budget.test.mjs
require_fixed 'enforces the absolute request budget despite active upstream data' web/server-lifecycle.test.mjs
require_fixed 'retries a cold upload before starting it with a truncated inference budget' web/server-lifecycle.test.mjs
require_fixed 'rejects timeout configurations outside the frontend platform boundary' web/server-lifecycle.test.mjs

require_fixed 'normalized_browser_readiness_image = trimspace(var.browser_readiness_image)' terraform/readiness.tf
require_pattern 'browser_readiness_enabled[[:space:]]+= \(local\.is_prod_workspace \|\| local\.is_preview_workspace\) && local\.normalized_browser_readiness_image != ""' terraform/readiness.tf
require_fixed 'check "browser_readiness_workspace_scope"' terraform/readiness.tf
require_fixed 'resource "google_compute_network" "application"' terraform/application_network.tf
require_fixed 'resource "google_compute_subnetwork" "application"' terraform/application_network.tf
require_fixed 'from = module.scribe.module.gcp[0].google_compute_network.cloud-compose[0]' terraform/application_network_moved.tf
require_fixed 'to   = google_compute_network.application' terraform/application_network_moved.tf
require_fixed 'from = module.scribe.module.gcp[0].google_compute_subnetwork.cloud-compose[0]' terraform/application_network_moved.tf
require_fixed 'to   = google_compute_subnetwork.application' terraform/application_network_moved.tf
require_fixed 'create                   = false' terraform/main.tf
require_fixed 'name                     = google_compute_network.application.self_link' terraform/main.tf
require_fixed 'subnetwork               = google_compute_subnetwork.application.self_link' terraform/main.tf
forbid_pattern 'resource "google_compute_network" "browser_readiness' terraform/readiness.tf
forbid_pattern 'resource "google_compute_(address|router|router_nat|subnetwork)" "browser_readiness_ipv6"' terraform/readiness.tf
require_fixed 'browser_readiness_image = local.normalized_browser_readiness_image' terraform/outputs.tf
require_fixed 'image = local.normalized_browser_readiness_image' terraform/readiness.tf
require_pattern 'browser_readiness_name_hash[[:space:]]+= substr\(sha256\("\$\{var\.name\}:\$\{local\.workspace_slug\}"\), 0, 8\)' terraform/readiness.tf
require_fixed 'substr(var.name, 0, 46)' terraform/readiness.tf
require_fixed 'substr(var.name, 0, 32)' terraform/readiness.tf
# shellcheck disable=SC2016 # This is a literal Terraform source assertion.
require_fixed 'substr("probe-browser-${local.workspace_slug}", 0, 21)' terraform/readiness.tf
for resource in google_compute_address google_compute_router google_compute_router_nat google_compute_subnetwork google_cloud_run_v2_job google_service_account; do
  require_pattern "resource \"${resource}\" \"browser_readiness\"" terraform/readiness.tf
done
require_pattern 'resource "google_compute_firewall" "browser_readiness_isolation"' terraform/readiness.tf
for required in \
  'nat_ip_allocate_option             = "MANUAL_ONLY"' \
  'nat_ips                            = [google_compute_address.browser_readiness[0].self_link]' \
  'source_subnetwork_ip_ranges_to_nat = "LIST_OF_SUBNETWORKS"' \
  'name                    = google_compute_subnetwork.browser_readiness[0].self_link' \
  'source_ip_ranges_to_nat = ["ALL_IP_RANGES"]' \
  'service_account = google_service_account.browser_readiness[0].email' \
  'egress = "ALL_TRAFFIC"' \
  'value = local.public_base_url'; do
  require_fixed "$required" terraform/readiness.tf
done
require_fixed 'value = "--dns-result-order=ipv6first --no-network-family-autoselection"' terraform/readiness.tf
for required in \
  'network                  = google_compute_network.application.self_link' \
  'stack_type               = "IPV4_IPV6"' \
  'ipv6_access_type         = "EXTERNAL"' \
  'strcontains(self.external_ipv6_prefix, ":")' \
  'network    = local.browser_readiness_network_resource_name' \
  'subnetwork = local.browser_readiness_subnetwork_resource_name' \
  'tags       = [local.browser_readiness_network_tag]'; do
  require_fixed "$required" terraform/readiness.tf
done
require_fixed 'google_compute_subnetwork.browser_readiness[0].external_ipv6_prefix' terraform/readiness.tf
browser_readiness_allowlist="$(sed -n \
  '/browser_readiness_allowed_ips =/,/] : \[\]/p' terraform/readiness.tf)"
[ "$(rg -c -F 'google_compute_subnetwork.browser_readiness[0].external_ipv6_prefix' \
  <<<"$browser_readiness_allowlist")" -eq 1 ] ||
  fail "the protected PPB allowlist must contain exactly the dedicated external IPv6 prefix"
if rg -q 'google_compute_address' <<<"$browser_readiness_allowlist"; then
  fail "a browser NAT IPv4 address still grants protected PPB ingress"
fi
if rg -q 'try\(' <<<"$browser_readiness_allowlist"; then
  fail "the protected PPB allowlist must not fall back around the verified external IPv6 prefix"
fi
protected_browser_resources="$(sed -n \
  '/^resource "google_compute_subnetwork" "browser_readiness"/,/^resource "google_service_account" "backend_readiness"/p' \
  terraform/readiness.tf)"
forbid_pattern 'module\.scribe' /dev/stdin <<<"$protected_browser_resources"
# A data source reading this subnet back would observe live infrastructure at
# plan time and see the pre-update empty prefix on the initial IPV4_ONLY ->
# IPV4_IPV6 transition, since depends_on alone does not defer a data read whose
# own arguments are already known. The postcondition instead lives on the
# managed resource's own computed attribute, which Terraform correctly treats
# as unknown-until-apply on that transition and defers accordingly.
managed_browser_subnet="$(sed -n \
  '/^resource "google_compute_subnetwork" "browser_readiness"/,/^}/p' \
  terraform/readiness.tf)"
for required in \
  'self.stack_type == "IPV4_IPV6"' \
  'self.ipv6_access_type == "EXTERNAL"' \
  'can(cidrhost(self.external_ipv6_prefix, 0))' \
  'strcontains(self.external_ipv6_prefix, ":")' \
  'endswith(self.external_ipv6_prefix, "/64")'; do
  require_fixed "$required" /dev/stdin <<<"$managed_browser_subnet"
done
if rg -q '^data "google_compute_subnetwork" "browser_readiness"' terraform/readiness.tf; then
  fail "the external IPv6 postcondition must live on the managed subnet, not a separate data read"
fi
require_fixed 'ip_cidr_range            = var.browser_readiness_subnet_cidr' terraform/readiness.tf
require_fixed 'private_ip_google_access = false' terraform/readiness.tf
[ "$(rg -c -F 'private_ip_google_access = false' terraform/readiness.tf)" -eq 1 ] ||
  fail "the dedicated dual-stack browser subnet must keep Private Google Access disabled"
require_fixed 'private_ip_google_access = true' terraform/application_network.tf
require_fixed 'check "browser_readiness_subnet_isolated"' terraform/readiness.tf
require_fixed 'setintersection(' terraform/readiness.tf
for required in \
  'direction          = "EGRESS"' \
  'priority           = 100' \
  'destination_ranges = [var.network_ip_cidr_range]' \
  'target_tags        = [local.browser_readiness_network_tag]' \
  'protocol = "all"'; do
  require_fixed "$required" terraform/readiness.tf
done
require_fixed 'name  = "SCRIBE_BROWSER_MODE"' terraform/readiness.tf
require_fixed 'value = local.is_prod_workspace ? "production" : "preview"' terraform/readiness.tf
require_fixed 'name = "SCRIBE_BROWSER_STORAGE_STATE_JSON"' terraform/readiness.tf
require_fixed 'secret  = google_secret_manager_secret.browser_session[0].secret_id' terraform/readiness.tf
require_fixed 'version = google_secret_manager_secret_version.browser_session_placeholder[0].version' terraform/readiness.tf
require_fixed 'condition     = self.version == "1"' terraform/production_browser_session.tf
require_fixed 'browser_readiness_session_secret = try(google_secret_manager_secret.browser_session[0].secret_id, "")' terraform/outputs.tf
require_fixed 'output "browser_readiness_session_secret"' terraform/outputs.tf
# shellcheck disable=SC2016 # These are literal Terraform source assertions.
for required in \
  'resource "google_secret_manager_secret" "browser_session"' \
  'secret_id = local.production_browser_session_secret_id' \
  'resource "google_secret_manager_secret_version" "browser_session_placeholder"' \
  'secret_data_wo         = jsonencode({ cookies = [], origins = [] })' \
  'resource "google_secret_manager_secret_iam_member" "browser_session_accessor"' \
  'role      = "roles/secretmanager.secretAccessor"' \
  'member    = "serviceAccount:${google_service_account.browser_readiness[0].email}"' \
  'resource "google_secret_manager_secret_iam_member" "browser_session_version_manager"' \
  'role      = "roles/secretmanager.secretVersionManager"' \
  'member    = "serviceAccount:${local.production_deploy_service_account_email}"'; do
  require_fixed "$required" terraform/production_browser_session.tf
done
require_fixed 'service            = "secretmanager.googleapis.com"' terraform/foundation/main.tf
browser_readiness_job="$(sed -n \
  '/^resource "google_cloud_run_v2_job" "browser_readiness"/,/^resource "google_cloud_run_v2_job" "backend_readiness"/p' \
  terraform/readiness.tf)"
for required in \
  'google_compute_firewall.browser_readiness_isolation,' \
  'google_compute_router_nat.browser_readiness,' \
  'google_secret_manager_secret_iam_member.browser_session_accessor,' \
  'google_secret_manager_secret_version.browser_session_placeholder,'; do
  require_fixed "$required" /dev/stdin <<<"$browser_readiness_job"
done
[ "$(rg -c -F 'subnetwork = local.readiness_subnetwork_resource_name' terraform/readiness.tf)" -eq 2 ] ||
  fail "only the backend and OCR probes may use the application subnet"
[ "$(rg -c -F 'subnetwork = local.browser_readiness_subnetwork_resource_name' terraform/readiness.tf)" -eq 1 ] ||
  fail "only the browser probe may use the isolated dual-stack subnet"
require_fixed 'power_button_allowed_ips = distinct(concat(var.allowed_ips, local.browser_readiness_allowed_ips))' terraform/main.tf
[ "$(rg -l 'browser_readiness_allowed_ips' terraform/*.tf | wc -l)" -eq 2 ] ||
  fail "the derived browser /64 must be consumed only by readiness and its protected environment's PPB allowlist"
for iam_file in terraform/backup.tf terraform/iam.tf terraform/kraken.tf terraform/pubsub.tf terraform/storage.tf terraform/vault.tf; do
  [ ! -f "$iam_file" ] || forbid_pattern 'browser_readiness' "$iam_file"
done

for replay_file in \
  terraform/outputs.tf \
  terraform/deploy-local.sh \
  ci/resolve-destroy-inputs.sh \
  ci/resolve-refresh-inputs.sh \
  ci/resolve-rollback-inputs.sh; do
  require_fixed 'browser_readiness_image' "$replay_file"
  require_fixed 'browser_readiness_subnet_cidr' "$replay_file"
done
require_fixed '!endswith(local.normalized_browser_readiness_image, "sha256:0000000000000000000000000000000000000000000000000000000000000000")' terraform/outputs.tf
require_fixed 'has("browser_readiness_image") | not' ci/resolve-destroy-inputs.sh
require_fixed 'has("browser_readiness_image") | not' ci/resolve-rollback-inputs.sh

replay_test_dir="$(mktemp -d)"
trap 'rm -rf "$replay_test_dir"' EXIT
jq 'del(.browser_readiness_image)' ci/fixtures/deployment-inputs.json >"$replay_test_dir/legacy.json"
legacy_replay="$(GCLOUD_PROJECT=scribe-test1 ci/resolve-destroy-inputs.sh <"$replay_test_dir/legacy.json")"
[ "$(jq -r '.browser_readiness_image' <<<"$legacy_replay")" = "" ] ||
  fail "pre-bootstrap lifecycle state did not normalize to the historical empty browser image"
jq 'del(.configuration.browser_readiness_subnet_cidr)' ci/fixtures/deployment-inputs.json >"$replay_test_dir/legacy-subnet.json"
legacy_subnet_replay="$(GCLOUD_PROJECT=scribe-test1 ci/resolve-destroy-inputs.sh <"$replay_test_dir/legacy-subnet.json")"
[ "$(jq -r '.configuration.browser_readiness_subnet_cidr' <<<"$legacy_subnet_replay")" = "10.43.0.0/26" ] ||
  fail "pre-bootstrap lifecycle state did not normalize to the reviewed browser subnet default"
for invalid_filter in \
  '.browser_readiness_image = null' \
  '.browser_readiness_image = "us-docker.pkg.dev/scribe-test1/internal/scribe-browser-readiness@sha256:0000000000000000000000000000000000000000000000000000000000000000"' \
  '.configuration.browser_readiness_subnet_cidr = null' \
  '.configuration.browser_readiness_subnet_cidr = "10.43.0.0/24"'; do
  jq "$invalid_filter" ci/fixtures/deployment-inputs.json >"$replay_test_dir/invalid.json"
  if GCLOUD_PROJECT=scribe-test1 ci/resolve-destroy-inputs.sh <"$replay_test_dir/invalid.json" >/dev/null 2>&1; then
    fail "destroy replay accepted a null or placeholder browser readiness image"
  fi
done

require_fixed 'ref: refs/heads/main' .github/workflows/terraform-preview.yaml
# shellcheck disable=SC2016 # This is a literal GitHub expression assertion.
require_fixed 'checkout_ref: ${{ needs.prepare.outputs.base_sha }}' .github/workflows/terraform-preview.yaml
# shellcheck disable=SC2016 # This is a literal GitHub expression assertion.
require_fixed 'browser_readiness_source_sha: ${{ needs.prepare.outputs.head_sha }}' .github/workflows/terraform-preview.yaml
require_fixed 'browser_readiness_source_sha:' .github/workflows/terraform-deploy.yaml
require_fixed 'preview apply requires an immutable browser readiness source SHA' .github/workflows/terraform-deploy.yaml
require_fixed 'browser readiness source is restricted to preview apply' .github/workflows/terraform-deploy.yaml
require_fixed "if: inputs.mode == 'apply' && inputs.pr_number != ''" .github/workflows/terraform-deploy.yaml
# shellcheck disable=SC2016 # This is a literal GitHub expression assertion.
require_fixed 'PROTECTED_SOURCE_SHA: ${{ inputs.checkout_ref }}' .github/workflows/terraform-deploy.yaml
# shellcheck disable=SC2016 # This is a literal GitHub expression assertion.
require_fixed 'BROWSER_READINESS_SOURCE_SHA: ${{ inputs.browser_readiness_source_sha }}' .github/workflows/terraform-deploy.yaml
require_fixed 'name: Stage exact PR-head browser readiness source' .github/workflows/terraform-deploy.yaml
# shellcheck disable=SC2016 # This is a literal shell-source assertion.
require_fixed 'run: bash ./ci/prepare-browser-readiness-source.sh "$BROWSER_READINESS_SOURCE_SHA"' .github/workflows/terraform-deploy.yaml
# shellcheck disable=SC2016 # This is a literal GitHub expression assertion.
require_fixed 'BROWSER_READINESS_CONTENTS_TOKEN: ${{ github.token }}' .github/workflows/terraform-deploy.yaml
forbid_pattern '(node|npm|npx|bash|sh)[[:space:]].*(web/e2e/)?deployed-readiness\.mjs' .github/workflows/terraform-deploy.yaml
require_fixed 'git hash-object --no-filters web/e2e/deployed-readiness.mjs' .github/workflows/terraform-deploy.yaml
require_fixed 'SCRIBE_BROWSER_READINESS_SCRIPT_BLOB_SHA' .github/workflows/terraform-deploy.yaml
# shellcheck disable=SC2016 # This is a literal shell-source assertion.
require_fixed '[ "$SCRIBE_BROWSER_READINESS_SOURCE_SHA" = "$BROWSER_READINESS_SOURCE_SHA" ]' .github/workflows/terraform-deploy.yaml
require_fixed '--file Dockerfile.browser-readiness' .github/workflows/terraform-deploy.yaml
require_fixed '--provenance=true' .github/workflows/terraform-deploy.yaml
require_fixed '--sbom=true' .github/workflows/terraform-deploy.yaml
# shellcheck disable=SC2016 # This is a literal shell-source assertion.
require_fixed 'scribe-browser-readiness:${readiness_source_sha}' .github/workflows/terraform-deploy.yaml
# shellcheck disable=SC2016 # This is a literal shell-source assertion.
require_fixed 'SCRIBE_BROWSER_READINESS_IMAGE=$readiness_image' .github/workflows/terraform-deploy.yaml
require_fixed 'name: Verify frontend, backend, and OCR readiness' .github/workflows/terraform-deploy.yaml
require_fixed 'inputs.tf_workspace }}-browser-readiness-diagnostics.log' .github/workflows/terraform-deploy.yaml
require_fixed 'browser_readiness_image:' .github/workflows/terraform-deploy.yaml
# shellcheck disable=SC2016 # Literal reusable-workflow input expression.
require_fixed 'SCRIBE_BROWSER_READINESS_IMAGE: ${{ inputs.browser_readiness_image }}' .github/workflows/terraform-deploy.yaml
require_fixed 'name: Build isolated browser readiness image' .github/workflows/terraform-deploy.yaml
require_fixed 'browser_readiness_session_secret' .github/workflows/terraform-deploy.yaml
require_fixed 'run-production-browser-readiness.sh' .github/workflows/terraform-deploy.yaml
# shellcheck disable=SC2016 # Literal protected workflow input expression.
require_fixed 'SCRIBE_DEPLOYMENT_ENVIRONMENT: ${{ inputs.environment_name }}' .github/workflows/terraform-deploy.yaml
# shellcheck disable=SC2016 # Literal reusable-workflow output/input expressions.
require_fixed 'browser_readiness_image: ${{ steps.deployed.outputs.browser_readiness_image }}' .github/workflows/terraform-apply.yaml
# shellcheck disable=SC2016 # Match the literal workflow shell expression.
require_fixed 'echo "browser_readiness_image=$(jq -r '\''.browser_readiness_image'\'' <<<"$deployed")"' .github/workflows/terraform-apply.yaml
# shellcheck disable=SC2016 # Literal reusable-workflow input expression.
require_fixed 'browser_readiness_image: ${{ needs.resolve-plan-inputs.outputs.browser_readiness_image }}' .github/workflows/terraform-apply.yaml
assert_before .github/workflows/terraform-deploy.yaml 'name: Checkout repository' 'name: Stage exact PR-head browser readiness source'
assert_before .github/workflows/terraform-deploy.yaml 'name: Stage exact PR-head browser readiness source' 'name: Authenticate to Google Cloud'
assert_before .github/workflows/terraform-deploy.yaml 'run: ./ci/verify-gcp-wif\.sh' 'name: Build isolated browser readiness image'

stage_source_step="$(sed -n '/name: Stage exact PR-head browser readiness source/,/name: Set up Go for preview Vault reconciliation/p' .github/workflows/terraform-deploy.yaml)"
if rg -qi '(^|[[:space:]])(node|npm|npx)[[:space:]]|deployed-readiness\.mjs[[:space:]]*$' <<<"$stage_source_step"; then
  fail "the pre-cloud-auth staging step must not execute the PR-head script"
fi
# shellcheck disable=SC2016 # These are literal protected-helper source assertions.
for required in \
  'readonly SOURCE_PATH="web/e2e/deployed-readiness.mjs"' \
  'readonly MAX_SOURCE_BYTES=262144' \
  'readonly MAX_RESPONSE_BYTES=524288' \
  'curl --disable' \
  'commit_url="https://api.github.com/repos/${repository}/git/commits/${source_sha}"' \
  'root_tree_url="https://api.github.com/repos/${repository}/git/trees/${root_tree_sha}"' \
  'web_tree_url="https://api.github.com/repos/${repository}/git/trees/${web_tree_sha}"' \
  'e2e_tree_url="https://api.github.com/repos/${repository}/git/trees/${e2e_tree_sha}"' \
  'require_tree_entry "$root_tree_response_path" "$root_tree_sha" "web" "040000" "tree"' \
  'require_tree_entry "$web_tree_response_path" "$web_tree_sha" "e2e" "040000" "tree"' \
  'require_tree_entry "$e2e_tree_response_path" "$e2e_tree_sha" "deployed-readiness.mjs" "100644" "blob"' \
  '.type == "file"' \
  '.encoding == "base64"' \
  '.path == $path' \
  '.sha == $blob_sha' \
  'git hash-object --no-filters "$candidate_path"' \
  'install -m 0644 "$candidate_path" "$SOURCE_PATH"' \
  'SCRIBE_BROWSER_READINESS_SOURCE_SHA=%s' \
  'SCRIBE_BROWSER_READINESS_SCRIPT_BLOB_SHA=%s'; do
  require_fixed "$required" ci/prepare-browser-readiness-source.sh
done
forbid_pattern 'recursive(=|%3[dD])' ci/prepare-browser-readiness-source.sh
for tree_rejection in parent-symlink parent-gitlink source-symlink source-gitlink duplicate-source-entry truncated-tree; do
  require_fixed "$tree_rejection" ci/prepare-browser-readiness-source_test.sh
done
require_pattern "BROWSER_READINESS_LOG_PATTERN=.*structure.*manifest" ci/run-cloud-run-readiness.sh

echo "Protected browser readiness is exact-source, isolated, replayable, and categorical."
