import { spawn } from "node:child_process";
import { fileURLToPath } from "node:url";

function positiveInteger(name, fallback) {
  const value = Number.parseInt(process.env[name] || "", 10);
  return Number.isSafeInteger(value) && value > 0 ? value : fallback;
}

const attempts = positiveInteger("SCRIBE_READINESS_ATTEMPTS", 30);
const fetchTimeoutMs = positiveInteger("SCRIBE_READINESS_FETCH_TIMEOUT_MS", 5_000);
const retryMs = positiveInteger("SCRIBE_READINESS_RETRY_MS", 3_000);
const expectedAPIImage = (process.env.SCRIBE_EXPECTED_API_IMAGE || "").trim();
if (!/^[^\s@]+@sha256:[0-9a-f]{64}$/.test(expectedAPIImage)) {
  throw new Error("SCRIBE_EXPECTED_API_IMAGE must be a digest-pinned image reference");
}
const readinessURL = "http://127.0.0.1:8888/healthz";
const serverPath = fileURLToPath(new URL("./server.mjs", import.meta.url));
const server = spawn(process.execPath, [serverPath], {
  env: process.env,
  stdio: "inherit",
});

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
const safeDiagnosticSymbol = Symbol("safeDiagnostic");
const safeErrorCodes = new Set([
  "ECONNREFUSED",
  "ECONNRESET",
  "EPIPE",
  "ETIMEDOUT",
]);

function errorKind(error) {
  const rawName = error && typeof error.name === "string" ? error.name : "Error";
  const name = /^[A-Za-z][A-Za-z0-9]{0,63}$/.test(rawName) ? rawName : "Error";
  const rawCode = error && typeof error.code === "string" ? error.code : "";
  return safeErrorCodes.has(rawCode) ? `${name}/${rawCode}` : name;
}

function readinessError(diagnostic) {
  const error = new Error("readiness verification failed");
  error[safeDiagnosticSymbol] = diagnostic;
  return error;
}

async function stopServer() {
  if (server.exitCode !== null || server.signalCode !== null) return;
  const exited = new Promise((resolve) => server.once("exit", resolve));
  server.kill("SIGTERM");
  await Promise.race([exited, sleep(5_000)]);
  if (server.exitCode === null && server.signalCode === null) {
    server.kill("SIGKILL");
    await exited;
  }
}

async function verifyFrontendProxy() {
  let lastError = "frontend did not respond";
  try {
    for (let attempt = 1; attempt <= attempts; attempt += 1) {
      if (server.exitCode !== null || server.signalCode !== null) {
        throw readinessError("frontend-server-exited");
      }
      const controller = new AbortController();
      const timer = setTimeout(() => controller.abort(), fetchTimeoutMs);
      try {
        const response = await fetch(readinessURL, {
          headers: {
            "x-forwarded-for": "127.0.0.1",
            "x-forwarded-host": "readiness.invalid",
            "x-forwarded-proto": "https",
          },
          signal: controller.signal,
        });
        let readiness = null;
        let readinessCategory = "invalid-json";
        try {
          readiness = await response.json();
          if (!readiness || typeof readiness !== "object" || Array.isArray(readiness)) {
            readinessCategory = "invalid-payload";
          } else if (readiness.status !== "ready") {
            readinessCategory = typeof readiness.status === "string"
              ? "non-ready-status"
              : "missing-status";
          } else if (readiness.api_image !== expectedAPIImage) {
            readinessCategory = "api-image-mismatch";
          } else {
            readinessCategory = "ready";
          }
        } catch {
          // HTTP status plus a categorical payload result is sufficient. Do
          // not copy response content from a deployed service into CI logs.
        }
        if (
          response.ok
          && readinessCategory === "ready"
        ) {
          console.log(`frontend proxy and backend release ${expectedAPIImage} verified`);
          return;
        }
        lastError = `HTTP ${response.status} (${readinessCategory})`;
      } catch (error) {
        lastError = `transport-${errorKind(error)}`;
      } finally {
        clearTimeout(timer);
      }
      if (attempt < attempts) await sleep(retryMs);
    }
    throw readinessError(lastError);
  } finally {
    await stopServer();
  }
}

verifyFrontendProxy().catch((error) => {
  const diagnostic = error && typeof error[safeDiagnosticSymbol] === "string"
    ? error[safeDiagnosticSymbol]
    : `internal-${errorKind(error)}`;
  console.error(`frontend readiness failed: ${diagnostic}`);
  process.exitCode = 1;
});
