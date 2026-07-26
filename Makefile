.PHONY: dev build build-go tidy

# Preferred: full Wails pipeline (bindings + assets + production tags)
dev:
	wails dev

build:
	wails build

# Manual Go build — REQUIRES -tags production (or dev).
# Plain `go build .` will open a Wails error dialog and not run the app.
build-go:
	go build -tags production -ldflags "-s -w" -o build/bin/folio .

tidy:
	go mod tidy
