import {
  APIError,
  cancelUpload as cancelRemoteUpload,
  createUpload,
  getUpload,
  loadTransferStatus,
  probeUploadEndpoint,
  uploadChunk,
} from "./api.js";

const IOS_DEVICE = isIOSDevice();
const MAX_CONCURRENT_UPLOADS = IOS_DEVICE ? 1 : 2;
const DEFAULT_SAFE_CHUNK_SIZE = 2 * 1024 * 1024;
const IOS_SAFE_CHUNK_SIZE = 128 * 1024;
const CLIENT_SAFE_CHUNK_SIZE = IOS_DEVICE
  ? IOS_SAFE_CHUNK_SIZE
  : DEFAULT_SAFE_CHUNK_SIZE;
const LAN_PROBE_TIMEOUT = 2500;
const LAN_PROBE_SUCCESS_CACHE = 30_000;
const LAN_PROBE_FAILURE_CACHE = 10_000;
const TRANSFER_DISCOVERY_TIMEOUT = 5000;
const RETRY_DELAYS = [1000, 2500, 5000];
let nextLocalID = 1;

export class UploadQueue extends EventTarget {
  constructor() {
    super();
    this.items = [];
    this.activeCount = 0;
    this.lastProgressNotification = -Infinity;
    this.progressNotificationTimer = 0;
    this.lanProbeEndpoint = "";
    this.lanProbeAvailable = false;
    this.lanProbeCheckedAt = 0;
    this.lanProbePromise = null;
  }

