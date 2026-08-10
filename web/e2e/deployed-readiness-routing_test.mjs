import assert from "node:assert/strict";
import test from "node:test";

import {
  chromiumIPv6HostResolverArgument,
  configureCanonicalIPv6Routing,
  selectCanonicalGlobalIPv6,
} from "./deployed-readiness-routing.mjs";

test("selectCanonicalGlobalIPv6 accepts only a bounded global-unicast answer", () => {
  assert.equal(
    selectCanonicalGlobalIPv6([
      "2a00:1450:4001:82b::2004",
      "2607:f8b0:4004:c1b::65",
      "2A00:1450:4001:82B::2004",
    ]),
    "2607:f8b0:4004:c1b::65",
  );
  for (const records of [
    [],
    ["192.0.2.10"],
    ["::1"],
    ["fe80::1"],
    ["fd00::1"],
    ["ff02::1"],
    Array.from({ length: 33 }, (_, index) => `2607:f8b0:4004:c1b::${index + 1}`),
  ]) {
    assert.throws(
      () => selectCanonicalGlobalIPv6(records),
      /canonical IPv6 resolution failed/,
    );
  }
});

test("chromium resolver rule maps only a canonical Scribe run.app host", () => {
  assert.equal(
    chromiumIPv6HostResolverArgument(
      "scribe-pr-99-915966395449.us-east5.run.app",
      "2607:f8b0:4004:c1b::65",
    ),
    "--host-resolver-rules=MAP scribe-pr-99-915966395449.us-east5.run.app [2607:f8b0:4004:c1b::65]",
  );
  assert.equal(
    chromiumIPv6HostResolverArgument(
      "scribe-915966395449.us-east5.run.app",
      "2607:f8b0:4004:c1b::65",
    ),
    "--host-resolver-rules=MAP scribe-915966395449.us-east5.run.app [2607:f8b0:4004:c1b::65]",
  );
  assert.throws(
    () => chromiumIPv6HostResolverArgument("other.example", "2607:f8b0:4004:c1b::65"),
    /invalid canonical Scribe host/,
  );
  assert.throws(
    () => chromiumIPv6HostResolverArgument(
      "scribe-pr-99-915966395449.us-east5.run.app",
      "127.0.0.1",
    ),
    /invalid canonical IPv6 address/,
  );
});

test("configureCanonicalIPv6Routing fences Node fallback and returns one Chromium map", async () => {
  const calls = [];
  const resolver = {
    cancel() {
      calls.push("cancel");
    },
    setServers(servers) {
      calls.push(["servers", servers]);
    },
    async resolve6(hostname) {
      calls.push(["resolve6", hostname]);
      return ["2a00:1450:4001:82b::2004"];
    },
  };
  const chromiumArgument = await configureCanonicalIPv6Routing(
    "scribe-pr-99-915966395449.us-east5.run.app",
    {
      resolverFactory: () => resolver,
      setAutoSelectFamily: (value) => calls.push(["auto", value]),
      setResultOrder: (value) => calls.push(["order", value]),
      timeoutMs: 100,
    },
  );
  assert.equal(
    chromiumArgument,
    "--host-resolver-rules=MAP scribe-pr-99-915966395449.us-east5.run.app [2a00:1450:4001:82b::2004]",
  );
  assert.deepEqual(calls, [
    ["servers", ["8.8.8.8", "8.8.4.4"]],
    ["resolve6", "scribe-pr-99-915966395449.us-east5.run.app"],
    ["order", "ipv6first"],
    ["auto", false],
  ]);
});

test("configureCanonicalIPv6Routing cancels a bounded unresolved query", async () => {
  let cancelled = false;
  const resolver = {
    cancel() {
      cancelled = true;
    },
    setServers() {},
    async resolve6() {
      return new Promise(() => {});
    },
  };
  await assert.rejects(
    configureCanonicalIPv6Routing("scribe-915966395449.us-east5.run.app", {
      resolverFactory: () => resolver,
      timeoutMs: 5,
    }),
    /canonical IPv6 resolution timed out/,
  );
  assert.equal(cancelled, true);
});

test("configureCanonicalIPv6Routing retries a transient fixed-DNS failure", async () => {
  let attempts = 0;
  const chromiumArgument = await configureCanonicalIPv6Routing(
    "scribe-pr-99-915966395449.us-east5.run.app",
    {
      attemptTimeoutMs: 20,
      resolverFactory: () => ({
        cancel() {},
        setServers() {},
        async resolve6() {
          attempts += 1;
          if (attempts === 1) throw new Error("transient network failure");
          return ["2607:f8b0:4004:c1b::65"];
        },
      }),
      retryIntervalMs: 1,
      setAutoSelectFamily() {},
      setResultOrder() {},
      timeoutMs: 100,
    },
  );
  assert.equal(attempts, 2);
  assert.equal(
    chromiumArgument,
    "--host-resolver-rules=MAP scribe-pr-99-915966395449.us-east5.run.app [2607:f8b0:4004:c1b::65]",
  );
});
