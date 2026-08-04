import { Code, ConnectError, createClient } from "@connectrpc/connect";
import { AnnotationService } from "../proto/scribe/v1/annotation_connect";
import { AnnotationExportFormat, AnnotationGranularity } from "../proto/scribe/v1/annotation_pb";
import type {
  CanonicalIIIFAnnotationPage,
  ScribeAnnotationClient,
} from "mirador-scribe";
import { parseIIIFJSON } from "../lib/iiif-json";
import { getTransport } from "./transport";

function client() {
  return createClient(AnnotationService, getTransport());
}

export type AnnotationPage = CanonicalIIIFAnnotationPage;

export interface AnnotationPageSnapshot {
  page: AnnotationPage;
  revision: string;
  updatedAt: string;
  canvasUri: string;
}

export class AnnotationRevisionConflictError extends Error {
  constructor(expectedRevision: string, cause: ConnectError) {
    super(`annotation page revision conflict at expected revision ${expectedRevision}`, { cause });
    this.name = "RevisionConflict";
  }
}

export class TerminalAnnotationEnrichmentError extends Error {
  readonly scribeBatchDisposition = "stop" as const;

  constructor(message: string, cause: ConnectError) {
    super(message, { cause });
    this.name = "TerminalAnnotationEnrichmentError";
  }
}

function parseAnnotationPage(raw: string): AnnotationPage {
  const value = parseIIIFJSON(raw);
  if (value === null || typeof value !== "object" || Array.isArray(value)) {
    throw new Error("annotation service returned a non-object page");
  }
  const page = value as Record<string, unknown>;
  if (page.type !== "AnnotationPage" || typeof page.id !== "string" || !Array.isArray(page.items)) {
    throw new Error("annotation service returned an invalid IIIF AnnotationPage");
  }
  if (!page.items.every((item) => (
    item !== null
      && typeof item === "object"
      && !Array.isArray(item)
      && typeof (item as Record<string, unknown>).id === "string"
  ))) {
    throw new Error("annotation service returned an AnnotationPage with invalid items");
  }
  return page as AnnotationPage;
}

export async function getAnnotationPage(itemImageId: string): Promise<AnnotationPageSnapshot> {
  const resp = await client().getAnnotationPage({ itemImageId: BigInt(itemImageId) });
  return {
    canvasUri: resp.canvasUri,
    page: parseAnnotationPage(resp.annotationPageJson),
    revision: resp.revision.toString(),
    updatedAt: resp.updatedAt,
  };
}

export async function saveAnnotationPage(
  itemImageId: string,
  annotationPageJson: string,
  expectedRevision: string,
): Promise<AnnotationPageSnapshot> {
  try {
    const resp = await client().saveAnnotationPage({
      annotationPageJson,
      expectedRevision: BigInt(expectedRevision || "0"),
      itemImageId: BigInt(itemImageId),
    });
    return {
      canvasUri: resp.canvasUri,
      page: parseAnnotationPage(resp.annotationPageJson),
      revision: resp.revision.toString(),
      updatedAt: resp.updatedAt,
    };
  } catch (error) {
    const connectError = ConnectError.from(error);
    if (connectError.code === Code.Aborted) {
      throw new AnnotationRevisionConflictError(expectedRevision || "0", connectError);
    }
    throw error;
  }
}

export async function searchAnnotations({
  itemImageId,
  canvasUri = "",
  granularity = AnnotationGranularity.ALL,
}: {
  itemImageId: string;
  canvasUri?: string;
  granularity?: AnnotationGranularity;
}): Promise<AnnotationPageSnapshot> {
  const resp = await client().searchAnnotations({
    canvasUri,
    granularity,
    itemImageId: BigInt(itemImageId),
  });
  return {
    canvasUri,
    page: parseAnnotationPage(resp.annotationPageJson),
    revision: resp.revision.toString(),
    updatedAt: "",
  };
}

export async function getAnnotation(itemImageId: string, annotationId: string): Promise<unknown> {
  const resp = await client().getAnnotation({ id: annotationId, itemImageId: BigInt(itemImageId) });
  return parseIIIFJSON(resp.annotationJson);
}

