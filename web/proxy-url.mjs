export function createUpstreamURL(configuredOrigin, requestTarget = "/") {
  const requested = new URL(requestTarget, "http://frontend.local");
  const target = new URL(configuredOrigin);

  // Assigning these fields cannot replace the configured authority, even when
  // an absolute-form request target contains a path beginning with `//`.
  target.pathname = requested.pathname;
  target.search = requested.search;
  return target;
}

export function isIIIFPath(pathname) {
  return pathname === "/iiif" || pathname.startsWith("/iiif/");
}

export function isPresentationPath(pathname) {
  return pathname === "/presentation" || pathname.startsWith("/presentation/");
}

export function isAllowedIIIFMethod(method) {
  return method === "GET" || method === "HEAD" || method === "OPTIONS";
}
