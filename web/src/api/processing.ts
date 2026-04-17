import { createClient } from "@connectrpc/connect";
import { ImageProcessingService } from "../proto/scribe/v1/process_connect";
import {
  type OCRRun,
  type ProcessImageResponse,
  type ReprocessItemImageResponse,
  type SaveOCREditsResponse,
} from "../proto/scribe/v1/process_pb";
import { getTransport } from "./transport";
import { readFileBytes } from "../lib/util";

function client() {
  return createClient(ImageProcessingService, getTransport());
}

export async function processImageURL(imageUrl: string, contextId = BigInt(0)): Promise<ProcessImageResponse> {
  return client().processImageURL({ imageUrl, contextId });
}

export async function processImageUpload(file: File, contextId = BigInt(0)): Promise<ProcessImageResponse> {
  const imageData = await readFileBytes(file);
  return client().processImageUpload({
    imageData,
    filename: file.name,
    contextId,
  });
}

export async function processHOCR(hocr: string, imageUrl = "", imageData?: Uint8Array, filename = ""): Promise<ProcessImageResponse> {
  return client().processHOCR({ hocr, imageUrl, imageData, filename });
}

export async function getOCRRun(itemImageId: string): Promise<OCRRun> {
  return client().getOCRRun({
    itemImageId: BigInt(itemImageId),
  });
}

export async function saveOCREdits(sessionId: string, itemImageId: string, correctedHocr: string, editCount: number): Promise<SaveOCREditsResponse> {
  return client().saveOCREdits({
    sessionId,
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
