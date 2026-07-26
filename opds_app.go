package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/folio-reader/folio/internal/library"
	"github.com/folio-reader/folio/internal/opds"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// OPDS settings / index — lazy, separate from PDF renderer.
var (
	settingsOnce sync.Once
	opdsIdxOnce  sync.Once
)

func (a *App) ensureSettings() *library.SettingsStore {
	settingsOnce.Do(func() {
		path, err := library.DefaultSettingsPath()
		if err != nil {
			return
		}
		st, err := library.OpenSettings(path)
		if err != nil {
			return
		}
		a.mu.Lock()
		a.settings = st
		a.mu.Unlock()
	})
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.settings
}

func (a *App) ensureOPDSIndex() *library.OPDSIndex {
	opdsIdxOnce.Do(func() {
		path, err := library.DefaultOPDSIndexPath()
		if err != nil {
			return
		}
		idx, err := library.OpenOPDSIndex(path)
		if err != nil {
			return
		}
		a.mu.Lock()
		a.opdsIndex = idx
		a.mu.Unlock()
	})
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.opdsIndex
}

func (a *App) opdsClient() (*opds.Client, error) {
	st := a.ensureSettings()
	if st == nil {
		// Settings store failed to open — still allow the built-in default.
		return opds.NewClient(library.DefaultOPDSBaseURL, "", ""), nil
	}
	cfg := st.Get()
	base := cfg.EffectiveOPDSBaseURL()
	return opds.NewClient(base, cfg.OPDSUsername, cfg.OPDSPassword), nil
}

// --- DTOs -----------------------------------------------------------------

// OPDSSettingsDTO is exposed to the frontend.
type OPDSSettingsDTO struct {
	BaseURL  string `json:"baseURL"`
	Username string `json:"username"`
	// Password is returned so the settings form can re-save; stored only locally.
	Password string `json:"password"`
	BooksDir string `json:"booksDir"`
}

// OPDSAcquisitionDTO is one download format.
type OPDSAcquisitionDTO struct {
	Href   string `json:"href"`
	Type   string `json:"type"`
	Length int64  `json:"length"`
	Format string `json:"format"` // pdf | epub | bin
}

// OPDSBookDTO is a catalog card for the UI.
type OPDSBookDTO struct {
	ID            string                `json:"id"`
	Title         string                `json:"title"`
	Authors       []string              `json:"authors"`
	Summary       string                `json:"summary"`
	CoverURL      string                `json:"coverURL"`
	Acquisitions  []OPDSAcquisitionDTO  `json:"acquisitions"`
	State         string                `json:"state"` // not_downloaded | downloaded | in_progress | read
	Progress      float64               `json:"progress"` // 0–1
	ProgressLabel string                `json:"progressLabel"`
	LocalBookID   string                `json:"localBookId"`
	LocalPath     string                `json:"localPath"`
	IsNavigation  bool                  `json:"isNavigation"`
	NavURL        string                `json:"navURL"`
}

// OPDSPageDTO is one feed page (lazy scroll).
type OPDSPageDTO struct {
	Title   string        `json:"title"`
	SelfURL string        `json:"selfURL"`
	NextURL string        `json:"nextURL"`
	Books   []OPDSBookDTO `json:"books"`
}

// OPDSDownloadResult is returned after a successful download + shelf register.
type OPDSDownloadResult struct {
	Book     OPDSBookDTO `json:"book"`
	LocalID  string      `json:"localId"`
	Path     string      `json:"path"`
	Skipped  bool        `json:"skipped"` // already had identical content
}

// --- Settings API ---------------------------------------------------------

// GetOPDSSettings returns OPDS config and books/ path.
// BaseURL falls back to the built-in default when unset.
func (a *App) GetOPDSSettings() OPDSSettingsDTO {
	st := a.ensureSettings()
	cfg := library.Settings{OPDSBaseURL: library.DefaultOPDSBaseURL}
	if st != nil {
		cfg = st.Get()
	}
	books, _ := library.BooksDir()
	return OPDSSettingsDTO{
		BaseURL:  cfg.EffectiveOPDSBaseURL(),
		Username: cfg.OPDSUsername,
		Password: cfg.OPDSPassword,
		BooksDir: books,
	}
}

