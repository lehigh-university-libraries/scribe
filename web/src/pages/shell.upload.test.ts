// @vitest-environment happy-dom

import { beforeEach, describe, expect, it, vi } from "vitest";

import { createAPIKey, deleteAPIKey, getAuthMe, listAPIKeys, listProviderSecrets, logout } from "../api/auth";
import { getContextMetrics, listContexts } from "../api/context";
import { subscribeToEvents } from "../api/events";
import { createItemFromManifest, listItems, listItemProviderCallAudits, uploadItemImages } from "../api/items";
import { processImageURL, processImageUpload } from "../api/processing";
import { listTranscriptionJobs } from "../api/transcription";
import { listWorkspaceMembers, listWorkspaces } from "../api/workspaces";
import { renderShell } from "./shell";

vi.mock("../api/auth", () => ({
  createAPIKey: vi.fn(),
  createProviderSecret: vi.fn(),
  deleteAPIKey: vi.fn(),
  deleteProviderSecret: vi.fn(),
  getAuthMe: vi.fn(),
  listAPIKeys: vi.fn(),
  listProviderSecrets: vi.fn(),
  logout: vi.fn(),
}));

vi.mock("../api/context", () => ({
  createContext: vi.fn(),
  getContextMetrics: vi.fn(),
  listContexts: vi.fn(),
}));

vi.mock("../api/events", () => ({
  subscribeToEvents: vi.fn(),
}));

vi.mock("../api/items", () => ({
  createItemFromManifest: vi.fn(),
  deleteItem: vi.fn(),
  listItemProviderCallAudits: vi.fn(),
  listItems: vi.fn(),
  uploadItemImages: vi.fn(),
}));

vi.mock("../api/processing", () => ({
  processImageUpload: vi.fn(),
  processImageURL: vi.fn(),
  reprocessItemImage: vi.fn(),
}));

vi.mock("../api/transcription", () => ({
  listTranscriptionJobs: vi.fn(),
}));

vi.mock("../api/workspaces", () => ({
  addWorkspaceMember: vi.fn(),
  createWorkspace: vi.fn(),
  deleteWorkspaceMember: vi.fn(),
  listWorkspaceMembers: vi.fn(),
  listWorkspaces: vi.fn(),
  updateWorkspace: vi.fn(),
  updateWorkspaceMember: vi.fn(),
}));

const authWorkspace = { id: 7n, name: "Manuscripts", role: "admin" };
const workspace = { id: 7n, name: "Manuscripts", role: "admin", isPersonal: false };

async function waitFor(assertion: () => void): Promise<void> {
  const deadline = Date.now() + 1000;
  let lastError: unknown;
  while (Date.now() < deadline) {
    try {
      assertion();
      return;
    } catch (error) {
      lastError = error;
      await new Promise((resolve) => window.setTimeout(resolve, 10));
    }
  }
  throw lastError;
}

async function setupShell(view: "library" | "settings" = "library", eventClose = vi.fn()): Promise<HTMLElement> {
  document.body.innerHTML = `<main id="app"></main>`;
  window.history.replaceState(null, "", `/${view}`);
  window.localStorage.clear();

  vi.mocked(getAuthMe).mockResolvedValue({
    authenticated: true,
    authType: "session",
    loginUrl: "/auth/login",
    logoutUrl: "/logout",
    user: { id: 12n, email: "user@example.test", name: "User", pictureUrl: "", isAdmin: false, defaultWorkspaceId: 7n },
    workspace: authWorkspace,
  } as never);
  vi.mocked(listWorkspaces).mockResolvedValue([{ workspace, role: "admin" }] as never);
  vi.mocked(listItems).mockResolvedValue([]);
  vi.mocked(listContexts).mockResolvedValue([]);
  vi.mocked(listWorkspaceMembers).mockResolvedValue({ workspace, members: [] } as never);
  vi.mocked(getContextMetrics).mockResolvedValue({
    context_id: 0,
    total_runs: 0,
    corrected_runs: 0,
    avg_levenshtein_distance: 0,
    avg_edit_count: 0,
    avg_box_change_score: 0,
  });
  vi.mocked(listProviderSecrets).mockResolvedValue([]);
  vi.mocked(listAPIKeys).mockResolvedValue([]);
  vi.mocked(listItemProviderCallAudits).mockResolvedValue([]);
  vi.mocked(subscribeToEvents).mockReturnValue({ close: eventClose });
  vi.mocked(logout).mockResolvedValue();
  vi.mocked(listTranscriptionJobs).mockResolvedValue([{ totalSegments: 1, completedSegments: 0 } as never]);

  const app = document.getElementById("app");
  if (!app) throw new Error("missing app root");
  await renderShell(app, view);
  return app;
}

