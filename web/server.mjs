import http from "node:http";
import https from "node:https";
import path from "node:path";
import { createReadStream, promises as fs } from "node:fs";
import { performance } from "node:perf_hooks";
import { fileURLToPath } from "node:url";
import {
  establishForwardingHeaders,
  isLoopbackAddress,
  resolveForwardingIdentity,
  stripCredentialHeaders,
  stripCredentialResponseHeaders,
  stripHopByHopHeaders,
} from "./proxy-headers.mjs";
import {
  createUpstreamURL,
  isAllowedIIIFMethod,
  isIIIFPath,
  isPresentationPath,
} from "./proxy-url.mjs";
import {
  pipeUpstreamRequest,
  pipeUpstreamResponse,
} from "./proxy-pipeline.mjs";
import { pipeStaticResponse } from "./static-pipeline.mjs";

function positiveInteger(name, fallback) {
  const value = Number.parseInt(process.env[name] || "", 10);
  return Number.isSafeInteger(value) && value > 0 ? value : fallback;
}

function configuredPort(name, fallback) {
  const raw = (process.env[name] || "").trim();
  if (!raw) return fallback;
  if (!/^\d+$/.test(raw)) throw new Error(`${name} must be an integer between 0 and 65535`);
  const value = Number(raw);
  if (!Number.isSafeInteger(value) || value < 0 || value > 65_535) {
    throw new Error(`${name} must be an integer between 0 and 65535`);
  }
  return value;
}

const port = configuredPort("SCRIBE_FRONTEND_PORT", 8888);
const shutdownTimeoutMs = positiveInteger("SCRIBE_FRONTEND_SHUTDOWN_TIMEOUT_MS", 8_000);
const backendStartupTimeoutMs = positiveInteger("SCRIBE_FRONTEND_BACKEND_STARTUP_TIMEOUT_MS", 180_000);
const backendProbeTimeoutMs = positiveInteger("SCRIBE_FRONTEND_BACKEND_PROBE_TIMEOUT_MS", 2_000);
const backendProbeRetryMs = positiveInteger("SCRIBE_FRONTEND_BACKEND_PROBE_RETRY_MS", 1_000);
const backendReadyCacheMs = positiveInteger("SCRIBE_FRONTEND_BACKEND_READY_CACHE_MS", 10_000);
const edgeMode = (process.env.SCRIBE_FRONTEND_EDGE_MODE || "direct").trim();
if (edgeMode !== "direct" && edgeMode !== "ppb") {
  throw new Error("SCRIBE_FRONTEND_EDGE_MODE must be direct or ppb");
}
const activeStreams = new Set();
const activeSockets = new Set();
let shuttingDown = false;
let shutdownPromise = null;
let backendReadyUntil = 0;
let backendProbePromise = null;
let backendProbeController = null;
let canonicalPublicOrigin = null;

