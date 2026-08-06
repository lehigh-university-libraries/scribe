import { Code, ConnectError, createClient } from "@connectrpc/connect";
import { ItemService } from "../proto/scribe/v1/item_connect";
import type { AnnotationExportFormat } from "../proto/scribe/v1/annotation_pb";
import {
  type Item,
  type ItemImageRevision,
  type ListItemsResponse,
  type PrepareItemExportResponse,
  type ProviderCallAudit,
  type UploadBatch,
  type UploadItemImageResponse,
  UploadBatchFileStatus,
  UploadBatchStatus,
} from "../proto/scribe/v1/item_pb";
import { getTransport } from "./transport";
import { readFileBytes, uint64ToString } from "../lib/util";
import { sha256Hex } from "../lib/digest";

function client() {
  return createClient(ItemService, getTransport());
}

export type ItemPage = Pick<ListItemsResponse, "items" | "nextPageToken">;

export interface ListItemsOptions {
  pageSize?: number;
  pageToken?: string;
  query?: string;
  signal?: AbortSignal;
}

export async function listItems(options: ListItemsOptions = {}): Promise<ItemPage> {
  const pageSize = options.pageSize ?? 0;
  if (!Number.isInteger(pageSize) || pageSize < 0 || pageSize > 100) {
    throw new RangeError("pageSize must be an integer between 0 and 100");
  }
  const query = options.query?.trim() ?? "";
  if ([...query].length > 200) {
    throw new RangeError("query must contain at most 200 Unicode characters");
  }
  const resp = await client().listItems({
    pageSize,
    pageToken: options.pageToken ?? "",
    query,
  }, { signal: options.signal });
  return { items: resp.items, nextPageToken: resp.nextPageToken };
}


export async function importManifest(manifestUrl: string, contextId = 0n): Promise<{ item: Item; firstItemImageId: string }> {
  const normalizedURL = manifestUrl.trim();
  if (!normalizedURL) throw new Error("manifest URL is required");
  const idempotencyKey = await sha256Hex(JSON.stringify(["manifest", normalizedURL, contextId.toString()]));
  const resp = await client().importManifest({
    manifestUrl: normalizedURL,
    contextId,
    idempotencyKey,
  });
  if (!resp.item) throw new Error("no item in response");
  const firstImage = resp.item.images[0];
  const firstItemImageId = firstImage ? uint64ToString(firstImage.id) : "";
  return { item: resp.item, firstItemImageId };
}

export interface UploadBatchProgress {
  completed: number;
  total: number;
  filename: string;
  sequence: number;
  status: "hashing" | "uploading" | "completed" | "retrying" | "failed" | "canceled";
  attempt: number;
  error?: string;
}

export interface UploadBatchOptions {
  contextId?: bigint;
  signal?: AbortSignal;
  onProgress?: (progress: UploadBatchProgress) => void;
  maxAttempts?: number;
  retryDelayMs?: number;
}

export interface UploadBatchResult {
  item: Item;
  batch: UploadBatch;
}

export class UploadBatchCancellationError extends Error {
  constructor(options?: ErrorOptions) {
    super("Upload stopped locally, but server cancellation could not be confirmed. Select the same files to resume and verify the batch state.", options);
    this.name = "UploadBatchCancellationError";
  }
}

export class UploadBatchError extends Error {
  readonly batch: UploadBatch;
  readonly completed: number;
  readonly failedSequence: number;

  constructor(message: string, batch: UploadBatch, failedSequence: number, options?: ErrorOptions) {
    super(message, options);
    this.name = "UploadBatchError";
    this.batch = batch;
    this.completed = batch.completedFiles;
    this.failedSequence = failedSequence;
  }
}

class UploadBatchResponseError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "UploadBatchResponseError";
  }
}

