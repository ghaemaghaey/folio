.PHONY: dev build build-go tidy

# Preferred: full Wails pipeline (bindings + assets + production tags)
dev:
	wails dev

build:
	wails build

# Manual Go build — REQUIRES -tags production (or dev).
# Plain `go build .` will open a Wails error dialog and not run the app.
# Windows: -H windowsgui hides the console window.
build-go:
	go build -tags production -ldflags "-s -w -H windowsgui" -o build/bin/folio.exe .

tidy:
	go mod tidy