// SaveOPDSSettings persists base URL (and optional Basic Auth for later).
func (a *App) SaveOPDSSettings(baseURL, username, password string) (OPDSSettingsDTO, error) {
	st := a.ensureSettings()
	if st == nil {
		return OPDSSettingsDTO{}, fmt.Errorf("settings not ready")
	}
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		baseURL = library.DefaultOPDSBaseURL
	}
	err := st.Update(library.Settings{
		OPDSBaseURL:  baseURL,
		OPDSUsername: username,
		OPDSPassword: password,
	})
	if err != nil {
		return OPDSSettingsDTO{}, err
	}
	return a.GetOPDSSettings(), nil
}

// --- Browse API -----------------------------------------------------------

// OPDSOpenLibrary loads the first page of the remote book list (newest first).
func (a *App) OPDSOpenLibrary() (*OPDSPageDTO, error) {
	client, err := a.opdsClient()
	if err != nil {
		return nil, err
	}
	feed, err := client.FetchBooksRoot()
	if err != nil {
		return nil, err
	}
	return a.feedToPage(feed), nil
}

// OPDSSearch searches the catalog. Empty query returns newest-first listing.
func (a *App) OPDSSearch(query string) (*OPDSPageDTO, error) {
	client, err := a.opdsClient()
	if err != nil {
		return nil, err
	}
	feed, err := client.Search(query)
	if err != nil {
		return nil, err
	}
	return a.feedToPage(feed), nil
}

// OPDSFetchPage loads an arbitrary feed URL (rel=next or navigation).
func (a *App) OPDSFetchPage(pageURL string) (*OPDSPageDTO, error) {
	client, err := a.opdsClient()
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(pageURL) == "" {
		return a.OPDSOpenLibrary()
	}
	feed, err := client.Fetch(pageURL)
	if err != nil {
		return nil, err
	}
	return a.feedToPage(feed), nil
}

func (a *App) feedToPage(feed *opds.Feed) *OPDSPageDTO {
	page := &OPDSPageDTO{
		Title:   feed.Title,
		SelfURL: feed.SelfURL,
		NextURL: feed.NextURL,
		Books:   make([]OPDSBookDTO, 0, len(feed.Entries)),
	}
	for _, e := range feed.Entries {
		page.Books = append(page.Books, a.enrichEntry(e))
	}
	return page
}

func (a *App) enrichEntry(e opds.Entry) OPDSBookDTO {
	dto := OPDSBookDTO{
		ID:           e.ID,
		Title:        e.Title,
		Authors:      e.Authors,
		Summary:      e.Summary,
		CoverURL:     firstNonEmpty(e.ThumbnailURL, e.CoverURL),
		IsNavigation: e.IsNavigation,
		NavURL:       e.NavURL,
		State:        "not_downloaded",
		Progress:     0,
	}
	for _, ac := range e.Acquisitions {
		dto.Acquisitions = append(dto.Acquisitions, OPDSAcquisitionDTO{
			Href:   ac.Href,
			Type:   ac.Type,
			Length: ac.Length,
			Format: ac.FormatLabel(),
		})
	}
	if e.IsNavigation || len(e.Acquisitions) == 0 {
		return dto
	}

	// Ownership + progress via OPDS index and local shelf fingerprints.
	local, ok := a.resolveLocalForOPDS(e)
	if !ok {
		return dto
	}
	dto.LocalBookID = local.ID
	dto.LocalPath = local.Path
	prog := library.ReadingProgress(local)
	dto.Progress = prog
	switch library.ReadingState(local) {
	case "read":
		dto.State = "read"
		dto.ProgressLabel = "Read"
	case "in_progress":
		dto.State = "in_progress"
		dto.ProgressLabel = fmt.Sprintf("%d%%", int(prog*100+0.5))
	default:
		dto.State = "downloaded"
		dto.ProgressLabel = "Downloaded"
	}
	return dto
}

