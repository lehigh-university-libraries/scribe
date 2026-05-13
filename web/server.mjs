import http from "node:http";
import https from "node:https";
import path from "node:path";
import { createReadStream, promises as fs } from "node:fs";
import { fileURLToPath } from "node:url";

const port = 8888;
const backendOriginRaw = (process.env.SCRIBE_FRONTEND_BACKEND_ORIGIN || "").trim();
const backendOrigin = backendOriginRaw ? new URL(backendOriginRaw) : null;
const iiifOriginRaw = (process.env.SCRIBE_FRONTEND_IIIF_ORIGIN || "").trim();
const iiifOrigin = iiifOriginRaw ? new URL(iiifOriginRaw) : null;
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
    "connect-src 'self' http: https:",
    "base-uri 'self'",
    "form-action 'self'",
    "frame-ancestors 'none'",
  ].join("; "),
  "x-content-type-options": "nosniff",
  "x-frame-options": "DENY",
  "referrer-policy": "strict-origin-when-cross-origin",
  "permissions-policy": "camera=(), microphone=(), geolocation=()",
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

const proxyMatchers = [
  (pathname) => pathname === "/healthz",
  (pathname) => pathname === "/logout" || pathname.startsWith("/logout/"),
  (pathname) => pathname === "/auth" || pathname.startsWith("/auth/"),
  (pathname) => pathname === "/v1" || pathname.startsWith("/v1/"),
  (pathname) => pathname.startsWith("/scribe.v1."),
  (pathname) => pathname === "/static/uploads" || pathname.startsWith("/static/uploads/"),
  (pathname) => pathname === "/iiif" || pathname.startsWith("/iiif/"),
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
  return { ...securityHeaders, ...headers };
}

function stripHopByHopHeaders(headers) {
  const filtered = { ...headers };
  for (const name of [
    "connection",
    "keep-alive",
    "proxy-authenticate",
    "proxy-authorization",
    "te",
    "trailers",
    "transfer-encoding",
    "upgrade",
  ]) {
    delete filtered[name];
  }
  return filtered;
}

function stripCredentialHeaders(headers) {
  const filtered = { ...headers };
  for (const name of [
    "authorization",
    "cookie",
    "x-scribe-api-key",
    "x-scribe-workspace-id",
  ]) {
    delete filtered[name];
  }
  return filtered;
}

function appendForwardedFor(existing, remoteAddress) {
  if (!remoteAddress) {
    return existing || "";
  }
  if (!existing) {
    return remoteAddress;
  }
  return `${existing}, ${remoteAddress}`;
}

function targetOriginForPath(pathname) {
  if (pathname === "/iiif" || pathname.startsWith("/iiif/")) {
    return iiifOrigin || backendOrigin;
  }
  return backendOrigin;
}

function proxyRequest(req, res, pathname) {
  const url = new URL(req.url || "/", "http://frontend.local");
  const targetOrigin = targetOriginForPath(pathname);
  if (!targetOrigin) {
    res.writeHead(502, responseHeaders({ "content-type": "text/plain; charset=utf-8" }));
    res.end("frontend upstream origin is not configured");
    return;
  }

  const targetURL = new URL(req.url || "/", targetOrigin);
  const client = targetURL.protocol === "https:" ? https : http;
  const isSeparateIIIFOrigin = Boolean(iiifOrigin && backendOrigin && targetOrigin.origin !== backendOrigin.origin);
  const headers = isSeparateIIIFOrigin
    ? stripCredentialHeaders(stripHopByHopHeaders(req.headers))
    : stripHopByHopHeaders(req.headers);
  const forwardedProto = req.headers["x-forwarded-proto"] || (req.socket.encrypted ? "https" : "http");

  headers.host = targetURL.host;
  headers["x-forwarded-host"] = req.headers["x-forwarded-host"] || req.headers.host || "";
  headers["x-forwarded-proto"] = Array.isArray(forwardedProto) ? forwardedProto.join(",") : forwardedProto;
  headers["x-forwarded-for"] = appendForwardedFor(req.headers["x-forwarded-for"], req.socket.remoteAddress || "");

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
      res.writeHead(upstreamRes.statusCode || 502, responseHeaders(stripHopByHopHeaders(upstreamRes.headers)));
      upstreamRes.pipe(res);
    },
  );

  upstream.on("error", (error) => {
    console.error("frontend proxy request failed", error);
    if (!res.headersSent) {
      res.writeHead(502, responseHeaders({ "content-type": "text/plain; charset=utf-8" }));
    }
    res.end("upstream request failed");
  });

  req.pipe(upstream);
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
  createReadStream(filePath).pipe(res);
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
  const method = req.method || "GET";
  const url = new URL(req.url || "/", "http://frontend.local");
  const pathname = safeDecodePathname(url.pathname);
  if (pathname === null) {
    res.writeHead(400, responseHeaders({ "content-type": "text/plain; charset=utf-8" }));
    res.end("invalid request path");
    return;
  }

  if (method !== "GET" && method !== "HEAD" && isProxyPath(pathname)) {
    proxyRequest(req, res, pathname);
    return;
  }

  if (isProxyPath(pathname)) {
    proxyRequest(req, res, pathname);
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
    console.error("frontend request failed", error);
    res.writeHead(500, responseHeaders({ "content-type": "text/plain; charset=utf-8" }));
    res.end("frontend request failed");
  }
});

server.headersTimeout = 15_000;
server.requestTimeout = 120_000;
server.keepAliveTimeout = 60_000;

server.listen(port, "0.0.0.0", () => {
  console.log(`frontend listening on :${port}`);
  if (backendOrigin) {
    console.log(`frontend proxying backend paths to ${backendOrigin.toString()}`);
  }
  if (iiifOrigin) {
    console.log(`frontend proxying IIIF paths to ${iiifOrigin.toString()}`);
  }
});
