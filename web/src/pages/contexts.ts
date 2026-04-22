import { create } from "@bufbuild/protobuf";
import { createContext, getContextMetrics, listContexts, type ContextMetrics } from "../api/context";
import {
  createProviderSecret,
  deleteProviderSecret,
  getAuthMe,
  listProviderSecrets,
  logout,
  type ProviderSecretRecord,
} from "../api/auth";
import { escHtml } from "../lib/util";
import { ContextSchema, type Context } from "../proto/scribe/v1/context_pb";

function levBadgeClass(avg: number): string {
  if (avg === 0) return "bg-slate-700 text-slate-300";
  if (avg < 10) return "bg-emerald-900 text-emerald-300";
  if (avg < 50) return "bg-amber-900 text-amber-300";
  return "bg-red-900 text-red-300";
}

function renderContextCard(ctx: Context, metrics: ContextMetrics): string {
  const totalRuns = metrics.total_runs;
  const correctedRuns = metrics.corrected_runs;
  const avgLev = metrics.avg_levenshtein_distance;
  const correctedPct = totalRuns > 0 ? Math.round((correctedRuns / totalRuns) * 100) : 0;

  return `
    <article class="rounded-xl border border-slate-700 bg-slate-900/60 p-5">
      <div class="flex items-start justify-between gap-3">
        <div>
          <h2 class="text-lg font-semibold">${escHtml(ctx.name)}</h2>
          <p class="mt-1 text-sm text-slate-400">${escHtml(ctx.description || "No description.")}</p>
        </div>
        ${ctx.isDefault ? `<span class="rounded border border-brand-600 px-2 py-0.5 text-xs text-brand-400">Default</span>` : ""}
      </div>

      <dl class="mt-4 grid gap-3 sm:grid-cols-2">
        <div class="rounded-lg bg-slate-800/70 px-3 py-2">
          <dt class="text-xs uppercase tracking-wide text-slate-500">Provider</dt>
          <dd class="mt-1 text-sm text-slate-200">${escHtml(ctx.transcriptionProvider || "—")}</dd>
        </div>
        <div class="rounded-lg bg-slate-800/70 px-3 py-2">
          <dt class="text-xs uppercase tracking-wide text-slate-500">Model</dt>
          <dd class="mt-1 truncate font-mono text-xs text-slate-200" title="${escHtml(ctx.transcriptionModel || "")}">${escHtml(ctx.transcriptionModel || "—")}</dd>
        </div>
        <div class="rounded-lg bg-slate-800/70 px-3 py-2">
          <dt class="text-xs uppercase tracking-wide text-slate-500">Segmentation</dt>
          <dd class="mt-1 truncate font-mono text-xs text-slate-200" title="${escHtml(ctx.segmentationModel || "")}">${escHtml(ctx.segmentationModel || "—")}</dd>
        </div>
        <div class="rounded-lg bg-slate-800/70 px-3 py-2 sm:col-span-2">
          <dt class="text-xs uppercase tracking-wide text-slate-500">Endpoint</dt>
          <dd class="mt-1 truncate font-mono text-xs text-slate-200" title="${escHtml(ctx.transcriptionBaseUrl || "")}">${escHtml(ctx.transcriptionBaseUrl || "Global config default")}</dd>
        </div>
        <div class="rounded-lg bg-slate-800/70 px-3 py-2">
          <dt class="text-xs uppercase tracking-wide text-slate-500">Runs with edits</dt>
          <dd class="mt-1 text-sm text-slate-200">${correctedRuns} / ${totalRuns} <span class="text-slate-500">(${correctedPct}%)</span></dd>
        </div>
      </dl>

      <div class="mt-4 grid gap-3 sm:grid-cols-3">
        <div class="rounded-lg border border-slate-800 px-3 py-3">
          <p class="text-xs uppercase tracking-wide text-slate-500">Avg Levenshtein</p>
          <p class="mt-2">
            <span class="inline-flex rounded px-2 py-1 text-sm font-semibold ${levBadgeClass(avgLev)}">${avgLev.toFixed(1)}</span>
          </p>
          <p class="mt-2 text-xs text-slate-500">Across saved edited runs only.</p>
        </div>
        <div class="rounded-lg border border-slate-800 px-3 py-3">
          <p class="text-xs uppercase tracking-wide text-slate-500">Avg text edits</p>
          <p class="mt-2 text-2xl font-semibold">${metrics.avg_edit_count.toFixed(1)}</p>
        </div>
        <div class="rounded-lg border border-slate-800 px-3 py-3">
          <p class="text-xs uppercase tracking-wide text-slate-500">Avg box change</p>
          <p class="mt-2 text-2xl font-semibold">${metrics.avg_box_change_score.toFixed(1)}</p>
        </div>
      </div>
    </article>`;
}

