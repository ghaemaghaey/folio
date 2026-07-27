package main

import (
	"log"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/ghaemaghaey/folio/server/internal/api"
	"github.com/ghaemaghaey/folio/server/internal/config"
	"github.com/ghaemaghaey/folio/server/internal/db"
	"github.com/ghaemaghaey/folio/server/internal/store"
)

func main() {
	cfg := config.Load()
	if cfg.JWTSecret == "change-me-in-production" {
		log.Printf("WARNING: JWT_SECRET is the default value — set a strong secret in production")
	}
	if cfg.CalibreLibraryPath == "" {
		log.Printf("WARNING: CALIBRE_LIBRARY_PATH unset — /books/upload will store metadata only (no calibredb)")
	}

	sqlDB, err := db.Open(cfg.DBPath)
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer sqlDB.Close()

	st := store.New(sqlDB)
	srv := api.New(cfg, st)

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(120 * time.Second))
	r.Use(corsMiddleware)

	srv.Routes(r)

	log.Printf("folio-server %s listening on %s (db=%s library=%q)",
		api.Version, cfg.ListenAddr, cfg.DBPath, cfg.CalibreLibraryPath)
	if err := http.ListenAndServe(cfg.ListenAddr, r); err != nil {
		log.Fatal(err)
	}
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
