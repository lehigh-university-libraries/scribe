import Mirador from "mirador";
import scribeMiradorPlugin, { annotationAdapters } from "../../vendor/mirador-scribe/dist/mirador-scribe.es.js";
import { getOCRRun } from "../api/processing";
import { uint64ToString } from "../lib/util";

export async function renderEditor(app: HTMLElement): Promise<void> {
  const params = new URLSearchParams(window.location.search);
  const itemImageID = params.get("itemImageId") ?? "";

  app.innerHTML = `
    <main class="h-screen w-screen overflow-hidden bg-slate-950">
      <header class="flex items-center justify-between border-b border-slate-800 px-4 py-2">
        <div>
          <h1 class="text-xl font-bold">Scribe Editor</h1>
          <p id="editor-meta" class="text-xs text-slate-300"></p>
        </div>
        <a href="/" class="rounded border border-slate-600 px-3 py-2 text-sm hover:bg-slate-800">Back</a>
      </header>
      <section class="h-[calc(100vh-56px)]">
        <div id="mirador-viewer" class="h-full w-full"></div>
      </section>
    </main>
  `;

  const meta = document.getElementById("editor-meta") as HTMLParagraphElement;
  const annotationBase = (import.meta as ImportMeta & { env?: Record<string, string | undefined> }).env?.VITE_ANNOTATION_API_BASE
    || window.location.origin;
  const ScribeAnnotationAdapter = annotationAdapters.ScribeAnnotationAdapter as new (
    endpointURL: string,
    iiifPresentationVersion: 3,
    canvasID: string,
    user: string
  ) => unknown;
  const osdConfig = {
    crossOriginPolicy: "Anonymous",
    ajaxWithCredentials: false,
  };

  // No itemImageId — open a bare Mirador workspace so the user can paste any
  // IIIF manifest URL. Annotations are auto-registered by the backend when the
  // annotation adapter first calls SearchAnnotations for an unknown canvas.
  if (itemImageID === "") {
    meta.textContent = "Open a IIIF manifest using the workspace panel (+ button)";
    Mirador.viewer({
      id: "mirador-viewer",
      osdConfig,
      annotation: {
        adapter: (canvasID: string) => new ScribeAnnotationAdapter(`${annotationBase}/v1`, 3, canvasID, "Scribe User"),
        readonly: false,
      },
      annotations: { htmlSanitizationRuleSet: "liberal" },
      windows: [],
      workspaceControlPanel: { enabled: true },
      thumbnailNavigation: { defaultPosition: "off", displaySettings: false },
      window: {
        sideBarPanel: "annotations",
        defaultSideBarPanel: "annotations",
        sideBarOpenByDefault: true,
        defaultSidebarPanelWidth: Math.round(window.innerWidth * 0.45),
        panels: {
          info: false,
          attribution: false,
          canvas: false,
          annotations: true,
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
      adapter: (canvasID: string) => new ScribeAnnotationAdapter(`${annotationBase}/v1`, 3, canvasID, "Scribe User"),
      readonly: false,
    },
    annotations: { htmlSanitizationRuleSet: "liberal" },
    windows: [{ manifestId: manifestURL }],
    workspaceControlPanel: { enabled: false },
    thumbnailNavigation: { defaultPosition: "off", displaySettings: false },
    window: {
      allowClose: false,
      allowFullscreen: false,
      allowMaximize: false,
      allowTopMenuButton: false,
      hideWindowTitle: true,
      sideBarPanel: "annotations",
      defaultSideBarPanel: "annotations",
      sideBarOpenByDefault: true,
      defaultSidebarPanelWidth: Math.round(window.innerWidth * 0.45),
      panels: {
        info: false,
        attribution: false,
        canvas: false,
        annotations: true,
        search: false,
        layers: false,
      },
    },
  }, [...scribeMiradorPlugin]);
}
