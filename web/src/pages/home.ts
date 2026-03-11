import { listItems, createItemFromManifest, uploadItemImages, deleteItem } from "../api/items";
import { processImageURL, processImageUpload } from "../api/processing";
import { listTranscriptionJobs } from "../api/transcription";
import { subscribeToEvents } from "../api/events";
import { TranscriptionJobStatus } from "../proto/scribe/v1/transcription_pb";
import { uint64ToString, escHtml } from "../lib/util";
import type { Item } from "../proto/scribe/v1/item_pb";

function isPendingStatus(status: TranscriptionJobStatus | string | number): boolean {
  return status === TranscriptionJobStatus.PENDING
    || status === "TRANSCRIPTION_JOB_STATUS_PENDING"
    || status === "pending";
}

function isRunningStatus(status: TranscriptionJobStatus | string | number): boolean {
  return status === TranscriptionJobStatus.RUNNING
    || status === "TRANSCRIPTION_JOB_STATUS_RUNNING"
    || status === "running";
}

function isCompletedStatus(status: TranscriptionJobStatus | string | number): boolean {
  return status === TranscriptionJobStatus.COMPLETED
    || status === "TRANSCRIPTION_JOB_STATUS_COMPLETED"
    || status === "completed";
}

interface ProcessingOverlay {
  el: HTMLDivElement;
  advance: (step: number) => void;
  complete: (detail?: string) => void;
  setDetail: (detail?: string) => void;
  remove: () => void;
}

function createProcessingOverlay(steps: string[]): ProcessingOverlay {
  const el = document.createElement("div");
  el.className = "fixed inset-0 z-[9999] flex flex-col items-center justify-center bg-slate-950";
  el.innerHTML = `
    <div class="w-full max-w-sm space-y-10 px-8 text-center">
      <div>
        <p class="text-3xl font-bold tracking-tight">Scribe</p>
        <p class="mt-1 text-sm text-slate-400">Processing your image</p>
      </div>
      <div class="flex justify-center">
        <div id="proc-spinner" class="h-12 w-12 animate-spin rounded-full border-4 border-slate-800 border-t-brand-500"></div>
      </div>
      <ul id="proc-steps" class="space-y-3 text-left text-sm">
        ${steps.map((label, i) => `
          <li id="proc-step-${i}" class="flex items-center gap-3 text-slate-600 transition-colors duration-300">
            <span id="proc-icon-${i}" class="flex h-5 w-5 flex-shrink-0 items-center justify-center text-xs">·</span>
            <span>${label}</span>
          </li>`).join("")}
      </ul>
      <p id="proc-detail" class="text-xs text-slate-500"></p>
    </div>
  `;
  document.body.appendChild(el);

  let activeStep = -1;

  function advance(step: number) {
    if (step <= activeStep) return;
    for (let i = Math.max(0, activeStep); i <= step; i++) {
      const li = document.getElementById(`proc-step-${i}`);
      const icon = document.getElementById(`proc-icon-${i}`);
      if (!li || !icon) continue;
      if (i < step) {
        li.className = "flex items-center gap-3 text-slate-300 transition-colors duration-300";
        icon.innerHTML = `<svg class="h-4 w-4 text-brand-500" viewBox="0 0 20 20" fill="currentColor"><path fill-rule="evenodd" d="M16.707 5.293a1 1 0 010 1.414l-8 8a1 1 0 01-1.414 0l-4-4a1 1 0 011.414-1.414L8 12.586l7.293-7.293a1 1 0 011.414 0z" clip-rule="evenodd"/></svg>`;
      } else {
        li.className = "flex items-center gap-3 text-white transition-colors duration-300";
        icon.innerHTML = `<span class="inline-block h-3 w-3 animate-spin rounded-full border-2 border-slate-700 border-t-brand-500"></span>`;
      }
    }
    activeStep = step;
  }

  function complete(detail = "Opening editor…") {
    for (let i = 0; i < steps.length; i++) {
      const li = document.getElementById(`proc-step-${i}`);
      const icon = document.getElementById(`proc-icon-${i}`);
      if (!li || !icon) continue;
      li.className = "flex items-center gap-3 text-slate-300 transition-colors duration-300";
      icon.innerHTML = `<svg class="h-4 w-4 text-brand-500" viewBox="0 0 20 20" fill="currentColor"><path fill-rule="evenodd" d="M16.707 5.293a1 1 0 010 1.414l-8 8a1 1 0 01-1.414 0l-4-4a1 1 0 011.414-1.414L8 12.586l7.293-7.293a1 1 0 011.414 0z" clip-rule="evenodd"/></svg>`;
    }
    const spinner = document.getElementById("proc-spinner");
    if (spinner) spinner.remove();
    const detailEl = document.getElementById("proc-detail");
    if (detailEl) detailEl.textContent = detail;
  }

  function setDetail(detail = "") {
    const detailEl = document.getElementById("proc-detail");
    if (detailEl) detailEl.textContent = detail;
  }

  function remove() {
    el.remove();
  }

  advance(0);
  return { el, advance, complete, setDetail, remove };
}

