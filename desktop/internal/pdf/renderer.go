package pdf

import (
	"bytes"
	"container/list"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"image/jpeg"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/klippa-app/go-pdfium"
	"github.com/klippa-app/go-pdfium/references"
	"github.com/klippa-app/go-pdfium/requests"
	"github.com/klippa-app/go-pdfium/webassembly"
	"github.com/tetratelabs/wazero"
)

// HTTPPrefix is served by the Wails asset handler (see main.go).
// Using paths instead of multi‑MB base64 over the JS bridge is required for WebView2.
const HTTPPrefix = "/__folio_pdf/"

// Renderer wraps go-pdfium with WebAssembly + session + memory/disk page cache.
type Renderer struct {
	pool     pdfium.Pool
	instance pdfium.Pdfium
	mu       sync.Mutex

	openPath  string
	openBytes []byte
	docRef    references.FPDF_DOCUMENT
	pageCount int
	hasDoc    bool
	docHash   string

	cache    map[string]*list.Element
	cacheLRU *list.List
	cacheCap int

	diskRoot string
}

// Page is a rendered page ready for the UI.
type Page struct {
	// URL is a same-origin path like /__folio_pdf/<hash>/page-0@128.jpg
	URL string
	// DataURL is only filled for tiny images (e.g. covers) when requested.
	DataURL string
	Width   int
	Height  int
}

type cacheEntry struct {
	key    string
	url    string
	width  int
	height int
}

// NewRenderer initializes a PDFium worker pool using WebAssembly.
func NewRenderer() (*Renderer, error) {
	fs := windowsFriendlyFS()
	pool, err := webassembly.Init(webassembly.Config{
		MinIdle:      1,
		MaxIdle:      1,
		MaxTotal:     1,
		FSConfig:     fs,
		ReuseWorkers: true,
	})
	if err != nil {
		return nil, fmt.Errorf("pdfium webassembly init: %w", err)
	}

	instance, err := pool.GetInstance(90 * time.Second)
	if err != nil {
		_ = pool.Close()
		return nil, fmt.Errorf("pdfium get instance: %w", err)
	}

	diskRoot, _ := DefaultDiskRoot()

	return &Renderer{
		pool:     pool,
		instance: instance,
		cache:    make(map[string]*list.Element),
		cacheLRU: list.New(),
		cacheCap: 96,
		diskRoot: diskRoot,
	}, nil
}

// DefaultDiskRoot is ~/.folio/cache/pdf
func DefaultDiskRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".folio", "cache", "pdf")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

