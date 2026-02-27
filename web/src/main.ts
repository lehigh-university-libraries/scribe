import "./styles.css";

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
};

const app = document.getElementById("app");
if (!app) {
  throw new Error("missing #app element");
}

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
  let progressTimer: number | null = null;

  function getAcceptHeader(format: string): string {
    return format === "text" ? "text/plain" : "text/html";
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

  async function showResponse(response: Response): Promise<string> {
    const body = await response.text();
    const sessionID = response.headers.get("X-Session-ID") ?? "";
    const imageURL = response.headers.get("X-Image-URL") ?? "";

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

  function newProgressID(): string {
    return `p_${Date.now()}_${Math.random().toString(36).slice(2, 10)}`;
  }

  function stopProgressPolling(): void {
    if (progressTimer !== null) {
      window.clearInterval(progressTimer);
      progressTimer = null;
    }
  }

  function startProgressPolling(progressID: string): void {
    stopProgressPolling();
    sessionMeta.textContent = "processing...";
    const poll = async () => {
      try {
        const resp = await fetch(`/v1/progress/${encodeURIComponent(progressID)}`);
        if (!resp.ok) return;
        const state = await resp.json() as ProgressState;
        const status = (state.status || "processing").trim();
        const message = (state.message || "").trim();
        const suffix = message ? `: ${message}` : "";
        sessionMeta.textContent = `${status}${suffix}`;
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
      const response = await fetch(`/v1/process/url?format=${encodeURIComponent(format)}`, {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          "Accept": getAcceptHeader(format),
          "X-Progress-ID": progressID
        },
        body: JSON.stringify({ image_url: imageURL, provider, model })
      });

      await showResponse(response);
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
    const formData = new FormData();
    formData.append("file", file);
    formData.append("provider", provider);
    if (model !== "") {
      formData.append("model", model);
    }

    try {
      const response = await fetch(`/v1/process/upload?format=${encodeURIComponent(format)}`, {
        method: "POST",
        headers: {
          "Accept": getAcceptHeader(format),
          "X-Progress-ID": progressID
        },
        body: formData
      });

      const sessionID = await showResponse(response);
      if (response.ok && sessionID) {
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
    const formData = new FormData();
    formData.append("hocr", hocr);
    formData.append("provider", provider);
    formData.append("model", model);
    formData.append("image_url", imageURL);
    if (imageFile) {
      formData.append("file", imageFile);
    }

    try {
      const response = await fetch(`/v1/process/hocr?format=${encodeURIComponent(format)}`, {
        method: "POST",
        headers: {
          "Accept": getAcceptHeader(format),
          "X-Progress-ID": progressID
        },
        body: formData
      });

      await showResponse(response);
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

      <div class="grid h-[78vh] gap-3 md:grid-cols-2">
        <section class="relative overflow-auto rounded-xl border border-slate-700 bg-slate-900/60 p-2">
          <div id="image-wrap" class="relative inline-block">
            <img id="editor-image" class="max-h-[72vh] rounded" alt="source" />
            <div id="line-overlay" class="absolute inset-0"></div>
          </div>
        </section>
        <section class="relative overflow-auto rounded-xl border border-slate-700 bg-slate-900/60 p-2">
          <div id="line-list" class="relative h-[72vh]"></div>
          <div id="line-info" class="absolute bottom-2 right-2 rounded border border-slate-700 bg-slate-950/90 px-2 py-1 text-[11px] text-slate-300"></div>
          <p id="save-status" class="absolute bottom-2 left-2 text-[11px] text-slate-400"></p>
        </section>
      </div>

      <div class="mt-3 grid gap-3 md:grid-cols-2">
        <div></div>
        <section class="rounded-xl border border-slate-700 bg-slate-900/60 p-2">
          <div class="mb-2 flex flex-wrap gap-2">
            <button id="add-box" class="rounded border border-slate-600 px-2 py-1 text-xs hover:bg-slate-800">Add</button>
            <button id="split-line" class="rounded border border-slate-600 px-2 py-1 text-xs hover:bg-slate-800">Split Line</button>
            <button id="explode-line" class="rounded border border-slate-600 px-2 py-1 text-xs hover:bg-slate-800">Explode Words</button>
            <button id="delete-box" class="rounded border border-rose-600 px-2 py-1 text-xs text-rose-300 hover:bg-rose-950">Delete</button>
            <button id="save-edits" class="rounded bg-brand-500 px-3 py-1 text-xs font-medium hover:bg-brand-600">Save</button>
          </div>
          <div class="rounded border border-slate-700 bg-slate-900/40 px-2 py-1 text-[11px] text-slate-300">
            <strong>Keyboard Shortcuts:</strong><br>
            • <kbd>Tab</kbd> - Next line<br>
            • <kbd>Shift+Tab</kbd> - Previous line<br>
            • <kbd>Enter</kbd> - Apply changes<br>
            • <kbd>Delete</kbd> - Delete selected line<br>
            • <kbd>D</kbd> - Toggle drawing mode<br>
            • <kbd>S</kbd> - Split line<br>
            • <kbd>E</kbd> - Explode words<br>
            • <kbd>Esc</kbd> - Clear selection/Exit drawing
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
  const lineOverlay = document.getElementById("line-overlay") as HTMLDivElement;
  const lineList = document.getElementById("line-list") as HTMLDivElement;
  const lineInfo = document.getElementById("line-info") as HTMLDivElement;
  const saveStatus = document.getElementById("save-status") as HTMLParagraphElement;
  const saveBtn = document.getElementById("save-edits") as HTMLButtonElement;
  const addBoxBtn = document.getElementById("add-box") as HTMLButtonElement;
  const splitLineBtn = document.getElementById("split-line") as HTMLButtonElement;
  const explodeLineBtn = document.getElementById("explode-line") as HTMLButtonElement;
  const deleteBoxBtn = document.getElementById("delete-box") as HTMLButtonElement;

  const runResp = await fetch(`/v1/ocr/runs/${encodeURIComponent(sessionID)}`);
  if (!runResp.ok) {
    meta.textContent = `Failed to load session ${sessionID}`;
    return;
  }

  const run = await runResp.json() as OCRRun;
  const workingHOCR = run.corrected_hocr && run.corrected_hocr.trim() !== "" ? run.corrected_hocr : run.original_hocr;
  const providerLabel = run.provider && run.provider.trim() !== "" ? run.provider : "unknown";
  meta.textContent = `session ${run.session_id} | provider ${providerLabel} | model ${run.model} | edits ${run.edit_count}`;

  image.src = run.image_url;
  if (!run.image_url || run.image_url.trim() === "") image.classList.add("hidden");

  const parsed = parseHOCR(workingHOCR);
  const lines = parsed.lines;
  let activeLineID = lines.length > 0 ? lines[0].id : "";
  const changedLineIDs = new Set<string>();
  const changedBoxIDs = new Set<string>();
  let nextLineCounter = lines.length + 1;
  let isAddBoxMode = false;

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

  function syncEditorHeights(): void {
    const minHeight = 280;
    const imageHeight = Math.round(image.clientHeight || 0);
    const target = Math.max(minHeight, imageHeight);
    lineList.style.height = `${target}px`;
  }

  function getLineByID(id: string): ParsedLine | undefined {
    return lines.find((line) => line.id === id);
  }

  function markBoxChange(line: ParsedLine): void {
    if (!line.originalBBox || !sameBBox(line.bbox, line.originalBBox)) changedBoxIDs.add(line.id);
    else changedBoxIDs.delete(line.id);
  }

  function setActiveLine(id: string): void {
    activeLineID = id;
    renderEditorState();
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

  function deleteActiveLine(): void {
    if (!activeLineID) return;
    const idx = lines.findIndex((line) => line.id === activeLineID);
    if (idx < 0) return;
    lines.splice(idx, 1);
    changedLineIDs.add(activeLineID);
    changedBoxIDs.add(activeLineID);
    activeLineID = lines.length > 0 ? orderedLines()[0].id : "";
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
    const words = wordsFromText(line.text);
    if (line.words.length <= 1 && words.length <= 1) {
      line.words = words.length === 1 ? [{ id: `${line.id}_w_1`, text: words[0], bbox: { ...line.bbox } }] : [];
      return;
    }
    line.words = distributeWordsInLine(line, words);
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
    const sorted = [...lines].sort((a, b) => (a.bbox.y1 === b.bbox.y1 ? a.bbox.x1 - b.bbox.x1 : a.bbox.y1 - b.bbox.y1));

    for (const line of sorted) {
      if (!image.classList.contains("hidden")) {
        const marker = document.createElement("div");
        marker.className = `absolute border ${line.id === activeLineID ? "border-cyan-300 bg-cyan-300/20" : "border-amber-400/70 bg-amber-300/20"}`;
        marker.style.left = `${(line.bbox.x1 / pageWidth) * 100}%`;
        marker.style.top = `${(line.bbox.y1 / pageHeight) * 100}%`;
        marker.style.width = `${Math.max(0.5, ((line.bbox.x2 - line.bbox.x1) / pageWidth) * 100)}%`;
        marker.style.height = `${Math.max(1.2, ((line.bbox.y2 - line.bbox.y1) / pageHeight) * 100)}%`;
        marker.style.cursor = "move";

        marker.addEventListener("mousedown", (e) => {
          e.preventDefault();
          const doc = pointerToDoc(e.clientX, e.clientY);
          if (!doc) return;
          setActiveLine(line.id);
          interaction = { lineID: line.id, mode: "move", handle: "", startDocX: doc.x, startDocY: doc.y, startBox: { ...line.bbox } };
        });
        marker.addEventListener("click", () => setActiveLine(line.id));

        if (line.id === activeLineID) {
          for (const handle of ["n", "s", "e", "w", "nw", "ne", "sw", "se"]) {
            const h = document.createElement("div");
            h.dataset.handle = handle;
            h.className = "absolute h-2 w-2 bg-cyan-200";
            h.style.pointerEvents = "auto";
            h.style.cursor = `${handle}-resize`;
            positionHandle(h, handle);
            h.addEventListener("mousedown", (e) => {
              e.stopPropagation();
              e.preventDefault();
              const doc = pointerToDoc(e.clientX, e.clientY);
              if (!doc) return;
              interaction = { lineID: line.id, mode: "resize", handle, startDocX: doc.x, startDocY: doc.y, startBox: { ...line.bbox } };
            });
            marker.appendChild(h);
          }
        }
        lineOverlay.appendChild(marker);

        if (line.id === activeLineID && line.words.length > 1) {
          for (const word of line.words) {
            const wordBox = document.createElement("div");
            wordBox.className = "absolute border border-emerald-300/90 bg-emerald-300/10";
            wordBox.style.left = `${(word.bbox.x1 / pageWidth) * 100}%`;
            wordBox.style.top = `${(word.bbox.y1 / pageHeight) * 100}%`;
            wordBox.style.width = `${Math.max(0.3, ((word.bbox.x2 - word.bbox.x1) / pageWidth) * 100)}%`;
            wordBox.style.height = `${Math.max(1, ((word.bbox.y2 - word.bbox.y1) / pageHeight) * 100)}%`;
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

      const input = document.createElement("input");
      input.type = "text";
      input.className = `w-full rounded border px-2 py-1 text-sm leading-tight ${line.id === activeLineID ? "border-cyan-400 bg-slate-900" : "border-slate-700 bg-slate-950"}`;
      input.value = line.text;
      input.addEventListener("focus", () => setActiveLine(line.id));
      input.addEventListener("input", () => {
        line.text = input.value;
        refreshWordBoxesForLine(line);
        if (line.text.trim() !== line.originalText.trim()) changedLineIDs.add(line.id);
        else changedLineIDs.delete(line.id);
      });
      row.appendChild(input);
      lineList.appendChild(row);
    }

    const active = getLineByID(activeLineID);
    if (active) {
      lineInfo.textContent = `${active.id} | (${active.bbox.x1},${active.bbox.y1})-(${active.bbox.x2},${active.bbox.y2})`;
    } else {
      lineInfo.textContent = "";
    }
  }

  window.addEventListener("mousemove", (e) => {
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
      renderEditorState();
      return;
    }
    if (interaction.mode === "resize") {
      line.bbox = applyResize(interaction.startBox, interaction.handle, dx, dy);
      refreshWordBoxesForLine(line);
      markBoxChange(line);
      renderEditorState();
      return;
    }
    if (interaction.mode === "draw") {
      line.bbox = applyResize(interaction.startBox, "se", dx, dy);
      refreshWordBoxesForLine(line);
      markBoxChange(line);
      renderEditorState();
    }
  });

  window.addEventListener("mouseup", () => { interaction = null; });

  addBoxBtn.addEventListener("click", () => {
    setAddMode(!isAddBoxMode);
  });

  lineOverlay.addEventListener("mousedown", (e) => {
    if (!isAddBoxMode || e.target !== lineOverlay) return;
    const doc = pointerToDoc(e.clientX, e.clientY);
    if (!doc) return;
    const id = `line_new_${nextLineCounter++}`;
    const newLine: ParsedLine = {
      id,
      text: "",
      originalText: "",
      bbox: { x1: roundInt(doc.x), y1: roundInt(doc.y), x2: roundInt(doc.x + 6), y2: roundInt(doc.y + 6) },
      originalBBox: null,
      words: []
    };
    lines.push(newLine);
    changedLineIDs.add(id);
    changedBoxIDs.add(id);
    setActiveLine(id);
    interaction = { lineID: id, mode: "draw", handle: "se", startDocX: doc.x, startDocY: doc.y, startBox: { ...newLine.bbox } };
    renderEditorState();
  });

  splitLineBtn.addEventListener("click", () => {
    const line = getLineByID(activeLineID);
    if (!line) return;
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
    line.words = distributeWordsInLine(line, wordsFromText(topText));
    markBoxChange(line);
    changedLineIDs.add(line.id);

    const id = `line_new_${nextLineCounter++}`;
    const bottom: ParsedLine = {
      id,
      text: bottomText,
      originalText: "",
      bbox: clampBox({ x1: original.x1, y1: splitY, x2: original.x2, y2: original.y2 }),
      originalBBox: null,
      words: []
    };
    bottom.words = distributeWordsInLine(bottom, wordsFromText(bottomText));
    lines.push(bottom);
    changedLineIDs.add(id);
    changedBoxIDs.add(id);
    setActiveLine(line.id);
    renderEditorState();
  });

  explodeLineBtn.addEventListener("click", () => {
    const line = getLineByID(activeLineID);
    if (!line) return;
    const words = line.text.trim().split(/\s+/).filter(Boolean);
    if (words.length <= 1) return;
    line.words = distributeWordsInLine(line, words);
    changedLineIDs.add(line.id);
    changedBoxIDs.add(line.id);
    setActiveLine(line.id);
    renderEditorState();
  });

  deleteBoxBtn.addEventListener("click", () => {
    deleteActiveLine();
  });

  renderEditorState();
  image.addEventListener("load", () => {
    syncEditorHeights();
    renderEditorState();
  });
  window.addEventListener("resize", renderEditorState);
  window.addEventListener("keydown", (event) => {
    const target = event.target as HTMLElement | null;
    const inTextField = !!target && (target.tagName === "INPUT" || target.tagName === "TEXTAREA" || target.isContentEditable);

    if (event.key === "Tab") {
      event.preventDefault();
      const sorted = orderedLines();
      if (sorted.length === 0) return;
      const idx = sorted.findIndex((line) => line.id === activeLineID);
      if (idx < 0) {
        setActiveLine(sorted[0].id);
        return;
      }
      const next = event.shiftKey
        ? sorted[(idx - 1 + sorted.length) % sorted.length]
        : sorted[(idx + 1) % sorted.length];
      setActiveLine(next.id);
      return;
    }

    if (event.key === "Enter") {
      event.preventDefault();
      saveBtn.click();
      return;
    }

    if (event.key === "Delete" && !inTextField) {
      event.preventDefault();
      deleteActiveLine();
      return;
    }

    if ((event.key === "d" || event.key === "D") && !inTextField) {
      event.preventDefault();
      setAddMode(!isAddBoxMode);
      return;
    }

    if ((event.key === "s" || event.key === "S") && !inTextField) {
      event.preventDefault();
      splitLineBtn.click();
      return;
    }

    if ((event.key === "e" || event.key === "E") && !inTextField) {
      event.preventDefault();
      explodeLineBtn.click();
      return;
    }

    if (event.key === "Escape") {
      event.preventDefault();
      activeLineID = "";
      setAddMode(false);
      renderEditorState();
    }
  });

  saveBtn.addEventListener("click", async () => {
    const correctedHOCR = buildCorrectedHOCR(workingHOCR, lines);
    const response = await fetch(`/v1/ocr/runs/${encodeURIComponent(sessionID)}/edits`, {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ corrected_hocr: correctedHOCR, edit_count: changedLineIDs.size })
    });
    if (!response.ok) {
      saveStatus.textContent = "Failed to save edits";
      return;
    }

    const payload = await response.json() as {
      edit_count: number;
      levenshtein_distance: number;
      box_edit_count: number;
      boxes_added: number;
      boxes_deleted: number;
      box_change_score: number;
    };
    saveStatus.textContent = `saved text=${payload.edit_count} box=${payload.box_edit_count} +${payload.boxes_added}/-${payload.boxes_deleted} severity=${payload.box_change_score.toFixed(3)} lev=${payload.levenshtein_distance}`;
  });
}

function parseHOCR(hocrXML: string): { lines: ParsedLine[]; pageWidth: number; pageHeight: number } {
  const parser = new DOMParser();
  const doc = parser.parseFromString(hocrXML, "application/xml");

  const page = doc.querySelector(".ocr_page");
  const pageBBox = parseBBox(page?.getAttribute("title") ?? "") ?? { x1: 0, y1: 0, x2: 1, y2: 1 };

  const lineNodes = Array.from(doc.querySelectorAll(".ocr_line"));
  const lines: ParsedLine[] = lineNodes.map((node, idx) => {
    const id = node.getAttribute("id") ?? `line_${idx + 1}`;
    const bbox = parseBBox(node.getAttribute("title") ?? "") ?? { x1: 0, y1: 0, x2: 1, y2: 1 };
    const words: ParsedWord[] = Array.from(node.querySelectorAll(".ocrx_word"))
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

    return { id, bbox, text, originalText: text, originalBBox: { ...bbox }, words };
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
      bbox: {
        x1: Math.round(x),
        y1: Math.round(line.bbox.y1),
        x2: Math.round(nextX),
        y2: Math.round(line.bbox.y2)
      }
    });
    x = nextX;
  }
  return out;
}

function buildCorrectedHOCR(originalHOCR: string, lines: ParsedLine[]): string {
  const parser = new DOMParser();
  const doc = parser.parseFromString(originalHOCR, "application/xml");
  const page = doc.querySelector(".ocr_page");
  if (!page) return originalHOCR;

  const existing = Array.from(page.querySelectorAll(".ocr_line"));
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

    const textWords = line.text.trim() === "" ? [] : line.text.trim().split(/\s+/);
    const availableWordBoxes = line.words.length === textWords.length
      ? line.words
      : fallbackWordBoxes(line, textWords);
    for (let i = 0; i < textWords.length; i += 1) {
      const word = textWords[i];
      const wordBox = availableWordBoxes[i] ?? {
        id: `${line.id}_w_${i + 1}`,
        text: word,
        bbox: { ...line.bbox }
      };
      const wordEl = doc.createElement("span");
      wordEl.setAttribute("class", "ocrx_word");
      wordEl.setAttribute("id", wordBox.id || `${line.id}_w_${i + 1}`);
      wordEl.setAttribute(
        "title",
        `bbox ${Math.round(wordBox.bbox.x1)} ${Math.round(wordBox.bbox.y1)} ${Math.round(wordBox.bbox.x2)} ${Math.round(wordBox.bbox.y2)}; x_wconf 95`
      );
      wordEl.textContent = word;
      lineEl.appendChild(wordEl);
      lineEl.appendChild(doc.createTextNode(" "));
    }

    page.appendChild(lineEl);
    page.appendChild(doc.createTextNode("\n"));
  }

  return new XMLSerializer().serializeToString(doc);
}
