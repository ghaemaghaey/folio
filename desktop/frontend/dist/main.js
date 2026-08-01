/**
 * Folio reader — distraction-free chrome, PDF cache, EPUB pages/TOC/links,
 * image lightbox, viewport reading guide.
 */

const THEMES = ["sepia", "light", "dark"];
/** Fixed render DPI — zoom is CSS-only (no re-render). Disk-cached at this DPI. */
const BASE_DPI = 128;
const ZOOM_MIN = 0.5;
const ZOOM_MAX = 3;
const ZOOM_STEP = 0.1;

const FONTS_EN = [
  { id: "literata", label: "Literata" },
  { id: "source-serif", label: "Source Serif" },
  { id: "merriweather", label: "Merriweather" },
  { id: "ibm-plex-serif", label: "IBM Plex Serif" },
  { id: "georgia", label: "Georgia" },
];
const FONTS_FA = [
  { id: "vazirmatn", label: "وزیرمتن" },
  { id: "samim", label: "صمیم" },
  { id: "shabnam", label: "شبنم" },
  { id: "sahel", label: "ساحل" },
  { id: "tanha", label: "تنها" },
  { id: "parastoo", label: "پرستو" },
  { id: "gandom", label: "گندم" },
];

const state = {
  theme: localStorage.getItem("folio.theme") || "sepia",
  font: localStorage.getItem("folio.font") || "literata",
  mode: localStorage.getItem("folio.mode") || "page",
  zoom: parseFloat(localStorage.getItem("folio.zoom") || "1") || 1,
  view: "library",
  doc: null,
  chromeVisible: false,
  rendering: false,
  scrollLoaded: new Set(),
  progressTimer: null,
  // EPUB page mode
  epubPage: 0,
  epubPageCount: 1,
  epubChapterIndex: 0,
  toc: [],
  // reading guide
  guideOn: localStorage.getItem("folio.guideOn") === "1",
  guideTop: parseFloat(localStorage.getItem("folio.guideTop") || "40"),
  guideHeight: parseInt(localStorage.getItem("folio.guideHeight") || "48", 10),
  guideHue: parseInt(localStorage.getItem("folio.guideHue") || "48", 10),
  guideOpacity: parseInt(localStorage.getItem("folio.guideOpacity") || "28", 10),
  guideLocked: localStorage.getItem("folio.guideLock") === "1",
  guideDragging: false,
  clientCache: new Map(), // pageIndex -> rendered bitmap
  zoomLockPage: false, // while zooming, do not change current page from scroll
  epubPageHeight: 0,
  chapterPageCounts: {}, // chapterIndex -> measured page count
  globalPage: 0,
  globalPageTotal: 0,
  chromeHideTimer: null,
  pan: { active: false, el: null, x: 0, y: 0, sl: 0, st: 0, moved: false },
  // While true, do not persist progress (avoids saving a wrong interim position).
  restoring: false,
  // Content-stable EPUB anchor (0–1 through full document height).
  epubScrollRatio: 0,
  // Library sub-view: "shelf" | "catalog"
  libTab: "shelf",
  catalog: {
    nextURL: "",
    loading: false,
    books: [], // OPDSBookDTO[]
    downloading: new Map(), // id -> percent
    query: "",
    searchTimer: null,
    searchSeq: 0, // ignore stale debounced responses
  },
};

/** Built-in Calibre-Web (open OPDS, no auth). */
const DEFAULT_OPDS_BASE = "https://calibre.ghaemghh.ir";

/** Folio cloud API (auth + library upload). Browse/download stay public via OPDS. */
const FOLIO_API_BASE = (localStorage.getItem("folio.apiBase") || "https://api.ghaemghh.ir").replace(/\/$/, "");
const AUTH_TOKEN_KEY = "folio.auth.token";
const AUTH_USER_KEY = "folio.auth.username";
const AUTH_USER_ID_KEY = "folio.auth.userId";

function hasWails() {
  return typeof window.go !== "undefined" && window.go?.main?.App;
}
function api() {
  return window.go.main.App;
}

/** Session for folio-server (persisted). App works without login. */
const account = {
  token: localStorage.getItem(AUTH_TOKEN_KEY) || "",
  username: localStorage.getItem(AUTH_USER_KEY) || "",
  userId: parseInt(localStorage.getItem(AUTH_USER_ID_KEY) || "0", 10) || 0,
  mode: "login", // login | register
  uploadFile: null,
  /** When set, upload uses native path (open local book) instead of &lt;input type=file&gt;. */
  uploadLocalPath: "",
  uploading: false,
};

function isLoggedIn() {
  return !!(account.token && account.username);
}

function saveAccountSession(token, username, userId) {
  account.token = token || "";
  account.username = username || "";
  account.userId = userId || 0;
  if (account.token) {
    localStorage.setItem(AUTH_TOKEN_KEY, account.token);
    localStorage.setItem(AUTH_USER_KEY, account.username);
    localStorage.setItem(AUTH_USER_ID_KEY, String(account.userId));
  } else {
    localStorage.removeItem(AUTH_TOKEN_KEY);
    localStorage.removeItem(AUTH_USER_KEY);
    localStorage.removeItem(AUTH_USER_ID_KEY);
  }
  updateAccountChrome();
}

function clearAccountSession() {
  saveAccountSession("", "", 0);
}

async function folioApi(path, { method = "GET", body, token, formData } = {}) {
  const headers = {};
  const auth = token !== undefined ? token : account.token;
  if (auth) headers["Authorization"] = `Bearer ${auth}`;
  let payload = body;
  if (formData) {
    payload = formData;
  } else if (body != null && typeof body === "object") {
    headers["Content-Type"] = "application/json";
    payload = JSON.stringify(body);
  }
  const res = await fetch(`${FOLIO_API_BASE}${path}`, {
    method,
    headers,
    body: payload,
  });
  const text = await res.text();
  let data = null;
  try {
    data = text ? JSON.parse(text) : null;
  } catch {
    data = { error: text || res.statusText };
  }
  if (!res.ok) {
    const msg = (data && (data.error || data.message)) || `HTTP ${res.status}`;
    const err = new Error(msg);
    err.status = res.status;
    err.data = data;
    throw err;
  }
  return data;
}

// ── Cloud progress sync (fire-and-forget) ──────────────────────────────

/** Detect device name for per-device progress tracking. */
function getDeviceName() {
  if (typeof navigator !== "undefined") {
    const ua = navigator.userAgent || "";
    if (/Android/i.test(ua)) return "Android";
    if (/iPhone|iPad|iPod/i.test(ua)) return "iOS";
  }
  return "Desktop";
}

/** Serialize reading position to a compact JSON string for the server. */
function serializePosition(page, chapter, subPage, scroll) {
  return JSON.stringify({ p: page | 0, c: chapter | 0, s: subPage | 0, sc: scroll || 0 });
}

/** Deserialize server position string back to numbers. Returns null on error. */
function deserializePosition(posStr) {
  if (!posStr) return null;
  try {
    const o = JSON.parse(posStr);
    if (o && typeof o.p === "number") return o;
    return null;
  } catch { return null; }
}

/**
 * Push current reading position to folio-server (best-effort, silent on error).
 * Called alongside the local SaveBookProgress so the server always has the
 * latest position when the user is logged in.
 */
async function syncProgressToServer(fingerprint, page, chapter, subPage, scroll) {
  if (!isLoggedIn() || !fingerprint) {
    console.log("[sync] skipped: loggedIn=" + isLoggedIn() + " fp=" + fingerprint);
    return;
  }
  const pos = serializePosition(page, chapter, subPage, scroll);
  const device = getDeviceName();
  console.log("[sync] POST fp=" + fingerprint + " device=" + device + " pos=" + pos + " api=" + FOLIO_API_BASE);
  try {
    await folioApi("/progress", {
      method: "POST",
      body: { fingerprint, position: pos, device },
    });
    console.log("[sync] POST ok");
  } catch (e) { console.warn("[sync] POST failed:", e.message, e.status); }
}

/**
 * Fetch all device positions for a book. Returns array of {device, position, updated_at}.
 * Returns empty array on any error.
 */
async function fetchAllDeviceProgress(fingerprint) {
  if (!isLoggedIn() || !fingerprint) return [];
  try {
    const data = await folioApi(`/progress/${encodeURIComponent(fingerprint)}/devices`);
    return Array.isArray(data) ? data : [];
  } catch { return []; }
}

/**
 * Fetch the server's stored position for a book (returns null on any error).
 * Used on book-open to see if another device saved a newer position.
 */
async function fetchProgressFromServer(fingerprint) {
  if (!isLoggedIn() || !fingerprint) return null;
  try {
    const data = await folioApi(`/progress/${encodeURIComponent(fingerprint)}`);
    return data; // { fingerprint, device, position, updated_at }
  } catch { return null; }
}

const $ = (s) => document.querySelector(s);
const el = {
  library: $("#view-library"),
  reader: $("#view-reader"),
  libMain: $("#lib-main-shelf") || document.querySelector(".lib-main"),
  libMainShelf: $("#lib-main-shelf"),
  libMainCatalog: $("#lib-main-catalog"),
  tabShelf: $("#tab-shelf"),
  tabCatalog: $("#tab-catalog"),
  shelf: $("#shelf"),
  welcome: $("#welcome-card"),
  openFile: $("#btn-open-file"),
  openFileHero: $("#btn-open-file-hero"),
  themeCycle: $("#btn-theme-cycle"),
  catalogGrid: $("#catalog-grid"),
  catalogEmpty: $("#catalog-empty"),
  catalogStatus: $("#catalog-status"),
  catalogSub: $("#catalog-sub"),
  catalogSettings: $("#catalog-settings"),
  catalogSentinel: $("#catalog-sentinel"),
  catalogBooksDir: $("#catalog-books-dir"),
  catalogSearch: $("#catalog-search-input"),
  opdsBaseURL: $("#opds-base-url"),
  opdsUser: $("#opds-user"),
  opdsPass: $("#opds-pass"),
  btnCatalogSettings: $("#btn-catalog-settings"),
  btnCatalogRefresh: $("#btn-catalog-refresh"),
  btnCatalogUpload: $("#btn-catalog-upload"),
  btnCatalogSetup: $("#btn-catalog-setup"),
  btnOpdsSave: $("#btn-opds-save"),
  btnAccount: $("#btn-account"),
  btnAccountLabel: $("#btn-account-label"),
  accountModal: $("#account-modal"),
  accountModalClose: $("#account-modal-close"),
  accountModalTitle: $("#account-modal-title"),
  accountModalSub: $("#account-modal-sub"),
  accountTabLogin: $("#account-tab-login"),
  accountTabRegister: $("#account-tab-register"),
  accountForm: $("#account-form"),
  accountUsername: $("#account-username"),
  accountPassword: $("#account-password"),
  accountError: $("#account-error"),
  accountSubmit: $("#account-submit"),
  accountSubmitLabel: $("#account-submit-label"),
  accountSubmitSpinner: $("#account-submit-spinner"),
  accountLoggedIn: $("#account-logged-in"),
  accountUsernameDisplay: $("#account-username-display"),
  btnAccountUpload: $("#btn-account-upload"),
  btnAccountLogout: $("#btn-account-logout"),
  uploadModal: $("#upload-modal"),
  uploadModalClose: $("#upload-modal-close"),
  uploadDrop: $("#upload-drop"),
  uploadFile: $("#upload-file"),
  uploadDropTitle: $("#upload-drop-title"),
  uploadFileName: $("#upload-file-name"),
  uploadTitle: $("#upload-title"),
  uploadAuthor: $("#upload-author"),
  uploadProgressWrap: $("#upload-progress-wrap"),
  uploadProgressLabel: $("#upload-progress-label"),
  uploadProgressPct: $("#upload-progress-pct"),
  uploadProgressBar: $("#upload-progress-bar"),
  uploadProgressFill: $("#upload-progress-fill"),
  uploadError: $("#upload-error"),
  uploadCancel: $("#upload-cancel"),
  uploadSubmit: $("#upload-submit"),
  uploadSubmitSpinner: $("#upload-submit-spinner"),
  readerUpload: $("#btn-reader-upload"),
  readerTheme: $("#btn-reader-theme"),
  back: $("#btn-back"),
  prev: $("#btn-prev"),
  next: $("#btn-next"),
  edgePrev: $("#edge-prev"),
  edgeNext: $("#edge-next"),
  stage: $("#reader-stage"),
  pageViewport: $("#page-viewport"),
  pageImage: $("#page-image"),
  pageFrame: $("#page-frame"),
  pageLoading: $("#page-loading"),
  scrollViewport: $("#scroll-viewport"),
  epubViewport: $("#epub-viewport"),
  epubBook: $("#epub-book"),
  epubPages: $("#epub-pages"),
  epubContent: $("#epub-content"),
  readerTitle: $("#reader-title"),
  readerMeta: $("#reader-meta"),
  pageIndicator: $("#page-indicator"),
  version: $("#app-version"),
  toast: $("#toast"),
  modeToggle: $("#btn-mode-toggle"),
  zoomIn: $("#btn-zoom-in"),
  zoomOut: $("#btn-zoom-out"),
  zoomLabel: $("#zoom-label"),
  fontMenu: $("#btn-font-menu"),
  fontPanel: $("#font-panel"),
  fontEn: $("#font-chips-en"),
  fontFa: $("#font-chips-fa"),
  tocBtn: $("#btn-toc"),
  tocPanel: $("#toc-panel"),
  tocList: $("#toc-list"),
  tocClose: $("#btn-toc-close"),
  guideBtn: $("#btn-guide"),
  guideSettingsBtn: $("#btn-guide-settings"),
  guidePanel: $("#guide-panel"),
  guide: $("#reading-guide"),
  guideHeight: $("#guide-height"),
  guideHue: $("#guide-hue"),
  guideOpacity: $("#guide-opacity"),
  guideLock: $("#guide-lock"),
  lightbox: $("#lightbox"),
  lightboxImg: $("#lightbox-img"),
  lightboxClose: $("#lightbox-close"),
  hotspotTop: $("#hotspot-top"),
  hotspotBottom: $("#hotspot-bottom"),
};

// ─── Theme / font / zoom ─────────────────────────────────────

function applyTheme(name) {
  if (!THEMES.includes(name)) name = "sepia";
  state.theme = name;
  document.documentElement.setAttribute("data-theme", name);
  localStorage.setItem("folio.theme", name);
  // PDF bitmaps get CSS theme filters via [data-theme] rules
}
function cycleTheme() {
  applyTheme(THEMES[(THEMES.indexOf(state.theme) + 1) % THEMES.length]);
}
function applyFont(id) {
  state.font = id;
  document.documentElement.setAttribute("data-font", id);
  localStorage.setItem("folio.font", id);
  document.querySelectorAll(".font-chip").forEach((c) => {
    c.classList.toggle("is-active", c.dataset.font === id);
  });
}
/**
 * Zoom is visual only for PDFs (scale the existing bitmap).
 * Never re-renders PDF. Pins the current page so zoom cannot change page number.
 */
