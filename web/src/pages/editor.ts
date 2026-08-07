import Mirador, { updateWindow } from "mirador";
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
import type { AnnotationPageSnapshot } from "../api/annotations";
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
import {
  TranscriptionJobAttemptOutcome,
  TranscriptionJobStatus,
} from "../proto/scribe/v1/transcription_pb";
import { html, setHTML, uint64ToString } from "../lib/util";
import { renderEditorLayout } from "./editor/layout";
import { createLeaveDialogController } from "./editor/leave-dialog";
import {
  bottomPaneHeightForViewport,
  commonViewerOptions,
  hiddenPanels,
  observeResponsiveBottomPane,
} from "./editor/mirador";
import { renderEditorRecovery } from "./editor/recovery";
import {
  type CanvasImageRegistry,
  createCanvasImageRegistry,
} from "./editor/canvas-image-registry";
import {
  eventBigInt,
  eventNumber,
  isCanceledStatus,
  isCompletedStatus,
  isFailedStatus,
  isPendingStatus,
  isRunningStatus,
} from "./editor/status";

const EDITOR_WINDOW_ID = "scribe-editor-window";
const COMPLETED_REPLAY_MAX_DURATION_MS = 5_000;
const COMPLETED_REPLAY_MAX_SEGMENTS = 500;
const COMPLETED_REPLAY_MAX_LINE_DELAY_MS = 400;
const COMPLETED_REPLAY_MIN_LINE_DELAY_MS = 10;
const COMPLETED_RELOAD_ACK_TIMEOUT_MS = 15_000;

interface EditorTranscriptionJob {
  id: bigint;
  itemImageId?: bigint;
  status: TranscriptionJobStatus | string | number;
  attemptCount?: number;
  completedSegments: number;
  totalSegments: number;
  failedSegments?: number;
  currentAnnotationId?: string;
  currentAnnotationJson?: string;
  lastResultAnnotationJson?: string;
  updatedAt?: string;
  errorMessage?: string;
  attempts?: Array<{
    attemptNumber?: number;
    jobId: bigint;
    outcome: TranscriptionJobAttemptOutcome | number;
    resultRevision: bigint;
  }>;
}

interface ReplayAnnotation extends Record<string, unknown> {
  id: string;
}

function annotationHasReplayText(annotation: ReplayAnnotation): boolean {
  const bodies = Array.isArray(annotation.body)
    ? annotation.body
    : [annotation.body];
  return bodies.some((body) => {
    if (typeof body === "string") return body.trim() !== "";
    if (body === null || typeof body !== "object" || Array.isArray(body)) {
      return false;
    }
    const value = (body as Record<string, unknown>).value;
    return typeof value === "string" && value.trim() !== "";
  });
}

function exactCompletedAttempt(
  job: EditorTranscriptionJob,
): { attemptNumber: number; resultRevision: string } | null {
  const successfulAttempts = (job.attempts ?? []).filter((attempt) =>
    attempt.jobId === job.id
    && attempt.outcome === TranscriptionJobAttemptOutcome.COMPLETED
    && Number.isSafeInteger(attempt.attemptNumber)
    && (attempt.attemptNumber ?? 0) > 0
    && attempt.resultRevision > 0n
  );
  if (successfulAttempts.length !== 1) return null;
  return {
    attemptNumber: successfulAttempts[0].attemptNumber ?? 0,
    resultRevision: successfulAttempts[0].resultRevision.toString(),
  };
}

function transcriptionAttemptNumber(job: EditorTranscriptionJob): number {
  return Number.isSafeInteger(job.attemptCount) && (job.attemptCount ?? 0) > 0
    ? job.attemptCount ?? 0
    : 0;
}

function canonicalReplayLines(
  snapshot: AnnotationPageSnapshot,
): ReplayAnnotation[] {
  const lines = snapshot.page.items.filter((annotation) =>
    typeof annotation.textGranularity === "string"
    && annotation.textGranularity.trim().toLowerCase() === "line"
  );
  if (!lines.every((annotation): annotation is ReplayAnnotation =>
    typeof annotation.id === "string"
    && annotation.id.trim() !== ""
    && annotation.type === "Annotation"
    && annotationHasReplayText(annotation)
  )) return [];
  return lines;
}

function completedReplayLineDelay(totalSegments: number): number {
  return Math.min(
    COMPLETED_REPLAY_MAX_LINE_DELAY_MS,
    Math.max(
      COMPLETED_REPLAY_MIN_LINE_DELAY_MS,
      Math.floor(COMPLETED_REPLAY_MAX_DURATION_MS / totalSegments),
    ),
  );
}

function parseAnnotationRevision(value: string): bigint | null {
  if (!/^(0|[1-9][0-9]*)$/u.test(value)) return null;
  try {
    return BigInt(value);
  } catch {
    return null;
  }
}

