package api_test

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/ghaemaghaey/folio/server/internal/api"
	"github.com/ghaemaghaey/folio/server/internal/config"
	"github.com/ghaemaghaey/folio/server/internal/db"
	"github.com/ghaemaghaey/folio/server/internal/store"
)

func setup(t *testing.T) http.Handler {
	t.Helper()
	dir := t.TempDir()
	sqlDB, err := db.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	cfg := config.Config{
		JWTSecret: "test-secret",
		// no calibre path → upload stores metadata only
	}
	s := api.New(cfg, store.New(sqlDB))
	r := chi.NewRouter()
	s.Routes(r)
	return r
}

func TestRegisterLoginProgressUpload(t *testing.T) {
	h := setup(t)

	// Register
	body := `{"username":"alice","password":"secret12"}`
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/register", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("register: %d %s", rr.Code, rr.Body.String())
	}
	var auth struct {
		Token  string `json:"token"`
		UserID int64  `json:"user_id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &auth); err != nil || auth.Token == "" {
		t.Fatalf("auth parse: %v body=%s", err, rr.Body.String())
	}

	// Login
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/login", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("login: %d %s", rr.Code, rr.Body.String())
	}

	// Progress (creates stub book if needed)
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/progress", bytes.NewBufferString(
		`{"fingerprint":"abc123","position":"page:5"}`,
	))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+auth.Token)
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("progress post: %d %s", rr.Code, rr.Body.String())
	}

	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/progress/abc123", nil)
	req.Header.Set("Authorization", "Bearer "+auth.Token)
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("progress get: %d %s", rr.Code, rr.Body.String())
	}

	// Upload without calibre
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", "hello.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(fw, "%PDF-1.4 fake content for test"); err != nil {
		t.Fatal(err)
	}
	_ = mw.Close()
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/books/upload", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+auth.Token)
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", rr.Code, rr.Body.String())
	}
	var up struct {
		Book struct {
			Fingerprint string `json:"fingerprint"`
			Format      string `json:"format"`
		} `json:"book"`
		Deduped bool `json:"deduped"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &up); err != nil {
		t.Fatal(err)
	}
	if up.Book.Fingerprint == "" || up.Book.Format != "pdf" || up.Deduped {
		t.Fatalf("unexpected upload: %+v", up)
	}

	// Dedup second upload
	buf.Reset()
	mw = multipart.NewWriter(&buf)
	fw, _ = mw.CreateFormFile("file", "hello.pdf")
	_, _ = io.WriteString(fw, "%PDF-1.4 fake content for test")
	_ = mw.Close()
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/books/upload", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+auth.Token)
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("dedup upload: %d %s", rr.Code, rr.Body.String())
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &up); err != nil {
		t.Fatal(err)
	}
	if !up.Deduped {
		t.Fatalf("expected deduped")
	}

	_ = os.DevNull // keep import used on older go if needed
}
