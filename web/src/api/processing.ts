import { createClient } from "@connectrpc/connect";
import { ImageProcessingService } from "../proto/scribe/v1/process_connect";
import {
  type GetOCRRunResponse,
  type ProcessHOCRResponse,
  type ProcessImageURLResponse,
  type ProcessImageUploadResponse,
  type ReprocessItemImageResponse,
  type SaveOCREditsResponse,
} from "../proto/scribe/v1/process_pb";
import { getTransport } from "./transport";
import { readFileBytes } from "../lib/util";

function client() {
  return createClient(ImageProcessingService, getTransport());
}

export async function processImageURL(imageUrl: string, contextId = BigInt(0)): Promise<ProcessImageURLResponse> {
  return client().processImageURL({ imageUrl, contextId });
}

export async function processImageUpload(file: File, contextId = BigInt(0)): Promise<ProcessImageUploadResponse> {
  const imageData = await readFileBytes(file);
  return client().processImageUpload({
    imageData,
    filename: file.name,
    contextId,
  });
}

export async function processHOCR(hocr: string, imageUrl = "", imageData?: Uint8Array, filename = ""): Promise<ProcessHOCRResponse> {
  return client().processHOCR({ hocr, imageUrl, imageData, filename });
}

export async function getOCRRun(itemImageId: string): Promise<GetOCRRunResponse> {
  return client().getOCRRun({
    itemImageId: BigInt(itemImageId),
  });
}

export async function saveOCREdits(itemImageId: string, correctedHocr: string, editCount: number): Promise<SaveOCREditsResponse> {
  return client().saveOCREdits({
    itemImageId: BigInt(itemImageId),
    correctedHocr,
    editCount,
  });
}

export async function reprocessItemImage(itemImageId: string, contextId = 0): Promise<ReprocessItemImageResponse> {
  return client().reprocessItemImage({
    itemImageId: BigInt(itemImageId),
    contextId: BigInt(contextId),
  });
}