async function hashFileSHA256(file: File, signal?: AbortSignal): Promise<string> {
  throwIfAborted(signal);
  if (typeof Worker === "undefined") {
    const digest = await sha256Hex(await readFileBytes(file));
    throwIfAborted(signal);
    return digest;
  }

  return new Promise<string>((resolve, reject) => {
    const worker = new Worker(
      new URL("../workers/sha256-worker.js", import.meta.url),
      { type: "module", name: "scribe-upload-hash" },
    );
    let settled = false;
    const finish = (callback: () => void) => {
      if (settled) return;
      settled = true;
      signal?.removeEventListener("abort", handleAbort);
      worker.terminate();
      callback();
    };
    const handleAbort = () => finish(() => reject(new DOMException("The operation was aborted", "AbortError")));
    signal?.addEventListener("abort", handleAbort, { once: true });
    worker.addEventListener("message", (event: MessageEvent<{ digest?: string; error?: string }>) => {
      const digest = event.data?.digest ?? "";
      if (/^[0-9a-f]{64}$/.test(digest)) finish(() => resolve(digest));
      else finish(() => reject(new Error(event.data?.error || "File hashing failed")));
    }, { once: true });
    worker.addEventListener("error", () => {
      finish(() => reject(new Error("File hashing worker failed")));
    }, { once: true });
    worker.postMessage({ file });
  });
}

interface PreparedBatchFile {
  file: File;
  contentSHA256: string;
}

const maxUploadBatchFiles = 1000;
const maxUploadFileBytes = 100 * 1024 * 1024;
const maxUploadBatchBytes = 2 * 1024 * 1024 * 1024;

async function prepareBatchFiles(files: File[], contextId: bigint, options: UploadBatchOptions): Promise<{ files: PreparedBatchFile[]; fingerprint: string }> {
  const prepared: PreparedBatchFile[] = [];
  const identity: string[] = [contextId.toString()];
  for (const [index, file] of files.entries()) {
    throwIfAborted(options.signal);
    reportProgress(options, {
      attempt: 0,
      completed: index,
      filename: file.name,
      sequence: index + 1,
      status: "hashing",
      total: files.length,
    });
    const contentSHA256 = await hashFileSHA256(file, options.signal);
    throwIfAborted(options.signal);
    prepared.push({ file, contentSHA256 });
    identity.push(file.name, file.size.toString(), contentSHA256);
  }
  return { files: prepared, fingerprint: await sha256Hex(JSON.stringify(identity)) };
}

function batchStorage(): Storage | undefined {
  try {
    return globalThis.localStorage;
  } catch {
    return undefined;
  }
}

function readStoredBatchID(storage: Storage | undefined, key: string): string | undefined {
  try {
    const value = storage?.getItem(key) ?? "";
    return /^[A-Za-z0-9_-]{1,64}$/.test(value) ? value : undefined;
  } catch {
    return undefined;
  }
}

function writeStoredBatchID(storage: Storage | undefined, key: string, batchID: string): void {
  try {
    storage?.setItem(key, batchID);
  } catch {
    // Durable state lives on the server. Storage restrictions only disable
    // automatic discovery of the batch after a full page reload.
  }
}

function removeStoredBatchID(storage: Storage | undefined, key: string): void {
  try {
    storage?.removeItem(key);
  } catch {
    // See writeStoredBatchID.
  }
}

function newBatchID(): string {
  if (typeof globalThis.crypto.randomUUID === "function") return globalThis.crypto.randomUUID();
  const bytes = globalThis.crypto.getRandomValues(new Uint8Array(16));
  return Array.from(bytes, (part) => part.toString(16).padStart(2, "0")).join("");
}

function assertCompletedBatchFileIdentities(batch: UploadBatch, expectedFileCount: number): void {
  if (batch.files.length !== expectedFileCount) {
    throw new UploadBatchResponseError("completed upload batch did not return the exact declared file set");
  }
  if (batch.completedFiles !== expectedFileCount || batch.failedFiles !== 0) {
    throw new UploadBatchResponseError("completed upload batch returned inconsistent completion counters");
  }
  const sequences = new Set<number>();
  const imageIDs = new Set<bigint>();
  const jobIDs = new Set<bigint>();
  for (const file of batch.files) {
    if (file.sequence < 1 || file.sequence > expectedFileCount || sequences.has(file.sequence)) {
      throw new UploadBatchResponseError("completed upload batch returned an invalid file sequence");
    }
    sequences.add(file.sequence);
    if (file.status !== UploadBatchFileStatus.COMPLETED) {
      throw new UploadBatchResponseError(`completed upload batch file ${file.sequence} is not completed`);
    }
    if (file.itemImageId === 0n) {
      throw new UploadBatchResponseError(`completed upload file ${file.sequence} omitted its image identity`);
    }
    if (imageIDs.has(file.itemImageId)) {
      throw new UploadBatchResponseError("completed upload batch returned a duplicate image identity");
    }
    imageIDs.add(file.itemImageId);
    if (file.transcriptionJobId === 0n) {
      throw new UploadBatchResponseError(`completed upload file ${file.sequence} omitted its transcription job identity`);
    }
    if (jobIDs.has(file.transcriptionJobId)) {
      throw new UploadBatchResponseError("completed upload batch returned a duplicate transcription job identity");
    }
    jobIDs.add(file.transcriptionJobId);
  }
}

