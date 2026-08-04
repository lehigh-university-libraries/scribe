import Mirador from "mirador";
import { Code, ConnectError } from "@connectrpc/connect";
import scribeMiradorPlugin, {
  annotationAdapters,
  type ScribeAnnotationAdapterConstructor,
} from "mirador-scribe";
import {
  annotationClient,
  getAnnotationPage,
  publishItemImageEdits,
} from "../api/annotations";
import { getEditorManifest } from "../api/items";
import { getOCRRun, reprocessItemImage } from "../api/processing";
import {
  getTranscriptionJob,
  listTranscriptionJobs,
} from "../api/transcription";
import { subscribeToEvents } from "../api/events";
import {
  syncWorkspaceSelectionFromLocation,
  workspaceAwarePath,
} from "../lib/workspace";
import { TranscriptionJobStatus } from "../proto/scribe/v1/transcription_pb";
import { html, setHTML, uint64ToString } from "../lib/util";
import { renderEditorLayout } from "./editor/layout";
import { createLeaveDialogController } from "./editor/leave-dialog";
import { commonViewerOptions, hiddenPanels } from "./editor/mirador";
import { renderEditorRecovery } from "./editor/recovery";
import {
  type CanvasImageRegistry,
  createCanvasImageRegistry,
} from "./editor/canvas-image-registry";
import {
  eventBigInt,
  eventNumber,
  isCompletedStatus,
  isFailedStatus,
  isPendingStatus,
  isRunningStatus,
} from "./editor/status";

const EDITOR_WINDOW_ID = "scribe-editor-window";

