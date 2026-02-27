import "./styles.css";

type OCRRun = {
  session_id: string;
  image_url: string;
  model: string;
  original_hocr: string;
  original_text: string;
  corrected_hocr?: string;
  corrected_text?: string;
  edit_count: number;
  levenshtein_distance: number;
};

type ParsedLine = {
  id: string;
  text: string;
  originalText: string;
  bbox: { x1: number; y1: number; x2: number; y2: number };
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
          Output format
          <select id="output-format" class="rounded border border-slate-600 bg-slate-950 px-3 py-2">
            <option value="hocr">hOCR</option>
            <option value="text">Plain text</option>
          </select>
        </label>
        <label class="flex flex-col gap-2 text-sm text-slate-300">
          Model (optional)
          <input id="model-name" type="text" class="rounded border border-slate-600 bg-slate-950 px-3 py-2" placeholder="gpt-4o, mistral-small3.2:24b, ..." />
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
  const outputFormatInput = document.getElementById("output-format") as HTMLSelectElement;
  const modelNameInput = document.getElementById("model-name") as HTMLInputElement;
  const resultOutput = document.getElementById("result-output") as HTMLPreElement;
  const sessionMeta = document.getElementById("session-meta") as HTMLSpanElement;
  const resultImage = document.getElementById("result-image") as HTMLImageElement;
  const openEditor = document.getElementById("open-editor") as HTMLAnchorElement;

  function getAcceptHeader(format: string): string {
    return format === "text" ? "text/plain" : "text/html";
  }

  async function showResponse(response: Response): Promise<void> {
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
  }

  urlForm.addEventListener("submit", async (event) => {
    event.preventDefault();
    const imageURL = imageURLInput.value.trim();
    if (!imageURL) return;

    const format = outputFormatInput.value;
    const model = modelNameInput.value.trim();
    const response = await fetch(`/v1/process/url?format=${encodeURIComponent(format)}`, {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        "Accept": getAcceptHeader(format)
      },
      body: JSON.stringify({ image_url: imageURL, model })
    });

    await showResponse(response);
  });

  uploadForm.addEventListener("submit", async (event) => {
    event.preventDefault();
    const file = fileInput.files?.[0];
    if (!file) return;

    const format = outputFormatInput.value;
    const model = modelNameInput.value.trim();
    const formData = new FormData();
    formData.append("file", file);
    if (model !== "") {
      formData.append("model", model);
    }

    const response = await fetch(`/v1/process/upload?format=${encodeURIComponent(format)}`, {
      method: "POST",
      headers: { "Accept": getAcceptHeader(format) },
      body: formData
    });

    await showResponse(response);
  });

  hocrForm.addEventListener("submit", async (event) => {
    event.preventDefault();
    const hocr = hocrInput.value.trim();
    if (!hocr) return;

    const format = outputFormatInput.value;
    const model = modelNameInput.value.trim();
    const imageURL = hocrImageURLInput.value.trim();
    const imageFile = hocrImageFileInput.files?.[0];
    const formData = new FormData();
    formData.append("hocr", hocr);
    formData.append("model", model);
    formData.append("image_url", imageURL);
    if (imageFile) {
      formData.append("file", imageFile);
    }

    const response = await fetch(`/v1/process/hocr?format=${encodeURIComponent(format)}`, {
      method: "POST",
      headers: {
        "Accept": getAcceptHeader(format)
      },
      body: formData
    });

    await showResponse(response);
  });
}

