package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/ghaemaghaey/folio/server/internal/store"
)

// Version is returned by /health and /v1/meta.
const Version = "0.1.0"

// Mount registers /v1 routes.
func Mount(r chi.Router, st *store.Store) {
	r.Route("/v1", func(r chi.Router) {
		r.Get("/meta", func(w http.ResponseWriter, _ *http.Request) {
			jsonOK(w, map[string]any{
				"name":    "folio-server",
				"version": Version,
				"purpose": "Reading progress sync and multi-device library metadata",
			})
		})

		r.Get("/books", func(w http.ResponseWriter, _ *http.Request) {
			jsonOK(w, st.ListBooks())
		})

		r.Get("/books/{id}", func(w http.ResponseWriter, r *http.Request) {
			id := chi.URLParam(r, "id")
			b, ok := st.GetBook(id)
			if !ok {
				jsonErr(w, http.StatusNotFound, "book not found")
				return
			}
			jsonOK(w, b)
		})

		r.Put("/books/{id}", func(w http.ResponseWriter, r *http.Request) {
			id := chi.URLParam(r, "id")
			var body store.Book
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				jsonErr(w, http.StatusBadRequest, "invalid json")
				return
			}
			body.ID = id
			if strings.TrimSpace(body.Title) == "" {
				jsonErr(w, http.StatusBadRequest, "title required")
				return
			}
			saved, err := st.UpsertBook(body)
			if err != nil {
				jsonErr(w, http.StatusInternalServerError, err.Error())
				return
			}
			jsonOK(w, saved)
		})

		r.Delete("/books/{id}", func(w http.ResponseWriter, r *http.Request) {
			id := chi.URLParam(r, "id")
			if err := st.DeleteBook(id); err != nil {
				jsonErr(w, http.StatusNotFound, err.Error())
				return
			}
			jsonOK(w, map[string]any{"ok": true, "id": id})
		})

		r.Get("/books/{id}/progress", func(w http.ResponseWriter, r *http.Request) {
			id := chi.URLParam(r, "id")
			p, ok := st.GetProgress(id)
			if !ok {
				jsonOK(w, store.Progress{BookID: id})
				return
			}
			jsonOK(w, p)
		})

		r.Put("/books/{id}/progress", func(w http.ResponseWriter, r *http.Request) {
			id := chi.URLParam(r, "id")
			var body store.Progress
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				jsonErr(w, http.StatusBadRequest, "invalid json")
				return
			}
			body.BookID = id
			saved, err := st.SetProgress(body)
			if err != nil {
				jsonErr(w, http.StatusInternalServerError, err.Error())
				return
			}
			jsonOK(w, saved)
		})
	})
}

func jsonOK(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

func jsonErr(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": msg})
}