function applyZoom(z) {
  const next = Math.min(
    ZOOM_MAX,
    Math.max(ZOOM_MIN, Math.round(z * 100) / 100)
  );
  state.zoom = next;
  localStorage.setItem("folio.zoom", String(state.zoom));
  document.documentElement.style.setProperty("--zoom-scale", String(state.zoom));
  document.documentElement.style.setProperty("--pdf-zoom", String(state.zoom));
  el.zoomLabel.textContent = `${Math.round(state.zoom * 100)}%`;

  if (state.view !== "reader" || !state.doc) return;

  if (state.doc.format === "pdf") {
    const stayPage = state.doc.pageIndex ?? 0;
    state.zoomLockPage = true;

    const vp =
      state.mode === "scroll" ? el.scrollViewport : el.pageViewport;

    // Anchor to the current page element (scroll) or image point (page mode)
    let anchorTop = 0;
    let anchorLeft = 0;
    let anchorFracY = 0;
    if (state.mode === "scroll" && vp) {
      const slot = vp.querySelector(`[data-page="${stayPage}"]`);
      if (slot) {
        const vr = vp.getBoundingClientRect();
        const sr = slot.getBoundingClientRect();
        anchorTop = sr.top - vr.top; // px of page top relative to viewport
        anchorLeft = vp.scrollLeft;
        const h = Math.max(1, sr.height);
        anchorFracY = Math.min(1, Math.max(0, -anchorTop / h));
      }
    } else if (vp) {
      anchorLeft = vp.scrollLeft;
      anchorTop = vp.scrollTop;
      const img = el.pageImage;
      if (img && img.offsetWidth) {
        anchorFracY = (vp.scrollTop + vp.clientHeight / 2) / Math.max(1, vp.scrollHeight);
        anchorLeft = (vp.scrollLeft + vp.clientWidth / 2) / Math.max(1, vp.scrollWidth);
      }
    }

    // Disable smooth scroll during restore
    if (vp) vp.style.scrollBehavior = "auto";
    applyPdfVisualZoom();

    requestAnimationFrame(() => {
      requestAnimationFrame(() => {
        if (!state.doc) return;
        state.doc.pageIndex = stayPage;

        if (state.mode === "scroll" && vp) {
          const slot = vp.querySelector(`[data-page="${stayPage}"]`);
          if (slot) {
            // Keep the same relative position within the same page
            const newTop = slot.offsetTop + slot.offsetHeight * anchorFracY;
            vp.scrollTop = Math.max(0, newTop - Math.min(anchorTop, vp.clientHeight * 0.35));
            vp.scrollLeft = anchorLeft;
          }
        } else if (vp) {
          // Page mode: restore center ratio of the single page view
          vp.scrollLeft = Math.max(0, anchorLeft * vp.scrollWidth - vp.clientWidth / 2);
          vp.scrollTop = Math.max(0, anchorFracY * vp.scrollHeight - vp.clientHeight / 2);
        }

        updateChromeMeta();
        syncGuideWidth();
        setTimeout(() => {
          state.zoomLockPage = false;
          if (vp) vp.style.scrollBehavior = "";
        }, 200);
      });
    });
    return;
  }

  // EPUB: CSS font scale only — keep scroll position ratio
  if (state.doc?.format === "epub") {
    const ratio = currentScrollRatio();
    requestAnimationFrame(() => {
      restoreScroll(ratio);
      updateEpubPositionFromScroll();
      updateChromeMeta();
    });
  }
  syncGuideWidth();
}

/** Always render PDFs at one quality; zoom does not change this. */
function dpi() {
  return BASE_DPI;
}

/** Fit size at 100% zoom (contain in viewport). */
function pageFitBase(naturalW, naturalH, viewportEl) {
  const pad = 48;
  const vw = Math.max(120, (viewportEl?.clientWidth || window.innerWidth) - pad);
  const vh = Math.max(120, (viewportEl?.clientHeight || window.innerHeight) - pad);
  const s = Math.min(vw / Math.max(1, naturalW), vh / Math.max(1, naturalH));
  return {
    w: Math.max(1, naturalW * s),
    h: Math.max(1, naturalH * s),
  };
}

/** Apply CSS pixel size to PDF images from current zoom — no backend call. */
function applyPdfVisualZoom() {
  const z = state.zoom;

  const img = el.pageImage;
  if (img && img.naturalWidth > 0) {
    const base = pageFitBase(img.naturalWidth, img.naturalHeight, el.pageViewport);
    img.style.width = `${base.w * z}px`;
    img.style.height = "auto";
    img.style.maxWidth = "none";
    img.style.maxHeight = "none";
    if (el.pageFrame) {
      el.pageFrame.style.maxWidth = "none";
      el.pageFrame.style.maxHeight = "none";
      el.pageFrame.style.width = `${base.w * z}px`;
    }
  }

  const col =
    Math.min(920, Math.max(200, (el.scrollViewport?.clientWidth || 900) - 32));
  el.scrollViewport?.querySelectorAll(".scroll-page, .scroll-page-slot").forEach((slot) => {
    slot.style.width = `${col * z}px`;
    slot.style.maxWidth = "none";
  });
  el.scrollViewport?.querySelectorAll(".scroll-page img").forEach((im) => {
    im.style.width = "100%";
    im.style.maxWidth = "none";
    im.style.height = "auto";
  });
}
function applyMode(mode) {
  state.mode = mode === "scroll" ? "scroll" : "page";
  localStorage.setItem("folio.mode", state.mode);
  el.reader.setAttribute("data-mode", state.mode);
  el.epubViewport?.setAttribute("data-mode", state.mode);
  el.modeToggle.title =
    state.mode === "page" ? "Switch to scroll" : "Switch to page mode";
}

// ─── Chrome: edge-only reveal; open panels pin chrome ────────

function anyPanelOpen() {
  return (
    !el.fontPanel?.classList.contains("is-hidden") ||
    !el.guidePanel?.classList.contains("is-hidden") ||
    !el.tocPanel?.classList.contains("is-hidden")
  );
}

function cancelChromeHide() {
  if (state.chromeHideTimer) {
    clearTimeout(state.chromeHideTimer);
    state.chromeHideTimer = null;
  }
}

function setChrome(v) {
  state.chromeVisible = v;
  el.reader.setAttribute("data-chrome", v ? "visible" : "hidden");
  // Never auto-close popovers here — only closeAllPanels / explicit clicks do that
}

function showChromeBriefly() {
  cancelChromeHide();
  setChrome(true);
}

function scheduleHideChrome() {
  cancelChromeHide();
  state.chromeHideTimer = setTimeout(() => {
    state.chromeHideTimer = null;
    // Keep chrome (and panels) while a panel is open or hovered
    if (anyPanelOpen()) return;
    if (
      document.querySelector(
        ".reader-chrome:hover, .font-panel:hover, .guide-panel:hover, .toc-panel:hover"
      )
    ) {
      return;
    }
    setChrome(false);
  }, 700);
}

function hideChrome() {
  // If a header panel is open, only dim the bars — keep the panel
  if (anyPanelOpen()) {
    cancelChromeHide();
    return;
  }
  cancelChromeHide();
  setChrome(false);
}

function closeAllPanels() {
  el.fontPanel?.classList.add("is-hidden");
  el.guidePanel?.classList.add("is-hidden");
  // TOC is closed only via close button / Escape / open book leave
}

function openPanel(panelEl) {
  // close other popovers (not TOC — TOC is a side drawer)
  if (panelEl !== el.tocPanel) {
    el.fontPanel?.classList.add("is-hidden");
    el.guidePanel?.classList.add("is-hidden");
  }
  if (panelEl !== el.tocPanel) {
    panelEl?.classList.remove("is-hidden");
  }
  showChromeBriefly();
}

// ─── Toast ───────────────────────────────────────────────────

let toastTimer;
function toast(msg, isError = false, ms = 3400) {
  if (!el.toast) return;
  el.toast.textContent = msg;
  el.toast.hidden = false;
  el.toast.classList.toggle("is-error", !!isError);
  if (toastTimer) clearTimeout(toastTimer);
  if (ms > 0) {
    toastTimer = setTimeout(() => {
      el.toast.hidden = true;
    }, ms);
  }
}
function hideToast() {
  if (toastTimer) clearTimeout(toastTimer);
  toastTimer = null;
  if (el.toast) el.toast.hidden = true;
}

// ─── Progress / meta ─────────────────────────────────────────

function scheduleProgress() {
  if (state.progressTimer) clearTimeout(state.progressTimer);
  state.progressTimer = setTimeout(() => {
    flushProgress().catch(() => {});
  }, 250);
}

/** Immediate write of reading position (awaitable). */
async function flushProgress() {
  if (state.progressTimer) {
    clearTimeout(state.progressTimer);
    state.progressTimer = null;
  }
  // Never persist while layout/position is still settling after open/resize.
  if (state.restoring) return;
  if (!hasWails() || !state.doc) return;
  const id = state.doc.id;
  if (!id) {
    console.warn("flushProgress: missing book id");
    return;
  }
  try {
    let page = 0;
    let chapter = 0;
    let sub = 0;
    let scroll = 0;
    if (state.doc.format === "epub") {
      updateEpubPositionFromScroll();
      // page is only a display/hint metric (viewport-height units); true anchor is scroll.
      page = state.globalPage | 0;
      chapter = state.epubChapterIndex | 0;
      sub = state.epubPage | 0;
      scroll = currentScrollRatio() || 0;
      state.epubScrollRatio = scroll;
    } else {
      // PDF page numbers are document-native and stable across window sizes.
      page = Math.max(0, state.doc.pageIndex | 0);
      // Only record scroll ratio in continuous mode as a soft hint, not the restore key.
      scroll =
        state.mode === "scroll" ? currentScrollRatio() || 0 : 0;
    }
    // Prefer explicit book id so progress never depends on openDoc races
    if (typeof api().SaveBookProgress === "function") {
      await api().SaveBookProgress(id, page, chapter, sub, scroll);
    } else {
      await api().SaveProgress(page, chapter, sub, scroll);
    }
  } catch (err) {
    console.error("SaveProgress failed", err);
  }
}

function updateChromeMeta() {
  if (!state.doc) return;
  el.readerTitle.textContent = state.doc.title || "Document";
  if (state.doc.format === "epub") {
    updateEpubPositionFromScroll();
    const ch = (state.epubChapterIndex ?? 0) + 1;
    const chName =
      state.toc?.[state.epubChapterIndex]?.label || `Chapter ${ch}`;
    const g = (state.globalPage || 0) + 1;
    const gt = Math.max(1, state.globalPageTotal || 1);
    el.readerMeta.textContent = chName;
    el.pageIndicator.textContent = `${g} / ${gt}`;
    state.doc.pageIndex = state.globalPage;
  } else {
    const n = (state.doc.pageIndex ?? 0) + 1;
    const total = state.doc.pageCount ?? 1;
    el.readerMeta.textContent = `Page ${n} of ${total}`;
    el.pageIndicator.textContent = `${n} / ${total}`;
  }
  syncGuideWidth();
}

/**
 * Map continuous EPUB scroll → virtual page + chapter for the chrome only.
 *
 * IMPORTANT: "pages" here are viewport-height slices (screen-size dependent).
 * They must NEVER be the sole restore key — use scroll ratio / chapter for that.
 * Same reading spot → different page numbers when the window is resized.
 */
function updateEpubPositionFromScroll() {
  const v = el.epubBook;
  if (!v || !state.doc) return;
  const pageH = Math.max(1, v.clientHeight);
  const totalH = Math.max(pageH, el.epubContent?.scrollHeight || pageH);
  state.globalPageTotal = Math.max(1, Math.ceil(totalH / pageH - 0.001));
  state.globalPage = Math.min(
    state.globalPageTotal - 1,
    Math.max(0, Math.floor(v.scrollTop / pageH + 0.01))
  );
  state.epubPage = state.globalPage;
  state.epubPageCount = state.globalPageTotal;
  state.epubScrollRatio = currentScrollRatio() || 0;

  // Visible chapter from section tops
  const sections = el.epubContent?.querySelectorAll(".epub-chapter") || [];
  const vrTop = v.getBoundingClientRect().top + 8;
  let ch = 0;
  sections.forEach((sec) => {
    const r = sec.getBoundingClientRect();
    if (r.top <= vrTop + pageH * 0.35) {
      ch = parseInt(sec.dataset.chapter || "0", 10) || 0;
    }
  });
  state.epubChapterIndex = ch;
  highlightTOC();
}

// ─── Views ───────────────────────────────────────────────────

function showLibrary() {
  state.view = "library";
  el.library.classList.remove("is-hidden");
  el.reader.classList.add("is-hidden");
  el.fontPanel?.classList.add("is-hidden");
  el.tocPanel?.classList.add("is-hidden");
  el.guidePanel?.classList.add("is-hidden");
  if (state.libTab === "catalog") {
    switchLibTab("catalog", { skipLoad: false });
  } else {
    switchLibTab("shelf", { skipLoad: true });
    refreshShelf();
  }
}

// ─── Shelf / Catalog tabs ────────────────────────────────────

function switchLibTab(tab, { skipLoad = false } = {}) {
  state.libTab = tab === "catalog" ? "catalog" : "shelf";
  const onShelf = state.libTab === "shelf";
  el.tabShelf?.classList.toggle("is-active", onShelf);
  el.tabCatalog?.classList.toggle("is-active", !onShelf);
  el.tabShelf?.setAttribute("aria-selected", onShelf ? "true" : "false");
  el.tabCatalog?.setAttribute("aria-selected", onShelf ? "false" : "true");
  el.libMainShelf?.classList.toggle("is-hidden", !onShelf);
  el.libMainCatalog?.classList.toggle("is-hidden", onShelf);
  if (!onShelf && !skipLoad) {
    loadCatalogInitial().catch((e) => console.error(e));
  }
  if (onShelf) refreshShelf();
}

function setCatalogStatus(msg, isError = false) {
  if (!el.catalogStatus) return;
  if (!msg) {
    el.catalogStatus.hidden = true;
    el.catalogStatus.textContent = "";
    return;
  }
  el.catalogStatus.hidden = false;
  el.catalogStatus.textContent = msg;
  el.catalogStatus.classList.toggle("is-error", !!isError);
}

async function loadOPDSSettingsIntoForm() {
  if (!hasWails() || typeof api().GetOPDSSettings !== "function") {
    if (el.opdsBaseURL && !el.opdsBaseURL.value) {
      el.opdsBaseURL.value = DEFAULT_OPDS_BASE;
    }
    return { baseURL: DEFAULT_OPDS_BASE };
  }
  try {
    const s = await api().GetOPDSSettings();
    const base = s.baseURL || s.BaseURL || DEFAULT_OPDS_BASE;
    if (el.opdsBaseURL) el.opdsBaseURL.value = base || DEFAULT_OPDS_BASE;
    if (el.opdsUser) el.opdsUser.value = s.username || s.Username || "";
    if (el.opdsPass) el.opdsPass.value = s.password || s.Password || "";
    const dir = s.booksDir || s.BooksDir || "";
    if (el.catalogBooksDir && dir) {
      el.catalogBooksDir.innerHTML =
        `Default server is <code>${escapeHtml(DEFAULT_OPDS_BASE)}</code> (no login). ` +
        `Downloads save to <code>${escapeHtml(dir)}</code>. ` +
        `Change the URL only if you use another instance.`;
    }
    return s;
  } catch (e) {
    console.warn(e);
    if (el.opdsBaseURL) el.opdsBaseURL.value = DEFAULT_OPDS_BASE;
    return { baseURL: DEFAULT_OPDS_BASE };
  }
}

async function saveOPDSSettings() {
  if (!hasWails()) {
    toast("Run Folio via Wails to use the catalog.", true);
    return;
  }
  const base = (el.opdsBaseURL?.value || "").trim() || DEFAULT_OPDS_BASE;
  const user = (el.opdsUser?.value || "").trim();
  const pass = el.opdsPass?.value || "";
  try {
    await api().SaveOPDSSettings(base, user, pass);
    el.catalogSettings?.classList.add("is-hidden");
    toast("Catalog settings saved");
    if (el.catalogSearch) el.catalogSearch.value = "";
    state.catalog.query = "";
    await loadCatalogInitial();
  } catch (err) {
    toast(String(err?.message || err), true);
  }
}