function waitForCompletedReplayDelay(
  delayMs: number,
  signal: AbortSignal,
): Promise<boolean> {
  if (signal.aborted) return Promise.resolve(false);
  return new Promise((resolve) => {
    const finish = (completed: boolean) => {
      window.clearTimeout(timeoutID);
      signal.removeEventListener("abort", handleAbort);
      resolve(completed);
    };
    const handleAbort = () => finish(false);
    const timeoutID = window.setTimeout(() => finish(true), delayMs);
    signal.addEventListener("abort", handleAbort, { once: true });
  });
}

export async function renderEditor(app: HTMLElement): Promise<void> {
  syncWorkspaceSelectionFromLocation();
  const params = new URLSearchParams(window.location.search);
  const itemImageID = params.get("itemImageId") ?? "";
  const itemID = params.get("itemId") ?? "";
  const jobIdParam = params.get("jobId");
  let requestedJobID = (() => {
    if (
      jobIdParam === null
      || jobIdParam.length > 20
      || !/^[1-9][0-9]*$/u.test(jobIdParam)
    ) return null;
    const parsed = BigInt(jobIdParam);
    return parsed <= 18446744073709551615n ? parsed : null;
  })();
  let requestedJobItemImageID = requestedJobID === null ? "" : itemImageID;
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
  const readyTranscriptionOverlays = new Set<string>();

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
    cancelCompletedReplay();
    cancelPendingCompletedReload();
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
      const reprocessResponse = await reprocessItemImage(
        targetItemImageID,
        targetContextID,
        canonicalPage.revision,
      );
      const responseItemImageID = uint64ToString(
        reprocessResponse.itemImageId,
      );
      const successorJobID = reprocessResponse.transcriptionJobId;
      if (
        responseItemImageID !== targetItemImageID ||
        successorJobID <= 0n
      ) {
        throw new Error(
          "Reprocessing returned an invalid successor transcription job.",
        );
      }
      if (activeItemImageID === targetItemImageID) {
        requestedJobID = successorJobID;
        requestedJobItemImageID = targetItemImageID;
        const route = new URL(window.location.href);
        route.searchParams.set("jobId", successorJobID.toString());
        window.history.replaceState(window.history.state, "", route);
        void monitoredJobReconcile?.().catch(() => {
          if (activeItemImageID === targetItemImageID) {
            publishBatchState(
              "Fresh segmentation completed. Waiting to refresh the successor transcription job...",
              true,
            );
          }
        });
      }
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
      cancelCompletedReplay();
      cancelPendingCompletedReload();
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
  const blockedCompletedJobs = new Set<string>();
  const settledCompletedReplays = new Set<string>();
  const authoritativeTerminalJobs = new Set<string>();
  const visibleCompletedOrdinals = new Map<string, Set<number>>();
  let completedReplay: {
    controller: AbortController;
    identity: string;
    jobID: string;
    key: string;
  } | null = null;
  let completedReplayRetryTimer: number | null = null;
  let completedReloadSequence = 0;
  let pendingCompletedReload: {
    completionKey: string;
    dispatched: boolean;
    identity: string;
    job: EditorTranscriptionJob;
    requestId: string;
    targetCanvasID: string;
    targetItemImageID: string;
    targetWindowID: string;
    timeoutID: number | null;
  } | null = null;
  let editorDisposed = false;
  let stopResponsiveBottomPane = () => {};
  let miradorViewer: ReturnType<typeof Mirador.viewer> | null = null;

  function remoteRebaseIdentity(
    targetItemImageID: string,
    targetCanvasID: string,
    targetWindowID: string,
  ): string {
    return `${targetWindowID}\u0000${targetCanvasID}\u0000${targetItemImageID}`;
  }

  function transcriptionOverlayReady(targetItemImageID: string): boolean {
    if (!activeCanvasID || !activeWindowID) return false;
    return readyTranscriptionOverlays.has(remoteRebaseIdentity(
      targetItemImageID,
      activeCanvasID,
      activeWindowID,
    ));
  }

  function completedReloadIsActive(
    pending: NonNullable<typeof pendingCompletedReload>,
  ): boolean {
    return !editorDisposed
      && pendingCompletedReload === pending
      && activeItemImageID === pending.targetItemImageID
      && activeCanvasID === pending.targetCanvasID
      && activeWindowID === pending.targetWindowID;
  }

  function cancelPendingCompletedReload(): void {
    const pending = pendingCompletedReload;
    pendingCompletedReload = null;
    if (pending?.timeoutID != null) window.clearTimeout(pending.timeoutID);
  }

  function failPendingCompletedReload(
    pending: NonNullable<typeof pendingCompletedReload>,
  ): void {
    if (pendingCompletedReload !== pending) return;
    const targetsActive = !editorDisposed
      && activeItemImageID === pending.targetItemImageID
      && activeCanvasID === pending.targetCanvasID
      && activeWindowID === pending.targetWindowID;
    if (pending.timeoutID !== null) window.clearTimeout(pending.timeoutID);
    pendingCompletedReload = null;
    if (!targetsActive) return;
    blockedCompletedJobs.add(pending.completionKey);
    blockCompletedTranscription(
      "The completed transcription could not be loaded into the editor. Reload the page to try again.",
      "Scribe could not confirm that the committed annotation page loaded. Reload this page before editing or retranscribing.",
    );
  }

  function dispatchPendingCompletedReload(identity: string): void {
    const pending = pendingCompletedReload;
    if (
      !pending
      || pending.identity !== identity
      || pending.dispatched
      || !remoteRebaseReady.has(identity)
      || !completedReloadIsActive(pending)
    ) return;
    pending.dispatched = true;
    pending.timeoutID = window.setTimeout(
      () => failPendingCompletedReload(pending),
      COMPLETED_RELOAD_ACK_TIMEOUT_MS,
    );
    publishBatchState(
      "Loading completed transcription into the editor...",
      true,
    );
    setBatchBanner(
      "Loading completed transcription",
      "Scribe is refreshing the editor from the committed annotation page.",
      true,
    );
    document.dispatchEvent(
      new CustomEvent("scribe:reload-annotations", {
        detail: {
          canvasId: pending.targetCanvasID,
          itemImageId: pending.targetItemImageID,
          requestId: pending.requestId,
          windowId: pending.targetWindowID,
        },
      }),
    );
  }

  function reloadCompletedJob(
    job: EditorTranscriptionJob,
    targetItemImageID: string,
    targetCanvasID: string,
    targetWindowID: string,
  ): boolean {
    const jobID = job.id.toString();
    const identity = remoteRebaseIdentity(
      targetItemImageID,
      targetCanvasID,
      targetWindowID,
    );
    const completionKey = `${identity}\u0000${jobID}`;
    if (reloadedCompletedJobs.has(completionKey)) return true;
    if (blockedCompletedJobs.has(completionKey)) return false;
    if (pendingCompletedReload?.completionKey === completionKey) {
      dispatchPendingCompletedReload(identity);
      return false;
    }
    cancelPendingCompletedReload();
    const pending = {
      completionKey,
      dispatched: false,
      identity,
      job,
      requestId: `completed-reload-${jobID}-${++completedReloadSequence}`,
      targetCanvasID,
      targetItemImageID,
      targetWindowID,
      timeoutID: null,
    };
    pendingCompletedReload = pending;
    if (remoteRebaseReady.has(identity)) {
      dispatchPendingCompletedReload(identity);
    } else {
      publishBatchState(
        "Completed transcription is ready. Waiting for the editor to load it...",
        true,
      );
      setBatchBanner(
        "Completed transcription is ready",
        "Scribe will refresh the editor as soon as its annotation bridge is ready.",
        true,
      );
    }
    return false;
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

  function blockCompletedTranscription(message: string, detail: string) {
    publishBatchState(message, true);
    setBatchBanner("Editor reload required", detail, true);
  }

  function cancelCompletedReplay(): void {
    completedReplay?.controller.abort();
    completedReplay = null;
    if (completedReplayRetryTimer !== null) {
      window.clearTimeout(completedReplayRetryTimer);
      completedReplayRetryTimer = null;
    }
  }

  function scheduleCompletedReplayRetry(
    replay: NonNullable<typeof completedReplay>,
    targetItemImageID: string,
    targetCanvasID: string,
    targetWindowID: string,
  ): void {
    if (completedReplayRetryTimer !== null) return;
    completedReplayRetryTimer = window.setTimeout(() => {
      completedReplayRetryTimer = null;
      if (
        editorDisposed
        || activeItemImageID !== targetItemImageID
        || activeCanvasID !== targetCanvasID
        || activeWindowID !== targetWindowID
        || !readyTranscriptionOverlays.has(replay.identity)
      ) return;
      void monitoredJobReconcile?.().catch(() => {
        if (!editorDisposed && activeItemImageID === targetItemImageID) {
          publishBatchState(
            "Could not reload the completed transcription yet. Scribe will keep retrying...",
            true,
          );
        }
      });
    }, 2_000);
  }

  function completedReplayIsActive(
    replay: NonNullable<typeof completedReplay>,
    targetItemImageID: string,
    targetCanvasID: string,
    targetWindowID: string,
  ): boolean {
    if (
      editorDisposed
      || replay.controller.signal.aborted
      || completedReplay !== replay
      || activeItemImageID !== targetItemImageID
      || activeCanvasID !== targetCanvasID
      || activeWindowID !== targetWindowID
      || !readyTranscriptionOverlays.has(replay.identity)
      || !canvasImageRegistry
    ) return false;
    try {
      return canvasImageRegistry.itemImageIdForCanvas(targetCanvasID) ===
        targetItemImageID;
    } catch {
      return false;
    }
  }

  function clearTranscriptionWand(
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
    if (!readyTranscriptionOverlays.has(identity)) return;
    document.dispatchEvent(
      new CustomEvent("scribe:transcription-segment", {
        detail: {
          annotation: null,
          canvasId: targetCanvasID,
          itemImageId: targetItemImageID,
          jobId: jobID,
          persisted: false,
          windowId: targetWindowID,
        },
      }),
    );
  }

  function finalizeCompletedJob(
    job: EditorTranscriptionJob,
    targetItemImageID: string,
    targetCanvasID: string,
    targetWindowID: string,
  ): void {
    if (
      activeItemImageID !== targetItemImageID
      || activeCanvasID !== targetCanvasID
      || activeWindowID !== targetWindowID
    ) return;
    const jobID = job.id.toString();
    clearTranscriptionWand(
      jobID,
      targetItemImageID,
      targetCanvasID,
      targetWindowID,
    );
    const reloaded = reloadCompletedJob(
      job,
      targetItemImageID,
      targetCanvasID,
      targetWindowID,
    );
    if (reloaded) renderJobStatus(job);
  }

  function rejectCompletedJob(
    job: EditorTranscriptionJob,
    targetItemImageID: string,
    targetCanvasID: string,
    targetWindowID: string,
  ): void {
    if (
      activeItemImageID !== targetItemImageID
      || activeCanvasID !== targetCanvasID
      || activeWindowID !== targetWindowID
    ) return;
    const identity = remoteRebaseIdentity(
      targetItemImageID,
      targetCanvasID,
      targetWindowID,
    );
    blockedCompletedJobs.add(`${identity}\u0000${job.id.toString()}`);
    clearTranscriptionWand(
      job.id.toString(),
      targetItemImageID,
      targetCanvasID,
      targetWindowID,
    );
    blockCompletedTranscription(
      "The completed transcription could not be safely applied to this page.",
      "Scribe could not verify the exact committed annotation page. Reload this page before editing or retranscribing.",
    );
  }

  function applyCompletedJob(
    job: EditorTranscriptionJob,
    targetItemImageID: string,
    authoritative: boolean,
  ): void {
    if (!authoritative) {
      publishBatchState(
        "Automatic transcription finished. Preparing the completed text for the editor...",
        true,
      );
      setBatchBanner(
        "Preparing completed transcription",
        "Scribe is loading the exact committed annotation revision before applying the finished lines.",
        true,
      );
      return;
    }

    const jobID = job.id.toString();
    const completedAttempt = exactCompletedAttempt(job);
    const resultRevision = completedAttempt?.resultRevision ?? "";
    const attemptNumber = completedAttempt?.attemptNumber ?? 0;
    const targetCanvasID = activeCanvasID;
    const targetWindowID = activeWindowID;
    const identity = remoteRebaseIdentity(
      targetItemImageID,
      targetCanvasID,
      targetWindowID,
    );
    if (blockedCompletedJobs.has(`${identity}\u0000${jobID}`)) return;

    if (job.itemImageId?.toString() !== targetItemImageID) {
      rejectCompletedJob(
        job,
        targetItemImageID,
        targetCanvasID,
        targetWindowID,
      );
      return;
    }
    if (
      job.completedSegments !== job.totalSegments
      || (job.failedSegments ?? 0) !== 0
      || job.totalSegments < 0
      || job.totalSegments > COMPLETED_REPLAY_MAX_SEGMENTS
    ) {
      rejectCompletedJob(
        job,
        targetItemImageID,
        targetCanvasID,
        targetWindowID,
      );
      return;
    }
    // Zero-line pages and older projections without attempt history have no
    // safe line animation to reconstruct. They still retain the established
    // canonical reload behavior, but never synthesize transcription results.
    if (!resultRevision || job.totalSegments === 0) {
      finalizeCompletedJob(
        job,
        targetItemImageID,
        targetCanvasID,
        targetWindowID,
      );
      return;
    }

    const replayKey = `${identity}\u0000${jobID}\u0000${attemptNumber}\u0000${resultRevision}`;
    if (settledCompletedReplays.has(replayKey)) {
      finalizeCompletedJob(
        job,
        targetItemImageID,
        targetCanvasID,
        targetWindowID,
      );
      return;
    }
    if (!readyTranscriptionOverlays.has(identity)) {
      publishBatchState(
        "Automatic transcription is complete. Waiting for the editor overlay before applying the finished lines...",
        true,
      );
      setBatchBanner(
        "Completed transcription is ready",
        "Scribe will apply the finished lines as soon as the page overlay is ready.",
        true,
      );
      return;
    }
    if (completedReplay?.key === replayKey) return;
    cancelCompletedReplay();

    const replay = {
      controller: new AbortController(),
      identity,
      jobID,
      key: replayKey,
    };
    completedReplay = replay;
    publishBatchState(
      `Applying completed transcription: line 1/${job.totalSegments}.`,
      true,
    );
    setBatchBanner(
      `Applying completed transcription: 1/${job.totalSegments}`,
      "Scribe is showing the committed text line by line.",
      true,
    );

    void (async () => {
      let snapshot: AnnotationPageSnapshot;
      try {
        snapshot = await getAnnotationPage(targetItemImageID);
      } catch {
        if (completedReplayIsActive(
          replay,
          targetItemImageID,
          targetCanvasID,
          targetWindowID,
        )) {
          publishBatchState(
            "Could not load the completed transcription yet. Scribe will retry...",
            true,
          );
          setBatchBanner(
            "Completed transcription is waiting",
            "Scribe could not load the committed annotation page yet and will retry automatically.",
            true,
          );
          scheduleCompletedReplayRetry(
            replay,
            targetItemImageID,
            targetCanvasID,
            targetWindowID,
          );
        }
        return;
      }
      if (!completedReplayIsActive(
        replay,
        targetItemImageID,
        targetCanvasID,
        targetWindowID,
      )) return;

      if (snapshot.canvasUri !== targetCanvasID) {
        rejectCompletedJob(
          job,
          targetItemImageID,
          targetCanvasID,
          targetWindowID,
        );
        return;
      }
      const canonicalRevision = parseAnnotationRevision(snapshot.revision);
      const completedRevision = BigInt(resultRevision);
      if (canonicalRevision === null) {
        rejectCompletedJob(
          job,
          targetItemImageID,
          targetCanvasID,
          targetWindowID,
        );
        return;
      }
      if (canonicalRevision > completedRevision) {
        settledCompletedReplays.add(replayKey);
        finalizeCompletedJob(
          job,
          targetItemImageID,
          targetCanvasID,
          targetWindowID,
        );
        return;
      }
      if (canonicalRevision < completedRevision) {
        clearTranscriptionWand(
          jobID,
          targetItemImageID,
          targetCanvasID,
          targetWindowID,
        );
        publishBatchState(
          "The completed transcription page is not available yet. Scribe will retry...",
          true,
        );
        setBatchBanner(
          "Completed transcription is waiting",
          "Scribe is waiting for the exact committed annotation revision and will retry automatically.",
          true,
        );
        scheduleCompletedReplayRetry(
          replay,
          targetItemImageID,
          targetCanvasID,
          targetWindowID,
        );
        return;
      }

      const lines = canonicalReplayLines(snapshot);
      if (lines.length !== job.totalSegments) {
        rejectCompletedJob(
          job,
          targetItemImageID,
          targetCanvasID,
          targetWindowID,
        );
        return;
      }

      const visibleKey = `${identity}\u0000${jobID}\u0000${attemptNumber}`;
      let visibleOrdinals = visibleCompletedOrdinals.get(visibleKey);
      if (!visibleOrdinals) {
        visibleOrdinals = new Set<number>();
        visibleCompletedOrdinals.set(visibleKey, visibleOrdinals);
      }
      const lineDelay = completedReplayLineDelay(lines.length);
      for (let index = 0; index < lines.length; index += 1) {
        const done = index + 1;
        if (visibleOrdinals.has(done)) continue;
        if (!completedReplayIsActive(
          replay,
          targetItemImageID,
          targetCanvasID,
          targetWindowID,
        )) return;
        const annotation = lines[index];
        publishBatchState(
          `Applying completed transcription: line ${done}/${lines.length}.`,
          true,
        );
        setBatchBanner(
          `Applying completed transcription: ${done}/${lines.length}`,
          "Scribe is showing the committed text line by line.",
          true,
        );
        document.dispatchEvent(
          new CustomEvent("scribe:transcription-segment", {
            detail: {
              annotation,
              annotationId: annotation.id,
              attemptNumber,
              canvasId: targetCanvasID,
              catchUp: true,
              done,
              itemImageId: targetItemImageID,
              jobId: jobID,
              persisted: false,
              total: lines.length,
              windowId: targetWindowID,
            },
          }),
        );
        if (!(await waitForCompletedReplayDelay(
          lineDelay,
          replay.controller.signal,
        ))) return;
        if (!completedReplayIsActive(
          replay,
          targetItemImageID,
          targetCanvasID,
          targetWindowID,
        )) return;
        document.dispatchEvent(
          new CustomEvent("scribe:transcription-result", {
            detail: {
              annotation,
              annotationId: annotation.id,
              attemptNumber,
              canvasId: targetCanvasID,
              catchUp: true,
              done,
              itemImageId: targetItemImageID,
              jobId: jobID,
              persisted: false,
              total: lines.length,
              windowId: targetWindowID,
            },
          }),
        );
        if (!completedReplayIsActive(
          replay,
          targetItemImageID,
          targetCanvasID,
          targetWindowID,
        )) return;
        visibleOrdinals.add(done);
      }

      if (!completedReplayIsActive(
        replay,
        targetItemImageID,
        targetCanvasID,
        targetWindowID,
      )) return;
      settledCompletedReplays.add(replayKey);
      finalizeCompletedJob(
        job,
        targetItemImageID,
        targetCanvasID,
        targetWindowID,
      );
    })().finally(() => {
      if (completedReplay === replay) completedReplay = null;
    });
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
      const processedSegments = Math.max(
        0,
        job.completedSegments + (job.failedSegments ?? 0),
      );
      publishBatchState(
        `Batch transcription is running. Automatic transcription progress: ${processedSegments}/${total}. You can keep editing while new text is applied.`,
        true,
      );
      setBatchBanner(
        `Automatic transcription in progress: ${processedSegments}/${total}`,
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
    if (isCanceledStatus(job.status)) {
      publishBatchState("Automatic transcription was canceled.", false);
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
    job: EditorTranscriptionJob,
    targetItemImageID: string,
    authoritative = false,
  ) {
    if (targetItemImageID !== activeItemImageID) return;
    const terminalJobKey = `${targetItemImageID}\u0000${job.id.toString()}`;
    if (!authoritative && authoritativeTerminalJobs.has(terminalJobKey)) return;
    if (
      authoritative
      && (
        isCompletedStatus(job.status)
        || isFailedStatus(job.status)
        || isCanceledStatus(job.status)
      )
    ) {
      authoritativeTerminalJobs.add(terminalJobKey);
    }
    if (completedReplay && completedReplay.jobID !== job.id.toString()) {
      cancelCompletedReplay();
    }
    if (
      pendingCompletedReload
      && pendingCompletedReload.job.id !== job.id
    ) {
      cancelPendingCompletedReload();
    }
    if (isCompletedStatus(job.status)) {
      applyCompletedJob(job, targetItemImageID, authoritative);
      return;
    }
    renderJobStatus(job);
    const overlayReady = transcriptionOverlayReady(targetItemImageID);
    const attemptNumber = transcriptionAttemptNumber(job);
    const processedSegments = Math.max(
      0,
      job.completedSegments + (job.failedSegments ?? 0),
    );

    if (job.currentAnnotationJson && overlayReady) {
      const segmentKey = `${job.id.toString()}:${attemptNumber}:${job.currentAnnotationId ?? ""}:${job.updatedAt ?? ""}:${processedSegments}/${job.totalSegments}`;
      if (segmentKey !== lastSegmentKey) {
        lastSegmentKey = segmentKey;
        try {
          const anno = JSON.parse(job.currentAnnotationJson);
          const annotationID = job.currentAnnotationId?.trim()
            || (typeof anno?.id === "string" ? anno.id : "");
          document.dispatchEvent(
            new CustomEvent("scribe:transcription-segment", {
              detail: {
                annotation: anno,
                annotationId: annotationID,
                attemptNumber,
                canvasId: activeCanvasID,
                catchUp: false,
                done: job.totalSegments > 0
                  ? Math.min(processedSegments + 1, job.totalSegments)
                  : processedSegments,
                itemImageId: targetItemImageID,
                jobId: job.id.toString(),
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

    if (job.lastResultAnnotationJson && overlayReady) {
      const resultKey = `${job.id.toString()}:${attemptNumber}:${job.updatedAt ?? ""}:${processedSegments}/${job.totalSegments}`;
      if (resultKey !== lastResultKey) {
        lastResultKey = resultKey;
        try {
          const anno = JSON.parse(job.lastResultAnnotationJson);
          const annotationID = typeof anno?.id === "string" ? anno.id : "";
          document.dispatchEvent(
            new CustomEvent("scribe:transcription-result", {
              detail: {
                annotation: anno,
                annotationId: annotationID,
                attemptNumber,
                canvasId: activeCanvasID,
                catchUp: false,
                done: processedSegments,
                itemImageId: targetItemImageID,
                jobId: job.id.toString(),
                // Per-segment results are progress previews. The canonical page is
                // committed atomically only when the entire job completes.
                persisted: false,
                total: job.totalSegments,
                windowId: activeWindowID,
              },
            }),
          );
          const identity = remoteRebaseIdentity(
            targetItemImageID,
            activeCanvasID,
            activeWindowID,
          );
          if (
            annotationID
            && processedSegments > 0
            && processedSegments <= job.totalSegments
          ) {
            const visibleKey = `${identity}\u0000${job.id.toString()}\u0000${attemptNumber}`;
            let visibleOrdinals = visibleCompletedOrdinals.get(visibleKey);
            if (!visibleOrdinals) {
              visibleOrdinals = new Set<number>();
              visibleCompletedOrdinals.set(visibleKey, visibleOrdinals);
            }
            visibleOrdinals.add(processedSegments);
          }
        } catch {
          /* ignore */
        }
      }
    }

    if (
      overlayReady
      && (isFailedStatus(job.status) || isCanceledStatus(job.status))
    ) {
      clearTranscriptionWand(
        job.id.toString(),
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
          "dev.scribe.transcription.canceled",
        ],
      },
      (event) => {
        const data = event.data ?? {};
        const eventJobID = eventBigInt(data.jobId);
        if (
          requestedJobID !== null &&
          requestedJobItemImageID === targetItemImageID &&
          eventJobID !== requestedJobID
        ) {
          // Starting a successor job supersedes the exact deep-linked job in
          // the database without emitting a separate superseded event for the
          // old identity. Refresh that exact job when any successor signal
          // arrives so its running UI cannot remain pinned forever.
          reconcile();
          return;
        }
        switch (event.type) {
          case "dev.scribe.transcription.task.started":
            applyJobUpdate(
              {
                id: eventBigInt(data.jobId),
                status: TranscriptionJobStatus.RUNNING,
                attemptCount: eventNumber(data.attemptNumber),
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
                attemptCount: eventNumber(data.attemptNumber),
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
                attemptCount: eventNumber(data.attemptNumber),
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
                attemptCount: eventNumber(data.attemptNumber),
                completedSegments: eventNumber(data.completedSegments),
                failedSegments: eventNumber(data.failedSegments),
                totalSegments: eventNumber(data.totalSegments),
                updatedAt: typeof event.time === "string" ? event.time : "",
                errorMessage: typeof data.error === "string" ? data.error : "",
              },
              targetItemImageID,
            );
            break;
          case "dev.scribe.transcription.canceled":
            applyJobUpdate(
              {
                id: eventBigInt(data.jobId),
                status: TranscriptionJobStatus.CANCELED,
                attemptCount: eventNumber(data.attemptNumber),
                completedSegments: eventNumber(data.completedSegments),
                failedSegments: eventNumber(data.failedSegments),
                totalSegments: eventNumber(data.totalSegments),
                updatedAt: typeof event.time === "string" ? event.time : "",
              },
              targetItemImageID,
            );
            break;
          default:
            break;
        }
        if (
          event.type === "dev.scribe.transcription.completed" ||
          event.type === "dev.scribe.transcription.failed" ||
          event.type === "dev.scribe.transcription.canceled"
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
    cancelCompletedReplay();
    cancelPendingCompletedReload();
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
            const exactJobID = requestedJobItemImageID === targetItemImageID
              ? requestedJobID
              : null;
            if (!latest && exactJobID !== null) {
              latest = await getTranscriptionJob(exactJobID);
              if (
                latest.id !== exactJobID ||
                uint64ToString(latest.itemImageId) !== targetItemImageID
              ) {
                throw new Error("The transcription job belongs to a different item image.");
              }
            }
            if (!latest) {
              const jobs = await listTranscriptionJobs(BigInt(targetItemImageID));
              const summary = jobs[0];
              if (summary) {
                const fullJob = await getTranscriptionJob(summary.id);
                if (uint64ToString(fullJob.itemImageId) !== targetItemImageID) {
                  throw new Error("The latest transcription job belongs to a different item image.");
                }
                latest = fullJob;
              }
            }
            if (sequence !== activationSequence) return;
            if (
              exactJobID !== null &&
              (
                requestedJobID !== exactJobID ||
                requestedJobItemImageID !== targetItemImageID
              )
            ) return;
            if (latest) {
              applyJobUpdate(latest, targetItemImageID, true);
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
      cancelCompletedReplay();
      cancelPendingCompletedReload();
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
      const route = new URL(window.location.href);
      if (
        targetItemImageID !== activeItemImageID
        || canvasID !== activeCanvasID
        || windowID !== activeWindowID
      ) {
        cancelCompletedReplay();
        cancelPendingCompletedReload();
      }
      activeCanvasID = canvasID;
      activeWindowID = windowID;
      if (targetItemImageID !== activeItemImageID) {
        requestedJobID = null;
        requestedJobItemImageID = "";
        route.searchParams.delete("jobId");
      }
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
    dispatchPendingCompletedReload(identity);
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
  const handleReloadAnnotationsResult = (event: Event) => {
    const detail = (
      event as CustomEvent<{
        canvasId: string;
        itemImageId: string;
        ok: boolean;
        requestId: string;
        windowId: string;
      }>
    ).detail;
    const pending = pendingCompletedReload;
    if (
      !pending
      || !pending.dispatched
      || detail?.requestId?.trim() !== pending.requestId
      || detail?.canvasId?.trim() !== pending.targetCanvasID
      || detail?.itemImageId?.trim() !== pending.targetItemImageID
      || detail?.windowId?.trim() !== pending.targetWindowID
      || typeof detail.ok !== "boolean"
    ) return;
    const targetsActive = completedReloadIsActive(pending);
    if (pending.timeoutID !== null) window.clearTimeout(pending.timeoutID);
    pendingCompletedReload = null;
    if (!targetsActive) return;
    if (!detail.ok) {
      blockedCompletedJobs.add(pending.completionKey);
      blockCompletedTranscription(
        "The completed transcription could not be loaded into the editor. Reload the page to try again.",
        "Scribe could not confirm that the committed annotation page loaded. Reload this page before editing or retranscribing.",
      );
      return;
    }
    reloadedCompletedJobs.add(pending.completionKey);
    renderJobStatus(pending.job);
  };
  const handleTranscriptionOverlayState = (event: Event) => {
    const detail = (
      event as CustomEvent<{
        canvasId: string;
        ready: boolean;
        windowId: string;
      }>
    ).detail;
    const canvasID = detail?.canvasId?.trim() ?? "";
    const windowID = detail?.windowId?.trim() ?? "";
    if (!canvasID || !windowID || !canvasImageRegistry) return;
    let targetItemImageID = "";
    try {
      targetItemImageID = canvasImageRegistry.itemImageIdForCanvas(canvasID);
    } catch {
      return;
    }
    const identity = remoteRebaseIdentity(
      targetItemImageID,
      canvasID,
      windowID,
    );
    if (!detail.ready) {
      readyTranscriptionOverlays.delete(identity);
      if (completedReplay?.identity === identity) {
        cancelCompletedReplay();
        if (
          targetItemImageID === activeItemImageID
          && canvasID === activeCanvasID
          && windowID === activeWindowID
        ) {
          publishBatchState(
            "Automatic transcription is complete. Waiting for the editor overlay before applying the finished lines...",
            true,
          );
          setBatchBanner(
            "Completed transcription is ready",
            "Scribe will resume applying the finished lines when the page overlay returns.",
            true,
          );
        }
      }
      if (
        targetItemImageID === activeItemImageID
        && canvasID === activeCanvasID
        && windowID === activeWindowID
      ) {
        lastSegmentKey = "";
        lastResultKey = "";
      }
      return;
    }
    const wasReady = readyTranscriptionOverlays.has(identity);
    readyTranscriptionOverlays.add(identity);
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
  document.addEventListener(
    "scribe:reload-annotations-result",
    handleReloadAnnotationsResult,
  );
  document.addEventListener(
    "scribe:transcription-overlay-state",
    handleTranscriptionOverlayState,
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
      editorDisposed = true;
      cancelCompletedReplay();
      cancelPendingCompletedReload();
      document.removeEventListener("scribe:dirty-state", handleDirtyState);
      document.removeEventListener("scribe:active-canvas", handleActiveCanvas);
      document.removeEventListener(
        "scribe:remote-rebase-ready",
        handleRemoteRebaseReady,
      );
      document.removeEventListener(
        "scribe:reload-annotations-result",
        handleReloadAnnotationsResult,
      );
      document.removeEventListener(
        "scribe:transcription-overlay-state",
        handleTranscriptionOverlayState,
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
      stopResponsiveBottomPane();
      miradorViewer?.unmount();
      miradorViewer = null;
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
    bottomPaneHeightForViewport({
      height: document.getElementById("mirador-viewer")?.clientHeight || window.innerHeight,
      width: document.getElementById("mirador-viewer")?.clientWidth || window.innerWidth,
    }),
  );

  miradorViewer = Mirador.viewer(
    {
      ...viewerOptions,
      windows: [{
        canvasId: activeCanvasID || undefined,
        id: EDITOR_WINDOW_ID,
        manifestId: manifestURL,
      }],
      workspaceControlPanel: { enabled: false },
      window: {
        ...viewerOptions.window,
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
  const viewerElement = document.getElementById("mirador-viewer");
  if (viewerElement) {
    stopResponsiveBottomPane = observeResponsiveBottomPane(
      viewerElement,
      (height) => miradorViewer?.store.dispatch(updateWindow(
        EDITOR_WINDOW_ID,
        { defaultSidebarPanelHeight: height },
      )),
    );
  }

  await activateItemImage(runItemImageID, runResp, requestedJob);
}