function assertBatchIdentity(item: Item, batch: UploadBatch, expectedBatchID: string, expectedItemID?: string): void {
  if (batch.id !== expectedBatchID) {
    throw new UploadBatchResponseError("upload response returned an inconsistent batch identity");
  }
  if (!item.id.trim() || batch.itemId !== item.id || (expectedItemID !== undefined && item.id !== expectedItemID)) {
    throw new UploadBatchResponseError("upload response returned an inconsistent item identity");
  }
}

function assertCompletedFileResponse(
  response: UploadItemImageResponse,
  expectedBatchID: string,
  expectedItemID: string,
  expectedSequence: number,
): { item: Item; batch: UploadBatch } {
  if (!response.item || !response.image || !response.batch) {
    throw new UploadBatchResponseError("incomplete upload response");
  }
  assertBatchIdentity(response.item, response.batch, expectedBatchID, expectedItemID);
  const batchFile = response.batch.files.find((candidate) => candidate.sequence === expectedSequence);
  if (!batchFile || batchFile.status !== UploadBatchFileStatus.COMPLETED) {
    throw new UploadBatchResponseError(`upload response did not complete file ${expectedSequence}`);
  }
  if (
    response.image.id === 0n
    || response.image.id !== batchFile.itemImageId
    || response.image.itemId !== response.item.id
    || response.image.sequence !== expectedSequence
  ) {
    throw new UploadBatchResponseError(`upload response did not return the exact image identity for file ${expectedSequence}`);
  }
  if (response.transcriptionJobId === 0n || response.transcriptionJobId !== batchFile.transcriptionJobId) {
    throw new UploadBatchResponseError(`upload response did not return the exact transcription job identity for file ${expectedSequence}`);
  }
  return { item: response.item, batch: response.batch };
}

