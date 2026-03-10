import Mirador from "mirador";
import scribeMiradorPlugin, { annotationAdapters } from "../../vendor/mirador-scribe/dist/mirador-scribe.es.js";
import { annotationClient } from "../api/annotations";
import { getOCRRun } from "../api/processing";
import { uint64ToString } from "../lib/util";

export async function renderEditor(app: HTMLElement): Promise<void> {
  const params = new URLSearchParams(window.location.search);
  const itemImageID = params.get("itemImageId") ?? "";
  let hasUnsavedChanges = false;
  let saveSequence = 0;

  function requestSave(): Promise<boolean> {
    return new Promise((resolve) => {
      saveSequence += 1;
      const requestId = `save-${saveSequence}`;

      const handleResult = (event: Event) => {
        const detail = (event as CustomEvent<{ ok: boolean; requestId: string }>).detail;
        if (!detail || detail.requestId !== requestId) return;
        document.removeEventListener("scribe:save-result", handleResult);
        resolve(Boolean(detail.ok));
      };

      document.addEventListener("scribe:save-result", handleResult);
      document.dispatchEvent(new CustomEvent("scribe:request-save", {
        detail: {
          requestId,
          windowId: undefined,
        },
      }));
    });
  }

  function navigateHome() {
    window.location.href = "/";
  }

  app.innerHTML = `
    <main class="h-screen w-screen overflow-hidden bg-slate-950">
      <header class="flex items-center justify-between border-b border-slate-800 bg-slate-950/95 px-4 py-2">
        <div class="flex items-center gap-4">
          <a href="/" class="text-lg font-bold tracking-tight">Scribe</a>
          <nav class="flex items-center gap-2 text-sm text-slate-300">
            <button id="home-nav" class="rounded border border-slate-700 px-3 py-2 hover:bg-slate-800">Home</button>
          </nav>
        </div>
        <div class="text-right">
          <h1 class="text-xl font-bold">Editor</h1>
          <p id="editor-meta" class="text-xs text-slate-300"></p>
        </div>
      </header>
      <section class="h-[calc(100vh-56px)]">
        <div id="mirador-viewer" class="h-full w-full"></div>
      </section>
      <div id="leave-dialog" class="hidden fixed inset-0 z-50 items-center justify-center bg-slate-950/70">
        <div class="w-full max-w-md rounded-2xl border border-slate-700 bg-slate-900 p-6 shadow-2xl">
          <h2 class="text-lg font-semibold">Leave editor?</h2>
          <p class="mt-2 text-sm text-slate-300">You have unsaved changes. Save before returning home?</p>
          <div class="mt-5 flex justify-end gap-2">
            <button id="leave-cancel" class="rounded border border-slate-700 px-3 py-2 text-sm hover:bg-slate-800">Cancel</button>
            <button id="leave-discard" class="rounded border border-amber-800 px-3 py-2 text-sm text-amber-300 hover:bg-amber-950/40">Discard</button>
            <button id="leave-save" class="rounded bg-brand-500 px-3 py-2 text-sm font-medium hover:bg-brand-600">Save</button>
          </div>
        </div>
      </div>
    </main>
  `;

  const meta = document.getElementById("editor-meta") as HTMLParagraphElement;
  const homeNav = document.getElementById("home-nav") as HTMLButtonElement;
  const leaveDialog = document.getElementById("leave-dialog") as HTMLDivElement;
  const leaveCancel = document.getElementById("leave-cancel") as HTMLButtonElement;
  const leaveDiscard = document.getElementById("leave-discard") as HTMLButtonElement;
  const leaveSave = document.getElementById("leave-save") as HTMLButtonElement;
  const annotationBase = (import.meta as ImportMeta & { env?: Record<string, string | undefined> }).env?.VITE_ANNOTATION_API_BASE
    || window.location.origin;
  const ScribeAnnotationAdapter = annotationAdapters.ScribeAnnotationAdapter as new (
    endpointURL: string,
    iiifPresentationVersion: 3,
    canvasID: string,
    user: string,
    client: typeof annotationClient,
  ) => unknown;
  const osdConfig = {
    crossOriginPolicy: "Anonymous",
    ajaxWithCredentials: false,
  };

  function openLeaveDialog() {
    leaveDialog.classList.remove("hidden");
    leaveDialog.classList.add("flex");
  }

  function closeLeaveDialog() {
    leaveDialog.classList.add("hidden");
    leaveDialog.classList.remove("flex");
  }

  async function handleHomeNavigation() {
    if (!hasUnsavedChanges) {
      navigateHome();
      return;
    }
    openLeaveDialog();
  }

  homeNav.addEventListener("click", () => { void handleHomeNavigation(); });
  leaveCancel.addEventListener("click", closeLeaveDialog);
  leaveDiscard.addEventListener("click", navigateHome);
  leaveSave.addEventListener("click", async () => {
    leaveSave.disabled = true;
    const ok = await requestSave();
    leaveSave.disabled = false;
    if (ok) navigateHome();
  });

  const handleDirtyState = (event: Event) => {
    const detail = (event as CustomEvent<{ dirty: boolean }>).detail;
    hasUnsavedChanges = Boolean(detail?.dirty);
  };
  document.addEventListener("scribe:dirty-state", handleDirtyState);
  window.addEventListener("beforeunload", (event) => {
    if (!hasUnsavedChanges) return;
    event.preventDefault();
    event.returnValue = "";
  });

  // No itemImageId — open a bare Mirador workspace so the user can paste any
  // IIIF manifest URL. Annotations are auto-registered by the backend when the
  // annotation adapter first calls SearchAnnotations for an unknown canvas.
  if (itemImageID === "") {
    meta.textContent = "Open a IIIF manifest using the workspace panel (+ button)";
    Mirador.viewer({
      id: "mirador-viewer",
      osdConfig,
      annotation: {
        adapter: (canvasID: string) => new ScribeAnnotationAdapter(annotationBase, 3, canvasID, "Scribe User", annotationClient),
        readonly: false,
      },
      annotations: { htmlSanitizationRuleSet: "liberal" },
      windows: [],
      workspaceControlPanel: { enabled: true },
      thumbnailNavigation: { defaultPosition: "off", displaySettings: false },
      window: {
        forceDrawAnnotations: true,
        panels: {
          info: false,
          attribution: false,
          canvas: false,
          annotations: false,
          search: false,
          layers: false,
        },
      },
    }, [...scribeMiradorPlugin]);
    return;
  }

  let runResp: Awaited<ReturnType<typeof getOCRRun>> | null = null;
  try {
    runResp = await getOCRRun(itemImageID);
  } catch {
    runResp = null;
  }
  if (runResp == null) {
    meta.textContent = "Failed to load OCR run";
    return;
  }

  const runItemImageID = uint64ToString(runResp.itemImageId);
  meta.textContent = `item image ${runItemImageID || "unknown"} | model ${runResp.model}`;

  if (!runResp.imageUrl || runResp.imageUrl.trim() === "") {
    const viewer = document.getElementById("mirador-viewer");
    if (viewer) {
      viewer.innerHTML = `<div class="flex h-full items-center justify-center text-sm text-slate-400">No image is available for this OCR run.</div>`;
    }
    return;
  }

  if (!runItemImageID) {
    meta.textContent = "Missing item image reference required for IIIF manifest route";
    return;
  }

  const manifestURL = `${window.location.origin}/v1/item-images/${encodeURIComponent(runItemImageID)}/manifest`;

  Mirador.viewer({
    id: "mirador-viewer",
    theme: { direction: "rtl" },
    osdConfig,
    annotation: {
      adapter: (canvasID: string) => new ScribeAnnotationAdapter(annotationBase, 3, canvasID, "Scribe User", annotationClient),
      readonly: false,
    },
    annotations: { htmlSanitizationRuleSet: "liberal" },
    windows: [{ manifestId: manifestURL }],
    workspaceControlPanel: { enabled: false },
    thumbnailNavigation: { defaultPosition: "off", displaySettings: false },
    window: {
      forceDrawAnnotations: true,
      allowClose: false,
      allowFullscreen: false,
      allowMaximize: false,
      allowTopMenuButton: false,
      hideWindowTitle: true,
      panels: {
        info: false,
        attribution: false,
        canvas: false,
        annotations: false,
        search: false,
        layers: false,
      },
    },
  }, [...scribeMiradorPlugin]);
}
