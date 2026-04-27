import { beforeEach, describe, expect, it, vi } from "vitest";

import {
  applyWorkspaceToLocation,
  getCurrentWorkspaceId,
  syncWorkspaceSelectionFromLocation,
  setCurrentWorkspaceId,
  workspaceAwarePath,
  workspaceHeaders,
} from "./workspace";

type StoredValue = string | null;

function installWindow(url = "https://scribe.test/library"): Map<string, string> {
  const current = new URL(url);
  const storage = new Map<string, string>();
  const location = {
    get href() {
      return current.href;
    },
    get origin() {
      return current.origin;
    },
    get pathname() {
      return current.pathname;
    },
    get search() {
      return current.search;
    },
    get hash() {
      return current.hash;
    },
  };
  const history = {
    state: null,
    replaceState: vi.fn((_state: unknown, _title: string, nextUrl: string) => {
      const next = new URL(nextUrl, current.origin);
      current.pathname = next.pathname;
      current.search = next.search;
      current.hash = next.hash;
    }),
  };
  const localStorage = {
    getItem: vi.fn((key: string): StoredValue => storage.get(key) ?? null),
    setItem: vi.fn((key: string, value: string) => {
      storage.set(key, value);
    }),
    removeItem: vi.fn((key: string) => {
      storage.delete(key);
    }),
  };

  vi.stubGlobal("window", { history, localStorage, location });
  return storage;
}

describe("workspace routing", () => {
  beforeEach(() => {
    vi.unstubAllGlobals();
    installWindow();
  });

  it("adds the selected workspace to generated app paths", () => {
    setCurrentWorkspaceId("42");

    expect(workspaceAwarePath("/editor?itemImageId=9#page")).toBe(
      "/editor?itemImageId=9&workspace_id=42#page",
    );
  });

  it("replaces stale workspace ids in generated paths", () => {
    setCurrentWorkspaceId("42");

    expect(workspaceAwarePath("/editor?itemImageId=9&workspace_id=13#page")).toBe(
      "/editor?itemImageId=9&workspace_id=42#page",
    );
  });

  it("does not add a workspace query for invalid selections", () => {
    setCurrentWorkspaceId("not-a-workspace");

    expect(workspaceAwarePath("/editor?itemImageId=9#page")).toBe("/editor?itemImageId=9#page");
  });
});

describe("workspace headers", () => {
  beforeEach(() => {
    vi.unstubAllGlobals();
    installWindow();
  });

  it("preserves existing headers while adding the selected workspace id", () => {
    setCurrentWorkspaceId(42n);

    const headers = workspaceHeaders({ Accept: "application/json" });

    expect(headers.get("Accept")).toBe("application/json");
    expect(headers.get("X-Scribe-Workspace-ID")).toBe("42");
  });

  it("omits the workspace header for invalid explicit ids", () => {
    setCurrentWorkspaceId(42n);

    const headers = workspaceHeaders({ Accept: "application/json" }, "abc");

    expect(headers.get("Accept")).toBe("application/json");
    expect(headers.has("X-Scribe-Workspace-ID")).toBe(false);
  });
});

describe("workspace location updates", () => {
  beforeEach(() => {
    vi.unstubAllGlobals();
  });

  it("updates both local storage and the current URL", () => {
    const storage = installWindow("https://scribe.test/library?panel=settings#top");

    expect(applyWorkspaceToLocation("7")).toBe("7");
    expect(storage.get("scribe.selectedWorkspaceId")).toBe("7");
    expect(window.location.href).toBe("https://scribe.test/library?panel=settings&workspace_id=7#top");
  });

  it("loads workspace ids from the URL before falling back to storage", () => {
    const storage = installWindow("https://scribe.test/editor?itemImageId=9&workspace_id=12");
    storage.set("scribe.selectedWorkspaceId", "7");

    expect(syncWorkspaceSelectionFromLocation()).toBe("12");
    expect(getCurrentWorkspaceId()).toBe("12");
    expect(storage.get("scribe.selectedWorkspaceId")).toBe("12");
  });

  it("clears stored selection and removes the URL query when selection is empty", () => {
    const storage = installWindow("https://scribe.test/library?workspace_id=7&panel=settings#top");
    storage.set("scribe.selectedWorkspaceId", "7");

    expect(applyWorkspaceToLocation(null)).toBe("");
    expect(storage.has("scribe.selectedWorkspaceId")).toBe(false);
    expect(window.location.href).toBe("https://scribe.test/library?panel=settings#top");
  });
});
