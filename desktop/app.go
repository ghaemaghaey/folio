package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/folio-reader/folio/internal/epub"
	"github.com/folio-reader/folio/internal/library"
	"github.com/folio-reader/folio/internal/pdf"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// App is the Wails application backend.
type App struct {
	ctx      context.Context
	renderer *pdf.Renderer
	lib      *library.Store
	mu       sync.Mutex
	libOnce  sync.Once
	libReady atomic.Bool

	openDoc  *openDocument
	epubBook *epub.Book

	// OPDS / Calibre-Web catalog (lazy)
	settings  *library.SettingsStore
	opdsIndex *library.OPDSIndex
}

type openDocument struct {
	ID        string
	Path      string
	Title     string
	Format    library.Format
	PageCount int
	PageIndex int // PDF page or EPUB spine index
}

// DocumentInfo is returned when a book is opened.
type DocumentInfo struct {
	ID          string  `json:"id"`
	Path        string  `json:"path"`
	Title       string  `json:"title"`
	Format      string  `json:"format"`
	PageCount   int     `json:"pageCount"`
	PageIndex   int     `json:"pageIndex"`   // PDF page or EPUB global page
	LastChapter int     `json:"lastChapter"` // EPUB spine index
	LastSubPage int     `json:"lastSubPage"` // EPUB page within chapter
	LastScroll  float64 `json:"lastScroll"`
	Fingerprint string  `json:"fingerprint"` // SHA-256 of first 64 KiB (server sync key)
	Status      string  `json:"status"`
}