export async function renderEditor(app: HTMLElement): Promise<void> {
  syncWorkspaceSelectionFromLocation();
  const params = new URLSearchParams(window.location.search);
  const itemImageID = params.get("itemImageId") ?? "";
  const itemID = params.get("itemId") ?? "";
  const autoTranscribe = params.get("autoTranscribe") === "1";
  const jobIdParam = params.get("jobId");
  const requestedJobID = (() => {
    if (
      jobIdParam === null
      || jobIdParam.length > 20
      || !/^[1-9][0-9]*$/u.test(jobIdParam)
    ) return null;
    const parsed = BigInt(jobIdParam);
    return parsed <= 18446744073709551615n ? parsed : null;
  })();
  const invalidJobID = jobIdParam !== null && requestedJobID === null;
  const dirtyWindows = new Map<string, boolean>();
  let beforeUnloadRegistered = false;
  let processingContextID = params.get("contextId") ?? "0";
  let activeItemImageID = itemImageID;
  let activeCanvasID = "";
  let activeWindowID = EDITOR_WINDOW_ID;
  const processingContexts = new Map<string, string>();
  let saveSequence = 0;
  let reprocessInFlight = false;
  let allowHistoryBack = false;
  let leaveAction: "home" | "history-back" = "home";
  let eventSubscription: { close: () => void } | null = null;
  let monitoredItemImageID = "";
  let monitoredJobReconcile: (() => Promise<void>) | null = null;
  let activationSequence = 0;
  let canvasImageRegistry: CanvasImageRegistry | null = null;
  let editorManifestObjectURL = "";
  const remoteRebaseReady = new Set<string>();
  const pendingCompletedJobs = new Map<string, string>();

  const handleBeforeUnload = (event: BeforeUnloadEvent) => {
    event.preventDefault();
    event.returnValue = "";
  };

  function hasDirtyWindows(): boolean {
    return [...dirtyWindows.values()].some(Boolean);
  }

  function syncBeforeUnloadGuard(): void {
    const shouldGuard = hasDirtyWindows();
    if (shouldGuard === beforeUnloadRegistered) return;
    if (shouldGuard) {
      window.addEventListener("beforeunload", handleBeforeUnload);
    } else {
      window.removeEventListener("beforeunload", handleBeforeUnload);
    }
    beforeUnloadRegistered = shouldGuard;
  }

  function clearDirtyNavigationGuard(): void {
    dirtyWindows.clear();
    syncBeforeUnloadGuard();
  }

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
      const targetWindowID = activeWindowID;
      let settled = false;
      const finish = (ok: boolean) => {
        if (settled) return;
        settled = true;
        window.clearTimeout(timeoutID);
        document.removeEventListener("scribe:save-result", handleResult);
        resolve(ok);
      };

      const handleResult = (event: Event) => {
        const detail = (
          event as CustomEvent<{
            ok: boolean;
            requestId: string;
            windowId: string;
          }>
        ).detail;
        if (
          !detail ||
          detail.requestId !== requestId ||
          detail.windowId !== targetWindowID
        )
          return;
        finish(Boolean(detail.ok));
      };
      const timeoutID = window.setTimeout(() => finish(false), 30_000);

      document.addEventListener("scribe:save-result", handleResult);
      document.dispatchEvent(
        new CustomEvent("scribe:request-save", {
          detail: {
            canvasId: activeCanvasID,
            requestId,
            windowId: targetWindowID,
          },
        }),
      );
    });
  }

  async function processingContextForItemImage(
    targetItemImageID: string,
  ): Promise<string> {
    const cached = processingContexts.get(targetItemImageID);
    if (cached !== undefined) return cached;
    try {
      const run = await getOCRRun(targetItemImageID);
      const returnedItemImageID = uint64ToString(run.itemImageId);
      if (returnedItemImageID !== targetItemImageID) {
        throw new Error("The OCR run belongs to a different item image.");
      }
      const contextID = run.contextId.toString();
      processingContexts.set(targetItemImageID, contextID);
      return contextID;
    } catch (runError) {
      if (ConnectError.from(runError).code !== Code.NotFound) throw runError;
      const jobs = await listTranscriptionJobs(BigInt(targetItemImageID));
      const contextID = jobs[0]?.contextId?.toString() ?? "0";
      processingContexts.set(targetItemImageID, contextID);
      return contextID;
    }
  }

  function navigateHome() {
    window.location.href = workspaceAwarePath("/");
  }

  async function handleFullReprocess() {
    if (!activeItemImageID || reprocessInFlight) return;
    const targetItemImageID = activeItemImageID;
    const targetCanvasID = activeCanvasID;
    const targetWindowID = activeWindowID;
    reprocessInFlight = true;
    reprocessNav.disabled = true;
    publishBatchState("Preparing the canonical page for reprocessing...", true);
    setBatchBanner(
      "Preparing to re-segment page",
      "Scribe will save pending edits, then reprocess the exact committed annotation revision.",
      true,
    );
    try {
      if ([...dirtyWindows.values()].some(Boolean) && !(await requestSave())) {
        throw new Error(
          "Pending edits could not be saved, so reprocessing was not started.",
        );
      }

      const targetContextID =
        await processingContextForItemImage(targetItemImageID);
      const canonicalPage = await getAnnotationPage(targetItemImageID);
      publishBatchState("Reprocessing page with fresh segmentation...", true);
      setBatchBanner(
        "Re-segmenting page",
        "Scribe is rebuilding page regions and restarting transcription for the new segments.",
        true,
      );
      await reprocessItemImage(
        targetItemImageID,
        targetContextID,
        canonicalPage.revision,
      );
      document.dispatchEvent(
        new CustomEvent("scribe:reload-annotations", {
          detail: {
            canvasId: targetCanvasID,
            itemImageId: targetItemImageID,
            windowId: targetWindowID,
          },
        }),
      );
      publishBatchState(
        "Fresh segmentation complete. Automatic transcription is continuing on the new regions.",
        true,
      );
    } catch (error) {
      const message =
        error instanceof Error ? error.message : "Reprocess failed.";
      publishBatchState(`Reprocess failed: ${message}`, false);
      setBatchBanner("", "", false);
    } finally {
      reprocessInFlight = false;
      reprocessNav.disabled = false;
    }
  }

  function leaveEditor() {
    // This is reached only after the user explicitly saves or discards. Remove
    // the native unload guard immediately before the requested navigation so a
    // second browser prompt cannot obscure the user's confirmed choice.
    clearDirtyNavigationGuard();
    leaveDialogController.close();
    if (leaveAction === "history-back") {
      allowHistoryBack = true;
      // A dirty back navigation has already moved across the top sentinel and
      // pushed a replacement guard entry. Skip both editor entries after the
      // user confirms; one `back()` would only redisplay the editor.
      window.history.go(-2);
      return;
    }
    navigateHome();
  }

  renderEditorLayout(app);

  const meta = document.getElementById("editor-meta") as HTMLParagraphElement;
  const transcriptionStatus = document.getElementById(
    "editor-transcription-status",
  ) as HTMLParagraphElement;
  const batchBanner = document.getElementById(
    "editor-batch-banner",
  ) as HTMLDivElement;
  const batchBannerTitle = document.getElementById(
    "editor-batch-banner-title",
  ) as HTMLParagraphElement;
  const batchBannerDetail = document.getElementById(
    "editor-batch-banner-detail",
  ) as HTMLParagraphElement;
  const homeNav = document.getElementById("home-nav") as HTMLButtonElement;
  const brandNav = document.getElementById("brand-nav") as HTMLAnchorElement;
  const reprocessNav = document.getElementById(
    "reprocess-nav",
  ) as HTMLButtonElement;
  const leaveDialog = document.getElementById("leave-dialog") as HTMLDivElement;
  const leaveCancel = document.getElementById(
    "leave-cancel",
  ) as HTMLButtonElement;
  const leaveDiscard = document.getElementById(
    "leave-discard",
  ) as HTMLButtonElement;
  const leaveSave = document.getElementById("leave-save") as HTMLButtonElement;
  const leaveDialogController = createLeaveDialogController({
    cancel: leaveCancel,
    dialog: leaveDialog,
    discard: leaveDiscard,
    onDiscard: leaveEditor,
    onSave: async () => {
      if (await requestSave()) leaveEditor();
    },
    save: leaveSave,
  });
  const runtimeConfig = (
    window as Window & {
      __SCRIBE_RUNTIME_CONFIG?: Record<string, string | undefined>;
    }
  ).__SCRIBE_RUNTIME_CONFIG;
  const annotationBase =
    runtimeConfig?.ANNOTATION_API_BASE ||
    (import.meta as ImportMeta & { env?: Record<string, string | undefined> })
      .env?.VITE_ANNOTATION_API_BASE ||
    window.location.origin;
  const ScribeAnnotationAdapter: ScribeAnnotationAdapterConstructor =
    annotationAdapters.ScribeAnnotationAdapter;
  const osdConfig = {
    crossOriginPolicy: "Anonymous",
    ajaxWithCredentials: false,
  };
  let lastSegmentKey = "";
  let lastResultKey = "";
  const reloadedCompletedJobs = new Set<string>();

  function remoteRebaseIdentity(
    targetItemImageID: string,
    targetCanvasID: string,
    targetWindowID: string,
  ): string {
    return `${targetWindowID}\u0000${targetCanvasID}\u0000${targetItemImageID}`;
  }

  function reloadCompletedJob(
    jobID: string,
    targetItemImageID: string,
    targetCanvasID: string,
    targetWindowID: string,
  ): void {
    const identity = remoteRebaseIdentity(
      targetItemImageID,
      targetCanvasID,
      targetWindowID,
    );
    const completionKey = `${identity}\u0000${jobID}`;
    if (reloadedCompletedJobs.has(completionKey)) return;
    if (!remoteRebaseReady.has(identity)) {
      pendingCompletedJobs.set(identity, jobID);
      return;
    }
    pendingCompletedJobs.delete(identity);
    reloadedCompletedJobs.add(completionKey);
    document.dispatchEvent(
      new CustomEvent("scribe:reload-annotations", {
        detail: {
          canvasId: targetCanvasID,
          itemImageId: targetItemImageID,
          windowId: targetWindowID,
        },
      }),
    );
  }

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
    document.dispatchEvent(
      new CustomEvent("scribe:transcription-job-state", {
        detail: {
          active,
          canvasId: activeCanvasID,
          itemImageId: activeItemImageID,
          message,
          windowId: activeWindowID,
        },
      }),
    );
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
      publishBatchState(
        `Batch transcription is running. Automatic transcription progress: ${job.completedSegments}/${total}. You can keep editing while new text is applied.`,
        true,
      );
      setBatchBanner(
        `Automatic transcription in progress: ${job.completedSegments}/${total}`,
        "Scribe is still writing text onto the page line by line. You can keep working in edit mode while those updates continue.",
        true,
      );
      return;
    }
    if (isPendingStatus(job.status)) {
      publishBatchState(
        "Preparing batch transcription. Layout is ready; text is being generated line by line.",
        true,
      );
      setBatchBanner(
        "Automatic transcription is starting",
        "The page structure is ready. Scribe is starting line-by-line transcription and the first results will appear here shortly.",
        true,
      );
      return;
    }
    if (isFailedStatus(job.status)) {
      publishBatchState(
        job.errorMessage?.trim()
          ? `Batch transcription failed: ${job.errorMessage}`
          : "Batch transcription failed.",
        false,
      );
      setBatchBanner("", "", false);
      return;
    }
    if (isCompletedStatus(job.status)) {
      publishBatchState(
        "Batch transcription complete. Updated text is now available in the editor.",
        false,
      );
      setBatchBanner("", "", false);
      return;
    }
    publishBatchState("", false);
    setBatchBanner("", "", false);
  }

  function applyJobUpdate(
    job: {
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
    },
    targetItemImageID: string,
  ) {
    if (targetItemImageID !== activeItemImageID) return;
    renderJobStatus(job);

    if (job.currentAnnotationJson) {
      const segmentKey = `${job.id.toString()}:${job.currentAnnotationId ?? ""}:${job.updatedAt ?? ""}:${job.completedSegments}/${job.totalSegments}`;
      if (segmentKey !== lastSegmentKey) {
        lastSegmentKey = segmentKey;
        try {
          const anno = JSON.parse(job.currentAnnotationJson);
          document.dispatchEvent(
            new CustomEvent("scribe:transcription-segment", {
              detail: {
                annotation: anno,
                canvasId: activeCanvasID,
                done: job.completedSegments,
                itemImageId: targetItemImageID,
                total: job.totalSegments,
                windowId: activeWindowID,
              },
            }),
          );
        } catch {
          /* ignore */
        }
      }
    }

    if (job.lastResultAnnotationJson) {
      const resultKey = `${job.id.toString()}:${job.updatedAt ?? ""}:${job.completedSegments}/${job.totalSegments}`;
      if (resultKey !== lastResultKey) {
        lastResultKey = resultKey;
        try {
          const anno = JSON.parse(job.lastResultAnnotationJson);
          document.dispatchEvent(
            new CustomEvent("scribe:transcription-result", {
              detail: {
                annotation: anno,
                canvasId: activeCanvasID,
                done: job.completedSegments,
                itemImageId: targetItemImageID,
                // Per-segment results are progress previews. The canonical page is
                // committed atomically only when the entire job completes.
                persisted: false,
                total: job.totalSegments,
                windowId: activeWindowID,
              },
            }),
          );
        } catch {
          /* ignore */
        }
      }
    }

    if (isCompletedStatus(job.status) || isFailedStatus(job.status)) {
      document.dispatchEvent(
        new CustomEvent("scribe:transcription-segment", {
          detail: {
            annotation: null,
            canvasId: activeCanvasID,
            itemImageId: targetItemImageID,
            windowId: activeWindowID,
          },
        }),
      );
    }

    if (isCompletedStatus(job.status)) {
      const jobID = job.id.toString();
      reloadCompletedJob(
        jobID,
        targetItemImageID,
        activeCanvasID,
        activeWindowID,
      );
    }
  }

  function subscribeToItemImageEvents(
    targetItemImageID: string,
    reconcile: () => void,
  ) {
    return subscribeToEvents(
      {
        itemImageId: targetItemImageID,
        onReady: reconcile,
        types: [
          "dev.scribe.transcription.task.started",
          "dev.scribe.transcription.task.completed",
          "dev.scribe.transcription.completed",
          "dev.scribe.transcription.failed",
        ],
      },
      (event) => {
        const data = event.data ?? {};
        const eventJobID = eventBigInt(data.jobId);
        if (
          requestedJobID !== null &&
          targetItemImageID === itemImageID &&
          eventJobID !== requestedJobID
        ) {
          return;
        }
        switch (event.type) {
          case "dev.scribe.transcription.task.started":
            applyJobUpdate(
              {
                id: eventBigInt(data.jobId),
                status: TranscriptionJobStatus.RUNNING,
                completedSegments: eventNumber(data.completedSegments),
                failedSegments: eventNumber(data.failedSegments),
                totalSegments: eventNumber(data.totalSegments),
                currentAnnotationId:
                  typeof data.annotationId === "string"
                    ? data.annotationId
                    : "",
                currentAnnotationJson:
                  typeof data.annotationJson === "string"
                    ? data.annotationJson
                    : "",
                updatedAt: typeof event.time === "string" ? event.time : "",
              },
              targetItemImageID,
            );
            break;
          case "dev.scribe.transcription.task.completed":
            applyJobUpdate(
              {
                id: eventBigInt(data.jobId),
                status: TranscriptionJobStatus.RUNNING,
                completedSegments: eventNumber(data.completedSegments),
                failedSegments: eventNumber(data.failedSegments),
                totalSegments: eventNumber(data.totalSegments),
                lastResultAnnotationJson:
                  typeof data.annotationJson === "string"
                    ? data.annotationJson
                    : "",
                updatedAt: typeof event.time === "string" ? event.time : "",
              },
              targetItemImageID,
            );
            break;
          case "dev.scribe.transcription.completed":
            applyJobUpdate(
              {
                id: eventBigInt(data.jobId),
                status: TranscriptionJobStatus.COMPLETED,
                completedSegments: eventNumber(data.completedSegments),
                failedSegments: eventNumber(data.failedSegments),
                totalSegments: eventNumber(data.totalSegments),
                updatedAt: typeof event.time === "string" ? event.time : "",
              },
              targetItemImageID,
            );
            break;
          case "dev.scribe.transcription.failed":
            applyJobUpdate(
              {
                id: eventBigInt(data.jobId),
                status: TranscriptionJobStatus.FAILED,
                completedSegments: eventNumber(data.completedSegments),
                failedSegments: eventNumber(data.failedSegments),
                totalSegments: eventNumber(data.totalSegments),
                updatedAt: typeof event.time === "string" ? event.time : "",
                errorMessage: typeof data.error === "string" ? data.error : "",
              },
              targetItemImageID,
            );
            break;
          default:
            break;
        }
        if (
          event.type === "dev.scribe.transcription.completed" ||
          event.type === "dev.scribe.transcription.failed"
        ) {
          reconcile();
        }
      },
      () => {
        reconcile();
        if (
          activeItemImageID === targetItemImageID &&
          !transcriptionStatus.textContent
        ) {
          publishBatchState(
            "Waiting for automatic transcription events...",
            true,
          );
        }
      },
    );
  }

  async function activateItemImage(
    targetItemImageID: string,
    knownRun?: Awaited<ReturnType<typeof getOCRRun>>,
    knownJob?: Awaited<ReturnType<typeof getTranscriptionJob>>,
  ): Promise<void> {
    if (!targetItemImageID) return;
    if (targetItemImageID === monitoredItemImageID && eventSubscription) return;
    const sequence = ++activationSequence;
    activeItemImageID = targetItemImageID;
    lastSegmentKey = "";
    lastResultKey = "";
    eventSubscription?.close();
    eventSubscription = null;
    monitoredItemImageID = "";
    monitoredJobReconcile = null;
    publishBatchState(
      "Loading editor annotations and transcription status...",
      true,
    );

    try {
      const run = knownRun ?? (await getOCRRun(targetItemImageID));
      if (sequence !== activationSequence) return;
      const returnedItemImageID = uint64ToString(run.itemImageId);
      if (returnedItemImageID !== targetItemImageID) {
        throw new Error("The OCR run belongs to a different item image.");
      }
      processingContextID = run.contextId.toString();
      processingContexts.set(targetItemImageID, processingContextID);
      meta.textContent = itemID
        ? `item ${itemID} | image ${targetItemImageID} | model ${run.model}`
        : `item image ${targetItemImageID} | model ${run.model}`;

      let reconciliation = Promise.resolve();
      let initialKnownJob:
        | Awaited<ReturnType<typeof getTranscriptionJob>>
        | Awaited<ReturnType<typeof listTranscriptionJobs>>[number]
        | undefined = knownJob;
      const reconcile = () => {
        reconciliation = reconciliation
          .catch(() => undefined)
          .then(async () => {
            let latest = initialKnownJob;
            initialKnownJob = undefined;
            if (!latest && requestedJobID !== null && targetItemImageID === itemImageID) {
              latest = await getTranscriptionJob(requestedJobID);
              if (
                latest.id !== requestedJobID ||
                uint64ToString(latest.itemImageId) !== targetItemImageID
              ) {
                throw new Error("The transcription job belongs to a different item image.");
              }
            }
            if (!latest) {
              const jobs = await listTranscriptionJobs(BigInt(targetItemImageID));
              latest = jobs[0];
            }
            if (sequence !== activationSequence) return;
            if (latest) {
              applyJobUpdate(latest, targetItemImageID);
            } else {
              publishBatchState(
                "Loading editor annotations. No active batch transcription job detected yet.",
                false,
              );
            }
          });
        return reconciliation;
      };
      monitoredJobReconcile = reconcile;
      const reconcileAfterStreamSignal = () => {
        void reconcile().catch(() => {
          if (sequence === activationSequence) {
            publishBatchState(
              "Failed to refresh transcription status; the event stream will retry.",
              true,
            );
          }
        });
      };
      eventSubscription = subscribeToItemImageEvents(
        targetItemImageID,
        reconcileAfterStreamSignal,
      );
      monitoredItemImageID = targetItemImageID;
      await reconcile();
    } catch (error) {
      if (sequence !== activationSequence) return;
      publishBatchState(
        error instanceof Error
          ? error.message
          : "Failed to load item image state.",
        false,
      );
    }
  }

  async function handleHomeNavigation() {
    leaveAction = "home";
    if (!hasDirtyWindows()) {
      navigateHome();
      return;
    }
    leaveDialogController.open();
  }

  function handleHistoryBackNavigation() {
    leaveAction = "history-back";
    if (!hasDirtyWindows()) {
      allowHistoryBack = true;
      window.history.back();
      return;
    }
    window.history.pushState(historySentinel, "", window.location.href);
    leaveDialogController.open();
  }

  const handleHomeNavigationClick = (event: Event) => {
    event.preventDefault();
    void handleHomeNavigation();
  };
  const handleReprocessClick = () => {
    void handleFullReprocess();
  };
  brandNav.addEventListener("click", handleHomeNavigationClick);
  homeNav.addEventListener("click", handleHomeNavigationClick);
  reprocessNav.addEventListener("click", handleReprocessClick);
  const handleDirtyState = (event: Event) => {
    const detail = (event as CustomEvent<{ dirty: boolean; windowId: string }>)
      .detail;
    const windowID = detail?.windowId?.trim() ?? "";
    if (!windowID) return;
    if (detail.dirty) dirtyWindows.set(windowID, true);
    else dirtyWindows.delete(windowID);
    syncBeforeUnloadGuard();
  };
  const handleActiveCanvas = (event: Event) => {
    const detail = (
      event as CustomEvent<{
        canvasId: string;
        itemImageId: string;
        windowId: string;
      }>
    ).detail;
    const canvasID = detail?.canvasId?.trim() ?? "";
    const windowID = detail?.windowId?.trim() ?? "";
    if (!canvasID || !windowID || !canvasImageRegistry) return;
    try {
      const targetItemImageID =
        canvasImageRegistry.itemImageIdForCanvas(canvasID);
      if (detail.itemImageId?.trim() !== targetItemImageID) {
        throw new Error("The focused Canvas item-image identity does not match this item.");
      }
      activeCanvasID = canvasID;
      activeWindowID = windowID;
      const route = new URL(window.location.href);
      route.searchParams.set("itemImageId", targetItemImageID);
      window.history.replaceState(window.history.state, "", route);
      void activateItemImage(targetItemImageID);
    } catch (error) {
      publishBatchState(
        error instanceof Error
          ? error.message
          : "The focused Canvas is not part of this item.",
        false,
      );
    }
  };
  const handleRemoteRebaseReady = (event: Event) => {
    const detail = (
      event as CustomEvent<{
        canvasId: string;
        itemImageId: string;
        windowId: string;
      }>
    ).detail;
    const canvasID = detail?.canvasId?.trim() ?? "";
    const targetItemImageID = detail?.itemImageId?.trim() ?? "";
    const windowID = detail?.windowId?.trim() ?? "";
    if (!canvasID || !targetItemImageID || !windowID || !canvasImageRegistry) {
      return;
    }
    try {
      if (
        canvasImageRegistry.itemImageIdForCanvas(canvasID) !==
        targetItemImageID
      ) {
        return;
      }
    } catch {
      return;
    }
    const identity = remoteRebaseIdentity(
      targetItemImageID,
      canvasID,
      windowID,
    );
    const wasReady = remoteRebaseReady.has(identity);
    remoteRebaseReady.add(identity);
    const pendingJobID = pendingCompletedJobs.get(identity);
    if (pendingJobID) {
      reloadCompletedJob(
        pendingJobID,
        targetItemImageID,
        canvasID,
        windowID,
      );
    }
    if (
      !wasReady &&
      targetItemImageID === monitoredItemImageID &&
      monitoredJobReconcile
    ) {
      void monitoredJobReconcile().catch(() => {
        publishBatchState(
          "Failed to refresh transcription status; the event stream will retry.",
          true,
        );
      });
    }
  };
  const handlePopState = () => {
    if (allowHistoryBack) {
      allowHistoryBack = false;
      return;
    }
    handleHistoryBackNavigation();
  };
  document.addEventListener("scribe:dirty-state", handleDirtyState);
  document.addEventListener("scribe:active-canvas", handleActiveCanvas);
  document.addEventListener(
    "scribe:remote-rebase-ready",
    handleRemoteRebaseReady,
  );
  window.addEventListener("popstate", handlePopState);

  const handlePublishRequest = async (event: Event) => {
    const detail = (
      event as CustomEvent<{
        canvasId: string;
        itemImageId: string;
        expectedRevision: string;
        requestId: string;
        windowId: string;
      }>
    ).detail;
    if (
      !detail?.canvasId ||
      !detail?.itemImageId ||
      !detail?.expectedRevision ||
      !detail?.requestId ||
      !detail?.windowId ||
      !canvasImageRegistry
    )
      return;
    let ok = false;
    let result: Awaited<ReturnType<typeof publishItemImageEdits>> | undefined;
    try {
      if (
        canvasImageRegistry.itemImageIdForCanvas(detail.canvasId) !==
        detail.itemImageId
      ) {
        throw new Error("Publish identity does not match the selected Canvas.");
      }
      result = await publishItemImageEdits(
        detail.itemImageId,
        detail.expectedRevision,
      );
      ok = true;
    } catch {
      ok = false;
    }
    document.dispatchEvent(
      new CustomEvent("scribe:publish-result", {
        detail: {
          ok,
          canvasId: detail.canvasId,
          publicUrl: result?.publicUrl,
          publishedRevision: result?.publishedRevision,
          requestId: detail.requestId,
          windowId: detail.windowId,
        },
      }),
    );
  };
  document.addEventListener("scribe:request-publish", handlePublishRequest);

  window.addEventListener(
    "pagehide",
    () => {
      document.removeEventListener("scribe:dirty-state", handleDirtyState);
      document.removeEventListener("scribe:active-canvas", handleActiveCanvas);
      document.removeEventListener(
        "scribe:remote-rebase-ready",
        handleRemoteRebaseReady,
      );
      document.removeEventListener(
        "scribe:request-publish",
        handlePublishRequest,
      );
      leaveDialogController.destroy();
      brandNav.removeEventListener("click", handleHomeNavigationClick);
      homeNav.removeEventListener("click", handleHomeNavigationClick);
      reprocessNav.removeEventListener("click", handleReprocessClick);
      window.removeEventListener("popstate", handlePopState);
      window.removeEventListener("beforeunload", handleBeforeUnload);
      beforeUnloadRegistered = false;
      eventSubscription?.close();
      if (editorManifestObjectURL) {
        URL.revokeObjectURL(editorManifestObjectURL);
        editorManifestObjectURL = "";
      }
    },
    { once: true },
  );

  // Importing a manifest is an explicit authenticated operation. Read-only
  // annotation loading must never create backend resources as a side effect.
  if (itemImageID === "") {
    reprocessNav.classList.add("hidden");
    renderEditorRecovery(meta, document.getElementById("mirador-viewer"), {
      message: "This editor link is missing the required itemImageId. Select or import an item, then open its editor again.",
    });
    return;
  }
  if (invalidJobID) {
    reprocessNav.classList.add("hidden");
    renderEditorRecovery(meta, document.getElementById("mirador-viewer"), {
      message: "This editor link has an invalid jobId. Open the item from the library and try again.",
    });
    return;
  }

  let runResp: Awaited<ReturnType<typeof getOCRRun>> | null = null;
  try {
    runResp = await getOCRRun(itemImageID);
  } catch {
    runResp = null;
  }
  if (runResp == null) {
    renderEditorRecovery(meta, document.getElementById("mirador-viewer"), {
      message: "Failed to load the OCR run. The link may be stale, or the service may be temporarily unavailable.",
      retry: () => window.location.reload(),
    });
    return;
  }

  const runItemImageID = uint64ToString(runResp.itemImageId);
  if (runItemImageID !== itemImageID) {
    reprocessNav.classList.add("hidden");
    renderEditorRecovery(meta, document.getElementById("mirador-viewer"), {
      message: "The OCR run belongs to a different item image.",
    });
    return;
  }
  let requestedJob: Awaited<ReturnType<typeof getTranscriptionJob>> | undefined;
  if (requestedJobID !== null) {
    try {
      requestedJob = await getTranscriptionJob(requestedJobID);
    } catch {
      reprocessNav.classList.add("hidden");
      renderEditorRecovery(meta, document.getElementById("mirador-viewer"), {
        message: "Failed to load the transcription job. The link may be stale, or the service may be temporarily unavailable.",
        retry: () => window.location.reload(),
      });
      return;
    }
    if (
      requestedJob.id !== requestedJobID ||
      uint64ToString(requestedJob.itemImageId) !== runItemImageID
    ) {
      reprocessNav.classList.add("hidden");
      renderEditorRecovery(meta, document.getElementById("mirador-viewer"), {
        message: "The transcription job belongs to a different item image.",
      });
      return;
    }
  }
  processingContextID = runResp.contextId.toString() || processingContextID;
  processingContexts.set(runItemImageID, processingContextID);
  meta.textContent = itemID
    ? `item ${itemID} | image ${runItemImageID || "unknown"} | model ${runResp.model}`
    : `item image ${runItemImageID || "unknown"} | model ${runResp.model}`;

  if (!runResp.imageUrl || runResp.imageUrl.trim() === "") {
    const viewer = document.getElementById("mirador-viewer");
    if (viewer) {
      setHTML(
        viewer,
        html`<div
          class="flex h-full items-center justify-center text-sm text-muted-foreground"
        >
          No image is available for this OCR run.
        </div>`,
      );
    }
    return;
  }

  if (!runItemImageID) {
    meta.textContent =
      "Missing item image reference required for IIIF manifest route";
    return;
  }
  let manifestURL = "";
  try {
    const editorManifest = await getEditorManifest(runItemImageID);
    if (itemID && editorManifest.item.id !== itemID) {
      throw new Error(
        "The loaded item identity does not match the editor route.",
      );
    }
    canvasImageRegistry = createCanvasImageRegistry(editorManifest.item.images);
    if (!canvasImageRegistry.hasItemImageId(runItemImageID)) {
      throw new Error("The selected item image is not part of this item.");
    }
    activeCanvasID = canvasImageRegistry.canvasIdForItemImage(runItemImageID);
    if (activeCanvasID !== editorManifest.selectedCanvasId) {
      throw new Error("The editor manifest selected a different Canvas.");
    }
    editorManifestObjectURL = URL.createObjectURL(new Blob(
      [editorManifest.manifestJSON],
      { type: "application/ld+json" },
    ));
    manifestURL = editorManifestObjectURL;
  } catch (error) {
    meta.textContent =
      error instanceof Error ? error.message : "Failed to map item Canvases.";
    return;
  }

  publishBatchState(
    "Loading editor and checking batch transcription status...",
    true,
  );

  const viewerOptions = commonViewerOptions(
    annotationBase,
    ScribeAnnotationAdapter,
    annotationClient,
    (canvasID) => {
      if (!canvasImageRegistry)
        throw new Error("Canvas registry is not initialized");
      const mappedItemImageID =
        canvasImageRegistry.itemImageIdForCanvas(canvasID);
      return {
        contextId: processingContexts.get(mappedItemImageID) ?? "0",
        itemImageId: mappedItemImageID,
        windowId: EDITOR_WINDOW_ID,
        resolveContextId: () =>
          processingContextForItemImage(mappedItemImageID),
      };
    },
    osdConfig,
  );

  Mirador.viewer(
    {
      ...viewerOptions,
      windows: [{
        canvasId: activeCanvasID || undefined,
        id: EDITOR_WINDOW_ID,
        manifestId: manifestURL,
      }],
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
    },
    [...scribeMiradorPlugin],
  );

  await activateItemImage(runItemImageID, runResp, requestedJob);

  if (!jobIdParam && autoTranscribe) {
    // Legacy path: client-side segment-by-segment transcription via the magic wand.
    setTimeout(() => {
      document.dispatchEvent(
        new CustomEvent("scribe:request-transcribe-all", {
          detail: { canvasId: activeCanvasID, windowId: activeWindowID },
        }),
      );
    }, 3000);
  }
}
