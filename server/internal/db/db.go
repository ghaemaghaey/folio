package db // folio-server database layer — v0.7.3

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
	if _, err := db.Exec(schema); err != nil {
		return err
	}

	// Migration: ensure reading_positions has device column (added in v0.7.1)
	if err := migrateAddDeviceColumn(db); err != nil {
		return fmt.Errorf("migration: %w", err)
	}

	return nil
}

// migrateAddDeviceColumn checks if the reading_positions table has the device
// column and adds it if missing. SQLite doesn't support ALTER PRIMARY KEY, so
// we recreate the table with the correct schema.
func migrateAddDeviceColumn(db *sql.DB) error {
	rows, err := db.Query("PRAGMA table_info(reading_positions)")
	if err != nil {
		return err // table might not exist yet, schema will create it
	}
	defer rows.Close()

	hasDevice := false
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull int
		var dfltValue interface{}
		var pk int
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk); err != nil {
			return err
		}
		if name == "device" {
			hasDevice = true
			break
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	if hasDevice {
		return nil // already migrated
	}

	// Recreate table with device column
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Create new table with correct schema
	if _, err := tx.Exec(`
		CREATE TABLE reading_positions_new (
			user_id INTEGER REFERENCES users(id),
			book_fingerprint TEXT REFERENCES books(fingerprint),
			device TEXT NOT NULL DEFAULT '',
			position TEXT NOT NULL,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (user_id, book_fingerprint, device)
		)`); err != nil {
		return fmt.Errorf("create new table: %w", err)
	}

	// Copy existing data with empty device
	if _, err := tx.Exec(`
		INSERT INTO reading_positions_new (user_id, book_fingerprint, device, position, updated_at)
		SELECT user_id, book_fingerprint, '', position, updated_at
		FROM reading_positions`); err != nil {
		return fmt.Errorf("copy data: %w", err)
	}

	// Drop old table, rename new
	if _, err := tx.Exec("DROP TABLE reading_positions"); err != nil {
		return fmt.Errorf("drop old table: %w", err)
	}
	if _, err := tx.Exec("ALTER TABLE reading_positions_new RENAME TO reading_positions"); err != nil {
		return fmt.Errorf("rename table: %w", err)
	}

	// Recreate indexes
	if _, err := tx.Exec(`
		CREATE INDEX IF NOT EXISTS idx_positions_user ON reading_positions(user_id);
		CREATE INDEX IF NOT EXISTS idx_positions_user_fp ON reading_positions(user_id, book_fingerprint);
	`); err != nil {
		return fmt.Errorf("recreate indexes: %w", err)
	}

	return tx.Commit()
}
