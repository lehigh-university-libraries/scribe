// @vitest-environment happy-dom

import { describe, expect, it } from "vitest";
import { create } from "@bufbuild/protobuf";

import type { Context } from "../proto/scribe/v1/context_pb";
import { ItemSummarySchema } from "../proto/scribe/v1/item_pb";
import { setHTML } from "../lib/util";
import { contextOptions, renderItemActions, renderItemCard } from "./shell_helpers";

describe("contextOptions", () => {
  it("keeps resolver-backed Default selected instead of a concrete default row", () => {
    const markup = contextOptions([
      { id: 0n, name: "synthetic", isDefault: true },
      { id: 11n, name: "Scribe Custom", isDefault: true },
      { id: 12n, name: "Workspace default", isDefault: true },
      { id: 13n, name: "Gemini Pro", isDefault: false },
    ] as Context[]).map((option) => option.value).join("");

    expect(markup).not.toContain('value="0"');
    expect(markup).not.toContain(" selected");
    expect(markup).toContain('value="11"');
    expect(markup).toContain('value="13"');
  });
});

describe("item delete action", () => {
  it("is the final, accessible destructive action in cards and sidebar rows", () => {
    const item = create(ItemSummarySchema, {
      createdAt: "2026-08-04T00:00:00Z",
      id: "item-7",
      imageCount: 1n,
      name: "Folio 7",
      previewImage: { id: 42n },
      sourceType: "image",
    });

    for (const markup of [renderItemCard(item), renderItemActions(item)]) {
      const container = document.createElement("div");
      setHTML(container, markup);
      const actionButtons = Array.from(container.querySelectorAll<HTMLButtonElement>("button"));
      const deleteButton = actionButtons.at(-1);

      expect(deleteButton?.dataset.itemDelete).toBe("item-7");
      expect(deleteButton?.getAttribute("aria-label")).toBe("Delete item Folio 7");
      expect(deleteButton?.classList.contains("bg-destructive")).toBe(true);
      expect(deleteButton?.classList.contains("text-background")).toBe(true);
      expect(deleteButton?.querySelector('svg[aria-hidden="true"]')).not.toBeNull();
      expect(deleteButton?.textContent?.trim()).toBe("Delete");
    }
  });
});
