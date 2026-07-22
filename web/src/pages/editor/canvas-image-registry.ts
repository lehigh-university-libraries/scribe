import type { ItemImage } from "../../proto/scribe/v1/item_pb";

export type CanvasImageReference = Pick<ItemImage, "canvasUri" | "id">;

export interface CanvasImageRegistryOptions {
  /**
   * Enables the standalone, one-image editor route where the manifest Canvas
   * may not have been available when the registry was assembled. This mode is
   * deliberately rejected for multi-image items.
   */
  singleImageFallback?: string | bigint;
}

export interface CanvasImageRegistry {
  canvasIdForItemImage(itemImageId: string | bigint): string;
  hasItemImageId(itemImageId: string | bigint): boolean;
  itemImageIdForCanvas(canvasId: string): string;
}

function canonicalItemImageId(value: string | bigint, field: string): string {
  const raw = String(value).trim();
  if (!/^[1-9][0-9]*$/.test(raw)) {
    throw new Error(`${field} must be a positive item image ID`);
  }
  return BigInt(raw).toString();
}

function strictCanvasId(value: string, field: string): string {
  if (value !== value.trim() || value === "") {
    throw new Error(
      `${field} must be a non-empty HTTP(S) URI without surrounding whitespace`,
    );
  }
  let parsed: URL;
  try {
    parsed = new URL(value);
  } catch {
    throw new Error(`${field} must be an absolute HTTP(S) URI`);
  }
  if (
    (parsed.protocol !== "http:" && parsed.protocol !== "https:") ||
    parsed.username ||
    parsed.password
  ) {
    throw new Error(
      `${field} must be an absolute HTTP(S) URI without credentials`,
    );
  }
  if (parsed.hash) {
    throw new Error(`${field} must identify a Canvas, not a fragment`);
  }
  return value;
}

/**
 * Builds the authoritative mapping between IIIF Canvas identities and Scribe
 * item-image identities. Canvas lookups are exact by design: URL rewriting or
 * sequence-based guessing can silently persist one page's edits onto another.
 */
export function createCanvasImageRegistry(
  images: readonly CanvasImageReference[],
  options: CanvasImageRegistryOptions = {},
): CanvasImageRegistry {
  const byCanvas = new Map<string, string>();
  const byItemImage = new Map<string, string>();
  const itemImageIds = new Set<string>();

  for (const [index, image] of images.entries()) {
    const canvasId = strictCanvasId(
      image.canvasUri,
      `images[${index}].canvasUri`,
    );
    const itemImageId = canonicalItemImageId(image.id, `images[${index}].id`);
    const existing = byCanvas.get(canvasId);
    if (existing !== undefined) {
      throw new Error(`Canvas '${canvasId}' is registered more than once`);
    }
    if (itemImageIds.has(itemImageId)) {
      throw new Error(
        `Item image '${itemImageId}' is registered for more than one Canvas`,
      );
    }
    byCanvas.set(canvasId, itemImageId);
    byItemImage.set(itemImageId, canvasId);
    itemImageIds.add(itemImageId);
  }

  let fallbackItemImageId = "";
  if (options.singleImageFallback !== undefined) {
    fallbackItemImageId = canonicalItemImageId(
      options.singleImageFallback,
      "singleImageFallback",
    );
    if (images.length > 1) {
      throw new Error(
        "singleImageFallback cannot be used with a multi-image item",
      );
    }
    if (itemImageIds.size === 1 && !itemImageIds.has(fallbackItemImageId)) {
      throw new Error(
        "singleImageFallback must match the registered item image",
      );
    }
    itemImageIds.add(fallbackItemImageId);
  }

  return Object.freeze({
    canvasIdForItemImage(itemImageId: string | bigint): string {
      const canonical = canonicalItemImageId(itemImageId, "itemImageId");
      const canvasId = byItemImage.get(canonical);
      if (canvasId !== undefined) return canvasId;
      if (fallbackItemImageId === canonical && byCanvas.size === 0) return "";
      throw new Error(`Item image '${canonical}' is not part of this item`);
    },

    hasItemImageId(itemImageId: string | bigint): boolean {
      let canonical: string;
      try {
        canonical = canonicalItemImageId(itemImageId, "itemImageId");
      } catch {
        return false;
      }
      return itemImageIds.has(canonical);
    },

    itemImageIdForCanvas(canvasId: string): string {
      const strictId = strictCanvasId(canvasId, "canvasId");
      const itemImageId = byCanvas.get(strictId);
      if (itemImageId !== undefined) return itemImageId;
      if (fallbackItemImageId !== "") return fallbackItemImageId;
      throw new Error(`Canvas '${strictId}' is not part of this item`);
    },
  });
}