/** Load newest-first listing (or re-run active search). */
async function loadCatalogInitial() {
  if (!hasWails() || typeof api().OPDSOpenLibrary !== "function") {
    setCatalogStatus("Catalog requires a current Folio build.", true);
    return;
  }
  await loadOPDSSettingsIntoForm();
  const q = (state.catalog.query || "").trim();
  if (q) {
    await runCatalogSearch(q);
    return;
  }
  el.catalogEmpty?.classList.add("is-hidden");
  state.catalog.books = [];
  state.catalog.nextURL = "";
  el.catalogGrid && (el.catalogGrid.innerHTML = "");
  setCatalogStatus("Loading newest books…");
  state.catalog.loading = true;
  const seq = ++state.catalog.searchSeq;
  try {
    const page = await api().OPDSOpenLibrary();
    if (seq !== state.catalog.searchSeq) return;
    appendCatalogPage(page);
    setCatalogStatus("");
    if (el.catalogSub) {
      const n = state.catalog.books.filter((b) => !b.isNavigation).length;
      el.catalogSub.textContent = page?.title
        ? `${page.title} · newest first`
        : `Newest first · ${n}+ books`;
    }
    if (!state.catalog.books.length) {
      el.catalogEmpty?.classList.remove("is-hidden");
    }
  } catch (err) {
    if (seq !== state.catalog.searchSeq) return;
    console.error(err);
    setCatalogStatus(String(err?.message || err || "Could not load catalog"), true);
    el.catalogEmpty?.classList.remove("is-hidden");
  } finally {
    if (seq === state.catalog.searchSeq) {
      state.catalog.loading = false;
      updateCatalogSentinel();
    }
  }
}

async function runCatalogSearch(query) {
  if (!hasWails()) return;
  const q = (query || "").trim();
  state.catalog.query = q;
  if (!q) {
    await loadCatalogInitial();
    return;
  }
  if (typeof api().OPDSSearch !== "function") {
    setCatalogStatus("Search requires a current Folio build.", true);
    return;
  }
  el.catalogEmpty?.classList.add("is-hidden");
  state.catalog.books = [];
  state.catalog.nextURL = "";
  el.catalogGrid && (el.catalogGrid.innerHTML = "");
  setCatalogStatus(`Searching for “${q}”…`);
  state.catalog.loading = true;
  const seq = ++state.catalog.searchSeq;
  try {
    const page = await api().OPDSSearch(q);
    if (seq !== state.catalog.searchSeq) return;
    appendCatalogPage(page);
    setCatalogStatus("");
    if (el.catalogSub) {
      el.catalogSub.textContent = `Search: “${q}”`;
    }
    if (!state.catalog.books.length) {
      el.catalogEmpty?.classList.remove("is-hidden");
      setCatalogStatus(`No results for “${q}”`);
    }
  } catch (err) {
    if (seq !== state.catalog.searchSeq) return;
    console.error(err);
    setCatalogStatus(String(err?.message || err || "Search failed"), true);
    el.catalogEmpty?.classList.remove("is-hidden");
  } finally {
    if (seq === state.catalog.searchSeq) {
      state.catalog.loading = false;
      updateCatalogSentinel();
    }
  }
}

/** Debounce search: request after user stops typing (~400ms). */
function onCatalogSearchInput() {
  const q = el.catalogSearch?.value || "";
  if (state.catalog.searchTimer) clearTimeout(state.catalog.searchTimer);
  state.catalog.searchTimer = setTimeout(() => {
    state.catalog.searchTimer = null;
    const next = (el.catalogSearch?.value || "").trim();
    // Empty field → back to newest listing
    if (!next) {
      state.catalog.query = "";
      loadCatalogInitial();
      return;
    }
    runCatalogSearch(next);
  }, 400);
}

async function loadCatalogMore() {
  if (state.catalog.loading || !state.catalog.nextURL) return;
  if (!hasWails()) return;
  state.catalog.loading = true;
  updateCatalogSentinel();
  try {
    const page = await api().OPDSFetchPage(state.catalog.nextURL);
    appendCatalogPage(page);
  } catch (err) {
    toast(String(err?.message || err), true);
  } finally {
    state.catalog.loading = false;
    updateCatalogSentinel();
  }
}

function appendCatalogPage(page) {
  if (!page) return;
  state.catalog.nextURL = page.nextURL || page.NextURL || "";
  const books = page.books || page.Books || [];
  for (const raw of books) {
    const b = normalizeOPDSBook(raw);
    // Skip pure nav nodes in the grid (we open the flat book list)
    if (b.isNavigation && !(b.acquisitions && b.acquisitions.length)) continue;
    state.catalog.books.push(b);
    el.catalogGrid?.appendChild(renderCatalogCard(b));
  }
  if (state.catalog.books.length) {
    el.catalogEmpty?.classList.add("is-hidden");
  }
}

function normalizeOPDSBook(raw) {
  return {
    id: raw.id || raw.ID || "",
    title: raw.title || raw.Title || "Untitled",
    authors: raw.authors || raw.Authors || [],
    summary: raw.summary || raw.Summary || "",
    coverURL: raw.coverURL || raw.CoverURL || "",
    acquisitions: raw.acquisitions || raw.Acquisitions || [],
    state: raw.state || raw.State || "not_downloaded",
    progress: raw.progress ?? raw.Progress ?? 0,
    progressLabel: raw.progressLabel || raw.ProgressLabel || "",
    localBookId: raw.localBookId || raw.LocalBookID || "",
    localPath: raw.localPath || raw.LocalPath || "",
    isNavigation: !!(raw.isNavigation ?? raw.IsNavigation),
    navURL: raw.navURL || raw.NavURL || "",
  };
}

function catalogStateLabel(b) {
  const dl = state.catalog.downloading.get(b.id);
  if (dl != null) {
    return dl >= 100 ? "Finishing…" : `${Math.round(dl)}%`;
  }
  switch (b.state) {
    case "read":
      return "Read";
    case "in_progress":
      return b.progressLabel || `${Math.round((b.progress || 0) * 100)}%`;
    case "downloaded":
      return "Downloaded";
    default:
      return "Not downloaded";
  }
}

function renderCatalogCard(b) {
  const card = document.createElement("article");
  card.className = "catalog-card";
  card.dataset.id = b.id;

  const cover = document.createElement("div");
  cover.className = "catalog-cover";
  if (b.coverURL) {
    const img = document.createElement("img");
    img.alt = "";
    img.loading = "lazy";
    img.src = b.coverURL;
    img.onerror = () => {
      img.remove();
      const fb = document.createElement("div");
      fb.className = "catalog-cover-fallback";
      fb.textContent = (b.title || "?").slice(0, 40);
      cover.appendChild(fb);
    };
    cover.appendChild(img);
  } else {
    const fb = document.createElement("div");
    fb.className = "catalog-cover-fallback";
    fb.textContent = (b.title || "?").slice(0, 40);
    cover.appendChild(fb);
  }

  const badge = document.createElement("span");
  badge.className = "catalog-badge";
  const label = catalogStateLabel(b);
  badge.textContent = label;
  if (state.catalog.downloading.has(b.id)) badge.classList.add("is-downloading");
  else if (b.state === "read") badge.classList.add("is-read");
  else if (b.state === "in_progress") badge.classList.add("is-progress");
  cover.appendChild(badge);

  const title = document.createElement("h3");
  title.className = "catalog-title";
  title.textContent = b.title;

  const author = document.createElement("p");
  author.className = "catalog-author";
  author.textContent = (b.authors || []).join(", ") || "Unknown author";

  const actions = document.createElement("div");
  actions.className = "catalog-card-actions";
  const btn = document.createElement("button");
  btn.type = "button";
  const owned =
    b.state === "downloaded" ||
    b.state === "in_progress" ||
    b.state === "read" ||
    !!b.localBookId;
  if (state.catalog.downloading.has(b.id)) {
    btn.className = "btn btn--ghost";
    btn.disabled = true;
    btn.textContent = "Downloading…";
  } else if (owned) {
    btn.className = "btn btn--primary";
    btn.textContent = "Open";
    btn.addEventListener("click", () => openCatalogBook(b));
  } else {
    btn.className = "btn btn--primary";
    btn.textContent = "Download";
    btn.addEventListener("click", () => downloadCatalogBook(b, btn));
  }
  actions.appendChild(btn);

  const bar = document.createElement("div");
  bar.className = "catalog-progress-bar";
  bar.hidden = !state.catalog.downloading.has(b.id);
  const fill = document.createElement("span");
  fill.style.width = `${state.catalog.downloading.get(b.id) || 0}%`;
  bar.appendChild(fill);

  card.append(cover, title, author, actions, bar);
  return card;
}

