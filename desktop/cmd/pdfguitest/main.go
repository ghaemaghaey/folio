//go:build windows

package main

// Simulates the Windows -H windowsgui environment by replacing os.Stdout/os.Stderr
// with invalid handles, then tries to init the PDFium WASM engine.
// Results are written to a file since stdout/stderr are intentionally broken.

import (
	"fmt"
	"os"
	"syscall"

	"github.com/folio-reader/folio/internal/pdf"
)

func main() {
	logFile := "pdfguitest-result.log"
	f, _ := os.Create(logFile)
	defer f.Close()
	log := func(format string, args ...any) {
		fmt.Fprintf(f, format+"\n", args...)
	}

	// On Windows, -H windowsgui means syscall.Stdout is an invalid handle.
	// Simulate this by replacing os.Stdout/Stderr with *os.File wrapping
	// an invalid handle (exactly what the runtime does under -H windowsgui).
	invalid := os.NewFile(uintptr(syscall.InvalidHandle), "/dev/stdout")
	os.Stdout = invalid
	os.Stderr = invalid

	log("Starting PDFium WASM with invalid stdout/stderr (simulating -H windowsgui)...")

	r, err := pdf.NewRenderer()
	if err != nil {
		log("FAIL NewRenderer: %v", err)
		f.Close()
		fmt.Println("Test failed — see", logFile)
		os.Exit(1)
	}
	defer r.Close()
	log("NewRenderer OK")

	n, err := r.Open("testdata/sample.pdf")
	if err != nil {
		log("OPEN FAIL: %v", err)
		f.Close()
		fmt.Println("Test failed — see", logFile)
		os.Exit(2)
	}
	log("OK pages=%d", n)
	f.Close()
	fmt.Println("Test passed — see", logFile)
}

