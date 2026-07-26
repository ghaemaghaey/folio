/**
 * Folio reader — distraction-free chrome, PDF cache, EPUB pages/TOC/links,
 * image lightbox, viewport reading guide.
 */

const THEMES = ["sepia", "light", "dark"];
/** Fixed render DPI — zoom is CSS-only (no re-render). */
const BASE_DPI = 144;
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
};

function hasWails() {
  return typeof window.go !== "undefined" && window.go?.main?.App;
}
function api() {
  return window.go.main.App;
}

const $ = (s) => document.querySelector(s);
const el = {
  library: $("#view-library"),
  reader: $("#view-reader"),
  libMain: document.querySelector(".lib-main"),
  shelf: $("#shelf"),
  welcome: $("#welcome-card"),
  openFile: $("#btn-open-file"),
  openFileHero: $("#btn-open-file-hero"),
  themeCycle: $("#btn-theme-cycle"),
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
  if (state.doc?.format === "epub" && state.mode === "page") {
    requestAnimationFrame(() => layoutEpubPages());
  }
}
/**
 * Zoom is visual only for PDFs (scale the existing bitmap).
 * EPUB still uses font/layout scale via --zoom-scale.
 * Never re-renders PDF or changes the current page.
 */
function applyZoom(z) {
  const next = Math.min(
    ZOOM_MAX,
    Math.max(ZOOM_MIN, Math.round(z * 100) / 100)
  );
  const prev = state.zoom;
  state.zoom = next;
  localStorage.setItem("folio.zoom", String(state.zoom));
  document.documentElement.style.setProperty("--zoom-scale", String(state.zoom));
  document.documentElement.style.setProperty("--pdf-zoom", String(state.zoom));
  el.zoomLabel.textContent = `${Math.round(state.zoom * 100)}%`;

  if (state.view !== "reader" || !state.doc) return;

  if (state.doc.format === "pdf") {
    // Keep the same point under the viewport center while scaling.
    // Never re-render and never change pageIndex.
    const stayPage = state.doc.pageIndex;
    state.zoomLockPage = true;
    const vp =
      state.mode === "scroll" ? el.scrollViewport : el.pageViewport;
    const ax = vp
      ? (vp.scrollLeft + vp.clientWidth / 2) / Math.max(1, vp.scrollWidth)
      : 0.5;
    const ay = vp
      ? (vp.scrollTop + vp.clientHeight / 2) / Math.max(1, vp.scrollHeight)
      : 0.5;

    applyPdfVisualZoom();

    if (vp && prev > 0) {
      requestAnimationFrame(() => {
        vp.scrollLeft = Math.max(0, ax * vp.scrollWidth - vp.clientWidth / 2);
        vp.scrollTop = Math.max(0, ay * vp.scrollHeight - vp.clientHeight / 2);
        if (state.doc) state.doc.pageIndex = stayPage;
        updateChromeMeta();
        syncGuideWidth();
        // release after layout settles
        setTimeout(() => {
          state.zoomLockPage = false;
        }, 120);
      });
    } else {
      if (state.doc) state.doc.pageIndex = stayPage;
      syncGuideWidth();
      state.zoomLockPage = false;
    }
    return;
  }

  // EPUB: reflow/pagination scale (not a PDF re-render)
  if (state.mode === "page") {
    requestAnimationFrame(() => layoutEpubPages());
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

// ─── Chrome: edge-only reveal, never on content click ────────

function setChrome(v) {
  state.chromeVisible = v;
  el.reader.setAttribute("data-chrome", v ? "visible" : "hidden");
  if (!v) {
    el.fontPanel?.classList.add("is-hidden");
    // keep guide panel if guide on — still hide font/toc chrome clutter
  }
}

function showChromeBriefly() {
  setChrome(true);
}

function hideChrome() {
  setChrome(false);
  el.tocPanel?.classList.add("is-hidden");
  el.fontPanel?.classList.add("is-hidden");
  // don't hide guide panel while configuring
}

// ─── Toast ───────────────────────────────────────────────────

let toastTimer;
function toast(msg, isError = false) {
  el.toast.textContent = msg;
  el.toast.hidden = false;
  el.toast.classList.toggle("is-error", isError);
  if (toastTimer) clearTimeout(toastTimer);
  toastTimer = setTimeout(() => {
    el.toast.hidden = true;
  }, 3400);
}

// ─── Progress / meta ─────────────────────────────────────────

function scheduleProgress(pageIndex, scroll = 0) {
  if (state.progressTimer) clearTimeout(state.progressTimer);
  state.progressTimer = setTimeout(async () => {
    if (!hasWails() || !state.doc) return;
    try {
      // For EPUB store chapter index; page-in-chapter via scroll
      const idx =
        state.doc.format === "epub" ? state.epubChapterIndex : pageIndex;
      await api().SaveProgress(idx, scroll);
    } catch (_) {}
  }, 450);
}

function updateChromeMeta() {
  if (!state.doc) return;
  el.readerTitle.textContent = state.doc.title || "Document";
  if (state.doc.format === "epub") {
    const ch = (state.epubChapterIndex ?? 0) + 1;
    const chTotal = state.doc.chapterCount || state.doc.pageCount || 1;
    if (state.mode === "page") {
      const p = state.epubPage + 1;
      const pt = Math.max(1, state.epubPageCount);
      el.readerMeta.textContent = `Chapter ${ch}/${chTotal} · Page ${p}/${pt}`;
      el.pageIndicator.textContent = `${p} / ${pt}`;
    } else {
      el.readerMeta.textContent = `Chapter ${ch} of ${chTotal}`;
      el.pageIndicator.textContent = `Ch ${ch}/${chTotal}`;
    }
  } else {
    const n = (state.doc.pageIndex ?? 0) + 1;
    const total = state.doc.pageCount ?? 1;
    el.readerMeta.textContent = `Page ${n} of ${total}`;
    el.pageIndicator.textContent = `${n} / ${total}`;
  }
  syncGuideWidth();
}

// ─── Views ───────────────────────────────────────────────────

function showLibrary() {
  state.view = "library";
  el.library.classList.remove("is-hidden");
  el.reader.classList.add("is-hidden");
  el.fontPanel?.classList.add("is-hidden");
  el.tocPanel?.classList.add("is-hidden");
  el.guidePanel?.classList.add("is-hidden");
  refreshShelf();
}

function showReader() {
  state.view = "reader";
  el.library.classList.add("is-hidden");
  el.reader.classList.remove("is-hidden");
  hideChrome(); // start distraction-free
  el.stage.focus({ preventScroll: true });
  applyGuideUI();
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
    el.libMain?.classList.remove("has-shelf");
    return;
  }
  el.welcome?.classList.add("is-hidden");
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
      meta.textContent = item.format === "epub" ? `Ch. ${p}` : `p. ${p}`;
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
    const doc = await api().OpenBook(id);
    if (doc) await enterDocument(doc);
  } catch (err) {
    toast(String(err?.message || err), true);
    refreshShelf();
  }
}
async function openFile() {
  if (!hasWails()) {
    toast("Run Folio via Wails to open files.", true);
    return;
  }
  try {
    const doc = await api().OpenFileDialog();
    if (doc) await enterDocument(doc);
  } catch (err) {
    toast(String(err?.message || err), true);
  }
}

