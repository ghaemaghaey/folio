package main
import (
  "fmt"
  "os"
  "github.com/folio-reader/folio/internal/pdf"
)
func main() {
  r, err := pdf.NewRenderer()
  if err != nil { fmt.Fprintln(os.Stderr, "FAIL", err); os.Exit(1) }
  defer r.Close()
  n, err := r.Open("testdata/sample.pdf")
  if err != nil { fmt.Fprintln(os.Stderr, "OPEN", err); os.Exit(2) }
  fmt.Println("OK pages", n)
}
