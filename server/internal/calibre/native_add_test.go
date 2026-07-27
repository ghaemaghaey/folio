package calibre

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestNativeAdd(t *testing.T) {
	dir := t.TempDir()
	// Minimal Calibre library: empty metadata.db with required tables
	meta := filepath.Join(dir, "metadata.db")
	db, err := sql.Open("sqlite", meta)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
CREATE TABLE books (
  id INTEGER PRIMARY KEY,
  title TEXT NOT NULL DEFAULT 'Unknown',
  sort TEXT NOT NULL DEFAULT '',
  timestamp TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  pubdate TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  series_index REAL NOT NULL DEFAULT 1.0,
  author_sort TEXT NOT NULL DEFAULT '',
  path TEXT NOT NULL DEFAULT '',
  flags INTEGER NOT NULL DEFAULT 1,
  uuid TEXT,
  has_cover BOOL DEFAULT 0,
  last_modified TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE authors (id INTEGER PRIMARY KEY, name TEXT, sort TEXT, link TEXT);
CREATE TABLE books_authors_link (book INTEGER, author INTEGER, UNIQUE(book, author));
CREATE TABLE data (
  id INTEGER PRIMARY KEY,
  book INTEGER,
  format TEXT,
  uncompressed_size INTEGER,
  name TEXT
);
`)
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	src := filepath.Join(dir, "hello.epub")
	if err := os.WriteFile(src, []byte("PK fake epub"), 0o644); err != nil {
		t.Fatal(err)
	}

	id, err := NativeAdd(dir, src, "Hello World", "Test Author", "epub")
	if err != nil {
		t.Fatal(err)
	}
	if id != 1 {
		t.Fatalf("id=%d", id)
	}

	// File should exist under Author/Title (1)/
	matches, _ := filepath.Glob(filepath.Join(dir, "Test Author", "Hello World (1)", "*.epub"))
	if len(matches) != 1 {
		t.Fatalf("expected one epub, got %v", matches)
	}

	// Second book
	id2, err := NativeAdd(dir, src, "Second", "Test Author", "epub")
	if err != nil || id2 != 2 {
		t.Fatalf("id2=%d err=%v", id2, err)
	}
}
