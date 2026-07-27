package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ghaemaghaey/folio/server/internal/models"
)

var (
	ErrNotFound      = errors.New("not found")
	ErrConflict      = errors.New("conflict")
	ErrInvalidInput  = errors.New("invalid input")
)

// Store wraps SQL operations for users, books, and positions.
type Store struct {
	db *sql.DB
}

func New(db *sql.DB) *Store {
	return &Store{db: db}
}

// --- Users ---

func (s *Store) CreateUser(username, passwordHash string) (models.User, error) {
	username = strings.TrimSpace(username)
	if username == "" || passwordHash == "" {
		return models.User{}, ErrInvalidInput
	}
	res, err := s.db.Exec(
		`INSERT INTO users (username, password_hash) VALUES (?, ?)`,
		username, passwordHash,
	)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return models.User{}, ErrConflict
		}
		return models.User{}, err
	}
	id, _ := res.LastInsertId()
	return models.User{ID: id, Username: username}, nil
}

func (s *Store) GetUserByUsername(username string) (models.User, error) {
	var u models.User
	err := s.db.QueryRow(
		`SELECT id, username, password_hash, COALESCE(created_at,'') FROM users WHERE username = ?`,
		username,
	).Scan(&u.ID, &u.Username, &u.PasswordHash, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return models.User{}, ErrNotFound
	}
	return u, err
}

// --- Books ---

func (s *Store) GetBook(fingerprint string) (models.Book, error) {
	var b models.Book
	var calibreID sql.NullInt64
	err := s.db.QueryRow(
		`SELECT fingerprint, calibre_book_id, COALESCE(title,''), COALESCE(author,''),
		        COALESCE(format,''), COALESCE(uploaded_by,0), COALESCE(created_at,'')
		 FROM books WHERE fingerprint = ?`,
		fingerprint,
	).Scan(&b.Fingerprint, &calibreID, &b.Title, &b.Author, &b.Format, &b.UploadedBy, &b.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return models.Book{}, ErrNotFound
	}
	if err != nil {
		return models.Book{}, err
	}
	if calibreID.Valid {
		v := calibreID.Int64
		b.CalibreBookID = &v
	}
	return b, nil
}

func (s *Store) ListBooks() ([]models.Book, error) {
	rows, err := s.db.Query(
		`SELECT fingerprint, calibre_book_id, COALESCE(title,''), COALESCE(author,''),
		        COALESCE(format,''), COALESCE(uploaded_by,0), COALESCE(created_at,'')
		 FROM books ORDER BY created_at DESC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Book
	for rows.Next() {
		var b models.Book
		var calibreID sql.NullInt64
		if err := rows.Scan(&b.Fingerprint, &calibreID, &b.Title, &b.Author, &b.Format, &b.UploadedBy, &b.CreatedAt); err != nil {
			return nil, err
		}
		if calibreID.Valid {
			v := calibreID.Int64
			b.CalibreBookID = &v
		}
		out = append(out, b)
	}
	if out == nil {
		out = []models.Book{}
	}
	return out, rows.Err()
}

func (s *Store) InsertBook(b models.Book) (models.Book, error) {
	if b.Fingerprint == "" {
		return models.Book{}, ErrInvalidInput
	}
	var calibre any
	if b.CalibreBookID != nil {
		calibre = *b.CalibreBookID
	}
	_, err := s.db.Exec(
		`INSERT INTO books (fingerprint, calibre_book_id, title, author, format, uploaded_by)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		b.Fingerprint, calibre, b.Title, b.Author, b.Format, b.UploadedBy,
	)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return s.GetBook(b.Fingerprint)
		}
		return models.Book{}, err
	}
	return s.GetBook(b.Fingerprint)
}

// --- Reading positions ---

func (s *Store) UpsertProgress(userID int64, fingerprint, position string) (models.ReadingPosition, error) {
	fingerprint = strings.TrimSpace(fingerprint)
	position = strings.TrimSpace(position)
	if userID == 0 || fingerprint == "" || position == "" {
		return models.ReadingPosition{}, ErrInvalidInput
	}
	// Ensure book row exists so FK is satisfied — progress can be recorded for
	// OPDS-known fingerprints even before local upload by inserting a stub.
	_, err := s.GetBook(fingerprint)
	if errors.Is(err, ErrNotFound) {
		_, err = s.InsertBook(models.Book{
			Fingerprint: fingerprint,
			Title:       fingerprint[:min(16, len(fingerprint))],
			Format:      "unknown",
			UploadedBy:  userID,
		})
		if err != nil {
			return models.ReadingPosition{}, err
		}
	} else if err != nil {
		return models.ReadingPosition{}, err
	}

	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	_, err = s.db.Exec(
		`INSERT INTO reading_positions (user_id, book_fingerprint, position, updated_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(user_id, book_fingerprint) DO UPDATE SET
		   position = excluded.position,
		   updated_at = excluded.updated_at`,
		userID, fingerprint, position, now,
	)
	if err != nil {
		return models.ReadingPosition{}, err
	}
	return s.GetProgress(userID, fingerprint)
}

func (s *Store) GetProgress(userID int64, fingerprint string) (models.ReadingPosition, error) {
	var p models.ReadingPosition
	err := s.db.QueryRow(
		`SELECT user_id, book_fingerprint, position, COALESCE(updated_at,'')
		 FROM reading_positions WHERE user_id = ? AND book_fingerprint = ?`,
		userID, fingerprint,
	).Scan(&p.UserID, &p.BookFingerprint, &p.Position, &p.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return models.ReadingPosition{}, ErrNotFound
	}
	return p, err
}

func (s *Store) ListProgress(userID int64) ([]models.ReadingPosition, error) {
	rows, err := s.db.Query(
		`SELECT user_id, book_fingerprint, position, COALESCE(updated_at,'')
		 FROM reading_positions WHERE user_id = ? ORDER BY updated_at DESC`,
		userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.ReadingPosition
	for rows.Next() {
		var p models.ReadingPosition
		if err := rows.Scan(&p.UserID, &p.BookFingerprint, &p.Position, &p.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	if out == nil {
		out = []models.ReadingPosition{}
	}
	return out, rows.Err()
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// Ensure schema helper for tests
func (s *Store) Ping() error {
	if s.db == nil {
		return fmt.Errorf("nil db")
	}
	return s.db.Ping()
}
