import { createPromiseClient } from "@connectrpc/connect";
import { protoInt64 } from "@bufbuild/protobuf";
import { ImageProcessingService } from "../proto/scribe/v1/process_connect";
import {
  GetOCRRunRequest,
  ProcessImageUploadRequest,
  ProcessImageURLRequest,
  type OCRRun,
  type ProcessImageResponse,
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

export async function getOCRRun(itemImageId: string): Promise<OCRRun> {
  return client().getOCRRun(new GetOCRRunRequest({
    itemImageId: protoInt64.parse(itemImageId),
  }));
}
