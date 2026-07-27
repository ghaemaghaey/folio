package calibre

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Writer modes for Client.Writer.
const (
	WriterAuto      = "auto"      // native if no calibredb, else calibredb
	WriterNative    = "native"    // pure-Go metadata.db (slim containers)
	WriterCalibredb = "calibredb" // shell out to full Calibre
)

// Client adds books to a Calibre library (native Go and/or calibredb).
type Client struct {
	Bin         string // default: calibredb
	LibraryPath string
	// Writer: auto | native | calibredb (from LIBRARY_WRITER env).
	Writer string
	// Optional metadata for native writer (set by API before Add).
	Title  string
	Author string
	Format string
}

// Add imports a file into the Calibre library and returns the new book id.
func (c *Client) Add(filePath string) (int64, error) {
	if strings.TrimSpace(c.LibraryPath) == "" {
		return 0, fmt.Errorf("CALIBRE_LIBRARY_PATH is not set")
	}
	abs, err := filepath.Abs(filePath)
	if err != nil {
		return 0, err
	}
	if _, err := os.Stat(abs); err != nil {
		return 0, fmt.Errorf("temp file: %w", err)
	}
	if _, err := os.Stat(c.LibraryPath); err != nil {
		return 0, fmt.Errorf("library path: %w", err)
	}

	mode := strings.ToLower(strings.TrimSpace(c.Writer))
	if mode == "" {
		mode = WriterAuto
	}
	switch mode {
	case WriterNative:
		return NativeAdd(c.LibraryPath, abs, c.Title, c.Author, c.Format)
	case WriterCalibredb:
		return c.addCalibredb(abs)
	default: // auto
		if _, err := exec.LookPath(c.calibredbBin()); err != nil {
			return NativeAdd(c.LibraryPath, abs, c.Title, c.Author, c.Format)
		}
		id, err := c.addCalibredb(abs)
		if err != nil {
			// Fall back to native if calibredb is broken in the container
			return NativeAdd(c.LibraryPath, abs, c.Title, c.Author, c.Format)
		}
		return id, nil
	}
}

func (c *Client) calibredbBin() string {
	if c.Bin != "" {
		return c.Bin
	}
	return "calibredb"
}

// addCalibredb shells out to calibredb. Output is typically: "Added book ids: 42"
func (c *Client) addCalibredb(abs string) (int64, error) {
	bin := c.calibredbBin()

	cmd := exec.Command(bin, "add", abs, "--with-library", c.LibraryPath)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return 0, fmt.Errorf("calibredb add failed: %w; stderr=%s; stdout=%s",
			err, strings.TrimSpace(stderr.String()), strings.TrimSpace(stdout.String()))
	}
	out := stdout.String() + "\n" + stderr.String()
	id, err := parseAddedBookID(out)
	if err != nil {
		return 0, fmt.Errorf("%w; raw output: %q", err, strings.TrimSpace(out))
	}
	return id, nil
}

var (
	reAddedIDs = regexp.MustCompile(`(?i)Added book ids?:\s*([0-9]+(?:\s*,\s*[0-9]+)*)`)
	reAnyID    = regexp.MustCompile(`(?i)book id[s]?:\s*([0-9]+)`)
)

func parseAddedBookID(output string) (int64, error) {
	if m := reAddedIDs.FindStringSubmatch(output); len(m) > 1 {
		// take first id if multiple
		part := strings.Split(m[1], ",")[0]
		part = strings.TrimSpace(part)
		return strconv.ParseInt(part, 10, 64)
	}
	if m := reAnyID.FindStringSubmatch(output); len(m) > 1 {
		return strconv.ParseInt(strings.TrimSpace(m[1]), 10, 64)
	}
	// last resort: last integer in output
	reNum := regexp.MustCompile(`\b([0-9]{1,9})\b`)
	all := reNum.FindAllString(output, -1)
	if len(all) > 0 {
		return strconv.ParseInt(all[len(all)-1], 10, 64)
	}
	return 0, fmt.Errorf("could not parse calibredb book id from output")
}

// GuessTitleFormat derives title + format from a filename (e.g. "My Book.epub").
func GuessTitleFormat(filename string) (title, format string) {
	base := filepath.Base(filename)
	ext := strings.ToLower(filepath.Ext(base))
	title = strings.TrimSuffix(base, filepath.Ext(base))
	if title == "" {
		title = base
	}
	switch ext {
	case ".epub":
		format = "epub"
	case ".pdf":
		format = "pdf"
	case ".mobi":
		format = "mobi"
	case ".azw", ".azw3":
		format = "azw3"
	case ".cbz", ".cbr":
		format = strings.TrimPrefix(ext, ".")
	default:
		if ext != "" {
			format = strings.TrimPrefix(ext, ".")
		} else {
			format = "bin"
		}
	}
	return title, format
}
