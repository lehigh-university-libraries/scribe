import "./styles.css";
import Panzoom from "@panzoom/panzoom";
import { createPromiseClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import { ImageProcessingService } from "./proto/hocredit/v1/process_connect";
import {
  GetOCRRunRequest,
  OutputFormat,
  ProcessHOCRRequest,
  ProcessImageUploadRequest,
  ProcessImageURLRequest,
  SaveOCREditsRequest
} from "./proto/hocredit/v1/process_pb";

type OCRRun = {
  session_id: string;
  image_url: string;
  provider?: string;
  model: string;
  original_hocr: string;
  original_text: string;
  corrected_hocr?: string;
  corrected_text?: string;
  edit_count: number;
  levenshtein_distance: number;
};

type LLMProviderOption = {
  id: string;
  name: string;
  enabled: boolean;
  default_model: string;
  models: string[];
};

type ProgressState = {
  id: string;
  status: string;
  message: string;
  done: boolean;
  error?: string;
  started_at: string;
  updated_at: string;
};

type BBox = { x1: number; y1: number; x2: number; y2: number };

type ParsedWord = {
  id: string;
  text: string;
  bbox: BBox;
};

type ParsedLine = {
  id: string;
  text: string;
  originalText: string;
  bbox: BBox;
  originalBBox: BBox | null;
  words: ParsedWord[];
  exploded: boolean;
};

const app = document.getElementById("app");
if (!app) {
  throw new Error("missing #app element");
}

const connectTransport = createConnectTransport({
  baseUrl: window.location.origin
});
const imageClient = createPromiseClient(ImageProcessingService, connectTransport);

if (window.location.pathname.startsWith("/editor")) {
  renderEditor();
} else {
  renderHome();
}

function renderHome(): void {
  app!.innerHTML = `
    <main class="mx-auto max-w-5xl p-8">
      <header class="mb-6">
        <h1 class="text-4xl font-bold">hOCRedit</h1>
        <p class="mt-2 text-slate-300">Upload an image or provide an image URL for OCR processing.</p>
      </header>

      <section class="grid gap-4 rounded-xl border border-slate-700 bg-slate-900/60 p-6 md:grid-cols-2">
        <div class="space-y-3">
          <h2 class="text-xl font-semibold">Process from URL</h2>
          <form id="url-form" class="space-y-3">
            <input id="image-url" type="url" required class="w-full rounded border border-slate-600 bg-slate-950 px-3 py-2" placeholder="https://example.org/image.jpg" />
            <button class="rounded bg-brand-500 px-4 py-2 font-medium hover:bg-brand-600" type="submit">Process URL</button>
          </form>
        </div>

        <div class="space-y-3">
          <h2 class="text-xl font-semibold">Process upload</h2>
          <form id="upload-form" class="space-y-3">
            <input id="image-file" type="file" required accept=".jpg,.jpeg,.png,.gif,.webp,.jp2,.jpx,.j2k,.tif,.tiff" class="w-full rounded border border-slate-600 bg-slate-950 px-3 py-2" />
            <button class="rounded bg-brand-500 px-4 py-2 font-medium hover:bg-brand-600" type="submit">Upload and process</button>
          </form>
        </div>
      </section>

      <section class="mt-4 rounded-xl border border-slate-700 bg-slate-900/60 p-6">
        <h2 class="text-xl font-semibold">Start from existing hOCR</h2>
        <form id="hocr-form" class="mt-3 space-y-3">
          <textarea id="hocr-input" rows="8" required class="w-full rounded border border-slate-600 bg-slate-950 px-3 py-2 font-mono text-xs" placeholder="<html ...>hOCR XML</html>"></textarea>
          <input id="hocr-image-file" type="file" accept=".jpg,.jpeg,.png,.gif,.webp,.jp2,.jpx,.j2k,.tif,.tiff" class="w-full rounded border border-slate-600 bg-slate-950 px-3 py-2" />
          <input id="hocr-image-url" type="url" class="w-full rounded border border-slate-600 bg-slate-950 px-3 py-2" placeholder="Optional image URL for editor overlay" />
          <button class="rounded bg-brand-500 px-4 py-2 font-medium hover:bg-brand-600" type="submit">Use this hOCR</button>
        </form>
      </section>

      <section class="mt-4 grid gap-3 rounded-xl border border-slate-700 bg-slate-900/60 p-6 md:grid-cols-2">
        <label class="flex flex-col gap-2 text-sm text-slate-300">
          Provider
          <select id="provider-select" class="rounded border border-slate-600 bg-slate-950 px-3 py-2"></select>
        </label>
        <label class="flex flex-col gap-2 text-sm text-slate-300">
          Model
          <select id="model-select" class="rounded border border-slate-600 bg-slate-950 px-3 py-2"></select>
        </label>
        <label class="flex flex-col gap-2 text-sm text-slate-300 md:col-span-2">
          Output format
          <select id="output-format" class="rounded border border-slate-600 bg-slate-950 px-3 py-2 max-w-xs">
            <option value="hocr">hOCR</option>
            <option value="text">Plain text</option>
          </select>
        </label>
      </section>

      <section class="mt-4 rounded-xl border border-slate-700 bg-slate-900/60 p-6">
        <div class="mb-2 flex items-center justify-between">
          <h2 class="text-xl font-semibold">Result</h2>
          <span id="session-meta" class="text-xs text-slate-400"></span>
        </div>
        <a id="open-editor" class="mb-3 hidden inline-block rounded bg-emerald-600 px-3 py-2 text-sm font-medium hover:bg-emerald-700" href="#">Open Editor</a>
        <img id="result-image" class="mb-3 hidden max-h-96 rounded border border-slate-700" alt="Processed source" />
        <pre id="result-output" class="max-h-[28rem] overflow-auto whitespace-pre-wrap rounded border border-slate-700 bg-slate-950 p-4 text-sm text-slate-200"></pre>
      </section>
    </main>
    <div id="progress-overlay" class="hidden fixed inset-0 z-50 bg-slate-950/95 backdrop-blur-sm">
      <div class="flex h-full w-full items-center justify-center p-6">
        <div class="w-full max-w-xl rounded-2xl border border-slate-700 bg-slate-900 p-8 text-center shadow-2xl">
          <p class="mb-4 text-sm uppercase tracking-[0.25em] text-slate-400">Processing</p>
          <p id="progress-overlay-status" class="text-2xl font-semibold text-slate-100">Starting...</p>
          <div class="mt-6 h-2 w-full overflow-hidden rounded bg-slate-800">
            <div class="h-full w-full animate-pulse bg-brand-500"></div>
          </div>
        </div>
      </div>
    </div>
  `;

  const urlForm = document.getElementById("url-form") as HTMLFormElement;
  const uploadForm = document.getElementById("upload-form") as HTMLFormElement;
  const hocrForm = document.getElementById("hocr-form") as HTMLFormElement;
  const imageURLInput = document.getElementById("image-url") as HTMLInputElement;
  const fileInput = document.getElementById("image-file") as HTMLInputElement;
  const hocrInput = document.getElementById("hocr-input") as HTMLTextAreaElement;
  const hocrImageFileInput = document.getElementById("hocr-image-file") as HTMLInputElement;
  const hocrImageURLInput = document.getElementById("hocr-image-url") as HTMLInputElement;
  const providerSelect = document.getElementById("provider-select") as HTMLSelectElement;
  const modelSelect = document.getElementById("model-select") as HTMLSelectElement;
  const outputFormatInput = document.getElementById("output-format") as HTMLSelectElement;
  const resultOutput = document.getElementById("result-output") as HTMLPreElement;
  const sessionMeta = document.getElementById("session-meta") as HTMLSpanElement;
  const resultImage = document.getElementById("result-image") as HTMLImageElement;
  const openEditor = document.getElementById("open-editor") as HTMLAnchorElement;
  const progressOverlay = document.getElementById("progress-overlay") as HTMLDivElement;
  const progressOverlayStatus = document.getElementById("progress-overlay-status") as HTMLParagraphElement;
  let progressTimer: number | null = null;

  function toOutputFormat(format: string): OutputFormat {
    return format === "text" ? OutputFormat.TEXT : OutputFormat.HOCR;
  }

  async function loadLLMOptions(): Promise<void> {
    let providers: LLMProviderOption[] = [
      { id: "ollama", name: "Ollama", enabled: true, default_model: "mistral-small3.2:24b", models: ["mistral-small3.2:24b"] }
    ];
    let defaultProvider = "ollama";

    try {
      const response = await fetch("/v1/llm/options");
      if (response.ok) {
        const payload = await response.json() as { default_provider: string; providers: LLMProviderOption[] };
        providers = payload.providers;
        defaultProvider = payload.default_provider || defaultProvider;
      }
    } catch {
      // Keep fallback options
    }

    const enabled = providers.filter((p) => p.enabled);
    providerSelect.innerHTML = "";
    for (const provider of enabled) {
      const opt = document.createElement("option");
      opt.value = provider.id;
      opt.textContent = provider.name;
      providerSelect.appendChild(opt);
    }

    if (enabled.some((p) => p.id === defaultProvider)) {
      providerSelect.value = defaultProvider;
    } else if (enabled.length > 0) {
      providerSelect.value = enabled[0].id;
    }

    function renderModels(): void {
      const provider = enabled.find((p) => p.id === providerSelect.value);
      modelSelect.innerHTML = "";
      if (!provider) return;
      const models = provider.models.length > 0 ? provider.models : [provider.default_model];
      for (const model of models) {
        const opt = document.createElement("option");
        opt.value = model;
        opt.textContent = model;
        modelSelect.appendChild(opt);
      }
      if (models.includes(provider.default_model)) {
        modelSelect.value = provider.default_model;
      }
    }

    providerSelect.addEventListener("change", renderModels);
    renderModels();
  }

  function showProcessResult(result: { sessionId: string; imageUrl: string; hocr: string; plainText: string }, format: string): string {
    const body = format === "text" ? result.plainText : result.hocr;
    const sessionID = result.sessionId ?? "";
    const imageURL = result.imageUrl ?? "";

    if (sessionID) {
      sessionMeta.textContent = `session ${sessionID}`;
      openEditor.href = `/editor?session=${encodeURIComponent(sessionID)}`;
      openEditor.classList.remove("hidden");
    } else {
      sessionMeta.textContent = "";
      openEditor.classList.add("hidden");
    }

    if (imageURL !== "") {
      resultImage.src = imageURL;
      resultImage.classList.remove("hidden");
    } else {
      resultImage.classList.add("hidden");
    }

    resultOutput.textContent = body;
    return sessionID;
  }

  async function readFileBytes(file: File): Promise<Uint8Array> {
    const buffer = await file.arrayBuffer();
    return new Uint8Array(buffer);
  }

  function newProgressID(): string {
    return `p_${Date.now()}_${Math.random().toString(36).slice(2, 10)}`;
  }

  function stopProgressPolling(): void {
    if (progressTimer !== null) {
      window.clearInterval(progressTimer);
      progressTimer = null;
    }
    progressOverlay.classList.add("hidden");
  }

  function startProgressPolling(progressID: string): void {
    stopProgressPolling();
    progressOverlay.classList.remove("hidden");
    sessionMeta.textContent = "processing...";
    progressOverlayStatus.textContent = "Processing...";
    const poll = async () => {
      try {
        const resp = await fetch(`/v1/progress/${encodeURIComponent(progressID)}`);
        if (!resp.ok) return;
        const state = await resp.json() as ProgressState;
        const status = (state.status || "processing").trim();
        const message = (state.message || "").trim();
        const suffix = message ? `: ${message}` : "";
        sessionMeta.textContent = `${status}${suffix}`;
        progressOverlayStatus.textContent = `${status}${suffix}`;
        if (state.done) {
          stopProgressPolling();
        }
      } catch {
        // Keep polling; this is best-effort UI feedback.
      }
    };
    void poll();
    progressTimer = window.setInterval(() => {
      void poll();
    }, 900);
  }

  urlForm.addEventListener("submit", async (event) => {
    event.preventDefault();
    const imageURL = imageURLInput.value.trim();
    if (!imageURL) return;

    const format = outputFormatInput.value;
    const provider = providerSelect.value;
    const model = modelSelect.value;
    const progressID = newProgressID();
    startProgressPolling(progressID);
    try {
      const result = await imageClient.processImageURL(new ProcessImageURLRequest({
        imageUrl: imageURL,
        model,
        outputFormat: toOutputFormat(format)
      }), {
        headers: {
          "X-Progress-ID": progressID,
          "X-Provider": provider
        }
      });
      showProcessResult(result, format);
    } finally {
      stopProgressPolling();
    }
  });

  let uploadInFlight = false;

  uploadForm.addEventListener("submit", async (event) => {
    event.preventDefault();
    if (uploadInFlight) return;
    const file = fileInput.files?.[0];
    if (!file) return;
    uploadInFlight = true;

    const format = outputFormatInput.value;
    const provider = providerSelect.value;
    const model = modelSelect.value;
    const progressID = newProgressID();
    startProgressPolling(progressID);

    try {
      const imageData = await readFileBytes(file);
      const result = await imageClient.processImageUpload(new ProcessImageUploadRequest({
        imageData,
        filename: file.name,
        model,
        outputFormat: toOutputFormat(format)
      }), {
        headers: {
          "X-Progress-ID": progressID,
          "X-Provider": provider
        }
      });
      const sessionID = showProcessResult(result, format);
      if (sessionID) {
        window.location.href = `/editor?session=${encodeURIComponent(sessionID)}`;
      }
    } finally {
      stopProgressPolling();
      uploadInFlight = false;
    }
  });

  fileInput.addEventListener("change", () => {
    if (!fileInput.files?.[0]) return;
    uploadForm.requestSubmit();
  });

  hocrForm.addEventListener("submit", async (event) => {
    event.preventDefault();
    const hocr = hocrInput.value.trim();
    if (!hocr) return;

    const format = outputFormatInput.value;
    const provider = providerSelect.value;
    const model = modelSelect.value;
    const progressID = newProgressID();
    startProgressPolling(progressID);
    const imageURL = hocrImageURLInput.value.trim();
    const imageFile = hocrImageFileInput.files?.[0];

    try {
      const payload = new ProcessHOCRRequest({
        hocr,
        model,
        imageUrl: imageURL,
        outputFormat: toOutputFormat(format)
      });
      if (imageFile) {
        payload.imageData = await readFileBytes(imageFile);
        payload.filename = imageFile.name;
      }
      const result = await imageClient.processHOCR(payload, {
        headers: {
          "X-Progress-ID": progressID,
          "X-Provider": provider
        }
      });
      showProcessResult(result, format);
    } finally {
      stopProgressPolling();
    }
  });

  void loadLLMOptions();
}

async function renderEditor(): Promise<void> {
  const params = new URLSearchParams(window.location.search);
  const sessionID = params.get("session") ?? "";

  app!.innerHTML = `
    <main class="mx-auto max-w-[92rem] p-4">
      <header class="mb-3 flex items-center justify-between">
        <div>
          <h1 class="text-2xl font-bold">hOCR Box Editor</h1>
          <p id="editor-meta" class="text-xs text-slate-300"></p>
        </div>
        <a href="/" class="rounded border border-slate-600 px-3 py-2 text-sm hover:bg-slate-800">Back</a>
      </header>

      <div class="grid gap-3 md:grid-cols-2">
        <section class="relative rounded-xl border border-slate-700 bg-slate-900/60 p-2">
          <div class="mb-2 flex items-center gap-2 text-xs">
            <button id="zoom-out" class="rounded border border-slate-600 px-2 py-1 hover:bg-slate-800">-</button>
            <input id="zoom-slider" type="range" min="100" max="800" step="10" value="100" class="w-44" />
            <button id="zoom-in" class="rounded border border-slate-600 px-2 py-1 hover:bg-slate-800">+</button>
            <button id="zoom-reset" class="rounded border border-slate-600 px-2 py-1 hover:bg-slate-800">Reset</button>
            <span id="zoom-label" class="text-slate-300">100%</span>
          </div>
          <div id="image-wrap" class="relative h-[78vh] w-full overflow-hidden rounded bg-slate-950/50">
            <div id="image-stage" class="relative inline-block origin-top-left will-change-transform">
              <img id="editor-image" class="max-h-[78vh] rounded select-none" alt="source" />
              <div id="line-overlay" class="absolute inset-0"></div>
            </div>
          </div>
        </section>
        <section class="relative rounded-xl border border-slate-700 bg-slate-900/60 p-2">
          <div id="line-list" class="relative min-h-[18rem]"></div>
          <div id="line-info" class="absolute bottom-2 right-2 rounded border border-slate-700 bg-slate-950/90 px-2 py-1 text-[11px] text-slate-300"></div>
          <p id="save-status" class="absolute bottom-2 left-2 text-[11px] text-slate-400"></p>
        </section>
      </div>

      <div class="mt-2 grid gap-3 md:grid-cols-2">
        <section class="rounded-xl border border-slate-700 bg-slate-900/60 p-2">
          <div class="rounded border border-slate-700 bg-slate-900/40 px-2 py-1 text-[11px] text-slate-300">
            <strong>Keyboard Shortcuts:</strong><br>
            • <kbd>Tab</kbd> - Next line/word<br>
            • <kbd>Shift+Tab</kbd> - Previous line/word<br>
            • <kbd>Enter</kbd> - Apply changes<br>
            • <kbd>Delete</kbd> - Delete selected line<br>
            • <kbd>D</kbd> - Toggle drawing mode<br>
            • <kbd>S</kbd> - Split line<br>
            • <kbd>E</kbd> - Explode words<br>
            • <kbd>M</kbd> - Toggle merge mode<br>
            • <kbd>Ctrl/Cmd+Z</kbd> - Undo<br>
            • <kbd>Ctrl/Cmd+Y</kbd> - Redo<br>
            • <kbd>Esc</kbd> - Clear selection/Exit drawing
          </div>
        </section>
        <section class="rounded-xl border border-slate-700 bg-slate-900/60 p-2">
          <div class="mb-2 flex flex-wrap gap-2">
            <button id="add-box" class="rounded border border-slate-600 px-2 py-1 text-xs hover:bg-slate-800">Add</button>
            <button id="split-line" class="rounded border border-slate-600 px-2 py-1 text-xs hover:bg-slate-800">Split Line</button>
            <button id="explode-line" class="rounded border border-slate-600 px-2 py-1 text-xs hover:bg-slate-800">Explode Words</button>
            <button id="transcribe-box" class="rounded border border-sky-600 px-2 py-1 text-xs text-sky-300 hover:bg-sky-950/30">Transcribe Box</button>
            <button id="merge-mode" class="rounded border border-slate-600 px-2 py-1 text-xs hover:bg-slate-800">Merge Mode</button>
            <button id="merge-selected" class="hidden rounded border border-emerald-500 px-2 py-1 text-xs text-emerald-300 hover:bg-emerald-900/20">Merge Selected</button>
            <button id="reset-edits" class="rounded border border-amber-600 px-2 py-1 text-xs text-amber-300 hover:bg-amber-950/30">Reset</button>
            <button id="undo-edit" class="rounded border border-slate-600 px-2 py-1 text-xs hover:bg-slate-800">Undo</button>
            <button id="redo-edit" class="rounded border border-slate-600 px-2 py-1 text-xs hover:bg-slate-800">Redo</button>
            <button id="delete-box" class="rounded border border-rose-600 px-2 py-1 text-xs text-rose-300 hover:bg-rose-950">Delete</button>
            <button id="save-edits" class="rounded bg-brand-500 px-3 py-1 text-xs font-medium hover:bg-brand-600">Save</button>
          </div>
        </section>
      </div>
    </main>
  `;

  if (!sessionID) {
    (document.getElementById("editor-meta") as HTMLParagraphElement).textContent = "Missing session query parameter";
    return;
  }

  const meta = document.getElementById("editor-meta") as HTMLParagraphElement;
  const image = document.getElementById("editor-image") as HTMLImageElement;
  const imageWrap = document.getElementById("image-wrap") as HTMLDivElement;
  const imageStage = document.getElementById("image-stage") as HTMLDivElement;
  const lineOverlay = document.getElementById("line-overlay") as HTMLDivElement;
  const zoomOutBtn = document.getElementById("zoom-out") as HTMLButtonElement;
  const zoomInBtn = document.getElementById("zoom-in") as HTMLButtonElement;
  const zoomResetBtn = document.getElementById("zoom-reset") as HTMLButtonElement;
  const zoomSlider = document.getElementById("zoom-slider") as HTMLInputElement;
  const zoomLabel = document.getElementById("zoom-label") as HTMLSpanElement;
  const lineList = document.getElementById("line-list") as HTMLDivElement;
  const lineInfo = document.getElementById("line-info") as HTMLDivElement;
  const saveStatus = document.getElementById("save-status") as HTMLParagraphElement;
  const saveBtn = document.getElementById("save-edits") as HTMLButtonElement;
  const addBoxBtn = document.getElementById("add-box") as HTMLButtonElement;
  const splitLineBtn = document.getElementById("split-line") as HTMLButtonElement;
  const explodeLineBtn = document.getElementById("explode-line") as HTMLButtonElement;
  const transcribeBoxBtn = document.getElementById("transcribe-box") as HTMLButtonElement;
  const mergeModeBtn = document.getElementById("merge-mode") as HTMLButtonElement;
  const mergeSelectedBtn = document.getElementById("merge-selected") as HTMLButtonElement;
  const resetBtn = document.getElementById("reset-edits") as HTMLButtonElement;
  const undoBtn = document.getElementById("undo-edit") as HTMLButtonElement;
  const redoBtn = document.getElementById("redo-edit") as HTMLButtonElement;
  const deleteBoxBtn = document.getElementById("delete-box") as HTMLButtonElement;

  let runResp;
  try {
    runResp = await imageClient.getOCRRun(new GetOCRRunRequest({ sessionId: sessionID }));
  } catch {
    meta.textContent = `Failed to load session ${sessionID}`;
    return;
  }
  const runProto = runResp;
  const run: OCRRun = {
    session_id: runProto.sessionId,
    image_url: runProto.imageUrl,
    model: runProto.model,
    original_hocr: runProto.originalHocr,
    original_text: runProto.originalText,
    corrected_hocr: runProto.correctedHocr,
    corrected_text: runProto.correctedText,
    edit_count: runProto.editCount,
    levenshtein_distance: runProto.levenshteinDistance
  };
  const workingHOCR = run.corrected_hocr && run.corrected_hocr.trim() !== "" ? run.corrected_hocr : run.original_hocr;
  const providerLabel = run.provider && run.provider.trim() !== "" ? run.provider : "unknown";
  meta.textContent = `session ${run.session_id} | provider ${providerLabel} | model ${run.model} | edits ${run.edit_count}`;

  image.src = run.image_url;
  if (!run.image_url || run.image_url.trim() === "") image.classList.add("hidden");

  const parsed = parseHOCR(workingHOCR);
  const lines = parsed.lines;
  let activeLineID = lines.length > 0 ? lines[0].id : "";
  let activeWordID = "";
  const changedLineIDs = new Set<string>();
  const changedBoxIDs = new Set<string>();
  let nextLineCounter = lines.length + 1;
  let isAddBoxMode = false;
  let isMergeMode = false;
  const selectedLineIDs = new Set<string>();
  const selectedWordKeys = new Set<string>();
  type EditorSnapshot = {
    lines: ParsedLine[];
    activeLineID: string;
    activeWordID: string;
  };
  const undoStack: EditorSnapshot[] = [];
  const redoStack: EditorSnapshot[] = [];
  let autosaveTimer: number | null = null;
  let autosaveInFlight = false;
  let autosaveQueued = false;
  let dragDirty = false;
  let restoringInputFocus = false;
  let autoTranscribeStarted = false;
  let didAutoZoom = false;
  let panzoomInstance: ReturnType<typeof Panzoom> | null = null;

  const pageWidth = parsed.pageWidth || 1;
  const pageHeight = parsed.pageHeight || 1;

  let interaction: null | {
    lineID: string;
    mode: "move" | "resize" | "draw";
    handle: string;
    startDocX: number;
    startDocY: number;
    startBox: { x1: number; y1: number; x2: number; y2: number };
  } = null;
  let wordInteraction: null | {
    lineID: string;
    wordID: string;
    mode: "move" | "resize";
    handle: "w" | "e";
    startDocX: number;
    startBox: BBox;
  } = null;

  function syncEditorHeights(): void {
    const minHeight = 280;
    const imageHeight = Math.round(imageWrap.clientHeight || image.clientHeight || 0);
    const target = Math.max(minHeight, imageHeight);
    lineList.style.height = `${target}px`;
  }

  function getLineByID(id: string): ParsedLine | undefined {
    return lines.find((line) => line.id === id);
  }

  function getWordByID(line: ParsedLine, wordID: string): ParsedWord | undefined {
    return line.words.find((word) => word.id === wordID);
  }

  function currentScale(): number {
    return panzoomInstance ? panzoomInstance.getScale() : 1;
  }

  function currentPan(): { x: number; y: number } {
    return panzoomInstance ? panzoomInstance.getPan() : { x: 0, y: 0 };
  }

  function updateZoomUI(): void {
    const scale = currentScale();
    const pct = Math.round(scale * 100);
    zoomLabel.textContent = `${pct}%`;
    zoomSlider.value = String(Math.max(100, Math.min(800, pct)));
  }

  function viewportDocBounds(): BBox | null {
    const stageW = image.clientWidth;
    const stageH = image.clientHeight;
    if (stageW <= 0 || stageH <= 0) return null;
    const wrapW = imageWrap.clientWidth;
    const wrapH = imageWrap.clientHeight;
    if (wrapW <= 0 || wrapH <= 0) return null;
    const scale = currentScale();
    const pan = currentPan();
    const vx1 = Math.max(0, (-pan.x) / scale);
    const vy1 = Math.max(0, (-pan.y) / scale);
    const vx2 = Math.min(stageW, (wrapW - pan.x) / scale);
    const vy2 = Math.min(stageH, (wrapH - pan.y) / scale);
    if (vx2 <= vx1 || vy2 <= vy1) return null;
    return {
      x1: (vx1 / stageW) * pageWidth,
      y1: (vy1 / stageH) * pageHeight,
      x2: (vx2 / stageW) * pageWidth,
      y2: (vy2 / stageH) * pageHeight
    };
  }

  function visibleLineIDs(all: ParsedLine[]): Set<string> {
    const view = viewportDocBounds();
    if (!view) return new Set(all.map((line) => line.id));
    const visible = new Set<string>();
    for (const line of all) {
      const overlapX = Math.min(view.x2, line.bbox.x2) - Math.max(view.x1, line.bbox.x1);
      const overlapY = Math.min(view.y2, line.bbox.y2) - Math.max(view.y1, line.bbox.y1);
      if (overlapX > 0 && overlapY > 0) visible.add(line.id);
    }
    return visible;
  }

  function clampPanToBounds(x: number, y: number, scale: number): { x: number; y: number } {
    const stageW = image.clientWidth;
    const stageH = image.clientHeight;
    const wrapW = imageWrap.clientWidth;
    const wrapH = imageWrap.clientHeight;
    if (stageW <= 0 || stageH <= 0 || wrapW <= 0 || wrapH <= 0) return { x, y };
    const minX = Math.min(0, wrapW - stageW * scale);
    const minY = Math.min(0, wrapH - stageH * scale);
    const maxX = 0;
    const maxY = 0;
    return {
      x: Math.max(minX, Math.min(maxX, x)),
      y: Math.max(minY, Math.min(maxY, y))
    };
  }

  function panToLine(line: ParsedLine, position: "center" | "top" = "center"): void {
    if (!panzoomInstance) return;
    const stageW = image.clientWidth;
    const stageH = image.clientHeight;
    if (stageW <= 0 || stageH <= 0) return;
    const wrapW = imageWrap.clientWidth;
    const wrapH = imageWrap.clientHeight;
    const scale = currentScale();
    const centerY = ((line.bbox.y1 + line.bbox.y2) / 2 / pageHeight) * stageH;
    const topY = (line.bbox.y1 / pageHeight) * stageH;
    const centerX = ((line.bbox.x1 + line.bbox.x2) / 2 / pageWidth) * stageW;
    let targetX = (wrapW * 0.5) - (centerX * scale);
    let targetY = position === "top"
      ? (wrapH * 0.18) - (topY * scale)
      : (wrapH * 0.5) - (centerY * scale);
    const clamped = clampPanToBounds(targetX, targetY, scale);
    panzoomInstance.pan(clamped.x, clamped.y, { animate: true });
  }

  function applyScale(nextScale: number): void {
    if (!panzoomInstance) return;
    const scale = Math.max(1, Math.min(8, nextScale));
    panzoomInstance.zoom(scale, { animate: false, force: true });
    updateZoomUI();
    renderEditorState();
  }

  function navigableLines(): ParsedLine[] {
    const sorted = orderedLines();
    if (currentScale() <= 1.01) return sorted;
    const ids = visibleLineIDs(sorted);
    const filtered = sorted.filter((line) => ids.has(line.id));
    return filtered.length > 0 ? filtered : sorted;
  }

  function maybeAutoZoomTopLine(): void {
    if (didAutoZoom || !panzoomInstance) return;
    const sorted = orderedLines();
    if (sorted.length <= 10) {
      didAutoZoom = true;
      return;
    }
    const top = sorted[0];
    const lineHeightPx = Math.max(1, ((top.bbox.y2 - top.bbox.y1) / pageHeight) * image.clientHeight);
    const target = Math.max(1.8, Math.min(6, (imageWrap.clientHeight * 0.28) / lineHeightPx));
    applyScale(target);
    setActiveLine(top.id, false);
    panToLine(top, "top");
    renderEditorState();
    didAutoZoom = true;
  }

  function markBoxChange(line: ParsedLine): void {
    if (!line.originalBBox || !sameBBox(line.bbox, line.originalBBox)) changedBoxIDs.add(line.id);
    else changedBoxIDs.delete(line.id);
  }

  function setActiveLine(id: string, shouldRender: boolean = true): void {
    activeLineID = id;
    const line = getLineByID(id);
    if (!line || line.words.length <= 1) {
      activeWordID = "";
    } else if (!line.words.some((w) => w.id === activeWordID)) {
      activeWordID = line.words[0].id;
    }
    if (shouldRender) renderEditorState();
  }

  function setActiveWord(lineID: string, wordID: string, shouldRender: boolean = true): void {
    activeLineID = lineID;
    activeWordID = wordID;
    if (shouldRender) renderEditorState();
  }

  function cloneLines(src: ParsedLine[]): ParsedLine[] {
    return src.map((line) => ({
      id: line.id,
      text: line.text,
      originalText: line.originalText,
      bbox: { ...line.bbox },
      originalBBox: line.originalBBox ? { ...line.originalBBox } : null,
      words: line.words.map((w) => ({ id: w.id, text: w.text, bbox: { ...w.bbox } })),
      exploded: line.exploded
    }));
  }

  function captureSnapshot(): EditorSnapshot {
    return {
      lines: cloneLines(lines),
      activeLineID,
      activeWordID
    };
  }

  function applySnapshot(s: EditorSnapshot): void {
    lines.splice(0, lines.length, ...cloneLines(s.lines));
    activeLineID = s.activeLineID;
    activeWordID = s.activeWordID;
    clearMergeSelections();
    updateMergeModeUI();
    renderEditorState();
  }

  function pushUndoSnapshot(): void {
    undoStack.push(captureSnapshot());
    if (undoStack.length > 200) undoStack.shift();
    redoStack.length = 0;
  }

  function undoEdit(): void {
    if (undoStack.length === 0) return;
    redoStack.push(captureSnapshot());
    const snap = undoStack.pop();
    if (!snap) return;
    applySnapshot(snap);
    markDirty();
  }

  function redoEdit(): void {
    if (redoStack.length === 0) return;
    undoStack.push(captureSnapshot());
    const snap = redoStack.pop();
    if (!snap) return;
    applySnapshot(snap);
    markDirty();
  }

  function restoreInputFocus(lineID: string, wordID: string | null, start: number | null, end: number | null): void {
    requestAnimationFrame(() => {
      const selector = wordID
        ? `input[data-line-id="${lineID}"][data-word-id="${wordID}"]`
        : `input[data-line-id="${lineID}"]:not([data-word-id])`;
      const next = document.querySelector(selector) as HTMLInputElement | null;
      if (!next) return;
      restoringInputFocus = true;
      next.focus();
      if (start !== null && end !== null) {
        try {
          next.setSelectionRange(start, end);
        } catch {
          // ignore non-text selection errors
        }
      }
      requestAnimationFrame(() => {
        restoringInputFocus = false;
      });
    });
  }

  function focusCurrentEditorInput(selectAll: boolean = false): void {
    if (activeLineID === "") return;
    const selectors = activeWordID !== ""
      ? [
        `input[data-line-id="${activeLineID}"][data-word-id="${activeWordID}"]`,
        `input[data-line-id="${activeLineID}"][data-word-id]`
      ]
      : [
        `input[data-line-id="${activeLineID}"]:not([data-word-id])`,
        `input[data-line-id="${activeLineID}"]`
      ];

    const tryFocus = (attempt: number): void => {
      let input: HTMLInputElement | null = null;
      for (const selector of selectors) {
        input = document.querySelector(selector) as HTMLInputElement | null;
        if (input) break;
      }
      if (!input) {
        if (attempt < 2) requestAnimationFrame(() => tryFocus(attempt + 1));
        return;
      }
      restoringInputFocus = true;
      input.focus();
      try {
        if (selectAll) input.setSelectionRange(0, input.value.length);
        else {
          const end = input.value.length;
          input.setSelectionRange(end, end);
        }
      } catch {
        // ignore
      }
      requestAnimationFrame(() => {
        restoringInputFocus = false;
      });
    };
    requestAnimationFrame(() => tryFocus(0));
  }

  function scheduleAutoSave(delayMs: number = 1200): void {
    if (autosaveTimer !== null) {
      window.clearTimeout(autosaveTimer);
    }
    autosaveTimer = window.setTimeout(() => {
      autosaveTimer = null;
      void persistEdits("auto");
    }, delayMs);
  }

  function markDirty(): void {
    scheduleAutoSave();
  }

  async function autoTranscribeDetectedLines(): Promise<void> {
    if (autoTranscribeStarted) return;
    autoTranscribeStarted = true;

    const pending = orderedLines().filter((line) => line.text.trim() === "");
    if (pending.length === 0) return;

    const concurrency = 5;
    let next = 0;
    let completed = 0;
    let success = 0;

    saveStatus.textContent = `transcribing ${pending.length} lines...`;

    const worker = async (): Promise<void> => {
      for (;;) {
        const idx = next;
        next += 1;
        if (idx >= pending.length) return;
        const target = pending[idx];
        try {
          const resp = await fetch(`/v1/ocr/runs/${encodeURIComponent(sessionID)}/transcribe-region`, {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({
              x1: Math.round(target.bbox.x1),
              y1: Math.round(target.bbox.y1),
              x2: Math.round(target.bbox.x2),
              y2: Math.round(target.bbox.y2),
              model: run.model ?? ""
            })
          });
          if (resp.ok) {
            const payload = await resp.json() as { text?: string };
            const text = (payload.text ?? "").trim();
            const live = getLineByID(target.id);
            if (live && live.text.trim() === "" && text !== "") {
              live.text = text;
              live.originalText = text;
              live.words = [];
              changedLineIDs.delete(live.id);
              success += 1;
              markDirty();
              renderEditorState();
            }
          }
        } catch {
          // best-effort
        } finally {
          completed += 1;
          saveStatus.textContent = `transcribing ${completed}/${pending.length} lines...`;
        }
      }
    };

    const workers: Promise<void>[] = [];
    for (let i = 0; i < Math.min(concurrency, pending.length); i += 1) {
      workers.push(worker());
    }
    await Promise.all(workers);
    saveStatus.textContent = `transcribed ${success}/${pending.length} lines`;
  }

  async function persistEdits(mode: "auto" | "manual"): Promise<void> {
    if (autosaveInFlight) {
      autosaveQueued = true;
      return;
    }
    autosaveInFlight = true;
    if (mode === "auto") {
      saveStatus.textContent = "autosaving...";
    } else {
      saveStatus.textContent = "saving...";
    }
    try {
      const correctedHOCR = buildCorrectedHOCR(workingHOCR, lines);
      const payload = await imageClient.saveOCREdits(new SaveOCREditsRequest({
        sessionId: sessionID,
        correctedHocr: correctedHOCR,
        editCount: changedLineIDs.size
      }));
      const stamp = new Date().toLocaleTimeString();
      saveStatus.textContent = `saved ${mode === "auto" ? "(auto)" : ""} text=${payload.editCount} lev=${payload.levenshteinDistance} @ ${stamp}`;
    } catch {
      saveStatus.textContent = "Failed to save edits";
    } finally {
      autosaveInFlight = false;
      if (autosaveQueued) {
        autosaveQueued = false;
        scheduleAutoSave(250);
      }
    }
  }

  function orderedLines(): ParsedLine[] {
    return [...lines].sort((a, b) => (a.bbox.y1 === b.bbox.y1 ? a.bbox.x1 - b.bbox.x1 : a.bbox.y1 - b.bbox.y1));
  }

  function setAddMode(next: boolean): void {
    isAddBoxMode = next;
    addBoxBtn.className = isAddBoxMode
      ? "rounded border border-cyan-400 px-2 py-1 text-xs bg-cyan-900/30"
      : "rounded border border-slate-600 px-2 py-1 text-xs hover:bg-slate-800";
  }

  function wordKey(lineID: string, wordID: string): string {
    return `${lineID}::${wordID}`;
  }

  function clearMergeSelections(): void {
    selectedLineIDs.clear();
    selectedWordKeys.clear();
  }

  function updateMergeModeUI(): void {
    mergeModeBtn.className = isMergeMode
      ? "rounded border border-emerald-400 px-2 py-1 text-xs bg-emerald-900/30"
      : "rounded border border-slate-600 px-2 py-1 text-xs hover:bg-slate-800";

    const canMergeWords = selectedWordKeys.size >= 2;
    const canMergeLines = selectedWordKeys.size === 0 && selectedLineIDs.size >= 2;
    if (isMergeMode && (canMergeWords || canMergeLines)) {
      mergeSelectedBtn.classList.remove("hidden");
    } else {
      mergeSelectedBtn.classList.add("hidden");
    }
  }

  function setMergeMode(next: boolean): void {
    isMergeMode = next;
    if (!isMergeMode) clearMergeSelections();
    updateMergeModeUI();
    renderEditorState();
  }

  function deleteActiveLine(): void {
    if (!activeLineID) return;
    const idx = lines.findIndex((line) => line.id === activeLineID);
    if (idx < 0) return;
    lines.splice(idx, 1);
    changedLineIDs.add(activeLineID);
    changedBoxIDs.add(activeLineID);
    activeLineID = lines.length > 0 ? orderedLines()[0].id : "";
    activeWordID = "";
    renderEditorState();
  }

  function deleteWordFromLine(lineID: string, wordID: string): void {
    const line = getLineByID(lineID);
    if (!line || line.words.length <= 1) return;
    const idx = line.words.findIndex((w) => w.id === wordID);
    if (idx < 0) return;
    line.words.splice(idx, 1);
    line.text = line.words.map((w) => w.text.trim()).filter((w) => w !== "").join(" ");
    if (line.words.length <= 1) {
      line.exploded = false;
      activeWordID = "";
      if (line.words.length === 1) {
        line.text = line.words[0].text;
      }
      line.words = [];
    } else {
      const next = line.words[Math.min(idx, line.words.length - 1)];
      activeWordID = next.id;
    }
    ensureLineContainsWords(line);
    markBoxChange(line);
    changedLineIDs.add(line.id);
    changedBoxIDs.add(line.id);
    renderEditorState();
  }

  function roundInt(v: number): number {
    return Math.round(v);
  }

  function clampBox(box: { x1: number; y1: number; x2: number; y2: number }): { x1: number; y1: number; x2: number; y2: number } {
    const minW = 4;
    const minH = 4;
    let x1 = roundInt(Math.max(0, Math.min(pageWidth, box.x1)));
    let x2 = roundInt(Math.max(0, Math.min(pageWidth, box.x2)));
    let y1 = roundInt(Math.max(0, Math.min(pageHeight, box.y1)));
    let y2 = roundInt(Math.max(0, Math.min(pageHeight, box.y2)));
    if (x2 < x1) [x1, x2] = [x2, x1];
    if (y2 < y1) [y1, y2] = [y2, y1];
    if (x2 - x1 < minW) x2 = Math.min(pageWidth, x1 + minW);
    if (y2 - y1 < minH) y2 = Math.min(pageHeight, y1 + minH);
    return { x1: roundInt(x1), y1: roundInt(y1), x2: roundInt(x2), y2: roundInt(y2) };
  }

  function wordsFromText(text: string): string[] {
    return text.trim() === "" ? [] : text.trim().split(/\s+/).filter(Boolean);
  }

  function distributeWordsInLine(line: ParsedLine, wordTexts?: string[]): ParsedWord[] {
    const words = wordTexts ?? wordsFromText(line.text);
    if (words.length === 0) return [];
    const fullWidth = Math.max(1, line.bbox.x2 - line.bbox.x1);
    const units = words.reduce((sum, word) => sum + Math.max(1, word.length), 0);
    let x = line.bbox.x1;
    const out: ParsedWord[] = [];
    for (let i = 0; i < words.length; i += 1) {
      const word = words[i];
      const ratio = Math.max(1, word.length) / units;
      const w = Math.max(6, Math.round(fullWidth * ratio));
      const nextX = i === words.length - 1 ? line.bbox.x2 : x + w;
      out.push({
        id: `${line.id}_w_${i + 1}`,
        text: word,
        bbox: clampBox({ x1: x, y1: line.bbox.y1, x2: nextX, y2: line.bbox.y2 })
      });
      x = nextX;
    }
    return out;
  }

  function refreshWordBoxesForLine(line: ParsedLine): void {
    if (line.words.length === 0) {
      line.words = [];
      return;
    }
    const words = wordsFromText(line.text);
    if (line.words.length <= 1 && words.length <= 1) {
      line.words = words.length === 1 ? [{ id: `${line.id}_w_1`, text: words[0], bbox: { ...line.bbox } }] : [];
      return;
    }
    line.words = distributeWordsInLine(line, words);
  }

  function clampWordBox(box: BBox, line: ParsedLine): BBox {
    const minW = 3;
    let x1 = roundInt(Math.max(0, Math.min(pageWidth, box.x1)));
    let x2 = roundInt(Math.max(0, Math.min(pageWidth, box.x2)));
    if (x2 < x1) [x1, x2] = [x2, x1];
    if (x2-x1 < minW) {
      x2 = Math.min(pageWidth, x1+minW);
    }
    return {
      x1: roundInt(x1),
      y1: line.bbox.y1,
      x2: roundInt(x2),
      y2: line.bbox.y2
    };
  }

  function ensureLineContainsWords(line: ParsedLine): void {
    if (line.words.length === 0) return;
    let minX = line.bbox.x1;
    let maxX = line.bbox.x2;
    for (const word of line.words) {
      if (word.bbox.x1 < minX) minX = word.bbox.x1;
      if (word.bbox.x2 > maxX) maxX = word.bbox.x2;
      word.bbox.y1 = line.bbox.y1;
      word.bbox.y2 = line.bbox.y2;
    }
    line.bbox = clampBox({
      x1: minX,
      y1: line.bbox.y1,
      x2: maxX,
      y2: line.bbox.y2
    });
  }

  function pointerToDoc(clientX: number, clientY: number): { x: number; y: number } | null {
    if (image.classList.contains("hidden")) return null;
    const rect = image.getBoundingClientRect();
    if (rect.width <= 0 || rect.height <= 0) return null;
    const x = ((clientX - rect.left) / rect.width) * pageWidth;
    const y = ((clientY - rect.top) / rect.height) * pageHeight;
    return { x: Math.max(0, Math.min(pageWidth, x)), y: Math.max(0, Math.min(pageHeight, y)) };
  }

  function applyResize(startBox: { x1: number; y1: number; x2: number; y2: number }, handle: string, dx: number, dy: number) {
    const out = { ...startBox };
    if (handle.includes("n")) out.y1 = startBox.y1 + dy;
    if (handle.includes("s")) out.y2 = startBox.y2 + dy;
    if (handle.includes("w")) out.x1 = startBox.x1 + dx;
    if (handle.includes("e")) out.x2 = startBox.x2 + dx;
    return clampBox(out);
  }

  function positionHandle(el: HTMLDivElement, handle: string): void {
    if (handle.includes("n")) el.style.top = "-4px";
    if (handle.includes("s")) el.style.bottom = "-4px";
    if (handle.includes("w")) el.style.left = "-4px";
    if (handle.includes("e")) el.style.right = "-4px";
    if (handle === "n" || handle === "s") {
      el.style.left = "50%";
      el.style.transform = "translateX(-50%)";
    }
    if (handle === "e" || handle === "w") {
      el.style.top = "50%";
      el.style.transform = "translateY(-50%)";
    }
  }

  function renderEditorState(): void {
    syncEditorHeights();
    lineOverlay.innerHTML = "";
    lineList.innerHTML = "";
    if (isMergeMode && !image.classList.contains("hidden")) {
      const dim = document.createElement("div");
      dim.className = "absolute inset-0 bg-slate-950/70";
      dim.style.pointerEvents = "none";
      lineOverlay.appendChild(dim);
    }
    const sorted = [...lines].sort((a, b) => (a.bbox.y1 === b.bbox.y1 ? a.bbox.x1 - b.bbox.x1 : a.bbox.y1 - b.bbox.y1));

    for (const line of sorted) {
      if (!image.classList.contains("hidden")) {
        const marker = document.createElement("div");
        const isActiveLine = line.id === activeLineID;
        const activeLineInWordMode = isActiveLine && line.words.length > 1;
        const isLineSelected = selectedLineIDs.has(line.id);
        marker.className = `absolute border ${isMergeMode
          ? (isLineSelected ? "border-emerald-300 bg-emerald-300/20" : "border-slate-600/70 bg-transparent")
          : activeLineInWordMode
            ? "border-slate-500/70 bg-transparent"
            : isActiveLine
              ? "border-cyan-300 bg-cyan-300/20"
              : "border-amber-400/70 bg-amber-300/20"
        }`;
        marker.style.left = `${(line.bbox.x1 / pageWidth) * 100}%`;
        marker.style.top = `${(line.bbox.y1 / pageHeight) * 100}%`;
        marker.style.width = `${Math.max(0.5, ((line.bbox.x2 - line.bbox.x1) / pageWidth) * 100)}%`;
        marker.style.height = `${Math.max(1.2, ((line.bbox.y2 - line.bbox.y1) / pageHeight) * 100)}%`;
        marker.style.cursor = "move";
        if (!isMergeMode && isActiveLine && !activeLineInWordMode) {
          marker.style.boxShadow = "0 0 0 9999px rgba(2,6,23,0.55)";
        }
        if (isMergeMode) {
          marker.style.cursor = "pointer";
        }

        marker.addEventListener("mousedown", (e) => {
          if (isMergeMode) return;
          e.preventDefault();
          const doc = pointerToDoc(e.clientX, e.clientY);
          if (!doc) return;
          pushUndoSnapshot();
          setActiveLine(line.id);
          interaction = { lineID: line.id, mode: "move", handle: "", startDocX: doc.x, startDocY: doc.y, startBox: { ...line.bbox } };
        });
        marker.addEventListener("click", () => {
          if (isMergeMode) {
            if (selectedLineIDs.has(line.id)) selectedLineIDs.delete(line.id);
            else selectedLineIDs.add(line.id);
            selectedWordKeys.clear();
            updateMergeModeUI();
            renderEditorState();
            return;
          }
          setActiveLine(line.id);
        });

        if (line.id === activeLineID) {
          for (const handle of ["n", "s", "e", "w", "nw", "ne", "sw", "se"]) {
            const h = document.createElement("div");
            h.dataset.handle = handle;
            h.className = "absolute h-2 w-2 bg-cyan-200";
            h.style.pointerEvents = "auto";
            h.style.cursor = `${handle}-resize`;
            positionHandle(h, handle);
            h.addEventListener("mousedown", (e) => {
              if (isMergeMode) return;
              e.stopPropagation();
              e.preventDefault();
              const doc = pointerToDoc(e.clientX, e.clientY);
              if (!doc) return;
              pushUndoSnapshot();
              interaction = { lineID: line.id, mode: "resize", handle, startDocX: doc.x, startDocY: doc.y, startBox: { ...line.bbox } };
            });
            marker.appendChild(h);
          }
        }
        lineOverlay.appendChild(marker);

        if (line.id === activeLineID && line.words.length > 1) {
          for (const word of line.words) {
            const wordBox = document.createElement("div");
            const isWordActive = word.id === activeWordID;
            wordBox.className = `absolute border ${isWordActive ? "border-cyan-300 bg-cyan-300/20" : "border-emerald-300/90 bg-emerald-300/10"}`;
            wordBox.style.left = `${(word.bbox.x1 / pageWidth) * 100}%`;
            wordBox.style.top = `${(word.bbox.y1 / pageHeight) * 100}%`;
            wordBox.style.width = `${Math.max(0.3, ((word.bbox.x2 - word.bbox.x1) / pageWidth) * 100)}%`;
            wordBox.style.height = `${Math.max(1, ((word.bbox.y2 - word.bbox.y1) / pageHeight) * 100)}%`;
            wordBox.style.cursor = "move";
            const key = wordKey(line.id, word.id);
            const isWordSelected = selectedWordKeys.has(key);
            if (isMergeMode) {
              wordBox.className = isWordSelected
                ? "absolute border border-emerald-300 bg-emerald-300/20"
                : "absolute border border-slate-600/70 bg-transparent";
            }
            if (!isMergeMode && isWordActive) {
              wordBox.style.boxShadow = "0 0 0 9999px rgba(2,6,23,0.55)";
            }
            wordBox.addEventListener("click", (e) => {
              e.stopPropagation();
              if (isMergeMode) {
                if (selectedWordKeys.has(key)) selectedWordKeys.delete(key);
                else selectedWordKeys.add(key);
                selectedLineIDs.clear();
                updateMergeModeUI();
                renderEditorState();
                return;
              }
              setActiveWord(line.id, word.id);
            });
            wordBox.addEventListener("mousedown", (e) => {
              if (isMergeMode) return;
              e.stopPropagation();
              e.preventDefault();
              const doc = pointerToDoc(e.clientX, e.clientY);
              if (!doc) return;
              pushUndoSnapshot();
              setActiveWord(line.id, word.id);
              wordInteraction = {
                lineID: line.id,
                wordID: word.id,
                mode: "move",
                handle: "e",
                startDocX: doc.x,
                startBox: { ...word.bbox }
              };
            });

            if (isWordActive) {
              for (const handle of ["w", "e"] as const) {
                const h = document.createElement("div");
                h.className = "absolute h-2 w-2 bg-cyan-200";
                h.style.top = "50%";
                h.style.transform = "translateY(-50%)";
                h.style.cursor = `${handle}-resize`;
                if (handle === "w") h.style.left = "-4px";
                if (handle === "e") h.style.right = "-4px";
                h.addEventListener("mousedown", (e) => {
                  if (isMergeMode) return;
                  e.stopPropagation();
                  e.preventDefault();
                  const doc = pointerToDoc(e.clientX, e.clientY);
                  if (!doc) return;
                  pushUndoSnapshot();
                  wordInteraction = {
                    lineID: line.id,
                    wordID: word.id,
                    mode: "resize",
                    handle,
                    startDocX: doc.x,
                    startBox: { ...word.bbox }
                  };
                });
                wordBox.appendChild(h);
              }
            }
            lineOverlay.appendChild(wordBox);
          }
        }
      }

      const row = document.createElement("div");
      const yMid = ((line.bbox.y1 + line.bbox.y2) / 2 / pageHeight) * 100;
      row.className = "absolute left-1 right-1";
      row.style.top = `${Math.max(0, Math.min(98, yMid))}%`;
      row.style.transform = "translateY(-50%)";
      row.style.zIndex = line.id === activeLineID ? "20" : "10";

      if (line.words.length > 1) {
        const wordsWrap = document.createElement("div");
        wordsWrap.className = "flex w-full gap-1";
        const lineWidth = Math.max(1, line.bbox.x2 - line.bbox.x1);
        for (const word of line.words) {
          const key = wordKey(line.id, word.id);
          const isWordMergeSelected = selectedWordKeys.has(key);
          const wInput = document.createElement("input");
          wInput.type = "text";
          wInput.dataset.lineId = line.id;
          wInput.dataset.wordId = word.id;
          wInput.className = `min-w-[3.5rem] rounded border px-2 py-1 text-sm leading-tight ${
            isMergeMode
              ? (isWordMergeSelected ? "border-emerald-400 bg-slate-900" : "border-slate-700 bg-slate-950/80")
              : word.id === activeWordID ? "border-cyan-400 bg-slate-900" : "border-slate-700 bg-slate-950"
          }`;
          wInput.value = word.text;
          wInput.setAttribute("value", word.text);
          const wordWidth = Math.max(1, word.bbox.x2 - word.bbox.x1);
          const widthPct = Math.max(2, (wordWidth / lineWidth) * 100);
          wInput.style.flex = `0 0 ${widthPct}%`;
          wInput.style.width = `${widthPct}%`;
          if (isMergeMode) {
            wInput.readOnly = true;
            wInput.addEventListener("click", (e) => {
              e.preventDefault();
              if (selectedWordKeys.has(key)) selectedWordKeys.delete(key);
              else selectedWordKeys.add(key);
              selectedLineIDs.clear();
              updateMergeModeUI();
              renderEditorState();
            });
            wordsWrap.appendChild(wInput);
            continue;
          }
          wInput.addEventListener("focus", () => {
            if (restoringInputFocus) return;
            if (activeLineID === line.id && activeWordID === word.id) return;
            const start = wInput.selectionStart;
            const end = wInput.selectionEnd;
            setActiveWord(line.id, word.id);
            restoreInputFocus(line.id, word.id, start, end);
          });
          wInput.addEventListener("input", () => {
            pushUndoSnapshot();
            word.text = wInput.value;
            line.text = line.words.map((w) => w.text.trim()).filter((w) => w !== "").join(" ");
            if (line.text.trim() !== line.originalText.trim()) changedLineIDs.add(line.id);
            else changedLineIDs.delete(line.id);
            markDirty();
          });
          wordsWrap.appendChild(wInput);
        }
        row.appendChild(wordsWrap);
      } else {
        const isLineMergeSelected = selectedLineIDs.has(line.id);
        const input = document.createElement("input");
        input.type = "text";
        input.dataset.lineId = line.id;
        input.className = `w-full rounded border px-2 py-1 text-sm leading-tight ${
          isMergeMode
            ? (isLineMergeSelected ? "border-emerald-400 bg-slate-900" : "border-slate-700 bg-slate-950/80")
            : line.id === activeLineID ? "border-cyan-400 bg-slate-900" : "border-slate-700 bg-slate-950"
        }`;
        input.value = line.text;
        input.setAttribute("value", line.text);
        if (isMergeMode) {
          input.readOnly = true;
          input.addEventListener("click", (e) => {
            e.preventDefault();
            if (selectedLineIDs.has(line.id)) selectedLineIDs.delete(line.id);
            else selectedLineIDs.add(line.id);
            selectedWordKeys.clear();
            updateMergeModeUI();
            renderEditorState();
          });
          row.appendChild(input);
          lineList.appendChild(row);
          continue;
        }
        input.addEventListener("focus", () => {
          if (restoringInputFocus) return;
          if (activeLineID === line.id && (line.words.length <= 1 || activeWordID === "")) return;
          const start = input.selectionStart;
          const end = input.selectionEnd;
          setActiveLine(line.id);
          restoreInputFocus(line.id, null, start, end);
        });
        input.addEventListener("input", () => {
          pushUndoSnapshot();
          line.text = input.value;
          refreshWordBoxesForLine(line);
          if (line.text.trim() !== line.originalText.trim()) changedLineIDs.add(line.id);
          else changedLineIDs.delete(line.id);
          markDirty();
        });
        row.appendChild(input);
      }
      lineList.appendChild(row);
    }

    const active = getLineByID(activeLineID);
    if (active) {
      if (activeWordID !== "") {
        const word = getWordByID(active, activeWordID);
        if (word) {
          lineInfo.textContent = `${active.id}:${word.id} | (${word.bbox.x1},${word.bbox.y1})-(${word.bbox.x2},${word.bbox.y2})`;
        } else {
          lineInfo.textContent = `${active.id} | (${active.bbox.x1},${active.bbox.y1})-(${active.bbox.x2},${active.bbox.y2})`;
        }
      } else {
        lineInfo.textContent = `${active.id} | (${active.bbox.x1},${active.bbox.y1})-(${active.bbox.x2},${active.bbox.y2})`;
      }
    } else {
      lineInfo.textContent = "";
    }
  }

  window.addEventListener("mousemove", (e) => {
    if (wordInteraction) {
      const doc = pointerToDoc(e.clientX, e.clientY);
      if (!doc) return;
      const line = getLineByID(wordInteraction.lineID);
      if (!line) return;
      const word = getWordByID(line, wordInteraction.wordID);
      if (!word) return;

      const dx = doc.x - wordInteraction.startDocX;
      if (wordInteraction.mode === "move") {
        word.bbox = clampWordBox({
          x1: wordInteraction.startBox.x1 + dx,
          y1: line.bbox.y1,
          x2: wordInteraction.startBox.x2 + dx,
          y2: line.bbox.y2
        }, line);
      } else {
        const next = { ...wordInteraction.startBox };
        if (wordInteraction.handle === "w") next.x1 = wordInteraction.startBox.x1 + dx;
        if (wordInteraction.handle === "e") next.x2 = wordInteraction.startBox.x2 + dx;
        word.bbox = clampWordBox(next, line);
      }
      ensureLineContainsWords(line);
      markBoxChange(line);
      changedLineIDs.add(line.id);
      changedBoxIDs.add(line.id);
      dragDirty = true;
      renderEditorState();
      return;
    }

    if (!interaction) return;
    const doc = pointerToDoc(e.clientX, e.clientY);
    if (!doc) return;
    const line = getLineByID(interaction.lineID);
    if (!line) return;

    const dx = doc.x - interaction.startDocX;
    const dy = doc.y - interaction.startDocY;
    if (interaction.mode === "move") {
      line.bbox = clampBox({ x1: interaction.startBox.x1 + dx, y1: interaction.startBox.y1 + dy, x2: interaction.startBox.x2 + dx, y2: interaction.startBox.y2 + dy });
      refreshWordBoxesForLine(line);
      markBoxChange(line);
      dragDirty = true;
      renderEditorState();
      return;
    }
    if (interaction.mode === "resize") {
      line.bbox = applyResize(interaction.startBox, interaction.handle, dx, dy);
      refreshWordBoxesForLine(line);
      markBoxChange(line);
      dragDirty = true;
      renderEditorState();
      return;
    }
    if (interaction.mode === "draw") {
      line.bbox = applyResize(interaction.startBox, "se", dx, dy);
      refreshWordBoxesForLine(line);
      markBoxChange(line);
      dragDirty = true;
      renderEditorState();
    }
  });

  window.addEventListener("mouseup", () => {
    interaction = null;
    wordInteraction = null;
    if (dragDirty) {
      dragDirty = false;
      markDirty();
    }
  });

  addBoxBtn.addEventListener("click", () => {
    setAddMode(!isAddBoxMode);
  });

  lineOverlay.addEventListener("mousedown", (e) => {
    if (isMergeMode || !isAddBoxMode || e.target !== lineOverlay) return;
    const doc = pointerToDoc(e.clientX, e.clientY);
    if (!doc) return;
    pushUndoSnapshot();
    const id = `line_new_${nextLineCounter++}`;
    const newLine: ParsedLine = {
      id,
      text: "",
      originalText: "",
      bbox: { x1: roundInt(doc.x), y1: roundInt(doc.y), x2: roundInt(doc.x + 6), y2: roundInt(doc.y + 6) },
      originalBBox: null,
      words: [],
      exploded: false
    };
    lines.push(newLine);
    changedLineIDs.add(id);
    changedBoxIDs.add(id);
    activeWordID = "";
    setActiveLine(id);
    interaction = { lineID: id, mode: "draw", handle: "se", startDocX: doc.x, startDocY: doc.y, startBox: { ...newLine.bbox } };
    markDirty();
    renderEditorState();
  });

  splitLineBtn.addEventListener("click", () => {
    const line = getLineByID(activeLineID);
    if (!line) return;
    pushUndoSnapshot();
    const original = { ...line.bbox };
    const splitY = Math.round((original.y1 + original.y2) / 2);
    const words = line.text.trim().split(/\s+/).filter(Boolean);
    let topText = "";
    let bottomText = "";
    if (words.length >= 2) {
      const mid = Math.ceil(words.length / 2);
      topText = words.slice(0, mid).join(" ");
      bottomText = words.slice(mid).join(" ");
    } else if (words.length === 1) {
      const w = words[0];
      const mid = Math.max(1, Math.floor(w.length / 2));
      topText = w.slice(0, mid);
      bottomText = w.slice(mid);
    }

    line.bbox = clampBox({ x1: original.x1, y1: original.y1, x2: original.x2, y2: splitY });
    line.text = topText;
    line.exploded = false;
    line.words = [];
    markBoxChange(line);
    changedLineIDs.add(line.id);

    const id = `line_new_${nextLineCounter++}`;
    const bottom: ParsedLine = {
      id,
      text: bottomText,
      originalText: "",
      bbox: clampBox({ x1: original.x1, y1: splitY, x2: original.x2, y2: original.y2 }),
      originalBBox: null,
      words: [],
      exploded: false
    };
    bottom.words = [];
    lines.push(bottom);
    changedLineIDs.add(id);
    changedBoxIDs.add(id);
    activeWordID = "";
    setActiveLine(line.id);
    markDirty();
    renderEditorState();
  });

  explodeLineBtn.addEventListener("click", () => {
    const line = getLineByID(activeLineID);
    if (!line) return;
    const words = line.text.trim().split(/\s+/).filter(Boolean);
    if (words.length <= 1) return;
    pushUndoSnapshot();
    line.exploded = true;
    line.words = distributeWordsInLine(line, words);
    changedLineIDs.add(line.id);
    changedBoxIDs.add(line.id);
    activeWordID = line.words.length > 0 ? line.words[0].id : "";
    setActiveLine(line.id);
    markDirty();
    renderEditorState();
  });

  transcribeBoxBtn.addEventListener("click", async () => {
    const line = getLineByID(activeLineID);
    if (!line) return;
    transcribeBoxBtn.disabled = true;
    saveStatus.textContent = "transcribing selected box...";
    try {
      const resp = await fetch(`/v1/ocr/runs/${encodeURIComponent(sessionID)}/transcribe-region`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          x1: Math.round(line.bbox.x1),
          y1: Math.round(line.bbox.y1),
          x2: Math.round(line.bbox.x2),
          y2: Math.round(line.bbox.y2),
          provider: run.provider ?? "",
          model: run.model ?? ""
        })
      });
      if (!resp.ok) {
        const msg = await resp.text();
        throw new Error(msg || "transcription failed");
      }
      const payload = await resp.json() as { text?: string };
      const text = (payload.text ?? "").trim();
      if (text === "") {
        throw new Error("empty transcription");
      }
      pushUndoSnapshot();
      line.text = text;
      line.words = [];
      activeWordID = "";
      changedLineIDs.add(line.id);
      markDirty();
      renderEditorState();
      focusCurrentEditorInput(true);
      saveStatus.textContent = "box transcribed";
    } catch (err) {
      const msg = err instanceof Error ? err.message : "transcription failed";
      saveStatus.textContent = `transcribe failed: ${msg}`;
    } finally {
      transcribeBoxBtn.disabled = false;
    }
  });

  mergeModeBtn.addEventListener("click", () => {
    setMergeMode(!isMergeMode);
  });

  mergeSelectedBtn.addEventListener("click", () => {
    if (!isMergeMode) return;

    if (selectedWordKeys.size >= 2) {
      const parsed = [...selectedWordKeys].map((k) => k.split("::"));
      const lineID = parsed[0]?.[0] ?? "";
      if (lineID === "" || !parsed.every((p) => p[0] === lineID)) return;
      const line = getLineByID(lineID);
      if (!line || line.words.length <= 1) return;
      const selectedIDs = new Set(parsed.map((p) => p[1]));
      const selectedWords = line.words.filter((w) => selectedIDs.has(w.id));
      if (selectedWords.length < 2) return;

      pushUndoSnapshot();
      const remaining = line.words.filter((w) => !selectedIDs.has(w.id));
      const sortedSelected = [...selectedWords].sort((a, b) => a.bbox.x1 - b.bbox.x1);
      const merged: ParsedWord = {
        id: sortedSelected[0].id,
        text: sortedSelected.map((w) => w.text).join(" ").trim(),
        bbox: {
          x1: Math.min(...sortedSelected.map((w) => w.bbox.x1)),
          y1: line.bbox.y1,
          x2: Math.max(...sortedSelected.map((w) => w.bbox.x2)),
          y2: line.bbox.y2
        }
      };

      line.words = [...remaining, merged].sort((a, b) => a.bbox.x1 - b.bbox.x1);
      line.text = line.words.map((w) => w.text.trim()).filter((w) => w !== "").join(" ");
      if (line.words.length <= 1) line.exploded = false;
      ensureLineContainsWords(line);
      markBoxChange(line);
      changedLineIDs.add(line.id);
      changedBoxIDs.add(line.id);
      clearMergeSelections();
      activeLineID = line.id;
      activeWordID = line.words.length > 1 ? merged.id : "";
      updateMergeModeUI();
      markDirty();
      renderEditorState();
      return;
    }

    if (selectedLineIDs.size >= 2) {
      const selected = lines.filter((l) => selectedLineIDs.has(l.id));
      if (selected.length < 2) return;
      pushUndoSnapshot();

      selected.sort((a, b) => (a.bbox.y1 === b.bbox.y1 ? a.bbox.x1 - b.bbox.x1 : a.bbox.y1 - b.bbox.y1));
      const base = selected[0];
      base.text = selected.map((l) => l.text.trim()).filter((t) => t !== "").join(" ");
      base.exploded = false;
      base.words = [];
      base.bbox = clampBox({
        x1: Math.min(...selected.map((l) => l.bbox.x1)),
        y1: Math.min(...selected.map((l) => l.bbox.y1)),
        x2: Math.max(...selected.map((l) => l.bbox.x2)),
        y2: Math.max(...selected.map((l) => l.bbox.y2))
      });
      markBoxChange(base);
      changedLineIDs.add(base.id);
      changedBoxIDs.add(base.id);

      const removeIDs = new Set(selected.slice(1).map((l) => l.id));
      for (let i = lines.length - 1; i >= 0; i -= 1) {
        if (removeIDs.has(lines[i].id)) {
          changedLineIDs.add(lines[i].id);
          changedBoxIDs.add(lines[i].id);
          lines.splice(i, 1);
        }
      }

      clearMergeSelections();
      activeLineID = base.id;
      activeWordID = "";
      updateMergeModeUI();
      markDirty();
      renderEditorState();
    }
  });

  resetBtn.addEventListener("click", () => {
    pushUndoSnapshot();
    const resetParsed = parseHOCR(run.original_hocr);
    lines.splice(0, lines.length, ...resetParsed.lines);
    activeLineID = lines.length > 0 ? lines[0].id : "";
    activeWordID = "";
    clearMergeSelections();
    updateMergeModeUI();
    nextLineCounter = lines.length + 1;
    markDirty();
    renderEditorState();
  });

  deleteBoxBtn.addEventListener("click", () => {
    pushUndoSnapshot();
    deleteActiveLine();
    markDirty();
  });

  undoBtn.addEventListener("click", undoEdit);
  redoBtn.addEventListener("click", redoEdit);

  updateMergeModeUI();
  renderEditorState();
  void autoTranscribeDetectedLines();
  image.addEventListener("load", () => {
    syncEditorHeights();
    renderEditorState();
  });
  window.addEventListener("resize", renderEditorState);
  window.addEventListener("keydown", (event) => {
    const target = event.target as HTMLElement | null;
    const inTextField = !!target && (target.tagName === "INPUT" || target.tagName === "TEXTAREA" || target.isContentEditable);
    const isMod = event.ctrlKey || event.metaKey;
    const targetInput = target instanceof HTMLInputElement ? target : null;
    const targetLineID = targetInput?.dataset.lineId ?? "";
    const targetWordID = targetInput?.dataset.wordId ?? "";

    if (isMod && (event.key === "z" || event.key === "Z")) {
      event.preventDefault();
      if (event.shiftKey) redoEdit();
      else undoEdit();
      return;
    }

    if (isMod && (event.key === "y" || event.key === "Y")) {
      event.preventDefault();
      redoEdit();
      return;
    }

    if (isMod && (event.key === "Delete" || event.key === "Backspace") && inTextField && targetLineID !== "") {
      event.preventDefault();
      pushUndoSnapshot();
      if (targetWordID !== "") {
        deleteWordFromLine(targetLineID, targetWordID);
        focusCurrentEditorInput();
      } else {
        activeLineID = targetLineID;
        deleteActiveLine();
        focusCurrentEditorInput();
      }
      markDirty();
      return;
    }

    if (event.key === "Tab") {
      event.preventDefault();
      const sorted = orderedLines();
      if (sorted.length === 0) return;
      const baseLineID = targetLineID !== "" ? targetLineID : activeLineID;
      const lineIdx = sorted.findIndex((line) => line.id === baseLineID);
      const currentLine = lineIdx >= 0 ? sorted[lineIdx] : sorted[0];
      const currentWordID = targetWordID !== "" ? targetWordID : activeWordID;
      if (currentLine.words.length > 1) {
        const wordIdx = currentLine.words.findIndex((w) => w.id === currentWordID);
        if (wordIdx >= 0) {
          const step = event.shiftKey ? -1 : 1;
          const nextWordIdx = wordIdx + step;
          if (nextWordIdx >= 0 && nextWordIdx < currentLine.words.length) {
            setActiveWord(currentLine.id, currentLine.words[nextWordIdx].id);
            focusCurrentEditorInput();
            return;
          }
          const nextLineIdx = event.shiftKey
            ? (lineIdx - 1 + sorted.length) % sorted.length
            : (lineIdx + 1) % sorted.length;
          const nextLine = sorted[nextLineIdx];
          if (nextLine.words.length > 1) {
            const nextWord = event.shiftKey ? nextLine.words[nextLine.words.length - 1] : nextLine.words[0];
            setActiveWord(nextLine.id, nextWord.id);
            focusCurrentEditorInput();
          } else {
            setActiveLine(nextLine.id);
            focusCurrentEditorInput();
          }
          return;
        }
      }
      const nextLineIdx = event.shiftKey
        ? ((lineIdx >= 0 ? lineIdx : 0) - 1 + sorted.length) % sorted.length
        : ((lineIdx >= 0 ? lineIdx : 0) + 1) % sorted.length;
      const nextLine = sorted[nextLineIdx];
      if (nextLine.words.length > 1) {
        const nextWord = event.shiftKey ? nextLine.words[nextLine.words.length - 1] : nextLine.words[0];
        setActiveWord(nextLine.id, nextWord.id);
        focusCurrentEditorInput();
      } else {
        setActiveLine(nextLine.id);
        focusCurrentEditorInput();
      }
      return;
    }

    if (event.key === "Enter") {
      event.preventDefault();
      if (isMergeMode) {
        mergeSelectedBtn.click();
        return;
      }
      saveBtn.click();
      return;
    }

    if (event.key === "Delete" && !inTextField) {
      event.preventDefault();
      pushUndoSnapshot();
      deleteActiveLine();
      markDirty();
      return;
    }

    if ((event.key === "d" || event.key === "D") && !inTextField) {
      event.preventDefault();
      setAddMode(!isAddBoxMode);
      return;
    }

    if ((event.key === "s" || event.key === "S") && !inTextField && !event.repeat) {
      event.preventDefault();
      splitLineBtn.click();
      return;
    }

    if ((event.key === "e" || event.key === "E") && !inTextField && !event.repeat) {
      event.preventDefault();
      explodeLineBtn.click();
      return;
    }

    if ((event.key === "m" || event.key === "M") && !inTextField && !event.repeat) {
      event.preventDefault();
      setMergeMode(!isMergeMode);
      return;
    }

    if (event.key === "Escape") {
      event.preventDefault();
      activeLineID = "";
      activeWordID = "";
      setAddMode(false);
      setMergeMode(false);
      renderEditorState();
    }
  });

  saveBtn.addEventListener("click", async () => {
    await persistEdits("manual");
  });
}

