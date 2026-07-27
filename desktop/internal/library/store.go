package library

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Status describes shelf item health relative to the stored path.
type Status string

const (
	StatusOK       Status = "ok"
	StatusMissing  Status = "missing"
	StatusReplaced Status = "replaced"
)

// Format is the document kind.
type Format string

const (
	FormatPDF  Format = "pdf"
	FormatEPUB Format = "epub"
)

// Book is a library shelf entry.
type Book struct {
	ID           string    `json:"id"`
	Path         string    `json:"path"`
	Title        string    `json:"title"`
	Format       Format    `json:"format"`
	PageCount    int     `json:"pageCount"`
	LastPage     int     `json:"lastPage"`     // PDF: page index; EPUB: global page index (0-based)
	LastChapter  int     `json:"lastChapter"`  // EPUB spine index (fast restore)
	LastSubPage  int     `json:"lastSubPage"`  // EPUB page within chapter (0-based)
	LastScroll   float64 `json:"lastScroll"`   // 0–1 scroll ratio (scroll mode)
	FileSize     int64   `json:"fileSize"`
	ModTimeUnix  int64     `json:"modTimeUnix"`
	Fingerprint  string    `json:"fingerprint"` // hash of first 64KiB
	AddedAtUnix  int64  `json:"addedAtUnix"`
	OpenedAtUnix int64  `json:"openedAtUnix"`
	CoverDataURL string `json:"coverDataURL,omitempty"`
}

// ShelfItem is a book plus live status for the UI.
type ShelfItem struct {
	Book
	Status      Status `json:"status"`
	StatusLabel string `json:"statusLabel"`
}

type storeFile struct {
	Version int    `json:"version"`
	Books   []Book `json:"books"`
}

// Store is a JSON-backed library.
type Store struct {
	path string
	mu   sync.Mutex
	data storeFile
}

// DefaultPath returns ~/.folio/library.json (or %USERPROFILE%\.folio on Windows).
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".folio")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(dir, "library.json"), nil
}

// Open loads or creates the library store.
func Open(path string) (*Store, error) {
	s := &Store{
		path: path,
		data: storeFile{Version: 1, Books: []Book{}},
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, s.Save()
		}
		return nil, err
	}
	if len(b) > 0 {
		if err := json.Unmarshal(b, &s.data); err != nil {
			return nil, fmt.Errorf("parse library: %w", err)
		}
	}
	if s.data.Books == nil {
		s.data.Books = []Book{}
	}
	return s, nil
}

// Save writes the library to disk.
func (s *Store) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked()
}

func (s *Store) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// List returns shelf items with live file status.
func (s *Store) List() []ShelfItem {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]ShelfItem, 0, len(s.data.Books))
	// Newest opened first
	books := append([]Book(nil), s.data.Books...)
	for i := 0; i < len(books); i++ {
		for j := i + 1; j < len(books); j++ {
			if books[j].OpenedAtUnix > books[i].OpenedAtUnix {
				books[i], books[j] = books[j], books[i]
			}
		}
	}
	for _, b := range books {
		st, label := probeStatus(b)
		out = append(out, ShelfItem{Book: b, Status: st, StatusLabel: label})
	}
	return out
}

// Get returns a book by id.
func (s *Store) Get(id string) (Book, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, b := range s.data.Books {
		if b.ID == id {
			return b, true
		}
	}
	return Book{}, false
}

// Upsert adds or updates a book by path (case-insensitive on Windows via equalPath).
func (s *Store) Upsert(book Book) (Book, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().Unix()
	if book.ID == "" {
		book.ID = newID()
	}
	if book.AddedAtUnix == 0 {
		book.AddedAtUnix = now
	}
	book.OpenedAtUnix = now

	for i, b := range s.data.Books {
		if b.ID == book.ID || equalPath(b.Path, book.Path) {
			// Keep existing id + timestamps
			book.ID = b.ID
			if book.AddedAtUnix == 0 {
				book.AddedAtUnix = b.AddedAtUnix
			}
			if book.CoverDataURL == "" {
				book.CoverDataURL = b.CoverDataURL
			}
			// CRITICAL: Upsert is metadata-only. Never wipe reading progress here.
			// UpdateProgress is the only path that intentionally writes position.
			book.LastPage = b.LastPage
			book.LastChapter = b.LastChapter
			book.LastSubPage = b.LastSubPage
			book.LastScroll = b.LastScroll
			s.data.Books[i] = book
			return book, s.saveLocked()
		}
	}
	s.data.Books = append(s.data.Books, book)
	return book, s.saveLocked()
}

// UpdateProgress saves reading position for a book.
func (s *Store) UpdateProgress(id string, lastPage, lastChapter, lastSubPage int, lastScroll float64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, b := range s.data.Books {
		if b.ID == id {
			s.data.Books[i].LastPage = lastPage
			s.data.Books[i].LastChapter = lastChapter
			s.data.Books[i].LastSubPage = lastSubPage
			s.data.Books[i].LastScroll = lastScroll
			s.data.Books[i].OpenedAtUnix = time.Now().Unix()
			return s.saveLocked()
		}
	}
	return fmt.Errorf("book not found")
}

