import { isIP } from "node:net";

function deleteHeader(headers, name) {
  for (const key of Object.keys(headers)) {
    if (key.toLowerCase() === name) delete headers[key];
  }
}

export function stripHopByHopHeaders(headers) {
  const filtered = { ...headers };
  const connection = Array.isArray(headers.connection)
    ? headers.connection.join(",")
    : (headers.connection || "");

  for (const token of connection.split(",")) {
    const name = token.trim().toLowerCase();
    if (name) deleteHeader(filtered, name);
  }
  for (const name of [
    "connection",
    "forwarded",
    "keep-alive",
    "proxy-authenticate",
    "proxy-authorization",
    "proxy-connection",
    "te",
    "trailers",
    "transfer-encoding",
    "upgrade",
  ]) {
    deleteHeader(filtered, name);
  }
  return filtered;
}

function headerValue(headers, name) {
  for (const [key, value] of Object.entries(headers)) {
    if (key.toLowerCase() === name) return value;
  }
  return undefined;
}

function singleHeaderValue(headers, name) {
  const value = headerValue(headers, name);
  if (typeof value !== "string" || value.trim() === "" || value.includes(",")) {
    throw new Error(`${name} must contain exactly one value`);
  }
  return value.trim();
}

function canonicalIPAddress(raw) {
  if (typeof raw !== "string") throw new Error("forwarded client address is missing");
  const candidate = raw.trim().replace(/^\[|\]$/g, "");
  if (!isIP(candidate)) throw new Error("forwarded client address is invalid");
  return candidate;
}

export function selectForwardedClient(value, depth) {
  if (!Number.isSafeInteger(depth) || depth < 0 || depth > 10) {
    throw new Error("forwarded client depth must be a whole number from 0 through 10");
  }
  const values = Array.isArray(value) ? value : [value];
  const chain = values.flatMap((entry) => typeof entry === "string" ? entry.split(",") : []);
  // An exact chain length is intentional. A trusted edge must normalize away
  // every attacker-supplied prefix before this boundary; accepting extra
  // entries here would make a topology mistake silently spoofable.
  if (chain.length !== depth + 1) {
    throw new Error("forwarded client chain does not match the trusted topology");
  }
  return canonicalIPAddress(chain[chain.length - 1 - depth]);
}

export function isLoopbackAddress(raw) {
  let address;
  try {
    address = canonicalIPAddress(raw).toLowerCase();
  } catch {
    return false;
  }
  return address === "::1"
    || address.startsWith("127.")
    || address.startsWith("::ffff:127.");
}

function validateForwardedHost(raw) {
  const host = raw.trim();
  if (host.length > 255 || /[\s,/@\\?#]/u.test(host)) {
    throw new Error("x-forwarded-host is invalid");
  }
  let parsed;
  try {
    parsed = new URL(`https://${host}`);
  } catch {
    throw new Error("x-forwarded-host is invalid");
  }
  if (!parsed.hostname || parsed.pathname !== "/" || parsed.search || parsed.hash) {
    throw new Error("x-forwarded-host is invalid");
  }
  return host;
}

export function resolveForwardingIdentity(headers, {
  edgeMode,
  encrypted,
  remoteAddress,
  requestHost,
}) {
  if (edgeMode === "direct") {
    return {
      clientAddress: canonicalIPAddress(remoteAddress),
      host: validateForwardedHost(requestHost),
      proto: encrypted ? "https" : "http",
    };
  }
  if (edgeMode !== "ppb") throw new Error("unsupported frontend edge mode");
  if (!isLoopbackAddress(remoteAddress)) {
    throw new Error("PPB forwarding is accepted only from the loopback peer");
  }

  const proto = singleHeaderValue(headers, "x-forwarded-proto").toLowerCase();
  if (proto !== "https") throw new Error("PPB must establish external HTTPS");
  return {
    // PPB 0.5.1 validates its configured Cloud Run depth, removes the incoming
    // chain, and sends exactly one canonical client IP to this sidecar.
    clientAddress: selectForwardedClient(headerValue(headers, "x-forwarded-for"), 0),
    host: validateForwardedHost(singleHeaderValue(headers, "x-forwarded-host")),
    proto,
  };
}

export function establishForwardingHeaders(headers, { clientAddress, host, proto }) {
  const filtered = { ...headers };
  for (const name of [
    "forwarded",
    "x-forwarded-client-cert",
    "x-forwarded-for",
    "x-forwarded-host",
    "x-forwarded-port",
    "x-forwarded-prefix",
    "x-forwarded-proto",
    "x-forwarded-server",
    "x-real-ip",
  ]) {
    deleteHeader(filtered, name);
  }
  if (host) filtered["x-forwarded-host"] = String(host).trim();
  if (proto) filtered["x-forwarded-proto"] = String(proto).trim();
  if (clientAddress) filtered["x-forwarded-for"] = String(clientAddress).trim();
  return filtered;
}

export function stripCredentialHeaders(headers) {
  const filtered = { ...headers };
  for (const name of [
    "authorization",
    "cookie",
    "x-scribe-api-key",
    "x-scribe-workspace-id",
  ]) {
    deleteHeader(filtered, name);
  }
  return filtered;
}

export function stripCredentialResponseHeaders(headers) {
  const filtered = { ...headers };
  deleteHeader(filtered, "set-cookie");
  return filtered;
}