export async function uploadItemImages(files: File[], options: UploadBatchOptions = {}): Promise<UploadBatchResult> {
  if (files.length === 0) throw new Error("no files provided");
  if (files.length > maxUploadBatchFiles) throw new Error(`a batch may contain at most ${maxUploadBatchFiles} files`);
  let totalBytes = 0;
  for (const file of files) {
    if (file.size <= 0 || file.size > maxUploadFileBytes) throw new Error(`${file.name || "file"} must be between 1 byte and 100 MiB`);
    totalBytes += file.size;
    if (totalBytes > maxUploadBatchBytes) throw new Error("a batch may contain at most 2 GiB of image data");
  }
  const contextId = options.contextId ?? 0n;
  const prepared = await prepareBatchFiles(files, contextId, options);
  const fingerprint = prepared.fingerprint;
  const storageKey = `scribe:upload-batch:${fingerprint}`;
  const storage = batchStorage();
  const batchID = readStoredBatchID(storage, storageKey) ?? newBatchID();
  writeStoredBatchID(storage, storageKey, batchID);
  let startResponse;
  try {
    startResponse = await client().startUploadBatch({
      batchId: batchID,
      name: files[0].name || "Untitled Item",
      contextId,
      files: prepared.files.map(({ file, contentSHA256 }) => ({
        filename: file.name,
        size: BigInt(file.size),
        contentSha256: contentSHA256,
      })),
    }, { signal: options.signal });
  } catch (error) {
    if (options.signal?.aborted) {
      await cancelBatchAfterAbort(batchID, storage, storageKey);
    }
    throw error;
  }
  if (!startResponse.item || !startResponse.batch) throw new Error("incomplete upload batch response");
  let item = startResponse.item;
  let batch = startResponse.batch;
  assertBatchIdentity(item, batch, batchID);
  const itemID = item.id;
  if (batch.status === UploadBatchStatus.COMPLETED) {
    assertCompletedBatchFileIdentities(batch, prepared.files.length);
    removeStoredBatchID(storage, storageKey);
    return { item, batch };
  }
  if (batch.status === UploadBatchStatus.CANCELED) {
    removeStoredBatchID(storage, storageKey);
    throw new DOMException("Upload batch was canceled", "AbortError");
  }

  const maxAttempts = Math.max(1, Math.min(5, Math.trunc(options.maxAttempts ?? 3)));
  const retryDelayMs = Math.max(0, Math.min(5000, Math.trunc(options.retryDelayMs ?? 250)));
  try {
    for (let i = 0; i < prepared.files.length; i++) {
      throwIfAborted(options.signal);
      const declared = batch.files.find((candidate) => candidate.sequence === i + 1);
      if (!declared) throw new Error(`batch response omitted file sequence ${i + 1}`);
      if (declared.status === UploadBatchFileStatus.COMPLETED) {
        reportProgress(options, {
          completed: batch.completedFiles,
          total: prepared.files.length,
          filename: prepared.files[i].file.name,
          sequence: i + 1,
          status: "completed",
          attempt: declared.attemptCount,
        });
        throwIfAborted(options.signal);
        continue;
      }
      reportProgress(options, {
        completed: batch.completedFiles,
        total: prepared.files.length,
        filename: prepared.files[i].file.name,
        sequence: i + 1,
        status: "uploading",
        attempt: 1,
      });
      throwIfAborted(options.signal);
      const imageData = await readFileBytes(prepared.files[i].file);
      throwIfAborted(options.signal);
      let lastError: unknown;
      for (let attempt = 1; attempt <= maxAttempts; attempt++) {
        try {
          const response = await client().uploadItemImage({
            batchId: batchID,
            sequence: i + 1,
            imageData,
          }, { signal: options.signal });
          const completed = assertCompletedFileResponse(response, batchID, itemID, i + 1);
          item = completed.item;
          batch = completed.batch;
          if (batch.status === UploadBatchStatus.COMPLETED) {
            assertCompletedBatchFileIdentities(batch, prepared.files.length);
          }
          reportProgress(options, {
            completed: batch.completedFiles,
            total: prepared.files.length,
            filename: prepared.files[i].file.name,
            sequence: i + 1,
            status: "completed",
            attempt,
          });
          // A committed final response wins over an abort that arrives from a
          // synchronous progress observer. Completed batches cannot be
          // canceled server-side; returning their durable identities lets the
          // caller continue the exact editor/job handoff.
          if (batch.status !== UploadBatchStatus.COMPLETED) throwIfAborted(options.signal);
          lastError = undefined;
          break;
        } catch (error) {
          lastError = error;
          if (options.signal?.aborted) throw error;
          if (attempt >= maxAttempts || !isRetryableUploadError(error)) break;
          reportProgress(options, {
            completed: batch.completedFiles,
            total: prepared.files.length,
            filename: prepared.files[i].file.name,
            sequence: i + 1,
            status: "retrying",
            attempt,
            error: String(error),
          });
          await abortableDelay(retryDelayMs * 2 ** (attempt - 1), options.signal);
        }
      }
      if (lastError !== undefined) {
        const latest = await client().getUploadBatch({ batchId: batchID }).catch(() => undefined);
        if (latest?.batch) batch = latest.batch;
        reportProgress(options, {
          completed: batch.completedFiles,
          total: prepared.files.length,
          filename: prepared.files[i].file.name,
          sequence: i + 1,
          status: "failed",
          attempt: maxAttempts,
          error: String(lastError),
        });
        throw new UploadBatchError(`upload failed for ${prepared.files[i].file.name}`, batch, i + 1, { cause: lastError });
      }
    }
    if (batch.status !== UploadBatchStatus.COMPLETED) {
      throwIfAborted(options.signal);
      throw new UploadBatchResponseError("upload did not return a completed upload batch");
    }
    assertCompletedBatchFileIdentities(batch, prepared.files.length);
    removeStoredBatchID(storage, storageKey);
    return { item, batch };
  } catch (error) {
    if (options.signal?.aborted) {
      reportProgress(options, {
        completed: batch.completedFiles,
        total: prepared.files.length,
        filename: prepared.files[Math.min(batch.completedFiles, prepared.files.length - 1)]?.file.name ?? "",
        sequence: Math.min(batch.completedFiles + 1, prepared.files.length),
        status: "canceled",
        attempt: 0,
      });
      await cancelBatchAfterAbort(batchID, storage, storageKey);
    }
    throw error;
  }
}

