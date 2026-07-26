package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

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

	openDoc  *openDocument
	epubBook *epub.Book
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
	ID         string  `json:"id"`
	Path       string  `json:"path"`
	Title      string  `json:"title"`
	Format     string  `json:"format"`
	PageCount  int     `json:"pageCount"`
	PageIndex  int     `json:"pageIndex"`
	LastScroll float64 `json:"lastScroll"`
	Status     string  `json:"status"`
}

// PageImage is a rendered PDF page.
type PageImage struct {
	DataURL   string `json:"dataURL"`
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

	// Library JSON is cheap — load now so the shelf is ready.
	// PDFium WASM is heavy: lazy-init on first PDF open so the window appears fast.
	path, err := library.DefaultPath()
	if err != nil {
		runtime.LogErrorf(ctx, "library path: %v", err)
	} else {
		store, err := library.Open(path)
		if err != nil {
			runtime.LogErrorf(ctx, "library open: %v", err)
		} else {
			a.lib = store
		}
	}
}

// ensureRenderer lazily starts go-pdfium (WASM). Safe to call often.
func (a *App) ensureRenderer() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.renderer != nil {
		return nil
	}
	r, err := pdf.NewRenderer()
	if err != nil {
		return fmt.Errorf("pdfium init: %w", err)
	}
	a.renderer = r
	return nil
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
	return "0.4.0"
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

// PrefetchPDFPages warms the page cache (non-blocking from UI perspective when awaited in parallel).
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
		dpi = 120
	}
	for _, p := range pages {
		if p < 0 {
			continue
		}
		_, _, _, _ = renderer.RenderPage(path, p, dpi)
	}
}

