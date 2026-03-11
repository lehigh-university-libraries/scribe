import Mirador from "mirador";
import scribeMiradorPlugin, { annotationAdapters } from "../../vendor/mirador-scribe/dist/mirador-scribe.es.js";
import { annotationClient, publishItemImageEdits } from "../api/annotations";
import { getOCRRun } from "../api/processing";
import { listTranscriptionJobs } from "../api/transcription";
import { subscribeToEvents } from "../api/events";
import { TranscriptionJobStatus } from "../proto/scribe/v1/transcription_pb";
import { uint64ToString } from "../lib/util";

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

function isFailedStatus(status: TranscriptionJobStatus | string | number): boolean {
  return status === TranscriptionJobStatus.FAILED
    || status === "TRANSCRIPTION_JOB_STATUS_FAILED"
    || status === "failed";
}

export async function renderEditor(app: HTMLElement): Promise<void> {
  const params = new URLSearchParams(window.location.search);
  const itemImageID = params.get("itemImageId") ?? "";
  const autoTranscribe = params.get("autoTranscribe") === "1";
  const jobIdParam = params.get("jobId");
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
          <p id="editor-transcription-status" class="mt-1 text-xs text-amber-300"></p>
        </div>
      </header>
      <section class="relative h-[calc(100vh-56px)]">
        <div id="editor-batch-banner" class="hidden pointer-events-none absolute inset-x-0 top-0 z-40 px-4 py-4">
          <div class="mx-auto flex max-w-6xl items-start justify-between gap-4 rounded-xl border border-amber-500/30 bg-slate-950/92 px-4 py-3 shadow-2xl backdrop-blur">
            <div>
              <p id="editor-batch-banner-title" class="text-sm font-semibold text-amber-200"></p>
              <p id="editor-batch-banner-detail" class="mt-1 text-sm text-amber-100/80"></p>
            </div>
          </div>
        </div>
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
  const transcriptionStatus = document.getElementById("editor-transcription-status") as HTMLParagraphElement;
  const batchBanner = document.getElementById("editor-batch-banner") as HTMLDivElement;
  const batchBannerTitle = document.getElementById("editor-batch-banner-title") as HTMLParagraphElement;
  const batchBannerDetail = document.getElementById("editor-batch-banner-detail") as HTMLParagraphElement;
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
  let lastSegmentKey = "";
  let lastResultKey = "";
  let reloadedCompletedJobId = "";

  function setTranscriptionStatus(message = "") {
    transcriptionStatus.textContent = message;
  }

  function setBatchBanner(title = "", detail = "", active = false) {
    if (!active) {
      batchBanner.classList.add("hidden");
      batchBannerTitle.textContent = "";
      batchBannerDetail.textContent = "";
      return;
    }
    batchBanner.classList.remove("hidden");
    batchBannerTitle.textContent = title;
    batchBannerDetail.textContent = detail;
  }

  function publishBatchState(message: string, active: boolean) {
    setTranscriptionStatus(message);
    document.dispatchEvent(new CustomEvent("scribe:transcription-job-state", {
      detail: {
        active,
        message,
        windowId: undefined,
      },
    }));
  }

  function renderJobStatus(job: {
    status: TranscriptionJobStatus | string | number;
    completedSegments: number;
    totalSegments: number;
    failedSegments?: number;
    errorMessage?: string;
  }) {
    if (isRunningStatus(job.status)) {
      const total = job.totalSegments > 0 ? job.totalSegments : "?";
      publishBatchState(`Batch transcription is running. Automatic transcription progress: ${job.completedSegments}/${total}. Edit overlay is paused while new text is applied.`, true);
      setBatchBanner(
        `Automatic transcription in progress: ${job.completedSegments}/${total}`,
        "Scribe is still writing text onto the page line by line. You can view the page now, but editing tools stay out of the way until the current updates settle.",
        true,
      );
      return;
    }
    if (isPendingStatus(job.status)) {
      publishBatchState("Preparing batch transcription. Layout is ready; text is being generated line by line.", true);
      setBatchBanner(
        "Automatic transcription is starting",
        "The page structure is ready. Scribe is starting line-by-line transcription and the first results will appear here shortly.",
        true,
      );
      return;
    }
    if (isFailedStatus(job.status)) {
      publishBatchState(job.errorMessage?.trim() ? `Batch transcription failed: ${job.errorMessage}` : "Batch transcription failed.", false);
      setBatchBanner("", "", false);
      return;
    }
    if (isCompletedStatus(job.status)) {
      publishBatchState("Batch transcription complete. Updated text is now available in the editor.", false);
      setBatchBanner("", "", false);
      return;
    }
    publishBatchState("", false);
    setBatchBanner("", "", false);
  }

  function applyJobUpdate(job: {
    id: bigint;
    status: TranscriptionJobStatus | string | number;
    completedSegments: number;
    totalSegments: number;
    currentAnnotationId?: string;
    currentAnnotationJson?: string;
    lastResultAnnotationJson?: string;
    updatedAt?: string;
    errorMessage?: string;
  }) {
    renderJobStatus(job);

    if (job.currentAnnotationJson) {
      const segmentKey = `${job.id.toString()}:${job.currentAnnotationId ?? ""}:${job.updatedAt ?? ""}:${job.completedSegments}/${job.totalSegments}`;
      if (segmentKey !== lastSegmentKey) {
        lastSegmentKey = segmentKey;
        try {
          const anno = JSON.parse(job.currentAnnotationJson);
          document.dispatchEvent(new CustomEvent("scribe:transcription-segment", {
            detail: { annotation: anno, done: job.completedSegments, total: job.totalSegments },
          }));
        } catch { /* ignore */ }
      }
    }

    if (job.lastResultAnnotationJson) {
      const resultKey = `${job.id.toString()}:${job.updatedAt ?? ""}:${job.completedSegments}/${job.totalSegments}`;
      if (resultKey !== lastResultKey) {
        lastResultKey = resultKey;
        try {
          const anno = JSON.parse(job.lastResultAnnotationJson);
          document.dispatchEvent(new CustomEvent("scribe:transcription-result", {
            detail: { annotation: anno, done: job.completedSegments, total: job.totalSegments },
          }));
        } catch { /* ignore */ }
      }
    }

    if (isCompletedStatus(job.status) || isFailedStatus(job.status)) {
      document.dispatchEvent(new CustomEvent("scribe:transcription-segment", {
        detail: { annotation: null },
      }));
    }

    if (isCompletedStatus(job.status)) {
      const jobID = job.id.toString();
      if (reloadedCompletedJobId !== jobID) {
        reloadedCompletedJobId = jobID;
        document.dispatchEvent(new CustomEvent("scribe:reload-annotations", { detail: {} }));
      }
    }
  }

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

  document.addEventListener("scribe:request-publish", async (event: Event) => {
    const detail = (event as CustomEvent<{ itemImageId: string; requestId: string; windowId?: string }>).detail;
    if (!detail?.itemImageId || !detail?.requestId) return;
    let ok = false;
    try {
      await publishItemImageEdits(detail.itemImageId);
      ok = true;
    } catch {
      ok = false;
    }
    document.dispatchEvent(new CustomEvent("scribe:publish-result", {
      detail: {
        ok,
        requestId: detail.requestId,
        windowId: detail.windowId,
      },
    }));
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

  publishBatchState("Loading editor and checking batch transcription status...", true);

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

  if (jobIdParam) {
    publishBatchState("Preparing batch transcription...", true);
  } else if (autoTranscribe) {
    // Legacy path: client-side segment-by-segment transcription via the magic wand.
    setTimeout(() => {
      document.dispatchEvent(new CustomEvent("scribe:request-transcribe-all", {
        detail: { windowId: undefined },
      }));
    }, 3000);
  }

  if (itemImageID) {
    try {
      const jobs = await listTranscriptionJobs(BigInt(itemImageID));
      const latest = jobs[0];
      if (latest) {
        applyJobUpdate(latest);
      } else {
        publishBatchState("Loading editor annotations. No active batch transcription job detected yet.", false);
      }
    } catch {
      publishBatchState("Loading editor annotations.", false);
    }
  }

  const eventSubscription = subscribeToEvents(
    {
      itemImageId: itemImageID,
      types: [
        "dev.scribe.transcription.task.started",
        "dev.scribe.transcription.task.completed",
        "dev.scribe.transcription.completed",
        "dev.scribe.transcription.failed",
      ],
    },
    (event) => {
      const data = event.data ?? {};
      switch (event.type) {
        case "dev.scribe.transcription.task.started": {
          const annotationJson = typeof data.annotationJson === "string" ? data.annotationJson : "";
          const annotationId = typeof data.annotationId === "string" ? data.annotationId : "";
          applyJobUpdate({
            id: BigInt(data.jobId ?? 0),
            status: TranscriptionJobStatus.RUNNING,
            completedSegments: Number(data.completedSegments ?? 0),
            failedSegments: Number(data.failedSegments ?? 0),
            totalSegments: Number(data.totalSegments ?? 0),
            currentAnnotationId: annotationId,
            currentAnnotationJson: annotationJson,
            updatedAt: typeof event.time === "string" ? event.time : "",
          });
          break;
        }
        case "dev.scribe.transcription.task.completed": {
          const annotationJson = typeof data.annotationJson === "string" ? data.annotationJson : "";
          const completedSegments = Number(data.completedSegments ?? 0);
          const failedSegments = Number(data.failedSegments ?? 0);
          const totalSegments = Number(data.totalSegments ?? 0);
          applyJobUpdate({
            id: BigInt(data.jobId ?? 0),
            status: TranscriptionJobStatus.RUNNING,
            completedSegments,
            failedSegments,
            totalSegments,
            lastResultAnnotationJson: annotationJson,
            updatedAt: typeof event.time === "string" ? event.time : "",
          });
          break;
        }
        case "dev.scribe.transcription.completed":
          applyJobUpdate({
            id: BigInt(data.jobId ?? 0),
            status: TranscriptionJobStatus.COMPLETED,
            completedSegments: Number(data.completedSegments ?? 0),
            failedSegments: Number(data.failedSegments ?? 0),
            totalSegments: Number(data.totalSegments ?? 0),
            updatedAt: typeof event.time === "string" ? event.time : "",
          });
          break;
        case "dev.scribe.transcription.failed":
          applyJobUpdate({
            id: BigInt(data.jobId ?? 0),
            status: TranscriptionJobStatus.FAILED,
            completedSegments: Number(data.completedSegments ?? 0),
            failedSegments: Number(data.failedSegments ?? 0),
            totalSegments: Number(data.totalSegments ?? 0),
            updatedAt: typeof event.time === "string" ? event.time : "",
            errorMessage: typeof data.error === "string" ? data.error : "",
          });
          break;
        default:
          break;
      }
    },
    () => {
      if (!transcriptionStatus.textContent) {
        publishBatchState("Waiting for automatic transcription events...", true);
      }
    },
  );
  window.addEventListener("beforeunload", () => {
    eventSubscription.close();
  }, { once: true });
}
