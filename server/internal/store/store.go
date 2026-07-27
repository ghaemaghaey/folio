package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Book is cloud/library metadata (not the file bytes).
type Book struct {
	ID          string  `json:"id"`
	Title       string  `json:"title"`
	Format      string  `json:"format"` // pdf | epub
	Fingerprint string  `json:"fingerprint,omitempty"`
	PageCount   int     `json:"pageCount,omitempty"`
	UpdatedAt   int64   `json:"updatedAt"`
}

// Progress is multi-device reading position.
type Progress struct {
	BookID      string  `json:"bookId"`
	Page        int     `json:"page"`
	Chapter     int     `json:"chapter"`
	SubPage     int     `json:"subPage"`
	Scroll      float64 `json:"scroll"`
	Device      string  `json:"device,omitempty"`
	UpdatedAt   int64   `json:"updatedAt"`
}

type fileData struct {
	Version  int                 `json:"version"`
	Books    map[string]Book     `json:"books"`
	Progress map[string]Progress `json:"progress"`
}

// Store is a simple JSON-backed persistence layer.
type Store struct {
	path string
	mu   sync.Mutex
	data fileData
}

// Open loads or creates the store under dir/store.json.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "store.json")
	s := &Store{
		path: path,
		data: fileData{
			Version:  1,
			Books:    map[string]Book{},
			Progress: map[string]Progress{},
		},
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, s.saveLocked()
		}
		return nil, err
	}
	if len(b) > 0 {
		if err := json.Unmarshal(b, &s.data); err != nil {
			return nil, fmt.Errorf("parse store: %w", err)
		}
	}
	if s.data.Books == nil {
		s.data.Books = map[string]Book{}
	}
	if s.data.Progress == nil {
		s.data.Progress = map[string]Progress{}
	}
	return s, nil
}

func (s *Store) saveLocked() error {
	tmp := s.path + ".tmp"
	b, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *Store) ListBooks() []Book {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Book, 0, len(s.data.Books))
	for _, b := range s.data.Books {
		out = append(out, b)
	}
	return out
}

func (s *Store) GetBook(id string) (Book, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.data.Books[id]
	return b, ok
}

func (s *Store) UpsertBook(b Book) (Book, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b.UpdatedAt = time.Now().Unix()
	s.data.Books[b.ID] = b
	if err := s.saveLocked(); err != nil {
		return Book{}, err
	}
	return b, nil
}

func (s *Store) DeleteBook(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data.Books[id]; !ok {
		return fmt.Errorf("book not found")
	}
	delete(s.data.Books, id)
	delete(s.data.Progress, id)
	return s.saveLocked()
}

func (s *Store) GetProgress(bookID string) (Progress, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.data.Progress[bookID]
	return p, ok
}

func (s *Store) SetProgress(p Progress) (Progress, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p.UpdatedAt = time.Now().Unix()
	s.data.Progress[p.BookID] = p
	if err := s.saveLocked(); err != nil {
		return Progress{}, err
	}
	return p, nil
}
