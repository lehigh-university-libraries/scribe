import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  enrichAnnotation: vi.fn(),
  exportAnnotationPage: vi.fn(),
  getAnnotation: vi.fn(),
  joinLines: vi.fn(),
  joinWordsIntoLine: vi.fn(),
  searchAnnotations: vi.fn(),
  splitLineIntoTwoLines: vi.fn(),
  splitLineIntoWords: vi.fn(),
}));

vi.mock("@connectrpc/connect", async (importOriginal) => {
  const original = await importOriginal<typeof import("@connectrpc/connect")>();
  return {
    ...original,
    createClient: () => mocks,
  };
});

vi.mock("./transport", () => ({
  getTransport: () => ({}),
}));

import {
  enrichAnnotation,
  exportAnnotationPage,
  getAnnotation,
  joinLines,
  joinWordsIntoLine,
  searchAnnotations,
  splitLineIntoTwoLines,
  splitLineIntoWords,
} from "./annotations";
import { AnnotationExportFormat } from "../proto/scribe/v1/annotation_pb";

const page = {
  id: "https://scribe.test/presentation/v3/item-image-91/canvas/page-1/annotations",
  items: [
    { id: "https://scribe.test/annotations/a", type: "Annotation" },
    { id: "https://scribe.test/annotations/b", type: "Annotation" },
  ],
  type: "AnnotationPage" as const,
  workflow: { unsaved: true },
};

describe("structural annotation API", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    const response = { annotationPageJson: JSON.stringify(page) };
    mocks.joinLines.mockResolvedValue(response);
    mocks.joinWordsIntoLine.mockResolvedValue(response);
    mocks.splitLineIntoTwoLines.mockResolvedValue(response);
    mocks.splitLineIntoWords.mockResolvedValue(response);
    mocks.enrichAnnotation.mockResolvedValue({ annotationJson: JSON.stringify(page.items[0]) });
    mocks.exportAnnotationPage.mockResolvedValue({
      content: new Uint8Array([79, 67, 82]),
      filename: "item-91.txt",
      itemImageId: 91n,
      mediaType: "text/plain; charset=utf-8",
      revision: 8n,
    });
    mocks.getAnnotation.mockResolvedValue({ annotationJson: JSON.stringify(page.items[0]) });
    mocks.searchAnnotations.mockResolvedValue({
      annotationPageJson: JSON.stringify(page),
      revision: 8n,
    });
  });

  it("binds enrichment authorization to the requested item image", async () => {
    const annotationJson = JSON.stringify(page.items[0]);

    await expect(enrichAnnotation("91", "line", annotationJson, "23"))
      .resolves.toEqual(page.items[0]);

    expect(mocks.enrichAnnotation).toHaveBeenCalledWith({
      annotationJson,
      contextId: 23n,
      itemImageId: 91n,
      scope: "line",
    });
  });

  it("exports one exact committed item-image revision", async () => {
    await expect(exportAnnotationPage(
      "91",
      "8",
      AnnotationExportFormat.PLAIN_TEXT,
    )).resolves.toMatchObject({
      filename: "item-91.txt",
      itemImageId: "91",
      mediaType: "text/plain; charset=utf-8",
      revision: "8",
    });

    expect(mocks.exportAnnotationPage).toHaveBeenCalledWith({
      expectedRevision: 8n,
      format: AnnotationExportFormat.PLAIN_TEXT,
      itemImageId: 91n,
    });
  });

  it("uses item-image identity for search and exact annotation reads", async () => {
    await expect(searchAnnotations({
      canvasUri: "https://source.test/canvas/1?choice=default",
      itemImageId: "91",
    })).resolves.toMatchObject({ page, revision: "8" });
    await expect(getAnnotation("91", page.items[0].id)).resolves.toEqual(page.items[0]);

    expect(mocks.searchAnnotations).toHaveBeenCalledWith(expect.objectContaining({
      canvasUri: "https://source.test/canvas/1?choice=default",
      itemImageId: 91n,
    }));
    expect(mocks.getAnnotation).toHaveBeenCalledWith({
      id: page.items[0].id,
      itemImageId: 91n,
    });
  });

  it("does not round unknown numeric properties in returned IIIF JSON", async () => {
    mocks.searchAnnotations.mockResolvedValue({
      annotationPageJson: `{
        "id":"https://scribe.test/presentation/v3/item-image-91/canvas/page-1/annotations",
        "type":"AnnotationPage",
        "items":[{"id":"https://scribe.test/annotations/a","type":"Annotation"}],
        "ex:largeInteger":9007199254740993,
        "ex:preciseDecimal":0.123456789012345678901
      }`,
      revision: 8n,
    });

    const result = await searchAnnotations({ itemImageId: "91" });

    expect(result.page["ex:largeInteger"]).toBeInstanceOf(String);
    expect((result.page["ex:largeInteger"] as String).valueOf()).toBe("9007199254740993");
    expect((result.page["ex:preciseDecimal"] as String).valueOf()).toBe("0.123456789012345678901");
  });

  it("sends a complete page and selected ID for split transforms", async () => {
    const annotationPageJson = JSON.stringify(page);

    await expect(splitLineIntoWords("91", annotationPageJson, page.items[0].id, ["al", "pha"]))
      .resolves.toEqual(page);
    await expect(splitLineIntoTwoLines("91", annotationPageJson, page.items[0].id, 1))
      .resolves.toEqual(page);

    expect(mocks.splitLineIntoWords).toHaveBeenCalledWith({
      annotationPageJson,
      itemImageId: 91n,
      selectedAnnotationId: page.items[0].id,
      words: ["al", "pha"],
    });
    expect(mocks.splitLineIntoTwoLines).toHaveBeenCalledWith({
      annotationPageJson,
      itemImageId: 91n,
      selectedAnnotationId: page.items[0].id,
      splitAtWord: 1,
    });
  });

  it("sends a complete page and selected IDs for join transforms", async () => {
    const annotationPageJson = JSON.stringify(page);
    const selectedAnnotationIds = page.items.map(({ id }) => id);

    await expect(joinLines("91", annotationPageJson, selectedAnnotationIds)).resolves.toEqual(page);
    await expect(joinWordsIntoLine("91", annotationPageJson, selectedAnnotationIds)).resolves.toEqual(page);

    expect(mocks.joinLines).toHaveBeenCalledWith({
      annotationPageJson,
      itemImageId: 91n,
      selectedAnnotationIds,
    });
    expect(mocks.joinWordsIntoLine).toHaveBeenCalledWith({
      annotationPageJson,
      itemImageId: 91n,
      selectedAnnotationIds,
    });
  });
});