function refreshCatalogCard(id) {
  const b = state.catalog.books.find((x) => x.id === id);
  if (!b || !el.catalogGrid) return;
  const safe = String(id).replace(/\\/g, "\\\\").replace(/"/g, '\\"');
  const old = el.catalogGrid.querySelector(`.catalog-card[data-id="${safe}"]`);
  const next = renderCatalogCard(b);
  if (old) old.replaceWith(next);
}

async function openCatalogBook(b) {
  if (b.localBookId) {
    await openBookId(b.localBookId);
    return;
  }
  // Path-only ownership (not yet on shelf with id)
  if (b.localPath && hasWails() && typeof api().OpenPath === "function") {
    try {
      toast("Opening…", false, 0);
      const doc = await api().OpenPath(b.localPath);
      if (doc) await enterDocument(doc);
      hideToast();
    } catch (err) {
      toast(String(err?.message || err), true);
    }
  }
}

async function downloadCatalogBook(b, btnEl) {
  if (!hasWails() || typeof api().OPDSDownload !== "function") {
    toast("Download is not available in this build.", true);
    return;
  }
  const acq = (b.acquisitions || [])[0];
  // Prefer epub then pdf
  let pick = acq;
  for (const a of b.acquisitions || []) {
    const f = (a.format || a.Format || "").toLowerCase();
    const t = (a.type || a.Type || "").toLowerCase();
    if (f === "epub" || t.includes("epub")) {
      pick = a;
      break;
    }
    if (f === "pdf" || t.includes("pdf")) pick = a;
  }
  if (!pick) {
    toast("No downloadable format for this book.", true);
    return;
  }
  const href = pick.href || pick.Href;
  const mime = pick.type || pick.Type || "";
  state.catalog.downloading.set(b.id, 0);
  refreshCatalogCard(b.id);
  if (btnEl) {
    btnEl.disabled = true;
    btnEl.textContent = "Downloading…";
  }
  try {
    const result = await api().OPDSDownload(b.id, b.title, href, mime);
    const updated = normalizeOPDSBook(result?.book || result?.Book || {});
    const idx = state.catalog.books.findIndex((x) => x.id === b.id);
    if (idx >= 0) {
      state.catalog.books[idx] = {
        ...b,
        ...updated,
        id: b.id,
        title: b.title || updated.title,
        authors: b.authors,
        coverURL: b.coverURL,
        acquisitions: b.acquisitions,
      };
    }
    state.catalog.downloading.delete(b.id);
    refreshCatalogCard(b.id);
    toast(result?.skipped ? "Already on your shelf" : "Downloaded to books/");
    refreshShelf();
  } catch (err) {
    state.catalog.downloading.delete(b.id);
    refreshCatalogCard(b.id);
    toast(String(err?.message || err), true);
  }
}

function updateCatalogSentinel() {
  if (!el.catalogSentinel) return;
  const show = !!state.catalog.nextURL || state.catalog.loading;
  el.catalogSentinel.hidden = !show;
  if (!state.catalog.nextURL && !state.catalog.loading) {
    el.catalogSentinel.hidden = true;
  }
}

function onCatalogScroll() {
  const main = el.libMainCatalog;
  if (!main || state.libTab !== "catalog") return;
  if (state.catalog.loading || !state.catalog.nextURL) return;
  const nearBottom =
    main.scrollTop + main.clientHeight >= main.scrollHeight - 280;
  if (nearBottom) loadCatalogMore();
}

function wireOPDSProgressEvents() {
  try {
    const rt = window.runtime;
    if (!rt || typeof rt.EventsOn !== "function") return;
    rt.EventsOn("opds:download-progress", (payload) => {
      const id = payload?.id;
      if (!id) return;
      const done = !!payload.done;
      const pct = Number(payload.percent) || 0;
      if (done) {
        state.catalog.downloading.delete(id);
      } else {
        state.catalog.downloading.set(id, pct);
      }
      refreshCatalogCard(id);
    });
    // PDF engine warm-up status (WASM can take several seconds once per launch)
    rt.EventsOn("folio:pdf-engine", (payload) => {
      const st = payload?.status;
      const msg = payload?.message || "";
      if (st === "starting" || st === "opening") {
        toast(msg || "Preparing PDF engine…", false, 12000);
      } else if (st === "ready") {
        hideToast();
      } else if (st === "error") {
        toast("PDF engine: " + (msg || "failed to start"), true, 8000);
      }
    });
  } catch (_) {}
}

function showReader() {
  state.view = "reader";
  el.library.classList.add("is-hidden");
  el.reader.classList.remove("is-hidden");
  hideChrome(); // start distraction-free
  el.stage.focus({ preventScroll: true });
  applyGuideUI();
  updateReaderUploadButton();
}

function updateReaderUploadButton() {
  const show = !!(state.doc && state.doc.path);
  el.readerUpload?.classList.toggle("is-hidden", !show);
}

// ─── Shelf (unchanged flow) ──────────────────────────────────

async function refreshShelf() {
  if (!hasWails()) return;
  let items = [];
  try {
    items = (await api().GetLibrary()) || [];
  } catch (e) {
    console.error(e);
    return;
  }
  if (!items.length) {
    el.shelf.classList.add("is-hidden");
    el.shelf.innerHTML = "";
    el.welcome?.classList.remove("is-hidden");
    el.libMainShelf?.classList.remove("has-shelf");
    el.libMain?.classList.remove("has-shelf");
    return;
  }
  el.welcome?.classList.add("is-hidden");
  el.libMainShelf?.classList.add("has-shelf");
  el.libMain?.classList.add("has-shelf");
  el.shelf.classList.remove("is-hidden");
  el.shelf.innerHTML = "";
  for (const item of items) {
    const card = document.createElement("article");
    card.className = "shelf-card" + (item.status !== "ok" ? " is-bad" : "");
    const cover = document.createElement("div");
    cover.className = "shelf-cover";
    if (item.coverDataURL) {
      const img = document.createElement("img");
      img.src = item.coverDataURL;
      img.alt = "";
      cover.appendChild(img);
    } else {
      const fb = document.createElement("div");
      fb.className = "shelf-cover-fallback";
      fb.textContent = (item.title || "?").slice(0, 28);
      cover.appendChild(fb);
    }
    const badge = document.createElement("span");
    badge.className = "shelf-badge";
    if (item.status === "missing" || item.status === "replaced") {
      badge.classList.add("is-warn");
      badge.textContent = item.statusLabel || item.status;
    } else badge.textContent = (item.format || "pdf").toUpperCase();
    cover.appendChild(badge);
    const title = document.createElement("h3");
    title.className = "shelf-title";
    if (item.status === "missing") title.textContent = `${item.title} — Removed`;
    else if (item.status === "replaced")
      title.textContent = `${item.title} — Replaced`;
    else title.textContent = item.title;
    const meta = document.createElement("p");
    meta.className = "shelf-meta";
    if (item.status === "ok") {
      const p = (item.lastPage || 0) + 1;
      meta.textContent = `p. ${p}`;
    } else meta.textContent = item.statusLabel || "Unavailable";
    card.append(cover, title, meta);
    if (item.status !== "ok") {
      const actions = document.createElement("div");
      actions.className = "shelf-actions";
      const remap = document.createElement("button");
      remap.type = "button";
      remap.className = "btn btn--primary";
      remap.textContent = "Map file…";
      remap.addEventListener("click", (e) => {
        e.stopPropagation();
        remapBook(item.id);
      });
      const remove = document.createElement("button");
      remove.type = "button";
      remove.className = "btn btn--ghost";
      remove.textContent = "Remove";
      remove.addEventListener("click", async (e) => {
        e.stopPropagation();
        try {
          await api().RemoveFromLibrary(item.id);
          refreshShelf();
        } catch (err) {
          toast(String(err), true);
        }
      });
      actions.append(remap, remove);
      card.appendChild(actions);
    } else card.addEventListener("click", () => openBookId(item.id));
    el.shelf.appendChild(card);
  }
}

async function remapBook(id) {
  try {
    const doc = await api().RemapBookDialog(id);
    if (doc) await enterDocument(doc);
  } catch (err) {
    toast(String(err?.message || err), true);
    refreshShelf();
  }
}
async function openBookId(id) {
  try {
    toast("Opening…", false, 0); // stay until we hide it
    const doc = await api().OpenBook(id);
    if (!doc) {
      toast("Could not open book.", true);
      return;
    }
    // Re-read saved progress from library (source of truth)
    let savedPage = doc.pageIndex ?? doc.PageIndex ?? 0;
    let savedScroll = doc.lastScroll ?? doc.LastScroll ?? 0;
    let savedChapter = doc.lastChapter ?? doc.LastChapter ?? 0;
    let savedSubPage = doc.lastSubPage ?? doc.LastSubPage ?? 0;
    try {
      if (typeof api().GetBookProgress === "function") {
        const prog = await api().GetBookProgress(doc.id || doc.ID || id);
        if (prog && (prog.page != null || prog.Page != null)) {
          savedPage = prog.page ?? prog.Page ?? savedPage;
          savedScroll = prog.scroll ?? prog.Scroll ?? savedScroll;
          savedChapter = prog.chapter ?? prog.Chapter ?? savedChapter;
          savedSubPage = prog.subPage ?? prog.SubPage ?? savedSubPage;
        }
      }
    } catch (_) {}
    // Cloud sync: check if server has positions from multiple devices.
    const fp = doc.fingerprint || doc.Fingerprint || "";
    const devices = await fetchAllDeviceProgress(fp);
    if (devices.length > 0) {
      // Parse each device position
      const parsed = devices.map((d) => {
        const sp = deserializePosition(d.position);
        return { device: d.device || "?", pos: sp, updated: d.updated_at || "" };
      }).filter((d) => d.pos);
      // Check if devices disagree on position
      const unique = new Set(parsed.map((d) => d.pos.p));
      if (parsed.length > 1 && unique.size > 1) {
        // Show device picker popup
        const chosen = await showDevicePickerPopup(parsed, doc.title || "this book");
        if (chosen && chosen.pos) {
          savedPage = chosen.pos.p ?? savedPage;
          savedChapter = chosen.pos.c ?? savedChapter;
          savedSubPage = chosen.pos.s ?? savedSubPage;
          savedScroll = chosen.pos.sc ?? savedScroll;
        }
      } else if (parsed.length === 1) {
        // Single device — use its position directly
        const sp = parsed[0].pos;
        savedPage = sp.p ?? savedPage;
        savedChapter = sp.c ?? savedChapter;
        savedSubPage = sp.s ?? savedSubPage;
        savedScroll = sp.sc ?? savedScroll;
      }
    }
    doc.pageIndex = savedPage | 0;
    doc.lastScroll = savedScroll || 0;
    doc.lastChapter = savedChapter | 0;
    doc.lastSubPage = savedSubPage | 0;
    await enterDocument(doc);
    hideToast();
  } catch (err) {
    console.error("OpenBook", err);
    toast(String(err?.message || err || "Open failed"), true);
    refreshShelf();
  }
}

/**
 * Show a popup letting the user choose which device's reading position to restore.
 * Returns the chosen entry {device, pos, updated} or null if cancelled.
 */
function showDevicePickerPopup(devices, bookTitle) {
  return new Promise((resolve) => {
    const overlay = document.createElement("div");
    overlay.className = "device-picker-overlay";
    const box = document.createElement("div");
    box.className = "device-picker";
    const icons = { Desktop: "\uD83D\uDDA5\uFE0F", Android: "\uD83D\uDCF1", iOS: "\uD83D\uDCF1" };
    let html = `<h3 class="device-picker-title">Continue reading</h3>`;
    html += `<p class="device-picker-sub">${escapeHtml(bookTitle)}</p>`;
    for (const d of devices) {
      const icon = icons[d.device] || "\uD83D\uDCBB";
      const pg = (d.pos.p || 0) + 1;
      const timeAgo = d.updated ? timeAgoStr(d.updated) : "";
      html += `<button class="device-picker-btn" data-device="${escapeHtml(d.device)}">` +
        `<span class="device-picker-icon">${icon}</span>` +
        `<span class="device-picker-name">${escapeHtml(d.device)}</span>` +
        `<span class="device-picker-page">Page ${pg}</span>` +
        `<span class="device-picker-time">${timeAgo}</span>` +
        `</button>`;
    }
    html += `<button class="device-picker-btn device-picker-cancel">Cancel</button>`;
    box.innerHTML = html;
    overlay.appendChild(box);
    document.body.appendChild(overlay);
    overlay.addEventListener("click", (e) => {
      const btn = e.target.closest(".device-picker-btn");
      if (!btn) return;
      if (btn.classList.contains("device-picker-cancel")) {
        overlay.remove();
        resolve(null);
        return;
      }
      const devName = btn.dataset.device;
      const chosen = devices.find((d) => d.device === devName);
      overlay.remove();
      resolve(chosen || null);
    });
  });
}

/** Simple time-ago string from a datetime string. */
function timeAgoStr(dateStr) {
  try {
    const d = new Date(dateStr.replace(" ", "T") + "Z");
    const diff = (Date.now() - d.getTime()) / 1000;
    if (diff < 60) return "just now";
    if (diff < 3600) return Math.floor(diff / 60) + "m ago";
    if (diff < 86400) return Math.floor(diff / 3600) + "h ago";
    return Math.floor(diff / 86400) + "d ago";
  } catch { return ""; }
}
async function openFile() {
  if (!hasWails()) {
    toast("Run Folio via Wails to open files.", true);
    return;
  }
  try {
    // PDF engine (WASM) can take a few seconds the first time — show feedback.
    toast("Opening… first PDF may take a moment while the engine starts", false, 8000);
    const doc = await api().OpenFileDialog();
    hideToast();
    if (doc) await enterDocument(doc);
  } catch (err) {
    hideToast();
    toast(String(err?.message || err), true, 8000);
  }
}

async function enterDocument(doc) {
  if (!doc) return;

  // Normalize fields (Wails may use either casing)
  const format = String(doc.format || doc.Format || "pdf").toLowerCase();
  const pageCount = doc.pageCount ?? doc.PageCount ?? 1;
  const pageIndex = doc.pageIndex ?? doc.PageIndex ?? 0;

  state.rendering = false; // never block a new open because a prior load stuck
  state.restoring = true; // block progress writes until we land on the saved spot
  state.doc = {
    id: doc.id || doc.ID || "",
    path: doc.path || doc.Path || "",
    title: doc.title || doc.Title || "Document",
    fingerprint: doc.fingerprint || doc.Fingerprint || "",
    format,
    pageCount,
    chapterCount: pageCount,
    pageIndex,
    lastScroll: doc.lastScroll ?? doc.LastScroll ?? 0,
    lastChapter: doc.lastChapter ?? doc.LastChapter ?? 0,
    lastSubPage: doc.lastSubPage ?? doc.LastSubPage ?? 0,
  };
  state.scrollLoaded = new Set();
  state.clientCache.clear();
  state.chapterPageCounts = {};
  state.globalPage = pageIndex;
  state.globalPageTotal = Math.max(1, pageCount);
  state.epubScrollRatio = state.doc.lastScroll || 0;

  state.epubChapterIndex = state.doc.lastChapter || 0;
  state.epubPage = state.doc.lastSubPage || 0;

  showReader();
  setupViewports();
  el.tocBtn.classList.toggle("is-hidden", format !== "epub");
  el.modeToggle?.classList.toggle("is-hidden", format === "epub");

  try {
    if (format === "epub") {
      await loadTOC();
      await loadEpubContinuous({
        // Content-stable anchor (0–1). Page numbers are NOT used for restore.
        restoreScroll: state.doc.lastScroll || 0,
        restoreChapter: state.epubChapterIndex,
      });
    } else {
      // PDF — always land in page mode first (most reliable). User can switch to scroll.
      el.epubViewport?.classList.add("is-hidden");
      el.scrollViewport?.classList.add("is-hidden");
      el.pageViewport?.classList.remove("is-hidden");
      // If user prefers scroll, load page then switch (avoids blank continuous view on engine miss).
      await renderPage();
      if (state.mode === "scroll") {
        try {
          el.pageViewport?.classList.add("is-hidden");
          el.scrollViewport?.classList.remove("is-hidden");
          await reloadScroll(true);
          await scrollPdfToPage(state.doc.pageIndex, { smooth: false });
        } catch (scrollErr) {
          console.error("PDF scroll mode failed, staying in page mode", scrollErr);
          applyMode("page");
          el.scrollViewport?.classList.add("is-hidden");
          el.pageViewport?.classList.remove("is-hidden");
          await renderPage();
        }
      }
      prefetchAround(state.doc.pageIndex);
    }
  } catch (err) {
    console.error("enterDocument failed", err);
    toast(String(err?.message || err || "Could not open document"), true, 10000);
  } finally {
    state.restoring = false;
  }
  updateChromeMeta();
  syncGuideWidth();
  // One clean save after restore settled (open-time flushes were blocked).
  scheduleProgress();
}

/** Scroll continuous PDF view to a document page (stable across window sizes). */
async function scrollPdfToPage(pageIndex, { smooth = false } = {}) {
  const vp = el.scrollViewport;
  if (!vp || !state.doc) return;
  const target = Math.max(
    0,
    Math.min(state.doc.pageCount - 1, pageIndex | 0)
  );
  state.doc.pageIndex = target;
  // Ensure target (and neighbors) are measured with real image heights when possible.
  await ensureScrollPage(target);
  ensureScrollPage(target + 1);
  ensureScrollPage(target - 1);

  const apply = () => {
    const slot = vp.querySelector(`[data-page="${target}"]`);
    if (!slot) return;
    slot.scrollIntoView({
      block: "start",
      behavior: smooth ? "smooth" : "auto",
    });
  };
  apply();
  // Second pass after layout/images settle so we don't stick on placeholder heights.
  await new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r)));
  apply();
  await new Promise((r) => setTimeout(r, 50));
  apply();
}

function setupViewports() {
  const isEpub = state.doc?.format === "epub";
  if (isEpub) {
    el.epubViewport?.classList.remove("is-hidden");
    el.epubViewport?.setAttribute("data-mode", "scroll");
    el.pageViewport?.classList.add("is-hidden");
    el.scrollViewport?.classList.add("is-hidden");
    return;
  }
  // PDF
  el.epubViewport?.classList.add("is-hidden");
  if (state.mode === "scroll") {
    el.pageViewport?.classList.add("is-hidden");
    el.scrollViewport?.classList.remove("is-hidden");
  } else {
    el.scrollViewport?.classList.add("is-hidden");
    el.pageViewport?.classList.remove("is-hidden");
  }
}

async function closeReader() {
  if (hasWails() && state.doc) {
    try {
      await flushProgress();
      // Sync to cloud server before closing (await so the request completes).
      const fp = state.doc.fingerprint;
      if (fp && isLoggedIn()) {
        let page = 0, chapter = 0, sub = 0, scroll = 0;
        if (state.doc.format === "epub") {
          page = state.globalPage | 0;
          chapter = state.epubChapterIndex | 0;
          sub = state.epubPage | 0;
          scroll = currentScrollRatio() || 0;
        } else {
          page = Math.max(0, state.doc.pageIndex | 0);
          scroll = state.mode === "scroll" ? currentScrollRatio() || 0 : 0;
        }
        await syncProgressToServer(fp, page, chapter, sub, scroll);
      }
      await api().CloseDocument();
    } catch (_) {}
  }
  state.doc = null;
  state.toc = [];
  el.pageImage.removeAttribute("src");
  el.scrollViewport.innerHTML = "";
  el.epubContent.innerHTML = "";
  el.tocList.innerHTML = "";
  showLibrary();
}

function currentScrollRatio() {
  if (state.doc?.format === "epub") {
    const v = el.epubBook;
    if (!v || v.scrollHeight <= v.clientHeight) return 0;
    return v.scrollTop / (v.scrollHeight - v.clientHeight);
  }
  if (state.mode === "scroll" && state.doc?.format === "pdf") {
    const v = el.scrollViewport;
    if (!v || v.scrollHeight <= v.clientHeight) return 0;
    return v.scrollTop / (v.scrollHeight - v.clientHeight);
  }
  return 0;
}

/**
 * Restore vertical position by fraction of max scroll (content-stable when
 * total height proportions are stable). Re-apply several times as images/fonts
 * load so we do not freeze on a pre-layout height.
 */
function restoreScroll(ratio, { times = 4, gapMs = 120 } = {}) {
  if (ratio == null || ratio < 0) return Promise.resolve();
  const clamped = Math.min(1, Math.max(0, ratio));
  if (clamped === 0) return Promise.resolve();

  const apply = () => {
    const v =
      state.doc?.format === "epub" ? el.epubBook : el.scrollViewport;
    if (!v) return false;
    const max = v.scrollHeight - v.clientHeight;
    if (max <= 0) return false;
    v.scrollTop = max * clamped;
    return true;
  };

  return new Promise((resolve) => {
    let n = 0;
    const tick = () => {
      apply();
      n += 1;
      if (n >= times) {
        resolve();
        return;
      }
      setTimeout(() => requestAnimationFrame(tick), gapMs);
    };
    requestAnimationFrame(() => requestAnimationFrame(tick));
  });
}

/**
 * Keep the same content position when the EPUB viewport is resized.
 * Virtual page numbers will change; the text under the eye should not jump.
 */
function preserveEpubContentOnResize() {
  if (state.doc?.format !== "epub" || !el.epubBook) return;
  const ratio =
    state.epubScrollRatio || currentScrollRatio() || 0;
  if (ratio <= 0) {
    updateEpubPositionFromScroll();
    updateChromeMeta();
    return;
  }
  state.restoring = true;
  const v = el.epubBook;
  const max = v.scrollHeight - v.clientHeight;
  if (max > 0) v.scrollTop = max * Math.min(1, Math.max(0, ratio));
  updateEpubPositionFromScroll();
  updateChromeMeta();
  // Release after a frame so scroll handlers don't overwrite saved progress.
  requestAnimationFrame(() => {
    state.restoring = false;
  });
}

// ─── PDF (cached + prefetch) ─────────────────────────────────

function cacheKey(i) {
  // DPI is fixed; cache per page only so zoom never invalidates bitmaps
  return String(i);
}

async function fetchPDFPage(index, { setCurrent = true } = {}) {
  const key = cacheKey(index);
  if (state.clientCache.has(key)) {
    const hit = state.clientCache.get(key);
    if (setCurrent && state.doc) state.doc.pageIndex = index;
    return hit;
  }
  if (!hasWails()) return null;
  // Prefetch must NOT move the "current page" cursor on the backend
  let page;
  try {
    if (setCurrent) {
      page = await api().RenderPDFPage(index, dpi());
    } else if (typeof api().PrefetchPDFPage === "function") {
      page = await api().PrefetchPDFPage(index, dpi());
    } else {
      // Fallback: only warm disk/memory via bulk prefetch API
      await api().PrefetchPDFPages([index], dpi());
      page = await api().RenderPDFPage(index, dpi());
      // Restore cursor if we had to use RenderPDFPage
      if (state.doc) {
        try {
          await api().RenderPDFPage(state.doc.pageIndex | 0, dpi());
        } catch (_) {}
      }
    }
  } catch (err) {
    if (setCurrent) {
      toast("PDF render failed: " + String(err?.message || err), true, 8000);
    }
    throw err;
  }
  if (!page) return null;
  const packed = {
    // Prefer cache URL (small JSON); dataURL only for tiny fallbacks
    url: page.url || page.URL || "",
    dataURL: (page.dataURL ?? page.DataURL) || "",
    pageIndex: page.pageIndex ?? page.PageIndex ?? index,
    pageCount: page.pageCount ?? page.PageCount ?? state.doc?.pageCount ?? 1,
    width: page.width ?? page.Width,
    height: page.height ?? page.Height,
  };
  state.clientCache.set(key, packed);
  if (state.clientCache.size > 60) {
    const first = state.clientCache.keys().next().value;
    state.clientCache.delete(first);
  }
  if (setCurrent && state.doc) {
    state.doc.pageIndex = packed.pageIndex;
    state.doc.pageCount = packed.pageCount;
  }
  return packed;
}

