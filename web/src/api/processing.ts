import { Code, ConnectError, createClient } from "@connectrpc/connect";
import { ImageProcessingService } from "../proto/scribe/v1/process_connect";
import {
  type GetOCRRunResponse,
  type ProcessHOCRResponse,
  type ProcessImageURLResponse,
  type ReprocessItemImageResponse,
} from "../proto/scribe/v1/process_pb";
import { getTransport } from "./transport";
import { sha256Hex } from "../lib/digest";

function client() {
  return createClient(ImageProcessingService, getTransport());
}

export async function processImageURL(imageUrl: string, contextId = BigInt(0)): Promise<ProcessImageURLResponse> {
  const normalizedURL = imageUrl.trim();
  if (!normalizedURL) throw new Error("image URL is required");
  const idempotencyKey = await sha256Hex(JSON.stringify(["image-url", normalizedURL, contextId.toString()]));
  return client().processImageURL({ imageUrl: normalizedURL, contextId, idempotencyKey });
}

export async function processHOCR(hocr: string, imageUrl = "", imageData?: Uint8Array, filename = ""): Promise<ProcessHOCRResponse> {
  const normalizedHOCR = hocr.trim();
  const normalizedURL = imageUrl.trim();
  const hasImageData = Boolean(imageData?.byteLength);
  if (!normalizedHOCR) throw new Error("hOCR is required");
  if (hasImageData === Boolean(normalizedURL)) throw new Error("exactly one image URL or image byte array is required");
  const normalizedFilename = filename.trim();
  const hocrDigest = await sha256Hex(normalizedHOCR);
  const imageDigest = imageData?.byteLength ? await sha256Hex(imageData) : "";
  const idempotencyKey = await sha256Hex(JSON.stringify(["hocr", hocrDigest, normalizedURL, imageDigest, normalizedFilename]));
  return client().processHOCR({ hocr: normalizedHOCR, imageUrl: normalizedURL, imageData, filename: normalizedFilename, idempotencyKey });
}

export async function getOCRRun(itemImageId: string): Promise<GetOCRRunResponse> {
  return client().getOCRRun({
    itemImageId: BigInt(itemImageId),
  });
}

export class ReprocessRevisionConflictError extends Error {
  constructor(expectedRevision: string | number, cause: ConnectError) {
    super(`The annotation page changed after revision ${expectedRevision}; reload it before reprocessing.`, { cause });
    this.name = "RevisionConflict";
  }
}

export async function reprocessItemImage(
  itemImageId: string,
  contextId: string | number | bigint,
  expectedRevision: string | number,
): Promise<ReprocessItemImageResponse> {
  try {
    return await client().reprocessItemImage({
      itemImageId: BigInt(itemImageId),
      contextId: BigInt(contextId),
      expectedRevision: BigInt(expectedRevision),
    });
  } catch (error) {
    const connectError = ConnectError.from(error);
    if (connectError.code === Code.Aborted) {
      throw new ReprocessRevisionConflictError(expectedRevision, connectError);
    }
    throw error;
  }
}
