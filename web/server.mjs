import http from "node:http";
import https from "node:https";
import path from "node:path";
import { createReadStream, promises as fs } from "node:fs";
import { fileURLToPath } from "node:url";

const port = 8888;
const backendOriginRaw = (process.env.SCRIBE_FRONTEND_BACKEND_ORIGIN || "").trim();
const backendOrigin = backendOriginRaw ? new URL(backendOriginRaw) : null;
const rootDir = path.dirname(fileURLToPath(import.meta.url));
const distDir = path.join(rootDir, "dist");
const indexPath = path.join(distDir, "index.html");
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
  (pathname) => pathname === "/cantaloupe" || pathname.startsWith("/cantaloupe/"),
];

function isProxyPath(pathname) {
  return proxyMatchers.some((matches) => matches(pathname));
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

function appendForwardedFor(existing, remoteAddress) {
  if (!remoteAddress) {
    return existing || "";
  }
  if (!existing) {
    return remoteAddress;
  }
  return `${existing}, ${remoteAddress}`;
}

function proxyRequest(req, res) {
  if (!backendOrigin) {
    res.writeHead(502, { "content-type": "text/plain; charset=utf-8" });
    res.end("frontend backend origin is not configured");
    return;
  }

  const targetURL = new URL(req.url || "/", backendOrigin);
  const client = targetURL.protocol === "https:" ? https : http;
  const headers = stripHopByHopHeaders(req.headers);
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
      res.writeHead(upstreamRes.statusCode || 502, stripHopByHopHeaders(upstreamRes.headers));
      upstreamRes.pipe(res);
    },
  );

  upstream.on("error", (error) => {
    console.error("frontend proxy request failed", error);
    if (!res.headersSent) {
      res.writeHead(502, { "content-type": "text/plain; charset=utf-8" });
    }
    res.end("upstream request failed");
  });

  req.pipe(upstream);
}

async function serveFile(req, res, filePath) {
  const ext = path.extname(filePath).toLowerCase();
  const contentType = mimeTypes.get(ext) || "application/octet-stream";
  const stat = await fs.stat(filePath);
  res.writeHead(200, {
    "content-length": stat.size,
    "content-type": contentType,
  });
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
  const pathname = decodeURIComponent(url.pathname);

  if (method !== "GET" && method !== "HEAD" && isProxyPath(pathname)) {
    proxyRequest(req, res);
    return;
  }

  if (isProxyPath(pathname)) {
    proxyRequest(req, res);
    return;
  }

  if (method !== "GET" && method !== "HEAD") {
    res.writeHead(405, { allow: "GET, HEAD" });
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
    res.writeHead(500, { "content-type": "text/plain; charset=utf-8" });
    res.end("frontend request failed");
  }
});

server.listen(port, "0.0.0.0", () => {
  console.log(`frontend listening on :${port}`);
  if (backendOrigin) {
    console.log(`frontend proxying backend paths to ${backendOrigin.toString()}`);
  }
});