// GetLibrary returns shelf items with live status.
func (a *App) GetLibrary() []library.ShelfItem {
	if a.lib == nil {
		return []library.ShelfItem{}
	}
	return a.lib.List()
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
	if a.lib == nil {
		return nil, fmt.Errorf("library not ready")
	}
	book, ok := a.lib.Get(id)
	if !ok {
		return nil, fmt.Errorf("book not found")
	}
	items := a.lib.List()
	for _, it := range items {
		if it.ID == id && it.Status != library.StatusOK {
			return &DocumentInfo{
				ID:     it.ID,
				Path:   it.Path,
				Title:  it.Title,
				Format: string(it.Format),
				Status: string(it.Status),
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
	if a.lib == nil {
		return nil, fmt.Errorf("library not ready")
	}
	book, ok := a.lib.Get(id)
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
	updated, err := a.lib.Remap(id, path)
	if err != nil {
		return nil, err
	}
	// Re-open at saved progress
	return a.OpenBook(updated.ID)
}

// RemoveFromLibrary removes a shelf entry (not the file on disk).
func (a *App) RemoveFromLibrary(id string) error {
	if a.lib == nil {
		return fmt.Errorf("library not ready")
	}
	return a.lib.Remove(id)
}

// SaveProgress persists last page / scroll position.
func (a *App) SaveProgress(pageIndex int, scroll float64) error {
	a.mu.Lock()
	doc := a.openDoc
	a.mu.Unlock()
	if doc == nil || doc.ID == "" || a.lib == nil {
		return nil
	}
	if pageIndex < 0 {
		pageIndex = 0
	}
	return a.lib.UpdateProgress(doc.ID, pageIndex, scroll)
}

func (a *App) openPDF(path string, existingID string) (*DocumentInfo, error) {
	if err := a.ensureRenderer(); err != nil {
		return nil, err
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	if a.renderer == nil {
		return nil, fmt.Errorf("PDF engine is not ready")
	}

	count, err := a.renderer.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open PDF: %w", err)
	}

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

	// Preserve last page when reopening known book
	lastPage := 0
	lastScroll := 0.0
	if a.lib != nil {
		if existingID != "" {
			if prev, ok := a.lib.Get(existingID); ok {
				lastPage = prev.LastPage
				lastScroll = prev.LastScroll
				if prev.Title != "" {
					book.Title = prev.Title
				}
			}
		} else {
			// match by path in list
			for _, it := range a.lib.List() {
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
		book.LastPage = lastPage
		book.LastScroll = lastScroll
		saved, err := a.lib.Upsert(book)
		if err != nil {
			runtime.LogWarningf(a.ctx, "library upsert: %v", err)
		} else {
			book = saved
		}
	}

	// Thumbnail cover from page 0 (best-effort)
	if a.lib != nil && book.CoverDataURL == "" {
		if cover, _, _, err := a.renderer.RenderPage(path, 0, 48); err == nil {
			book.CoverDataURL = cover
			book.LastPage = lastPage
			if saved, err := a.lib.Upsert(book); err == nil {
				book = saved
			}
		}
	}

	a.epubBook = nil
	a.openDoc = &openDocument{
		ID:        book.ID,
		Path:      path,
		Title:     book.Title,
		Format:    library.FormatPDF,
		PageCount: count,
		PageIndex: lastPage,
	}

	return &DocumentInfo{
		ID:         book.ID,
		Path:       path,
		Title:      book.Title,
		Format:     string(library.FormatPDF),
		PageCount:  count,
		PageIndex:  lastPage,
		LastScroll: lastScroll,
		Status:     "ok",
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
	count := bookEPUB.ChapterCount()
	if count < 1 {
		return nil, fmt.Errorf("EPUB has no chapters")
	}

	libBook := library.Book{
		ID:          existingID,
		Path:        path,
		Title:       title,
		Format:      library.FormatEPUB,
		PageCount:   count,
		FileSize:    meta.Size,
		ModTimeUnix: meta.ModTimeUnix,
		Fingerprint: meta.Fingerprint,
	}

	lastPage := 0
	lastScroll := 0.0
	if a.lib != nil {
		if existingID != "" {
			if prev, ok := a.lib.Get(existingID); ok {
				lastPage = prev.LastPage
				lastScroll = prev.LastScroll
			}
		} else {
			for _, it := range a.lib.List() {
				if strings.EqualFold(it.Path, path) {
					lastPage = it.LastPage
					lastScroll = it.LastScroll
					libBook.ID = it.ID
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
		libBook.LastPage = lastPage
		libBook.LastScroll = lastScroll
		if saved, err := a.lib.Upsert(libBook); err == nil {
			libBook = saved
		}
	}

	a.mu.Lock()
	a.epubBook = bookEPUB
	a.openDoc = &openDocument{
		ID:        libBook.ID,
		Path:      path,
		Title:     libBook.Title,
		Format:    library.FormatEPUB,
		PageCount: count,
		PageIndex: lastPage,
	}
	a.mu.Unlock()

	return &DocumentInfo{
		ID:         libBook.ID,
		Path:       path,
		Title:      libBook.Title,
		Format:     string(library.FormatEPUB),
		PageCount:  count,
		PageIndex:  lastPage,
		LastScroll: lastScroll,
		Status:     "ok",
	}, nil
}

// RenderCurrentPage renders the active PDF page at dpi (zoom applied by caller via dpi).
func (a *App) RenderCurrentPage(dpi int) (*PageImage, error) {
	if err := a.ensureRenderer(); err != nil {
		return nil, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.openDoc == nil || a.openDoc.Format != library.FormatPDF {
		return nil, fmt.Errorf("no PDF open")
	}
	if a.renderer == nil {
		return nil, fmt.Errorf("PDF engine is not ready")
	}
	if dpi <= 0 {
		dpi = 144
	}

	img, w, h, err := a.renderer.RenderPage(a.openDoc.Path, a.openDoc.PageIndex, dpi)
	if err != nil {
		return nil, err
	}
	return &PageImage{
		DataURL:   img,
		PageIndex: a.openDoc.PageIndex,
		PageCount: a.openDoc.PageCount,
		Width:     w,
		Height:    h,
	}, nil
}

// RenderPDFPage renders an arbitrary PDF page (for scroll mode).
// setCurrent: when false, does not move the saved reading position (prefetch).
func (a *App) RenderPDFPage(pageIndex int, dpi int) (*PageImage, error) {
	return a.renderPDFPage(pageIndex, dpi, true)
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

	// Render outside app lock so prefetch/UI don't stall each other as hard
	img, w, h, err := renderer.RenderPage(path, pageIndex, dpi)
	if err != nil {
		return nil, err
	}
	return &PageImage{
		DataURL:   img,
		PageIndex: pageIndex,
		PageCount: count,
		Width:     w,
		Height:    h,
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

// CloseDocument clears the open document and saves progress.
func (a *App) CloseDocument() {
	a.mu.Lock()
	doc := a.openDoc
	a.openDoc = nil
	a.epubBook = nil
	renderer := a.renderer
	a.mu.Unlock()
	if renderer != nil {
		renderer.CloseDocument()
	}
	if doc != nil && a.lib != nil && doc.ID != "" {
		_ = a.lib.UpdateProgress(doc.ID, doc.PageIndex, 0)
	}
}

func titleFromPath(path string) string {
	title := filepath.Base(path)
	if ext := filepath.Ext(title); ext != "" {
		title = title[:len(title)-len(ext)]
	}
	return title
}