// resolveLocalForOPDS finds a local shelf book for this remote entry.
func (a *App) resolveLocalForOPDS(e opds.Entry) (library.Book, bool) {
	lib := a.libOrNil()
	idx := a.ensureOPDSIndex()

	// 1) Explicit OPDS id mapping (from a previous download)
	if idx != nil && e.ID != "" {
		if rec, ok := idx.Get(e.ID); ok {
			if lib != nil {
				if rec.LocalBookID != "" {
					if b, ok := lib.Get(rec.LocalBookID); ok {
						if _, err := os.Stat(b.Path); err == nil {
							return b, true
						}
					}
				}
				if rec.LocalPath != "" {
					if b, ok := lib.FindByPath(rec.LocalPath); ok {
						if _, err := os.Stat(b.Path); err == nil {
							return b, true
						}
					}
				}
				if rec.Fingerprint != "" {
					if b, ok := lib.FindByFingerprint(rec.Fingerprint); ok {
						if _, err := os.Stat(b.Path); err == nil {
							return b, true
						}
					}
				}
			}
			// File on disk under books/ but not (yet) on shelf
			if rec.LocalPath != "" {
				if _, err := os.Stat(rec.LocalPath); err == nil {
					meta, err := library.InspectFile(rec.LocalPath)
					if err == nil {
						return library.Book{
							ID:          rec.LocalBookID,
							Path:        rec.LocalPath,
							Title:       firstNonEmpty(rec.Title, e.Title),
							Format:      library.DetectFormat(rec.LocalPath),
							FileSize:    meta.Size,
							Fingerprint: meta.Fingerprint,
						}, true
					}
				}
			}
		}
	}

	// 2) Soft match: same file size + title as an existing shelf entry
	if lib != nil {
		acq, ok := e.PreferredAcquisition()
		if ok && acq.Length > 0 {
			for _, item := range lib.List() {
				if item.Status != library.StatusOK {
					continue
				}
				if item.FileSize == acq.Length && titlesRoughlyEqual(item.Title, e.Title) {
					return item.Book, true
				}
			}
		}
	}
	return library.Book{}, false
}

