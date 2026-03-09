import { createPromiseClient } from "@connectrpc/connect";
import { ItemService } from "../proto/hocredit/v1/item_connect";
import {
  CreateItemRequest,
  DeleteItemRequest,
  ListItemsRequest,
  UploadItemImageRequest,
  type Item,
} from "../proto/hocredit/v1/item_pb";
import { getTransport } from "./transport";
import { readFileBytes, uint64ToString } from "../lib/util";

function client() {
  return createPromiseClient(ItemService, getTransport());
}

export async function listItems(): Promise<Item[]> {
  const resp = await client().listItems(new ListItemsRequest());
  return resp.items;
}

export async function createItemFromManifest(manifestUrl: string): Promise<{ item: Item; firstItemImageId: string }> {
  const resp = await client().createItem(new CreateItemRequest({
    name: manifestUrl,
    sourceType: "manifest",
    sourceUrl: manifestUrl,
  }));
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
    const resp = await client().uploadItemImage(new UploadItemImageRequest({
      itemId,
      name: files[0].name,
      imageData,
      filename: file.name,
      sequence: i + 1,
    }));
    if (!resp.item) throw new Error("no item in response");
    item = resp.item;
    itemId = item.id;
  }
  if (!item) throw new Error("no files provided");
  return item;
}

export async function deleteItem(itemId: string): Promise<void> {
  await client().deleteItem(new DeleteItemRequest({ itemId }));
}
