const unsafeMethods = new Set(["POST", "PUT", "PATCH", "DELETE"]);

export class APIError extends Error {
  constructor(message, status) {
    super(message);
    this.name = "APIError";
    this.status = status;
  }
}

export async function apiFetch(path, options = {}) {
  const method = (options.method || "GET").toUpperCase();
  const headers = new Headers(options.headers || {});
  headers.set("Accept", "application/json");
  if (unsafeMethods.has(method)) {
    headers.set("X-ClawFiles-Request", "1");
  }

  let body = options.body;
  if (body !== undefined && body !== null && !(body instanceof Blob) && typeof body !== "string") {
    headers.set("Content-Type", "application/json");
    body = JSON.stringify(body);
  }

  const response = await fetch(path, {
    ...options,
    method,
    headers,
    body,
    credentials: "same-origin",
  });

  if (response.status === 401) {
    window.dispatchEvent(new CustomEvent("clawfiles:auth-required"));
  }

  const contentType = response.headers.get("Content-Type") || "";
  const payload = contentType.includes("application/json")
    ? await response.json()
    : null;

  if (!response.ok) {
    throw new APIError(payload?.error || `请求失败 (${response.status})`, response.status);
  }
  return payload;
}

export function session() {
  return apiFetch("/api/session");
}

export function login(password) {
  return apiFetch("/api/auth/login", {
    method: "POST",
    body: { password },
  });
}

export function logout() {
  return apiFetch("/api/auth/logout", { method: "POST" });
}

export function loadConfig() {
  return apiFetch("/api/config");
}

export function loadTransferStatus(options = {}) {
  return apiFetch("/api/transfer", options);
}

export async function probeTransferEndpoint(baseURL, signal) {
  const origin = String(baseURL || "").replace(/\/+$/, "");
  if (!origin) throw new APIError("传输通道配置无效", 0);
  let response;
  try {
    response = await fetch(`${origin}/api/health`, {
      method: "GET",
      credentials: "omit",
      cache: "no-store",
      signal,
    });
  } catch (error) {
    if (error?.name === "AbortError") throw error;
    throw new APIError("传输通道不可用", 0);
  }
  if (!response.ok) {
    throw new APIError(`传输通道不可用 (${response.status})`, response.status);
  }
  return true;
}

export function listFiles(path = "") {
  return apiFetch(`/api/files?path=${encodeURIComponent(path)}`);
}

export function createFolder(path, name) {
  return apiFetch("/api/folders", {
    method: "POST",
    body: { path, name },
  });
}

export function prepareSelectionArchive(directory, paths) {
  return apiFetch("/api/selection/archive", {
    method: "POST",
    body: { directory, paths },
  });
}

export function deleteSelection(directory, paths) {
  return apiFetch("/api/selection/delete", {
    method: "POST",
    body: { directory, paths },
  });
}

export function renameSelection(directory, path, name) {
  return apiFetch("/api/selection/rename", {
    method: "POST",
    body: { directory, path, name },
  });
}

export function moveSelection(directory, paths, destination) {
  return apiFetch("/api/selection/move", {
    method: "POST",
    body: { directory, paths, destination },
  });
}

export function loadRecent() {
  return apiFetch("/api/recent");
}

export function createUpload({ directory, name, size, lastModified }) {
  return apiFetch("/api/uploads", {
    method: "POST",
    body: { directory, name, size, lastModified },
  });
}

export function getUpload(id) {
  return apiFetch(`/api/uploads/${encodeURIComponent(id)}`);
}

export async function probeUploadEndpoint(id, baseURL, transferToken, signal) {
  const origin = String(baseURL || "").replace(/\/+$/, "");
  if (!origin || !transferToken) {
    throw new APIError("传输通道配置无效", 0);
  }
  let response;
  try {
    response = await fetch(`${origin}/api/uploads/${encodeURIComponent(id)}`, {
      method: "HEAD",
      headers: {
        "Accept": "application/json",
        "X-ClawFiles-Transfer-Token": transferToken,
      },
      credentials: "omit",
      cache: "no-store",
      signal,
    });
  } catch (error) {
    if (error?.name === "AbortError") throw error;
    throw new APIError("传输通道不可用", 0);
  }
  if (!response.ok) {
    throw new APIError(`传输通道不可用 (${response.status})`, response.status);
  }
  return true;
}

