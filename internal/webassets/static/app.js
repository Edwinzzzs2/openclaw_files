import {
  APIError,
  contentURL,
  createFolder,
  downloadURL,
  listFiles,
  loadConfig,
  loadRecent,
  login,
  logout,
  session,
} from "./api.js";
import { UploadQueue } from "./uploads.js";

const state = {
  currentPath: "",
  currentServerPath: "",
  entries: [],
  uploadTarget: "",
  pickerPath: "",
  view: "uploads",
  config: null,
};

const uploadQueue = new UploadQueue();
const elements = {
  root: document.documentElement,
  loginScreen: document.querySelector("#login-screen"),
  loginForm: document.querySelector("#login-form"),
  loginError: document.querySelector("#login-error"),
  passwordInput: document.querySelector("#password-input"),
  appShell: document.querySelector("#app-shell"),
  logoutButton: document.querySelector("#logout-button"),
  themeButton: document.querySelector("#theme-button"),
  searchWrap: document.querySelector("#search-wrap"),
  searchInput: document.querySelector("#search-input"),
  breadcrumbs: document.querySelector("#breadcrumbs"),
  currentServerPath: document.querySelector("#current-server-path"),
  entryCount: document.querySelector("#entry-count"),
  fileList: document.querySelector("#file-list"),
  filesEmpty: document.querySelector("#files-empty"),
  filesLoading: document.querySelector("#files-loading"),
  newFolderButton: document.querySelector("#new-folder-button"),
  filesUploadButton: document.querySelector("#files-upload-button"),
  refreshFilesButton: document.querySelector("#refresh-files-button"),
  uploadTargetPath: document.querySelector("#upload-target-path"),
  chooseTargetButton: document.querySelector("#choose-target-button"),
  filePicker: document.querySelector("#file-picker"),
  dropzone: document.querySelector("#dropzone"),
  uploadList: document.querySelector("#upload-list"),
  uploadsEmpty: document.querySelector("#uploads-empty"),
  clearCompletedButton: document.querySelector("#clear-completed-button"),
  recentList: document.querySelector("#recent-list"),
  recentEmpty: document.querySelector("#recent-empty"),
  recentLoading: document.querySelector("#recent-loading"),
  refreshRecentButton: document.querySelector("#refresh-recent-button"),
  previewDialog: document.querySelector("#preview-dialog"),
  previewTitle: document.querySelector("#preview-title"),
  previewMeta: document.querySelector("#preview-meta"),
  previewDownload: document.querySelector("#preview-download"),
  previewContent: document.querySelector("#preview-content"),
  folderDialog: document.querySelector("#folder-dialog"),
  folderForm: document.querySelector("#folder-form"),
  folderNameInput: document.querySelector("#folder-name-input"),
  folderError: document.querySelector("#folder-error"),
  pickerDialog: document.querySelector("#folder-picker-dialog"),
  pickerCurrentPath: document.querySelector("#picker-current-path"),
  pickerList: document.querySelector("#picker-list"),
  pickerUpButton: document.querySelector("#picker-up-button"),
  pickerConfirmButton: document.querySelector("#picker-confirm-button"),
  toast: document.querySelector("#toast"),
  toastTitle: document.querySelector("#toast-title"),
  toastMessage: document.querySelector("#toast-message"),
};

let toastTimer;

initialize();

async function initialize() {
  initializeTheme();
  bindEvents();
  registerServiceWorker();

  try {
    const currentSession = await session();
    if (currentSession.authRequired && !currentSession.authenticated) {
      showLogin();
      return;
    }
    await enterApplication();
  } catch (error) {
    showLogin(error?.message || "无法连接服务器");
  }
}

