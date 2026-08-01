import { spawn } from "node:child_process";
import { lookup } from "node:dns/promises";
import { request as httpRequest } from "node:http";
import { connect, isIP } from "node:net";
import { fileURLToPath } from "node:url";

function positiveInteger(name, fallback) {
  const value = Number.parseInt(process.env[name] || "", 10);
  return Number.isSafeInteger(value) && value > 0 ? value : fallback;
}

function exactHTTPSOrigin(raw) {
  try {
    const parsed = new URL(raw);
    if (
      parsed.protocol !== "https:"
      || parsed.username
      || parsed.password
      || parsed.pathname !== "/"
      || parsed.search
      || parsed.hash
      || parsed.origin !== raw
    ) {
      return null;
    }
    return parsed.origin;
  } catch {
    return null;
  }
}

const attempts = positiveInteger("SCRIBE_READINESS_ATTEMPTS", 30);
const fetchTimeoutMs = positiveInteger("SCRIBE_READINESS_FETCH_TIMEOUT_MS", 5_000);
const retryMs = positiveInteger("SCRIBE_READINESS_RETRY_MS", 3_000);
const networkProbeTimeoutMs = 5_000;
const networkHTTPMaxResponseBytes = 4_096;
const expectedAPIImage = (process.env.SCRIBE_EXPECTED_API_IMAGE || "").trim();
if (!/^[^\s@]+@sha256:[0-9a-f]{64}$/.test(expectedAPIImage)) {
  throw new Error("SCRIBE_EXPECTED_API_IMAGE must be a digest-pinned image reference");
}
const expectedPublicOriginRaw = (process.env.SCRIBE_EXPECTED_PUBLIC_ORIGIN || "").trim();
// Protected pull-request previews intentionally run their cloud orchestration
// from main while testing the head image. During the one rollout that adds
// this input, the trusted base cannot supply it yet. The response must still
// contain an exact HTTPS origin below; upgraded orchestration also pins it to
// the deterministic Terraform value.
const expectedPublicOrigin = expectedPublicOriginRaw
  ? exactHTTPSOrigin(expectedPublicOriginRaw)
  : null;
if (expectedPublicOriginRaw && !expectedPublicOrigin) {
  throw new Error("SCRIBE_EXPECTED_PUBLIC_ORIGIN must be an exact HTTPS origin");
}
const expectedBackendIP = (process.env.SCRIBE_EXPECTED_BACKEND_IP || "").trim();
if (isIP(expectedBackendIP) !== 4) {
  throw new Error("SCRIBE_EXPECTED_BACKEND_IP must be an IPv4 address");
}
const frontendPort = positiveInteger("SCRIBE_FRONTEND_PORT", 8888);
const readinessURL = `http://127.0.0.1:${frontendPort}/healthz`;
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
  "EAI_AGAIN",
  "EHOSTUNREACH",
  "ENETUNREACH",
  "ENOTFOUND",
  "EPIPE",
  "ETIMEDOUT",
]);

function errorKind(error) {
  const rawName = error && typeof error.name === "string" ? error.name : "Error";
  const name = /^[A-Za-z][A-Za-z0-9]{0,63}$/.test(rawName) ? rawName : "Error";
  const rawCode = error && typeof error.code === "string" ? error.code : "";
  const rawCauseCode = error
    && error.cause
    && typeof error.cause.code === "string"
    ? error.cause.code
    : "";
  const code = safeErrorCodes.has(rawCode)
    ? rawCode
    : (safeErrorCodes.has(rawCauseCode) ? rawCauseCode : "");
  return code ? `${name}/${code}` : name;
}

function readinessError(diagnostic) {
  const error = new Error("readiness verification failed");
  error[safeDiagnosticSymbol] = diagnostic;
  return error;
}

function configuredBackendTarget() {
  const raw = (process.env.SCRIBE_FRONTEND_BACKEND_ORIGIN || "").trim();
  try {
    const origin = new URL(raw);
    if (
      origin.protocol !== "http:"
      || origin.username
      || origin.password
      || origin.pathname !== "/"
      || origin.search
      || origin.hash
      || !origin.hostname
    ) {
      return null;
    }
    const port = origin.port ? Number(origin.port) : 80;
    if (!Number.isSafeInteger(port) || port < 1 || port > 65_535) return null;
    return { hostname: origin.hostname, hostHeader: origin.host, port };
  } catch {
    return null;
  }
}

async function probeBackendDNS(hostname) {
  const literalFamily = isIP(hostname);
  if (literalFamily !== 0) {
    return literalFamily === 4 && hostname === expectedBackendIP
      ? "dns-match"
      : "dns-mismatch";
  }

  const timeout = Symbol("dns-timeout");
  let timer;
  try {
    const addresses = await Promise.race([
      lookup(hostname, { all: true, family: 4 }),
      new Promise((resolve) => {
        timer = setTimeout(() => resolve(timeout), networkProbeTimeoutMs);
      }),
    ]);
    if (addresses === timeout) return "dns-timeout";
    if (addresses.length === 0) return "dns-empty";
    return addresses.some(({ address }) => address === expectedBackendIP)
      ? "dns-match"
      : "dns-mismatch";
  } catch {
    return "dns-error";
  } finally {
    clearTimeout(timer);
  }
}

