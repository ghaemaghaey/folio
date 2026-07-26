package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

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
	Status      string  `json:"status"`
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
	// Do not block the first paint: library + PDFium load lazily on demand.
	// Kick library open in the background so the shelf is ready moments later.
	go a.ensureLibrary()
	// Warm PDFium WASM in the background so the first PDF open is much faster.
	// This is the main "first run is slow" cost (WASM compile + pool start).
	go func() {
		if err := a.ensureRenderer(); err != nil {
			runtime.LogWarningf(ctx, "pdfium warm-up: %v", err)
		}
	}()
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

	r, err := pdf.NewRenderer()
	if err != nil {
		return fmt.Errorf("pdfium init: %w", err)
	}

	a.mu.Lock()
	if a.renderer == nil {
		a.renderer = r
	} else {
		// Another goroutine won the race — drop ours
		_ = r.Close()
	}
	a.mu.Unlock()
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
	return "0.6.4"
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
		_, _, _, _ = renderer.RenderPage(path, p, dpi)
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
	if err := a.ensureRenderer(); err != nil {
		return nil, err
	}
	lib := a.libOrNil()

	// Snapshot renderer without holding a.mu during Open/Render (those are slow).
	a.mu.Lock()
	renderer := a.renderer
	a.mu.Unlock()
	if renderer == nil {
		return nil, fmt.Errorf("PDF engine is not ready")
	}

	count, err := renderer.Open(path)
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
			cover, _, _, err := renderer.RenderPage(p, 0, 48)
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

func titleFromPath(path string) string {
	title := filepath.Base(path)
	if ext := filepath.Ext(title); ext != "" {
		title = title[:len(title)-len(ext)]
	}
	return title
}