const safeErrorCodes = new Set([
  "EACCES",
  "EADDRINUSE",
  "EAI_AGAIN",
  "ECONNREFUSED",
  "ECONNRESET",
  "EHOSTUNREACH",
  "ENETUNREACH",
  "ENOTFOUND",
  "EPIPE",
  "ENOENT",
  "ETIMEDOUT",
  "ERR_STREAM_PREMATURE_CLOSE",
]);
const safeDiagnosticSymbol = Symbol("safeDiagnostic");
// Only an explicit non-ready status is transient. These shapes violate the
// frontend/backend contract, so retrying them would only delay a safe 503.
const readinessContractFailures = new Set([
  "invalid-json",
  "invalid-payload",
  "invalid-public-origin",
  "missing-status",
  "ready-payload-with-non-success-http",
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

function logFailure(category, error) {
  const diagnostic = error && typeof error[safeDiagnosticSymbol] === "string"
    ? `; ${error[safeDiagnosticSymbol]}`
    : "";
  console.error(`${category} [${errorKind(error)}${diagnostic}]`);
}

function safeDiagnosticError(diagnostic) {
  const error = new Error("operation failed");
  error[safeDiagnosticSymbol] = diagnostic;
  return error;
}

function trackActiveStream(stream) {
  activeStreams.add(stream);
  stream.once("close", () => activeStreams.delete(stream));
}
function parseConfiguredOrigin(raw, name) {
  if (!raw) return null;
  const parsed = new URL(raw);
  if (
    (parsed.protocol !== "http:" && parsed.protocol !== "https:")
    || parsed.username
    || parsed.password
    || parsed.pathname !== "/"
    || parsed.search
    || parsed.hash
  ) {
    throw new Error(`${name} must be an HTTP(S) origin without credentials or a path`);
  }
  return parsed;
}

const backendOriginRaw = (process.env.SCRIBE_FRONTEND_BACKEND_ORIGIN || "").trim();
const backendOrigin = parseConfiguredOrigin(backendOriginRaw, "SCRIBE_FRONTEND_BACKEND_ORIGIN");
const presentationOriginRaw = (process.env.SCRIBE_FRONTEND_PRESENTATION_ORIGIN || "").trim();
const presentationOrigin = parseConfiguredOrigin(
  presentationOriginRaw,
  "SCRIBE_FRONTEND_PRESENTATION_ORIGIN",
);
const rootDir = path.dirname(fileURLToPath(import.meta.url));
const distDir = path.join(rootDir, "dist");
const indexPath = path.join(distDir, "index.html");
const securityHeaders = {
  "content-security-policy": [
    "default-src 'self'",
    "script-src 'self'",
    "style-src 'self' 'unsafe-inline'",
    "img-src 'self' data: blob: http: https:",
    "font-src 'self' data:",
    // The editor gives Mirador an authorized private Manifest as an in-memory
    // object URL; Mirador loads that document through fetch.
    "connect-src 'self' blob: https:",
    "base-uri 'self'",
    "form-action 'self'",
    "frame-ancestors 'none'",
  ].join("; "),
  "x-content-type-options": "nosniff",
  "x-frame-options": "DENY",
  "referrer-policy": "strict-origin-when-cross-origin",
  "permissions-policy": "camera=(), microphone=(), geolocation=()",
  // PPB terminates external TLS but does not own this application's response
  // policy. Browsers ignore HSTS received over plain HTTP, so this remains
  // safe for direct local development.
  "strict-transport-security": "max-age=31536000; includeSubDomains",
};
const mimeTypes = new Map([
  [".css", "text/css; charset=utf-8"],
  [".gif", "image/gif"],
  [".html", "text/html; charset=utf-8"],
  [".ico", "image/x-icon"],
  [".jpg", "image/jpeg"],
  [".js", "text/javascript; charset=utf-8"],
  [".json", "application/json; charset=utf-8"],
  [".map", "application/json; charset=utf-8"],
  [".mjs", "text/javascript; charset=utf-8"],
  [".png", "image/png"],
  [".svg", "image/svg+xml"],
  [".txt", "text/plain; charset=utf-8"],
  [".wasm", "application/wasm"],
  [".webp", "image/webp"],
  [".woff", "font/woff"],
  [".woff2", "font/woff2"],
]);

const transparentBackendStatusPaths = new Set(["/livez", "/readyz"]);
const canonicalOriginExemptPaths = new Set(["/healthz", ...transparentBackendStatusPaths]);
const proxyMatchers = [
  (pathname) => pathname === "/healthz" || transparentBackendStatusPaths.has(pathname),
  (pathname) => pathname === "/logout" || pathname.startsWith("/logout/"),
  (pathname) => pathname === "/auth" || pathname.startsWith("/auth/"),
  (pathname) => pathname === "/v1" || pathname.startsWith("/v1/"),
  (pathname) => pathname.startsWith("/scribe.v1."),
  (pathname) => pathname === "/static/uploads" || pathname.startsWith("/static/uploads/"),
  isIIIFPath,
  isPresentationPath,
];

function isProxyPath(pathname) {
  return proxyMatchers.some((matches) => matches(pathname));
}

function safeDecodePathname(pathname) {
  try {
    return decodeURIComponent(pathname);
  } catch {
    return null;
  }
}

function responseHeaders(headers = {}) {
  // Upstreams may add content headers, but they cannot weaken the frontend's
  // browser security policy. Preserve the backend's stricter no-referrer
  // response on one-time credential redemption so a same-origin redirect
  // cannot copy a bearer token from the source URL into Referer.
  const merged = { ...headers, ...securityHeaders };
  const upstreamReferrerPolicy = headers["referrer-policy"];
  const upstreamPolicies = Array.isArray(upstreamReferrerPolicy)
    ? upstreamReferrerPolicy
    : [upstreamReferrerPolicy];
  if (upstreamPolicies.some((value) => (
    typeof value === "string" && value.trim().toLowerCase() === "no-referrer"
  ))) {
    merged["referrer-policy"] = "no-referrer";
  }
  return merged;
}

function targetOriginForPath(pathname) {
  if (isPresentationPath(pathname)) {
    return presentationOrigin || backendOrigin;
  }
  return backendOrigin;
}

function abortError(message) {
  const error = new Error(message);
  error.name = "AbortError";
  return error;
}

function abortableSleep(ms, signal) {
  return new Promise((resolve, reject) => {
    if (signal.aborted) {
      reject(signal.reason || abortError("backend readiness wait aborted"));
      return;
    }
    const timer = setTimeout(finish, ms);
    const onAbort = () => finish(signal.reason || abortError("backend readiness wait aborted"));
    function finish(error = null) {
      clearTimeout(timer);
      signal.removeEventListener("abort", onAbort);
      if (error) reject(error);
      else resolve();
    }
    signal.addEventListener("abort", onAbort, { once: true });
  });
}

async function probeBackendUntilReady(signal) {
  const readinessURL = new URL("/readyz", backendOrigin);
  const deadline = performance.now() + backendStartupTimeoutMs;
  let lastError = "backend did not report ready";

  while (!signal.aborted && !shuttingDown && performance.now() < deadline) {
    const probeController = new AbortController();
    const abortProbe = () => probeController.abort(signal.reason || abortError("backend readiness wait aborted"));
    signal.addEventListener("abort", abortProbe, { once: true });
    const remainingMs = Math.max(1, deadline - performance.now());
    const timer = setTimeout(
      () => probeController.abort(abortError("backend readiness probe timed out")),
      Math.min(backendProbeTimeoutMs, remainingMs),
    );
    try {
      const response = await fetch(readinessURL, {
        cache: "no-store",
        headers: { accept: "application/json" },
        signal: probeController.signal,
      });
      let readiness = null;
      let readinessCategory = "invalid-json";
      let reportedPublicOrigin = null;
      try {
        readiness = await response.json();
        if (!readiness || typeof readiness !== "object" || Array.isArray(readiness)) {
          readinessCategory = "invalid-payload";
        } else if (readiness.status === "ready") {
          const hasPublicOrigin = Object.hasOwn(readiness, "public_origin");
          if (hasPublicOrigin) {
            const rawPublicOrigin = typeof readiness.public_origin === "string"
              ? readiness.public_origin.trim()
              : "";
            try {
              reportedPublicOrigin = parseConfiguredOrigin(
                rawPublicOrigin,
                "backend readiness public_origin",
              );
            } catch {
              readinessCategory = "invalid-public-origin";
            }
            if (!reportedPublicOrigin) {
              readinessCategory = "invalid-public-origin";
            }
          }
          if (
            readinessCategory !== "invalid-public-origin"
            && reportedPublicOrigin
            && edgeMode === "ppb"
            && reportedPublicOrigin.protocol !== "https:"
          ) {
            readinessCategory = "invalid-public-origin";
          } else if (readinessCategory !== "invalid-public-origin") {
            readinessCategory = response.ok
              ? "ready"
              : "ready-payload-with-non-success-http";
          }
        } else if (typeof readiness.status === "string") {
          readinessCategory = "non-ready-status";
        } else {
          readinessCategory = "missing-status";
        }
      } catch {
        // Status plus a categorical payload result is enough for diagnostics;
        // readiness response content may contain upstream implementation data.
      }
      if (response.ok && readinessCategory === "ready") {
        // The immediately previous API revision did not report public_origin.
        // Keep routes available while Terraform replaces the VM and Cloud Run
        // revision independently, but disable redirects until the new API is
        // observed. The post-apply readiness job requires the exact origin, so
        // a deployment cannot pass while this compatibility state remains.
        canonicalPublicOrigin = reportedPublicOrigin;
        backendReadyUntil = performance.now() + backendReadyCacheMs;
        return;
      }
      lastError = `HTTP ${response.status} (${readinessCategory})`;
      if (readinessContractFailures.has(readinessCategory)) {
        throw safeDiagnosticError(`readiness-contract; ${lastError}`);
      }
    } catch (error) {
      if (signal.aborted || shuttingDown) throw signal.reason || abortError("backend readiness wait aborted");
      if (error && typeof error[safeDiagnosticSymbol] === "string") throw error;
      lastError = `transport-${errorKind(error)}`;
    } finally {
      clearTimeout(timer);
      signal.removeEventListener("abort", abortProbe);
    }

    const remainingAfterProbe = deadline - performance.now();
    if (remainingAfterProbe > 0) {
      await abortableSleep(Math.min(backendProbeRetryMs, remainingAfterProbe), signal);
    }
  }

  if (signal.aborted || shuttingDown) {
    throw signal.reason || abortError("backend readiness wait aborted");
  }
  throw safeDiagnosticError(`startup-deadline; ${lastError}`);
}

function waitForSharedProbe(promise, signal) {
  if (!signal) return promise;
  if (signal.aborted) return Promise.reject(signal.reason || abortError("client disconnected"));
  return new Promise((resolve, reject) => {
    const onAbort = () => reject(signal.reason || abortError("client disconnected"));
    signal.addEventListener("abort", onAbort, { once: true });
    promise.then(resolve, reject).finally(() => signal.removeEventListener("abort", onAbort));
  });
}

function waitForBackendReady(signal) {
  if (!backendOrigin || performance.now() < backendReadyUntil) return Promise.resolve();
  if (!backendProbePromise) {
    backendProbeController = new AbortController();
    const currentProbe = probeBackendUntilReady(backendProbeController.signal);
    const wrappedProbe = currentProbe.finally(() => {
      if (backendProbePromise === wrappedProbe) {
        backendProbePromise = null;
        backendProbeController = null;
      }
    });
    backendProbePromise = wrappedProbe;
  }
  return waitForSharedProbe(backendProbePromise, signal);
}

function invalidateBackendReadiness() {
  backendReadyUntil = 0;
}

function requestForwardingIdentity(req, headers) {
  const requestHost = Array.isArray(req.headers.host) ? req.headers.host.at(-1) : req.headers.host;
  return resolveForwardingIdentity(headers, {
    edgeMode,
    encrypted: Boolean(req.socket.encrypted),
    remoteAddress: req.socket.remoteAddress || "",
    requestHost: requestHost || "",
  });
}

async function waitForRequestBackend(req, res) {
  const waitController = new AbortController();
  const abortWait = () => waitController.abort(abortError("client disconnected during backend startup"));
  req.once("aborted", abortWait);
  res.once("close", abortWait);
  try {
    await waitForBackendReady(waitController.signal);
    return true;
  } catch (error) {
    if (waitController.signal.aborted || req.destroyed || res.destroyed) return false;
    logFailure("frontend backend startup gate failed", error);
    res.writeHead(503, responseHeaders({
      "content-type": "text/plain; charset=utf-8",
      "retry-after": "2",
    }));
    res.end("backend is still starting");
    return false;
  } finally {
    req.off("aborted", abortWait);
    res.off("close", abortWait);
  }
}

async function enforceCanonicalRequestOrigin(req, res, requestURL, pathname) {
  if (
    edgeMode !== "ppb"
    || !backendOrigin
    || canonicalOriginExemptPaths.has(pathname)
  ) {
    return { continueRequest: true, forwardingIdentity: null };
  }

  let forwardingIdentity;
  try {
    forwardingIdentity = requestForwardingIdentity(req, stripHopByHopHeaders(req.headers));
  } catch (error) {
    logFailure("frontend rejected invalid edge forwarding identity", error);
    res.writeHead(502, responseHeaders({ "content-type": "text/plain; charset=utf-8" }));
    res.end("frontend edge forwarding identity is invalid");
    return { continueRequest: false, forwardingIdentity: null };
  }

  if (!await waitForRequestBackend(req, res)) {
    return { continueRequest: false, forwardingIdentity: null };
  }
  if (!canonicalPublicOrigin) {
    return { continueRequest: true, forwardingIdentity };
  }

  const requestOrigin = new URL(
    `${forwardingIdentity.proto}://${forwardingIdentity.host}`,
  ).origin;
  if (requestOrigin === canonicalPublicOrigin.origin) {
    return { continueRequest: true, forwardingIdentity };
  }

  if (req.method === "GET" || req.method === "HEAD") {
    res.writeHead(308, responseHeaders({
      "cache-control": "no-store",
      location: `${canonicalPublicOrigin.origin}${requestURL.pathname}${requestURL.search}`,
    }));
    res.end();
  } else {
    // Redirecting a request with a body can replay a mutation against another
    // authority. Make the browser navigate to the canonical app first.
    res.writeHead(421, responseHeaders({ "content-type": "text/plain; charset=utf-8" }));
    res.end("request must use the canonical application origin");
  }
  return { continueRequest: false, forwardingIdentity: null };
}

async function proxyRequest(req, res, pathname, establishedForwardingIdentity = null) {
  const targetOrigin = targetOriginForPath(pathname);
  if (!targetOrigin) {
    res.writeHead(502, responseHeaders({ "content-type": "text/plain; charset=utf-8" }));
    res.end("frontend upstream origin is not configured");
    return;
  }

  const usesBackendOrigin = Boolean(backendOrigin && targetOrigin.origin === backendOrigin.origin);
  if (usesBackendOrigin && !transparentBackendStatusPaths.has(pathname)) {
    if (!await waitForRequestBackend(req, res)) return;
  }

  const targetURL = createUpstreamURL(targetOrigin, req.url || "/");
  const client = targetURL.protocol === "https:" ? https : http;
  const isSeparateOrigin = Boolean(!backendOrigin || targetOrigin.origin !== backendOrigin.origin);
  // Presentation is a public Triplet boundary even when Traefik shares the
  // backend authority. Never disclose browser session/API credentials to it,
  // and never let it mint credentials for the application origin. Public
  // status routes are also credential-free so liveness cannot trigger
  // authentication work or depend on browser identity.
  const isPublicPresentation = isPresentationPath(pathname);
  const isStatusPath = canonicalOriginExemptPaths.has(pathname);
  const stripsCredentials = isSeparateOrigin || isPublicPresentation || isStatusPath;
  const incomingHeaders = stripsCredentials
    ? stripCredentialHeaders(stripHopByHopHeaders(req.headers))
    : stripHopByHopHeaders(req.headers);
  let forwardingIdentity = establishedForwardingIdentity;
  const internalPPBStatusProbe = isStatusPath
    && edgeMode === "ppb"
    && isLoopbackAddress(req.socket.remoteAddress || "")
    && incomingHeaders["x-forwarded-for"] === undefined
    && incomingHeaders["x-forwarded-host"] === undefined
    && incomingHeaders["x-forwarded-proto"] === undefined;
  if (!internalPPBStatusProbe) {
    try {
      forwardingIdentity ||= requestForwardingIdentity(req, incomingHeaders);
    } catch (error) {
      logFailure("frontend rejected invalid edge forwarding identity", error);
      res.writeHead(502, responseHeaders({ "content-type": "text/plain; charset=utf-8" }));
      res.end("frontend edge forwarding identity is invalid");
      return;
    }
  }
  const headers = establishForwardingHeaders(incomingHeaders, forwardingIdentity || {});

  headers.host = targetURL.host;
  const upstream = client.request(
    {
      protocol: targetURL.protocol,
      hostname: targetURL.hostname,
      port: targetURL.port || (targetURL.protocol === "https:" ? 443 : 80),
      method: req.method,
      path: `${targetURL.pathname}${targetURL.search}`,
      headers,
    },
    (upstreamRes) => {
      trackActiveStream(upstreamRes);
      const upstreamHeaders = stripHopByHopHeaders(upstreamRes.headers);
      const headersForBrowser = stripsCredentials
        ? stripCredentialResponseHeaders(upstreamHeaders)
        : upstreamHeaders;
      res.writeHead(upstreamRes.statusCode || 502, responseHeaders(headersForBrowser));
      pipeUpstreamResponse(upstreamRes, res, (error) => {
        logFailure("frontend proxy response failed", error);
        if (!res.destroyed) res.destroy(error);
      });
    },
  );
  trackActiveStream(upstream);

  upstream.on("error", (error) => {
    logFailure("frontend proxy request failed", error);
    if (usesBackendOrigin) invalidateBackendReadiness();
    if (res.destroyed) return;
    if (!res.headersSent) {
      res.writeHead(502, responseHeaders({ "content-type": "text/plain; charset=utf-8" }));
    }
    res.end("upstream request failed");
  });
  upstream.setTimeout(120_000, () => upstream.destroy(new Error("upstream request timed out")));
  req.on("aborted", () => upstream.destroy());
  res.on("close", () => {
    if (!upstream.destroyed) upstream.destroy();
  });

  pipeUpstreamRequest(req, upstream, (error) => {
    logFailure("frontend proxy upload failed", error);
    upstream.destroy(error);
  });
}

async function serveFile(req, res, filePath) {
  const ext = path.extname(filePath).toLowerCase();
  const contentType = mimeTypes.get(ext) || "application/octet-stream";
  const stat = await fs.stat(filePath);
  const headers = {
    "content-length": stat.size,
    "content-type": contentType,
  };
  if (filePath !== indexPath && filePath.startsWith(path.join(distDir, "assets") + path.sep)) {
    headers["cache-control"] = "public, max-age=31536000, immutable";
  } else {
    headers["cache-control"] = "no-store";
  }
  res.writeHead(200, responseHeaders(headers));
  if (req.method === "HEAD") {
    res.end();
    return;
  }
  const source = createReadStream(filePath);
  trackActiveStream(source);
  await pipeStaticResponse(source, res);
}

async function resolveStaticPath(pathname) {
  if (pathname === "/") {
    return null;
  }

  const relativePath = pathname.replace(/^\/+/, "");
  const targetPath = path.resolve(distDir, relativePath);
  if (!targetPath.startsWith(distDir + path.sep)) {
    return null;
  }

  try {
    const stat = await fs.stat(targetPath);
    if (stat.isFile()) {
      return targetPath;
    }
  } catch {
    return null;
  }
  return null;
}

const server = http.createServer(async (req, res) => {
  if (shuttingDown) {
    res.writeHead(503, responseHeaders({
      connection: "close",
      "content-type": "text/plain; charset=utf-8",
      "retry-after": "1",
    }));
    res.end("frontend is shutting down");
    return;
  }
  const method = req.method || "GET";
  const url = new URL(req.url || "/", "http://frontend.local");
  const pathname = safeDecodePathname(url.pathname);
  if (pathname === null) {
    res.writeHead(400, responseHeaders({ "content-type": "text/plain; charset=utf-8" }));
    res.end("invalid request path");
    return;
  }

  const canonicalRequest = await enforceCanonicalRequestOrigin(req, res, url, pathname);
  if (!canonicalRequest.continueRequest) return;

  if ((isIIIFPath(pathname) || isPresentationPath(pathname)) && !isAllowedIIIFMethod(method)) {
    res.writeHead(405, responseHeaders({ allow: "GET, HEAD, OPTIONS" }));
    res.end();
    return;
  }

  if (method !== "GET" && method !== "HEAD" && isProxyPath(pathname)) {
    await proxyRequest(req, res, pathname, canonicalRequest.forwardingIdentity);
    return;
  }

  if (isProxyPath(pathname)) {
    await proxyRequest(req, res, pathname, canonicalRequest.forwardingIdentity);
    return;
  }

  if (method !== "GET" && method !== "HEAD") {
    res.writeHead(405, responseHeaders({ allow: "GET, HEAD" }));
    res.end();
    return;
  }

  try {
    const assetPath = await resolveStaticPath(pathname);
    if (assetPath) {
      await serveFile(req, res, assetPath);
      return;
    }
    await serveFile(req, res, indexPath);
  } catch (error) {
    logFailure("frontend request failed", error);
    if (res.destroyed) return;
    if (!res.headersSent) {
      res.writeHead(500, responseHeaders({ "content-type": "text/plain; charset=utf-8" }));
      res.end("frontend request failed");
    } else {
      res.destroy(error);
    }
  }
});

server.headersTimeout = 15_000;
server.requestTimeout = backendStartupTimeoutMs + 120_000;
server.keepAliveTimeout = 60_000;

server.on("connection", (socket) => {
  activeSockets.add(socket);
  socket.once("close", () => activeSockets.delete(socket));
});

server.on("error", (error) => {
  logFailure("frontend server error", error);
  if (!server.listening) process.exitCode = 1;
});

function stopServer(signal) {
  if (shutdownPromise) return shutdownPromise;
  shuttingDown = true;
  backendProbeController?.abort(abortError("frontend is shutting down"));
  console.log(`frontend received ${signal}; draining connections`);

  shutdownPromise = new Promise((resolve) => {
    let settled = false;
    const finish = (forced, error = null) => {
      if (settled) return;
      settled = true;
      clearTimeout(forceTimer);
      if (error) {
        logFailure("frontend shutdown failed", error);
        process.exitCode = 1;
      } else if (forced) {
        console.log("frontend shutdown forced after drain timeout");
      } else {
        console.log("frontend shutdown complete");
      }
      resolve();
    };
    const forceTimer = setTimeout(() => {
      for (const stream of activeStreams) {
        if (!stream.destroyed) stream.destroy();
      }
      server.closeAllConnections?.();
      for (const socket of activeSockets) socket.destroy();
      finish(true);
    }, shutdownTimeoutMs);
    forceTimer.unref?.();

    server.close((error) => finish(false, error));
    server.closeIdleConnections?.();
  });

  return shutdownPromise;
}

for (const signal of ["SIGTERM", "SIGINT"]) {
  process.once(signal, () => {
    stopServer(signal).then(() => {
      setImmediate(() => process.exit(process.exitCode || 0));
    });
  });
}

server.listen(port, "0.0.0.0", () => {
  const address = server.address();
  const listeningPort = address && typeof address !== "string" ? address.port : port;
  console.log(`frontend listening on :${listeningPort}`);
  if (backendOrigin) {
    console.log(`frontend proxying backend paths to ${backendOrigin.toString()}`);
  }
  if (presentationOrigin) {
    console.log(`frontend proxying Presentation paths to ${presentationOrigin.toString()}`);
  }
});
