import { createClient } from "@connectrpc/connect";
import { ItemService } from "../proto/scribe/v1/item_connect";
import { type Item } from "../proto/scribe/v1/item_pb";
import { getTransport } from "./transport";
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

export async function listItemProviderCallAudits(itemId: string, limit = 100): Promise<ItemProviderCallAudit[]> {
  const resp = await fetch(`/v1/items/${encodeURIComponent(itemId)}/provider-call-audits?limit=${encodeURIComponent(String(limit))}`);
  if (!resp.ok) {
    throw new Error(`failed to load item logs (${resp.status})`);
  }
  const body = await resp.json() as { audits?: Array<Record<string, unknown>> };
  return (body.audits ?? []).map((audit) => ({
    id: Number(audit.id ?? 0),
    itemId: String(audit.item_id ?? audit.itemId ?? itemId),
    itemImageId: audit.item_image_id == null ? undefined : String(audit.item_image_id),
    itemImageSequence: audit.item_image_sequence == null ? undefined : Number(audit.item_image_sequence),
    itemImageLabel: typeof audit.item_image_label === "string" ? audit.item_image_label : undefined,
    sessionId: typeof audit.session_id === "string" ? audit.session_id : undefined,
    contextId: audit.context_id == null ? undefined : String(audit.context_id),
    provider: String(audit.provider ?? ""),
    model: String(audit.model ?? ""),
    operation: String(audit.operation ?? ""),
    prompt: typeof audit.prompt === "string" ? audit.prompt : undefined,
    requestJson: typeof audit.request_json === "string" ? audit.request_json : undefined,
    responseJson: typeof audit.response_json === "string" ? audit.response_json : undefined,
    errorMessage: typeof audit.error_message === "string" ? audit.error_message : undefined,
    httpStatus: audit.http_status == null ? undefined : Number(audit.http_status),
    createdAt: String(audit.created_at ?? ""),
  }));
}
