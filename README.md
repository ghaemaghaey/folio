# Folio

**A calm, open-source desktop reader for Windows and Linux.**

Folio is a distraction-free app for reading PDFs and EPUBs — built because good reading software should feel like a book, not a browser tab full of toolbars.


[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.22+-00ADD8?logo=go)](https://go.dev/)
[![Platform](https://img.shields.io/badge/platform-Windows%20%7C%20Linux-lightgrey)](#install)
[![CI](https://github.com/ghaemaghaey/folio/actions/workflows/ci.yml/badge.svg)](https://github.com/OWNER/folio/actions/workflows/ci.yml)

> Replace `OWNER` in the badge URLs with your GitHub username or org after you push the repo.

---

## Why this project exists

Most desktop “readers” fall into one of two traps:

1. **Feature-heavy but ugly** — every setting visible at once, chrome fighting the page.
2. **Pretty on mobile only** — tools like ReadEra on Android nail calm UI; the desktop world still often feels like a PDF utility.

**Folio** started as an answer to that gap:

- Treat **UI/UX quality** as the main goal, not an afterthought.
- Give readers **paper-like themes**, real typography (including Persian fonts), and chrome that **gets out of the way**.
- Ship a **native, open-source** app for Windows and Linux with a stack that is pleasant to build and package (Go + Wails, PDF via PDFium/WASM — no CGO required for the default path).

In short: **a small, serious reading app you can trust with long sessions — and fork freely.**

---

## What it does

| Feature | Description |
| --- | --- |
| **PDF reading** | Render pages with [go-pdfium](https://github.com/klippa-app/go-pdfium) (WebAssembly / Wazero). Page mode or continuous scroll. |
| **EPUB reading** | Full-book continuous vertical scroll; chapters list for jumping. |
| **Bookshelf** | Remembers books you’ve opened; cover thumbs for PDFs; missing/replaced file remap. |
| **Last position** | Restores where you left off (page / scroll) per book. |
| **Zoom** | CSS zoom on PDF bitmaps (no re-render thrash); font scale on EPUB. |
| **Themes** | Sepia, light, and dark — tuned paper tones, not raw invert-only. PDF page filters follow the theme. |
| **Fonts** | English reading faces + Saber Rastikerdar Persian fonts (Vazirmatn, Samim, Shabnam, …). |
| **Reading guide** | Optional screen-fixed leading line (height / hue / lock). |
| **Distraction-free chrome** | Header/footer appear on edge hover or `M`; panels stay open while you use them. |

**Not (yet):** cloud sync, annotations store UI, full TOC search, DRM, macOS package (contributions welcome).

---

## Screenshots

_Add screenshots here after release (`docs/screenshots/`)._

---

## Install

### Prebuilt binaries (recommended)

1. Open **[Releases](https://github.com/OWNER/folio/releases)** on GitHub.
2. Download for your OS:
   - **Windows:** `folio-windows-amd64.exe` (or the zip from the release)
   - **Linux (generic / Ubuntu 22.04 CI):** `folio-linux-amd64`
   - **Debian 13 (Trixie):** `folio-linux-amd64-debian-trixie` — prefer this on Trixie laptops
3. Run the binary (Linux: `chmod +x folio-linux-amd64*` ).

Windows builds are linked as a **GUI app** (no console window).

### Build from source

**Requirements**

- [Go](https://go.dev/dl/) 1.22+
- Optional: [Wails CLI](https://wails.io) v2 for `wails build` / `wails dev`
- Linux: WebKitGTK / GTK deps (see [Wails installation](https://wails.io/docs/gettingstarted/installation))

```bash
git clone https://github.com/OWNER/folio.git
cd folio

# Windows (no console window)
go build -tags production -ldflags "-s -w -H windowsgui" -o build/bin/folio.exe .

# Linux
go build -tags production -ldflags "-s -w" -o build/bin/folio .
```

Or with Wails:

```bash
wails build
# output under build/bin/
```

**Important:** always use `-tags production` (or `wails build`). Plain `go build` / `go run` without tags shows a Wails error dialog.

---

## Usage (quick)

| Action | How |
| --- | --- |
| Open a book | **Open book** — PDF or EPUB |
| Hide / show chrome | Move to top/bottom edge, or press **`M`** |
| Page / scroll (PDF) | Toolbar toggle or **`S`** |
| Zoom | **`+` / `-`**, toolbar, or Ctrl+wheel |
| Themes | Theme control or **`T`** |
| Chapters (EPUB) | List icon or **`C`** |
| Reading guide | Guide icon or **`G`** |
| Back to shelf | Back arrow or **Esc** (when panels closed) |

Data is stored under your home directory:

- **Library / progress:** `~/.folio/library.json` (Windows: `%USERPROFILE%\.folio\library.json`)
- **PDF page cache:** `~/.folio/cache/pdf/`

---

## Stack

| Layer | Technology |
| --- | --- |
| Desktop shell | [Wails v2](https://wails.io) (Go + HTML/CSS/JS) |
| PDF | [go-pdfium](https://github.com/klippa-app/go-pdfium) WASM (Wazero) — no CGO by default |
| EPUB | Custom zip/OPF/XHTML parser + reflow in the webview |
| UI | Design tokens, plain CSS/JS (no heavy component library) |
| License | [MIT](LICENSE) |

---

## Project layout

```
folio/
├── main.go                 # Wails entry
├── app.go                  # Go ↔ UI bindings
├── internal/
│   ├── pdf/                # PDFium session + memory/disk cache
│   ├── epub/               # EPUB parse, TOC, chapters
│   └── library/            # Shelf + last-read store
├── frontend/dist/          # Embedded UI
├── .github/workflows/      # CI + release
├── scripts/build.ps1
├── Makefile
├── LICENSE
└── README.md
```

---

## Development

```bash
# Hot reload (needs Wails CLI + platform webview deps)
wails dev

# Or build production binary on the same OS you target
make build-go          # Windows Makefile target uses windowsgui
# Linux:
go build -tags production -ldflags "-s -w" -o build/bin/folio .
```

Frontend lives in `frontend/dist/` and is embedded as-is (no npm build step required).

---

## Release with GitHub Actions + GitHub CLI

### Automated releases (tags)

Pushing a version tag runs the **Release** workflow, which builds Windows and Linux binaries and uploads them to a GitHub Release:

```bash
# After commits are on the default branch:
git tag v0.6.3
git push origin v0.6.3
```

Workflow: [`.github/workflows/release.yml`](.github/workflows/release.yml)

Artifacts typically include:

- `folio-windows-amd64.exe`
- `folio-linux-amd64` (Ubuntu 22.04 runner)
- `folio-linux-amd64-debian-trixie` (built in `debian:trixie` container)
- checksums (`SHA256SUMS`)

### Manual publish with GitHub CLI (`gh`)

Install [GitHub CLI](https://cli.github.com/), then:

```bash
# Login once
gh auth login

# Create repo (first time only)
gh repo create folio --public --source=. --remote=origin --push

# After a tag + successful Actions run, or after local builds:
# Build locally first, then:
gh release create v0.6.0 \
  ./build/bin/folio-windows-amd64.exe \
  ./build/bin/folio-linux-amd64 \
  --title "Folio v0.6.0" \
  --notes "Calm desktop reader — PDF & EPUB."
```

Local multi-artifact helper (from repo root, on Windows you mainly produce the `.exe`; Linux binary is produced in CI):

```powershell
# Windows host
go build -tags production -ldflags "-s -w -H windowsgui" -o build/bin/folio-windows-amd64.exe .
gh release create v0.6.0 ./build/bin/folio-windows-amd64.exe --generate-notes
```

---

## Design principles

| Principle | Practice |
| --- | --- |
| Distraction-free by default | Chrome hides; reading surface first |
| Typography first | Reading fonts + careful themes |
| Paper, not gimmicks | Warm sepia / soft dark, not pure black invert alone |
| One design system | Shared spacing, color tokens, icons |
| Open and portable | MIT, native builds per OS, WASM PDF path |

---

## Contributing

Issues and PRs are welcome. Useful areas:

- macOS packaging  
- Bookmarks / highlights  
- In-document search & richer TOC  
- Faster first EPUB open for huge files  
- Screenshots and translations  

Please keep UI changes consistent with the existing design tokens in `frontend/dist/tokens.css`.

---

## License

[MIT](LICENSE) © Folio contributors.

Third-party components (PDFium via go-pdfium, Wazero, fonts) keep their own licenses (typically Apache-2.0 / OFL). See upstream projects for details.

---

## Acknowledgments

- Inspired by **ReadEra**, **Thorium**, and other calm reading apps  
- Persian type by **Saber Rastikerdar** (Vazirmatn and related families)  
- Built with **Wails** and **go-pdfium**
