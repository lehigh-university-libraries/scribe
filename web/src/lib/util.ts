export function uint64ToString(value: unknown): string {
  try {
    return BigInt(value as string | number | bigint).toString();
  } catch {
    return "";
  }
}

export async function readFileBytes(file: File): Promise<Uint8Array> {
  const buffer = await file.arrayBuffer();
  return new Uint8Array(buffer);
}

export function escHtml(s: string): string {
  return s
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&#39;");
}

// Branded marker for HTML that has already been escaped/vetted by this module.
// It is intentionally an object (not a branded string) so `serialize` can tell
// trusted fragments apart from arbitrary strings at runtime.
const TRUSTED_HTML = Symbol("scribe.trustedHtml");

export interface TrustedHTML {
  readonly [TRUSTED_HTML]: true;
  readonly value: string;
}

function mark(value: string): TrustedHTML {
  return { [TRUSTED_HTML]: true, value };
}

function isTrusted(value: unknown): value is TrustedHTML {
  return typeof value === "object" && value !== null
    && (value as Record<symbol, unknown>)[TRUSTED_HTML] === true;
}

// Converts an interpolated value into HTML text. Anything that is not already
// trusted is HTML-escaped, so a forgotten escape can no longer become XSS:
// `${userInput}` inside an html`` template is always escaped by default.
function serialize(value: unknown): string {
  if (value === null || value === undefined || value === false || value === true) {
    return "";
  }
  if (isTrusted(value)) {
    return value.value;
  }
  if (Array.isArray(value)) {
    return value.map(serialize).join("");
  }
  return escHtml(String(value));
}

// The single safe rendering model for the app shell: build markup with the
// html`` tagged template. Every ${interpolation} is escaped unless it is itself
// a TrustedHTML produced by a nested html`` fragment (or an array of them).
// There is deliberately no public raw-markup escape hatch.
export function html(strings: TemplateStringsArray, ...values: unknown[]): TrustedHTML {
  let out = strings[0];
  for (let i = 0; i < values.length; i++) {
    out += serialize(values[i]);
    out += strings[i + 1];
  }
  return mark(out);
}

export function setHTML(element: Element, markup: TrustedHTML): void {
  element.innerHTML = sanitizeHTML(markup.value);
}

function sanitizeHTML(markup: string): string {
  const template = document.createElement("template");
  template.innerHTML = markup;
  const blockedElements = new Set(["SCRIPT", "IFRAME", "OBJECT", "EMBED", "LINK", "META"]);
  const walker = document.createTreeWalker(template.content, NodeFilter.SHOW_ELEMENT);
  const remove: Element[] = [];
  for (let node = walker.nextNode(); node; node = walker.nextNode()) {
    const element = node as Element;
    if (blockedElements.has(element.tagName)) {
      remove.push(element);
      continue;
    }
    for (const attr of Array.from(element.attributes)) {
      const name = attr.name.toLowerCase();
      const value = attr.value.trim().toLowerCase();
      if (name.startsWith("on") || name === "srcdoc") {
        element.removeAttribute(attr.name);
        continue;
      }
      if (isURLAttribute(name) && !isSafeURL(value, name)) {
        element.removeAttribute(attr.name);
      }
    }
  }
  for (const element of remove) {
    element.remove();
  }
  return template.innerHTML;
}

function isURLAttribute(name: string): boolean {
  return name === "href" || name === "src" || name.endsWith(":href");
}

function isSafeURL(value: string, attrName: string): boolean {
  if (value === "" || value.startsWith("#") || value.startsWith("/") || value.startsWith("./") || value.startsWith("../")) {
    return true;
  }
  if (value.startsWith("data:")) {
    return attrName === "src" && /^data:image\/(?:png|gif|jpeg|jpg|webp);/i.test(value);
  }
  try {
    const parsed = new URL(value, "https://scribe.invalid");
    return parsed.protocol === "http:" || parsed.protocol === "https:" || parsed.protocol === "mailto:";
  } catch {
    return false;
  }
}
