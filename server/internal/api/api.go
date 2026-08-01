package api

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/ghaemaghaey/folio/server/internal/auth"
	"github.com/ghaemaghaey/folio/server/internal/calibre"
	"github.com/ghaemaghaey/folio/server/internal/config"
	"github.com/ghaemaghaey/folio/server/internal/models"
	"github.com/ghaemaghaey/folio/server/internal/store"
)

// Version of the folio-server API.
const Version = "1.0.0"

// Server holds dependencies for HTTP handlers.
type Server struct {
	cfg     config.Config
	store   *store.Store
	calibre *calibre.Client
}

// New constructs the API server.
func New(cfg config.Config, st *store.Store) *Server {
	return &Server{
		cfg:   cfg,
		store: st,
		calibre: &calibre.Client{
			Bin:         cfg.CalibredbBin,
			LibraryPath: cfg.CalibreLibraryPath,
			Writer:      cfg.LibraryWriter,
		},
	}
}

// Routes mounts public + authenticated routes on r.
func (s *Server) Routes(r chi.Router) {
	r.Get("/health", s.handleHealth)

	r.Post("/register", s.handleRegister)
	r.Post("/login", s.handleLogin)

	r.Group(func(pr chi.Router) {
		pr.Use(auth.Middleware(s.cfg.JWTSecret))

		pr.Post("/books/upload", s.handleBookUpload)
		pr.Get("/books", s.handleListBooks)
		pr.Get("/books/{fingerprint}", s.handleGetBook)

		pr.Post("/progress", s.handleUpsertProgress)
		pr.Get("/progress", s.handleListProgress)
		pr.Get("/progress/{fingerprint}", s.handleGetProgress)
		pr.Get("/progress/{fingerprint}/devices", s.handleGetProgressDevices)
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"service": "folio-server",
		"version": Version,
	})
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req models.AuthRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if len(req.Username) < 2 || len(req.Password) < 6 {
		writeError(w, http.StatusBadRequest, "username min 2 chars, password min 6 chars")
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not hash password")
		return
	}
	user, err := s.store.CreateUser(req.Username, hash)
	if errors.Is(err, store.ErrConflict) {
		writeError(w, http.StatusConflict, "username already taken")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	token, err := auth.IssueToken(s.cfg.JWTSecret, user.ID, user.Username)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not issue token")
		return
	}
	writeJSON(w, http.StatusCreated, models.AuthResponse{
		Token: token, UserID: user.ID, Username: user.Username,
	})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req models.AuthRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	user, err := s.store.GetUserByUsername(strings.TrimSpace(req.Username))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusUnauthorized, "invalid username or password")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !auth.CheckPassword(user.PasswordHash, req.Password) {
		writeError(w, http.StatusUnauthorized, "invalid username or password")
		return
	}
	token, err := auth.IssueToken(s.cfg.JWTSecret, user.ID, user.Username)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not issue token")
		return
	}
	writeJSON(w, http.StatusOK, models.AuthResponse{
		Token: token, UserID: user.ID, Username: user.Username,
	})
}

func (s *Server) handleBookUpload(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserIDFromContext(r.Context())
	if userID == 0 {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	// Cap multipart memory; file itself streams to disk.
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "invalid multipart form (max 64MiB in memory buffer)")
		return
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		// also accept "book"
		file, hdr, err = r.FormFile("book")
		if err != nil {
			writeError(w, http.StatusBadRequest, `multipart field "file" (or "book") is required`)
			return
		}
	}
	defer file.Close()

	tmpDir, err := os.MkdirTemp("", "folio-upload-*")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "temp dir")
		return
	}
	defer os.RemoveAll(tmpDir)

	safeName := filepath.Base(hdr.Filename)
	if safeName == "." || safeName == "" {
		safeName = "upload.bin"
	}
	tmpPath := filepath.Join(tmpDir, safeName)
	out, err := os.Create(tmpPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "temp file")
		return
	}

	h := sha256.New()
	mw := io.MultiWriter(out, h)
	if _, err := io.Copy(mw, file); err != nil {
		out.Close()
		writeError(w, http.StatusInternalServerError, "failed to save upload")
		return
	}
	if err := out.Close(); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to close upload")
		return
	}
	fingerprint := hex.EncodeToString(h.Sum(nil))

	// Dedup: same file content already known.
	if existing, err := s.store.GetBook(fingerprint); err == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"book":    existing,
			"deduped": true,
		})
		return
	} else if !errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	title, format := calibre.GuessTitleFormat(safeName)
	if t := strings.TrimSpace(r.FormValue("title")); t != "" {
		title = t
	}
	author := strings.TrimSpace(r.FormValue("author"))

	var calibreID *int64
	if strings.TrimSpace(s.cfg.CalibreLibraryPath) != "" {
		s.calibre.Title = title
		s.calibre.Author = author
		s.calibre.Format = format
		id, err := s.calibre.Add(tmpPath)
		if err != nil {
			log.Printf("library add error: %v", err)
			writeError(w, http.StatusBadGateway, "library add failed: "+err.Error())
			return
		}
		calibreID = &id
	} else {
		log.Printf("CALIBRE_LIBRARY_PATH unset — storing folio DB metadata only")
	}

	book, err := s.store.InsertBook(models.Book{
		Fingerprint:   fingerprint,
		CalibreBookID: calibreID,
		Title:         title,
		Author:        author,
		Format:        format,
		UploadedBy:    userID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"book":    book,
		"deduped": false,
	})
}

func (s *Server) handleListBooks(w http.ResponseWriter, r *http.Request) {
	books, err := s.store.ListBooks()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, books)
}

func (s *Server) handleGetBook(w http.ResponseWriter, r *http.Request) {
	fp := chi.URLParam(r, "fingerprint")
	book, err := s.store.GetBook(fp)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "book not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, book)
}

func (s *Server) handleUpsertProgress(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserIDFromContext(r.Context())
	var req models.ProgressRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	p, err := s.store.UpsertProgress(userID, req.Fingerprint, req.Device, req.Position)
	if errors.Is(err, store.ErrInvalidInput) {
		writeError(w, http.StatusBadRequest, "fingerprint and position are required")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleListProgress(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserIDFromContext(r.Context())
	list, err := s.store.ListProgress(userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleGetProgress(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserIDFromContext(r.Context())
	fp := chi.URLParam(r, "fingerprint")
	p, err := s.store.GetProgress(userID, fp)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "position not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleGetProgressDevices(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserIDFromContext(r.Context())
	fp := chi.URLParam(r, "fingerprint")
	list, err := s.store.GetProgressDevices(userID, fp)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}
