import {
  APIError,
  cancelUpload as cancelRemoteUpload,
  createUpload,
  getUpload,
  uploadChunk,
} from "./api.js";

const MAX_CONCURRENT_UPLOADS = 2;
const RETRY_DELAYS = [1000, 2500, 5000];
let nextLocalID = 1;

export class UploadQueue extends EventTarget {
  constructor() {
    super();
    this.items = [];
    this.activeCount = 0;
  }

  addFiles(files, directory) {
    for (const file of files) {
      const item = {
        localID: `local-${nextLocalID++}`,
        file,
        directory,
        remoteID: "",
        offset: 0,
        chunkSize: 8 * 1024 * 1024,
        status: "queued",
        speed: 0,
        error: "",
        result: null,
        abortController: null,
      };
      this.items.unshift(item);
    }
    this.notify();
    this.pump();
  }

  pause(localID) {
    const item = this.find(localID);
    if (!item || !["uploading", "retrying", "queued"].includes(item.status)) return;
    item.status = "paused";
    item.abortController?.abort();
    this.notify();
  }

  resume(localID) {
    const item = this.find(localID);
    if (!item || !["paused", "error"].includes(item.status)) return;
    item.status = "queued";
    item.error = "";
    this.notify();
    this.pump();
  }

  async cancel(localID) {
    const item = this.find(localID);
    if (!item) return;
    item.status = "cancelled";
    item.abortController?.abort();
    this.clearStoredSession(item);
    try {
      await cancelRemoteUpload(item.remoteID);
    } catch {
      // Local cancellation should still remove the task if the server session expired.
    }
    this.items = this.items.filter((candidate) => candidate.localID !== localID);
    this.notify();
    this.pump();
  }

  clearCompleted() {
    this.items = this.items.filter((item) => item.status !== "complete");
    this.notify();
  }

  find(localID) {
    return this.items.find((item) => item.localID === localID);
  }

  notify() {
    this.dispatchEvent(new CustomEvent("change", { detail: this.items }));
  }

  pump() {
    while (this.activeCount < MAX_CONCURRENT_UPLOADS) {
      const item = this.items.find((candidate) => candidate.status === "queued");
      if (!item) return;
      this.activeCount++;
      this.run(item)
        .catch((error) => {
          if (error?.name === "AbortError" || item.status === "paused" || item.status === "cancelled") {
            return;
          }
          item.status = "error";
          item.error = error?.message || "上传失败";
          this.notify();
        })
        .finally(() => {
          this.activeCount--;
          item.abortController = null;
          this.pump();
        });
    }
  }

  async run(item) {
    item.status = "uploading";
    this.notify();

    let status = await this.restoreOrCreate(item);
    item.remoteID = status.id;
    item.offset = status.offset;
    item.chunkSize = status.chunkSize;
    if (status.completed) {
      this.finish(item, status.file);
      return;
    }

    while (item.offset < item.file.size) {
      if (item.status !== "uploading" && item.status !== "retrying") return;
      const end = Math.min(item.offset + item.chunkSize, item.file.size);
      const chunk = item.file.slice(item.offset, end);
      const startedAt = performance.now();
      status = await this.sendWithRetry(item, chunk);
      const durationSeconds = Math.max((performance.now() - startedAt) / 1000, 0.01);
      item.speed = chunk.size / durationSeconds;
      item.offset = status.offset;
      item.status = "uploading";
      this.notify();

      if (status.completed) {
        this.finish(item, status.file);
        return;
      }
    }
  }

  async restoreOrCreate(item) {
    const storedID = localStorage.getItem(this.storageKey(item));
    if (storedID) {
      try {
        const status = await getUpload(storedID);
        if (
          status.name === item.file.name &&
          status.size === item.file.size &&
          status.directory === item.directory
        ) {
          item.remoteID = status.id;
          return status;
        }
      } catch (error) {
        if (!(error instanceof APIError) || error.status !== 404) throw error;
      }
      localStorage.removeItem(this.storageKey(item));
    }

    const status = await createUpload({
      directory: item.directory,
      name: item.file.name,
      size: item.file.size,
      lastModified: item.file.lastModified,
    });
    item.remoteID = status.id;
    localStorage.setItem(this.storageKey(item), status.id);
    return status;
  }

  async sendWithRetry(item, chunk) {
    let lastError;
    for (let attempt = 0; attempt <= RETRY_DELAYS.length; attempt++) {
      if (item.status === "paused" || item.status === "cancelled") {
        throw new DOMException("Upload stopped", "AbortError");
      }
      item.abortController = new AbortController();
      try {
        return await uploadChunk(
          item.remoteID,
          item.offset,
          chunk,
          item.abortController.signal,
        );
      } catch (error) {
        if (error?.name === "AbortError") throw error;
        lastError = error;
        if (error instanceof APIError && error.status === 409) {
          const status = await getUpload(item.remoteID);
          item.offset = status.offset;
          return status;
        }
        if (attempt >= RETRY_DELAYS.length) break;
        item.status = "retrying";
        item.error = "网络异常，正在重试";
        this.notify();
        await wait(RETRY_DELAYS[attempt]);
      }
    }
    throw lastError;
  }

  finish(item, file) {
    item.offset = item.file.size;
    item.status = "complete";
    item.result = file;
    item.speed = 0;
    item.error = "";
    this.clearStoredSession(item);
    this.notify();
    this.dispatchEvent(new CustomEvent("complete", { detail: item }));
  }

  storageKey(item) {
    return [
      "clawfiles-upload",
      item.directory,
      item.file.name,
      item.file.size,
      item.file.lastModified,
    ].join(":");
  }

  clearStoredSession(item) {
    localStorage.removeItem(this.storageKey(item));
  }
}

function wait(milliseconds) {
  return new Promise((resolve) => setTimeout(resolve, milliseconds));
}
