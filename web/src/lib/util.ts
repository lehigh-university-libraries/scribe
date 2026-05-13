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

export function setHTML(element: Element, markup: string): void {
  element.innerHTML = sanitizeHTML(markup);
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
      if ((name === "href" || name === "src" || name.endsWith(":href")) && (value.startsWith("javascript:") || value.startsWith("vbscript:"))) {
        element.removeAttribute(attr.name);
      }
    }
  }
  for (const element of remove) {
    element.remove();
  }
  return template.innerHTML;
}
