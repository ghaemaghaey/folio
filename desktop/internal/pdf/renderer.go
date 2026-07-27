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
	docHash   string // fingerprint for disk cache folder

	// in-memory LRU
	cache    map[string]*list.Element
	cacheLRU *list.List
	cacheCap int

	// disk cache root: ~/.folio/cache/pdf/<docHash>/
	diskRoot string
}

type cacheEntry struct {
	key     string
	dataURL string
	width   int
	height  int
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

	// First compile of pdfium.wasm can take several seconds on Windows.
	instance, err := pool.GetInstance(90 * time.Second)
	if err != nil {
		_ = pool.Close()
		return nil, fmt.Errorf("pdfium get instance: %w", err)
	}

	diskRoot, _ := defaultDiskRoot()

	return &Renderer{
		pool:     pool,
		instance: instance,
		cache:    make(map[string]*list.Element),
		cacheLRU: list.New(),
		cacheCap: 96,
		diskRoot: diskRoot,
	}, nil
}

// windowsFriendlyFS mounts every existing drive letter so FilePath access works
// no matter which volume the book lives on (C:, D:, …). In-memory File: opens
// do not need this, but some PDFium ops still touch the VFS.
func windowsFriendlyFS() wazero.FSConfig {
	fs := wazero.NewFSConfig()
	if runtime.GOOS != "windows" {
		return fs.WithDirMount("/", "/")
	}
	// Mount each drive under /c, /d, … and also map C:\ → / as default root.
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
	// Fallback: at least mount the volume of the working directory.
	if cwd, err := os.Getwd(); err == nil {
		vol := filepath.VolumeName(cwd)
		if vol != "" {
			fs = fs.WithDirMount(vol+`\`, "/")
		}
	}
	return fs
}

func defaultDiskRoot() (string, error) {
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
	// Keep an independent backing array for the WASM lifetime of the document.
	kept := make([]byte, len(pdfBytes))
	copy(kept, pdfBytes)

	doc, err := r.instance.OpenDocument(&requests.OpenDocument{
		File: &kept,
	})
	if err != nil {
		// Fallback: FilePath in POSIX form (may help some PDFs / code paths).
		posix := toPOSIXPath(path)
		doc, err = r.instance.OpenDocument(&requests.OpenDocument{
			FilePath: &posix,
		})
		if err != nil {
			return 0, fmt.Errorf("open document: %w", err)
		}
		// When opening by path we still keep bytes nil; document is held by path.
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

// toPOSIXPath converts a Windows path for go-pdfium WASM VFS.
// C:\foo\bar.pdf → /c/foo/bar.pdf when drives are mounted as /c, /d, …
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

// RenderPage renders pageIndex (0-based) at DPI.
// Order: memory LRU → disk JPEG cache → PDFium render (then write disk).
func (r *Renderer) RenderPage(path string, pageIndex int, dpi int) (dataURL string, width, height int, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.instance == nil {
		return "", 0, 0, fmt.Errorf("renderer closed")
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
			return "", 0, 0, err
		}
	}

	if pageIndex < 0 {
		pageIndex = 0
	}
	if pageIndex >= r.pageCount {
		pageIndex = r.pageCount - 1
	}

	key := fmt.Sprintf("%d@%d", pageIndex, dpi)

	// 1) Memory
	if el := r.cache[key]; el != nil {
		r.cacheLRU.MoveToFront(el)
		e := el.Value.(*cacheEntry)
		return e.dataURL, e.width, e.height, nil
	}

	// 2) Disk
	if dataURL, w, h, ok := r.loadDiskLocked(key); ok {
		r.putCacheLocked(key, dataURL, w, h)
		return dataURL, w, h, nil
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
		return "", 0, 0, fmt.Errorf("render page: %w", err)
	}
	defer pageRender.Cleanup()

	img := pageRender.Result.Image
	if img == nil {
		return "", 0, 0, fmt.Errorf("render returned empty image")
	}

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 82}); err != nil {
		return "", 0, 0, fmt.Errorf("encode jpeg: %w", err)
	}
	jpegBytes := buf.Bytes()
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()

	// Persist to disk (best-effort)
	r.saveDiskLocked(key, jpegBytes)

	b64 := base64.StdEncoding.EncodeToString(jpegBytes)
	dataURL = "data:image/jpeg;base64," + b64
	r.putCacheLocked(key, dataURL, w, h)
	return dataURL, w, h, nil
}

func (r *Renderer) diskDir() string {
	if r.diskRoot == "" || r.docHash == "" {
		return ""
	}
	return filepath.Join(r.diskRoot, r.docHash)
}

func (r *Renderer) diskPath(key string) string {
	dir := r.diskDir()
	if dir == "" {
		return ""
	}
	// key is "12@144" → page-12-dpi144.jpg
	safe := ""
	for _, c := range key {
		if (c >= '0' && c <= '9') || c == '@' {
			safe += string(c)
		}
	}
	return filepath.Join(dir, "page-"+safe+".jpg")
}

func (r *Renderer) loadDiskLocked(key string) (dataURL string, w, h int, ok bool) {
	p := r.diskPath(key)
	if p == "" {
		return "", 0, 0, false
	}
	b, err := os.ReadFile(p)
	if err != nil || len(b) < 32 {
		return "", 0, 0, false
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return "", 0, 0, false
	}
	dataURL = "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(b)
	return dataURL, cfg.Width, cfg.Height, true
}

func (r *Renderer) saveDiskLocked(key string, jpegBytes []byte) {
	p := r.diskPath(key)
	if p == "" {
		return
	}
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, jpegBytes, 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, p)
}

func (r *Renderer) putCacheLocked(key, dataURL string, w, h int) {
	if el, ok := r.cache[key]; ok {
		r.cacheLRU.MoveToFront(el)
		el.Value = &cacheEntry{key: key, dataURL: dataURL, width: w, height: h}
		return
	}
	el := r.cacheLRU.PushFront(&cacheEntry{key: key, dataURL: dataURL, width: w, height: h})
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
	// path + size + first/last 32KiB — stable enough and fast
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
