import { createPromiseClient } from "@connectrpc/connect";
import { AnnotationService } from "../proto/scribe/v1/annotation_connect";
import {
  CreateAnnotationRequest,
  CrosswalkRequest,
  DeleteAnnotationRequest,
  EnrichAnnotationRequest,
  GetAnnotationRequest,
  JoinAnnotationsRequest,
  SearchAnnotationsRequest,
  SplitLineIntoTwoLinesRequest,
  SplitLineIntoWordsRequest,
  UpdateAnnotationRequest,
} from "../proto/scribe/v1/annotation_pb";
import { getTransport } from "./transport";

function client() {
  return createPromiseClient(AnnotationService, getTransport());
}

export async function searchAnnotations(canvasUri: string): Promise<unknown> {
  const resp = await client().searchAnnotations(new SearchAnnotationsRequest({ canvasUri }));
  return JSON.parse(resp.annotationPageJson);
}

export async function getAnnotation(annotationId: string): Promise<unknown> {
  const resp = await client().getAnnotation(new GetAnnotationRequest({ id: annotationId }));
  return JSON.parse(resp.annotationJson);
}

export async function createAnnotation(annotationJson: string): Promise<unknown> {
  const resp = await client().createAnnotation(new CreateAnnotationRequest({ annotationJson }));
  return JSON.parse(resp.annotationJson);
}

export async function updateAnnotation(annotationJson: string): Promise<unknown> {
  const resp = await client().updateAnnotation(new UpdateAnnotationRequest({ annotationJson }));
  return JSON.parse(resp.annotationJson);
}

export async function deleteAnnotation(uri: string): Promise<void> {
  await client().deleteAnnotation(new DeleteAnnotationRequest({ uri }));
}

export async function enrichAnnotation(
  scope: "line" | "page",
  annotationJson: string,
  contextId = 0,
): Promise<unknown> {
  const resp = await client().enrichAnnotation(new EnrichAnnotationRequest({
    scope,
    annotationJson,
    contextId: BigInt(contextId),
  }));
  return JSON.parse(resp.annotationJson);
}

export async function splitLineIntoWords(
  annotationJson: string,
  words: string[] = [],
): Promise<unknown> {
  const resp = await client().splitLineIntoWords(new SplitLineIntoWordsRequest({ annotationJson, words }));
  return JSON.parse(resp.annotationPageJson);
}

export async function splitLineIntoTwoLines(
  annotationJson: string,
  splitAtWord = 0,
): Promise<unknown[]> {
  const resp = await client().splitLineIntoTwoLines(new SplitLineIntoTwoLinesRequest({ annotationJson, splitAtWord }));
  return resp.annotationJsons.map((j) => JSON.parse(j));
}

export async function joinLines(annotationJsons: string[]): Promise<unknown> {
  const resp = await client().joinLines(new JoinAnnotationsRequest({ annotationJsons }));
  return JSON.parse(resp.annotationJson);
}

export async function joinWordsIntoLine(annotationJsons: string[]): Promise<unknown> {
  const resp = await client().joinWordsIntoLine(new JoinAnnotationsRequest({ annotationJsons }));
  return JSON.parse(resp.annotationJson);
}

export async function crosswalkToPlainText(annotationPageJson: string, annotationJson = ""): Promise<{ format: string; content: string }> {
  const resp = await client().crosswalkToPlainText(new CrosswalkRequest({ annotationPageJson, annotationJson }));
  return { format: resp.format, content: resp.content };
}

export async function crosswalkToHOCR(annotationPageJson: string, annotationJson = ""): Promise<{ format: string; content: string }> {
  const resp = await client().crosswalkToHOCR(new CrosswalkRequest({ annotationPageJson, annotationJson }));
  return { format: resp.format, content: resp.content };
}

export async function crosswalkToPageXML(annotationPageJson: string, annotationJson = ""): Promise<{ format: string; content: string }> {
  const resp = await client().crosswalkToPageXML(new CrosswalkRequest({ annotationPageJson, annotationJson }));
  return { format: resp.format, content: resp.content };
}

export async function crosswalkToALTOXML(annotationPageJson: string, annotationJson = ""): Promise<{ format: string; content: string }> {
  const resp = await client().crosswalkToALTOXML(new CrosswalkRequest({ annotationPageJson, annotationJson }));
  return { format: resp.format, content: resp.content };
}

export const annotationClient = {
  searchAnnotations,
  getAnnotation,
  createAnnotation,
  updateAnnotation,
  deleteAnnotation,
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