function bindEvents() {
  elements.loginForm.addEventListener("submit", handleLogin);
  elements.logoutButton.addEventListener("click", handleLogout);
  elements.themeButton.addEventListener("click", toggleTheme);
  elements.searchInput.addEventListener("input", renderFiles);
  elements.newFolderButton.addEventListener("click", openFolderDialog);
  elements.filesUploadButton.addEventListener("click", () => {
    setUploadTarget(state.currentPath);
    switchView("uploads");
    elements.filePicker.click();
  });
  elements.refreshFilesButton.addEventListener("click", () => loadDirectory(state.currentPath));
  elements.refreshRecentButton.addEventListener("click", refreshRecent);
  elements.chooseTargetButton.addEventListener("click", openFolderPicker);
  elements.filePicker.addEventListener("change", () => {
    addSelectedFiles(elements.filePicker.files);
    elements.filePicker.value = "";
  });
  elements.clearCompletedButton.addEventListener("click", () => uploadQueue.clearCompleted());
  elements.folderForm.addEventListener("submit", handleCreateFolder);
  elements.pickerUpButton.addEventListener("click", pickerGoUp);
  elements.pickerConfirmButton.addEventListener("click", confirmPickerPath);
  elements.fileList.addEventListener("click", handleFileAction);
  elements.recentList.addEventListener("click", handleFileAction);
  elements.uploadList.addEventListener("click", handleUploadAction);
  elements.breadcrumbs.addEventListener("click", handleBreadcrumbClick);
  elements.pickerList.addEventListener("click", handlePickerClick);

  document.querySelectorAll("[data-view]").forEach((button) => {
    button.addEventListener("click", () => switchView(button.dataset.view));
  });
  document.querySelectorAll("[data-close-dialog]").forEach((button) => {
    button.addEventListener("click", () => closeDialog(button.dataset.closeDialog));
  });

  elements.previewDialog.addEventListener("close", clearPreview);
  window.addEventListener("clawfiles:auth-required", () => showLogin("登录状态已失效"));
  window.addEventListener("hashchange", () => {
    const requested = location.hash.replace(/^#/, "");
    if (["files", "uploads", "recent"].includes(requested)) {
      switchView(requested, false);
    }
  });

  for (const eventName of ["dragenter", "dragover"]) {
    elements.dropzone.addEventListener(eventName, (event) => {
      event.preventDefault();
      elements.dropzone.classList.add("dragging");
    });
  }
  for (const eventName of ["dragleave", "drop"]) {
    elements.dropzone.addEventListener(eventName, (event) => {
      event.preventDefault();
      elements.dropzone.classList.remove("dragging");
    });
  }
  elements.dropzone.addEventListener("drop", (event) => addSelectedFiles(event.dataTransfer.files));

  uploadQueue.addEventListener("change", renderUploads);
  uploadQueue.addEventListener("complete", (event) => {
    const file = event.detail.result;
    showToast("上传完成", file?.serverPath || event.detail.file.name);
    refreshRecent();
    const uploadedParent = normalizePath(file?.path).split("/").slice(0, -1).join("/");
    if (uploadedParent === normalizePath(state.currentPath)) {
      loadDirectory(state.currentPath);
    }
  });
}

async function handleLogin(event) {
  event.preventDefault();
  elements.loginError.textContent = "";
  const submitButton = elements.loginForm.querySelector('button[type="submit"]');
  submitButton.disabled = true;
  try {
    await login(elements.passwordInput.value);
    elements.passwordInput.value = "";
    await enterApplication();
  } catch (error) {
    elements.loginError.textContent = error?.message || "登录失败";
    elements.passwordInput.select();
  } finally {
    submitButton.disabled = false;
  }
}

async function handleLogout() {
  try {
    await logout();
  } finally {
    showLogin();
  }
}

async function enterApplication() {
  elements.loginScreen.hidden = true;
  elements.appShell.hidden = false;
  state.config = await loadConfig();
  const requestedView = location.hash.replace(/^#/, "");
  const initialView = ["files", "uploads", "recent"].includes(requestedView)
    ? requestedView
    : "uploads";
  setUploadTarget(state.uploadTarget);
  await loadDirectory(state.currentPath);
  switchView(initialView);
}

function showLogin(message = "") {
  elements.appShell.hidden = true;
  elements.loginScreen.hidden = false;
  elements.loginError.textContent = message;
  queueMicrotask(() => elements.passwordInput.focus());
}

function switchView(view, updateHash = true) {
  state.view = view;
  document.querySelectorAll("[data-view-panel]").forEach((panel) => {
    panel.classList.toggle("active", panel.dataset.viewPanel === view);
  });
  document.querySelectorAll("[data-view]").forEach((button) => {
    button.classList.toggle("active", button.dataset.view === view);
  });
  elements.searchWrap.hidden = view !== "files";
  if (updateHash && location.hash !== `#${view}`) {
    history.replaceState(null, "", `#${view}`);
  }
  if (view === "recent") refreshRecent();
}

async function loadDirectory(path) {
  elements.filesLoading.hidden = false;
  elements.filesEmpty.hidden = true;
  elements.fileList.replaceChildren();
  try {
    const response = await listFiles(path);
    state.currentPath = response.path;
    state.currentServerPath = response.serverPath;
    state.entries = response.entries;
    renderBreadcrumbs();
    renderFiles();
  } catch (error) {
    showToast("无法读取目录", error?.message || "请求失败");
  } finally {
    elements.filesLoading.hidden = true;
  }
}

function renderBreadcrumbs() {
  const fragments = [];
  fragments.push('<button class="breadcrumb-button" type="button" data-path="">根目录</button>');
  let current = "";
  for (const segment of state.currentPath.split("/").filter(Boolean)) {
    current = current ? `${current}/${segment}` : segment;
    fragments.push('<span class="breadcrumb-separator">/</span>');
    fragments.push(
      `<button class="breadcrumb-button" type="button" data-path="${escapeHTML(current)}">${escapeHTML(segment)}</button>`,
    );
  }
  elements.breadcrumbs.innerHTML = fragments.join("");
  elements.currentServerPath.textContent = state.currentServerPath;
  elements.currentServerPath.title = state.currentServerPath;
}

function renderFiles() {
  const query = elements.searchInput.value.trim().toLocaleLowerCase();
  const entries = query
    ? state.entries.filter((entry) => entry.name.toLocaleLowerCase().includes(query))
    : state.entries;

  elements.entryCount.textContent = `${entries.length} 项`;
  elements.filesEmpty.hidden = entries.length > 0;
  elements.fileList.innerHTML = entries.map(fileRowTemplate).join("");
}

function fileRowTemplate(entry, recent = false) {
  const isDirectory = entry.type === "directory";
  const type = fileType(entry);
  const mobileMeta = isDirectory
    ? `文件夹，${formatDate(entry.modifiedAt)}`
    : `${formatBytes(entry.size)}，${formatDate(entry.modifiedAt)}`;
  const timeLabel = recent && entry.uploadedAt
    ? formatDate(entry.uploadedAt)
    : formatDate(entry.modifiedAt);
  const primaryAction = isDirectory
    ? `<button class="action-button" type="button" aria-label="打开文件夹" data-action="open" data-path="${escapeHTML(entry.path)}">${actionIconSVG("open")}<span>打开</span></button>`
    : entry.preview
      ? `<button class="action-button preview-action" type="button" aria-label="预览文件" data-action="preview" data-entry="${encodeEntry(entry)}">${actionIconSVG("preview")}<span>预览</span></button>`
      : "";
  const copyAction = `<button class="action-button" type="button" aria-label="复制服务器路径" data-action="copy" data-path="${escapeHTML(entry.serverPath)}">${actionIconSVG("copy")}<span>复制路径</span></button>`;
  const downloadAction = isDirectory
    ? ""
    : `<button class="action-button" type="button" aria-label="下载文件" data-action="download" data-entry="${encodeEntry(entry)}">${actionIconSVG("download")}<span>下载</span></button>`;

  return `
    <article class="file-row">
      <div class="file-name">
        <div class="type-block ${escapeHTML(type.className)}" title="${escapeHTML(type.label)}" aria-hidden="true">${fileIconSVG(type.icon)}</div>
        <div class="file-name-copy">
          <button class="file-name-button" type="button"
            aria-label="${escapeHTML(isDirectory ? `打开文件夹 ${entry.name}` : `打开文件 ${entry.name}`)}"
            data-action="${isDirectory ? "open" : (entry.preview ? "preview" : "download")}"
            data-path="${escapeHTML(entry.path)}"
            data-entry="${encodeEntry(entry)}">
            <strong>${escapeHTML(entry.name)}</strong>
          </button>
          <span class="file-mobile-meta">${escapeHTML(mobileMeta)}</span>
        </div>
      </div>
      <div class="file-size">${isDirectory ? "文件夹" : formatBytes(entry.size)}</div>
      <time class="file-time">${escapeHTML(timeLabel)}</time>
      <div class="file-actions">${primaryAction}${copyAction}${downloadAction}</div>
    </article>`;
}

function handleFileAction(event) {
  const button = event.target.closest("[data-action]");
  if (!button) return;
  const action = button.dataset.action;
  if (action === "open") {
    loadDirectory(button.dataset.path);
    switchView("files");
    return;
  }
  if (action === "copy") {
    copyText(button.dataset.path);
    return;
  }
  const entry = decodeEntry(button.dataset.entry);
  if (!entry) return;
  if (action === "preview") openPreview(entry);
  if (action === "download") startDownload(entry);
}

function handleBreadcrumbClick(event) {
  const button = event.target.closest("[data-path]");
  if (button) loadDirectory(button.dataset.path);
}

function openFolderDialog() {
  elements.folderError.textContent = "";
  elements.folderNameInput.value = "";
  elements.folderDialog.showModal();
  queueMicrotask(() => elements.folderNameInput.focus());
}

async function handleCreateFolder(event) {
  event.preventDefault();
  elements.folderError.textContent = "";
  try {
    await createFolder(state.currentPath, elements.folderNameInput.value);
    elements.folderDialog.close();
    showToast("文件夹已创建", elements.folderNameInput.value);
    await loadDirectory(state.currentPath);
  } catch (error) {
    elements.folderError.textContent = error?.message || "创建失败";
  }
}

function setUploadTarget(path) {
  state.uploadTarget = normalizePath(path);
  const hostPath = joinHostPath(state.config?.hostPathPrefix || "", state.uploadTarget);
  elements.uploadTargetPath.textContent = hostPath;
  elements.uploadTargetPath.title = hostPath;
}

function addSelectedFiles(files) {
  if (!files?.length) return;
  uploadQueue.addFiles(Array.from(files), state.uploadTarget);
  switchView("uploads");
}

function renderUploads() {
  const items = uploadQueue.items;
  elements.uploadsEmpty.hidden = items.length > 0;
  elements.clearCompletedButton.hidden = !items.some((item) => item.status === "complete");
  elements.uploadList.innerHTML = items.map(uploadItemTemplate).join("");
}

function uploadItemTemplate(item) {
  const confirmedOffset = Math.min(item.file.size, item.offset);
  const percent = item.status === "complete" || item.file.size === 0
    ? 100
    : Math.min(99, Math.floor((confirmedOffset / item.file.size) * 100));
  const chunkFullySent = item.inflightBytes > 0 &&
    item.inflightBytes >= Math.min(item.chunkSize, item.file.size - item.offset);
  const statusText = item.status === "uploading" && item.inflightBytes > 0
    ? chunkFullySent ? "等待服务器确认" : "正在发送分片"
    : uploadStatusText(item);
  const speed = item.speed > 0 ? `${formatBytes(item.speed)}/s` : "";
  const route = item.status !== "complete" && item.route === "stun"
    ? "STUN 高速通道"
    : "";
  const error = item.error ? `<span>${escapeHTML(item.error)}</span>` : "";
  const resultPath = item.result?.serverPath
    ? `<span>${escapeHTML(item.result.serverPath)}</span>`
    : "";
  const type = fileType({ name: item.file.name, type: "file", mime: item.file.type });

  let controls = "";
  if (["uploading", "retrying", "queued"].includes(item.status)) {
    controls += `<button class="action-button" type="button" data-upload-action="pause" data-id="${item.localID}">暂停</button>`;
  }
  if (["paused", "error"].includes(item.status)) {
    controls += `<button class="action-button" type="button" data-upload-action="resume" data-id="${item.localID}">继续</button>`;
  }
  if (item.status === "complete" && item.result?.serverPath) {
    controls += `<button class="action-button" type="button" data-upload-action="copy" data-id="${item.localID}">复制路径</button>`;
  }
  if (item.status !== "complete") {
    controls += `<button class="action-button danger" type="button" data-upload-action="cancel" data-id="${item.localID}">取消</button>`;
  }

  return `
    <article class="upload-item ${item.status === "error" ? "error" : ""}">
      <div class="type-block ${escapeHTML(type.className)}" title="${escapeHTML(type.label)}" aria-hidden="true">${fileIconSVG(type.icon)}</div>
      <div class="upload-item-main">
        <div class="upload-item-title">
          <strong>${escapeHTML(item.file.name)}</strong>
          <span class="upload-percent">${percent}%</span>
        </div>
        <div class="progress" role="progressbar" aria-label="${escapeHTML(item.file.name)} 上传进度"
          aria-valuenow="${percent}" aria-valuemin="0" aria-valuemax="100">
          <span style="transform:scaleX(${percent / 100})"></span>
        </div>
        <div class="upload-meta">
          <span>${escapeHTML(statusText)}</span>
          <span>已确认 ${formatBytes(confirmedOffset)} / ${formatBytes(item.file.size)}</span>
          ${item.inflightBytes > 0 ? `<span>本分片已发送 ${formatBytes(item.inflightBytes)}</span>` : ""}
          ${route ? `<span>${route}</span>` : ""}
          ${speed ? `<span>${speed}</span>` : ""}
          ${error}
          ${resultPath}
        </div>
      </div>
      <div class="upload-item-actions">${controls}</div>
    </article>`;
}

function handleUploadAction(event) {
  const button = event.target.closest("[data-upload-action]");
  if (!button) return;
  const id = button.dataset.id;
  switch (button.dataset.uploadAction) {
    case "pause":
      uploadQueue.pause(id);
      break;
    case "resume":
      uploadQueue.resume(id);
      break;
    case "cancel":
      uploadQueue.cancel(id);
      break;
    case "copy": {
      const item = uploadQueue.find(id);
      if (item?.result?.serverPath) copyText(item.result.serverPath);
      break;
    }
  }
}

async function refreshRecent() {
  elements.recentLoading.hidden = false;
  elements.recentEmpty.hidden = true;
  try {
    const response = await loadRecent();
    elements.recentList.innerHTML = response.entries
      .map((entry) => fileRowTemplate({ ...entry, type: "file" }, true))
      .join("");
    elements.recentEmpty.hidden = response.entries.length > 0;
  } catch (error) {
    showToast("无法读取最近上传", error?.message || "请求失败");
  } finally {
    elements.recentLoading.hidden = true;
  }
}

async function openFolderPicker() {
  state.pickerPath = state.uploadTarget;
  elements.pickerDialog.showModal();
  await renderPicker();
}

async function renderPicker() {
  elements.pickerList.innerHTML = '<div class="loading-state">正在读取目录</div>';
  try {
    const response = await listFiles(state.pickerPath);
    state.pickerPath = response.path;
    elements.pickerCurrentPath.textContent = response.serverPath;
    elements.pickerUpButton.disabled = !response.path;
    const directories = response.entries.filter((entry) => entry.type === "directory");
    elements.pickerList.innerHTML = directories.length
      ? directories.map((entry) => `
          <button class="picker-entry" type="button" data-picker-path="${escapeHTML(entry.path)}">
            <span class="picker-entry-icon" aria-hidden="true">${fileIconSVG("folder")}</span>
            <span>${escapeHTML(entry.name)}</span>
          </button>`).join("")
      : '<div class="empty-state compact"><span>没有子文件夹</span></div>';
  } catch (error) {
    elements.pickerList.innerHTML = `<div class="empty-state compact"><span>${escapeHTML(error?.message || "读取失败")}</span></div>`;
  }
}

function handlePickerClick(event) {
  const button = event.target.closest("[data-picker-path]");
  if (!button) return;
  state.pickerPath = button.dataset.pickerPath;
  renderPicker();
}

function pickerGoUp() {
  const segments = state.pickerPath.split("/").filter(Boolean);
  segments.pop();
  state.pickerPath = segments.join("/");
  renderPicker();
}

function confirmPickerPath() {
  setUploadTarget(state.pickerPath);
  elements.pickerDialog.close();
}

async function openPreview(entry) {
  elements.previewTitle.textContent = entry.name;
  elements.previewMeta.textContent = `${formatBytes(entry.size)}，${entry.serverPath}`;
  elements.previewDownload.href = downloadURL(entry.path);
  elements.previewDownload.download = entry.name;
  elements.previewContent.replaceChildren();
  elements.previewDialog.showModal();

  const source = contentURL(entry.path);
  if (entry.preview === "image") {
    const image = document.createElement("img");
    image.alt = entry.name;
    image.src = source;
    elements.previewContent.append(image);
    return;
  }
  if (entry.preview === "video") {
    const video = document.createElement("video");
    video.controls = true;
    video.preload = "metadata";
    video.src = source;
    elements.previewContent.append(video);
    return;
  }
  if (entry.preview === "audio") {
    const audio = document.createElement("audio");
    audio.controls = true;
    audio.preload = "metadata";
    audio.src = source;
    elements.previewContent.append(audio);
    return;
  }
  if (entry.preview === "pdf") {
    const frame = document.createElement("iframe");
    frame.title = entry.name;
    frame.src = source;
    elements.previewContent.append(frame);
    return;
  }
  if (entry.preview === "text") {
    const pre = document.createElement("pre");
    pre.textContent = "正在读取文本";
    elements.previewContent.append(pre);
    try {
      const response = await fetch(source, {
        headers: { Range: "bytes=0-1048575" },
        credentials: "same-origin",
      });
      if (!response.ok && response.status !== 206) {
        throw new APIError(`读取失败 (${response.status})`, response.status);
      }
      pre.textContent = await response.text();
      if (entry.size > 1048576) {
        pre.textContent += "\n\n[预览仅显示前 1 MiB]";
      }
    } catch (error) {
      pre.textContent = error?.message || "无法读取文本";
    }
    return;
  }
  const message = document.createElement("div");
  message.className = "preview-message";
  message.textContent = "此文件暂不支持在线预览，可以下载后打开。";
  elements.previewContent.append(message);
}

function clearPreview() {
  elements.previewContent.replaceChildren();
  elements.previewDownload.removeAttribute("href");
}

function startDownload(entry) {
  const anchor = document.createElement("a");
  anchor.href = downloadURL(entry.path);
  anchor.download = entry.name;
  document.body.append(anchor);
  anchor.click();
  anchor.remove();
}

function closeDialog(id) {
  const dialog = document.getElementById(id);
  if (dialog?.open) dialog.close();
}

async function copyText(value) {
  try {
    await navigator.clipboard.writeText(value);
  } catch {
    const textArea = document.createElement("textarea");
    textArea.value = value;
    textArea.style.position = "fixed";
    textArea.style.opacity = "0";
    document.body.append(textArea);
    textArea.select();
    document.execCommand("copy");
    textArea.remove();
  }
  showToast("路径已复制", value);
}

function showToast(title, message = "") {
  elements.toastTitle.textContent = title;
  elements.toastMessage.textContent = message;
  elements.toast.classList.add("visible");
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => elements.toast.classList.remove("visible"), 2600);
}

function initializeTheme() {
  const storedTheme = localStorage.getItem("clawfiles-theme");
  const theme = storedTheme || (matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light");
  applyTheme(theme);
}

function toggleTheme() {
  applyTheme(elements.root.dataset.theme === "dark" ? "light" : "dark");
}

function applyTheme(theme) {
  elements.root.dataset.theme = theme;
  localStorage.setItem("clawfiles-theme", theme);
  const themeColor = document.querySelector('meta[name="theme-color"]');
  themeColor.content = theme === "dark" ? "#111415" : "#f5f7f8";
}

function uploadStatusText(item) {
  switch (item.status) {
    case "queued":
      return "等待上传";
    case "uploading":
      return "上传中";
    case "retrying":
      return "正在重试";
    case "paused":
      return "已暂停";
    case "complete":
      return "上传完成";
    case "error":
      return "上传失败";
    default:
      return "";
  }
}

function fileType(entry) {
  if (entry.type === "directory") {
    return { label: "文件夹", className: "folder", icon: "folder" };
  }
  const extension = fileExtension(entry.name);
  const mime = String(entry.mime || "").toLowerCase();
  if (["zip", "rar", "7z", "tar", "gz", "bz2", "xz", "zst", "tgz"].includes(extension)) {
    return { label: "压缩包", className: "archive", icon: "archive" };
  }
  if (extension === "apk" || extension === "xapk" || extension === "aab") {
    return { label: "Android 应用", className: "app-package", icon: "app" };
  }
  if (["exe", "msi", "appimage", "deb", "rpm", "pkg", "dmg"].includes(extension)) {
    return { label: "安装程序", className: "executable", icon: "terminal" };
  }
  if (extension === "pdf" || mime === "application/pdf") {
    return { label: "PDF 文档", className: "pdf", icon: "pdf" };
  }
  if (entry.preview === "image" || mime.startsWith("image/")) {
    return { label: "图片", className: "image", icon: "image" };
  }
  if (entry.preview === "video" || mime.startsWith("video/")) {
    return { label: "视频", className: "video", icon: "video" };
  }
  if (entry.preview === "audio" || mime.startsWith("audio/")) {
    return { label: "音频", className: "audio", icon: "audio" };
  }
  if (["doc", "docx", "odt", "rtf", "pages"].includes(extension)) {
    return { label: "文档", className: "document", icon: "document" };
  }
  if (["xls", "xlsx", "ods", "csv", "numbers"].includes(extension)) {
    return { label: "表格", className: "spreadsheet", icon: "spreadsheet" };
  }
  if (["ppt", "pptx", "odp", "key"].includes(extension)) {
    return { label: "演示文稿", className: "presentation", icon: "presentation" };
  }
  if (["go", "js", "jsx", "ts", "tsx", "html", "css", "scss", "vue", "py", "java", "rs", "php", "sh", "ps1", "sql", "xml"].includes(extension)) {
    return { label: "代码", className: "code", icon: "code" };
  }
  if (["txt", "md", "markdown", "log", "json", "yaml", "yml", "toml", "ini", "conf"].includes(extension) || entry.preview === "text") {
    return { label: "文本", className: "text-file", icon: "text" };
  }
  if (extension === "torrent") {
    return { label: "种子文件", className: "torrent", icon: "magnet" };
  }
  if (["iso", "img", "vhd", "vhdx"].includes(extension)) {
    return { label: "磁盘镜像", className: "disk-image", icon: "disc" };
  }
  return { label: extension ? `${extension.toUpperCase()} 文件` : "文件", className: "generic", icon: "file" };
}

function fileExtension(name) {
  const dot = name.lastIndexOf(".");
  if (dot <= 0 || dot === name.length - 1) return "";
  return name.slice(dot + 1).toLowerCase();
}

function fileIconSVG(kind) {
  const paths = {
    folder: '<path class="icon-fill" d="M3.5 7.25h6.1l1.8 2H20.5v8.25a2 2 0 0 1-2 2h-13a2 2 0 0 1-2-2V7.25Z"/><path d="M3.5 7.25V5.7a1.7 1.7 0 0 1 1.7-1.7h4.1l2 2.25h7.2a2 2 0 0 1 2 2v1"/>',
    archive: '<path d="M6 3.5h12v17H6z"/><path d="M10 3.5v3h4v3h-4v3h4v3h-4"/><path d="M10 18.5h4"/>',
    app: '<rect x="4" y="4" width="6" height="6" rx="1.5"/><rect x="14" y="4" width="6" height="6" rx="1.5"/><rect x="4" y="14" width="6" height="6" rx="1.5"/><rect x="14" y="14" width="6" height="6" rx="1.5"/>',
    terminal: '<rect x="3.5" y="4.5" width="17" height="15" rx="2"/><path d="m7 9 2.5 2.5L7 14m5.5 0H17"/>',
    pdf: '<path d="M6 2.75h8l4 4v14.5H6z"/><path d="M14 2.75v4h4"/><path d="M8.5 16.5h7M8.5 13h7"/>',
    image: '<rect x="3.5" y="4" width="17" height="16" rx="2"/><circle cx="9" cy="9" r="1.5"/><path d="m5.5 17 4.25-4.5 3 3 2-2 3.75 3.5"/>',
    video: '<rect x="3.5" y="5" width="17" height="14" rx="2"/><path class="icon-fill" d="m10 9 5 3-5 3V9Z"/>',
    audio: '<path d="M9 17.5V6l9-2v11.5"/><ellipse cx="6.5" cy="17.5" rx="2.5" ry="2"/><ellipse cx="15.5" cy="15.5" rx="2.5" ry="2"/>',
    document: '<path d="M6 2.75h8l4 4v14.5H6z"/><path d="M14 2.75v4h4M9 11h6M9 14.5h6M9 18h4"/>',
    spreadsheet: '<rect x="4" y="3" width="16" height="18" rx="1.5"/><path d="M4 8h16M4 13h16M4 17h16M10 8v13M15 8v13"/>',
    presentation: '<path d="M4 4h16v12H4zM8 20l4-4 4 4"/><path d="M12 8v4l3-2-3-2Z"/>',
    code: '<path d="m9 7-5 5 5 5m6-10 5 5-5 5m-2.5-12-2 14"/>',
    text: '<path d="M6 2.75h8l4 4v14.5H6z"/><path d="M14 2.75v4h4M9 12h6M9 15.5h6M9 19h4"/>',
    magnet: '<path d="M6 4v8a6 6 0 0 0 12 0V4M6 8h4m4 0h4"/><path d="M6 4h4v4H6zm8 0h4v4h-4z"/>',
    disc: '<circle cx="12" cy="12" r="8.5"/><circle cx="12" cy="12" r="2.25"/><path d="M12 3.5V8m0 8v4.5"/>',
    file: '<path d="M6 2.75h8l4 4v14.5H6z"/><path d="M14 2.75v4h4"/>',
  };
  return `<svg class="file-icon" viewBox="0 0 24 24" aria-hidden="true">${paths[kind] || paths.file}</svg>`;
}

function actionIconSVG(kind) {
  const paths = {
    open: '<path d="M3.5 7.5h6l1.7 2H20.5v8.75A1.75 1.75 0 0 1 18.75 20H5.25A1.75 1.75 0 0 1 3.5 18.25V7.5Z"/>',
    preview: '<path d="M2.75 12s3.25-5.5 9.25-5.5 9.25 5.5 9.25 5.5-3.25 5.5-9.25 5.5S2.75 12 2.75 12Z"/><circle cx="12" cy="12" r="2.5"/>',
    copy: '<rect x="8" y="8" width="11" height="11" rx="2"/><path d="M16 8V6a2 2 0 0 0-2-2H6a2 2 0 0 0-2 2v8a2 2 0 0 0 2 2h2"/>',
    download: '<path d="M12 3.5v11m0 0 4-4m-4 4-4-4"/><path d="M5 18.5h14"/>',
  };
  return `<svg class="action-icon" viewBox="0 0 24 24" aria-hidden="true">${paths[kind] || ""}</svg>`;
}

function formatBytes(bytes) {
  if (!Number.isFinite(bytes) || bytes < 0) return "";
  if (bytes < 1024) return `${bytes} B`;
  const units = ["KiB", "MiB", "GiB", "TiB"];
  let value = bytes;
  let unit = "";
  for (const candidate of units) {
    value /= 1024;
    unit = candidate;
    if (value < 1024) break;
  }
  const precision = value >= 100 ? 0 : value >= 10 ? 1 : 2;
  return `${value.toFixed(precision)} ${unit}`;
}

function formatDate(value) {
  const date = new Date(value);
  if (Number.isNaN(date.valueOf())) return "";
  const now = new Date();
  const sameDay = date.toDateString() === now.toDateString();
  if (sameDay) {
    return `今天 ${date.toLocaleTimeString("zh-CN", { hour: "2-digit", minute: "2-digit", hour12: false })}`;
  }
  return date.toLocaleString("zh-CN", {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    hour12: false,
  });
}

function normalizePath(path) {
  return String(path || "")
    .split("/")
    .filter((segment) => segment && segment !== ".")
    .join("/");
}

function joinHostPath(prefix, relative) {
  const cleanPrefix = String(prefix || "").replace(/[\\/]+$/, "");
  return relative ? `${cleanPrefix}/${relative}` : cleanPrefix;
}

function encodeEntry(entry) {
  return escapeHTML(encodeURIComponent(JSON.stringify(entry)));
}

function decodeEntry(value) {
  try {
    return JSON.parse(decodeURIComponent(value));
  } catch {
    return null;
  }
}

function escapeHTML(value) {
  return String(value ?? "")
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&#039;");
}

function registerServiceWorker() {
  if ("serviceWorker" in navigator) {
    navigator.serviceWorker.register("/sw.js").catch(() => {
      // The web app still works when service workers are unavailable.
    });
  }
}