function probeExpectedBackendTCP(port) {
  return new Promise((resolve) => {
    let settled = false;
    const socket = connect({
      host: expectedBackendIP,
      port,
      family: 4,
    });
    const finish = (category) => {
      if (settled) return;
      settled = true;
      socket.destroy();
      resolve(category);
    };
    socket.setTimeout(networkProbeTimeoutMs);
    socket.once("connect", () => finish("tcp-open"));
    socket.once("timeout", () => finish("tcp-timeout"));
    socket.once("error", (error) => {
      if (error && error.code === "ECONNREFUSED") {
        finish("tcp-refused");
      } else if (error && error.code === "ETIMEDOUT") {
        finish("tcp-timeout");
      } else if (
        error
        && (error.code === "EHOSTUNREACH" || error.code === "ENETUNREACH")
      ) {
        finish("tcp-unreachable");
      } else {
        finish("tcp-error");
      }
    });
  });
}

function httpTransportCategory(error) {
  const rawCode = error && typeof error.code === "string" ? error.code : "";
  const rawCauseCode = error
    && error.cause
    && typeof error.cause.code === "string"
    ? error.cause.code
    : "";
  const code = safeErrorCodes.has(rawCode)
    ? rawCode
    : (safeErrorCodes.has(rawCauseCode) ? rawCauseCode : "");
  return code ? `http-transport-${code}` : "http-transport-error";
}

function probeExpectedBackendHTTP(target) {
  return new Promise((resolve) => {
    let settled = false;
    let request = null;
    let response = null;
    const timer = setTimeout(() => finish("http-timeout"), networkProbeTimeoutMs);
    const finish = (category) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      response?.destroy();
      request?.destroy();
      resolve(category);
    };

    request = httpRequest({
      host: expectedBackendIP,
      port: target.port,
      method: "GET",
      path: "/readyz",
      agent: false,
      headers: {
        accept: "application/json",
        connection: "close",
        host: target.hostHeader,
      },
    }, (incoming) => {
      response = incoming;
      const chunks = [];
      let responseBytes = 0;
      incoming.on("data", (chunk) => {
        responseBytes += chunk.length;
        if (responseBytes > networkHTTPMaxResponseBytes) {
          finish("http-invalid");
          return;
        }
        chunks.push(chunk);
      });
      incoming.once("aborted", () => finish("http-transport-ERR_STREAM_PREMATURE_CLOSE"));
      incoming.once("error", (error) => finish(httpTransportCategory(error)));
      incoming.once("end", () => {
        if (settled) return;
        if (
          !Number.isInteger(incoming.statusCode)
          || incoming.statusCode < 200
          || incoming.statusCode >= 300
        ) {
          finish("http-error");
          return;
        }
        try {
          const readiness = JSON.parse(Buffer.concat(chunks).toString("utf8"));
          if (!readiness || typeof readiness !== "object" || Array.isArray(readiness)) {
            finish("http-invalid");
          } else if (readiness.status === "ready") {
            finish("http-ready");
          } else if (typeof readiness.status === "string") {
            finish("http-non-ready");
          } else {
            finish("http-invalid");
          }
        } catch {
          finish("http-invalid");
        }
      });
    });
    request.once("error", (error) => finish(httpTransportCategory(error)));
    request.end();
  });
}

async function backendNetworkMarker() {
  const target = configuredBackendTarget();
  if (!target) {
    return "frontend backend network probe [dns-invalid-origin; tcp-skipped; http-skipped]";
  }
  const [dnsResult, tcpResult, httpResult] = await Promise.allSettled([
    probeBackendDNS(target.hostname),
    probeExpectedBackendTCP(target.port),
    probeExpectedBackendHTTP(target),
  ]);
  const dnsCategory = dnsResult.status === "fulfilled" ? dnsResult.value : "dns-error";
  const tcpCategory = tcpResult.status === "fulfilled" ? tcpResult.value : "tcp-error";
  const httpCategory = httpResult.status === "fulfilled"
    ? httpResult.value
    : "http-transport-error";
  return `frontend backend network probe [${dnsCategory}; ${tcpCategory}; ${httpCategory}]`;
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
          redirect: "error",
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
            const reportedPublicOrigin = typeof readiness.public_origin === "string"
              ? exactHTTPSOrigin(readiness.public_origin)
              : null;
            if (
              !reportedPublicOrigin
              || (expectedPublicOrigin && reportedPublicOrigin !== expectedPublicOrigin)
            ) {
              readinessCategory = "public-origin-mismatch";
            } else {
              readinessCategory = "ready";
            }
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

verifyFrontendProxy().catch(async (error) => {
  const diagnostic = error && typeof error[safeDiagnosticSymbol] === "string"
    ? error[safeDiagnosticSymbol]
    : `internal-${errorKind(error)}`;
  console.error(`frontend readiness failed: ${diagnostic}`);
  console.error(await backendNetworkMarker());
  process.exitCode = 1;
});
