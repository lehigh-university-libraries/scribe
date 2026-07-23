import { spawn } from "node:child_process";
import http from "node:http";
import net from "node:net";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

const jobPath = fileURLToPath(new URL("./readiness-job.mjs", import.meta.url));

async function unusedPort() {
  const server = net.createServer();
  await new Promise((resolve, reject) => {
    server.once("error", reject);
    server.listen(0, "127.0.0.1", resolve);
  });
  const address = server.address();
  const port = typeof address === "object" && address ? address.port : 0;
  await new Promise((resolve, reject) => server.close((error) => error ? reject(error) : resolve()));
  return port;
}

function runReadinessJob(env) {
  return new Promise((resolve, reject) => {
    const child = spawn(process.execPath, [jobPath], {
      env: {
        ...process.env,
        // The frontend process is intentionally spawned by the job. Leave
        // enough retries for startup on contended CI runners before asserting
        // the health payload category.
        SCRIBE_READINESS_ATTEMPTS: "10",
        SCRIBE_READINESS_FETCH_TIMEOUT_MS: "1000",
        SCRIBE_READINESS_RETRY_MS: "100",
        SCRIBE_EXPECTED_API_IMAGE: "ghcr.io/example/scribe@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
        SCRIBE_EXPECTED_BACKEND_IP: "127.0.0.1",
        ...env,
      },
      stdio: ["ignore", "pipe", "pipe"],
    });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (chunk) => { stdout += chunk; });
    child.stderr.on("data", (chunk) => { stderr += chunk; });
    child.once("error", reject);
    child.once("exit", (code, signal) => resolve({ code, signal, stdout, stderr }));
  });
}

describe("deployed frontend readiness job", () => {
  it("passes only through the real frontend health proxy", async () => {
    const upstream = http.createServer((_request, response) => {
      response.writeHead(200, { "content-type": "application/json" });
      response.end('{"status":"ready","api_image":"ghcr.io/example/scribe@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}');
    });
    await new Promise((resolve, reject) => {
      upstream.once("error", reject);
      upstream.listen(0, "127.0.0.1", resolve);
    });
    const upstreamAddress = upstream.address();
    const upstreamPort = typeof upstreamAddress === "object" && upstreamAddress ? upstreamAddress.port : 0;
    try {
      const result = await runReadinessJob({
        SCRIBE_READINESS_ATTEMPTS: "20",
        SCRIBE_READINESS_RETRY_MS: "500",
        SCRIBE_FRONTEND_BACKEND_ORIGIN: `http://127.0.0.1:${upstreamPort}`,
      });
      expect(result, result.stderr).toMatchObject({ code: 0, signal: null });
      expect(result.stdout).toContain("frontend proxy and backend release");
    } finally {
      await new Promise((resolve, reject) => upstream.close((error) => error ? reject(error) : resolve()));
    }
  }, 30_000);

  it("fails when the image has a wrong baked backend origin", async () => {
    const unreachablePort = await unusedPort();
    const result = await runReadinessJob({
      SCRIBE_FRONTEND_BACKEND_ORIGIN: `http://127.0.0.1:${unreachablePort}`,
    });

    expect(result.code).not.toBe(0);
    expect(result.stderr).toContain("frontend readiness failed");
    expect(result.stderr).toContain(
      "frontend backend network probe [dns-match; tcp-refused; http-transport-ECONNREFUSED]",
    );
  }, 20_000);

  it("distinguishes stale backend DNS from a reachable current VM", async () => {
    let observedHost = "";
    const upstream = http.createServer((request, response) => {
      observedHost = request.headers.host || "";
      response.writeHead(200, { "content-type": "application/json" });
      response.end('{"status":"ready"}');
    });
    await new Promise((resolve, reject) => {
      upstream.once("error", reject);
      upstream.listen(0, "127.0.0.2", resolve);
    });
    const upstreamAddress = upstream.address();
    const upstreamPort = typeof upstreamAddress === "object" && upstreamAddress
      ? upstreamAddress.port
      : 0;
    try {
      const result = await runReadinessJob({
        SCRIBE_READINESS_ATTEMPTS: "1",
        SCRIBE_EXPECTED_BACKEND_IP: "127.0.0.2",
        SCRIBE_FRONTEND_BACKEND_ORIGIN: `http://localhost:${upstreamPort}`,
      });
      expect(result.code).not.toBe(0);
      const marker = result.stderr
        .split(/\r?\n/)
        .find((line) => line.startsWith("frontend backend network probe"));
      expect(marker).toBe(
        "frontend backend network probe [dns-mismatch; tcp-open; http-ready]",
      );
      expect(observedHost).toBe(`localhost:${upstreamPort}`);
      expect(marker).not.toContain("localhost");
      expect(marker).not.toContain("127.0.0.2");
    } finally {
      await new Promise((resolve, reject) => upstream.close((error) => error ? reject(error) : resolve()));
    }
  }, 20_000);

  it("fails when a healthy backend runs a different image digest", async () => {
    const upstream = http.createServer((_request, response) => {
      response.writeHead(200, { "content-type": "application/json" });
      response.end('{"status":"ready","api_image":"ghcr.io/example/scribe@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}');
    });
    await new Promise((resolve, reject) => {
      upstream.once("error", reject);
      upstream.listen(0, "127.0.0.1", resolve);
    });
    const upstreamAddress = upstream.address();
    const upstreamPort = typeof upstreamAddress === "object" && upstreamAddress ? upstreamAddress.port : 0;
    try {
      const result = await runReadinessJob({
        SCRIBE_FRONTEND_BACKEND_ORIGIN: `http://127.0.0.1:${upstreamPort}`,
      });
      expect(result.code).not.toBe(0);
      expect(result.stderr).toContain("frontend readiness failed");
      expect(result.stderr).toContain("api-image-mismatch");
    } finally {
      await new Promise((resolve, reject) => upstream.close((error) => error ? reject(error) : resolve()));
    }
  }, 20_000);

  it("does not log invalid health response content", async () => {
    const secret = "deployed-health-content-must-not-enter-ci-logs";
    const upstream = http.createServer((request, response) => {
      response.writeHead(request.url === "/readyz" ? 200 : 503, {
        "content-type": request.url === "/readyz" ? "application/json" : "text/plain",
      });
      response.end(request.url === "/readyz" ? '{"status":"ready"}' : secret);
    });
    await new Promise((resolve, reject) => {
      upstream.once("error", reject);
      upstream.listen(0, "127.0.0.1", resolve);
    });
    const upstreamAddress = upstream.address();
    const upstreamPort = typeof upstreamAddress === "object" && upstreamAddress ? upstreamAddress.port : 0;
    try {
      const result = await runReadinessJob({
        SCRIBE_FRONTEND_BACKEND_ORIGIN: `http://127.0.0.1:${upstreamPort}`,
      });
      expect(result.code).not.toBe(0);
      expect(result.stderr, JSON.stringify(result)).toContain("HTTP 503 (invalid-json)");
      expect(result.stderr).not.toContain(secret);
    } finally {
      await new Promise((resolve, reject) => upstream.close((error) => error ? reject(error) : resolve()));
    }
  }, 20_000);
});