async function renderEditor(): Promise<void> {
  const params = new URLSearchParams(window.location.search);
  const sessionID = params.get("session") ?? "";

  app!.innerHTML = `
    <main class="mx-auto max-w-7xl p-6">
      <header class="mb-4 flex items-center justify-between">
        <div>
          <h1 class="text-3xl font-bold">hOCR Line Editor</h1>
          <p id="editor-meta" class="mt-1 text-sm text-slate-300"></p>
        </div>
        <a href="/" class="rounded border border-slate-600 px-3 py-2 text-sm hover:bg-slate-800">Back</a>
      </header>

      <div class="grid h-[72vh] gap-4 md:grid-cols-2">
        <section class="relative overflow-auto rounded-xl border border-slate-700 bg-slate-900/60 p-3">
          <div id="image-wrap" class="relative inline-block">
            <img id="editor-image" class="max-h-[66vh] rounded" alt="source" />
            <div id="line-overlay" class="pointer-events-none absolute inset-0"></div>
          </div>
        </section>

        <section class="overflow-auto rounded-xl border border-slate-700 bg-slate-900/60 p-4">
          <div class="mb-3 flex items-center justify-between">
            <h2 class="text-lg font-semibold">Transcript Lines</h2>
            <button id="save-edits" class="rounded bg-brand-500 px-4 py-2 text-sm font-medium hover:bg-brand-600">Save Edits</button>
          </div>
          <p id="save-status" class="mb-2 text-xs text-slate-400"></p>
          <div id="line-list" class="space-y-2"></div>
        </section>
      </div>
    </main>
  `;

  if (!sessionID) {
    const meta = document.getElementById("editor-meta") as HTMLParagraphElement;
    meta.textContent = "Missing session query parameter";
    return;
  }

  const meta = document.getElementById("editor-meta") as HTMLParagraphElement;
  const image = document.getElementById("editor-image") as HTMLImageElement;
  const lineOverlay = document.getElementById("line-overlay") as HTMLDivElement;
  const lineList = document.getElementById("line-list") as HTMLDivElement;
  const saveBtn = document.getElementById("save-edits") as HTMLButtonElement;
  const saveStatus = document.getElementById("save-status") as HTMLParagraphElement;

  const runResp = await fetch(`/v1/ocr/runs/${encodeURIComponent(sessionID)}`);
  if (!runResp.ok) {
    meta.textContent = `Failed to load session ${sessionID}`;
    return;
  }

  const run = await runResp.json() as OCRRun;
  const workingHOCR = run.corrected_hocr && run.corrected_hocr.trim() !== "" ? run.corrected_hocr : run.original_hocr;

  meta.textContent = `session ${run.session_id} | model ${run.model} | existing edits ${run.edit_count}`;
  image.src = run.image_url;
  if (!run.image_url || run.image_url.trim() === "") {
    image.classList.add("hidden");
  }

  const parsed = parseHOCR(workingHOCR);
  const lines = parsed.lines;

  let activeLineID = "";
  const changedLineIDs = new Set<string>();

  const overlayByID = new Map<string, HTMLDivElement>();
  const inputByID = new Map<string, HTMLTextAreaElement>();

  const pageWidth = parsed.pageWidth || 1;
  const pageHeight = parsed.pageHeight || 1;

  for (const line of lines) {
    const item = document.createElement("div");
    item.className = "rounded border border-slate-700 bg-slate-950 p-2";

    const label = document.createElement("div");
    label.className = "mb-1 text-xs text-slate-400";
    label.textContent = line.id;

    const textarea = document.createElement("textarea");
    textarea.className = "w-full rounded border border-slate-600 bg-slate-900 px-2 py-1 text-sm";
    textarea.value = line.text;
    textarea.rows = 2;

    textarea.addEventListener("focus", () => setActiveLine(line.id));
    textarea.addEventListener("input", () => {
      line.text = textarea.value;
      if (line.text.trim() !== line.originalText.trim()) {
        changedLineIDs.add(line.id);
      } else {
        changedLineIDs.delete(line.id);
      }
    });

    item.append(label, textarea);
    lineList.appendChild(item);

    inputByID.set(line.id, textarea);

    const marker = document.createElement("button");
    marker.type = "button";
    marker.className = "absolute border border-amber-400/70 bg-amber-300/20";
    marker.style.left = `${(line.bbox.x1 / pageWidth) * 100}%`;
    marker.style.top = `${(line.bbox.y1 / pageHeight) * 100}%`;
    marker.style.width = `${Math.max(0.5, ((line.bbox.x2 - line.bbox.x1) / pageWidth) * 100)}%`;
    marker.style.height = `${Math.max(1.2, ((line.bbox.y2 - line.bbox.y1) / pageHeight) * 100)}%`;
    marker.style.pointerEvents = "auto";
    marker.addEventListener("click", () => {
      setActiveLine(line.id);
      inputByID.get(line.id)?.focus();
    });

    lineOverlay.appendChild(marker);
    overlayByID.set(line.id, marker);
  }

  function setActiveLine(lineID: string): void {
    activeLineID = lineID;
    for (const [id, marker] of overlayByID.entries()) {
      if (id === activeLineID) {
        marker.className = "absolute border border-cyan-300 bg-cyan-300/30";
      } else {
        marker.className = "absolute border border-amber-400/70 bg-amber-300/20";
      }
    }
  }

  saveBtn.addEventListener("click", async () => {
    const correctedHOCR = buildCorrectedHOCR(workingHOCR, lines);

    const response = await fetch(`/v1/ocr/runs/${encodeURIComponent(sessionID)}/edits`, {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        corrected_hocr: correctedHOCR,
        edit_count: changedLineIDs.size
      })
    });

    if (!response.ok) {
      saveStatus.textContent = "Failed to save edits";
      return;
    }

    const payload = await response.json() as {
      edit_count: number;
      levenshtein_distance: number;
    };

    saveStatus.textContent = `Saved. line edits=${payload.edit_count}, levenshtein=${payload.levenshtein_distance}`;
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
    const words = Array.from(node.querySelectorAll(".ocrx_word"))
      .map((w) => (w.textContent ?? "").trim())
      .filter((w) => w !== "");
    const text = words.join(" ");

    return { id, bbox, text, originalText: text };
  });

  return {
    lines,
    pageWidth: Math.max(1, pageBBox.x2 - pageBBox.x1),
    pageHeight: Math.max(1, pageBBox.y2 - pageBBox.y1)
  };
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

function buildCorrectedHOCR(originalHOCR: string, lines: ParsedLine[]): string {
  const parser = new DOMParser();
  const doc = parser.parseFromString(originalHOCR, "application/xml");

  const lineMap = new Map(lines.map((l) => [l.id, l]));
  const lineNodes = Array.from(doc.querySelectorAll(".ocr_line"));

  for (const lineNode of lineNodes) {
    const id = lineNode.getAttribute("id") ?? "";
    const line = lineMap.get(id);
    if (!line) continue;

    const bbox = parseBBox(lineNode.getAttribute("title") ?? "") ?? line.bbox;
    const words = line.text.trim() === "" ? [] : line.text.trim().split(/\s+/);

    while (lineNode.firstChild) {
      lineNode.removeChild(lineNode.firstChild);
    }

    for (let i = 0; i < words.length; i += 1) {
      const wordEl = doc.createElement("span");
      wordEl.setAttribute("class", "ocrx_word");
      wordEl.setAttribute("id", `${id}_w_${i + 1}`);
      wordEl.setAttribute("title", `bbox ${bbox.x1} ${bbox.y1} ${bbox.x2} ${bbox.y2}; x_wconf 95`);
      wordEl.textContent = words[i];
      lineNode.appendChild(wordEl);
      lineNode.appendChild(doc.createTextNode(" "));
    }
  }

  return new XMLSerializer().serializeToString(doc);
}
