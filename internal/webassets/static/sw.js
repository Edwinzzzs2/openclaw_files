const CACHE_NAME = "clawfiles-shell-v19";
const LAN_CONNECT_TIMEOUT = 2500;
const STUN_CONNECT_TIMEOUT = 7000;
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
    request.method === "GET" &&
    url.origin === self.location.origin &&
    url.pathname.startsWith("/selection/archive/")
  ) {
    event.respondWith(fetchSelectionArchive(url));
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

async function fetchSelectionArchive(url) {
  const segments = url.pathname.split("/").filter(Boolean);
  const id = segments[segments.length - 1] || "";
  let plan;
  try {
    const response = await fetch("/api/selection/archive/plan", {
      method: "POST",
      credentials: "same-origin",
      headers: {
        "Accept": "application/json",
        "Content-Type": "application/json",
        "X-ClawFiles-Request": "1",
      },
      body: JSON.stringify({ id }),
    });
    if (!response.ok) return response;
    plan = await response.json();
  } catch {
    plan = null;
  }

  const transferResponse = await fetchPreferredTransfer(plan, {
    method: "GET",
    credentials: "omit",
    cache: "no-store",
  });
  if (transferResponse) return transferResponse;
  notifyTransferRoute("stable");

  return fetch(
    plan?.fallbackUrl || `/api/selection/archive/${encodeURIComponent(id)}`,
    {
      method: "GET",
      credentials: "same-origin",
      cache: "no-store",
    },
  );
}

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

  const transferResponse = await fetchPreferredTransfer(plan, {
    method: "GET",
    headers,
    credentials: "omit",
    cache: "no-store",
  });
  if (transferResponse) return transferResponse;
  notifyTransferRoute("stable");

  const fallbackURL = plan?.fallbackUrl ||
    `/api/content?path=${encodeURIComponent(path)}${download ? "&download=1" : ""}`;
  return fetch(fallbackURL, {
    method: "GET",
    headers,
    credentials: "same-origin",
    cache: "no-store",
  });
}

async function fetchPreferredTransfer(plan, options) {
  const candidates = [
    { route: "lan", url: plan?.lanUrl, timeout: LAN_CONNECT_TIMEOUT },
    { route: "stun", url: plan?.directUrl, timeout: STUN_CONNECT_TIMEOUT },
  ];
  for (const candidate of candidates) {
    if (!candidate.url) continue;
    const response = await fetchTransferCandidate(
      candidate.url,
      options,
      candidate.timeout,
    );
    if (response?.ok) {
      notifyTransferRoute(candidate.route);
      return response;
    }
    await response?.body?.cancel();
  }
  return null;
}

async function fetchTransferCandidate(url, options, timeout) {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), timeout);
  try {
    return await fetch(url, {
      ...options,
      signal: controller.signal,
    });
  } catch {
    return null;
  } finally {
    clearTimeout(timer);
  }
}

function notifyTransferRoute(route) {
  self.clients.matchAll({ type: "window", includeUncontrolled: true })
    .then((clients) => {
      for (const client of clients) {
        client.postMessage({
          type: "clawfiles:transfer-route",
          route,
        });
      }
    });
}
