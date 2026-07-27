package calibre

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// NativeAdd adds a book to a Calibre library by writing metadata.db + files.
// No calibredb/Qt required — keeps the container ~20 MB instead of ~400 MB.
//
// This is intentionally minimal (title/author/format). Prefer full calibredb
// when LIBRARY_WRITER=calibredb and the binary is available.
func NativeAdd(libraryPath, filePath, title, author, format string) (int64, error) {
	libraryPath = strings.TrimSpace(libraryPath)
	if libraryPath == "" {
		return 0, fmt.Errorf("library path is empty")
	}
	metaPath := filepath.Join(libraryPath, "metadata.db")
	if _, err := os.Stat(metaPath); err != nil {
		return 0, fmt.Errorf("calibre metadata.db not found at %s (is this a Calibre library?): %w", metaPath, err)
	}
	if title == "" {
		title, format = GuessTitleFormat(filePath)
	}
	if format == "" {
		_, format = GuessTitleFormat(filePath)
	}
	format = strings.ToUpper(strings.TrimSpace(format))
	if format == "" {
		format = "EPUB"
	}
	if author == "" {
		author = "Unknown"
	}

	src, err := os.Open(filePath)
	if err != nil {
		return 0, err
	}
	defer src.Close()
	st, err := src.Stat()
	if err != nil {
		return 0, err
	}
	size := st.Size()

	db, err := sql.Open("sqlite", metaPath+"?_pragma=busy_timeout(10000)&_pragma=foreign_keys(0)")
	if err != nil {
		return 0, err
	}
	defer db.Close()

	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	var nextID int64
	if err := tx.QueryRow(`SELECT COALESCE(MAX(id), 0) + 1 FROM books`).Scan(&nextID); err != nil {
		return 0, fmt.Errorf("next book id: %w", err)
	}

	authorSort := author
	titleSort := title
	authorDir := sanitizePathComponent(author)
	titleDir := sanitizePathComponent(title)
	// Calibre path style: Author/Title (id)
	relPath := fmt.Sprintf("%s/%s (%d)", authorDir, titleDir, nextID)
	bookDir := filepath.Join(libraryPath, filepath.FromSlash(relPath))
	if err := os.MkdirAll(bookDir, 0o755); err != nil {
		return 0, err
	}

	fileBase := sanitizePathComponent(title)
	if fileBase == "" {
		fileBase = "book"
	}
	ext := strings.ToLower(format)
	destName := fileBase + "." + ext
	destPath := filepath.Join(bookDir, destName)

	dst, err := os.Create(destPath)
	if err != nil {
		return 0, err
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		return 0, err
	}
	if err := dst.Close(); err != nil {
		return 0, err
	}

	now := time.Now().UTC().Format("2006-01-02 15:04:05.000000+00:00")
	uuid := randomUUID()

	// Insert book row — column set works across recent Calibre library versions.
	_, err = tx.Exec(`
		INSERT INTO books (
			id, title, sort, timestamp, pubdate, series_index, author_sort,
			path, flags, uuid, has_cover, last_modified
		) VALUES (?, ?, ?, ?, ?, 1.0, ?, ?, 1, ?, 0, ?)`,
		nextID, title, titleSort, now, now, authorSort, relPath, uuid, now,
	)
	if err != nil {
		// Older schemas sometimes omit flags
		_, err = tx.Exec(`
			INSERT INTO books (
				id, title, sort, timestamp, pubdate, series_index, author_sort,
				path, uuid, has_cover, last_modified
			) VALUES (?, ?, ?, ?, ?, 1.0, ?, ?, ?, 0, ?)`,
			nextID, title, titleSort, now, now, authorSort, relPath, uuid, now,
		)
	}
	if err != nil {
		return 0, fmt.Errorf("insert books: %w", err)
	}

	// Author link
	var authorID int64
	err = tx.QueryRow(`SELECT id FROM authors WHERE name = ? COLLATE NOCASE`, author).Scan(&authorID)
	if err == sql.ErrNoRows {
		if err := tx.QueryRow(`SELECT COALESCE(MAX(id), 0) + 1 FROM authors`).Scan(&authorID); err != nil {
			return 0, err
		}
		if _, err := tx.Exec(
			`INSERT INTO authors (id, name, sort, link) VALUES (?, ?, ?, '')`,
			authorID, author, authorSort,
		); err != nil {
			return 0, fmt.Errorf("insert authors: %w", err)
		}
	} else if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(
		`INSERT OR IGNORE INTO books_authors_link (book, author) VALUES (?, ?)`,
		nextID, authorID,
	); err != nil {
		return 0, fmt.Errorf("books_authors_link: %w", err)
	}

	// Format row in `data` table
	var dataID int64
	if err := tx.QueryRow(`SELECT COALESCE(MAX(id), 0) + 1 FROM data`).Scan(&dataID); err != nil {
		return 0, err
	}
	// name is basename without extension
	dataName := fileBase
	if _, err := tx.Exec(
		`INSERT INTO data (id, book, format, uncompressed_size, name) VALUES (?, ?, ?, ?, ?)`,
		dataID, nextID, format, size, dataName,
	); err != nil {
		return 0, fmt.Errorf("insert data: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return nextID, nil
}

var unsafePath = regexp.MustCompile(`[<>:"/\\|?*\x00-\x1f]+`)

func sanitizePathComponent(s string) string {
	s = strings.TrimSpace(s)
	s = unsafePath.ReplaceAllString(s, "")
	s = strings.Trim(s, " .")
	if s == "" {
		return "Unknown"
	}
	// Keep paths short for Windows/library limits
	if len(s) > 80 {
		s = s[:80]
	}
	return s
}

func randomUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	// RFC 4122 version 4
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s", h[0:8], h[8:12], h[12:16], h[16:20], h[20:32])
}
