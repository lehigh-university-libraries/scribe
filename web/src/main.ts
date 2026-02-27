import "./styles.css";

const app = document.getElementById("app");
if (!app) {
  throw new Error("missing #app element");
}

app.innerHTML = `
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
      <img id="result-image" class="mb-3 hidden max-h-96 rounded border border-slate-700" alt="Processed source" />
      <pre id="result-output" class="max-h-[28rem] overflow-auto whitespace-pre-wrap rounded border border-slate-700 bg-slate-950 p-4 text-sm text-slate-200"></pre>
    </section>
  </main>
`;

const urlForm = document.getElementById("url-form") as HTMLFormElement;
const uploadForm = document.getElementById("upload-form") as HTMLFormElement;
const imageURLInput = document.getElementById("image-url") as HTMLInputElement;
const fileInput = document.getElementById("image-file") as HTMLInputElement;
const outputFormatInput = document.getElementById("output-format") as HTMLSelectElement;
const modelNameInput = document.getElementById("model-name") as HTMLInputElement;
const resultOutput = document.getElementById("result-output") as HTMLPreElement;
const sessionMeta = document.getElementById("session-meta") as HTMLSpanElement;
const resultImage = document.getElementById("result-image") as HTMLImageElement;

function getAcceptHeader(format: string): string {
  return format === "text" ? "text/plain" : "text/html";
}

async function showResponse(response: Response): Promise<void> {
  const body = await response.text();
  const sessionID = response.headers.get("X-Session-ID") ?? "";
  const imageURL = response.headers.get("X-Image-URL") ?? "";
  if (sessionID) {
    sessionMeta.textContent = `session ${sessionID}`;
  } else {
    sessionMeta.textContent = "";
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
  if (!imageURL) {
    return;
  }
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
  if (!file) {
    return;
  }
  const format = outputFormatInput.value;
  const model = modelNameInput.value.trim();
  const formData = new FormData();
  formData.append("file", file);
  if (model !== "") {
    formData.append("model", model);
  }

  const response = await fetch(`/v1/process/upload?format=${encodeURIComponent(format)}`, {
    method: "POST",
    headers: {
      "Accept": getAcceptHeader(format)
    },
    body: formData
  });
  await showResponse(response);
});
