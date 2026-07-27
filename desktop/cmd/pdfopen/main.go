package main

import (
	"fmt"
	"os"
	"path/filepath"
	"github.com/folio-reader/folio/internal/pdf"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: pdfopen <path>")
		os.Exit(2)
	}
	path := filepath.Clean(os.Args[1])
	fmt.Println("path:", path)
	st, err := os.Stat(path)
	if err != nil {
		fmt.Println("stat:", err)
		os.Exit(1)
	}
	fmt.Println("size:", st.Size())

	r, err := pdf.NewRenderer()
	if err != nil {
		fmt.Println("NewRenderer:", err)
		os.Exit(1)
	}
	defer r.Close()
	fmt.Println("renderer ok")

	n, err := r.Open(path)
	if err != nil {
		fmt.Println("Open:", err)
		os.Exit(1)
	}
	fmt.Println("pages:", n)

	url, w, h, err := r.RenderPage(path, 0, 128)
	if err != nil {
		fmt.Println("RenderPage:", err)
		os.Exit(1)
	}
	pre := url
	if len(pre) > 40 { pre = pre[:40] }
	fmt.Printf("render ok %dx%d urlLen=%d prefix=%s\n", w, h, len(url), pre)
}