// Remap changes the path of a book and refreshes fingerprint.
func (s *Store) Remap(id, newPath string) (Book, error) {
	meta, err := InspectFile(newPath)
	if err != nil {
		return Book{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, b := range s.data.Books {
		if b.ID == id {
			b.Path = newPath
			b.FileSize = meta.Size
			b.ModTimeUnix = meta.ModTimeUnix
			b.Fingerprint = meta.Fingerprint
			title := filepath.Base(newPath)
			if ext := filepath.Ext(title); ext != "" {
				title = title[:len(title)-len(ext)]
			}
			b.Title = title
			b.Format = DetectFormat(newPath)
			b.OpenedAtUnix = time.Now().Unix()
			s.data.Books[i] = b
			return b, s.saveLocked()
		}
	}
	return Book{}, fmt.Errorf("book not found")
}

// Remove deletes a shelf entry (not the file).
func (s *Store) Remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.data.Books[:0]
	for _, b := range s.data.Books {
		if b.ID != id {
			out = append(out, b)
		}
	}
	s.data.Books = out
	return s.saveLocked()
}

// FileMeta holds size, mtime, fingerprint.
type FileMeta struct {
	Size        int64
	ModTimeUnix int64
	Fingerprint string
}

// InspectFile computes metadata for a path.
func InspectFile(path string) (FileMeta, error) {
	st, err := os.Stat(path)
	if err != nil {
		return FileMeta{}, err
	}
	fp, err := fingerprintFile(path)
	if err != nil {
		return FileMeta{}, err
	}
	return FileMeta{
		Size:        st.Size(),
		ModTimeUnix: st.ModTime().Unix(),
		Fingerprint: fp,
	}, nil
}

// DetectFormat from extension.
func DetectFormat(path string) Format {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".epub":
		return FormatEPUB
	default:
		return FormatPDF
	}
}

func probeStatus(b Book) (Status, string) {
	st, err := os.Stat(b.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return StatusMissing, "Removed"
		}
		return StatusMissing, "Unavailable"
	}
	size := st.Size()
	mtime := st.ModTime().Unix()

	// Fast path: size (+ optional mtime) unchanged → skip expensive 64KiB rehash.
	// Rehash only when size changed (or we have no size baseline).
	if b.FileSize > 0 && size == b.FileSize {
		if b.ModTimeUnix == 0 || mtime == b.ModTimeUnix {
			return StatusOK, ""
		}
		// mtime drifted but size same — still treat as OK (common on cloud sync / AV).
		return StatusOK, ""
	}
	if b.FileSize > 0 && size != b.FileSize {
		// Size changed: confirm via fingerprint when we have one.
		if b.Fingerprint != "" {
			fp, err := fingerprintFile(b.Path)
			if err == nil && fp != b.Fingerprint {
				return StatusReplaced, "Replaced"
			}
			if err == nil && fp == b.Fingerprint {
				return StatusOK, ""
			}
		}
		return StatusReplaced, "Replaced"
	}
	// No baseline size stored — only fingerprint if present.
	if b.Fingerprint != "" {
		fp, err := fingerprintFile(b.Path)
		if err == nil && fp != b.Fingerprint {
			return StatusReplaced, "Replaced"
		}
	}
	return StatusOK, ""
}

func fingerprintFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	_, err = io.CopyN(h, f, 64*1024)
	if err != nil && err != io.EOF {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// FingerprintFile is the exported 64KiB SHA-256 used for shelf identity.
func FingerprintFile(path string) (string, error) {
	return fingerprintFile(path)
}

// ContentHash returns full-file SHA-256 (ownership / cross-path matching).
func ContentHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// FindByFingerprint returns the first shelf book with the given fingerprint.
func (s *Store) FindByFingerprint(fp string) (Book, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if fp == "" {
		return Book{}, false
	}
	for _, b := range s.data.Books {
		if b.Fingerprint == fp {
			return b, true
		}
	}
	return Book{}, false
}

// FindByPath returns a book whose path matches (case-insensitive on Windows).
func (s *Store) FindByPath(path string) (Book, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, b := range s.data.Books {
		if equalPath(b.Path, path) {
			return b, true
		}
	}
	return Book{}, false
}

// ReadingProgress returns 0–1 estimated progress for a book.
// Prefers scroll ratio; otherwise last page / page count.
func ReadingProgress(b Book) float64 {
	if b.LastScroll > 0.001 {
		if b.LastScroll > 1 {
			return 1
		}
		return b.LastScroll
	}
	if b.PageCount > 1 && b.LastPage > 0 {
		p := float64(b.LastPage) / float64(b.PageCount-1)
		if p < 0 {
			return 0
		}
		if p > 1 {
			return 1
		}
		return p
	}
	if b.PageCount == 1 && b.LastPage > 0 {
		return 1
	}
	return 0
}

// ReadingState classifies local progress for UI badges.
// not_started | in_progress | read
func ReadingState(b Book) string {
	p := ReadingProgress(b)
	switch {
	case p >= 0.95:
		return "read"
	case p >= 0.02:
		return "in_progress"
	default:
		return "not_started"
	}
}

func equalPath(a, b string) bool {
	a = filepath.Clean(a)
	b = filepath.Clean(b)
	return strings.EqualFold(a, b)
}

func newID() string {
	return fmt.Sprintf("%d", time.Now().UnixNano())
}
