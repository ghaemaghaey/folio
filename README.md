# Folio

**Calm reading, open source — desktop, Android, and a small sync server.**

This repository is a **monorepo** for the whole Folio stack:

| Package | Path | Description |
| --- | --- | --- |
| **Desktop** | [`desktop/`](desktop/) | Windows & Linux reader (Go + Wails) — PDF, EPUB, OPDS catalog |
| **Android** | [`android/`](android/) | Same UI/UX in a WebView shell (Kotlin backend) |
| **Server** | [`server/`](server/) | Lightweight HTTP API for multi-device progress sync |

[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![CI](https://github.com/ghaemaghaey/folio/actions/workflows/ci.yml/badge.svg)](https://github.com/ghaemaghaey/folio/actions/workflows/ci.yml)

---

## Quick start

### Desktop (Windows / Linux)

```bash
cd desktop
go build -tags production -ldflags "-s -w -H windowsgui" -o build/bin/folio.exe .   # Windows
# Linux:
# go build -tags production -ldflags "-s -w" -o build/bin/folio .
```

See [`desktop/README.md`](desktop/README.md) for install notes, themes, OPDS, and packaging.

### Android

Open `android/` in Android Studio, or:

```bash
cd android
./gradlew :app:assembleDebug
# APK: android/app/build/outputs/apk/debug/app-debug.apk
```

See [`android/README.md`](android/README.md).

### Server

```bash
cd server
go run .
# http://localhost:8787/health
```

See [`server/README.md`](server/README.md).

---

## Repository layout

```
folio/
├── desktop/          # Wails desktop app (original Folio)
├── android/          # Android app
├── server/           # folio-server HTTP API
├── .github/workflows # CI / releases
├── LICENSE
└── README.md         # this file
```

Git history for the desktop app is preserved (paths moved under `desktop/`).

---

## License

[MIT](LICENSE) — Folio Contributors.
