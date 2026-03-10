import { createPromiseClient } from "@connectrpc/connect";
import { protoInt64 } from "@bufbuild/protobuf";
import { ImageProcessingService } from "../proto/scribe/v1/process_connect";
import {
  GetOCRRunRequest,
  ProcessHOCRRequest,
  ProcessImageUploadRequest,
  ProcessImageURLRequest,
  ReprocessItemImageRequest,
  SaveOCREditsRequest,
  type OCRRun,
  type ProcessImageResponse,
  type ReprocessItemImageResponse,
  type SaveOCREditsResponse,
} from "../proto/scribe/v1/process_pb";
import { getTransport } from "./transport";
import { readFileBytes } from "../lib/util";

function client() {
  return createPromiseClient(ImageProcessingService, getTransport());
}

export async function processImageURL(imageUrl: string): Promise<ProcessImageResponse> {
  return client().processImageURL(new ProcessImageURLRequest({ imageUrl }));
}

export async function processImageUpload(file: File): Promise<ProcessImageResponse> {
  const imageData = await readFileBytes(file);
  return client().processImageUpload(new ProcessImageUploadRequest({
    imageData,
    filename: file.name,
  }));
}

export async function processHOCR(hocr: string, imageUrl = "", imageData?: Uint8Array, filename = ""): Promise<ProcessImageResponse> {
  return client().processHOCR(new ProcessHOCRRequest({ hocr, imageUrl, imageData, filename }));
}

export async function getOCRRun(itemImageId: string): Promise<OCRRun> {
  return client().getOCRRun(new GetOCRRunRequest({
    itemImageId: protoInt64.parse(itemImageId),
  }));
}

export async function saveOCREdits(sessionId: string, itemImageId: string, correctedHocr: string, editCount: number): Promise<SaveOCREditsResponse> {
  return client().saveOCREdits(new SaveOCREditsRequest({
    sessionId,
    itemImageId: protoInt64.parse(itemImageId),
    correctedHocr,
    editCount,
  }));
}

export async function reprocessItemImage(itemImageId: string, contextId = 0): Promise<ReprocessItemImageResponse> {
  return client().reprocessItemImage(new ReprocessItemImageRequest({
    itemImageId: protoInt64.parse(itemImageId),
    contextId: BigInt(contextId),
  }));
}