async function withProcessingOverlay<T>(
  steps: string[],
  timings: number[],
  fn: () => Promise<T>,
): Promise<T> {
  const overlay = createProcessingOverlay(steps);
  const timers = timings.map((ms, i) => setTimeout(() => overlay.advance(i + 1), ms));
  try {
    const result = await fn();
    timers.forEach(clearTimeout);
    // Redirect immediately — page navigation clears the overlay.
    return result;
  } catch (err) {
    timers.forEach(clearTimeout);
    overlay.remove();
    throw err;
  }
}

async function waitForAutomaticTranscriptionStart(
  itemImageId: string,
  overlay: ProcessingOverlay,
): Promise<void> {
  overlay.setDetail("Preparing automatic transcription...");
  const startedAt = Date.now();
  while (Date.now() - startedAt < 120000) {
    try {
      const jobs = await listTranscriptionJobs(BigInt(itemImageId));
      const latest = jobs[0];
      if (latest) {
        if (isPendingStatus(latest.status)) {
          overlay.setDetail("Automatic transcription is starting...");
          return;
        }
        if (isRunningStatus(latest.status)) {
          const total = latest.totalSegments > 0 ? latest.totalSegments : "?";
          overlay.setDetail(`Automatic transcription progress ${latest.completedSegments}/${total}`);
          return;
        }
        if (isCompletedStatus(latest.status)) {
          const total = latest.totalSegments > 0 ? latest.totalSegments : "?";
          overlay.setDetail(`Automatic transcription progress ${latest.completedSegments}/${total}`);
          return;
        }
        overlay.setDetail("Automatic transcription is starting...");
        return;
      }
    } catch {
      overlay.setDetail("Preparing automatic transcription...");
    }
    await new Promise((resolve) => window.setTimeout(resolve, 500));
  }
}

function exportHref(itemImageId: string, format: "hocr" | "pagexml" | "alto" | "txt"): string {
  return `/v1/item-images/${encodeURIComponent(itemImageId)}/export?format=${encodeURIComponent(format)}`;
}

function renderExportSelect(itemImageId: string): string {
  return `<select data-export-select="${escHtml(itemImageId)}" class="rounded border border-slate-700 bg-slate-950 px-2 py-1 text-xs text-slate-200">
      <option value="">Select format</option>
      <option value="hocr">hOCR</option>
      <option value="pagexml">PAGE XML</option>
      <option value="alto">ALTO XML</option>
      <option value="txt">Plain text</option>
    </select>`;
}

function renderImageExports(item: Item): string {
  if (item.images.length === 0) {
    return `<span class="text-xs text-slate-500">No images</span>`;
  }

  return item.images.map((image) => {
    const itemImageId = uint64ToString(image.id);

    return `
      <div class="rounded-lg border border-slate-800 bg-slate-950/40 px-3 py-2">
        ${renderExportSelect(itemImageId)}
      </div>`;
  }).join("");
}

