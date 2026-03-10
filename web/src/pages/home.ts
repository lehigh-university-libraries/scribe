import { listItems, createItemFromManifest, uploadItemImages, deleteItem } from "../api/items";
import { processImageURL, processImageUpload } from "../api/processing";
import { uint64ToString, escHtml } from "../lib/util";
import type { Item } from "../proto/scribe/v1/item_pb";

export async function renderHome(app: HTMLElement): Promise<void> {
  app.innerHTML = `
    <main class="mx-auto max-w-5xl p-8">
      <header class="mb-6">
        <h1 class="text-4xl font-bold">Scribe</h1>
        <p class="mt-2 text-slate-300">Process images for OCR and edit annotations.</p>
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

  // Items table renderer
  async function refreshItems() {
    const container = document.getElementById("items-container")!;
    container.innerHTML = `<p class="text-sm text-slate-400">Loading…</p>`;
    let items: Item[];
    try {
      items = await listItems();
    } catch (err) {
      container.innerHTML = `<p class="text-sm text-red-400">Failed to load items: ${escHtml(String(err))}</p>`;
      return;
    }

    if (items.length === 0) {
      container.innerHTML = `<p class="text-sm text-slate-400">No items yet. Create one above.</p>`;
      return;
    }

    const rows = items.map(item => {
      const firstImageId = item.images[0] ? uint64ToString(item.images[0].id) : "";
      const editHref = firstImageId ? `/editor?itemImageId=${encodeURIComponent(firstImageId)}` : "";
      const editBtn = editHref
        ? `<a href="${editHref}" class="rounded bg-emerald-700 px-2 py-1 text-xs font-medium hover:bg-emerald-600">Edit</a>`
        : `<span class="text-xs text-slate-500">No images</span>`;
      return `
        <tr class="border-t border-slate-800 hover:bg-slate-800/40">
          <td class="px-3 py-2 text-sm">${escHtml(item.name || item.id)}</td>
          <td class="px-3 py-2 text-xs text-slate-400">${escHtml(item.sourceType)}</td>
          <td class="px-3 py-2 text-xs text-slate-400 text-center">${item.images.length}</td>
          <td class="px-3 py-2 text-xs text-slate-400">${escHtml(item.createdAt.slice(0, 10))}</td>
          <td class="px-3 py-2">
            <div class="flex gap-2">
              ${editBtn}
              <button data-delete="${escHtml(item.id)}"
                class="rounded border border-red-800 px-2 py-1 text-xs text-red-400 hover:bg-red-900/40">
                Delete
              </button>
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
  }

  document.getElementById("refresh-btn")!.addEventListener("click", () => void refreshItems());

  // URL form
  const urlForm = document.getElementById("url-form") as HTMLFormElement;
  const urlStatus = document.getElementById("url-status")!;
  urlForm.addEventListener("submit", async (e) => {
    e.preventDefault();
    const imageUrl = (document.getElementById("image-url") as HTMLInputElement).value.trim();
    if (!imageUrl) return;
    urlStatus.textContent = "Processing…";
    try {
      const result = await processImageURL(imageUrl);
      const itemImageId = uint64ToString(result.itemImageId);
      if (itemImageId && itemImageId !== "0") {
        window.location.href = `/editor?itemImageId=${encodeURIComponent(itemImageId)}`;
      } else {
        urlStatus.textContent = "Done. Refreshing items…";
        await refreshItems();
      }
    } catch (err) {
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
    singleStatus.textContent = "Processing…";
    try {
      const result = await processImageUpload(file);
      const itemImageId = uint64ToString(result.itemImageId);
      if (itemImageId && itemImageId !== "0") {
        window.location.href = `/editor?itemImageId=${encodeURIComponent(itemImageId)}`;
      } else {
        singleStatus.textContent = "Done. Refreshing items…";
        await refreshItems();
      }
    } catch (err) {
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
    manifestStatus.textContent = "Ingesting…";
    try {
      const { firstItemImageId } = await createItemFromManifest(url);
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
}