export function uploadChunk(id, offset, chunk, signal, onProgress, options = {}) {
  return new Promise((resolve, reject) => {
    const noProgressTimeout = 20_000;
    const requestTimeout = 60_000;
    const request = new XMLHttpRequest();
    const baseURL = String(options.baseURL || "").replace(/\/+$/, "");
    const chunkLength = chunk instanceof ArrayBuffer
      ? chunk.byteLength
      : chunk.size;
    let watchdog = 0;
    let timedOut = false;
    let lastLoaded = 0;
    const abortRequest = () => request.abort();
    const cleanup = () => {
      clearTimeout(watchdog);
      signal?.removeEventListener("abort", abortRequest);
    };
    const armWatchdog = () => {
      clearTimeout(watchdog);
      watchdog = setTimeout(() => {
        timedOut = true;
        request.abort();
      }, noProgressTimeout);
    };

    request.open("PATCH", `${baseURL}/api/uploads/${encodeURIComponent(id)}`);
    request.responseType = "text";
    request.timeout = requestTimeout;
    request.withCredentials = !options.transferToken;
    request.setRequestHeader("Accept", "application/json");
    request.setRequestHeader("Content-Type", "application/offset+octet-stream");
    request.setRequestHeader("Upload-Offset", String(offset));
    request.setRequestHeader("Upload-Chunk-Length", String(chunkLength));
    request.setRequestHeader("X-ClawFiles-Request", "1");
    if (options.transferToken) {
      request.setRequestHeader("X-ClawFiles-Transfer-Token", options.transferToken);
    }

    request.upload.addEventListener("progress", (event) => {
      const loaded = Math.min(event.loaded, chunkLength);
      if (loaded <= lastLoaded) return;
      lastLoaded = loaded;
      armWatchdog();
      onProgress?.(loaded, event.total || chunkLength);
    });
    request.addEventListener("load", () => {
      cleanup();
      const payload = parseJSONResponse(request.responseText);
      if (request.status === 401) {
        window.dispatchEvent(new CustomEvent("clawfiles:auth-required"));
      }
      if (request.status < 200 || request.status >= 300) {
        reject(new APIError(payload?.error || `请求失败 (${request.status})`, request.status));
        return;
      }
      resolve(payload);
    });
    request.addEventListener("error", () => {
      cleanup();
      reject(new APIError("网络连接失败", 0));
    });
    request.addEventListener("timeout", () => {
      cleanup();
      timedOut = true;
      reject(new APIError("上传请求超时，正在重试", 0));
    });
    request.addEventListener("abort", () => {
      cleanup();
      if (timedOut) {
        reject(new APIError("上传连接长时间没有进度", 0));
      } else {
        reject(new DOMException("Upload stopped", "AbortError"));
      }
    });

    if (signal?.aborted) {
      reject(new DOMException("Upload stopped", "AbortError"));
      return;
    }
    signal?.addEventListener("abort", abortRequest, { once: true });
    armWatchdog();
    request.send(chunk);
  });
}

function parseJSONResponse(value) {
  if (!value) return null;
  try {
    return JSON.parse(value);
  } catch {
    return null;
  }
}

export async function cancelUpload(id) {
  if (!id) return;
  await apiFetch(`/api/uploads/${encodeURIComponent(id)}`, {
    method: "DELETE",
  });
}

export function contentURL(path, download = false) {
  const query = new URLSearchParams({ path });
  if (download) query.set("download", "1");
  return `/api/content?${query.toString()}`;
}

export function downloadURL(path, download = true) {
  const query = new URLSearchParams({ path });
  if (download) query.set("download", "1");
  return `/transfer/content?${query.toString()}`;
}