function parseHOCR(hocrXML: string): { lines: ParsedLine[]; pageWidth: number; pageHeight: number } {
  const doc = parseHOCRDocument(hocrXML);

  const page = firstElementWithClass(doc, "ocr_page");
  const pageBBox = parseBBox(page?.getAttribute("title") ?? "") ?? { x1: 0, y1: 0, x2: 1, y2: 1 };

  const lineNodes = elementsWithClass(doc, "ocr_line");
  const lines: ParsedLine[] = lineNodes.map((node, idx) => {
    const id = node.getAttribute("id") ?? `line_${idx + 1}`;
    const bbox = parseBBox(node.getAttribute("title") ?? "") ?? { x1: 0, y1: 0, x2: 1, y2: 1 };
    const words: ParsedWord[] = elementsWithClass(node, "ocrx_word")
      .map((w, wordIndex) => {
        const text = (w.textContent ?? "").trim();
        const wordBBox = parseBBox(w.getAttribute("title") ?? "") ?? { ...bbox };
        return {
          id: w.getAttribute("id") ?? `${id}_w_${wordIndex + 1}`,
          text,
          bbox: wordBBox
        };
      })
      .filter((w) => w.text !== "");
    const text = words.length > 0 ? words.map((w) => w.text).join(" ") : (node.textContent ?? "").trim();
    const exploded = words.length > 1;

    return { id, bbox, text, originalText: text, originalBBox: { ...bbox }, words, exploded };
  });

  return {
    lines,
    pageWidth: Math.max(1, pageBBox.x2 - pageBBox.x1),
    pageHeight: Math.max(1, pageBBox.y2 - pageBBox.y1)
  };
}