async function cancelBatchAfterAbort(batchID: string, storage: Storage | undefined, storageKey: string): Promise<void> {
  try {
    await client().cancelUploadBatch({ batchId: batchID });
    removeStoredBatchID(storage, storageKey);
  } catch (error) {
    if (ConnectError.from(error).code === Code.NotFound) {
      removeStoredBatchID(storage, storageKey);
      return;
    }
    throw new UploadBatchCancellationError({ cause: error });
  }
}

function reportProgress(options: UploadBatchOptions, progress: UploadBatchProgress): void {
  try {
    options.onProgress?.(progress);
  } catch {
    // Observer failures must not turn a committed upload into a failed batch.
  }
}

function throwIfAborted(signal?: AbortSignal): void {
  if (signal?.aborted) throw new DOMException("Upload canceled", "AbortError");
}

function isRetryableUploadError(error: unknown): boolean {
  if (error instanceof UploadBatchResponseError) return false;
  const code = ConnectError.from(error).code;
  return code === Code.Aborted
    || code === Code.AlreadyExists
    || code === Code.DeadlineExceeded
    || code === Code.Internal
    || code === Code.ResourceExhausted
    || code === Code.Unavailable
    || code === Code.Unknown;
}

function abortableDelay(delayMs: number, signal?: AbortSignal): Promise<void> {
  if (delayMs <= 0) {
    throwIfAborted(signal);
    return Promise.resolve();
  }
  return new Promise((resolve, reject) => {
    const timeout = globalThis.setTimeout(done, delayMs);
    signal?.addEventListener("abort", aborted, { once: true });
    function cleanup() {
      globalThis.clearTimeout(timeout);
      signal?.removeEventListener("abort", aborted);
    }
    function done() {
      cleanup();
      resolve();
    }
    function aborted() {
      cleanup();
      reject(new DOMException("Upload canceled", "AbortError"));
    }
  });
}

export async function getItem(itemId: string): Promise<Item> {
  const resp = await client().getItem({ itemId });
  if (!resp.item) throw new Error("no item in response");
  return resp.item;
}

export interface EditorManifest {
  item: Item;
  manifestJSON: string;
  selectedCanvasId: string;
}

export async function getEditorManifest(itemImageId: string): Promise<EditorManifest> {
  const normalized = itemImageId.trim();
  if (!/^[1-9][0-9]*$/u.test(normalized)) {
    throw new Error("item image ID must be a positive integer");
  }
  const resp = await client().getEditorManifest({ itemImageId: BigInt(normalized) });
  if (!resp.item) throw new Error("editor manifest response has no item");
  if (!resp.manifestJson.trim()) throw new Error("editor manifest response is empty");
  if (!resp.selectedCanvasId.trim()) throw new Error("editor manifest response has no selected Canvas");
  return {
    item: resp.item,
    manifestJSON: resp.manifestJson,
    selectedCanvasId: resp.selectedCanvasId,
  };
}

export async function getItemExportSnapshot(itemId: string): Promise<{
  item: Item;
  expectedRevisions: ItemImageRevision[];
}> {
  const resp = await client().getItem({ itemId });
  if (!resp.item) throw new Error("no item in response");
  if (resp.item.images.length === 0) throw new Error("item has no canonical annotation pages");
  if (resp.annotationRevisions.length !== resp.item.images.length) {
    throw new Error("item does not have a committed annotation page for every image");
  }
  const expectedRevisions = resp.annotationRevisions.map((revision, index) => {
    const image = resp.item!.images[index];
    if (image.id <= 0n || revision.itemImageId !== image.id || revision.revision <= 0n) {
      throw new Error("item annotation revision vector is invalid");
    }
    return revision;
  });
  return { item: resp.item, expectedRevisions };
}

export async function prepareItemExport(
  itemId: string,
  format: AnnotationExportFormat,
  expectedRevisions: ReadonlyArray<Pick<ItemImageRevision, "itemImageId" | "revision">>,
): Promise<PrepareItemExportResponse> {
  return client().prepareItemExport({
    expectedRevisions: [...expectedRevisions],
    format,
    itemId,
  });
}

export async function deleteItem(itemId: string): Promise<void> {
  await client().deleteItem({ itemId });
}

export async function listItemProviderCallAudits(itemId: string, limit = 100): Promise<ProviderCallAudit[]> {
  const resp = await client().listItemProviderCallAudits({ itemId, limit });
  return resp.audits;
}