function renderProviderSecrets(secrets: ProviderSecretRecord[]): string {
  if (secrets.length === 0) {
    return `<p class="text-sm text-slate-400">No workspace or personal provider secrets saved yet.</p>`;
  }

  return `
    <div class="overflow-x-auto">
      <table class="min-w-full text-left text-sm">
        <thead class="text-xs uppercase tracking-wide text-slate-500">
          <tr>
            <th class="px-3 py-2">Provider</th>
            <th class="px-3 py-2">Name</th>
            <th class="px-3 py-2">Scope</th>
            <th class="px-3 py-2">Key</th>
            <th class="px-3 py-2">Updated</th>
            <th class="px-3 py-2"></th>
          </tr>
        </thead>
        <tbody>
          ${secrets.map((secret) => `
            <tr class="border-t border-slate-800">
              <td class="px-3 py-3 text-slate-200">${escHtml(secret.provider)}</td>
              <td class="px-3 py-3 text-slate-200">${escHtml(secret.name)}</td>
              <td class="px-3 py-3 text-slate-300">${escHtml(secret.scope)}</td>
              <td class="px-3 py-3 font-mono text-xs text-slate-400">${secret.key_hint ? `****${escHtml(secret.key_hint)}` : "Stored"}</td>
              <td class="px-3 py-3 text-slate-400">${escHtml(new Date(secret.updated_at).toLocaleString())}</td>
              <td class="px-3 py-3 text-right">
                <button
                  type="button"
                  class="rounded border border-slate-700 px-3 py-1.5 text-xs text-slate-200 hover:bg-slate-800"
                  data-provider-secret-delete="${escHtml(String(secret.id))}"
                >
                  Delete
                </button>
              </td>
            </tr>`).join("")}
        </tbody>
      </table>
    </div>`;
}

