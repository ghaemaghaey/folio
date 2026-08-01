# Folio

**Calm reading, open source — desktop, Android, and a small sync server.**

This repository is a **monorepo** for the whole Folio stack:

| Package | Path | Description |
| --- | --- | --- |
| **Desktop** | [`desktop/`](desktop/) | Windows & Linux reader (Go + Wails) — PDF, EPUB, OPDS catalog |
| **Android** | [`android/`](android/) | Same UI/UX in a WebView shell (Kotlin backend) |
| **Server** | [`server/`](server/) | Auth + reading progress + Calibre library writer (`calibredb`) |

[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![CI](https://github.com/ghaemaghaey/folio/actions/workflows/ci.yml/badge.svg)](https://github.com/ghaemaghaey/folio/actions/workflows/ci.yml)
[![Android](https://img.shields.io/badge/Download-Android-green.svg)](https://github.com/ghaemaghaey/folio/actions/workflows/android.yml)

---

## Downloads

| Platform | Link |
| --- | --- |
| **Android** | [Download latest build](https://github.com/ghaemaghaey/folio/actions/workflows/android.yml) — click the newest run, scroll to Artifacts |
| **Windows** | [Download latest build](https://github.com/ghaemaghaey/folio/actions/workflows/ci.yml) — click the newest run, download `folio-windows-amd64` |
| **Linux** | [Download latest build](https://github.com/ghaemaghaey/folio/actions/workflows/ci.yml) — click the newest run, download `folio-linux-amd64-debian-trixie` |
| **Server** | `docker pull ghcr.io/ghaemaghaey/folio-server:latest` |

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

### Server (folio-server)

```bash
cd server
export JWT_SECRET=dev-secret
export DB_PATH=./data/folio.db
# optional shared Calibre library:
# export CALIBRE_LIBRARY_PATH=/path/to/Calibre\ Library
go run .
# http://127.0.0.1:8090/health
```

Docker image is published by CI to GHCR:

```text
ghcr.io/ghaemaghaey/folio-server:latest
```

On the Calibre host:

```bash
cd server
cp .env.example .env   # set JWT_SECRET + CALIBRE_LIBRARY_HOST_PATH
docker compose pull && docker compose up -d
```

See [`server/README.md`](server/README.md) for API docs and package visibility notes.

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
