import { createClient } from "@connectrpc/connect";
import { ItemService } from "../proto/scribe/v1/item_connect";
import { type Item } from "../proto/scribe/v1/item_pb";
import { getTransport } from "./transport";
import { scribeFetch, scribePath } from "./http";
import { readFileBytes, uint64ToString } from "../lib/util";

function client() {
  return createClient(ItemService, getTransport());
}

export async function listItems(): Promise<Item[]> {
  const resp = await client().listItems({});
  return resp.items;
}

export async function createItemFromManifest(manifestUrl: string): Promise<{ item: Item; firstItemImageId: string }> {
  const resp = await client().createItem({
    name: manifestUrl,
    sourceType: "manifest",
    sourceUrl: manifestUrl,
  });
  if (!resp.item) throw new Error("no item in response");
  const firstImage = resp.item.images[0];
  const firstItemImageId = firstImage ? uint64ToString(firstImage.id) : "";
  return { item: resp.item, firstItemImageId };
}

export async function uploadItemImages(files: File[]): Promise<Item> {
  let itemId = "";
  let item: Item | undefined;
  for (let i = 0; i < files.length; i++) {
    const file = files[i];
    const imageData = await readFileBytes(file);
    const resp = await client().uploadItemImage({
      itemId,
      name: files[0].name,
      imageData,
      filename: file.name,
      sequence: i + 1,
    });
    if (!resp.item) throw new Error("no item in response");
    item = resp.item;
    itemId = item.id;
  }
  if (!item) throw new Error("no files provided");
  return item;
}

export async function getItem(itemId: string): Promise<Item> {
  const resp = await client().getItem({ itemId });
  if (!resp.item) throw new Error("no item in response");
  return resp.item;
}

export async function deleteItem(itemId: string): Promise<void> {
  await client().deleteItem({ itemId });
}

export interface ItemProviderCallAudit {
  id: number;
  itemId: string;
  itemImageId?: string;
  itemImageSequence?: number;
  itemImageLabel?: string;
  sessionId?: string;
  contextId?: string;
  provider: string;
  model: string;
  operation: string;
  prompt?: string;
  requestJson?: string;
  responseJson?: string;
  errorMessage?: string;
  httpStatus?: number;
  createdAt: string;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function optionalString(value: unknown): string | undefined {
  return typeof value === "string" ? value : undefined;
}

function optionalNumber(value: unknown): number | undefined {
  if (value == null) return undefined;
  const parsed = Number(value);
  return Number.isFinite(parsed) ? parsed : undefined;
}

function parseAudit(value: unknown, fallbackItemId: string): ItemProviderCallAudit {
  if (!isRecord(value)) {
    throw new Error("invalid provider call audit response");
  }
  const id = Number(value.id ?? 0);
  const httpStatus = optionalNumber(value.http_status);
  const itemImageSequence = optionalNumber(value.item_image_sequence);
  return {
    id: Number.isFinite(id) ? id : 0,
    itemId: String(value.item_id ?? value.itemId ?? fallbackItemId),
    itemImageId: value.item_image_id == null ? undefined : String(value.item_image_id),
    itemImageSequence,
    itemImageLabel: optionalString(value.item_image_label),
    sessionId: optionalString(value.session_id),
    contextId: value.context_id == null ? undefined : String(value.context_id),
    provider: String(value.provider ?? ""),
    model: String(value.model ?? ""),
    operation: String(value.operation ?? ""),
    prompt: optionalString(value.prompt),
    requestJson: optionalString(value.request_json),
    responseJson: optionalString(value.response_json),
    errorMessage: optionalString(value.error_message),
    httpStatus,
    createdAt: String(value.created_at ?? ""),
  };
}

export async function listItemProviderCallAudits(itemId: string, limit = 100): Promise<ItemProviderCallAudit[]> {
  const resp = await scribeFetch(scribePath(`/v1/items/${encodeURIComponent(itemId)}/provider-call-audits?limit=${encodeURIComponent(String(limit))}`));
  if (!resp.ok) {
    throw new Error(`failed to load item logs (${resp.status})`);
  }
  const body: unknown = await resp.json();
  if (!isRecord(body) || !Array.isArray(body.audits)) {
    throw new Error("invalid provider call audits response");
  }
  return body.audits.map((audit) => parseAudit(audit, itemId));
}
