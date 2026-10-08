import { describe, expect, it } from "vitest";
import {
  establishForwardingHeaders,
  ingressPolicy,
  resolveForwardingIdentity,
  selectForwardedClient,
  stripCredentialHeaders,
  stripHopByHopHeaders,
} from "./proxy-headers.mjs";

describe("frontend proxy header boundaries", () => {
  it("strips Cloud Run and application credentials from public sidecar requests", () => {
    expect(stripCredentialHeaders({
      authorization: "Bearer application-token",
      "x-serverless-authorization": "Bearer platform-token",
      cookie: "scribe_session=private",
      accept: "application/json",
    })).toEqual({ accept: "application/json" });
  });
  it("accepts direct Cloud Run HTTPS and ignores a spoofed forwarded host", () => {
    const options = { edgeMode: "cloudrun", requestHost: "scribe-123.us-east5.run.app" };
    const headers = { "x-forwarded-for": "203.0.113.9", "x-forwarded-proto": "https", "x-forwarded-host": "evil.example" };
    expect(resolveForwardingIdentity(headers, options)).toEqual({ clientAddress: "203.0.113.9", host: options.requestHost, proto: "https" });
    expect(() => resolveForwardingIdentity({ ...headers, "x-forwarded-for": "198.51.100.2, 203.0.113.9" }, options)).toThrow(/topology/);
    expect(() => resolveForwardingIdentity({ ...headers, "x-forwarded-proto": "http" }, options)).toThrow(/HTTPS/);
  });

  it("enforces IPv4 and IPv6 ingress CIDRs and rejects malformed policies", () => {
    const allowed = ingressPolicy(["203.0.113.0/24", "2001:db8::/32"]);
    expect(allowed("203.0.113.9")).toBe(true);
    expect(allowed("198.51.100.9")).toBe(false);
    expect(allowed("2001:db8::1")).toBe(true);
    expect(allowed("2001:db9::1")).toBe(false);
    expect(() => ingressPolicy(["203.0.113.0/33"])).toThrow();
  });
  it("removes standard and Connection-nominated hop-by-hop headers", () => {
    const headers = stripHopByHopHeaders({
      connection: "keep-alive, x-remove-me",
      "keep-alive": "timeout=10",
      "x-remove-me": "secret",
      "x-preserve-me": "value",
    });

    expect(headers).toEqual({ "x-preserve-me": "value" });
  });

  it("accepts PPB's one validated Cloud Run client and re-emits only that identity", () => {
    const identity = resolveForwardingIdentity({
      "x-forwarded-for": "203.0.113.9",
      "x-forwarded-host": "scribe-123.us-east5.run.app",
      "x-forwarded-proto": "https",
    }, {
      edgeMode: "ppb",
      encrypted: false,
      remoteAddress: "::1",
      requestHost: "localhost:8888",
    });
    const headers = establishForwardingHeaders({
      forwarded: "for=attacker;host=evil.example;proto=https",
      "x-forwarded-client-cert": "secret-certificate",
      "x-forwarded-for": "198.51.100.8",
      "x-forwarded-host": "evil.example",
      "x-forwarded-port": "443",
      "x-forwarded-prefix": "/admin",
      "x-forwarded-proto": "http",
      "x-real-ip": "203.0.113.9",
      accept: "application/json",
    }, identity);

    expect(headers).toEqual({
      accept: "application/json",
      "x-forwarded-for": "203.0.113.9",
      "x-forwarded-host": "scribe-123.us-east5.run.app",
      "x-forwarded-proto": "https",
    });
  });

  it("selects direct Cloud Run depth zero and a reviewed one-ALB depth one", () => {
    expect(selectForwardedClient("203.0.113.9", 0)).toBe("203.0.113.9");
    expect(selectForwardedClient("203.0.113.9, 35.191.0.10", 1)).toBe("203.0.113.9");
  });

  it("fails closed for shallow, invalid, and spoof-prefixed chains", () => {
    expect(() => selectForwardedClient("203.0.113.9", 1)).toThrow(/trusted topology/);
    expect(() => selectForwardedClient("not-an-ip", 0)).toThrow(/invalid/);
    expect(() => selectForwardedClient("198.51.100.7, 203.0.113.9", 0)).toThrow(/trusted topology/);
    expect(() => selectForwardedClient("198.51.100.7, 203.0.113.9, 35.191.0.10", 1)).toThrow(/trusted topology/);
  });

  it("rejects missing or untrusted PPB forwarding invariants", () => {
    const validHeaders = {
      "x-forwarded-for": "203.0.113.9",
      "x-forwarded-host": "scribe-123.us-east5.run.app",
      "x-forwarded-proto": "https",
    };
    const options = {
      edgeMode: "ppb",
      encrypted: false,
      remoteAddress: "127.0.0.1",
      requestHost: "localhost:8888",
    };

    expect(() => resolveForwardingIdentity({}, options)).toThrow();
    expect(() => resolveForwardingIdentity(validHeaders, {
      ...options,
      remoteAddress: "10.42.0.8",
    })).toThrow(/loopback/);
    expect(() => resolveForwardingIdentity({
      ...validHeaders,
      "x-forwarded-proto": "http",
    }, options)).toThrow(/HTTPS/);
    expect(() => resolveForwardingIdentity({
      ...validHeaders,
      "x-forwarded-for": "198.51.100.8, 203.0.113.9",
    }, options)).toThrow(/trusted topology/);
  });

  it("keeps explicit direct mode available for local development", () => {
    expect(resolveForwardingIdentity({}, {
      edgeMode: "direct",
      encrypted: false,
      remoteAddress: "127.0.0.1",
      requestHost: "localhost:8888",
    })).toEqual({
      clientAddress: "127.0.0.1",
      host: "localhost:8888",
      proto: "http",
    });
  });

});