function prefetchAround(index) {
  if (!hasWails() || !state.doc || state.doc.format !== "pdf") return;
  const pages = [index - 1, index + 1, index + 2, index - 2].filter(
    (p) => p >= 0 && p < state.doc.pageCount
  );
  // Backend disk/memory warm — does not change current page
  try {
    api().PrefetchPDFPages(pages, dpi());
  } catch (_) {}
  // Client cache warm without moving current page
  for (const p of pages) {
    if (!state.clientCache.has(cacheKey(p))) {
      fetchPDFPage(p, { setCurrent: false }).catch(() => {});
    }
  }
}

/** Convert data: URL → blob: URL (WebView2 handles blob: far more reliably). */
function dataURLToBlobURL(dataURL) {
  try {
    const m = String(dataURL).match(/^data:([^;,]+);base64,([\s\S]+)$/);
    if (!m) return dataURL;
    const mime = m[1];
    const b64 = m[2];
    const bin = atob(b64);
    const bytes = new Uint8Array(bin.length);
    for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
    return URL.createObjectURL(new Blob([bytes], { type: mime }));
  } catch (_) {
    return dataURL;
  }
}

/** Resolve display src for a rendered PDF page. Prefer blob from dataURL, else cache URL. */
function pageSrc(page) {
  if (!page) return "";
  const data = page.dataURL || page.DataURL || "";
  if (data && data.startsWith("data:")) {
    if (page._blobUrl) return page._blobUrl;
    page._blobUrl = dataURLToBlobURL(data);
    return page._blobUrl;
  }
  return page.url || page.URL || data || "";
}

async function renderPage() {
  if (!hasWails() || !state.doc) return;
  if (state.doc.format !== "pdf") return;
  if (state.rendering) {
    state.rendering = false;
  }
  state.rendering = true;
  el.pageViewport?.classList.remove("is-hidden");
  el.scrollViewport?.classList.add("is-hidden");
  el.epubViewport?.classList.add("is-hidden");
  const target = Math.max(0, state.doc.pageIndex | 0);
  const showSpinner = !state.clientCache.has(cacheKey(target));
  if (showSpinner && el.pageLoading) el.pageLoading.hidden = false;
  try {
    const page = await fetchPDFPage(target, { setCurrent: true });
    const src = pageSrc(page);
    if (!page || !src) {
      toast("PDF page was empty — try reopening the file.", true);
      return;
    }
    // Keep the page we asked for (ignore prefetch races)
    state.doc.pageIndex = target;
    state.doc.pageCount = page.pageCount || state.doc.pageCount;
    await presentPage(page);
    updateChromeMeta();
    // Persist immediately so reopen lands here
    await flushProgress();
    prefetchAround(target);
  } catch (err) {
    console.error("renderPage", err);
    toast(String(err?.message || err || "PDF render failed"), true, 8000);
  } finally {
    if (el.pageLoading) el.pageLoading.hidden = true;
    state.rendering = false;
  }
}

function presentPage(page) {
  return new Promise((resolve) => {
    const img = el.pageImage;
    const frame = el.pageFrame;
    const src = pageSrc(page);
    if (!src) {
      resolve();
      return;
    }
    const finish = () => {
      frame.classList.remove("is-turning-out");
      frame.classList.add("is-turning-in");
      setTimeout(() => frame.classList.remove("is-turning-in"), 200);
      applyPdfVisualZoom();
      syncGuideWidth();
      resolve();
    };
    const apply = () => {
      if (img.getAttribute("data-folio-src") === src && img.complete && img.naturalWidth) {
        finish();
        return;
      }
      img.onload = () => finish();
      img.onerror = () => {
        // Fallback: try the other representation
        const alt =
          (page.dataURL || page.DataURL) && src !== (page.dataURL || page.DataURL)
            ? dataURLToBlobURL(page.dataURL || page.DataURL)
            : page.url || page.URL || "";
        if (alt && alt !== src) {
          img.onerror = () => {
            toast("Could not display PDF page image.", true, 8000);
            resolve();
          };
          img.setAttribute("data-folio-src", alt);
          img.src = alt;
          return;
        }
        toast("Could not display PDF page image.", true, 8000);
        resolve();
      };
      img.setAttribute("data-folio-src", src);
      img.src = src;
    };
    if (img.src && img.getAttribute("data-folio-src") !== src) {
      frame.classList.add("is-turning-out");
      setTimeout(apply, 80);
    } else apply();
  });
}

async function goRelative(dir) {
  if (!state.doc || state.rendering) return;

  if (state.doc.format === "epub") {
    // Continuous book: step by one viewport "page"
    const v = el.epubBook;
    if (!v) return;
    const pageH = Math.max(1, v.clientHeight);
    v.scrollBy({ top: dir * pageH * 0.92, behavior: "smooth" });
    return;
  }

  if (state.mode === "scroll") {
    const next = Math.min(
      state.doc.pageCount - 1,
      Math.max(0, state.doc.pageIndex + dir)
    );
    state.doc.pageIndex = next;
    const slot = el.scrollViewport.querySelector(`[data-page="${next}"]`);
    slot?.scrollIntoView({ behavior: "smooth", block: "start" });
    updateChromeMeta();
    scheduleProgress();
    prefetchAround(next);
    return;
  }

  const next = state.doc.pageIndex + dir;
  if (next < 0 || next >= state.doc.pageCount) return;
  state.doc.pageIndex = next;
  await renderPage();
  await flushProgress();
}

async function reloadScroll(clear = true) {
  if (!state.doc || state.doc.format !== "pdf") return;
  const vp = el.scrollViewport;
  if (clear) {
    vp.innerHTML = "";
    state.scrollLoaded = new Set();
  }
  for (let i = 0; i < state.doc.pageCount; i++) {
    let slot = vp.querySelector(`[data-page="${i}"]`);
    if (!slot) {
      slot = document.createElement("div");
      slot.className = "scroll-page-slot";
      slot.dataset.page = String(i);
      slot.textContent = `Page ${i + 1}`;
      vp.appendChild(slot);
    }
  }
  await ensureScrollPage(state.doc.pageIndex);
  ensureScrollPage(state.doc.pageIndex + 1);
  ensureScrollPage(state.doc.pageIndex - 1);
  prefetchAround(state.doc.pageIndex);
  vp.onscroll = onScrollViewport;
}

async function ensureScrollPage(index) {
  if (!state.doc || index < 0 || index >= state.doc.pageCount) return;
  if (state.scrollLoaded.has(index)) return;
  state.scrollLoaded.add(index);
  try {
    const page = await fetchPDFPage(index, { setCurrent: false });
    const slot = el.scrollViewport.querySelector(`[data-page="${index}"]`);
    if (!slot || !page) return;
    slot.className = "scroll-page";
    slot.textContent = "";
    const img = document.createElement("img");
    img.alt = `Page ${index + 1}`;
    img.draggable = false;
    img.onload = () => applyPdfVisualZoom();
    img.src = pageSrc(page);
    slot.appendChild(img);
    applyPdfVisualZoom();
  } catch (err) {
    state.scrollLoaded.delete(index);
    console.error("ensureScrollPage", index, err);
    if (index === (state.doc?.pageIndex | 0)) {
      toast("PDF page load failed: " + String(err?.message || err), true, 8000);
    }
  }
}

function onScrollViewport() {
  const vp = el.scrollViewport;
  // Lazy-load nearby pages always
  const vr = vp.getBoundingClientRect();
  for (const slot of vp.querySelectorAll("[data-page]")) {
    const r = slot.getBoundingClientRect();
    if (r.top < vr.bottom + 900 && r.bottom > vr.top - 900) {
      ensureScrollPage(parseInt(slot.dataset.page, 10));
    }
  }
  // During zoom / open restore, keep the same page — geometry is in flux.
  if (state.zoomLockPage || state.restoring) {
    return;
  }
  let best = state.doc?.pageIndex || 0;
  let bestVis = 0;
  for (const slot of vp.querySelectorAll("[data-page]")) {
    const r = slot.getBoundingClientRect();
    const vis = Math.max(
      0,
      Math.min(r.bottom, vr.bottom) - Math.max(r.top, vr.top)
    );
    if (vis > bestVis) {
      bestVis = vis;
      best = parseInt(slot.dataset.page, 10);
    }
  }
  if (state.doc && best !== state.doc.pageIndex) {
    state.doc.pageIndex = best;
    updateChromeMeta();
    prefetchAround(best);
  }
  scheduleProgress();
}

// ─── EPUB: chapters + page/scroll ────────────────────────────

async function loadTOC() {
  state.toc = [];
  el.tocList.innerHTML = "";
  if (!hasWails()) return;
  try {
    const raw = (await api().GetEPUBTOC()) || [];
    state.toc = raw.map((item, i) => ({
      index: item.index ?? item.Index ?? i,
      label: item.label || item.Label || `Chapter ${(item.index ?? item.Index ?? i) + 1}`,
      href: item.href || item.Href || "",
    }));
  } catch (_) {
    return;
  }
  for (const item of state.toc) {
    const li = document.createElement("li");
    const btn = document.createElement("button");
    btn.type = "button";
    btn.textContent = item.label;
    btn.dataset.index = String(item.index);
    if (item.index === state.epubChapterIndex) btn.classList.add("is-active");
    btn.addEventListener("click", (e) => {
      e.stopPropagation();
      jumpToEpubChapter(item.index);
    });
    li.appendChild(btn);
    el.tocList.appendChild(li);
  }
}

function highlightTOC() {
  el.tocList.querySelectorAll("button").forEach((b) => {
    b.classList.toggle(
      "is-active",
      parseInt(b.dataset.index, 10) === state.epubChapterIndex
    );
  });
}

function escapeHtml(s) {
  return String(s)
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;");
}

function epubChapterSectionHTML(idx, label, html) {
  return (
    `<section class="epub-chapter" data-chapter="${idx}" id="epub-ch-${idx}">` +
    `<header class="epub-chapter-head">${escapeHtml(label)}</header>` +
    `<div class="epub-chapter-body">${html || ""}</div>` +
    `</section>`
  );
}

/**
 * Load the entire EPUB as one continuous vertical document (all chapters).
 * Progressive load — never stuck forever on "Loading book…".
 */
async function loadEpubContinuous(opts = {}) {
  if (!hasWails() || !state.doc) return;
  state.rendering = true;
  state._epubClamping = false;
  el.epubContent.innerHTML =
    '<p class="epub-loading">Loading book…</p>';

  const setStatus = (msg) => {
    const p = el.epubContent.querySelector(".epub-loading");
    if (p) p.textContent = msg;
    else el.epubContent.innerHTML = `<p class="epub-loading">${escapeHtml(msg)}</p>`;
  };

  try {
    const chapters = await fetchAllEpubChapters(setStatus);
    if (!chapters.length) {
      el.epubContent.innerHTML =
        '<p class="epub-loading">This EPUB has no readable chapters.</p>';
      return;
    }

    state.doc.chapterCount = chapters.length;
    // Build off-DOM then swap once (faster than repeated innerHTML)
    const html = chapters
      .map((ch) => {
        const idx = ch.index ?? ch.Index ?? 0;
        const label = ch.label || ch.Label || `Chapter ${idx + 1}`;
        const body = ch.html || ch.HTML || "";
        return epubChapterSectionHTML(idx, label, body);
      })
      .join("");

    el.epubContent.innerHTML = html || "<p>(Empty book)</p>";
    el.epubContent.style.transform = "none";
    el.epubContent.style.height = "auto";
    el.epubContent.style.columns = "auto";

    lockEpubVerticalOnly();
    clampEpubHorizontalOverflow();
    wireEpubInteractions();

    requestAnimationFrame(() => {
      clampEpubHorizontalOverflow();
      lockEpubVerticalOnly();
    });
    // One delayed pass for late images — do not spam (avoids freeze loops)
    setTimeout(() => {
      if (state.doc?.format === "epub") clampEpubHorizontalOverflow();
    }, 500);

    await new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r)));

    // Restore by content fraction (scroll), never by virtual page index.
    // Virtual pages = viewport heights and shift when the window is not fullscreen.
    state.restoring = true;
    try {
      if (opts.restoreScroll > 0) {
        state.epubScrollRatio = opts.restoreScroll;
        await restoreScroll(opts.restoreScroll, { times: 6, gapMs: 100 });
      } else if (typeof opts.restoreChapter === "number" && opts.restoreChapter > 0) {
        jumpToEpubChapter(opts.restoreChapter, false);
      } else {
        el.epubBook.scrollTop = 0;
      }

      // Re-pin after late images (common cause of “opened a few pages off”).
      const imgs = el.epubContent?.querySelectorAll("img") || [];
      if (imgs.length && opts.restoreScroll > 0) {
        await Promise.race([
          Promise.all(
            [...imgs].map(
              (img) =>
                img.complete
                  ? Promise.resolve()
                  : new Promise((res) => {
                      img.addEventListener("load", res, { once: true });
                      img.addEventListener("error", res, { once: true });
                    })
            )
          ),
          new Promise((r) => setTimeout(r, 1500)),
        ]);
        await restoreScroll(opts.restoreScroll, { times: 3, gapMs: 80 });
      }

      updateEpubPositionFromScroll();
      updateChromeMeta();
    } finally {
      state.restoring = false;
    }
    // Persist only after we know we are on the intended content.
    scheduleProgress();
  } catch (err) {
    console.error("EPUB load failed", err);
    const msg = String(err?.message || err || "Failed to load EPUB");
    el.epubContent.innerHTML = `<p class="epub-loading is-error">${escapeHtml(msg)}</p>`;
    toast(msg, true);
    state.restoring = false;
  } finally {
    state.rendering = false;
  }
}

/**
 * Prefer GetAllEPUBChapters; fall back to per-chapter GetEPUBChapter with UI progress.
 */
async function fetchAllEpubChapters(setStatus) {
  const app = api();

  // Fast path: bulk API (if present in this binary)
  if (typeof app.GetAllEPUBChapters === "function") {
    setStatus("Loading book…");
    try {
      const bulk = await Promise.race([
        app.GetAllEPUBChapters(),
        new Promise((_, rej) =>
          setTimeout(() => rej(new Error("timeout")), 120000)
        ),
      ]);
      if (Array.isArray(bulk) && bulk.length) return bulk;
    } catch (e) {
      console.warn("GetAllEPUBChapters failed, falling back", e);
      setStatus("Loading chapters…");
    }
  }

  // Progressive fallback (always works if GetEPUBChapter exists)
  let n = state.doc.chapterCount || state.toc?.length || 0;
  if (typeof app.GetEPUBChapterCount === "function") {
    try {
      const c = await app.GetEPUBChapterCount();
      if (c > 0) n = c;
    } catch (_) {}
  }
  if (!n && typeof app.GetEPUBChapter === "function") {
    // Probe count via TOC already loaded
    n = state.toc?.length || 1;
  }
  if (!n) n = 1;

  const out = [];
  for (let i = 0; i < n; i++) {
    setStatus(`Loading chapter ${i + 1} of ${n}…`);
    try {
      const ch = await app.GetEPUBChapter(i);
      if (ch) {
        out.push(ch);
        // Keep chapterCount in sync if API reports it
        const reported = ch.chapterCount ?? ch.ChapterCount;
        if (reported > n) n = reported;
      } else {
        out.push({
          index: i,
          label: `Chapter ${i + 1}`,
          html: "<p></p>",
          chapterCount: n,
        });
      }
    } catch (e) {
      console.warn("chapter", i, e);
      out.push({
        index: i,
        label: `Chapter ${i + 1}`,
        html: "<p></p>",
        chapterCount: n,
      });
    }
    // Yield to UI so the status text can paint
    if (i % 2 === 1) {
      await new Promise((r) => setTimeout(r, 0));
    }
  }
  return out;
}

