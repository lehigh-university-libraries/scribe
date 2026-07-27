import { spawn } from "node:child_process";
import http from "node:http";
import net from "node:net";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

const serverPath = fileURLToPath(new URL("./server.mjs", import.meta.url));
const expectedContentSecurityPolicy = [
  "default-src 'self'",
  "script-src 'self'",
  "style-src 'self' 'unsafe-inline'",
  "img-src 'self' data: blob: http: https:",
  "font-src 'self' data:",
  "connect-src 'self' https:",
  "base-uri 'self'",
  "form-action 'self'",
  "frame-ancestors 'none'",
].join("; ");

async function listen(server) {
  await new Promise((resolve, reject) => {
    server.once("error", reject);
    server.listen(0, "127.0.0.1", resolve);
  });
  const address = server.address();
  if (!address || typeof address === "string") throw new Error("test server did not bind a TCP port");
  return address.port;
}

async function unusedPort() {
  const reservation = net.createServer();
  const port = await listen(reservation);
  await new Promise((resolve, reject) => reservation.close((error) => error ? reject(error) : resolve()));
  return port;
}

function handleReadinessProbe(request, response) {
  if (request.url !== "/readyz") return false;
  response.writeHead(200, { "content-type": "application/json" });
  response.end('{"status":"ready"}');
  return true;
}

function spawnFrontend(env) {
  const child = spawn(process.execPath, [serverPath], {
    env: { ...process.env, SCRIBE_FRONTEND_PORT: "0", ...env },
    stdio: ["ignore", "pipe", "pipe"],
  });
  let stdout = "";
  let stderr = "";
  let resolveStarted;
  let startedResolved = false;
  const started = new Promise((resolve) => { resolveStarted = resolve; });
  const settleStarted = (result) => {
    if (startedResolved) return;
    startedResolved = true;
    resolveStarted(result);
  };
  child.stdout.on("data", (chunk) => {
    stdout += chunk;
    const match = stdout.match(/frontend listening on :(\d+)/);
    if (match) settleStarted({ port: Number(match[1]) });
  });
  child.stderr.on("data", (chunk) => { stderr += chunk; });
  const exited = new Promise((resolve) => {
    child.once("error", (error) => settleStarted({ error }));
    child.once("close", (code, signal) => {
      settleStarted({ code, signal });
      resolve({ code, signal });
    });
  });
  return {
    child,
    exited,
    started,
    output: () => ({ stderr, stdout }),
  };
}

async function frontendPort(processFixture) {
  const result = await processFixture.started;
  if (Number.isInteger(result.port)) return result.port;
  throw new Error(`frontend exited before listening\n${JSON.stringify(processFixture.output())}`);
}

async function stopFrontend(processFixture, signal = "SIGTERM") {
  if (processFixture.child.exitCode === null && processFixture.child.signalCode === null) {
    processFixture.child.kill(signal);
  }
  return processFixture.exited;
}

function waitForStream(url, headers = {}) {
  return new Promise((resolve, reject) => {
    const request = http.get(url, { headers });
    request.once("error", reject);
    request.once("response", (response) => {
      const closed = new Promise((closeResolve) => response.once("close", closeResolve));
      response.once("error", reject);
      response.once("data", () => resolve({ closed, response }));
    });
  });
}