describe("annotation upload actions", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("starts URL image processing from the library form and opens the editor", async () => {
    await setupShell();
    vi.mocked(processImageURL).mockResolvedValue({ itemImageId: 101n } as never);

    const input = document.getElementById("library-image-url") as HTMLInputElement | null;
    const form = document.getElementById("library-form-url") as HTMLFormElement | null;
    expect(input).toBeTruthy();
    expect(form).toBeTruthy();

    input!.value = "https://example.test/page.jpg";
    form!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));

    await waitFor(() => {
      expect(processImageURL).toHaveBeenCalledWith("https://example.test/page.jpg", 0n);
      expect(window.location.href).toContain("/editor?itemImageId=101");
    });
  });

  it("starts single image upload processing from the library form and opens the editor", async () => {
    await setupShell();
    const file = new File(["image-bytes"], "page-one.jpg", { type: "image/jpeg" });
    vi.mocked(processImageUpload).mockResolvedValue({ itemImageId: 202n } as never);

    document.querySelector<HTMLButtonElement>('[data-library-tab="single"]')?.click();
    const input = document.getElementById("library-single-file") as HTMLInputElement | null;
    const form = document.getElementById("library-form-single") as HTMLFormElement | null;
    expect(input).toBeTruthy();
    expect(form).toBeTruthy();

    Object.defineProperty(input, "files", { configurable: true, value: [file] });
    form!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));

    await waitFor(() => {
      expect(processImageUpload).toHaveBeenCalledWith(file, 0n);
      expect(window.location.href).toContain("/editor?itemImageId=202");
    });
  });

  it("uploads a multi-image batch from the library form and refreshes the annotation list", async () => {
    await setupShell();
    const first = new File(["first"], "page-one.jpg", { type: "image/jpeg" });
    const second = new File(["second"], "page-two.jpg", { type: "image/jpeg" });
    vi.mocked(uploadItemImages).mockResolvedValue({ id: "batch-1", name: "Batch item", sourceType: "upload", images: [] } as never);
    vi.mocked(listItems).mockResolvedValue([
      { id: "batch-1", name: "Batch item", sourceType: "upload", createdAt: "2026-05-13T00:00:00Z", images: [] } as never,
    ]);

    document.querySelector<HTMLButtonElement>('[data-library-tab="multi"]')?.click();
    const input = document.getElementById("library-multi-files") as HTMLInputElement | null;
    const form = document.getElementById("library-form-multi") as HTMLFormElement | null;
    expect(input).toBeTruthy();
    expect(form).toBeTruthy();

    Object.defineProperty(input, "files", { configurable: true, value: [first, second] });
    form!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));

    await waitFor(() => {
      expect(uploadItemImages).toHaveBeenCalledWith([first, second]);
      expect(document.body.textContent).toContain("Batch item");
    });
  });

  it("imports an IIIF manifest from the library form and opens the first image in the editor", async () => {
    await setupShell();
    vi.mocked(createItemFromManifest).mockResolvedValue({
      item: { id: "manifest-1", name: "Manifest item", sourceType: "manifest", images: [{ id: 303n }] },
      firstItemImageId: "303",
    } as never);

    document.querySelector<HTMLButtonElement>('[data-library-tab="manifest"]')?.click();
    const input = document.getElementById("library-manifest-url") as HTMLInputElement | null;
    const form = document.getElementById("library-form-manifest") as HTMLFormElement | null;
    expect(input).toBeTruthy();
    expect(form).toBeTruthy();

    input!.value = "https://iiif.example.test/manifest.json";
    form!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));

    await waitFor(() => {
      expect(createItemFromManifest).toHaveBeenCalledWith("https://iiif.example.test/manifest.json");
      expect(window.location.href).toContain("/editor?itemImageId=303&itemId=manifest-1");
    });
  });

  it("closes the workspace event stream when the page is hidden", async () => {
    const close = vi.fn();

    await setupShell("library", close);
    window.dispatchEvent(new Event("pagehide"));

    expect(close).toHaveBeenCalledTimes(1);
  });

  it("creates and deletes API keys from settings", async () => {
    vi.mocked(createAPIKey).mockResolvedValue({
      key: "scribe_secret_once",
      apiKey: { id: 56n, name: "New key", role: "write", scopes: [], keyPrefix: "sk_new", createdAt: "2026-06-01T00:00:00Z" },
    } as never);
    vi.mocked(deleteAPIKey).mockResolvedValue({} as never);
    Object.defineProperty(window, "alert", { configurable: true, value: vi.fn() });
    Object.defineProperty(window, "confirm", { configurable: true, value: vi.fn(() => true) });

    await setupShell("settings");
    vi.mocked(listAPIKeys).mockResolvedValue([
      { id: 55n, name: "Existing key", role: "read", scopes: [], keyPrefix: "sk_live", createdAt: "2026-06-01T00:00:00Z" },
    ] as never);

    const name = document.getElementById("settings-api-key-name") as HTMLInputElement | null;
    const role = document.getElementById("settings-api-key-role") as HTMLSelectElement | null;
    const form = document.getElementById("settings-api-key-form") as HTMLFormElement | null;
    expect(name).toBeTruthy();
    expect(role).toBeTruthy();
    expect(form).toBeTruthy();

    name!.value = "New key";
    role!.value = "write";
    form!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));

    await waitFor(() => {
      expect(createAPIKey).toHaveBeenCalledWith({ name: "New key", role: "write" });
      expect(window.alert).toHaveBeenCalledWith(expect.stringContaining("scribe_secret_once"));
    });

    let deleteButton: HTMLButtonElement | null = null;
    await waitFor(() => {
      deleteButton = document.querySelector<HTMLButtonElement>("[data-api-key-delete=\"55\"]");
      expect(deleteButton).toBeTruthy();
    });
    deleteButton!.click();

    await waitFor(() => {
      expect(deleteAPIKey).toHaveBeenCalledWith("55");
    });
  });
});