export async function publishItemImageEdits(
  itemImageId: string,
  expectedRevision: string,
): Promise<{
  itemImageId: string;
  canvasUri: string;
  annotationPageJson: string;
  publishedAt: string;
  publishedRevision: string;
  publicUrl: string;
}> {
  const resp = await client().publishItemImageEdits({
    expectedRevision: BigInt(expectedRevision),
    itemImageId: BigInt(itemImageId),
  });
  return {
    itemImageId: resp.itemImageId.toString(),
    canvasUri: resp.canvasUri,
    annotationPageJson: resp.annotationPageJson,
    publishedAt: resp.publishedAt,
    publishedRevision: resp.publishedRevision.toString(),
    publicUrl: resp.publicUrl,
  };
}

export async function enrichAnnotation(
  itemImageId: string,
  scope: "line" | "page",
  annotationJson: string,
  contextId: string | number | bigint = 0,
): Promise<unknown> {
  try {
    const resp = await client().enrichAnnotation({
      itemImageId: BigInt(itemImageId),
      scope,
      annotationJson,
      contextId: BigInt(contextId),
    });
    return parseIIIFJSON(resp.annotationJson);
  } catch (error) {
    const connectError = ConnectError.from(error);
    if (connectError.code === Code.FailedPrecondition) {
      throw new TerminalAnnotationEnrichmentError(
        "The transcription provider rejected the selected context. Check its model and provider key, then try again.",
        connectError,
      );
    }
    if (connectError.code === Code.Unavailable) {
      throw new TerminalAnnotationEnrichmentError(
        "The transcription provider is temporarily unavailable. Try again shortly.",
        connectError,
      );
    }
    throw error;
  }
}

export async function splitLineIntoWords(
  itemImageId: string,
  annotationPageJson: string,
  selectedAnnotationId: string,
  words: string[] = [],
): Promise<AnnotationPage> {
  const resp = await client().splitLineIntoWords({
    annotationPageJson,
    itemImageId: BigInt(itemImageId),
    selectedAnnotationId,
    words,
  });
  return parseAnnotationPage(resp.annotationPageJson);
}

export async function splitLineIntoTwoLines(
  itemImageId: string,
  annotationPageJson: string,
  selectedAnnotationId: string,
  splitAtWord = 0,
): Promise<AnnotationPage> {
  const resp = await client().splitLineIntoTwoLines({
    annotationPageJson,
    itemImageId: BigInt(itemImageId),
    selectedAnnotationId,
    splitAtWord,
  });
  return parseAnnotationPage(resp.annotationPageJson);
}

export async function joinLines(
  itemImageId: string,
  annotationPageJson: string,
  selectedAnnotationIds: string[],
): Promise<AnnotationPage> {
  const resp = await client().joinLines({
    annotationPageJson,
    itemImageId: BigInt(itemImageId),
    selectedAnnotationIds,
  });
  return parseAnnotationPage(resp.annotationPageJson);
}

export async function joinWordsIntoLine(
  itemImageId: string,
  annotationPageJson: string,
  selectedAnnotationIds: string[],
): Promise<AnnotationPage> {
  const resp = await client().joinWordsIntoLine({
    annotationPageJson,
    itemImageId: BigInt(itemImageId),
    selectedAnnotationIds,
  });
  return parseAnnotationPage(resp.annotationPageJson);
}

export async function exportAnnotationPage(
  itemImageId: string,
  expectedRevision: string,
  format: AnnotationExportFormat,
): Promise<{ itemImageId: string; revision: string; mediaType: string; content: Uint8Array; filename: string }> {
  const resp = await client().exportAnnotationPage({
    expectedRevision: BigInt(expectedRevision),
    format,
    itemImageId: BigInt(itemImageId),
  });
  return {
    content: resp.content,
    filename: resp.filename,
    itemImageId: resp.itemImageId.toString(),
    mediaType: resp.mediaType,
    revision: resp.revision.toString(),
  };
}

const adapterAnnotationClient = {
  getAnnotationPage,
  saveAnnotationPage,
  enrichAnnotation,
  splitLineIntoWords,
  splitLineIntoTwoLines,
  joinLines,
  joinWordsIntoLine,
} satisfies ScribeAnnotationClient;

export const annotationClient = {
  ...adapterAnnotationClient,
  searchAnnotations,
  getAnnotation,
  publishItemImageEdits,
  exportAnnotationPage,
};
