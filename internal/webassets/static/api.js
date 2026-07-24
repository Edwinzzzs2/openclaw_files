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

export function uploadChunk(id, offset, chunk, signal) {
  return apiFetch(`/api/uploads/${encodeURIComponent(id)}`, {
    method: "PATCH",
    headers: {
      "Content-Type": "application/offset+octet-stream",
      "Upload-Offset": String(offset),
    },
    body: chunk,
    signal,
  });
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
