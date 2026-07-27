package main

import (
	"embed"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/folio-reader/folio/internal/pdf"
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/linux"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
)

//go:embed all:frontend/dist
var assets embed.FS

//go:embed build/appicon.png
var icon []byte

func main() {
	app := NewApp()

	err := wails.Run(&options.App{
		Title:            "Folio",
		Width:            1180,
		Height:           780,
		MinWidth:         800,
		MinHeight:        560,
		BackgroundColour: &options.RGBA{R: 245, G: 241, B: 232, A: 255},
		OnStartup:        app.startup,
		OnShutdown:       app.shutdown,
		AssetServer: &assetserver.Options{
			Assets: assets,
			// Middleware runs first so /__folio_pdf/* is not swallowed by SPA/embed 404.
			Middleware: pdfCacheMiddleware,
		},
		Bind: []interface{}{
			app,
		},
		Windows: &windows.Options{
			WebviewIsTransparent: false,
			WindowIsTranslucent:  false,
			DisableWindowIcon:   false,
			Theme:               windows.SystemDefault,
		},
		Linux: &linux.Options{
			Icon:                icon,
			WindowIsTranslucent: false,
		},
	})
	if err != nil {
		log.Fatal(err)
	}
}

func pdfCacheMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if i := strings.Index(path, pdf.HTTPPrefix); i >= 0 {
			path = path[i:]
		}
		if strings.HasPrefix(path, pdf.HTTPPrefix) &&
			(r.Method == http.MethodGet || r.Method == http.MethodHead) {
			servePDFCache(w, r, path)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func servePDFCache(w http.ResponseWriter, r *http.Request, urlPath string) {
	root, err := pdf.DefaultDiskRoot()
	if err != nil {
		http.Error(w, "cache unavailable", http.StatusInternalServerError)
		return
	}
	full, ok := pdf.ResolveCacheFile(root, urlPath)
	if !ok {
		pdf.DebugLog("cache miss path=%q root=%q", urlPath, root)
		http.NotFound(w, r)
		return
	}
	if !strings.EqualFold(filepath.Ext(full), ".jpg") {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(full)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	http.ServeContent(w, r, st.Name(), st.ModTime(), f)
}