function sameBBox(
  a: { x1: number; y1: number; x2: number; y2: number },
  b: { x1: number; y1: number; x2: number; y2: number }
): boolean {
  return a.x1 === b.x1 && a.y1 === b.y1 && a.x2 === b.x2 && a.y2 === b.y2;
}

function parseBBox(title: string): { x1: number; y1: number; x2: number; y2: number } | null {
  const m = title.match(/bbox\s+(\d+)\s+(\d+)\s+(\d+)\s+(\d+)/);
  if (!m) return null;
  return {
    x1: Number(m[1]),
    y1: Number(m[2]),
    x2: Number(m[3]),
    y2: Number(m[4])
  };
}

function fallbackWordBoxes(line: ParsedLine, words: string[]): ParsedWord[] {
  if (words.length === 0) return [];
  const out: ParsedWord[] = [];
  for (let i = 0; i < words.length; i += 1) {
    const word = words[i];
    out.push({
      id: `${line.id}_w_${i + 1}`,
      text: word,
      bbox: {
        x1: Math.round(line.bbox.x1),
        y1: Math.round(line.bbox.y1),
        x2: Math.round(line.bbox.x2),
        y2: Math.round(line.bbox.y2)
      }
    });
  }
  return out;
}

function buildCorrectedHOCR(originalHOCR: string, lines: ParsedLine[]): string {
  const doc = parseHOCRDocument(originalHOCR);
  const page = firstElementWithClass(doc, "ocr_page");
  if (!page) return originalHOCR;

  const existing = elementsWithClass(page, "ocr_line");
  for (const node of existing) {
    node.remove();
  }

  const ordered = [...lines].sort((a, b) => {
    if (a.bbox.y1 === b.bbox.y1) return a.bbox.x1 - b.bbox.x1;
    return a.bbox.y1 - b.bbox.y1;
  });

  for (const line of ordered) {
    const lineEl = doc.createElement("span");
    lineEl.setAttribute("class", "ocr_line");
    lineEl.setAttribute("id", line.id);
    lineEl.setAttribute("title", `bbox ${Math.round(line.bbox.x1)} ${Math.round(line.bbox.y1)} ${Math.round(line.bbox.x2)} ${Math.round(line.bbox.y2)}`);

    const wordEntries = line.words.length > 1
      ? line.words
      : fallbackWordBoxes(line, line.text.trim() === "" ? [] : line.text.trim().split(/\s+/)).map((w, i) => ({
          id: `${line.id}_w_${i + 1}`,
          text: w.text,
          bbox: w.bbox
        }));
    for (let i = 0; i < wordEntries.length; i += 1) {
      const wordEntry = wordEntries[i];
      if (wordEntry.text.trim() === "") continue;
      const wordEl = doc.createElement("span");
      wordEl.setAttribute("class", "ocrx_word");
      wordEl.setAttribute("id", wordEntry.id || `${line.id}_w_${i + 1}`);
      wordEl.setAttribute(
        "title",
        `bbox ${Math.round(wordEntry.bbox.x1)} ${Math.round(wordEntry.bbox.y1)} ${Math.round(wordEntry.bbox.x2)} ${Math.round(wordEntry.bbox.y2)}; x_wconf 95`
      );
      wordEl.textContent = wordEntry.text;
      lineEl.appendChild(wordEl);
      lineEl.appendChild(doc.createTextNode(" "));
    }

    page.appendChild(lineEl);
    page.appendChild(doc.createTextNode("\n"));
  }

  return new XMLSerializer().serializeToString(doc);
}

function hasClass(el: Element, className: string): boolean {
  const cls = (el.getAttribute("class") ?? "").trim();
  if (cls === "") return false;
  return cls.split(/\s+/).includes(className);
}

function parseHOCRDocument(hocrXML: string): Document {
  const parser = new DOMParser();
  const xmlDoc = parser.parseFromString(hocrXML, "application/xml");
  if (firstElementWithClass(xmlDoc, "ocr_page") || elementsWithClass(xmlDoc, "ocr_line").length > 0) {
    return xmlDoc;
  }
  return parser.parseFromString(hocrXML, "text/html");
}

function elementsWithClass(root: Document | Element, className: string): Element[] {
  const out: Element[] = [];
  const nodes = root.getElementsByTagName("*");
  for (const node of nodes) {
    if (hasClass(node, className)) out.push(node);
  }
  return out;
}

function firstElementWithClass(root: Document | Element, className: string): Element | null {
  const nodes = elementsWithClass(root, className);
  return nodes.length > 0 ? nodes[0] : null;
}
