import { createClient } from "@connectrpc/connect";
import { AnnotationService } from "../proto/scribe/v1/annotation_connect";
import { scribeFetch } from "./http";
import { getTransport } from "./transport";

function client() {
  return createClient(AnnotationService, getTransport());
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function stringValue(value: unknown, field: string): string {
  if (typeof value === "string") return value;
  throw new Error(`invalid ${field} in annotation response`);
}

function idString(value: unknown, field: string): string {
  if (typeof value === "string" || typeof value === "number" || typeof value === "bigint") {
    return `${value}`;
  }
  throw new Error(`invalid ${field} in annotation response`);
}

export async function searchAnnotations(canvasUri: string): Promise<unknown> {
  const resp = await client().searchAnnotations({ canvasUri });
  return JSON.parse(resp.annotationPageJson);
}

export async function getAnnotation(annotationId: string): Promise<unknown> {
  const resp = await client().getAnnotation({ id: annotationId });
  return JSON.parse(resp.annotationJson);
}

export async function createAnnotation(annotationJson: string): Promise<unknown> {
  const resp = await client().createAnnotation({ annotationJson });
  return JSON.parse(resp.annotationJson);
}

export async function updateAnnotation(annotationJson: string): Promise<unknown> {
  const resp = await client().updateAnnotation({ annotationJson });
  return JSON.parse(resp.annotationJson);
}

export async function deleteAnnotation(uri: string): Promise<void> {
  await client().deleteAnnotation({ uri });
}

export async function publishItemImageEdits(itemImageId: string): Promise<{ itemImageId: string; canvasUri: string; annotationPageJson: string; publishedAt: string }> {
  const resp = await scribeFetch("/scribe.v1.AnnotationService/PublishItemImageEdits", {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
    },
    body: JSON.stringify({ itemImageId }),
  });
  if (!resp.ok) {
    throw new Error(`publish failed: ${resp.status}`);
  }
  const body: unknown = await resp.json();
  if (!isRecord(body)) {
    throw new Error("invalid publish response");
  }
  return {
    itemImageId: idString(body.itemImageId, "itemImageId"),
    canvasUri: stringValue(body.canvasUri, "canvasUri"),
    annotationPageJson: stringValue(body.annotationPageJson, "annotationPageJson"),
    publishedAt: stringValue(body.publishedAt, "publishedAt"),
  };
}

export async function enrichAnnotation(
  scope: "line" | "page",
  annotationJson: string,
  contextId = 0,
): Promise<unknown> {
  const resp = await client().enrichAnnotation({
    scope,
    annotationJson,
    contextId: BigInt(contextId),
  });
  return JSON.parse(resp.annotationJson);
}

export async function splitLineIntoWords(
  annotationJson: string,
  words: string[] = [],
): Promise<unknown> {
  const resp = await client().splitLineIntoWords({ annotationJson, words });
  return JSON.parse(resp.annotationPageJson);
}

export async function splitLineIntoTwoLines(
  annotationJson: string,
  splitAtWord = 0,
): Promise<unknown[]> {
  const resp = await client().splitLineIntoTwoLines({ annotationJson, splitAtWord });
  return resp.annotationJsons.map((j) => JSON.parse(j));
}

export async function joinLines(annotationJsons: string[]): Promise<unknown> {
  const resp = await client().joinLines({ annotationJsons });
  return JSON.parse(resp.annotationJson);
}

export async function joinWordsIntoLine(annotationJsons: string[]): Promise<unknown> {
  const resp = await client().joinWordsIntoLine({ annotationJsons });
  return JSON.parse(resp.annotationJson);
}

export async function crosswalkToPlainText(annotationPageJson: string, annotationJson = ""): Promise<{ format: string; content: string }> {
  const resp = await client().crosswalkToPlainText({ annotationPageJson, annotationJson });
  return { format: resp.format, content: resp.content };
}

export async function crosswalkToHOCR(annotationPageJson: string, annotationJson = ""): Promise<{ format: string; content: string }> {
  const resp = await client().crosswalkToHOCR({ annotationPageJson, annotationJson });
  return { format: resp.format, content: resp.content };
}

export async function crosswalkToPageXML(annotationPageJson: string, annotationJson = ""): Promise<{ format: string; content: string }> {
  const resp = await client().crosswalkToPageXML({ annotationPageJson, annotationJson });
  return { format: resp.format, content: resp.content };
}

export async function crosswalkToALTOXML(annotationPageJson: string, annotationJson = ""): Promise<{ format: string; content: string }> {
  const resp = await client().crosswalkToALTOXML({ annotationPageJson, annotationJson });
  return { format: resp.format, content: resp.content };
}

export const annotationClient = {
  searchAnnotations,
  getAnnotation,
  createAnnotation,
  updateAnnotation,
  deleteAnnotation,
  publishItemImageEdits,
  enrichAnnotation,
  splitLineIntoWords,
  splitLineIntoTwoLines,
  joinLines,
  joinWordsIntoLine,
  crosswalkToPlainText,
  crosswalkToHOCR,
  crosswalkToPageXML,
  crosswalkToALTOXML,
};