function jumpToEpubChapter(index, save = true) {
  const sec = el.epubContent?.querySelector(`#epub-ch-${index}, [data-chapter="${index}"]`);
  if (sec) {
    sec.scrollIntoView({ block: "start", behavior: "auto" });
    state.epubChapterIndex = index;
    highlightTOC();
    updateEpubPositionFromScroll();
    updateChromeMeta();
    if (save) scheduleProgress();
  }
}

function onEpubScroll() {
  if (!state.doc || state.doc.format !== "epub") return;
  if (state.restoring) return;
  updateEpubPositionFromScroll();
  updateChromeMeta();
  scheduleProgress();
}

// Kept for link resolution / chapter jumps from internal links
async function loadEpubChapter(index) {
  jumpToEpubChapter(index);
}

/** Bind once: only vertical scrolling on the EPUB book surface. */
function lockEpubVerticalOnly() {
  const book = el.epubBook;
  const vp = el.epubViewport;
  if (!book || book.dataset.folioScrollLock === "1") {
    if (book) book.scrollLeft = 0;
    return;
  }
  book.dataset.folioScrollLock = "1";

  const killX = () => {
    if (book.scrollLeft !== 0) book.scrollLeft = 0;
    if (vp && vp.scrollLeft !== 0) vp.scrollLeft = 0;
  };

  book.style.overflowX = "hidden";
  book.style.overflowY = "auto";
  book.style.touchAction = "pan-y";
  if (vp) {
    vp.style.overflowX = "hidden";
    vp.style.overflowY = "hidden";
  }

  book.onscroll = (e) => {
    killX();
    onEpubScroll();
  };
  book.addEventListener(
    "wheel",
    (e) => {
      // Fold horizontal wheel into vertical; never allow X scroll
      if (e.deltaX !== 0) {
        e.preventDefault();
        book.scrollTop += e.deltaX + e.deltaY;
      }
      killX();
    },
    { passive: false }
  );
  book.addEventListener(
    "touchmove",
    () => {
      killX();
    },
    { passive: true }
  );
  // Do NOT MutationObserver→clamp (softBreak mutates text → infinite loop / freeze)
  killX();
}

/**
 * Force EPUB content into the column width so text never forces horizontal scroll.
 * Publisher HTML often sets huge fixed widths / nowrap / long unbreakable strings.
 */
function clampEpubHorizontalOverflow() {
  const root = el.epubContent;
  if (!root) return;
  // Re-entrancy guard — softBreak mutates DOM
  if (state._epubClamping) return;
  state._epubClamping = true;
  try {
    clampEpubHorizontalOverflowInner(root);
  } finally {
    state._epubClamping = false;
  }
}

function clampEpubHorizontalOverflowInner(root) {
  // Inject once: strongest CSS override inside the reading column
  if (!root.querySelector("style[data-folio-epub-clamp]")) {
    const st = document.createElement("style");
    st.setAttribute("data-folio-epub-clamp", "1");
    st.textContent = `
      .epub-content, .epub-content * {
        max-width: 100% !important;
        box-sizing: border-box !important;
      }
      .epub-content {
        width: 100% !important;
        overflow-x: hidden !important;
        overflow-wrap: anywhere !important;
        word-break: break-word !important;
        word-wrap: break-word !important;
      }
      .epub-content p, .epub-content div, .epub-content span,
      .epub-content li, .epub-content td, .epub-content th, .epub-content h1,
      .epub-content h2, .epub-content h3, .epub-content h4, .epub-content h5,
      .epub-content h6, .epub-content label, .epub-content em, .epub-content strong {
        white-space: normal !important;
        overflow-wrap: anywhere !important;
        word-break: break-word !important;
        max-width: 100% !important;
      }
      /* Long hyperlinks (e.g. full http://… URLs) — must use break-all */
      .epub-content a, .epub-content a[href], .epub-content a * {
        white-space: normal !important;
        overflow-wrap: anywhere !important;
        word-break: break-all !important;
        word-wrap: break-word !important;
        max-width: 100% !important;
        display: inline !important;
      }
      .epub-content img, .epub-content svg, .epub-content video, .epub-content canvas,
      .epub-content iframe, .epub-content object, .epub-content embed {
        max-width: 100% !important;
        height: auto !important;
        width: auto !important;
      }
      .epub-content table {
        width: 100% !important;
        max-width: 100% !important;
        table-layout: fixed !important;
        display: table !important;
        overflow: hidden !important;
      }
      .epub-content pre, .epub-content code {
        white-space: pre-wrap !important;
        overflow-x: hidden !important;
        word-break: break-word !important;
        max-width: 100% !important;
      }
    `;
    root.insertBefore(st, root.firstChild);
  }

  // Strip HTML width attributes (common in old EPUBs)
  root.querySelectorAll("[width], [height]").forEach((node) => {
    if (node.tagName === "IMG" || node.tagName === "TABLE" || node.tagName === "TD" || node.tagName === "TH") {
      node.removeAttribute("width");
      if (node.tagName === "IMG") node.removeAttribute("height");
    }
  });

  root.querySelectorAll("[style]").forEach((node) => {
    const s = node.getAttribute("style") || "";
    let next = s
      .replace(/max-width\s*:\s*[^;]+;?/gi, "")
      .replace(/min-width\s*:\s*[^;]+;?/gi, "")
      .replace(/width\s*:\s*[^;]+;?/gi, "width:100%;")
      .replace(/white-space\s*:\s*nowrap;?/gi, "white-space:normal;")
      .replace(/position\s*:\s*absolute;?/gi, "position:relative;")
      .replace(/left\s*:\s*[^;]+;?/gi, "")
      .replace(/margin-left\s*:\s*-?\d+px;?/gi, "")
      .replace(/transform\s*:\s*[^;]+;?/gi, "");
    if (next !== s) node.setAttribute("style", next);
  });

  root.querySelectorAll("img, svg, table, pre, video, iframe, object, embed").forEach((node) => {
    node.style.maxWidth = "100%";
    node.style.boxSizing = "border-box";
    if (node.tagName === "IMG" || node.tagName === "SVG" || node.tagName === "VIDEO") {
      node.style.height = "auto";
      node.style.width = "auto";
    }
    if (node.tagName === "TABLE") {
      node.style.width = "100%";
      node.style.tableLayout = "fixed";
      node.style.overflow = "hidden";
    }
    if (node.tagName === "PRE" || node.tagName === "CODE") {
      node.style.whiteSpace = "pre-wrap";
      node.style.overflowX = "hidden";
      node.style.wordBreak = "break-word";
    }
  });

  // Long bare URLs / link text cannot use normal word boundaries — soft-break them
  softBreakLongUrls(root);

  // Pin every shell node to the book column width (px), not just %
  const col = el.epubBook?.clientWidth || el.epubViewport?.clientWidth || 0;
  [el.epubViewport, el.epubBook, el.epubPages, el.epubContent].forEach((n) => {
    if (!n) return;
    n.style.overflowX = "hidden";
    n.style.maxWidth = "100%";
    n.style.minWidth = "0";
    if (n === el.epubBook) {
      n.scrollLeft = 0;
      n.style.overflowY = "auto";
    }
  });
  if (col > 40 && el.epubContent) {
    // Hard pixel cap so % max-width children cannot expand past the column
    const inner = Math.max(120, col - 2);
    el.epubPages.style.width = `${inner}px`;
    el.epubPages.style.maxWidth = `${inner}px`;
    el.epubContent.style.width = `${inner}px`;
    el.epubContent.style.maxWidth = `${inner}px`;
    el.epubContent.querySelectorAll(".epub-chapter, .epub-chapter-body").forEach((sec) => {
      sec.style.maxWidth = `${inner}px`;
      sec.style.width = "100%";
      sec.style.minWidth = "0";
      sec.style.overflowX = "hidden";
    });
  }
  if (el.epubBook) el.epubBook.scrollLeft = 0;
  if (el.epubViewport) el.epubViewport.scrollLeft = 0;
}

/**
 * Insert zero-width break opportunities into long URLs / unbroken tokens.
 * Skips nodes already processed (data-folio-softbroken).
 */
