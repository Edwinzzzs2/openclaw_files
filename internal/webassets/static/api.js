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

export function listFiles(path = "") {
  return apiFetch(`/api/files?path=${encodeURIComponent(path)}`);
}

export function createFolder(path, name) {
  return apiFetch("/api/folders", {
    method: "POST",
    body: { path, name },
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

export function uploadChunk(id, offset, chunk, signal, onProgress) {
  return new Promise((resolve, reject) => {
    const request = new XMLHttpRequest();
    const abortRequest = () => request.abort();
    const cleanup = () => signal?.removeEventListener("abort", abortRequest);

    request.open("PATCH", `/api/uploads/${encodeURIComponent(id)}`);
    request.responseType = "text";
    request.withCredentials = true;
    request.setRequestHeader("Accept", "application/json");
    request.setRequestHeader("Content-Type", "application/offset+octet-stream");
    request.setRequestHeader("Upload-Offset", String(offset));
    request.setRequestHeader("X-ClawFiles-Request", "1");

    request.upload.addEventListener("progress", (event) => {
      onProgress?.(event.loaded, event.total || chunk.size);
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
    request.addEventListener("abort", () => {
      cleanup();
      reject(new DOMException("Upload stopped", "AbortError"));
    });

    if (signal?.aborted) {
      reject(new DOMException("Upload stopped", "AbortError"));
      return;
    }
    signal?.addEventListener("abort", abortRequest, { once: true });
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
