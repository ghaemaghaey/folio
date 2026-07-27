package models

// User is a stored account (password hash never serialized in API responses).
type User struct {
	ID           int64  `json:"id"`
	Username     string `json:"username"`
	PasswordHash string `json:"-"`
	CreatedAt    string `json:"created_at,omitempty"`
}

// Book is a local library write record keyed by content fingerprint.
type Book struct {
	Fingerprint  string `json:"fingerprint"`
	CalibreBookID *int64 `json:"calibre_book_id"`
	Title        string `json:"title"`
	Author       string `json:"author"`
	Format       string `json:"format"`
	UploadedBy   int64  `json:"uploaded_by"`
	CreatedAt    string `json:"created_at,omitempty"`
}

// ReadingPosition is per-user progress for a book fingerprint.
type ReadingPosition struct {
	UserID          int64  `json:"user_id"`
	BookFingerprint string `json:"fingerprint"`
	Position        string `json:"position"`
	UpdatedAt       string `json:"updated_at,omitempty"`
}

// AuthRequest is shared by register/login.
type AuthRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// AuthResponse is returned after successful register/login.
type AuthResponse struct {
	Token    string `json:"token"`
	UserID   int64  `json:"user_id"`
	Username string `json:"username"`
}

// ProgressRequest is the body for POST /progress.
type ProgressRequest struct {
	Fingerprint string `json:"fingerprint"`
	Position    string `json:"position"`
}
