const CACHE_NAME = "clawfiles-shell-v4";
const APP_SHELL = [
  "/",
  "/index.html",
  "/styles.css",
  "/app.js",
  "/api.js",
  "/uploads.js",
  "/manifest.webmanifest",
  "/icon.svg",
];

self.addEventListener("install", (event) => {
  event.waitUntil(
    caches.open(CACHE_NAME)
      .then((cache) => cache.addAll(APP_SHELL))
      .then(() => self.skipWaiting()),
  );
});

self.addEventListener("activate", (event) => {
  event.waitUntil(
    caches.keys()
      .then((keys) => Promise.all(
        keys.filter((key) => key !== CACHE_NAME).map((key) => caches.delete(key)),
      ))
      .then(() => self.clients.claim()),
  );
});

self.addEventListener("fetch", (event) => {
  const request = event.request;
  const url = new URL(request.url);
  if (
    request.method === "GET" &&
    url.origin === self.location.origin &&
    url.pathname === "/transfer/content"
  ) {
    event.respondWith(fetchTransferContent(request, url));
    return;
  }
  if (
    request.method !== "GET" ||
    url.origin !== self.location.origin ||
    url.pathname.startsWith("/api/")
  ) {
    return;
  }

  if (request.mode === "navigate") {
    event.respondWith(
      fetch(request)
        .then((response) => {
          const copy = response.clone();
          caches.open(CACHE_NAME).then((cache) => cache.put("/index.html", copy));
          return response;
        })
        .catch(() => caches.match("/index.html")),
    );
    return;
  }

  event.respondWith(
    caches.match(request).then((cached) => cached || fetch(request)),
  );
});

async function fetchTransferContent(request, url) {
  const path = url.searchParams.get("path") || "";
  const download = url.searchParams.get("download") === "1";
  const headers = new Headers();
  const range = request.headers.get("Range");
  if (range) headers.set("Range", range);

  let plan;
  try {
    const response = await fetch("/api/transfer/content", {
      method: "POST",
      credentials: "same-origin",
      headers: {
        "Accept": "application/json",
        "Content-Type": "application/json",
        "X-ClawFiles-Request": "1",
      },
      body: JSON.stringify({ path, download }),
    });
    if (!response.ok) return response;
    plan = await response.json();
  } catch {
    plan = null;
  }

  if (plan?.directUrl) {
    try {
      const directResponse = await fetch(plan.directUrl, {
        method: "GET",
        headers,
        credentials: "omit",
        cache: "no-store",
      });
      if (directResponse.ok) return directResponse;
    } catch {
      // Fall through to the stable origin.
    }
  }

  const fallbackURL = plan?.fallbackUrl ||
    `/api/content?path=${encodeURIComponent(path)}${download ? "&download=1" : ""}`;
  return fetch(fallbackURL, {
    method: "GET",
    headers,
    credentials: "same-origin",
    cache: "no-store",
  });
}