describe("frontend server lifecycle", () => {
  it("proxies public liveness and readiness to the backend", async () => {
    const observed = [];
    const upstream = http.createServer((request, response) => {
      observed.push({
        credentialHeaders: [
          "authorization",
          "cookie",
          "x-scribe-api-key",
          "x-scribe-workspace-id",
        ].filter((name) => request.headers[name] !== undefined),
        url: request.url,
      });
      if (request.url === "/livez?source=external") {
        response.writeHead(200, {
          "content-type": "application/json",
          "set-cookie": "status-session=must-not-reach-browser; HttpOnly",
        });
        response.end('{"status":"ok","source":"backend-livez"}');
        return;
      }
      if (request.url === "/readyz?source=external") {
        response.writeHead(503, {
          "content-type": "application/json",
          "set-cookie": "status-session=must-not-reach-browser; HttpOnly",
        });
        response.end('{"status":"not_ready","source":"backend-readyz"}');
        return;
      }
      response.writeHead(404);
      response.end();
    });
    const upstreamPort = await listen(upstream);
    const frontend = spawnFrontend({
      SCRIBE_FRONTEND_BACKEND_ORIGIN: `http://127.0.0.1:${upstreamPort}`,
    });

    try {
      const boundFrontendPort = await frontendPort(frontend);
      const credentialHeaders = {
        authorization: "Bearer browser-secret",
        cookie: "scribe-session=browser-secret",
        "x-scribe-api-key": "browser-secret",
        "x-scribe-workspace-id": "browser-workspace",
      };
      const liveResponse = await fetch(
        `http://127.0.0.1:${boundFrontendPort}/livez?source=external`,
        { headers: credentialHeaders },
      );
      expect(liveResponse.status).toBe(200);
      expect(liveResponse.headers.get("content-type")).toBe("application/json");
      expect(liveResponse.headers.get("content-security-policy")).toBe(expectedContentSecurityPolicy);
      expect(liveResponse.headers.get("set-cookie")).toBeNull();
      expect(await liveResponse.json()).toEqual({ status: "ok", source: "backend-livez" });

      const readyResponse = await fetch(
        `http://127.0.0.1:${boundFrontendPort}/readyz?source=external`,
        { headers: credentialHeaders },
      );
      expect(readyResponse.status).toBe(503);
      expect(readyResponse.headers.get("content-type")).toBe("application/json");
      expect(readyResponse.headers.get("content-security-policy")).toBe(expectedContentSecurityPolicy);
      expect(readyResponse.headers.get("set-cookie")).toBeNull();
      expect(await readyResponse.json()).toEqual({ status: "not_ready", source: "backend-readyz" });
      expect(observed).toEqual([
        { credentialHeaders: [], url: "/livez?source=external" },
        { credentialHeaders: [], url: "/readyz?source=external" },
      ]);
    } finally {
      await stopFrontend(frontend);
      await new Promise((resolve, reject) => upstream.close((error) => error ? reject(error) : resolve()));
    }
  }, 10_000);

  it("preserves no-referrer on a one-time review-token redirect", async () => {
    const upstream = http.createServer((request, response) => {
      if (handleReadinessProbe(request, response)) return;
      if (request.url?.startsWith("/auth/review?token=")) {
        response.writeHead(303, {
          location: "/editor?itemImageId=7&workspace_id=3",
          "referrer-policy": "no-referrer",
        });
        response.end();
        return;
      }
      response.writeHead(404);
      response.end();
    });
    const upstreamPort = await listen(upstream);
    const frontend = spawnFrontend({
      SCRIBE_FRONTEND_BACKEND_ORIGIN: `http://127.0.0.1:${upstreamPort}`,
    });

    try {
      const boundFrontendPort = await frontendPort(frontend);
      const response = await fetch(
        `http://127.0.0.1:${boundFrontendPort}/auth/review?token=one-time-secret`,
        { redirect: "manual" },
      );
      expect(response.status).toBe(303);
      expect(response.headers.get("location")).toBe("/editor?itemImageId=7&workspace_id=3");
      expect(response.headers.get("referrer-policy")).toBe("no-referrer");
      expect(response.headers.get("content-security-policy")).toBe(expectedContentSecurityPolicy);
    } finally {
      await stopFrontend(frontend);
      await new Promise((resolve, reject) => upstream.close((error) => error ? reject(error) : resolve()));
    }
  }, 10_000);

  it("strips credentials at the same-origin public Presentation boundary", async () => {
    const observed = [];
    const upstream = http.createServer((request, response) => {
      if (handleReadinessProbe(request, response)) return;
      observed.push(request.headers);
      response.writeHead(200, {
        "content-type": "application/json",
        "set-cookie": "triplet-session=must-not-reach-browser; HttpOnly",
      });
      response.end('{"type":"AnnotationPage"}');
    });
    const upstreamPort = await listen(upstream);
    const frontend = spawnFrontend({
      SCRIBE_FRONTEND_BACKEND_ORIGIN: `http://127.0.0.1:${upstreamPort}`,
    });

    try {
      const boundFrontendPort = await frontendPort(frontend);
      const response = await fetch(
        `http://127.0.0.1:${boundFrontendPort}/presentation/v3/items/1/annotations`,
        {
          headers: {
            authorization: "Bearer browser-secret",
            cookie: "scribe-session=browser-secret",
            "x-scribe-api-key": "browser-secret",
          },
        },
      );
      expect(response.status).toBe(200);
      await response.text();
      expect(observed).toHaveLength(1);
      expect(observed[0]).not.toHaveProperty("authorization");
      expect(observed[0]).not.toHaveProperty("cookie");
      expect(observed[0]).not.toHaveProperty("x-scribe-api-key");
      expect(response.headers.get("set-cookie")).toBeNull();
      expect(response.headers.get("strict-transport-security"))
        .toBe("max-age=31536000; includeSubDomains");
      expect(response.headers.get("content-security-policy"))
        .toBe(expectedContentSecurityPolicy);
    } finally {
      await stopFrontend(frontend);
      await new Promise((resolve, reject) => upstream.close((error) => error ? reject(error) : resolve()));
    }
  }, 10_000);

  it("holds a Connect mutation through cold start and forwards its body exactly once", async () => {
    const upstreamPort = await unusedPort();
    const frontend = spawnFrontend({
      SCRIBE_FRONTEND_BACKEND_ORIGIN: `http://127.0.0.1:${upstreamPort}`,
      SCRIBE_FRONTEND_BACKEND_STARTUP_TIMEOUT_MS: "3000",
      SCRIBE_FRONTEND_BACKEND_PROBE_TIMEOUT_MS: "100",
      SCRIBE_FRONTEND_BACKEND_PROBE_RETRY_MS: "25",
      SCRIBE_FRONTEND_BACKEND_READY_CACHE_MS: "1000",
    });
    const mutationBodies = [];
    let readinessProbes = 0;
    let upstream;

    try {
      const boundFrontendPort = await frontendPort(frontend);
      const request = fetch(`http://127.0.0.1:${boundFrontendPort}/scribe.v1.AnnotationService/SaveAnnotationPage`, {
        body: '{"revision":"cold-start-once"}',
        headers: { "content-type": "application/json" },
        method: "POST",
      });

      await new Promise((resolve) => setTimeout(resolve, 150));
      upstream = http.createServer(async (incoming, response) => {
        if (incoming.url === "/readyz") {
          readinessProbes += 1;
          handleReadinessProbe(incoming, response);
          return;
        }
        const chunks = [];
        for await (const chunk of incoming) chunks.push(chunk);
        mutationBodies.push(Buffer.concat(chunks).toString());
        response.writeHead(200, { "content-type": "application/json" });
        response.end('{"saved":true}');
      });
      await new Promise((resolve, reject) => {
        upstream.once("error", reject);
        upstream.listen(upstreamPort, "127.0.0.1", resolve);
      });

      const response = await request;
      expect(response.status).toBe(200);
      expect(await response.json()).toEqual({ saved: true });
      expect(readinessProbes).toBeGreaterThan(0);
      expect(mutationBodies).toEqual(['{"revision":"cold-start-once"}']);
    } finally {
      await stopFrontend(frontend);
      if (upstream) await new Promise((resolve, reject) => upstream.close((error) => error ? reject(error) : resolve()));
    }
  }, 10_000);

  it("categorizes invalid readiness responses without logging their content", async () => {
    const secret = "provider-response-must-not-enter-frontend-logs";
    let readinessProbes = 0;
    const upstream = http.createServer((request, response) => {
      if (request.url === "/readyz") {
        readinessProbes += 1;
        response.writeHead(503, { "content-type": "text/plain" });
        response.end(secret);
        return;
      }
      response.writeHead(500);
      response.end();
    });
    const upstreamPort = await listen(upstream);
    const frontend = spawnFrontend({
      SCRIBE_FRONTEND_BACKEND_ORIGIN: `http://127.0.0.1:${upstreamPort}`,
    });

    try {
      const boundFrontendPort = await frontendPort(frontend);
      const response = await fetch(`http://127.0.0.1:${boundFrontendPort}/healthz`);
      expect(response.status).toBe(503);
      expect(await response.text()).toBe("backend is still starting");
      await stopFrontend(frontend);
      expect(frontend.output().stderr).toContain(
        "readiness-contract; HTTP 503 (invalid-json)",
      );
      expect(readinessProbes).toBe(1);
      expect(frontend.output().stderr).not.toContain(secret);
    } finally {
      await stopFrontend(frontend);
      await new Promise((resolve, reject) => upstream.close((error) => error ? reject(error) : resolve()));
    }
  }, 15_000);

  it("categorizes a fixed nested network error code without logging transport details", async () => {
    const unreachablePort = await unusedPort();
    const frontend = spawnFrontend({
      SCRIBE_FRONTEND_BACKEND_ORIGIN: `http://127.0.0.1:${unreachablePort}`,
      SCRIBE_FRONTEND_BACKEND_STARTUP_TIMEOUT_MS: "500",
      SCRIBE_FRONTEND_BACKEND_PROBE_RETRY_MS: "20",
    });

    try {
      const boundFrontendPort = await frontendPort(frontend);
      const response = await fetch(`http://127.0.0.1:${boundFrontendPort}/healthz`);
      expect(response.status).toBe(503);
      expect(await response.text()).toBe("backend is still starting");
      await stopFrontend(frontend);
      expect(frontend.output().stderr).toContain(
        "frontend backend startup gate failed [Error; startup-deadline; transport-TypeError/ECONNREFUSED]",
      );
      expect(frontend.output().stderr).not.toContain("fetch failed");
    } finally {
      await stopFrontend(frontend);
    }
  }, 10_000);

  it("logs only a categorical transport error when an upstream proxy fails", async () => {
    const secret = "query-secret-must-not-enter-frontend-logs";
    const upstream = http.createServer((request, response) => {
      if (handleReadinessProbe(request, response)) return;
      request.socket.destroy();
    });
    const upstreamPort = await listen(upstream);
    const frontend = spawnFrontend({
      SCRIBE_FRONTEND_BACKEND_ORIGIN: `http://127.0.0.1:${upstreamPort}`,
    });

    try {
      const boundFrontendPort = await frontendPort(frontend);
      await fetch(`http://127.0.0.1:${boundFrontendPort}/healthz?token=${secret}`);
      await stopFrontend(frontend);
      expect(frontend.output().stderr).toMatch(
        /frontend proxy request failed \[[A-Za-z0-9/]+\]/,
      );
      expect(frontend.output().stderr).not.toContain(secret);
    } finally {
      await stopFrontend(frontend);
      await new Promise((resolve, reject) => upstream.close((error) => error ? reject(error) : resolve()));
    }
  }, 10_000);

  it("proxies distinct PPB clients as distinct canonical upstream identities", async () => {
    const observed = [];
    const upstream = http.createServer((request, response) => {
      if (handleReadinessProbe(request, response)) return;
      observed.push({
        forwardedFor: request.headers["x-forwarded-for"],
        forwardedHost: request.headers["x-forwarded-host"],
        forwardedProto: request.headers["x-forwarded-proto"],
      });
      response.writeHead(204);
      response.end();
    });
    const upstreamPort = await listen(upstream);
    const frontend = spawnFrontend({
      SCRIBE_FRONTEND_BACKEND_ORIGIN: `http://127.0.0.1:${upstreamPort}`,
      SCRIBE_FRONTEND_EDGE_MODE: "ppb",
    });

    try {
      const boundFrontendPort = await frontendPort(frontend);
      for (const client of ["192.0.2.10", "192.0.2.11"]) {
        const response = await fetch(`http://127.0.0.1:${boundFrontendPort}/healthz`, {
          headers: {
            "x-forwarded-for": client,
            "x-forwarded-host": "scribe-123.us-east5.run.app",
            "x-forwarded-proto": "https",
          },
        });
        expect(response.status).toBe(204);
      }
      expect(observed).toEqual([
        {
          forwardedFor: "192.0.2.10",
          forwardedHost: "scribe-123.us-east5.run.app",
          forwardedProto: "https",
        },
        {
          forwardedFor: "192.0.2.11",
          forwardedHost: "scribe-123.us-east5.run.app",
          forwardedProto: "https",
        },
      ]);
    } finally {
      await stopFrontend(frontend);
      await new Promise((resolve, reject) => upstream.close((error) => error ? reject(error) : resolve()));
    }
  }, 10_000);

  it("bounds SIGTERM drain and tears down an active upstream stream", async () => {
    let closeUpstream;
    const upstreamClosed = new Promise((resolve) => { closeUpstream = resolve; });
    const upstream = http.createServer((request, response) => {
      if (handleReadinessProbe(request, response)) return;
      response.writeHead(200, { "content-type": "text/event-stream" });
      response.write("data: connected\n\n");
      request.once("close", closeUpstream);
    });
    const upstreamPort = await listen(upstream);
    const frontend = spawnFrontend({
      SCRIBE_FRONTEND_BACKEND_ORIGIN: `http://127.0.0.1:${upstreamPort}`,
      SCRIBE_FRONTEND_EDGE_MODE: "ppb",
      SCRIBE_FRONTEND_SHUTDOWN_TIMEOUT_MS: "250",
    });

    try {
      const boundFrontendPort = await frontendPort(frontend);
      const downstream = await waitForStream(`http://127.0.0.1:${boundFrontendPort}/healthz`, {
        "x-forwarded-for": "203.0.113.10",
        "x-forwarded-host": "scribe-123.us-east5.run.app",
        "x-forwarded-proto": "https",
      });
      const started = performance.now();
      const result = await stopFrontend(frontend);

      expect(result, JSON.stringify(frontend.output())).toEqual({ code: 0, signal: null });
      expect(performance.now() - started).toBeLessThan(2_000);
      await upstreamClosed;
      await downstream.closed;
      expect(downstream.response.destroyed).toBe(true);
      expect(frontend.output().stdout).toContain("draining connections");
    } finally {
      await stopFrontend(frontend, "SIGKILL");
      await new Promise((resolve, reject) => upstream.close((error) => error ? reject(error) : resolve()));
    }
  }, 10_000);

  it("reports a startup listen failure and exits nonzero", async () => {
    const reservation = net.createServer();
    const port = await listen(reservation);
    const frontend = spawnFrontend({ SCRIBE_FRONTEND_PORT: String(port) });
    try {
      const result = await frontend.exited;
      expect(result.code).not.toBe(0);
      expect(frontend.output().stderr).toContain("frontend server error");
      expect(frontend.output().stderr).toContain("EADDRINUSE");
    } finally {
      await stopFrontend(frontend, "SIGKILL");
      await new Promise((resolve, reject) => reservation.close((error) => error ? reject(error) : resolve()));
    }
  }, 10_000);
});
