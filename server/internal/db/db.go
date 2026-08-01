package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// Open creates/opens the SQLite database and runs migrations.
func Open(path string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create db dir: %w", err)
	}
	// WAL + busy timeout for concurrent readers/writers under modest load.
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // SQLite writer safety for simple single-file store
	db.SetConnMaxLifetime(time.Hour)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func migrate(db *sql.DB) error {
	const schema = `
CREATE TABLE IF NOT EXISTS users (
    id INTEGER PRIMARY KEY,
    username TEXT UNIQUE NOT NULL,
    password_hash TEXT NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS books (
    fingerprint TEXT PRIMARY KEY,
    calibre_book_id INTEGER,
    title TEXT,
    author TEXT,
    format TEXT,
    uploaded_by INTEGER REFERENCES users(id),
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS reading_positions (
    user_id INTEGER REFERENCES users(id),
    book_fingerprint TEXT REFERENCES books(fingerprint),
    device TEXT NOT NULL DEFAULT '',
    position TEXT NOT NULL,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (user_id, book_fingerprint, device)
);

CREATE INDEX IF NOT EXISTS idx_books_uploaded_by ON books(uploaded_by);
CREATE INDEX IF NOT EXISTS idx_positions_user ON reading_positions(user_id);
CREATE INDEX IF NOT EXISTS idx_positions_user_fp ON reading_positions(user_id, book_fingerprint);
`
	_, err := db.Exec(schema)
	return err
}