function softBreakLongUrls(root) {
  if (!root) return;
  const ZWSP = "\u200B";

  const breakToken = (s) =>
    s
      .replace(/(https?:\/\/[^\s<>"']+)/gi, (url) =>
        url
          .replace(/([\/\?&=._\-+%#:~])/g, `$1${ZWSP}`)
          .replace(/(.{18})/g, `$1${ZWSP}`)
      )
      .replace(/([^\s]{24})(?!\u200B)/g, `$1${ZWSP}`);

  // Only style actual links (a[href]) — anchor-only <a> tags (no href) are
  // used by many EPUBs for positioning and must not get link word-break styles.
  root.querySelectorAll("a[href]").forEach((a) => {
    a.style.wordBreak = "break-all";
    a.style.overflowWrap = "anywhere";
    a.style.whiteSpace = "normal";
    a.style.maxWidth = "100%";
  });

  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT, null);
  const texts = [];
  while (walker.nextNode()) texts.push(walker.currentNode);

  for (const tn of texts) {
    if (tn.parentElement?.closest?.("style,script")) continue;
    // Avoid re-processing (would stack ZWSPs forever)
    if (tn._folioSoftBroken) continue;
    const t = tn.nodeValue;
    if (!t || t.length < 30) continue;
    if (!/https?:\/\/|[A-Za-z0-9_\-./%=]{40,}/.test(t)) continue;
    // Already has soft breaks from a prior pass
    if (t.includes(ZWSP) && t.length > 80) {
      tn._folioSoftBroken = true;
      continue;
    }
    const next = breakToken(t);
    if (next !== t) {
      tn.nodeValue = next;
      tn._folioSoftBroken = true;
    }
  }
}

function wireEpubInteractions() {
  clampEpubHorizontalOverflow();
  // Links
  el.epubContent.querySelectorAll("a[href]").forEach((a) => {
    a.addEventListener("click", onEpubLinkClick);
  });
  // Images
  el.epubContent.querySelectorAll("img").forEach((img) => {
    img.addEventListener("click", (e) => {
      e.preventDefault();
      e.stopPropagation();
      openLightbox(img.src);
    });
  });
}

async function onEpubLinkClick(e) {
  e.preventDefault();
  e.stopPropagation();
  const a = e.currentTarget;
  const href = a.getAttribute("href") || "";
  if (!href) return;

  const lower = href.toLowerCase();
  if (
    lower.startsWith("http://") ||
    lower.startsWith("https://") ||
    lower.startsWith("mailto:")
  ) {
    if (hasWails()) {
      try {
        await api().OpenExternalURL(href);
      } catch (err) {
        toast(String(err), true);
      }
    } else {
      window.open(href, "_blank");
    }
    return;
  }

  if (hasWails()) {
    try {
      const res = await api().ResolveEPUBLink(href);
      if (res && res.ok) {
        jumpToEpubChapter(res.index);
        if (res.fragment) {
          requestAnimationFrame(() => {
            const t = el.epubContent.querySelector(
              `#${CSS.escape(res.fragment)}, [name="${CSS.escape(res.fragment)}"]`
            );
            t?.scrollIntoView({ behavior: "smooth", block: "start" });
          });
        }
        return;
      }
    } catch (_) {}
  }
  if (href.startsWith("#")) {
    const id = href.slice(1);
    const t = el.epubContent.querySelector(
      `#${CSS.escape(id)}, [name="${CSS.escape(id)}"]`
    );
    t?.scrollIntoView({ behavior: "smooth", block: "start" });
  }
}

// ─── Lightbox ────────────────────────────────────────────────

function openLightbox(src) {
  el.lightboxImg.src = src;
  el.lightbox.classList.remove("is-hidden");
}
function closeLightbox() {
  el.lightbox.classList.add("is-hidden");
  el.lightboxImg.removeAttribute("src");
}

// ─── Reading guide ───────────────────────────────────────────

function applyGuideUI() {
  const on = state.guideOn;
  el.guide.classList.toggle("is-hidden", !on);
  el.guideBtn.classList.toggle("is-on", on);
  el.guideSettingsBtn.classList.toggle("is-hidden", !on);
  el.guide.classList.toggle("is-unlocked", on && !state.guideLocked);
  el.guide.setAttribute("aria-hidden", on ? "false" : "true");

  document.documentElement.style.setProperty(
    "--guide-height",
    `${state.guideHeight}px`
  );
  document.documentElement.style.setProperty(
    "--guide-hue",
    String(state.guideHue)
  );
  document.documentElement.style.setProperty(
    "--guide-opacity",
    String(state.guideOpacity / 100)
  );
  document.documentElement.style.setProperty(
    "--guide-top",
    `${state.guideTop}vh`
  );

  if (el.guideHeight) el.guideHeight.value = String(state.guideHeight);
  if (el.guideHue) el.guideHue.value = String(state.guideHue);
  if (el.guideOpacity) el.guideOpacity.value = String(state.guideOpacity);
  if (el.guideLock) el.guideLock.checked = state.guideLocked;

  syncGuideWidth();
}

function syncGuideWidth() {
  if (!state.guideOn || !el.guide) return;
  // Align guide left+width to the book edges (not center-of-screen + 50%)
  let rect = null;
  if (state.doc?.format === "pdf" && state.mode === "page") {
    rect = el.pageFrame?.getBoundingClientRect();
  } else if (state.doc?.format === "pdf" && state.mode === "scroll") {
    const slot =
      el.scrollViewport.querySelector(`[data-page="${state.doc.pageIndex}"]`) ||
      el.scrollViewport.querySelector(".scroll-page, .scroll-page-slot");
    rect = slot?.getBoundingClientRect();
  } else if (state.doc?.format === "epub") {
    rect = el.epubBook?.getBoundingClientRect();
  }
  if (rect && rect.width > 40) {
    el.guide.style.left = `${Math.round(rect.left)}px`;
    el.guide.style.width = `${Math.round(rect.width)}px`;
    el.guide.style.transform = "none";
    el.guide.style.right = "auto";
  } else {
    el.guide.style.left = "50%";
    el.guide.style.width = "min(920px, calc(100vw - 2rem))";
    el.guide.style.transform = "translateX(-50%)";
  }
}

function toggleGuide() {
  state.guideOn = !state.guideOn;
  localStorage.setItem("folio.guideOn", state.guideOn ? "1" : "0");
  if (!state.guideOn) el.guidePanel.classList.add("is-hidden");
  applyGuideUI();
}

function persistGuide() {
  localStorage.setItem("folio.guideTop", String(state.guideTop));
  localStorage.setItem("folio.guideHeight", String(state.guideHeight));
  localStorage.setItem("folio.guideHue", String(state.guideHue));
  localStorage.setItem("folio.guideOpacity", String(state.guideOpacity));
  localStorage.setItem("folio.guideLock", state.guideLocked ? "1" : "0");
}

// ─── Mode toggle ─────────────────────────────────────────────

async function toggleMode() {
  if (state.doc?.format === "epub") {
    toast("EPUB is continuous scroll — use chapters to jump.");
    return;
  }
  const next = state.mode === "page" ? "scroll" : "page";
  applyMode(next);
  setupViewports();
  if (!state.doc) return;
  if (next === "scroll") await reloadScroll(true);
  else await renderPage();
}

// ─── Fonts UI ────────────────────────────────────────────────

function buildFontChips() {
  const make = (list, root) => {
    root.innerHTML = "";
    for (const f of list) {
      const b = document.createElement("button");
      b.type = "button";
      b.className = "font-chip" + (state.font === f.id ? " is-active" : "");
      b.dataset.font = f.id;
      b.textContent = f.label;
      b.addEventListener("click", () => applyFont(f.id));
      root.appendChild(b);
    }
  };
  make(FONTS_EN, el.fontEn);
  make(FONTS_FA, el.fontFa);
}

// ─── Folio account (api.ghaemghh.ir) ─────────────────────────

function updateAccountChrome() {
  const logged = isLoggedIn();
  if (el.btnAccount) {
    el.btnAccount.classList.toggle("is-signed-in", logged);
    el.btnAccount.title = logged ? `Signed in as ${account.username}` : "Sign in";
  }
  if (el.btnAccountLabel) {
    el.btnAccountLabel.textContent = logged ? account.username : "Sign in";
  }
  if (el.btnCatalogUpload) {
    el.btnCatalogUpload.title = logged
      ? "Upload a book to the shared library"
      : "Sign in to upload books (browse & download stay free)";
  }
}

function showModal(overlay) {
  if (!overlay) return;
  overlay.hidden = false;
  overlay.classList.remove("is-hidden");
  document.body.style.overflow = "hidden";
}

function hideModal(overlay) {
  if (!overlay) return;
  overlay.classList.add("is-hidden");
  overlay.hidden = true;
  if (
    el.accountModal?.classList.contains("is-hidden") &&
    el.uploadModal?.classList.contains("is-hidden")
  ) {
    document.body.style.overflow = "";
  }
}

function setAccountMode(mode) {
  account.mode = mode === "register" ? "register" : "login";
  const reg = account.mode === "register";
  el.accountTabLogin?.classList.toggle("is-active", !reg);
  el.accountTabRegister?.classList.toggle("is-active", reg);
  if (el.accountTabLogin) el.accountTabLogin.setAttribute("aria-selected", String(!reg));
  if (el.accountTabRegister) el.accountTabRegister.setAttribute("aria-selected", String(reg));
  if (el.accountSubmitLabel) el.accountSubmitLabel.textContent = reg ? "Create account" : "Sign in";
  if (el.accountPassword) {
    el.accountPassword.autocomplete = reg ? "new-password" : "current-password";
  }
  if (el.accountError) {
    el.accountError.classList.add("is-hidden");
    el.accountError.textContent = "";
  }
}

function openAccountModal() {
  const logged = isLoggedIn();
  el.accountForm?.classList.toggle("is-hidden", logged);
  el.accountLoggedIn?.classList.toggle("is-hidden", !logged);
  document.querySelector(".account-tabs")?.classList.toggle("is-hidden", logged);
  if (el.accountModalTitle) {
    el.accountModalTitle.textContent = logged ? "Your account" : "Welcome to Folio";
  }
  if (el.accountModalSub) {
    el.accountModalSub.textContent = logged
      ? "Upload books for everyone, or sign out on this device."
      : "Browse and download freely. Sign in only if you want to upload books to the shared library.";
  }
  if (logged && el.accountUsernameDisplay) {
    el.accountUsernameDisplay.textContent = account.username;
  }
  if (!logged) {
    setAccountMode(account.mode || "login");
    if (el.accountPassword) el.accountPassword.value = "";
  }
  showModal(el.accountModal);
  if (!logged) setTimeout(() => el.accountUsername?.focus(), 50);
}

function closeAccountModal() {
  hideModal(el.accountModal);
}

async function submitAccountForm(e) {
  e?.preventDefault?.();
  const username = (el.accountUsername?.value || "").trim();
  const password = el.accountPassword?.value || "";
  if (username.length < 2) {
    showAccountError("Username must be at least 2 characters.");
    return;
  }
  if (password.length < 6) {
    showAccountError("Password must be at least 6 characters.");
    return;
  }
  setAccountBusy(true);
  showAccountError("");
  try {
    const path = account.mode === "register" ? "/register" : "/login";
    const data = await folioApi(path, {
      method: "POST",
      body: { username, password },
      token: "",
    });
    saveAccountSession(data.token, data.username || username, data.user_id || data.userId || 0);
    toast(account.mode === "register" ? `Welcome, ${account.username}!` : `Signed in as ${account.username}`);
    openAccountModal(); // refresh to logged-in view
  } catch (err) {
    showAccountError(err.message || "Sign-in failed");
  } finally {
    setAccountBusy(false);
  }
}

function showAccountError(msg) {
  if (!el.accountError) return;
  if (!msg) {
    el.accountError.classList.add("is-hidden");
    el.accountError.textContent = "";
    return;
  }
  el.accountError.textContent = msg;
  el.accountError.classList.remove("is-hidden");
}

function setAccountBusy(busy) {
  if (el.accountSubmit) el.accountSubmit.disabled = !!busy;
  el.accountSubmitSpinner?.classList.toggle("is-hidden", !busy);
}

function logoutAccount() {
  clearAccountSession();
  toast("Signed out");
  closeAccountModal();
}

function requireLoginForUpload() {
  if (isLoggedIn()) return true;
  toast("Sign in to upload books — browsing stays free.");
  openAccountModal();
  setAccountMode("login");
  return false;
}

function openUploadModal() {
  if (!requireLoginForUpload()) return;
  closeAccountModal();
  resetUploadForm();
  showModal(el.uploadModal);
}

/** Prefill upload modal from the currently open local book (reader Upload). */
function openUploadModalForOpenBook() {
  if (!state.doc?.path) {
    toast("No local book open", true);
    return;
  }
  if (!requireLoginForUpload()) return;
  closeAccountModal();
  resetUploadForm();
  account.uploadLocalPath = state.doc.path;
  account.uploadFile = null;
  const base = (state.doc.path || "").split(/[/\\]/).pop() || "book";
  if (el.uploadTitle) el.uploadTitle.value = state.doc.title || base.replace(/\.(pdf|epub)$/i, "");
  if (el.uploadAuthor) el.uploadAuthor.value = "";
  if (el.uploadFileName) {
    el.uploadFileName.textContent = base;
    el.uploadFileName.classList.remove("is-hidden");
  }
  if (el.uploadDropTitle) el.uploadDropTitle.textContent = "Selected from library";
  el.uploadDrop?.classList.add("is-ready");
  if (el.uploadSubmit) el.uploadSubmit.disabled = false;
  showModal(el.uploadModal);
}

function closeUploadModal() {
  if (account.uploading) return;
  hideModal(el.uploadModal);
  resetUploadForm();
}

function resetUploadForm() {
  account.uploadFile = null;
  account.uploadLocalPath = "";
  if (el.uploadFile) el.uploadFile.value = "";
  if (el.uploadTitle) el.uploadTitle.value = "";
  if (el.uploadAuthor) el.uploadAuthor.value = "";
  if (el.uploadFileName) {
    el.uploadFileName.textContent = "";
    el.uploadFileName.classList.add("is-hidden");
  }
  if (el.uploadDropTitle) el.uploadDropTitle.textContent = "Drop a book here";
  el.uploadDrop?.classList.remove("is-ready", "is-dragover");
  if (el.uploadSubmit) el.uploadSubmit.disabled = true;
  el.uploadProgressWrap?.classList.add("is-hidden");
  setUploadProgress(0, "Uploading…");
  showUploadError("");
  el.uploadSubmitSpinner?.classList.add("is-hidden");
}

function setUploadFile(file) {
  if (!file) return;
  const name = (file.name || "").toLowerCase();
  const ok =
    name.endsWith(".pdf") ||
    name.endsWith(".epub") ||
    /pdf|epub/.test(file.type || "");
  if (!ok) {
    showUploadError("Please choose a PDF or EPUB file.");
    return;
  }
  account.uploadFile = file;
  showUploadError("");
  if (el.uploadFileName) {
    el.uploadFileName.textContent = file.name;
    el.uploadFileName.classList.remove("is-hidden");
  }
  if (el.uploadDropTitle) el.uploadDropTitle.textContent = "Ready to upload";
  el.uploadDrop?.classList.add("is-ready");
  if (el.uploadSubmit) el.uploadSubmit.disabled = false;
  if (el.uploadTitle && !el.uploadTitle.value) {
    el.uploadTitle.value = file.name.replace(/\.(pdf|epub)$/i, "");
  }
}

function showUploadError(msg) {
  if (!el.uploadError) return;
  if (!msg) {
    el.uploadError.classList.add("is-hidden");
    el.uploadError.textContent = "";
    return;
  }
  el.uploadError.textContent = msg;
  el.uploadError.classList.remove("is-hidden");
}

function setUploadProgress(pct, label) {
  const p = Math.max(0, Math.min(100, Math.round(pct)));
  if (el.uploadProgressFill) el.uploadProgressFill.style.width = `${p}%`;
  if (el.uploadProgressPct) el.uploadProgressPct.textContent = `${p}%`;
  if (el.uploadProgressBar) el.uploadProgressBar.setAttribute("aria-valuenow", String(p));
  if (label && el.uploadProgressLabel) el.uploadProgressLabel.textContent = label;
}

function uploadBookWithProgress(file, title, author, onProgress) {
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    xhr.open("POST", `${FOLIO_API_BASE}/books/upload`);
    xhr.setRequestHeader("Authorization", `Bearer ${account.token}`);
    xhr.upload.onprogress = (ev) => {
      if (!ev.lengthComputable) return;
      onProgress?.((ev.loaded / ev.total) * 100);
    };
    xhr.onload = () => {
      let data = null;
      try {
        data = xhr.responseText ? JSON.parse(xhr.responseText) : null;
      } catch {
        data = { error: xhr.responseText || xhr.statusText };
      }
      if (xhr.status >= 200 && xhr.status < 300) resolve(data);
      else {
        const err = new Error((data && data.error) || `HTTP ${xhr.status}`);
        err.status = xhr.status;
        reject(err);
      }
    };
    xhr.onerror = () => reject(new Error("Network error during upload"));
    xhr.onabort = () => reject(new Error("Upload cancelled"));
    const fd = new FormData();
    fd.append("file", file, file.name);
    if (title) fd.append("title", title);
    if (author) fd.append("author", author);
    xhr.send(fd);
  });
}

async function submitUpload() {
  if (!isLoggedIn()) {
    requireLoginForUpload();
    return;
  }
  const hasFile = !!account.uploadFile;
  const hasLocal = !!(account.uploadLocalPath && (hasWails() || typeof window.FolioAndroid !== "undefined"));
  if ((!hasFile && !hasLocal) || account.uploading) return;
  account.uploading = true;
  if (el.uploadSubmit) el.uploadSubmit.disabled = true;
  el.uploadSubmitSpinner?.classList.remove("is-hidden");
  el.uploadProgressWrap?.classList.remove("is-hidden");
  setUploadProgress(0, "Starting upload…");
  showUploadError("");
  try {
    const title = (el.uploadTitle?.value || "").trim();
    const author = (el.uploadAuthor?.value || "").trim();
    let result;
    if (hasLocal && hasWails() && typeof api().UploadLocalFile === "function") {
      // Desktop: stream file from disk via Go (progress via events)
      const unsub = wireNativeUploadProgress();
      try {
        result = await api().UploadLocalFile(
          FOLIO_API_BASE,
          account.token,
          account.uploadLocalPath,
          title,
          author
        );
      } finally {
        unsub?.();
      }
    } else if (hasFile) {
      result = await uploadBookWithProgress(
        account.uploadFile,
        title,
        author,
        (pct) => setUploadProgress(pct, pct < 100 ? "Uploading…" : "Processing…")
      );
    } else {
      throw new Error("No file selected");
    }
    setUploadProgress(100, result?.deduped ? "Already in library" : "Upload complete");
    const book = result?.book || result;
    toast(
      result?.deduped
        ? `"${book?.title || "Book"}" was already in the library`
        : `"${book?.title || "Book"}" uploaded — refresh catalog to see it`
    );
    setTimeout(() => {
      account.uploading = false;
      closeUploadModal();
      if (state.libTab === "catalog") loadCatalogInitial().catch(() => {});
    }, 650);
  } catch (err) {
    account.uploading = false;
    if (el.uploadSubmit) el.uploadSubmit.disabled = false;
    el.uploadSubmitSpinner?.classList.add("is-hidden");
    const msg = String(err?.message || err || "Upload failed");
    if (err.status === 401 || /not signed in|unauthorized|invalid or expired/i.test(msg)) {
      clearAccountSession();
      showUploadError("Session expired — please sign in again.");
      setTimeout(() => {
        closeUploadModal();
        openAccountModal();
      }, 800);
      return;
    }
    showUploadError(msg);
  }
}

function wireNativeUploadProgress() {
  try {
    const rt = window.runtime;
    if (!rt || typeof rt.EventsOn !== "function") return () => {};
    const off = rt.EventsOn("folio:upload-progress", (payload) => {
      const pct = Number(payload?.percent) || 0;
      const msg = payload?.message || "Uploading…";
      setUploadProgress(pct, msg);
    });
    return typeof off === "function" ? off : () => {};
  } catch {
    return () => {};
  }
}

function wireAccountUI() {
  updateAccountChrome();
  el.btnAccount?.addEventListener("click", () => openAccountModal());
  el.accountModalClose?.addEventListener("click", () => closeAccountModal());
  el.accountModal?.addEventListener("click", (e) => {
    if (e.target === el.accountModal) closeAccountModal();
  });
  el.accountTabLogin?.addEventListener("click", () => setAccountMode("login"));
  el.accountTabRegister?.addEventListener("click", () => setAccountMode("register"));
  el.accountForm?.addEventListener("submit", submitAccountForm);
  el.btnAccountLogout?.addEventListener("click", logoutAccount);
  el.btnAccountUpload?.addEventListener("click", () => openUploadModal());
  el.btnCatalogUpload?.addEventListener("click", () => openUploadModal());
  el.readerUpload?.addEventListener("click", (e) => {
    e.stopPropagation();
    openUploadModalForOpenBook();
  });

  el.uploadModalClose?.addEventListener("click", () => closeUploadModal());
  el.uploadCancel?.addEventListener("click", () => closeUploadModal());
  el.uploadModal?.addEventListener("click", (e) => {
    if (e.target === el.uploadModal && !account.uploading) closeUploadModal();
  });
  el.uploadDrop?.addEventListener("click", () => el.uploadFile?.click());
  el.uploadDrop?.addEventListener("keydown", (e) => {
    if (e.key === "Enter" || e.key === " ") {
      e.preventDefault();
      el.uploadFile?.click();
    }
  });
  el.uploadFile?.addEventListener("change", () => {
    const f = el.uploadFile.files && el.uploadFile.files[0];
    if (f) setUploadFile(f);
  });
  ["dragenter", "dragover"].forEach((ev) => {
    el.uploadDrop?.addEventListener(ev, (e) => {
      e.preventDefault();
      e.stopPropagation();
      el.uploadDrop.classList.add("is-dragover");
    });
  });
  ["dragleave", "drop"].forEach((ev) => {
    el.uploadDrop?.addEventListener(ev, (e) => {
      e.preventDefault();
      e.stopPropagation();
      el.uploadDrop.classList.remove("is-dragover");
    });
  });
  el.uploadDrop?.addEventListener("drop", (e) => {
    const f = e.dataTransfer?.files && e.dataTransfer.files[0];
    if (f) setUploadFile(f);
  });
  el.uploadSubmit?.addEventListener("click", () => submitUpload());

  document.addEventListener("keydown", (e) => {
    if (e.key !== "Escape") return;
    if (!el.uploadModal?.classList.contains("is-hidden") && !account.uploading) {
      closeUploadModal();
      return;
    }
    if (!el.accountModal?.classList.contains("is-hidden")) closeAccountModal();
  });
}

// ─── Events ──────────────────────────────────────────────────

function bindEvents() {
  wireAccountUI();
  el.tabShelf?.addEventListener("click", () => switchLibTab("shelf"));
  el.tabCatalog?.addEventListener("click", () => switchLibTab("catalog"));
  el.btnCatalogSettings?.addEventListener("click", () => {
    el.catalogSettings?.classList.toggle("is-hidden");
    loadOPDSSettingsIntoForm();
  });
  el.btnCatalogSetup?.addEventListener("click", () => {
    el.catalogSettings?.classList.remove("is-hidden");
    el.opdsBaseURL?.focus();
  });
  el.btnCatalogRefresh?.addEventListener("click", () => {
    if (el.catalogSearch) el.catalogSearch.value = state.catalog.query || "";
    loadCatalogInitial();
  });
  el.btnOpdsSave?.addEventListener("click", () => saveOPDSSettings());
  el.catalogSearch?.addEventListener("input", onCatalogSearchInput);
  el.catalogSearch?.addEventListener("keydown", (e) => {
    if (e.key === "Escape") {
      el.catalogSearch.value = "";
      state.catalog.query = "";
      if (state.catalog.searchTimer) clearTimeout(state.catalog.searchTimer);
      loadCatalogInitial();
    } else if (e.key === "Enter") {
      e.preventDefault();
      if (state.catalog.searchTimer) clearTimeout(state.catalog.searchTimer);
      const q = (el.catalogSearch.value || "").trim();
      if (!q) {
        state.catalog.query = "";
        loadCatalogInitial();
      } else runCatalogSearch(q);
    }
  });
  el.libMainCatalog?.addEventListener("scroll", onCatalogScroll, {
    passive: true,
  });
  wireOPDSProgressEvents();

  el.openFile?.addEventListener("click", openFile);
  el.openFileHero?.addEventListener("click", openFile);
  el.themeCycle?.addEventListener("click", cycleTheme);
  el.readerTheme?.addEventListener("click", (e) => {
    e.stopPropagation();
    cycleTheme();
  });
  el.back?.addEventListener("click", (e) => {
    e.stopPropagation();
    closeReader();
  });
  el.prev?.addEventListener("click", (e) => {
    e.stopPropagation();
    goRelative(-1);
  });
  el.next?.addEventListener("click", (e) => {
    e.stopPropagation();
    goRelative(1);
  });
  el.edgePrev?.addEventListener("click", (e) => {
    e.stopPropagation();
    goRelative(-1);
  });
  el.edgeNext?.addEventListener("click", (e) => {
    e.stopPropagation();
    goRelative(1);
  });
  el.modeToggle?.addEventListener("click", (e) => {
    e.stopPropagation();
    toggleMode();
  });
  el.zoomIn?.addEventListener("click", (e) => {
    e.stopPropagation();
    applyZoom(state.zoom + ZOOM_STEP);
  });
  el.zoomOut?.addEventListener("click", (e) => {
    e.stopPropagation();
    applyZoom(state.zoom - ZOOM_STEP);
  });
  // Prevent accidental page changes while zooming with wheel+ctrl
  el.fontMenu?.addEventListener("click", (e) => {
    e.stopPropagation();
    const opening = el.fontPanel.classList.contains("is-hidden");
    el.guidePanel.classList.add("is-hidden");
    if (opening) {
      el.fontPanel.classList.remove("is-hidden");
      showChromeBriefly();
    } else {
      el.fontPanel.classList.add("is-hidden");
    }
  });
  el.tocBtn?.addEventListener("click", (e) => {
    e.stopPropagation();
    const opening = el.tocPanel.classList.contains("is-hidden");
    el.fontPanel.classList.add("is-hidden");
    el.guidePanel.classList.add("is-hidden");
    if (opening) {
      el.tocPanel.classList.remove("is-hidden");
      showChromeBriefly();
    } else {
      el.tocPanel.classList.add("is-hidden");
    }
  });
  el.tocClose?.addEventListener("click", (e) => {
    e.stopPropagation();
    el.tocPanel.classList.add("is-hidden");
  });
  // Keep panels alive while hovering / scrolling them
  [el.fontPanel, el.guidePanel, el.tocPanel].forEach((p) => {
    p?.addEventListener("mouseenter", () => {
      cancelChromeHide();
      showChromeBriefly();
    });
    p?.addEventListener("mouseleave", () => {
      // only schedule chrome hide if panel still open and mouse left entirely
      if (!anyPanelOpen()) scheduleHideChrome();
    });
    p?.addEventListener("pointerdown", (e) => e.stopPropagation());
    p?.addEventListener("wheel", (e) => e.stopPropagation(), { passive: true });
  });
  el.tocList?.addEventListener("scroll", (e) => e.stopPropagation());
  el.guideBtn?.addEventListener("click", (e) => {
    e.stopPropagation();
    toggleGuide();
    showChromeBriefly();
  });
  el.guideSettingsBtn?.addEventListener("click", (e) => {
    e.stopPropagation();
    const opening = el.guidePanel.classList.contains("is-hidden");
    el.fontPanel.classList.add("is-hidden");
    if (opening) {
      el.guidePanel.classList.remove("is-hidden");
      showChromeBriefly();
    } else {
      el.guidePanel.classList.add("is-hidden");
    }
  });

  el.guideHeight?.addEventListener("input", () => {
    state.guideHeight = parseInt(el.guideHeight.value, 10);
    persistGuide();
    applyGuideUI();
  });
  el.guideHue?.addEventListener("input", () => {
    state.guideHue = parseInt(el.guideHue.value, 10);
    persistGuide();
    applyGuideUI();
  });
  el.guideOpacity?.addEventListener("input", () => {
    state.guideOpacity = parseInt(el.guideOpacity.value, 10);
    persistGuide();
    applyGuideUI();
  });
  el.guideLock?.addEventListener("change", () => {
    state.guideLocked = el.guideLock.checked;
    persistGuide();
    applyGuideUI();
  });

  // Drag guide vertically (screen-fixed)
  el.guide?.addEventListener("pointerdown", (e) => {
    if (state.guideLocked || !state.guideOn) return;
    state.guideDragging = true;
    el.guide.setPointerCapture(e.pointerId);
  });
  el.guide?.addEventListener("pointermove", (e) => {
    if (!state.guideDragging) return;
    const vh = window.innerHeight || 1;
    const y = (e.clientY / vh) * 100;
    state.guideTop = Math.min(92, Math.max(4, y));
    document.documentElement.style.setProperty(
      "--guide-top",
      `${state.guideTop}vh`
    );
  });
  el.guide?.addEventListener("pointerup", () => {
    if (!state.guideDragging) return;
    state.guideDragging = false;
    persistGuide();
  });

  el.lightbox?.addEventListener("click", (e) => {
    if (e.target === el.lightbox || e.target === el.lightboxClose) closeLightbox();
  });
  el.lightboxClose?.addEventListener("click", closeLightbox);

  // Click-drag pan (Android-like) on reading surfaces
  setupDragPan(el.pageViewport);
  setupDragPan(el.scrollViewport);
  setupDragPan(el.epubBook);
  setupDragPan(el.epubPages);

  // Edge hotspots only — do NOT show chrome on content click or general move
  el.hotspotTop?.addEventListener("mouseenter", () => {
    cancelChromeHide();
    showChromeBriefly();
  });
  el.hotspotBottom?.addEventListener("mouseenter", () => {
    cancelChromeHide();
    showChromeBriefly();
  });
  el.reader
    ?.querySelector(".reader-chrome--top")
    ?.addEventListener("mouseenter", cancelChromeHide);
  el.reader
    ?.querySelector(".reader-chrome--bottom")
    ?.addEventListener("mouseenter", cancelChromeHide);
  el.reader
    ?.querySelector(".reader-chrome--top")
    ?.addEventListener("mouseleave", (e) => {
      // Moving into a panel should not hide chrome
      if (e.relatedTarget?.closest?.(".font-panel, .guide-panel, .toc-panel")) {
        cancelChromeHide();
        return;
      }
      scheduleHideChrome();
    });
  el.reader
    ?.querySelector(".reader-chrome--bottom")
    ?.addEventListener("mouseleave", () => scheduleHideChrome());

  // Content click: hide chrome + popovers (not TOC drawer unless desired)
  el.stage?.addEventListener("click", (e) => {
    if (
      e.target.closest(".edge-nav") ||
      e.target.closest(".reader-chrome") ||
      e.target.closest(".font-panel") ||
      e.target.closest(".guide-panel") ||
      e.target.closest(".toc-panel") ||
      e.target.closest(".reading-guide") ||
      e.target.closest("a") ||
      e.target.closest("img")
    )
      return;
    el.fontPanel.classList.add("is-hidden");
    el.guidePanel.classList.add("is-hidden");
    hideChrome();
  });

  // NO mousemove bumpChrome on stage

  // EPUB continuous scroll handler is set in loadEpubContinuous (onEpubScroll)

  let resizeTimer = null;
  window.addEventListener("resize", () => {
    // Debounce: keep the same *content* under the eye when the window size
    // changes. Virtual page numbers will update; the text must not jump.
    if (resizeTimer) clearTimeout(resizeTimer);
    resizeTimer = setTimeout(() => {
      if (state.doc?.format === "epub") {
        preserveEpubContentOnResize();
      } else if (state.doc?.format === "pdf") {
        applyPdfVisualZoom();
        // Page mode is already page-index based (stable). Scroll mode: re-pin.
        if (state.mode === "scroll" && state.doc.pageIndex != null) {
          const stay = state.doc.pageIndex;
          state.restoring = true;
          scrollPdfToPage(stay, { smooth: false }).finally(() => {
            state.restoring = false;
            updateChromeMeta();
          });
        }
      }
      syncGuideWidth();
    }, 80);
  });

  window.addEventListener("keydown", (e) => {
    if (state.view !== "reader") {
      if ((e.key === "o" || e.key === "O") && (e.ctrlKey || e.metaKey)) {
        e.preventDefault();
        openFile();
      }
      return;
    }
    if (!el.lightbox.classList.contains("is-hidden")) {
      if (e.key === "Escape") {
        e.preventDefault();
        closeLightbox();
      }
      return;
    }
    switch (e.key) {
      case "ArrowRight":
      case "PageDown":
      case " ":
        if (e.target.closest("input,textarea")) return;
        e.preventDefault();
        goRelative(1);
        break;
      case "ArrowLeft":
      case "PageUp":
        e.preventDefault();
        goRelative(-1);
        break;
      case "Escape":
        e.preventDefault();
        if (!el.tocPanel.classList.contains("is-hidden")) {
          el.tocPanel.classList.add("is-hidden");
        } else if (!el.fontPanel.classList.contains("is-hidden")) {
          el.fontPanel.classList.add("is-hidden");
        } else if (!el.guidePanel.classList.contains("is-hidden")) {
          el.guidePanel.classList.add("is-hidden");
        } else if (state.chromeVisible) {
          hideChrome();
        } else closeReader();
        break;
      case "+":
      case "=":
        e.preventDefault();
        applyZoom(state.zoom + ZOOM_STEP);
        break;
      case "-":
      case "_":
        e.preventDefault();
        applyZoom(state.zoom - ZOOM_STEP);
        break;
      case "s":
      case "S":
        if (!e.ctrlKey && !e.metaKey) {
          e.preventDefault();
          toggleMode();
        }
        break;
      case "t":
      case "T":
        if (!e.ctrlKey && !e.metaKey) cycleTheme();
        break;
      case "c":
      case "C":
        if (!e.ctrlKey && !e.metaKey && state.doc?.format === "epub") {
          e.preventDefault();
          el.tocPanel.classList.toggle("is-hidden");
        }
        break;
      case "g":
      case "G":
        if (!e.ctrlKey && !e.metaKey) {
          e.preventDefault();
          toggleGuide();
        }
        break;
      case "m":
      case "M":
        if (!e.ctrlKey && !e.metaKey) {
          e.preventDefault();
          if (state.chromeVisible) hideChrome();
          else showChromeBriefly();
        }
        break;
      default:
        break;
    }
  });

  window.addEventListener(
    "wheel",
    (e) => {
      if (state.view !== "reader") return;
      if (e.ctrlKey || e.metaKey) {
        e.preventDefault();
        applyZoom(state.zoom + (e.deltaY < 0 ? ZOOM_STEP : -ZOOM_STEP));
      }
    },
    { passive: false }
  );
}

/** Pointer drag → scroll (touchpad/Android-style grab). */
function setupDragPan(node) {
  if (!node) return;
  node.classList.add("is-pannable");

  node.addEventListener("pointerdown", (e) => {
    if (e.button !== 0) return;
    if (e.target.closest("a, button, input, textarea, .edge-nav, .reading-guide"))
      return;
    // Only primary press; ignore when interacting with images lightbox later
    state.pan.active = true;
    state.pan.moved = false;
    state.pan.el = node;
    state.pan.x = e.clientX;
    state.pan.y = e.clientY;
    state.pan.sl = node.scrollLeft;
    state.pan.st = node.scrollTop;
    node.setPointerCapture?.(e.pointerId);
    node.classList.add("is-panning");
  });

  node.addEventListener("pointermove", (e) => {
    if (!state.pan.active || state.pan.el !== node) return;
    const dx = e.clientX - state.pan.x;
    const dy = e.clientY - state.pan.y;
    if (Math.abs(dx) + Math.abs(dy) > 3) state.pan.moved = true;
    // EPUB: vertical pan only (horizontal scroll ruins reading)
    const epubOnlyY =
      node === el.epubBook ||
      node === el.epubPages ||
      node === el.epubViewport ||
      node.closest?.("#epub-viewport");
    if (epubOnlyY || state.doc?.format === "epub") {
      node.scrollTop = state.pan.st - dy;
      node.scrollLeft = 0;
    } else {
      node.scrollLeft = state.pan.sl - dx;
      node.scrollTop = state.pan.st - dy;
    }
  });

  const endPan = (e) => {
    if (!state.pan.active || state.pan.el !== node) return;
    state.pan.active = false;
    node.classList.remove("is-panning");
    // If we panned, suppress the following click (page turn / hide chrome)
    if (state.pan.moved) {
      const block = (ev) => {
        ev.preventDefault();
        ev.stopPropagation();
        node.removeEventListener("click", block, true);
      };
      node.addEventListener("click", block, true);
    }
    state.pan.el = null;
  };
  node.addEventListener("pointerup", endPan);
  node.addEventListener("pointercancel", endPan);
}

async function boot() {
  // Paint UI immediately — no heavy work before first frame
  applyTheme(state.theme);
  applyFont(state.font);
  applyZoom(state.zoom);
  applyMode(state.mode);
  applyGuideUI();
  buildFontChips();
  bindEvents();
  updateAccountChrome();
  el.version.textContent = "Folio";

  // Account API works without native bridge (desktop WebView + Android).
  // Shelf/OPDS still need the host bridge when available.
  if (!hasWails()) {
    el.version.textContent = "Folio · browser preview";
    return;
  }
  // Snappy start: show version ASAP, load shelf without waiting on PDF engine.
  requestAnimationFrame(() => {
    // Don't block: AppVersion is sync/fast; shelf must not wait for PDFium.
    Promise.resolve()
      .then(async () => {
        try {
          const v = await Promise.race([
            api().AppVersion(),
            new Promise((_, rej) => setTimeout(() => rej(new Error("timeout")), 2500)),
          ]);
          el.version.textContent = `Folio v${v}`;
        } catch (_) {
          el.version.textContent = "Folio";
        }
      })
      .catch(() => {});

    // Shelf as soon as the native bridge answers (library is JSON, not WASM).
    setTimeout(async () => {
      try {
        await refreshShelf();
      } catch (_) {}
      setTimeout(() => refreshShelf().catch(() => {}), 600);
    }, 0);
  });
}

// Sync to server on page close (best-effort, non-blocking).
window.addEventListener("beforeunload", () => {
  if (state.doc && isLoggedIn() && state.doc.fingerprint) {
    let page = 0, chapter = 0, sub = 0, scroll = 0;
    if (state.doc.format === "epub") {
      page = state.globalPage | 0;
      chapter = state.epubChapterIndex | 0;
      sub = state.epubPage | 0;
      scroll = currentScrollRatio() || 0;
    } else {
      page = Math.max(0, state.doc.pageIndex | 0);
      scroll = state.mode === "scroll" ? currentScrollRatio() || 0 : 0;
    }
    const pos = serializePosition(page, chapter, sub, scroll);
    const device = getDeviceName();
    // Use sendBeacon for reliable delivery during page unload.
    const blob = new Blob([JSON.stringify({ fingerprint: state.doc.fingerprint, position: pos, device })], { type: "application/json" });
    navigator.sendBeacon(`${FOLIO_API_BASE}/progress`, blob);
  }
});

document.addEventListener("DOMContentLoaded", boot);
