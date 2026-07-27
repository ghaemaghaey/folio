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
			Assets:  assets,
			Handler: http.HandlerFunc(pdfCacheHandler),
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

// pdfCacheHandler serves rendered PDF page JPEGs at /__folio_pdf/...
// so the UI never needs multi‑MB base64 strings over the Wails bridge.
func pdfCacheHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !strings.HasPrefix(r.URL.Path, pdf.HTTPPrefix) {
		http.NotFound(w, r)
		return
	}
	root, err := pdf.DefaultDiskRoot()
	if err != nil {
		http.Error(w, "cache unavailable", http.StatusInternalServerError)
		return
	}
	full, ok := pdf.ResolveCacheFile(root, r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	// Extra safety: only .jpg under cache root
	if !strings.EqualFold(filepath.Ext(full), ".jpg") {
		http.NotFound(w, r)
		return
	}
	if _, err := os.Stat(full); err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	http.ServeFile(w, r, full)
}