export async function renderContexts(app: HTMLElement): Promise<void> {
  app.innerHTML = `
    <main class="min-h-screen bg-slate-950">
      <header class="border-b border-slate-800 bg-slate-950/95 px-8 py-3">
        <div class="mx-auto flex max-w-6xl items-center justify-between">
          <div class="flex items-center gap-4">
            <a href="/" class="text-2xl font-bold tracking-tight">Scribe</a>
            <nav class="flex items-center gap-2 text-sm text-slate-300">
              <a href="/" class="rounded border border-slate-700 px-3 py-2 hover:bg-slate-800">Home</a>
              <a href="/contexts" class="rounded border border-slate-600 bg-slate-800 px-3 py-2">Contexts</a>
            </nav>
          </div>
          <div class="flex items-center gap-4">
            <a href="/" class="text-sm text-slate-400 hover:text-slate-200">Back to dashboard</a>
            <div id="auth-summary" class="text-sm text-slate-400"></div>
          </div>
        </div>
      </header>
      <section class="mx-auto max-w-6xl p-8">
        <header class="mb-6 flex flex-wrap items-end justify-between gap-4">
          <div>
            <h1 class="text-4xl font-bold">Context Metrics</h1>
            <p class="mt-2 max-w-3xl text-slate-300">Correction distance is cached when edits are saved, then aggregated here by context so you can compare OCR performance without recalculating the corpus.</p>
          </div>
          <button id="refresh-contexts" class="rounded border border-slate-600 px-3 py-2 text-sm hover:bg-slate-800">Refresh</button>
        </header>
        <div id="contexts-summary" class="mb-6 grid gap-4 sm:grid-cols-3"></div>
        <div id="contexts-container">
          <p class="text-sm text-slate-400">Loading…</p>
        </div>

        <section class="mb-6 rounded-xl border border-slate-700 bg-slate-900/60 p-6">
          <div class="mb-4">
            <h2 class="text-xl font-semibold">Create context</h2>
            <p class="mt-1 text-sm text-slate-400">Add a processing profile for OCR runs and future metrics aggregation.</p>
          </div>
          <form id="create-context-form" class="grid gap-4 md:grid-cols-2">
            <label class="block">
              <span class="mb-1 block text-sm text-slate-300">Name</span>
              <input id="context-name" required class="w-full rounded border border-slate-600 bg-slate-950 px-3 py-2 text-sm" />
            </label>
            <label class="block">
              <span class="mb-1 block text-sm text-slate-300">Transcription provider</span>
              <input id="context-provider" value="ollama" class="w-full rounded border border-slate-600 bg-slate-950 px-3 py-2 text-sm" />
            </label>
            <label class="block md:col-span-2">
              <span class="mb-1 block text-sm text-slate-300">Description</span>
              <textarea id="context-description" rows="2" class="w-full rounded border border-slate-600 bg-slate-950 px-3 py-2 text-sm"></textarea>
            </label>
            <label class="block">
              <span class="mb-1 block text-sm text-slate-300">Transcription model</span>
              <input id="context-model" class="w-full rounded border border-slate-600 bg-slate-950 px-3 py-2 text-sm" />
            </label>
            <label class="block">
              <span class="mb-1 block text-sm text-slate-300">Segmentation model</span>
              <input id="context-segmentation" value="tesseract" class="w-full rounded border border-slate-600 bg-slate-950 px-3 py-2 text-sm" />
            </label>
            <label class="block md:col-span-2">
              <span class="mb-1 block text-sm text-slate-300">Transcription URL</span>
              <input id="context-base-url" placeholder="https://<service>.run.app" class="w-full rounded border border-slate-600 bg-slate-950 px-3 py-2 font-mono text-sm" />
            </label>
            <label class="block md:col-span-2">
              <span class="mb-1 block text-sm text-slate-300">Transcription audience</span>
              <input id="context-audience" placeholder="Optional custom Cloud Run audience" class="w-full rounded border border-slate-600 bg-slate-950 px-3 py-2 font-mono text-sm" />
            </label>
            <label class="block md:col-span-2">
              <span class="mb-1 block text-sm text-slate-300">System prompt</span>
              <textarea id="context-system-prompt" rows="4" class="w-full rounded border border-slate-600 bg-slate-950 px-3 py-2 text-sm"></textarea>
            </label>
            <div class="flex items-center gap-3 md:col-span-2">
              <label class="inline-flex items-center gap-2 text-sm text-slate-300">
                <input id="context-default" type="checkbox" class="rounded border-slate-600 bg-slate-950" />
                <span>Set as default system context</span>
              </label>
              <button type="submit" class="rounded bg-brand-500 px-4 py-2 text-sm font-medium hover:bg-brand-600">Create context</button>
            </div>
            <p id="create-context-status" class="md:col-span-2 text-sm text-slate-400"></p>
          </form>
        </section>

        <section class="rounded-xl border border-slate-700 bg-slate-900/60 p-6">
          <div class="mb-4">
            <h2 class="text-xl font-semibold">Provider secrets</h2>
            <p class="mt-1 text-sm text-slate-400">Store workspace or personal provider API keys in Vault. User-scoped keys override workspace keys for the same provider.</p>
          </div>
          <form id="provider-secret-form" class="grid gap-4 md:grid-cols-4">
            <label class="block">
              <span class="mb-1 block text-sm text-slate-300">Provider</span>
              <select id="provider-secret-provider" class="w-full rounded border border-slate-600 bg-slate-950 px-3 py-2 text-sm">
                <option value="gemini">gemini</option>
              </select>
            </label>
            <label class="block">
              <span class="mb-1 block text-sm text-slate-300">Name</span>
              <input id="provider-secret-name" required class="w-full rounded border border-slate-600 bg-slate-950 px-3 py-2 text-sm" />
            </label>
            <label class="block">
              <span class="mb-1 block text-sm text-slate-300">Scope</span>
              <select id="provider-secret-scope" class="w-full rounded border border-slate-600 bg-slate-950 px-3 py-2 text-sm">
                <option value="user">Personal</option>
                <option value="workspace">Workspace</option>
              </select>
            </label>
            <label class="block md:col-span-4">
              <span class="mb-1 block text-sm text-slate-300">API key</span>
              <input id="provider-secret-api-key" type="password" required class="w-full rounded border border-slate-600 bg-slate-950 px-3 py-2 text-sm" />
            </label>
            <div class="md:col-span-4 flex items-center gap-3">
              <button type="submit" class="rounded bg-brand-500 px-4 py-2 text-sm font-medium hover:bg-brand-600">Save provider secret</button>
              <p id="provider-secret-status" class="text-sm text-slate-400"></p>
            </div>
          </form>
          <div id="provider-secrets-container" class="mt-6"></div>
        </section>
      </section>
    </main>
  `;

  const authSummary = document.getElementById("auth-summary") as HTMLDivElement;
  void getAuthMe().then((authState) => {
    if (authState.authenticated && authState.user) {
      const displayName = authState.user.name || authState.user.email || "Signed in";
      authSummary.innerHTML = `
        <div class="flex items-center gap-2">
          <span class="text-slate-300">${escHtml(displayName)}</span>
          <button id="logout-btn" class="rounded border border-slate-700 px-3 py-1.5 text-xs hover:bg-slate-800">Logout</button>
        </div>`;
      const logoutBtn = document.getElementById("logout-btn") as HTMLButtonElement | null;
      logoutBtn?.addEventListener("click", async () => {
        await logout();
        window.location.href = "/";
      });
      return;
    }
    const redirect = encodeURIComponent(window.location.pathname + window.location.search);
    authSummary.innerHTML = `<a class="rounded border border-slate-700 px-3 py-1.5 text-xs text-slate-200 hover:bg-slate-800" href="${escHtml(authState.loginUrl)}?redirect=${redirect}">Sign in with Google</a>`;
  }).catch(() => {
    authSummary.textContent = "Auth unavailable";
  });

  const container = document.getElementById("contexts-container")!;
  const summary = document.getElementById("contexts-summary")!;
  const createForm = document.getElementById("create-context-form") as HTMLFormElement;
  const createStatus = document.getElementById("create-context-status")!;
  const providerSecretForm = document.getElementById("provider-secret-form") as HTMLFormElement;
  const providerSecretStatus = document.getElementById("provider-secret-status")!;
  const providerSecretsContainer = document.getElementById("provider-secrets-container")!;

  async function handleCreateContext(event: Event) {
    event.preventDefault();
    createStatus.textContent = "Creating context…";

    const name = (document.getElementById("context-name") as HTMLInputElement).value.trim();
    const provider = (document.getElementById("context-provider") as HTMLInputElement).value.trim();
    const model = (document.getElementById("context-model") as HTMLInputElement).value.trim();
    const segmentationModel = (document.getElementById("context-segmentation") as HTMLInputElement).value.trim();
    const transcriptionBaseUrl = (document.getElementById("context-base-url") as HTMLInputElement).value.trim();
    const transcriptionAudience = (document.getElementById("context-audience") as HTMLInputElement).value.trim();
    const description = (document.getElementById("context-description") as HTMLTextAreaElement).value.trim();
    const systemPrompt = (document.getElementById("context-system-prompt") as HTMLTextAreaElement).value.trim();
    const isDefault = (document.getElementById("context-default") as HTMLInputElement).checked;

    if (!name) {
      createStatus.textContent = "Name is required.";
      return;
    }

    try {
      await createContext(create(ContextSchema, {
        name,
        description,
        isDefault,
        segmentationModel: segmentationModel || "tesseract",
        transcriptionProvider: provider || "ollama",
        transcriptionModel: model,
        transcriptionBaseUrl,
        transcriptionAudience,
        temperature: -1,
        systemPrompt,
      }));
      createForm.reset();
      (document.getElementById("context-provider") as HTMLInputElement).value = "ollama";
      (document.getElementById("context-segmentation") as HTMLInputElement).value = "tesseract";
      createStatus.textContent = "Context created.";
      await refreshContexts();
    } catch (err) {
      createStatus.textContent = `Create failed: ${String(err)}`;
    }
  }

  async function refreshContexts() {
    container.innerHTML = `<p class="text-sm text-slate-400">Loading…</p>`;
    summary.innerHTML = "";

    let contexts: Context[];
    try {
      contexts = await listContexts();
    } catch (err) {
      container.innerHTML = `<p class="text-sm text-red-400">Failed to load contexts: ${escHtml(String(err))}</p>`;
      return;
    }

    if (contexts.length === 0) {
      container.innerHTML = `<p class="text-sm text-slate-400">No contexts found.</p>`;
      return;
    }

    const metricsResults = await Promise.all(
      contexts.map(async (ctx) => {
        try {
          return await getContextMetrics(ctx.id.toString());
        } catch {
          return {
            context_id: Number(ctx.id),
            total_runs: 0,
            corrected_runs: 0,
            avg_levenshtein_distance: 0,
            avg_edit_count: 0,
            avg_box_change_score: 0,
          } satisfies ContextMetrics;
        }
      }),
    );

    const totalRuns = metricsResults.reduce((sum, metrics) => sum + metrics.total_runs, 0);
    const totalCorrectedRuns = metricsResults.reduce((sum, metrics) => sum + metrics.corrected_runs, 0);
    const contextsWithEdits = metricsResults.filter((metrics) => metrics.corrected_runs > 0).length;

    summary.innerHTML = `
      <section class="rounded-xl border border-slate-700 bg-slate-900/60 p-4">
        <p class="text-xs uppercase tracking-wide text-slate-500">Contexts</p>
        <p class="mt-2 text-3xl font-semibold">${contexts.length}</p>
      </section>
      <section class="rounded-xl border border-slate-700 bg-slate-900/60 p-4">
        <p class="text-xs uppercase tracking-wide text-slate-500">OCR runs tracked</p>
        <p class="mt-2 text-3xl font-semibold">${totalRuns}</p>
      </section>
      <section class="rounded-xl border border-slate-700 bg-slate-900/60 p-4">
        <p class="text-xs uppercase tracking-wide text-slate-500">Contexts with saved edits</p>
        <p class="mt-2 text-3xl font-semibold">${contextsWithEdits}</p>
        <p class="mt-1 text-xs text-slate-500">${totalCorrectedRuns} corrected run${totalCorrectedRuns === 1 ? "" : "s"}</p>
      </section>
    `;

    container.innerHTML = `<div class="grid gap-5 lg:grid-cols-2">${contexts
      .map((ctx, index) => renderContextCard(ctx, metricsResults[index]))
      .join("")}</div>`;
  }

  async function refreshProviderSecrets() {
    providerSecretsContainer.innerHTML = `<p class="text-sm text-slate-400">Loading…</p>`;
    try {
      const secrets = await listProviderSecrets();
      providerSecretsContainer.innerHTML = renderProviderSecrets(secrets);
    } catch (err) {
      providerSecretsContainer.innerHTML = `<p class="text-sm text-red-400">Failed to load provider secrets: ${escHtml(String(err))}</p>`;
    }
  }

  async function handleCreateProviderSecret(event: Event) {
    event.preventDefault();
    providerSecretStatus.textContent = "Saving provider secret…";

    const provider = (document.getElementById("provider-secret-provider") as HTMLSelectElement).value.trim();
    const name = (document.getElementById("provider-secret-name") as HTMLInputElement).value.trim();
    const apiKey = (document.getElementById("provider-secret-api-key") as HTMLInputElement).value.trim();
    const scope = (document.getElementById("provider-secret-scope") as HTMLSelectElement).value.trim() as "user" | "workspace";

    if (!name || !apiKey) {
      providerSecretStatus.textContent = "Name and API key are required.";
      return;
    }

    try {
      await createProviderSecret({
        provider,
        name,
        apiKey,
        scope,
      });
      providerSecretForm.reset();
      (document.getElementById("provider-secret-provider") as HTMLSelectElement).value = "gemini";
      (document.getElementById("provider-secret-scope") as HTMLSelectElement).value = "user";
      providerSecretStatus.textContent = "Provider secret saved.";
      await refreshProviderSecrets();
    } catch (err) {
      providerSecretStatus.textContent = `Save failed: ${String(err)}`;
    }
  }

  document.getElementById("refresh-contexts")!.addEventListener("click", () => void refreshContexts());
  createForm.addEventListener("submit", (event) => { void handleCreateContext(event); });
  providerSecretForm.addEventListener("submit", (event) => { void handleCreateProviderSecret(event); });
  providerSecretsContainer.addEventListener("click", (event) => {
    const target = event.target as HTMLElement | null;
    const secretID = target?.getAttribute("data-provider-secret-delete");
    if (!secretID) {
      return;
    }
    providerSecretStatus.textContent = "Deleting provider secret…";
    void deleteProviderSecret(secretID)
      .then(async () => {
        providerSecretStatus.textContent = "Provider secret deleted.";
        await refreshProviderSecrets();
      })
      .catch((err) => {
        providerSecretStatus.textContent = `Delete failed: ${String(err)}`;
      });
  });
  void refreshContexts();
  void refreshProviderSecrets();
}