func titlesRoughlyEqual(a, b string) bool {
	na := strings.ToLower(strings.TrimSpace(a))
	nb := strings.ToLower(strings.TrimSpace(b))
	return na != "" && na == nb
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// --- Download API ---------------------------------------------------------

// OPDSDownload downloads the preferred (or specified) format into books/ and
// registers it on the local shelf. Emits "opds:download-progress" events.
func (a *App) OPDSDownload(opdsID, title, acquisitionHref, mimeType string) (*OPDSDownloadResult, error) {
	client, err := a.opdsClient()
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(acquisitionHref) == "" {
		return nil, fmt.Errorf("no acquisition link")
	}
	booksDir, err := library.BooksDir()
	if err != nil {
		return nil, err
	}

	ext := ".bin"
	format := library.FormatPDF
	mt := strings.ToLower(mimeType)
	switch {
	case strings.Contains(mt, "epub") || strings.HasSuffix(strings.ToLower(acquisitionHref), ".epub"):
		ext = ".epub"
		format = library.FormatEPUB
	case strings.Contains(mt, "pdf") || strings.HasSuffix(strings.ToLower(acquisitionHref), ".pdf"):
		ext = ".pdf"
		format = library.FormatPDF
	}

	stem := library.SanitizeFilename(title)
	dest := filepath.Join(booksDir, stem+ext)
	dest = uniquePath(dest)

	emit := func(percent float64, done bool, msg string) {
		if a.ctx == nil {
			return
		}
		runtime.EventsEmit(a.ctx, "opds:download-progress", map[string]interface{}{
			"id":       opdsID,
			"title":    title,
			"percent":  percent,
			"done":     done,
			"message":  msg,
			"path":     dest,
		})
	}
	emit(0, false, "Starting download…")

	res, err := client.Download(acquisitionHref)
	if err != nil {
		emit(0, true, err.Error())
		return nil, err
	}
	defer res.Body.Close()

	tmp := dest + ".part"
	out, err := os.Create(tmp)
	if err != nil {
		return nil, err
	}

	var total int64
	if res.ContentLength > 0 {
		total = res.ContentLength
	}
	var written int64
	buf := make([]byte, 32*1024)
	var lastPct atomic.Int32
	for {
		n, readErr := res.Body.Read(buf)
		if n > 0 {
			if _, werr := out.Write(buf[:n]); werr != nil {
				out.Close()
				_ = os.Remove(tmp)
				return nil, werr
			}
			written += int64(n)
			if total > 0 {
				pct := int32(written * 100 / total)
				if pct != lastPct.Load() {
					lastPct.Store(pct)
					emit(float64(pct), false, fmt.Sprintf("Downloading… %d%%", pct))
				}
			} else if written%(512*1024) < 32*1024 {
				emit(0, false, fmt.Sprintf("Downloading… %d KB", written/1024))
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			out.Close()
			_ = os.Remove(tmp)
			return nil, readErr
		}
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return nil, err
	}

	// Hash temp file for duplicate detection.
	fp, err := library.FingerprintFile(tmp)
	if err != nil {
		_ = os.Remove(tmp)
		return nil, err
	}
	contentHash, err := library.ContentHash(tmp)
	if err != nil {
		_ = os.Remove(tmp)
		return nil, err
	}

	lib := a.libOrNil()
	idx := a.ensureOPDSIndex()
	skipped := false
	finalPath := dest

	// If shelf already has this fingerprint, drop temp and reuse local file.
	if lib != nil {
		if existing, ok := lib.FindByFingerprint(fp); ok {
			if _, err := os.Stat(existing.Path); err == nil {
				_ = os.Remove(tmp)
				finalPath = existing.Path
				skipped = true
				format = existing.Format
				// Map OPDS id → existing book
				if idx != nil && opdsID != "" {
					_ = idx.Put(library.OPDSRecord{
						OPDSID:      opdsID,
						Title:       title,
						LocalPath:   existing.Path,
						LocalBookID: existing.ID,
						Fingerprint: existing.Fingerprint,
						ContentHash: contentHash,
						Size:        existing.FileSize,
						Format:      string(existing.Format),
					})
				}
				emit(100, true, "Already in your shelf")
				dto := a.enrichEntry(opds.Entry{
					ID:    opdsID,
					Title: title,
					Acquisitions: []opds.Acquisition{{
						Href: acquisitionHref,
						Type: mimeType,
					}},
				})
				// Force local fields from existing
				dto.LocalBookID = existing.ID
				dto.LocalPath = existing.Path
				dto.State = "downloaded"
				if library.ReadingState(existing) == "read" {
					dto.State = "read"
				} else if library.ReadingState(existing) == "in_progress" {
					dto.State = "in_progress"
					dto.Progress = library.ReadingProgress(existing)
					dto.ProgressLabel = fmt.Sprintf("%d%%", int(dto.Progress*100+0.5))
				} else {
					dto.ProgressLabel = "Downloaded"
				}
				return &OPDSDownloadResult{
					Book:    dto,
					LocalID: existing.ID,
					Path:    existing.Path,
					Skipped: true,
				}, nil
			}
		}
	}

	if err := os.Rename(tmp, finalPath); err != nil {
		// Cross-device: copy
		if err2 := copyFile(tmp, finalPath); err2 != nil {
			_ = os.Remove(tmp)
			return nil, err
		}
		_ = os.Remove(tmp)
	}

	meta, err := library.InspectFile(finalPath)
	if err != nil {
		return nil, err
	}

	book := library.Book{
		Path:        finalPath,
		Title:       title,
		Format:      format,
		FileSize:    meta.Size,
		ModTimeUnix: meta.ModTimeUnix,
		Fingerprint: meta.Fingerprint,
	}
	if book.Title == "" {
		book.Title = stem
	}

	localID := ""
	if lib != nil {
		saved, err := lib.Upsert(book)
		if err != nil {
			return nil, err
		}
		localID = saved.ID
		book = saved
	}

	if idx != nil && opdsID != "" {
		_ = idx.Put(library.OPDSRecord{
			OPDSID:      opdsID,
			Title:       book.Title,
			LocalPath:   finalPath,
			LocalBookID: localID,
			Fingerprint: meta.Fingerprint,
			ContentHash: contentHash,
			Size:        meta.Size,
			Format:      string(format),
		})
	}

	emit(100, true, "Download complete")
	dto := OPDSBookDTO{
		ID:            opdsID,
		Title:         book.Title,
		State:         "downloaded",
		ProgressLabel: "Downloaded",
		LocalBookID:   localID,
		LocalPath:     finalPath,
		Acquisitions: []OPDSAcquisitionDTO{{
			Href:   acquisitionHref,
			Type:   mimeType,
			Format: strings.TrimPrefix(ext, "."),
		}},
	}
	return &OPDSDownloadResult{
		Book:    dto,
		LocalID: localID,
		Path:    finalPath,
		Skipped: skipped,
	}, nil
}

func uniquePath(path string) string {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return path
	}
	ext := filepath.Ext(path)
	base := strings.TrimSuffix(path, ext)
	for i := 2; i < 1000; i++ {
		cand := fmt.Sprintf("%s-%d%s", base, i, ext)
		if _, err := os.Stat(cand); os.IsNotExist(err) {
			return cand
		}
	}
	return path
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}