export async function renderHome(app: HTMLElement): Promise<void> {
  app.innerHTML = `
    <main class="min-h-screen bg-slate-950">
      <header class="border-b border-slate-800 bg-slate-950/95 px-8 py-3">
        <div class="mx-auto flex max-w-5xl items-center justify-between">
          <div class="flex items-center gap-4">
            <a href="/" class="text-2xl font-bold tracking-tight">Scribe</a>
            <nav class="flex items-center gap-2 text-sm text-slate-300">
              <a href="/" class="rounded border border-slate-600 bg-slate-800 px-3 py-2">Home</a>
              <a href="/contexts" class="rounded border border-slate-700 px-3 py-2 hover:bg-slate-800">Contexts</a>
            </nav>
          </div>
          <p class="text-sm text-slate-300">Generate IIIF annotations for images.</p>
        </div>
      </header>
      <section class="mx-auto max-w-5xl p-8">
      <header class="mb-6">
        <h1 class="text-4xl font-bold">Workspace</h1>
        <p class="mt-2 text-slate-300">Segment text in images, generate OCR, and edit annotations.</p>
      </header>

      <!-- Create new item -->
      <section class="mb-6 rounded-xl border border-slate-700 bg-slate-900/60 p-6">
        <h2 class="mb-4 text-xl font-semibold">Create new item</h2>

        <!-- Tab bar -->
        <div class="mb-4 flex gap-1 border-b border-slate-700">
          <button data-tab="url"    class="tab-btn tab-active px-4 py-2 text-sm font-medium">Image URL</button>
          <button data-tab="single" class="tab-btn px-4 py-2 text-sm font-medium">Single upload</button>
          <button data-tab="multi"  class="tab-btn px-4 py-2 text-sm font-medium">Multi-upload</button>
          <button data-tab="manifest" class="tab-btn px-4 py-2 text-sm font-medium">IIIF Manifest</button>
        </div>

        <!-- URL tab -->
        <div id="tab-url" class="tab-panel space-y-3">
          <form id="url-form" class="flex gap-2">
            <input id="image-url" type="url" required
              class="flex-1 rounded border border-slate-600 bg-slate-950 px-3 py-2 text-sm"
              placeholder="https://example.org/image.jpg" />
            <button type="submit" class="rounded bg-brand-500 px-4 py-2 text-sm font-medium hover:bg-brand-600">
              Process URL
            </button>
          </form>
          <p id="url-status" class="text-xs text-slate-400"></p>
        </div>

        <!-- Single upload tab -->
        <div id="tab-single" class="tab-panel hidden space-y-3">
          <form id="single-form" class="flex gap-2">
            <input id="single-file" type="file" required
              accept=".jpg,.jpeg,.png,.gif,.webp,.jp2,.jpx,.j2k,.tif,.tiff"
              class="flex-1 rounded border border-slate-600 bg-slate-950 px-3 py-2 text-sm" />
            <button type="submit" class="rounded bg-brand-500 px-4 py-2 text-sm font-medium hover:bg-brand-600">
              Upload &amp; process
            </button>
          </form>
          <p id="single-status" class="text-xs text-slate-400"></p>
        </div>

        <!-- Multi-upload tab -->
        <div id="tab-multi" class="tab-panel hidden space-y-3">
          <form id="multi-form" class="flex gap-2">
            <input id="multi-files" type="file" multiple required
              accept=".jpg,.jpeg,.png,.gif,.webp,.jp2,.jpx,.j2k,.tif,.tiff"
              class="flex-1 rounded border border-slate-600 bg-slate-950 px-3 py-2 text-sm" />
            <button type="submit" class="rounded bg-brand-500 px-4 py-2 text-sm font-medium hover:bg-brand-600">
              Upload all
            </button>
          </form>
          <p id="multi-status" class="text-xs text-slate-400"></p>
        </div>

        <!-- Manifest tab -->
        <div id="tab-manifest" class="tab-panel hidden space-y-3">
          <form id="manifest-form" class="flex gap-2">
            <input id="manifest-url" type="url" required
              class="flex-1 rounded border border-slate-600 bg-slate-950 px-3 py-2 text-sm"
              placeholder="https://example.org/manifest.json" />
            <button type="submit" class="rounded bg-brand-500 px-4 py-2 text-sm font-medium hover:bg-brand-600">
              Ingest manifest
            </button>
          </form>
          <p id="manifest-status" class="text-xs text-slate-400"></p>
        </div>
      </section>

      <!-- Items table -->
      <section class="rounded-xl border border-slate-700 bg-slate-900/60 p-6">
        <div class="mb-4 flex items-center justify-between">
          <h2 class="text-xl font-semibold">Your items</h2>
          <button id="refresh-btn" class="rounded border border-slate-600 px-3 py-1.5 text-xs hover:bg-slate-800">
            Refresh
          </button>
        </div>
        <div id="items-container">
          <p class="text-sm text-slate-400">Loading…</p>
        </div>
      </section>
      </section>
    </main>
  `;

  // Tab switching
  const tabBtns = app.querySelectorAll<HTMLButtonElement>(".tab-btn");
  const tabPanels = app.querySelectorAll<HTMLDivElement>(".tab-panel");

  function switchTab(name: string) {
    tabBtns.forEach(btn => {
      btn.classList.toggle("tab-active", btn.dataset.tab === name);
    });
    tabPanels.forEach(panel => {
      panel.classList.toggle("hidden", panel.id !== `tab-${name}`);
    });
  }

  tabBtns.forEach(btn => {
    btn.addEventListener("click", () => switchTab(btn.dataset.tab ?? "url"));
  });

  let refreshInFlight = false;

  // Items table renderer
  async function refreshItems(options: { fromPoll?: boolean } = {}): Promise<boolean> {
    if (refreshInFlight) {
      if (!options.fromPoll) schedulePoll(1000);
      return false;
    }
    refreshInFlight = true;
    const container = document.getElementById("items-container")!;
    if (!options.fromPoll) {
      container.innerHTML = `<p class="text-sm text-slate-400">Loading…</p>`;
    }
    let items: Item[];
    try {
      items = await listItems();
    } catch (err) {
      container.innerHTML = `<p class="text-sm text-red-400">Failed to load items: ${escHtml(String(err))}</p>`;
      refreshInFlight = false;
      return false;
    }

    if (items.length === 0) {
      container.innerHTML = `<p class="text-sm text-slate-400">No items yet. Create one above.</p>`;
      refreshInFlight = false;
      return false;
    }

    // Fetch active transcription jobs for all images in parallel.
    const jobsByImageId = new Map<string, { completed: number; total: number; status: string }>();
    await Promise.all(items.flatMap(item =>
      item.images.map(async (image) => {
        try {
          const jobs = await listTranscriptionJobs(image.id);
          const active = jobs.find(j =>
            isRunningStatus(j.status) || isPendingStatus(j.status),
          ) ?? jobs[0];
          if (active) {
            jobsByImageId.set(uint64ToString(image.id), {
              completed: active.completedSegments,
              total: active.totalSegments,
              status: isRunningStatus(active.status) ? "running"
                : isPendingStatus(active.status) ? "pending"
                : isCompletedStatus(active.status) ? "done"
                : "failed",
            });
          }
        } catch { /* ignore per-image errors */ }
      })
    ));

    const hasActiveJobs = Array.from(jobsByImageId.values()).some((job) => job.status === "running" || job.status === "pending");

    const rows = items.map(item => {
      const itemLevelJob = item.images
        .map((image) => jobsByImageId.get(uint64ToString(image.id)))
        .find((job) => job && (job.status === "running" || job.status === "pending"))
        || item.images
          .map((image) => jobsByImageId.get(uint64ToString(image.id)))
          .find(Boolean);

      const itemProgressBadge = itemLevelJob
        ? itemLevelJob.status === "running" || itemLevelJob.status === "pending"
          ? `<span class="mt-2 inline-flex items-center gap-1 rounded border border-violet-800 px-1.5 py-0.5 text-xs text-violet-300">
               <span class="inline-block h-2 w-2 animate-spin rounded-full border border-violet-600 border-t-violet-300"></span>
               Transcribing ${itemLevelJob.completed}/${itemLevelJob.total}
             </span>`
          : itemLevelJob.status === "done"
          ? `<span class="mt-2 inline-flex rounded border border-emerald-900 px-1.5 py-0.5 text-xs text-emerald-400">${itemLevelJob.completed}/${itemLevelJob.total} done</span>`
          : `<span class="mt-2 inline-flex rounded border border-red-900 px-1.5 py-0.5 text-xs text-red-400">transcription failed</span>`
        : "";

      return `
        <tr class="border-t border-slate-800 align-top hover:bg-slate-800/40">
          <td class="px-3 py-3">
            <p class="text-sm">${escHtml(item.name || item.id)}</p>
            <p class="mt-1 text-xs text-slate-500">${escHtml(item.id)}</p>
            ${itemProgressBadge}
          </td>
          <td class="px-3 py-2 text-xs text-slate-400">${escHtml(item.sourceType)}</td>
          <td class="px-3 py-2 text-xs text-slate-400 text-center">${item.images.length}</td>
          <td class="px-3 py-2 text-xs text-slate-400">${escHtml(item.createdAt.slice(0, 10))}</td>
          <td class="px-3 py-2">
            <div class="flex flex-wrap items-center gap-1.5">
              ${item.images.length === 0
                ? `<span class="text-xs text-slate-500">No images</span>`
                : item.images.map((image) => {
                    const itemImageId = uint64ToString(image.id);
                    const editHref = `/editor?itemImageId=${encodeURIComponent(itemImageId)}`;
                    return `<a href="${editHref}" class="rounded border border-slate-600 px-2 py-0.5 text-xs text-slate-200 hover:border-slate-400 hover:bg-slate-800">Edit</a>`;
                  }).join("")}
              <button data-delete="${escHtml(item.id)}"
                class="rounded border border-slate-700 px-2 py-0.5 text-xs text-red-400 hover:border-red-800 hover:bg-red-900/30">
                Delete
              </button>
            </div>
          </td>
          <td class="px-3 py-2">
            <div class="space-y-2">
              ${renderImageExports(item)}
            </div>
          </td>
        </tr>`;
    }).join("");

    container.innerHTML = `
      <table class="w-full text-left">
        <thead>
          <tr class="text-xs text-slate-500">
            <th class="px-3 pb-2 font-medium">Name</th>
            <th class="px-3 pb-2 font-medium">Type</th>
            <th class="px-3 pb-2 font-medium text-center">Images</th>
            <th class="px-3 pb-2 font-medium">Created</th>
            <th class="px-3 pb-2 font-medium">Actions</th>
            <th class="px-3 pb-2 font-medium">Download</th>
          </tr>
        </thead>
        <tbody>${rows}</tbody>
      </table>`;

    container.querySelectorAll<HTMLButtonElement>("[data-delete]").forEach(btn => {
      btn.addEventListener("click", async () => {
        const id = btn.dataset.delete!;
        if (!confirm(`Delete item "${id}"?`)) return;
        try {
          await deleteItem(id);
          await refreshItems();
        } catch (err) {
          alert(`Delete failed: ${String(err)}`);
        }
      });
    });

    container.querySelectorAll<HTMLSelectElement>("[data-export-select]").forEach((select) => {
      select.addEventListener("change", () => {
        const itemImageId = select.dataset.exportSelect;
        const format = select.value as "" | "hocr" | "pagexml" | "alto" | "txt";
        if (!itemImageId || !format) return;
        window.location.href = exportHref(itemImageId, format);
        select.value = "";
      });
    });

    refreshInFlight = false;
    return hasActiveJobs;
  }

  document.getElementById("refresh-btn")!.addEventListener("click", () => void refreshItems());

  // URL form
  const urlForm = document.getElementById("url-form") as HTMLFormElement;
  const urlStatus = document.getElementById("url-status")!;
  urlForm.addEventListener("submit", async (e) => {
    e.preventDefault();
    const imageUrl = (document.getElementById("image-url") as HTMLInputElement).value.trim();
    if (!imageUrl) return;
    let overlay: ProcessingOverlay | null = null;
    let timers: number[] = [];
    try {
      overlay = createProcessingOverlay([
        "Fetching image",
        "Detecting layout",
        "Building document structure",
        "Starting automatic transcription",
      ]);
      timers = [600, 1800, 3200].map((ms, i) => setTimeout(() => overlay?.advance(i + 1), ms));
      const result = await processImageURL(imageUrl);
      timers.forEach(clearTimeout);
      overlay.advance(3);
      const itemImageId = uint64ToString(result.itemImageId);
      if (itemImageId && itemImageId !== "0") {
        overlay.setDetail("Preparing automatic transcription...");
        await waitForAutomaticTranscriptionStart(itemImageId, overlay);
        overlay.complete("Opening editor…");
        window.location.href = `/editor?itemImageId=${encodeURIComponent(itemImageId)}`;
      } else {
        overlay.remove();
        urlStatus.textContent = "Done. Refreshing items…";
        await refreshItems();
      }
    } catch (err) {
      timers.forEach(clearTimeout);
      overlay?.remove();
      urlStatus.textContent = `Error: ${String(err)}`;
    }
  });

  // Single upload form
  const singleForm = document.getElementById("single-form") as HTMLFormElement;
  const singleFile = document.getElementById("single-file") as HTMLInputElement;
  const singleStatus = document.getElementById("single-status")!;
  let singleInFlight = false;

  async function doSingleUpload() {
    if (singleInFlight) return;
    const file = singleFile.files?.[0];
    if (!file) return;
    singleInFlight = true;
    let overlay: ProcessingOverlay | null = null;
    let timers: number[] = [];
    try {
      overlay = createProcessingOverlay([
        "Uploading image",
        "Detecting layout",
        "Building document structure",
        "Starting automatic transcription",
      ]);
      timers = [800, 2200, 3600].map((ms, i) => setTimeout(() => overlay?.advance(i + 1), ms));
      const result = await processImageUpload(file);
      timers.forEach(clearTimeout);
      overlay.advance(3);
      const itemImageId = uint64ToString(result.itemImageId);
      if (itemImageId && itemImageId !== "0") {
        overlay.setDetail("Preparing automatic transcription...");
        await waitForAutomaticTranscriptionStart(itemImageId, overlay);
        overlay.complete("Opening editor…");
        window.location.href = `/editor?itemImageId=${encodeURIComponent(itemImageId)}`;
      } else {
        overlay.remove();
        singleStatus.textContent = "Done. Refreshing items…";
        await refreshItems();
      }
    } catch (err) {
      timers.forEach(clearTimeout);
      overlay?.remove();
      singleStatus.textContent = `Error: ${String(err)}`;
    } finally {
      singleInFlight = false;
    }
  }

  singleForm.addEventListener("submit", (e) => { e.preventDefault(); void doSingleUpload(); });
  singleFile.addEventListener("change", () => { void doSingleUpload(); });

  // Multi-upload form
  const multiForm = document.getElementById("multi-form") as HTMLFormElement;
  const multiFiles = document.getElementById("multi-files") as HTMLInputElement;
  const multiStatus = document.getElementById("multi-status")!;
  let multiInFlight = false;

  multiForm.addEventListener("submit", async (e) => {
    e.preventDefault();
    if (multiInFlight) return;
    const files = Array.from(multiFiles.files ?? []);
    if (files.length === 0) return;
    multiInFlight = true;
    multiStatus.textContent = `Uploading ${files.length} file(s)…`;
    try {
      await uploadItemImages(files);
      multiStatus.textContent = "Uploaded. Refreshing items…";
      multiForm.reset();
      await refreshItems();
    } catch (err) {
      multiStatus.textContent = `Error: ${String(err)}`;
    } finally {
      multiInFlight = false;
    }
  });

  // Manifest form
  const manifestForm = document.getElementById("manifest-form") as HTMLFormElement;
  const manifestStatus = document.getElementById("manifest-status")!;
  manifestForm.addEventListener("submit", async (e) => {
    e.preventDefault();
    const url = (document.getElementById("manifest-url") as HTMLInputElement).value.trim();
    if (!url) return;
    try {
      const { firstItemImageId } = await withProcessingOverlay(
        ["Fetching IIIF manifest", "Detecting layout", "Building document structure"],
        [900, 2400],
        () => createItemFromManifest(url),
      );
      if (firstItemImageId && firstItemImageId !== "0") {
        window.location.href = `/editor?itemImageId=${encodeURIComponent(firstItemImageId)}`;
      } else {
        manifestStatus.textContent = "Ingested. Refreshing items…";
        manifestForm.reset();
        await refreshItems();
      }
    } catch (err) {
      manifestStatus.textContent = `Error: ${String(err)}`;
    }
  });

  // Initial load
  void refreshItems();
  let refreshQueued = false;
  const subscription = subscribeToEvents(
    {
      types: [
        "dev.scribe.transcription.task.completed",
        "dev.scribe.transcription.completed",
        "dev.scribe.transcription.failed",
        "dev.scribe.annotations.created",
        "dev.scribe.annotations.published",
      ],
    },
    () => {
      if (refreshQueued) return;
      refreshQueued = true;
      window.setTimeout(() => {
        refreshQueued = false;
        void refreshItems({ fromPoll: true });
      }, 200);
    },
  );
  window.addEventListener("beforeunload", () => {
    subscription.close();
  }, { once: true });
}
