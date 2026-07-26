package main

import (
  "fmt"
  "os"
  "github.com/folio-reader/folio/internal/pdf"
)

func main() {
  r, err := pdf.NewRenderer()
  if err != nil { fmt.Println("init err:", err); os.Exit(1) }
  defer r.Close()
  n, err := r.PageCount("testdata/sample.pdf")
  if err != nil { fmt.Println("count err:", err); os.Exit(1) }
  fmt.Println("pages:", n)
  url, w, h, err := r.RenderPage("testdata/sample.pdf", 0, 96)
  if err != nil { fmt.Println("render err:", err); os.Exit(1) }
  fmt.Printf("ok w=%d h=%d dataURL_len=%d\n", w, h, len(url))
}