func windowsFriendlyFS() wazero.FSConfig {
	fs := wazero.NewFSConfig()
	if runtime.GOOS != "windows" {
		return fs.WithDirMount("/", "/")
	}
	for c := 'A'; c <= 'Z'; c++ {
		root := string(c) + `:\`
		if _, err := os.Stat(root); err != nil {
			continue
		}
		letter := strings.ToLower(string(c))
		fs = fs.WithDirMount(root, "/"+letter)
		if c == 'C' {
			fs = fs.WithDirMount(root, "/")
		}
	}
	if cwd, err := os.Getwd(); err == nil {
		vol := filepath.VolumeName(cwd)
		if vol != "" {
			fs = fs.WithDirMount(vol+`\`, "/")
		}
	}
	return fs
}

// Close releases PDFium resources.
func (r *Renderer) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closeDocLocked()
	var first error
	if r.instance != nil {
		if err := r.instance.Close(); err != nil && first == nil {
			first = err
		}
		r.instance = nil
	}
	if r.pool != nil {
		if err := r.pool.Close(); err != nil && first == nil {
			first = err
		}
		r.pool = nil
	}
	return first
}

// Open keeps a PDF loaded for fast multi-page access.
func (r *Renderer) Open(path string) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.openLocked(path)
}

func (r *Renderer) openLocked(path string) (int, error) {
	if r.instance == nil {
		return 0, fmt.Errorf("renderer closed")
	}
	path = filepath.Clean(path)
	if r.hasDoc && r.openPath == path {
		return r.pageCount, nil
	}
	r.closeDocLocked()

	pdfBytes, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("read file: %w", err)
	}
	if len(pdfBytes) < 5 {
		return 0, fmt.Errorf("file is empty or too small")
	}
	kept := make([]byte, len(pdfBytes))
	copy(kept, pdfBytes)

	doc, err := r.instance.OpenDocument(&requests.OpenDocument{
		File: &kept,
	})
	if err != nil {
		posix := toPOSIXPath(path)
		doc, err = r.instance.OpenDocument(&requests.OpenDocument{
			FilePath: &posix,
		})
		if err != nil {
			return 0, fmt.Errorf("open document: %w", err)
		}
		kept = nil
	}

	pageCount, err := r.instance.FPDF_GetPageCount(&requests.FPDF_GetPageCount{
		Document: doc.Document,
	})
	if err != nil {
		_, _ = r.instance.FPDF_CloseDocument(&requests.FPDF_CloseDocument{Document: doc.Document})
		return 0, fmt.Errorf("page count: %w", err)
	}
	if pageCount.PageCount < 1 {
		_, _ = r.instance.FPDF_CloseDocument(&requests.FPDF_CloseDocument{Document: doc.Document})
		return 0, fmt.Errorf("PDF has no pages")
	}

	r.openPath = path
	r.openBytes = kept
	r.docRef = doc.Document
	r.pageCount = pageCount.PageCount
	r.hasDoc = true
	if kept != nil {
		r.docHash = hashDoc(path, kept)
	} else {
		r.docHash = hashDoc(path, pdfBytes)
	}
	r.clearCacheLocked()
	return r.pageCount, nil
}

func toPOSIXPath(path string) string {
	path = filepath.Clean(path)
	if runtime.GOOS != "windows" {
		if filepath.IsAbs(path) {
			return filepath.ToSlash(path)
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			return filepath.ToSlash(path)
		}
		return filepath.ToSlash(abs)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	vol := filepath.VolumeName(abs)
	if vol == "" {
		return filepath.ToSlash(abs)
	}
	rest := strings.TrimPrefix(abs, vol)
	letter := strings.ToLower(strings.TrimSuffix(vol, ":"))
	return "/" + letter + filepath.ToSlash(rest)
}

// CloseDocument closes the current PDF session (keeps the engine + disk cache).
func (r *Renderer) CloseDocument() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closeDocLocked()
}

func (r *Renderer) closeDocLocked() {
	if r.hasDoc && r.instance != nil {
		_, _ = r.instance.FPDF_CloseDocument(&requests.FPDF_CloseDocument{
			Document: r.docRef,
		})
	}
	r.hasDoc = false
	r.openPath = ""
	r.openBytes = nil
	r.pageCount = 0
	r.docHash = ""
	r.clearCacheLocked()
}

func (r *Renderer) clearCacheLocked() {
	r.cache = make(map[string]*list.Element)
	r.cacheLRU = list.New()
}

// PageCount returns the number of pages (opens if needed).
func (r *Renderer) PageCount(path string) (int, error) {
	return r.Open(path)
}

// DiskRoot returns the PDF page cache directory.
func (r *Renderer) DiskRoot() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.diskRoot
}

// RenderPage renders pageIndex (0-based) at DPI and returns a cache URL
// (not a giant base64 string — WebView2/Wails choke on multi‑MB JS strings).
func (r *Renderer) RenderPage(path string, pageIndex int, dpi int) (Page, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.instance == nil {
		return Page{}, fmt.Errorf("renderer closed")
	}
	if dpi <= 0 {
		dpi = 120
	}
	if dpi > 200 {
		dpi = 200
	}

	path = filepath.Clean(path)
	if !r.hasDoc || r.openPath != path {
		if _, err := r.openLocked(path); err != nil {
			return Page{}, err
		}
	}

	if pageIndex < 0 {
		pageIndex = 0
	}
	if pageIndex >= r.pageCount {
		pageIndex = r.pageCount - 1
	}

	key := fmt.Sprintf("%d@%d", pageIndex, dpi)
	fileName := diskFileName(key)
	httpURL := HTTPPrefix + r.docHash + "/" + fileName

	// 1) Memory
	if el := r.cache[key]; el != nil {
		r.cacheLRU.MoveToFront(el)
		e := el.Value.(*cacheEntry)
		return Page{URL: e.url, Width: e.width, Height: e.height}, nil
	}

	// 2) Disk hit
	if w, h, ok := r.diskDimsLocked(key); ok {
		r.putCacheLocked(key, httpURL, w, h)
		out := Page{URL: httpURL, Width: w, Height: h}
		// Prefer loading via URL; attach dataURL only if small enough.
		if p := r.diskPath(key); p != "" {
			if b, err := os.ReadFile(p); err == nil && len(b) <= 900_000 {
				out.DataURL = "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(b)
			}
		}
		return out, nil
	}

	// 3) Render
	pageRender, err := r.instance.RenderPageInDPI(&requests.RenderPageInDPI{
		DPI: dpi,
		Page: requests.Page{
			ByIndex: &requests.PageByIndex{
				Document: r.docRef,
				Index:    pageIndex,
			},
		},
	})
	if err != nil {
		return Page{}, fmt.Errorf("render page: %w", err)
	}
	defer pageRender.Cleanup()

	img := pageRender.Result.Image
	if img == nil {
		return Page{}, fmt.Errorf("render returned empty image")
	}

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80}); err != nil {
		return Page{}, fmt.Errorf("encode jpeg: %w", err)
	}
	jpegBytes := buf.Bytes()
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()

	if err := r.saveDiskLocked(key, jpegBytes); err != nil {
		DebugLog("saveDisk failed key=%s: %v", key, err)
		// Fall back to in-memory data URL so the page still displays.
		return Page{
			DataURL: "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(jpegBytes),
			Width:   w,
			Height:  h,
		}, nil
	}

	r.putCacheLocked(key, httpURL, w, h)
	// Always include dataURL when reasonably small so WebView works even if
	// the asset middleware fails. Large pages use URL only.
	out := Page{URL: httpURL, Width: w, Height: h}
	if len(jpegBytes) <= 900_000 { // ~0.9MB raw jpeg → fine over bridge + blob
		out.DataURL = "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(jpegBytes)
	}
	return out, nil
}

// RenderPageDataURL renders and returns a data URL (for small cover thumbs only).
func (r *Renderer) RenderPageDataURL(path string, pageIndex int, dpi int) (string, int, int, error) {
	pg, err := r.RenderPage(path, pageIndex, dpi)
	if err != nil {
		return "", 0, 0, err
	}
	if pg.DataURL != "" {
		return pg.DataURL, pg.Width, pg.Height, nil
	}
	// Read from disk cache and base64 (covers are low DPI / small)
	r.mu.Lock()
	defer r.mu.Unlock()
	key := fmt.Sprintf("%d@%d", pageIndex, dpi)
	b, err := os.ReadFile(r.diskPath(key))
	if err != nil {
		return "", pg.Width, pg.Height, err
	}
	return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(b), pg.Width, pg.Height, nil
}

func diskFileName(key string) string {
	safe := ""
	for _, c := range key {
		if (c >= '0' && c <= '9') || c == '@' {
			safe += string(c)
		}
	}
	return "page-" + safe + ".jpg"
}

func (r *Renderer) diskPath(key string) string {
	if r.diskRoot == "" || r.docHash == "" {
		return ""
	}
	return filepath.Join(r.diskRoot, r.docHash, diskFileName(key))
}

func (r *Renderer) diskDimsLocked(key string) (w, h int, ok bool) {
	p := r.diskPath(key)
	if p == "" {
		return 0, 0, false
	}
	b, err := os.ReadFile(p)
	if err != nil || len(b) < 32 {
		return 0, 0, false
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return 0, 0, false
	}
	return cfg.Width, cfg.Height, true
}

func (r *Renderer) saveDiskLocked(key string, jpegBytes []byte) error {
	p := r.diskPath(key)
	if p == "" {
		return fmt.Errorf("no disk path")
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, jpegBytes, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func (r *Renderer) putCacheLocked(key, url string, w, h int) {
	if el, ok := r.cache[key]; ok {
		r.cacheLRU.MoveToFront(el)
		el.Value = &cacheEntry{key: key, url: url, width: w, height: h}
		return
	}
	el := r.cacheLRU.PushFront(&cacheEntry{key: key, url: url, width: w, height: h})
	r.cache[key] = el
	for r.cacheLRU.Len() > r.cacheCap {
		back := r.cacheLRU.Back()
		if back == nil {
			break
		}
		ent := back.Value.(*cacheEntry)
		delete(r.cache, ent.key)
		r.cacheLRU.Remove(back)
	}
}

func hashDoc(path string, data []byte) string {
	h := sha256.New()
	_, _ = h.Write([]byte(filepath.Clean(path)))
	_, _ = fmt.Fprintf(h, ":%d:", len(data))
	n := 32 * 1024
	if len(data) < n {
		_, _ = h.Write(data)
	} else {
		_, _ = h.Write(data[:n])
		_, _ = h.Write(data[len(data)-n:])
	}
	return hex.EncodeToString(h.Sum(nil))[:24]
}

// DebugLog appends a line to ~/.folio/pdf-debug.log (best-effort).
func DebugLog(format string, args ...any) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	dir := filepath.Join(home, ".folio")
	_ = os.MkdirAll(dir, 0o755)
	f, err := os.OpenFile(filepath.Join(dir, "pdf-debug.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = fmt.Fprintf(f, time.Now().Format("15:04:05.000")+" "+format+"\n", args...)
}

// ResolveCacheFile maps /__folio_pdf/<hash>/file.jpg → absolute path under disk root.
// Returns false if the path is unsafe or missing.
func ResolveCacheFile(diskRoot, urlPath string) (string, bool) {
	if diskRoot == "" {
		return "", false
	}
	if !strings.HasPrefix(urlPath, HTTPPrefix) {
		return "", false
	}
	rel := strings.TrimPrefix(urlPath, HTTPPrefix)
	rel = filepath.Clean(rel)
	if rel == "." || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return "", false
	}
	// Only allow hash/filename shape
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) != 2 {
		return "", false
	}
	full := filepath.Join(diskRoot, parts[0], parts[1])
	absRoot, err1 := filepath.Abs(diskRoot)
	absFull, err2 := filepath.Abs(full)
	if err1 != nil || err2 != nil {
		return "", false
	}
	if !strings.HasPrefix(strings.ToLower(absFull), strings.ToLower(absRoot)+string(os.PathSeparator)) &&
		!strings.EqualFold(absFull, absRoot) {
		return "", false
	}
	if st, err := os.Stat(absFull); err != nil || st.IsDir() {
		return "", false
	}
	return absFull, true
}
