import { setDefaultResultOrder } from "node:dns";
import { Resolver } from "node:dns/promises";
import { isIP, setDefaultAutoSelectFamily } from "node:net";

const canonicalScribeRunAppHostnamePattern = /^scribe(?:-pr-[1-9][0-9]*)?-[1-9][0-9]*\.[a-z]+-[a-z]+[0-9]+\.run\.app$/;
const maximumIPv6Answers = 32;
const defaultResolutionTimeoutMs = 120_000;
const maximumResolutionTimeoutMs = 180_000;
const defaultResolutionAttemptTimeoutMs = 10_000;
const defaultResolutionRetryIntervalMs = 2_000;
const publicDNSResolvers = Object.freeze(["8.8.8.8", "8.8.4.4"]);

function canonicalScribeHostname(hostname) {
  const normalized = String(hostname ?? "").trim().toLowerCase();
  if (!canonicalScribeRunAppHostnamePattern.test(normalized)) {
    throw new Error("invalid canonical Scribe host");
  }
  return normalized;
}

function globalUnicastIPv6(address) {
  const normalized = String(address ?? "").trim().toLowerCase();
  if (isIP(normalized) !== 6) return "";
  const firstHextet = Number.parseInt(normalized.split(":", 1)[0], 16);
  // IANA's current global-unicast allocation is 2000::/3. Restrict the
  // browser mapping to that public range so a malformed DNS response cannot
  // redirect the canonical origin to loopback, link-local, ULA, or multicast.
  if (
    !Number.isInteger(firstHextet)
    || firstHextet < 0x2000
    || firstHextet > 0x3fff
  ) return "";
  return normalized;
}

export function selectCanonicalGlobalIPv6(records) {
  if (!Array.isArray(records) || records.length === 0 || records.length > maximumIPv6Answers) {
    throw new Error("canonical IPv6 resolution failed");
  }
  const candidates = [...new Set(records.map(globalUnicastIPv6).filter(Boolean))].sort();
  if (candidates.length === 0) throw new Error("canonical IPv6 resolution failed");
  return candidates[0];
}

export function chromiumIPv6HostResolverArgument(hostname, address) {
  const canonicalHostname = canonicalScribeHostname(hostname);
  const canonicalAddress = globalUnicastIPv6(address);
  if (!canonicalAddress) throw new Error("invalid canonical IPv6 address");
  return `--host-resolver-rules=MAP ${canonicalHostname} [${canonicalAddress}]`;
}

export async function configureCanonicalIPv6Routing(hostname, options = {}) {
  const canonicalHostname = canonicalScribeHostname(hostname);
  const timeoutMs = options.timeoutMs ?? defaultResolutionTimeoutMs;
  const attemptTimeoutMs = options.attemptTimeoutMs ?? defaultResolutionAttemptTimeoutMs;
  const retryIntervalMs = options.retryIntervalMs ?? defaultResolutionRetryIntervalMs;
  if (
    !Number.isSafeInteger(timeoutMs)
    || timeoutMs < 1
    || timeoutMs > maximumResolutionTimeoutMs
    || !Number.isSafeInteger(attemptTimeoutMs)
    || attemptTimeoutMs < 1
    || attemptTimeoutMs > maximumResolutionTimeoutMs
    || !Number.isSafeInteger(retryIntervalMs)
    || retryIntervalMs < 0
    || retryIntervalMs > maximumResolutionTimeoutMs
  ) {
    throw new Error("invalid canonical IPv6 resolution timeout");
  }

  const resolverFactory = options.resolverFactory ?? (() => new Resolver());
  const deadline = Date.now() + timeoutMs;
  let address = "";
  while (Date.now() < deadline) {
    const resolver = resolverFactory();
    if (
      typeof resolver?.resolve6 !== "function"
      || typeof resolver?.cancel !== "function"
      || typeof resolver?.setServers !== "function"
    ) {
      throw new Error("canonical IPv6 resolver unavailable");
    }

    let attemptTimer;
    try {
      // The container resolver may suppress AAAA on an IPv4-first host even
      // though the Direct VPC subnet is dual stack. Use fixed validating Google
      // Public DNS resolvers so canonical routing never depends on that heuristic.
      resolver.setServers([...publicDNSResolvers]);
      const remainingMs = Math.max(1, deadline - Date.now());
      const records = await Promise.race([
        Promise.resolve().then(() => resolver.resolve6(canonicalHostname)),
        new Promise((_, reject) => {
          attemptTimer = setTimeout(() => {
            try {
              resolver.cancel();
            } catch {
              // Cancellation is best-effort; the total deadline remains
              // authoritative and no raw resolver error is emitted.
            }
            reject(new Error("canonical IPv6 resolution attempt timed out"));
          }, Math.min(attemptTimeoutMs, remainingMs));
        }),
      ]);
      address = selectCanonicalGlobalIPv6(records);
      break;
    } catch {
      // Direct VPC and Cloud NAT can both be cold when a new task starts.
      // Retry only this fixed DNS query until one absolute bounded deadline.
    } finally {
      clearTimeout(attemptTimer);
    }

    const remainingMs = deadline - Date.now();
    if (remainingMs <= 0) break;
    await new Promise((resolve) => {
      setTimeout(resolve, Math.min(retryIntervalMs, remainingMs));
    });
  }
  if (!address) throw new Error("canonical IPv6 resolution timed out");

  const setResultOrder = options.setResultOrder ?? setDefaultResultOrder;
  const setAutoSelectFamily = options.setAutoSelectFamily ?? setDefaultAutoSelectFamily;
  setResultOrder("ipv6first");
  setAutoSelectFamily(false);
  return chromiumIPv6HostResolverArgument(canonicalHostname, address);
}
