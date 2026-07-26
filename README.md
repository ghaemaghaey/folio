# Folio

A calm, beautiful desktop reading app for **Windows** and **Linux**.

Folio is inspired by [ReadEra](https://readera.org/) and modern reading apps (Thorium, Apple Books): distraction-free pages, paper-like themes, and typography that feels intentional — not a utility chrome shell around a PDF widget.

> **Current:** styled library shelf, PDF (page/scroll + zoom), EPUB reflow, last-position memory, missing/replaced remap, Persian (Rastikerdar) & English reading fonts.

![License](https://img.shields.io/badge/license-MIT-blue)
![Go](https://img.shields.io/badge/go-1.22+-00ADD8)
![Platform](https://img.shields.io/badge/platform-Windows%20%7C%20Linux-lightgrey)

## Why Folio looks the way it does

UI/UX quality is the primary success metric.

| Principle | How we apply it |
| --- | --- |
| **Distraction-free by default** | Reader chrome auto-hides; tap/hover or move the mouse to reveal |
| **Typography first** | Display face (Fraunces) + UI face (Inter); real sepia / light / dark tokens |
| **Paper, not “inverted colors”** | Sepia uses warm cream and brown ink; dark uses soft charcoal, not pure black |
| **One design system** | Spacing scale, radii, motion, and a single SVG icon language |
| **Native where it helps** | OS file dialogs and window chrome via Wails; custom reading surface |

## Stack

| Layer | Choice |
| --- | --- |
| Desktop shell | [Wails v2](https://wails.io) (Go + HTML/CSS/JS) |
| PDF | [go-pdfium](https://github.com/klippa-app/go-pdfium) **WebAssembly (Wazero)** — no CGO |
| Frontend | Plain HTML/CSS/JS with design tokens (no heavy UI kit) |
| License | **MIT** |

## Project layout

```
folio/
├── main.go                 # Wails entry
├── app.go                  # Bound methods (open PDF, render page, navigate)
├── internal/pdf/           # go-pdfium WASM renderer
├── frontend/dist/          # Styled UI (assets embedded as-is)
│   ├── index.html
│   ├── tokens.css          # Themes + spacing + type
│   ├── style.css
│   └── main.js
├── build/appicon.png
├── wails.json
├── LICENSE
└── README.md
```

## Prerequisites

- **Go** 1.22+
- **Wails CLI** v2 (`go install github.com/wailsapp/wails/v2/cmd/wails@latest`)
- Platform webview deps (see [Wails platform guide](https://wails.io/docs/gettingstarted/installation))
  - Windows: WebView2 (usually preinstalled on Win10/11)
  - Linux: `libgtk-3`, `libwebkit2gtk` (distro packages vary)

No CGO and no system PDFium install are required for the WASM backend.

## Develop

**Always use the Wails CLI** (or pass the correct Go build tags). A plain `go build` / `go run` without tags will show:

> *Wails applications will not build without the correct build tags.*

```bash
# from repo root — hot reload (uses -tags dev)
wails dev
```

Release binary **on the target OS** (native builds only for this project):

```bash
# Windows or Linux
wails build
```

Output: `build/bin/folio` (or `folio.exe` on Windows).

### Manual `go build` (if you must)

Wails selects platform code with build tags. You need **one** of:

| Tag | When |
| --- | --- |
| `production` | Shipping / normal run of embedded assets |
| `dev` | Development (what `wails dev` uses) |

```bash
# Windows
go build -tags production -o build/bin/folio.exe .

# Linux
go build -tags production -o build/bin/folio .
```

Do **not** run `go build .` or `go run .` without `-tags production` or `-tags dev`.

### Frontend-only preview

Open `frontend/dist/index.html` in a browser to review layout and themes. PDF open requires the Wails shell.

## Using the app

1. Launch Folio — welcome screen, or your **shelf** of previously opened books.
2. **Open book** — PDF or EPUB (native file dialog).
3. **Resume** — Folio reopens at the last page/chapter.
4. **Page vs scroll** — toggle in the reader chrome (or press `S` for PDF).
5. **Zoom** — `+` / `-`, toolbar buttons, or Ctrl+wheel.
6. **Fonts** — Aa menu: Literata, Source Serif, Merriweather, IBM Plex Serif, Georgia + Vazirmatn, Samim, Shabnam, Sahel, Tanha, Parastoo, Gandom (Saber Rastikerdar).
7. **Missing/replaced files** — shelf card greys out with label; **Map file…** rebinds the path.
8. **Themes:** Sepia → Light → Dark (`T`). Chrome auto-hides while reading.

## Design tokens (quick reference)

Defined in `frontend/dist/tokens.css`:

- **Spacing:** 4 → 8 → 12 → 16 → 24 → 32 → 48 → 64 (`--space-1` … `--space-8`)
- **Themes:** `sepia` (default), `light`, `dark`
- **Type:** UI = Inter; display/reading = Fraunces

## Roadmap (later phases)

- [x] Shelf of opened books + last-read position
- [x] Missing / replaced file detection + remap
- [x] Page / scroll modes + zoom
- [x] EPUB reflowable chapters
- [x] Persian (Rastikerdar) + English reading fonts
- [ ] Folder scan for new books
- [ ] Bookmarks & highlights
- [ ] TOC, in-document search
- [ ] Smoother PDF page cache / virtualized scroll

## License

MIT — see [LICENSE](./LICENSE).

PDFium (via go-pdfium) and Wazero are Apache-2.0; their terms apply to those components.