// PageImage is a rendered PDF page.
// Prefer URL (served via /__folio_pdf/…) over DataURL — large base64 breaks WebView2/Wails.
type PageImage struct {
	URL       string `json:"url,omitempty"`
	DataURL   string `json:"dataURL,omitempty"`
	PageIndex int    `json:"pageIndex"`
	PageCount int    `json:"pageCount"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
}

// EPUBChapterDTO is chapter HTML for the frontend.
type EPUBChapterDTO struct {
	Index      int    `json:"index"`
	Label      string `json:"label"`
	HTML       string `json:"html"`
	ChapterCount int  `json:"chapterCount"`
}

func NewApp() *App {
	return &App{}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	// Library JSON is cheap — load ASAP for shelf.
	go a.ensureLibrary()
	// PDFium WASM compile is the slow part. Start soon after paint (not 8s later)
	// so the engine is ready when the user opens a PDF. Emit status for the UI.
	go func() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(400 * time.Millisecond):
		}
		a.emitPDFEngine("starting", "Preparing PDF engine…")
		if err := a.ensureRenderer(); err != nil {
			runtime.LogWarningf(ctx, "pdfium warm-up: %v", err)
			a.emitPDFEngine("error", err.Error())
			return
		}
		a.emitPDFEngine("ready", "PDF engine ready")
	}()
}

func (a *App) emitPDFEngine(status, message string) {
	if a.ctx == nil {
		return
	}
	runtime.EventsEmit(a.ctx, "folio:pdf-engine", map[string]interface{}{
		"status":  status,
		"message": message,
	})
}

func (a *App) ensureLibrary() {
	a.libOnce.Do(func() {
		path, err := library.DefaultPath()
		if err != nil {
			return
		}
		store, err := library.Open(path)
		if err != nil {
			return
		}
		a.mu.Lock()
		a.lib = store
		a.mu.Unlock()
		a.libReady.Store(true)
	})
}

func (a *App) libOrNil() *library.Store {
	a.ensureLibrary()
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.lib
}

// ensureRenderer lazily starts go-pdfium (WASM). Never holds a.mu during WASM init
// (that blocked every UI call and made open/startup feel frozen).
func (a *App) ensureRenderer() error {
	a.mu.Lock()
	if a.renderer != nil {
		a.mu.Unlock()
		return nil
	}
	a.mu.Unlock()

	var lastErr error
	// First WASM compile can be flaky under antivirus; retry once.
	for attempt := 0; attempt < 2; attempt++ {
		r, err := pdf.NewRenderer()
		if err != nil {
			lastErr = err
			time.Sleep(400 * time.Millisecond)
			continue
		}
		a.mu.Lock()
		if a.renderer == nil {
			a.renderer = r
		} else {
			_ = r.Close()
		}
		a.mu.Unlock()
		return nil
	}
	return fmt.Errorf("pdfium init: %w", lastErr)
}

func (a *App) shutdown(ctx context.Context) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.renderer != nil {
		_ = a.renderer.Close()
		a.renderer = nil
	}
}

// AppVersion returns the app version string.
func (a *App) AppVersion() string {
	return "0.6.7"
}

// OpenExternalURL opens http(s)/mailto links in the OS default browser.
func (a *App) OpenExternalURL(url string) error {
	url = strings.TrimSpace(url)
	if url == "" {
		return fmt.Errorf("empty url")
	}
	lower := strings.ToLower(url)
	if !strings.HasPrefix(lower, "http://") &&
		!strings.HasPrefix(lower, "https://") &&
		!strings.HasPrefix(lower, "mailto:") {
		return fmt.Errorf("only http(s) and mailto links are allowed")
	}
	runtime.BrowserOpenURL(a.ctx, url)
	return nil
}

// GetEPUBTOC returns the chapter list for the open EPUB.
func (a *App) GetEPUBTOC() ([]epub.TOCItem, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.epubBook == nil {
		return nil, fmt.Errorf("no EPUB open")
	}
	return a.epubBook.TOC(), nil
}

// ResolveEPUBLink resolves an internal href relative to the current chapter.
func (a *App) ResolveEPUBLink(href string) (map[string]interface{}, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.epubBook == nil || a.openDoc == nil {
		return nil, fmt.Errorf("no EPUB open")
	}
	idx, frag, ok := a.epubBook.ResolveHref(a.openDoc.PageIndex, href)
	if !ok {
		return map[string]interface{}{"ok": false}, nil
	}
	return map[string]interface{}{
		"ok":       true,
		"index":    idx,
		"fragment": frag,
	}, nil
}

// PrefetchPDFPages warms the page cache without changing the current page cursor.
func (a *App) PrefetchPDFPages(pages []int, dpi int) {
	if err := a.ensureRenderer(); err != nil {
		return
	}
	a.mu.Lock()
	path := ""
	if a.openDoc != nil && a.openDoc.Format == library.FormatPDF {
		path = a.openDoc.Path
	}
	renderer := a.renderer
	a.mu.Unlock()
	if path == "" || renderer == nil {
		return
	}
	if dpi <= 0 {
		dpi = 128
	}
	for _, p := range pages {
		if p < 0 {
			continue
		}
		// Intentionally does NOT touch openDoc.PageIndex
		_, _ = renderer.RenderPage(path, p, dpi)
	}
}

// GetLibrary returns shelf items with live status.
func (a *App) GetLibrary() []library.ShelfItem {
	lib := a.libOrNil()
	if lib == nil {
		return []library.ShelfItem{}
	}
	return lib.List()
}

// OpenFileDialog opens PDF or EPUB via native dialog.
func (a *App) OpenFileDialog() (*DocumentInfo, error) {
	path, err := runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "Open book",
		Filters: []runtime.FileFilter{
			{DisplayName: "Books (PDF, EPUB)", Pattern: "*.pdf;*.epub"},
			{DisplayName: "PDF", Pattern: "*.pdf"},
			{DisplayName: "EPUB", Pattern: "*.epub"},
		},
	})
	if err != nil {
		return nil, err
	}
	if path == "" {
		return nil, nil
	}
	return a.OpenPath(path)
}

// OpenPath opens a book from disk and registers it on the shelf.
func (a *App) OpenPath(path string) (*DocumentInfo, error) {
	format := library.DetectFormat(path)
	switch format {
	case library.FormatEPUB:
		return a.openEPUB(path, "")
	default:
		return a.openPDF(path, "")
	}
}

// OpenBook opens a shelf entry by id (respects last page).
func (a *App) OpenBook(id string) (*DocumentInfo, error) {
	lib := a.libOrNil()
	if lib == nil {
		return nil, fmt.Errorf("library not ready")
	}
	book, ok := lib.Get(id)
	if !ok {
		return nil, fmt.Errorf("book not found")
	}
	items := lib.List()
	for _, it := range items {
		if it.ID == id && it.Status != library.StatusOK {
			return &DocumentInfo{
				ID:          it.ID,
				Path:        it.Path,
				Title:       it.Title,
				Format:      string(it.Format),
				Fingerprint: it.Fingerprint,
				Status:      string(it.Status),
			}, fmt.Errorf("book is %s — remap the file first", it.StatusLabel)
		}
	}
	switch book.Format {
	case library.FormatEPUB:
		return a.openEPUB(book.Path, book.ID)
	default:
		return a.openPDF(book.Path, book.ID)
	}
}

// RemapBookDialog picks a new file for a missing/replaced shelf item.
func (a *App) RemapBookDialog(id string) (*DocumentInfo, error) {
	lib := a.libOrNil()
	if lib == nil {
		return nil, fmt.Errorf("library not ready")
	}
	book, ok := lib.Get(id)
	if !ok {
		return nil, fmt.Errorf("book not found")
	}
	pattern := "*.pdf;*.epub"
	if book.Format == library.FormatPDF {
		pattern = "*.pdf"
	} else if book.Format == library.FormatEPUB {
		pattern = "*.epub"
	}
	path, err := runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "Locate file for “" + book.Title + "”",
		Filters: []runtime.FileFilter{
			{DisplayName: "Document", Pattern: pattern},
		},
	})
	if err != nil {
		return nil, err
	}
	if path == "" {
		return nil, nil
	}
	updated, err := lib.Remap(id, path)
	if err != nil {
		return nil, err
	}
	// Re-open at saved progress
	return a.OpenBook(updated.ID)
}

// RemoveFromLibrary removes a shelf entry (not the file on disk).
func (a *App) RemoveFromLibrary(id string) error {
	lib := a.libOrNil()
	if lib == nil {
		return fmt.Errorf("library not ready")
	}
	return lib.Remove(id)
}

// SaveProgress persists reading position for the currently open document.
// pageIndex: PDF page or EPUB global page. chapter/subPage used for EPUB restore.
func (a *App) SaveProgress(pageIndex, chapter, subPage int, scroll float64) error {
	a.mu.Lock()
	doc := a.openDoc
	id := ""
	if doc != nil {
		id = doc.ID
		// Only update in-memory cursor for PDF page index (not prefetch neighbors)
		if doc.Format == library.FormatPDF && pageIndex >= 0 {
			doc.PageIndex = pageIndex
		}
	}
	a.mu.Unlock()
	if id == "" {
		return nil
	}
	return a.SaveBookProgress(id, pageIndex, chapter, subPage, scroll)
}

// SaveBookProgress persists position by library id (reliable even if openDoc is racing).
func (a *App) SaveBookProgress(bookID string, pageIndex, chapter, subPage int, scroll float64) error {
	lib := a.libOrNil()
	if lib == nil || bookID == "" {
		return fmt.Errorf("library not ready")
	}
	if pageIndex < 0 {
		pageIndex = 0
	}
	if chapter < 0 {
		chapter = 0
	}
	if subPage < 0 {
		subPage = 0
	}
	return lib.UpdateProgress(bookID, pageIndex, chapter, subPage, scroll)
}

// GetProgress returns saved position for the open book (or zeros).
func (a *App) GetProgress() map[string]interface{} {
	a.mu.Lock()
	doc := a.openDoc
	id := ""
	if doc != nil {
		id = doc.ID
	}
	a.mu.Unlock()
	return a.GetBookProgress(id)
}

// GetBookProgress returns saved position for a shelf id.
func (a *App) GetBookProgress(bookID string) map[string]interface{} {
	out := map[string]interface{}{
		"page": 0, "chapter": 0, "subPage": 0, "scroll": 0.0, "id": bookID,
	}
	lib := a.libOrNil()
	if lib == nil || bookID == "" {
		return out
	}
	if b, ok := lib.Get(bookID); ok {
		out["page"] = b.LastPage
		out["chapter"] = b.LastChapter
		out["subPage"] = b.LastSubPage
		out["scroll"] = b.LastScroll
	}
	return out
}

func (a *App) openPDF(path string, existingID string) (*DocumentInfo, error) {
	path = filepath.Clean(path)
	pdf.DebugLog("openPDF path=%q id=%q", path, existingID)
	a.emitPDFEngine("opening", "Opening PDF…")
	if err := a.ensureRenderer(); err != nil {
		pdf.DebugLog("ensureRenderer: %v", err)
		return nil, fmt.Errorf("PDF engine failed to start (WASM). Try again in a few seconds: %w", err)
	}
	lib := a.libOrNil()

	// Snapshot renderer without holding a.mu during Open/Render (those are slow).
	a.mu.Lock()
	renderer := a.renderer
	a.mu.Unlock()
	if renderer == nil {
		return nil, fmt.Errorf("PDF engine is not ready — wait a moment and try again")
	}

	count, err := renderer.Open(path)
	if err != nil {
		pdf.DebugLog("Open failed: %v", err)
		// One recovery path: rebuild engine and retry (instance can die after crash).
		a.mu.Lock()
		if a.renderer != nil {
			_ = a.renderer.Close()
			a.renderer = nil
		}
		a.mu.Unlock()
		if err2 := a.ensureRenderer(); err2 != nil {
			pdf.DebugLog("retry ensureRenderer: %v", err2)
			return nil, fmt.Errorf("open PDF: %v (retry init: %v)", err, err2)
		}
		a.mu.Lock()
		renderer = a.renderer
		a.mu.Unlock()
		count, err = renderer.Open(path)
		if err != nil {
			pdf.DebugLog("Open retry failed: %v", err)
			return nil, fmt.Errorf("open PDF: %w", err)
		}
	}
	pdf.DebugLog("Open ok pages=%d path=%q", count, path)

	meta, err := library.InspectFile(path)
	if err != nil {
		return nil, err
	}

	title := titleFromPath(path)
	book := library.Book{
		ID:          existingID,
		Path:        path,
		Title:       title,
		Format:      library.FormatPDF,
		PageCount:   count,
		FileSize:    meta.Size,
		ModTimeUnix: meta.ModTimeUnix,
		Fingerprint: meta.Fingerprint,
	}

	lastPage := 0
	lastScroll := 0.0
	if lib != nil {
		if existingID != "" {
			if prev, ok := lib.Get(existingID); ok {
				lastPage = prev.LastPage
				lastScroll = prev.LastScroll
				if prev.Title != "" {
					book.Title = prev.Title
				}
			}
		} else {
			for _, it := range lib.List() {
				if strings.EqualFold(it.Path, path) {
					lastPage = it.LastPage
					lastScroll = it.LastScroll
					book.ID = it.ID
					book.Title = it.Title
					break
				}
			}
		}
		if lastPage >= count {
			lastPage = count - 1
		}
		if lastPage < 0 {
			lastPage = 0
		}
		saved, err := lib.Upsert(book)
		if err != nil {
			runtime.LogWarningf(a.ctx, "library upsert: %v", err)
		} else {
			book = saved
			lastPage = book.LastPage
			lastScroll = book.LastScroll
		}
	}

	// Cover is optional — never block open on thumbnail render.
	// Generate in background after the document is registered.
	needCover := lib != nil && book.CoverDataURL == ""
	bookID := book.ID

	if lastPage >= count {
		lastPage = count - 1
	}
	if lastPage < 0 {
		lastPage = 0
	}

	a.mu.Lock()
	a.epubBook = nil
	a.openDoc = &openDocument{
		ID:        book.ID,
		Path:      path,
		Title:     book.Title,
		Format:    library.FormatPDF,
		PageCount: count,
		PageIndex: lastPage,
	}
	a.mu.Unlock()

	if needCover && bookID != "" {
		go func(p, id string) {
			cover, _, _, err := renderer.RenderPageDataURL(p, 0, 48)
			if err != nil || cover == "" || lib == nil {
				return
			}
			b, ok := lib.Get(id)
			if !ok || b.CoverDataURL != "" {
				return
			}
			b.CoverDataURL = cover
			_, _ = lib.Upsert(b)
		}(path, bookID)
	}

	return &DocumentInfo{
		ID:          book.ID,
		Path:        path,
		Title:       book.Title,
		Format:      string(library.FormatPDF),
		PageCount:   count,
		PageIndex:   lastPage,
		LastScroll:  lastScroll,
		Fingerprint: book.Fingerprint,
		Status:      "ok",
	}, nil
}

func (a *App) openEPUB(path string, existingID string) (*DocumentInfo, error) {
	bookEPUB, err := epub.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open EPUB: %w", err)
	}

	meta, err := library.InspectFile(path)
	if err != nil {
		return nil, err
	}

	title := bookEPUB.Title
	if title == "" || title == "Untitled" {
		title = titleFromPath(path)
	}
	chapterCount := bookEPUB.ChapterCount()
	if chapterCount < 1 {
		return nil, fmt.Errorf("EPUB has no chapters")
	}

	lib := a.libOrNil()
	libBook := library.Book{
		ID:          existingID,
		Path:        path,
		Title:       title,
		Format:      library.FormatEPUB,
		PageCount:   chapterCount,
		FileSize:    meta.Size,
		ModTimeUnix: meta.ModTimeUnix,
		Fingerprint: meta.Fingerprint,
	}

	lastPage := 0
	lastChapter := 0
	lastSubPage := 0
	lastScroll := 0.0
	if lib != nil {
		if existingID != "" {
			if prev, ok := lib.Get(existingID); ok {
				lastPage = prev.LastPage
				lastChapter = prev.LastChapter
				lastSubPage = prev.LastSubPage
				lastScroll = prev.LastScroll
			}
		} else {
			for _, it := range lib.List() {
				if strings.EqualFold(it.Path, path) {
					lastPage = it.LastPage
					lastChapter = it.LastChapter
					lastSubPage = it.LastSubPage
					lastScroll = it.LastScroll
					libBook.ID = it.ID
					break
				}
			}
		}
		if lastChapter >= chapterCount {
			lastChapter = chapterCount - 1
		}
		if lastChapter < 0 {
			lastChapter = 0
		}
		if lastSubPage < 0 {
			lastSubPage = 0
		}
		if lastPage < 0 {
			lastPage = 0
		}
		if saved, err := lib.Upsert(libBook); err == nil {
			libBook = saved
			lastPage = libBook.LastPage
			lastChapter = libBook.LastChapter
			lastSubPage = libBook.LastSubPage
			lastScroll = libBook.LastScroll
		}
	}

	a.mu.Lock()
	a.epubBook = bookEPUB
	a.openDoc = &openDocument{
		ID:        libBook.ID,
		Path:      path,
		Title:     libBook.Title,
		Format:    library.FormatEPUB,
		PageCount: chapterCount,
		PageIndex: lastPage,
	}
	a.mu.Unlock()

	return &DocumentInfo{
		ID:          libBook.ID,
		Path:        path,
		Title:       libBook.Title,
		Format:      string(library.FormatEPUB),
		PageCount:   chapterCount,
		PageIndex:   lastPage,
		LastChapter: lastChapter,
		LastSubPage: lastSubPage,
		LastScroll:  lastScroll,
		Fingerprint: libBook.Fingerprint,
		Status:      "ok",
	}, nil
}

// RenderCurrentPage renders the active PDF page at dpi (zoom applied by caller via dpi).
func (a *App) RenderCurrentPage(dpi int) (*PageImage, error) {
	if dpi <= 0 {
		dpi = 128
	}
	a.mu.Lock()
	idx := 0
	if a.openDoc != nil {
		idx = a.openDoc.PageIndex
	}
	a.mu.Unlock()
	return a.renderPDFPage(idx, dpi, true)
}

// RenderPDFPage renders a PDF page and marks it as the current page.
func (a *App) RenderPDFPage(pageIndex int, dpi int) (*PageImage, error) {
	return a.renderPDFPage(pageIndex, dpi, true)
}

// PrefetchPDFPage renders a page into cache without changing the current page.
func (a *App) PrefetchPDFPage(pageIndex int, dpi int) (*PageImage, error) {
	return a.renderPDFPage(pageIndex, dpi, false)
}

func (a *App) renderPDFPage(pageIndex int, dpi int, setCurrent bool) (*PageImage, error) {
	if err := a.ensureRenderer(); err != nil {
		return nil, err
	}
	a.mu.Lock()
	if a.openDoc == nil || a.openDoc.Format != library.FormatPDF {
		a.mu.Unlock()
		return nil, fmt.Errorf("no PDF open")
	}
	if a.renderer == nil {
		a.mu.Unlock()
		return nil, fmt.Errorf("PDF engine is not ready")
	}
	if pageIndex < 0 {
		pageIndex = 0
	}
	if pageIndex >= a.openDoc.PageCount {
		pageIndex = a.openDoc.PageCount - 1
	}
	if dpi <= 0 {
		dpi = 120
	}
	path := a.openDoc.Path
	count := a.openDoc.PageCount
	if setCurrent {
		a.openDoc.PageIndex = pageIndex
	}
	renderer := a.renderer
	a.mu.Unlock()

	// Render outside app lock — returns a cache URL, not multi‑MB base64.
	pg, err := renderer.RenderPage(path, pageIndex, dpi)
	if err != nil {
		return nil, err
	}
	return &PageImage{
		URL:       pg.URL,
		DataURL:   pg.DataURL,
		PageIndex: pageIndex,
		PageCount: count,
		Width:     pg.Width,
		Height:    pg.Height,
	}, nil
}

// GoToPage jumps to a page/chapter index.
func (a *App) GoToPage(pageIndex int, dpi int) (*PageImage, error) {
	a.mu.Lock()
	if a.openDoc == nil {
		a.mu.Unlock()
		return nil, fmt.Errorf("no document open")
	}
	if pageIndex < 0 {
		pageIndex = 0
	}
	if pageIndex >= a.openDoc.PageCount {
		pageIndex = a.openDoc.PageCount - 1
	}
	a.openDoc.PageIndex = pageIndex
	format := a.openDoc.Format
	a.mu.Unlock()

	if format == library.FormatEPUB {
		return nil, nil
	}
	return a.RenderCurrentPage(dpi)
}

// NextPage advances one page.
func (a *App) NextPage(dpi int) (*PageImage, error) {
	a.mu.Lock()
	if a.openDoc == nil {
		a.mu.Unlock()
		return nil, fmt.Errorf("no document open")
	}
	if a.openDoc.PageIndex+1 < a.openDoc.PageCount {
		a.openDoc.PageIndex++
	}
	format := a.openDoc.Format
	a.mu.Unlock()
	if format == library.FormatEPUB {
		return nil, nil
	}
	return a.RenderCurrentPage(dpi)
}

// PrevPage goes back one page.
func (a *App) PrevPage(dpi int) (*PageImage, error) {
	a.mu.Lock()
	if a.openDoc == nil {
		a.mu.Unlock()
		return nil, fmt.Errorf("no document open")
	}
	if a.openDoc.PageIndex > 0 {
		a.openDoc.PageIndex--
	}
	format := a.openDoc.Format
	a.mu.Unlock()
	if format == library.FormatEPUB {
		return nil, nil
	}
	return a.RenderCurrentPage(dpi)
}

// GetEPUBChapter returns chapter HTML.
func (a *App) GetEPUBChapter(index int) (*EPUBChapterDTO, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.epubBook == nil || a.openDoc == nil || a.openDoc.Format != library.FormatEPUB {
		return nil, fmt.Errorf("no EPUB open")
	}
	if index < 0 {
		index = 0
	}
	if index >= a.openDoc.PageCount {
		index = a.openDoc.PageCount - 1
	}
	a.openDoc.PageIndex = index
	ch, err := a.epubBook.GetChapter(index)
	if err != nil {
		return nil, err
	}
	return &EPUBChapterDTO{
		Index:        ch.Index,
		Label:        ch.Label,
		HTML:         ch.HTML,
		ChapterCount: a.openDoc.PageCount,
	}, nil
}

// GetAllEPUBChapters returns every spine item's HTML for continuous full-book scroll.
// Does not hold the app mutex for the whole book (avoids UI freezes / deadlocks).
func (a *App) GetAllEPUBChapters() ([]EPUBChapterDTO, error) {
	a.mu.Lock()
	book := a.epubBook
	doc := a.openDoc
	a.mu.Unlock()

	if book == nil || doc == nil || doc.Format != library.FormatEPUB {
		return nil, fmt.Errorf("no EPUB open")
	}
	n := book.ChapterCount()
	out := make([]EPUBChapterDTO, 0, n)
	for i := 0; i < n; i++ {
		ch, err := book.GetChapter(i)
		if err != nil {
			out = append(out, EPUBChapterDTO{
				Index:        i,
				Label:        fmt.Sprintf("Chapter %d", i+1),
				HTML:         "<p></p>",
				ChapterCount: n,
			})
			continue
		}
		out = append(out, EPUBChapterDTO{
			Index:        ch.Index,
			Label:        ch.Label,
			HTML:         ch.HTML,
			ChapterCount: n,
		})
	}
	return out, nil
}

// GetEPUBChapterCount returns spine length for progressive loading.
func (a *App) GetEPUBChapterCount() (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.epubBook == nil {
		return 0, fmt.Errorf("no EPUB open")
	}
	return a.epubBook.ChapterCount(), nil
}

// GetDocument returns current document info.
func (a *App) GetDocument() *DocumentInfo {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.openDoc == nil {
		return nil
	}
	return &DocumentInfo{
		ID:        a.openDoc.ID,
		Path:      a.openDoc.Path,
		Title:     a.openDoc.Title,
		Format:    string(a.openDoc.Format),
		PageCount: a.openDoc.PageCount,
		PageIndex: a.openDoc.PageIndex,
		Status:    "ok",
	}
}

// CloseDocument clears the open document.
// Progress must already be flushed by the frontend via SaveProgress —
// do NOT overwrite library position with a stale backend page index.
func (a *App) CloseDocument() {
	a.mu.Lock()
	a.openDoc = nil
	a.epubBook = nil
	renderer := a.renderer
	a.mu.Unlock()
	if renderer != nil {
		renderer.CloseDocument()
	}
}

// UploadLocalFile streams a local book path to the Folio API (multipart).
// Emits "folio:upload-progress" events: { percent, done, message }.
// apiBase e.g. https://api.ghaemghh.ir — empty path uses currently open document.
func (a *App) UploadLocalFile(apiBase, token, filePath, title, author string) (map[string]interface{}, error) {
	apiBase = strings.TrimRight(strings.TrimSpace(apiBase), "/")
	token = strings.TrimSpace(token)
	if apiBase == "" {
		return nil, fmt.Errorf("api base is empty")
	}
	if token == "" {
		return nil, fmt.Errorf("not signed in")
	}
	path := strings.TrimSpace(filePath)
	if path == "" {
		a.mu.Lock()
		if a.openDoc != nil {
			path = a.openDoc.Path
			if title == "" {
				title = a.openDoc.Title
			}
		}
		a.mu.Unlock()
	}
	if path == "" {
		return nil, fmt.Errorf("no local book path")
	}
	st, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("file: %w", err)
	}
	if title == "" {
		title = titleFromPath(path)
	}

	emit := func(percent float64, done bool, msg string) {
		if a.ctx == nil {
			return
		}
		runtime.EventsEmit(a.ctx, "folio:upload-progress", map[string]interface{}{
			"percent": percent,
			"done":    done,
			"message": msg,
		})
	}
	emit(0, false, "Preparing…")

	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	_ = w.WriteField("title", title)
	if author != "" {
		_ = w.WriteField("author", author)
	}
	part, err := w.CreateFormFile("file", filepath.Base(path))
	if err != nil {
		return nil, err
	}
	// Copy with progress on read side
	total := st.Size()
	var written int64
	buf := make([]byte, 256*1024)
	var lastPct int64 = -1
	for {
		n, rerr := f.Read(buf)
		if n > 0 {
			if _, werr := part.Write(buf[:n]); werr != nil {
				return nil, werr
			}
			written += int64(n)
			if total > 0 {
				pct := written * 100 / total
				// Cap prep phase at 90% so server processing can fill the rest
				if pct > 90 {
					pct = 90
				}
				if pct != lastPct {
					lastPct = pct
					emit(float64(pct), false, "Uploading…")
				}
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return nil, rerr
		}
	}
	if err := w.Close(); err != nil {
		return nil, err
	}

	emit(92, false, "Sending…")
	req, err := http.NewRequest(http.MethodPost, apiBase+"/books/upload", &body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", "Folio-Desktop/0.6.7")

	client := &http.Client{Timeout: 10 * time.Minute}
	res, err := client.Do(req)
	if err != nil {
		emit(0, true, err.Error())
		return nil, err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		msg := strings.TrimSpace(string(raw))
		if msg == "" {
			msg = fmt.Sprintf("HTTP %d", res.StatusCode)
		}
		// try parse error field
		var er struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(raw, &er) == nil && er.Error != "" {
			msg = er.Error
		}
		emit(0, true, msg)
		return nil, fmt.Errorf("%s", msg)
	}
	var out map[string]interface{}
	if err := json.Unmarshal(raw, &out); err != nil {
		emit(100, true, "Done")
		return map[string]interface{}{"raw": string(raw)}, nil
	}
	emit(100, true, "Upload complete")
	return out, nil
}

func titleFromPath(path string) string {
	title := filepath.Base(path)
	if ext := filepath.Ext(title); ext != "" {
		title = title[:len(title)-len(ext)]
	}
	return title
}