  addFiles(files, directory) {
    for (const file of files) {
      const item = {
        localID: `local-${nextLocalID++}`,
        file,
        directory,
        remoteID: "",
        offset: 0,
        inflightBytes: 0,
        chunkSize: CLIENT_SAFE_CHUNK_SIZE,
        transferToken: "",
        transferEndpoint: "",
        lanEndpoint: "",
        stunEndpoint: "",
        route: "stable",
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
    item.inflightBytes = 0;
    item.speed = 0;
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
    item.inflightBytes = 0;
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
    item.chunkSize = Math.min(status.chunkSize, CLIENT_SAFE_CHUNK_SIZE);
    item.transferToken = status.transferToken || "";
    await this.selectInitialTransferRoute(item);
    this.notify();
    if (status.completed) {
      this.finish(item, status.file);
      return;
    }

    while (item.offset < item.file.size) {
      if (item.status !== "uploading" && item.status !== "retrying") return;
      const end = Math.min(item.offset + item.chunkSize, item.file.size);
      const sourceChunk = item.file.slice(item.offset, end);
      const chunk = IOS_DEVICE
        ? await materializeChunk(sourceChunk)
        : sourceChunk;
      const chunkLength = end - item.offset;
      const startedAt = performance.now();
      status = await this.sendWithRetry(item, chunk, chunkLength);
      const durationSeconds = Math.max((performance.now() - startedAt) / 1000, 0.01);
      const measuredSpeed = chunkLength / durationSeconds;
      item.speed = item.speed > 0
        ? (item.speed * 0.78) + (measuredSpeed * 0.22)
        : measuredSpeed;
      item.offset = status.offset;
      item.inflightBytes = 0;
      item.status = "uploading";
      item.error = "";
      this.notifyProgress();

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
          item.transferToken = status.transferToken || "";
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
    item.transferToken = status.transferToken || "";
    localStorage.setItem(this.storageKey(item), status.id);
    return status;
  }

  async refreshTransferPlan(item) {
    const controller = new AbortController();
    const timeout = setTimeout(() => controller.abort(), TRANSFER_DISCOVERY_TIMEOUT);
    try {
      const transfer = await loadTransferStatus({ signal: controller.signal });
      item.lanEndpoint = normalizeEndpoint(transfer.lanBaseUrl);
      item.stunEndpoint = normalizeEndpoint(
        transfer.stunBaseUrl || transfer.baseUrl,
      );
      return true;
    } catch {
      return false;
    } finally {
      clearTimeout(timeout);
    }
  }

  async selectInitialTransferRoute(item) {
    await this.refreshTransferPlan(item);
    await this.selectPreferredTransferRoute(item);
  }

  async selectPreferredTransferRoute(item) {
    if (
      item.lanEndpoint &&
      item.transferToken &&
      await this.probeLANEndpoint(item)
    ) {
      this.setTransferRoute(item, "lan", item.lanEndpoint);
      return;
    }
    if (item.stunEndpoint && item.transferToken) {
      this.setTransferRoute(item, "stun", item.stunEndpoint);
      return;
    }
    this.setTransferRoute(item, "stable", "");
  }

  async probeLANEndpoint(item) {
    const endpoint = item.lanEndpoint;
    const cacheDuration = this.lanProbeAvailable
      ? LAN_PROBE_SUCCESS_CACHE
      : LAN_PROBE_FAILURE_CACHE;
    if (
      endpoint === this.lanProbeEndpoint &&
      this.lanProbeCheckedAt > 0 &&
      Date.now() - this.lanProbeCheckedAt < cacheDuration
    ) {
      return this.lanProbeAvailable;
    }
    if (endpoint === this.lanProbeEndpoint && this.lanProbePromise) {
      return this.lanProbePromise;
    }

    const probe = this.performLANProbe(item);
    this.lanProbeEndpoint = endpoint;
    this.lanProbePromise = probe;
    try {
      const available = await probe;
      if (this.lanProbeEndpoint === endpoint && this.lanProbePromise === probe) {
        this.lanProbeAvailable = available;
        this.lanProbeCheckedAt = Date.now();
      }
      return available;
    } finally {
      if (this.lanProbePromise === probe) this.lanProbePromise = null;
    }
  }

  async performLANProbe(item) {
    const controller = new AbortController();
    const timeout = setTimeout(() => controller.abort(), LAN_PROBE_TIMEOUT);
    try {
      await probeUploadEndpoint(
        item.remoteID,
        item.lanEndpoint,
        item.transferToken,
        controller.signal,
      );
      return true;
    } catch {
      return false;
    } finally {
      clearTimeout(timeout);
    }
  }

  async advanceTransferRoute(item) {
    const failedRoute = item.route;
    const failedEndpoint = item.transferEndpoint;
    await this.refreshTransferPlan(item);

    if (failedRoute === "lan") {
      this.lanProbeAvailable = false;
      this.lanProbeCheckedAt = Date.now();
      if (item.stunEndpoint && item.transferToken) {
        this.setTransferRoute(item, "stun", item.stunEndpoint);
      } else {
        this.setTransferRoute(item, "stable", "");
      }
      return;
    }
    if (
      failedRoute === "stun" &&
      item.stunEndpoint &&
      item.transferToken &&
      item.stunEndpoint !== failedEndpoint
    ) {
      this.setTransferRoute(item, "stun", item.stunEndpoint);
      return;
    }
    if (failedRoute === "stable") {
      await this.selectPreferredTransferRoute(item);
      return;
    }
    this.setTransferRoute(item, "stable", "");
  }

  setTransferRoute(item, route, endpoint) {
    const previousRoute = item.route;
    item.transferEndpoint = endpoint;
    item.route = route;
    if (route !== previousRoute) {
      this.dispatchEvent(new CustomEvent("routechange", {
        detail: { route },
      }));
    }
  }

  transferRetryMessage(failedRoute, nextRoute) {
    if (failedRoute === "lan" && nextRoute === "stun") {
      return "局域网不可用，正在切换高速通道";
    }
    if (failedRoute === "lan") {
      return "局域网不可用，正在通过稳定通道重试";
    }
    if (failedRoute === "stun" && nextRoute === "stun") {
      return "高速通道已更新，正在重试";
    }
    if (failedRoute === "stun") {
      return "高速通道不可用，正在通过稳定通道重试";
    }
    return "网络异常，正在重试";
  }

  shouldAdvanceTransferRoute(item, error) {
    if (!(error instanceof APIError)) return false;
    if (error.status === 0) return true;
    return item.route !== "stable" &&
      [401, 403, 404, 413, 421, 502, 503, 504].includes(error.status);
  }

  async sendWithRetry(item, chunk, chunkLength) {
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
          (loaded) => {
            item.inflightBytes = Math.min(loaded, chunkLength);
          },
          {
            baseURL: item.transferEndpoint,
            transferToken: item.transferEndpoint ? item.transferToken : "",
          },
        );
      } catch (error) {
        item.inflightBytes = 0;
        item.speed = 0;
        if (error?.name === "AbortError") throw error;
        lastError = error;
        if (error instanceof APIError && error.status === 409) {
          const status = await getUpload(item.remoteID);
          item.offset = status.offset;
          item.transferToken = status.transferToken || item.transferToken;
          return status;
        }
        if (this.shouldAdvanceTransferRoute(item, error)) {
          const failedRoute = item.route;
          await this.advanceTransferRoute(item);
          item.error = this.transferRetryMessage(failedRoute, item.route);
        }
        if (attempt >= RETRY_DELAYS.length) break;
        item.status = "retrying";
        if (!item.error) item.error = "网络异常，正在重试";
        this.notify();
        await wait(RETRY_DELAYS[attempt]);
      }
    }
    throw lastError;
  }

  finish(item, file) {
    item.offset = item.file.size;
    item.inflightBytes = 0;
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

  notifyProgress() {
    const now = performance.now();
    const elapsed = now - this.lastProgressNotification;
    if (elapsed >= 250) {
      clearTimeout(this.progressNotificationTimer);
      this.progressNotificationTimer = 0;
      this.lastProgressNotification = now;
      this.notify();
      return;
    }
    if (this.progressNotificationTimer) return;
    this.progressNotificationTimer = setTimeout(() => {
      this.progressNotificationTimer = 0;
      this.lastProgressNotification = performance.now();
      this.notify();
    }, 250 - elapsed);
  }
}

function wait(milliseconds) {
  return new Promise((resolve) => setTimeout(resolve, milliseconds));
}

function normalizeEndpoint(value) {
  return String(value || "").replace(/\/+$/, "");
}

function isIOSDevice() {
  const userAgent = navigator.userAgent || "";
  return /iPad|iPhone|iPod/.test(userAgent) ||
    (navigator.platform === "MacIntel" && navigator.maxTouchPoints > 1);
}

async function materializeChunk(blob) {
  if (typeof blob.arrayBuffer === "function") {
    return blob.arrayBuffer();
  }
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.addEventListener("load", () => resolve(reader.result));
    reader.addEventListener("error", () => reject(reader.error || new Error("无法读取文件分片")));
    reader.readAsArrayBuffer(blob);
  });
}
