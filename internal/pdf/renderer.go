package pdf

import (
	"bytes"
	"container/list"
	"encoding/base64"
	"fmt"
	"image/jpeg"
	"os"
	"sync"
	"time"

	"github.com/klippa-app/go-pdfium"
	"github.com/klippa-app/go-pdfium/references"
	"github.com/klippa-app/go-pdfium/requests"
	"github.com/klippa-app/go-pdfium/webassembly"
)

// Renderer wraps go-pdfium with WebAssembly + open-document session + page cache.
type Renderer struct {
	pool     pdfium.Pool
	instance pdfium.Pdfium
	mu       sync.Mutex

	// open session — avoid re-reading/re-parsing the PDF on every page
	openPath  string
	openBytes []byte
	docRef    references.FPDF_DOCUMENT
	pageCount int
	hasDoc    bool

	// LRU cache of rendered pages (key: "pageIndex@dpi")
	cache    map[string]*list.Element
	cacheLRU *list.List
	cacheCap int
}

type cacheEntry struct {
	key     string
	dataURL string
	width   int
	height  int
}

// NewRenderer initializes a PDFium worker pool using WebAssembly.
func NewRenderer() (*Renderer, error) {
	pool, err := webassembly.Init(webassembly.Config{
		MinIdle:  1,
		MaxIdle:  1,
		MaxTotal: 1,
	})
	if err != nil {
		return nil, fmt.Errorf("pdfium webassembly init: %w", err)
	}

	instance, err := pool.GetInstance(30 * time.Second)
	if err != nil {
		_ = pool.Close()
		return nil, fmt.Errorf("pdfium get instance: %w", err)
	}

	return &Renderer{
		pool:     pool,
		instance: instance,
		cache:    make(map[string]*list.Element),
		cacheLRU: list.New(),
		cacheCap: 48,
	}, nil
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
	if r.hasDoc && r.openPath == path {
		return r.pageCount, nil
	}
	r.closeDocLocked()

	pdfBytes, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}

	doc, err := r.instance.OpenDocument(&requests.OpenDocument{
		File: &pdfBytes,
	})
	if err != nil {
		return 0, fmt.Errorf("open document: %w", err)
	}

	pageCount, err := r.instance.FPDF_GetPageCount(&requests.FPDF_GetPageCount{
		Document: doc.Document,
	})
	if err != nil {
		_, _ = r.instance.FPDF_CloseDocument(&requests.FPDF_CloseDocument{Document: doc.Document})
		return 0, err
	}

	r.openPath = path
	r.openBytes = pdfBytes
	r.docRef = doc.Document
	r.pageCount = pageCount.PageCount
	r.hasDoc = true
	r.clearCacheLocked()
	return r.pageCount, nil
}

// CloseDocument closes the current PDF session (keeps the engine).
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

// RenderPage renders pageIndex (0-based) at DPI. Uses session + LRU cache.
func (r *Renderer) RenderPage(path string, pageIndex int, dpi int) (dataURL string, width, height int, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.instance == nil {
		return "", 0, 0, fmt.Errorf("renderer closed")
	}
	if dpi <= 0 {
		dpi = 120
	}
	if dpi > 220 {
		dpi = 220
	}

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
	if el := r.cache[key]; el != nil {
		r.cacheLRU.MoveToFront(el)
		e := el.Value.(*cacheEntry)
		return e.dataURL, e.width, e.height, nil
	}

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
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 88}); err != nil {
		return "", 0, 0, fmt.Errorf("encode jpeg: %w", err)
	}

	b64 := base64.StdEncoding.EncodeToString(buf.Bytes())
	dataURL = "data:image/jpeg;base64," + b64
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()

	r.putCacheLocked(key, dataURL, w, h)
	return dataURL, w, h, nil
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