async function enterDocument(doc) {
  state.doc = {
    id: doc.id,
    path: doc.path,
    title: doc.title,
    format: doc.format || "pdf",
    pageCount: doc.pageCount,
    chapterCount: doc.pageCount,
    pageIndex: doc.pageIndex || 0,
    lastScroll: doc.lastScroll || 0,
  };
  state.scrollLoaded = new Set();
  state.clientCache.clear();
  state.epubChapterIndex = doc.pageIndex || 0;
  state.epubPage = 0;
  showReader();
  setupViewports();
  el.tocBtn.classList.toggle("is-hidden", state.doc.format !== "epub");

  if (state.doc.format === "epub") {
    await loadTOC();
    await loadEpubChapter(state.epubChapterIndex, { restoreScroll: true });
  } else if (state.mode === "scroll") {
    await reloadScroll(true);
    restoreScroll(state.doc.lastScroll);
  } else {
    await renderPage();
  }
  updateChromeMeta();
  syncGuideWidth();
}

function setupViewports() {
  const isEpub = state.doc?.format === "epub";
  el.epubViewport.classList.toggle("is-hidden", !isEpub);
  el.pageViewport.classList.toggle(
    "is-hidden",
    isEpub || state.mode === "scroll"
  );
  el.scrollViewport.classList.toggle(
    "is-hidden",
    isEpub || state.mode !== "scroll"
  );
  el.epubViewport.setAttribute("data-mode", state.mode);
}

