import Mirador from "mirador";
import scribeMiradorPlugin, { annotationAdapters } from "mirador-scribe";
import { annotationClient, publishItemImageEdits } from "../api/annotations";
import { getOCRRun, reprocessItemImage } from "../api/processing";
import { listTranscriptionJobs } from "../api/transcription";
import { subscribeToEvents } from "../api/events";
import { scribePath } from "../api/http";
import { syncWorkspaceSelectionFromLocation, workspaceAwarePath } from "../lib/workspace";
import { TranscriptionJobStatus } from "../proto/scribe/v1/transcription_pb";
import { html, setHTML, uint64ToString } from "../lib/util";
import { renderEditorLayout } from "./editor/layout";
import { commonViewerOptions, hiddenPanels } from "./editor/mirador";
import { eventBigInt, eventNumber, isCompletedStatus, isFailedStatus, isPendingStatus, isRunningStatus } from "./editor/status";

export async function renderEditor(app: HTMLElement): Promise<void> {
  syncWorkspaceSelectionFromLocation();
  const params = new URLSearchParams(window.location.search);
  const itemImageID = params.get("itemImageId") ?? "";
  const itemID = params.get("itemId") ?? "";
  const autoTranscribe = params.get("autoTranscribe") === "1";
  const jobIdParam = params.get("jobId");
  let hasUnsavedChanges = false;
  let saveSequence = 0;
  let reprocessInFlight = false;
  let allowHistoryBack = false;
  let leaveAction: "home" | "history-back" = "home";

  const historySentinel = {
    ...(window.history.state ?? {}),
    __scribeEditorSentinel: true,
  };

  window.history.replaceState(historySentinel, "", window.location.href);
  window.history.pushState(historySentinel, "", window.location.href);

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
    window.location.href = workspaceAwarePath("/");
  }

  async function handleFullReprocess() {
    if (!itemImageID || reprocessInFlight) return;
    reprocessInFlight = true;
    reprocessNav.disabled = true;
    publishBatchState("Reprocessing page with fresh segmentation...", true);
    setBatchBanner(
      "Re-segmenting page",
      "Scribe is rebuilding page regions and restarting transcription for the new segments.",
      true,
    );
    try {
      await reprocessItemImage(itemImageID);
      document.dispatchEvent(new CustomEvent("scribe:reload-annotations", { detail: {} }));
      publishBatchState("Fresh segmentation complete. Automatic transcription is continuing on the new regions.", true);
    } catch (error) {
      const message = error instanceof Error ? error.message : "Reprocess failed.";
      publishBatchState(`Reprocess failed: ${message}`, false);
      setBatchBanner("", "", false);
    } finally {
      reprocessInFlight = false;
      reprocessNav.disabled = false;
    }
  }

  function leaveEditor() {
    closeLeaveDialog();
    if (leaveAction === "history-back") {
      allowHistoryBack = true;
      window.history.back();
      return;
    }
    navigateHome();
  }

  renderEditorLayout(app);

  const meta = document.getElementById("editor-meta") as HTMLParagraphElement;
  const transcriptionStatus = document.getElementById("editor-transcription-status") as HTMLParagraphElement;
  const batchBanner = document.getElementById("editor-batch-banner") as HTMLDivElement;
  const batchBannerTitle = document.getElementById("editor-batch-banner-title") as HTMLParagraphElement;
  const batchBannerDetail = document.getElementById("editor-batch-banner-detail") as HTMLParagraphElement;
  const homeNav = document.getElementById("home-nav") as HTMLButtonElement;
  const reprocessNav = document.getElementById("reprocess-nav") as HTMLButtonElement;
  const leaveDialog = document.getElementById("leave-dialog") as HTMLDivElement;
  const leaveCancel = document.getElementById("leave-cancel") as HTMLButtonElement;
  const leaveDiscard = document.getElementById("leave-discard") as HTMLButtonElement;
  const leaveSave = document.getElementById("leave-save") as HTMLButtonElement;
  const runtimeConfig = (window as Window & {
    __SCRIBE_RUNTIME_CONFIG?: Record<string, string | undefined>;
  }).__SCRIBE_RUNTIME_CONFIG;
  const annotationBase = runtimeConfig?.ANNOTATION_API_BASE
    || (import.meta as ImportMeta & { env?: Record<string, string | undefined> }).env?.VITE_ANNOTATION_API_BASE
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
  const viewerOptions = commonViewerOptions(annotationBase, ScribeAnnotationAdapter, annotationClient, osdConfig);
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
      publishBatchState(`Batch transcription is running. Automatic transcription progress: ${job.completedSegments}/${total}. You can keep editing while new text is applied.`, true);
      setBatchBanner(
        `Automatic transcription in progress: ${job.completedSegments}/${total}`,
        "Scribe is still writing text onto the page line by line. You can keep working in edit mode while those updates continue.",
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
    failedSegments?: number;
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
    leaveAction = "home";
    if (!hasUnsavedChanges) {
      navigateHome();
      return;
    }
    openLeaveDialog();
  }

  function handleHistoryBackNavigation() {
    leaveAction = "history-back";
    if (!hasUnsavedChanges) {
      allowHistoryBack = true;
      window.history.back();
      return;
    }
    window.history.pushState(historySentinel, "", window.location.href);
    openLeaveDialog();
  }

  homeNav.addEventListener("click", () => { void handleHomeNavigation(); });
  reprocessNav.addEventListener("click", () => { void handleFullReprocess(); });
  leaveCancel.addEventListener("click", closeLeaveDialog);
  leaveDiscard.addEventListener("click", leaveEditor);
  leaveSave.addEventListener("click", async () => {
    leaveSave.disabled = true;
    const ok = await requestSave();
    leaveSave.disabled = false;
    if (ok) leaveEditor();
  });

  const handleDirtyState = (event: Event) => {
    const detail = (event as CustomEvent<{ dirty: boolean }>).detail;
    hasUnsavedChanges = Boolean(detail?.dirty);
  };
  const handlePopState = () => {
    if (allowHistoryBack) {
      allowHistoryBack = false;
      return;
    }
    handleHistoryBackNavigation();
  };
  document.addEventListener("scribe:dirty-state", handleDirtyState);
  window.addEventListener("popstate", handlePopState);

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
    reprocessNav.classList.add("hidden");
    meta.textContent = "Open a IIIF manifest using the workspace panel (+ button)";
    Mirador.viewer({
      ...viewerOptions,
      windows: [],
      workspaceControlPanel: { enabled: true },
      window: {
        forceDrawAnnotations: true,
        panels: hiddenPanels,
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
  meta.textContent = itemID
    ? `item ${itemID} | image ${runItemImageID || "unknown"} | model ${runResp.model}`
    : `item image ${runItemImageID || "unknown"} | model ${runResp.model}`;

  if (!runResp.imageUrl || runResp.imageUrl.trim() === "") {
    const viewer = document.getElementById("mirador-viewer");
    if (viewer) {
      setHTML(viewer, html`<div class="flex h-full items-center justify-center text-sm text-muted-foreground">No image is available for this OCR run.</div>`);
    }
    return;
  }

  if (!runItemImageID) {
    meta.textContent = "Missing item image reference required for IIIF manifest route";
    return;
  }

  const manifestURL = itemID
    ? `${window.location.origin}${scribePath(`/v1/items/${encodeURIComponent(itemID)}/manifest`)}`
    : `${window.location.origin}${scribePath(`/v1/item-images/${encodeURIComponent(runItemImageID)}/manifest`)}`;

  publishBatchState("Loading editor and checking batch transcription status...", true);

  Mirador.viewer({
    ...viewerOptions,
    windows: [{ manifestId: manifestURL }],
    workspaceControlPanel: { enabled: false },
    window: {
      forceDrawAnnotations: true,
      allowClose: false,
      allowFullscreen: false,
      allowMaximize: false,
      allowTopMenuButton: false,
      hideWindowTitle: true,
      panels: hiddenPanels,
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
            id: eventBigInt(data.jobId),
            status: TranscriptionJobStatus.RUNNING,
            completedSegments: eventNumber(data.completedSegments),
            failedSegments: eventNumber(data.failedSegments),
            totalSegments: eventNumber(data.totalSegments),
            currentAnnotationId: annotationId,
            currentAnnotationJson: annotationJson,
            updatedAt: typeof event.time === "string" ? event.time : "",
          });
          break;
        }
        case "dev.scribe.transcription.task.completed": {
          const annotationJson = typeof data.annotationJson === "string" ? data.annotationJson : "";
          const completedSegments = eventNumber(data.completedSegments);
          const failedSegments = eventNumber(data.failedSegments);
          const totalSegments = eventNumber(data.totalSegments);
          applyJobUpdate({
            id: eventBigInt(data.jobId),
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
            id: eventBigInt(data.jobId),
            status: TranscriptionJobStatus.COMPLETED,
            completedSegments: eventNumber(data.completedSegments),
            failedSegments: eventNumber(data.failedSegments),
            totalSegments: eventNumber(data.totalSegments),
            updatedAt: typeof event.time === "string" ? event.time : "",
          });
          break;
        case "dev.scribe.transcription.failed":
          applyJobUpdate({
            id: eventBigInt(data.jobId),
            status: TranscriptionJobStatus.FAILED,
            completedSegments: eventNumber(data.completedSegments),
            failedSegments: eventNumber(data.failedSegments),
            totalSegments: eventNumber(data.totalSegments),
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
  window.addEventListener("pagehide", () => {
    document.removeEventListener("scribe:dirty-state", handleDirtyState);
    window.removeEventListener("popstate", handlePopState);
    eventSubscription.close();
  }, { once: true });
}
