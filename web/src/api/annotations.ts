import { createClient } from "@connectrpc/connect";
import { AnnotationService } from "../proto/scribe/v1/annotation_connect";
import { getTransport } from "./transport";

function client() {
  return createClient(AnnotationService, getTransport());
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
  const resp = await fetch("/scribe.v1.AnnotationService/PublishItemImageEdits", {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
    },
    body: JSON.stringify({ itemImageId: Number(itemImageId) }),
  });
  if (!resp.ok) {
    throw new Error(`publish failed: ${resp.status}`);
  }
  return resp.json() as Promise<{ itemImageId: string; canvasUri: string; annotationPageJson: string; publishedAt: string }>;
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