async function closeReader() {
  if (hasWails() && state.doc) {
    try {
      await api().SaveProgress(
        state.doc.format === "epub"
          ? state.epubChapterIndex
          : state.doc.pageIndex || 0,
        currentScrollRatio()
      );
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
  if (state.doc?.format === "epub" && state.mode === "scroll") {
    const v = el.epubBook;
    if (!v || v.scrollHeight <= v.clientHeight) return 0;
    return v.scrollTop / (v.scrollHeight - v.clientHeight);
  }
  if (state.mode === "scroll" && state.doc?.format === "pdf") {
    const v = el.scrollViewport;
    if (!v || v.scrollHeight <= v.clientHeight) return 0;
    return v.scrollTop / (v.scrollHeight - v.clientHeight);
  }
  if (state.doc?.format === "epub" && state.mode === "page") {
    return state.epubPageCount > 1
      ? state.epubPage / (state.epubPageCount - 1)
      : 0;
  }
  return 0;
}

function restoreScroll(ratio) {
  if (!ratio) return;
  requestAnimationFrame(() => {
    const v =
      state.doc?.format === "epub" ? el.epubBook : el.scrollViewport;
    if (!v) return;
    const max = v.scrollHeight - v.clientHeight;
    if (max > 0) v.scrollTop = max * ratio;
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
  const page = await api().RenderPDFPage(index, dpi());
  const packed = {
    dataURL: page.dataURL,
    pageIndex: page.pageIndex,
    pageCount: page.pageCount,
    width: page.width,
    height: page.height,
  };
  state.clientCache.set(key, packed);
  // bound client cache
  if (state.clientCache.size > 60) {
    const first = state.clientCache.keys().next().value;
    state.clientCache.delete(first);
  }
  if (setCurrent && state.doc) {
    state.doc.pageIndex = page.pageIndex;
    state.doc.pageCount = page.pageCount;
  }
  return packed;
}

function prefetchAround(index) {
  if (!hasWails() || !state.doc || state.doc.format !== "pdf") return;
  const pages = [index - 1, index + 1, index + 2, index - 2].filter(
    (p) => p >= 0 && p < state.doc.pageCount
  );
  // Backend cache warm
  api().PrefetchPDFPages(pages, dpi()).catch?.(() => {});
  // Also fill client cache
  for (const p of pages) {
    if (!state.clientCache.has(cacheKey(p))) {
      fetchPDFPage(p, { setCurrent: false }).catch(() => {});
    }
  }
}

async function renderPage() {
  if (!hasWails() || !state.doc || state.doc.format !== "pdf" || state.rendering)
    return;
  state.rendering = true;
  const showSpinner = !state.clientCache.has(cacheKey(state.doc.pageIndex));
  if (showSpinner) el.pageLoading.hidden = false;
  try {
    const page = await fetchPDFPage(state.doc.pageIndex, { setCurrent: true });
    if (!page) return;
    await presentPage(page);
    updateChromeMeta();
    scheduleProgress(page.pageIndex, 0);
    prefetchAround(page.pageIndex);
  } catch (err) {
    toast(String(err?.message || err), true);
  } finally {
    el.pageLoading.hidden = true;
    state.rendering = false;
  }
}

function presentPage(page) {
  return new Promise((resolve) => {
    const img = el.pageImage;
    const frame = el.pageFrame;
    const finish = () => {
      frame.classList.remove("is-turning-out");
      frame.classList.add("is-turning-in");
      setTimeout(() => frame.classList.remove("is-turning-in"), 200);
      applyPdfVisualZoom();
      syncGuideWidth();
      resolve();
    };
    const apply = () => {
      if (img.src === page.dataURL && img.complete && img.naturalWidth) {
        finish();
        return;
      }
      img.onload = () => finish();
      img.onerror = () => resolve();
      img.src = page.dataURL;
    };
    if (img.src && img.src.startsWith("data:") && img.src !== page.dataURL) {
      frame.classList.add("is-turning-out");
      setTimeout(apply, 80);
    } else apply();
  });
}

async function goRelative(dir) {
  if (!state.doc || state.rendering) return;

  if (state.doc.format === "epub") {
    if (state.mode === "page") {
      const next = state.epubPage + dir;
      if (next >= 0 && next < state.epubPageCount) {
        setEpubPage(next);
        return;
      }
      // chapter boundary
      const ch = state.epubChapterIndex + dir;
      if (ch < 0 || ch >= (state.doc.chapterCount || 0)) return;
      await loadEpubChapter(ch, { atEnd: dir < 0 });
      return;
    }
    // scroll mode: chapter jump
    const ch = state.epubChapterIndex + dir;
    if (ch < 0 || ch >= (state.doc.chapterCount || 0)) return;
    await loadEpubChapter(ch);
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
    scheduleProgress(next, currentScrollRatio());
    prefetchAround(next);
    return;
  }

  const next = state.doc.pageIndex + dir;
  if (next < 0 || next >= state.doc.pageCount) return;
  state.doc.pageIndex = next;
  await renderPage();
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
    img.src = page.dataURL;
    slot.appendChild(img);
    applyPdfVisualZoom();
  } catch (err) {
    state.scrollLoaded.delete(index);
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
  // During zoom, keep the same page — scroll geometry changes briefly
  if (state.zoomLockPage) {
    scheduleProgress(state.doc?.pageIndex || 0, currentScrollRatio());
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
  scheduleProgress(best, currentScrollRatio());
}

// ─── EPUB: chapters + page/scroll ────────────────────────────

async function loadTOC() {
  state.toc = [];
  el.tocList.innerHTML = "";
  if (!hasWails()) return;
  try {
    state.toc = (await api().GetEPUBTOC()) || [];
  } catch (_) {
    return;
  }
  for (const item of state.toc) {
    const li = document.createElement("li");
    const btn = document.createElement("button");
    btn.type = "button";
    btn.textContent = item.label || `Chapter ${item.index + 1}`;
    btn.dataset.index = String(item.index);
    if (item.index === state.epubChapterIndex) btn.classList.add("is-active");
    btn.addEventListener("click", async () => {
      el.tocPanel.classList.add("is-hidden");
      await loadEpubChapter(item.index);
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

async function loadEpubChapter(index, opts = {}) {
  if (!hasWails() || !state.doc) return;
  state.rendering = true;
  try {
    const ch = await api().GetEPUBChapter(index);
    el.epubContent.innerHTML = ch.html || "<p>(Empty chapter)</p>";
    state.epubChapterIndex = ch.index;
    state.doc.chapterCount = ch.chapterCount;
    state.doc.pageCount = ch.chapterCount;
    highlightTOC();
    wireEpubInteractions();
    if (state.mode === "page") {
      requestAnimationFrame(() => {
        layoutEpubPages();
        if (opts.atEnd) setEpubPage(Math.max(0, state.epubPageCount - 1));
        else if (opts.restoreScroll && state.doc.lastScroll) {
          setEpubPage(
            Math.round(state.doc.lastScroll * Math.max(0, state.epubPageCount - 1))
          );
        } else setEpubPage(0);
        if (opts.fragment) {
          const target = el.epubContent.querySelector(
            `#${CSS.escape(opts.fragment)}, a[name="${CSS.escape(opts.fragment)}"]`
          );
          // best-effort: stay on page 0 if can't map
          if (target) {
            // reflow page containing element is hard; scroll mode better for fragments
          }
        }
      });
    } else {
      el.epubBook.scrollTop = 0;
      if (opts.restoreScroll) restoreScroll(state.doc.lastScroll);
      if (opts.fragment) {
        requestAnimationFrame(() => {
          const target = el.epubContent.querySelector(
            `#${CSS.escape(opts.fragment)}, [name="${CSS.escape(opts.fragment)}"]`
          );
          target?.scrollIntoView({ block: "start" });
        });
      }
    }
    updateChromeMeta();
    scheduleProgress(ch.index, 0);
  } catch (err) {
    toast(String(err?.message || err), true);
  } finally {
    state.rendering = false;
  }
}

function layoutEpubPages() {
  if (!el.epubContent || state.mode !== "page") return;
  const pages = el.epubPages;
  const content = el.epubContent;
  const pageW = Math.max(1, pages.clientWidth);
  const pageH = Math.max(1, pages.clientHeight);
  content.style.transform = "translateX(0)";
  content.style.height = `${pageH}px`;
  content.style.width = `${pageW}px`;
  content.style.columnWidth = `${pageW}px`;
  content.style.columnGap = "0px";
  content.style.columnFill = "auto";
  // Force layout then measure full column strip
  void content.offsetWidth;
  const total = content.scrollWidth;
  state.epubPageCount = Math.max(1, Math.round(total / pageW) || 1);
  if (state.epubPage >= state.epubPageCount) {
    state.epubPage = state.epubPageCount - 1;
  }
  setEpubPage(state.epubPage);
  syncGuideWidth();
}

function setEpubPage(n) {
  const pages = el.epubPages;
  const content = el.epubContent;
  const pageW = Math.max(1, pages.clientWidth);
  state.epubPage = Math.max(0, Math.min(n, state.epubPageCount - 1));
  content.style.transform = `translateX(${-state.epubPage * pageW}px)`;
  content.style.transition = "transform 180ms cubic-bezier(0.22,1,0.36,1)";
  updateChromeMeta();
  scheduleProgress(state.epubChapterIndex, currentScrollRatio());
}

function wireEpubInteractions() {
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

  // Internal
  if (hasWails()) {
    try {
      const res = await api().ResolveEPUBLink(href);
      if (res && res.ok) {
        if (res.index === state.epubChapterIndex && res.fragment) {
          if (state.mode === "scroll") {
            const t = el.epubContent.querySelector(
              `#${CSS.escape(res.fragment)}, [name="${CSS.escape(res.fragment)}"]`
            );
            t?.scrollIntoView({ behavior: "smooth", block: "start" });
          } else {
            toast("Open this chapter in scroll mode to jump to anchors.");
          }
        } else {
          await loadEpubChapter(res.index, { fragment: res.fragment });
        }
        return;
      }
    } catch (_) {}
  }
  // Fragment only in current doc
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
  if (!state.guideOn) return;
  let w = null;
  if (state.doc?.format === "pdf" && state.mode === "page") {
    w = el.pageFrame?.getBoundingClientRect().width;
  } else if (state.doc?.format === "pdf" && state.mode === "scroll") {
    const slot = el.scrollViewport.querySelector(".scroll-page, .scroll-page-slot");
    w = slot?.getBoundingClientRect().width;
  } else if (state.doc?.format === "epub") {
    w = el.epubBook?.getBoundingClientRect().width;
  }
  if (w && w > 40) {
    el.guide.style.width = `${Math.min(w, window.innerWidth - 16)}px`;
  } else {
    el.guide.style.width = "";
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
  const next = state.mode === "page" ? "scroll" : "page";
  applyMode(next);
  setupViewports();
  if (!state.doc) return;
  if (state.doc.format === "pdf") {
    if (next === "scroll") await reloadScroll(true);
    else await renderPage();
  } else {
    await loadEpubChapter(state.epubChapterIndex);
  }
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

// ─── Events ──────────────────────────────────────────────────

function bindEvents() {
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
    el.fontPanel.classList.toggle("is-hidden");
    el.guidePanel.classList.add("is-hidden");
    el.tocPanel.classList.add("is-hidden");
  });
  el.tocBtn?.addEventListener("click", (e) => {
    e.stopPropagation();
    el.tocPanel.classList.toggle("is-hidden");
    el.fontPanel.classList.add("is-hidden");
    el.guidePanel.classList.add("is-hidden");
  });
  el.tocClose?.addEventListener("click", () =>
    el.tocPanel.classList.add("is-hidden")
  );
  el.guideBtn?.addEventListener("click", (e) => {
    e.stopPropagation();
    toggleGuide();
  });
  el.guideSettingsBtn?.addEventListener("click", (e) => {
    e.stopPropagation();
    el.guidePanel.classList.toggle("is-hidden");
    el.fontPanel.classList.add("is-hidden");
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

  // Edge hotspots only — do NOT show chrome on content click or general move
  el.hotspotTop?.addEventListener("mouseenter", showChromeBriefly);
  el.hotspotBottom?.addEventListener("mouseenter", showChromeBriefly);
  const chromeLeave = () => setTimeout(() => hideChrome(), 500);
  el.reader
    ?.querySelector(".reader-chrome--top")
    ?.addEventListener("mouseleave", chromeLeave);
  el.reader
    ?.querySelector(".reader-chrome--bottom")
    ?.addEventListener("mouseleave", chromeLeave);
  // Keep chrome while hovering panels attached to it
  [el.fontPanel, el.guidePanel, el.tocPanel].forEach((p) => {
    p?.addEventListener("mouseenter", showChromeBriefly);
  });

  // Content click: hide chrome, do not toggle back
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
    hideChrome();
    el.fontPanel.classList.add("is-hidden");
    el.guidePanel.classList.add("is-hidden");
  });

  // NO mousemove bumpChrome on stage

  el.epubBook?.addEventListener("scroll", () => {
    if (state.mode === "scroll") {
      scheduleProgress(state.epubChapterIndex, currentScrollRatio());
    }
  });

  window.addEventListener("resize", () => {
    if (state.doc?.format === "epub" && state.mode === "page") layoutEpubPages();
    syncGuideWidth();
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

async function boot() {
  applyTheme(state.theme);
  applyFont(state.font);
  applyZoom(state.zoom);
  applyMode(state.mode);
  applyGuideUI();
  buildFontChips();
  bindEvents();

  if (hasWails()) {
    try {
      const v = await api().AppVersion();
      el.version.textContent = `Folio v${v}`;
    } catch (_) {
      el.version.textContent = "Folio";
    }
    await refreshShelf();
  } else {
    el.version.textContent = "Folio · browser preview";
  }
}

document.addEventListener("DOMContentLoaded", boot);
