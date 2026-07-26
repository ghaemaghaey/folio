// Package opds is a small OPDS 1.x (Atom) client for Calibre-Web-style catalogs.
package opds

import (
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Common OPDS / Atom link relations.
const (
	RelAcquisition = "http://opds-spec.org/acquisition"
	RelImage       = "http://opds-spec.org/image"
	RelThumbnail   = "http://opds-spec.org/image/thumbnail"
	RelSubsection  = "subsection"
	RelNext        = "next"
	RelStart       = "start"
	RelSelf        = "self"
)

// Client fetches OPDS feeds. Auth is optional Basic Auth (ready for later UI).
type Client struct {
	BaseURL  string
	Username string
	Password string
	HTTP     *http.Client
}

// NewClient builds a client for baseURL (e.g. http://host:8083). Trailing slash optional.
func NewClient(baseURL, username, password string) *Client {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	return &Client{
		BaseURL:  baseURL,
		Username: username,
		Password: password,
		HTTP: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

// Resolve turns a relative or absolute href into an absolute URL using BaseURL.
func (c *Client) Resolve(href string) (string, error) {
	href = strings.TrimSpace(href)
	if href == "" {
		return "", fmt.Errorf("empty href")
	}
	if c.BaseURL == "" {
		return "", fmt.Errorf("OPDS base URL is not configured")
	}
	base, err := url.Parse(c.BaseURL + "/")
	if err != nil {
		return "", fmt.Errorf("invalid base URL: %w", err)
	}
	ref, err := url.Parse(href)
	if err != nil {
		return "", err
	}
	return base.ResolveReference(ref).String(), nil
}

// Fetch retrieves and parses one OPDS/Atom feed page.
func (c *Client) Fetch(pathOrURL string) (*Feed, error) {
	if c.BaseURL == "" {
		return nil, fmt.Errorf("OPDS base URL is not configured")
	}
	abs := pathOrURL
	if pathOrURL == "" {
		abs = c.BaseURL + "/opds"
	} else if strings.HasPrefix(pathOrURL, "http://") || strings.HasPrefix(pathOrURL, "https://") {
		abs = pathOrURL
	} else if strings.HasPrefix(pathOrURL, "/") {
		abs = c.BaseURL + pathOrURL
	} else {
		var err error
		abs, err = c.Resolve(pathOrURL)
		if err != nil {
			return nil, err
		}
	}

	req, err := http.NewRequest(http.MethodGet, abs, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/atom+xml, application/xml, text/xml, */*")
	req.Header.Set("User-Agent", "Folio-OPDS/1.0")
	if c.Username != "" {
		req.SetBasicAuth(c.Username, c.Password)
	}

	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("OPDS request failed: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 512))
		return nil, fmt.Errorf("OPDS HTTP %d: %s", res.StatusCode, strings.TrimSpace(string(body)))
	}

	data, err := io.ReadAll(io.LimitReader(res.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	feed, err := ParseFeed(data)
	if err != nil {
		return nil, err
	}
	feed.SelfURL = abs
	// Resolve relative next / cover / acquisition hrefs against feed URL.
	feedBase, _ := url.Parse(abs)
	for i := range feed.Entries {
		e := &feed.Entries[i]
		e.CoverURL = resolveAgainst(feedBase, e.CoverURL)
		e.ThumbnailURL = resolveAgainst(feedBase, e.ThumbnailURL)
		for j := range e.Acquisitions {
			e.Acquisitions[j].Href = resolveAgainst(feedBase, e.Acquisitions[j].Href)
		}
	}
	if feed.NextURL != "" {
		feed.NextURL = resolveAgainst(feedBase, feed.NextURL)
	}
	return feed, nil
}

// FetchBooksRoot loads the book list, preferring newest-first feeds.
func (c *Client) FetchBooksRoot() (*Feed, error) {
	// Calibre-Web: /opds/new = “Recently added” (newest first).
	// Fallbacks cover alphabetical / root navigation.
	candidates := []string{
		"/opds/new",
		"/opds/newest",
		"/opds/books/letter/00",
		"/opds/books",
		"/opds",
	}
	var lastErr error
	for _, p := range candidates {
		feed, err := c.Fetch(p)
		if err != nil {
			lastErr = err
			continue
		}
		// Prefer a page that already has acquisition entries.
		if feed.BookCount() > 0 {
			return feed, nil
		}
		// Navigation-only: try to follow a “Books” style subsection.
		if next := feed.FindBooksNavHref(); next != "" {
			feed2, err2 := c.Fetch(next)
			if err2 == nil && feed2.BookCount() > 0 {
				return feed2, nil
			}
			// letter index: follow first subsection if present
			if feed2 != nil && len(feed2.Entries) > 0 {
				for _, e := range feed2.Entries {
					if e.IsNavigation && e.NavURL != "" {
						feed3, err3 := c.Fetch(e.NavURL)
						if err3 == nil && feed3.BookCount() > 0 {
							return feed3, nil
						}
					}
				}
			}
		}
		if lastErr == nil {
			lastErr = fmt.Errorf("no book entries at %s", p)
		}
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("could not find OPDS book list")
}

// Search runs a Calibre-Web OPDS search (OpenSearch-style path).
// Template from feed: /opds/search/{searchTerms}
func (c *Client) Search(query string) (*Feed, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return c.FetchBooksRoot()
	}
	// Path-segment encoding keeps spaces as %20 (Calibre-Web expects this form).
	enc := url.PathEscape(q)
	candidates := []string{
		"/opds/search/" + enc,
		"/opds/search?query=" + url.QueryEscape(q),
		"/opds/search?q=" + url.QueryEscape(q),
	}
	var lastErr error
	for _, p := range candidates {
		feed, err := c.Fetch(p)
		if err != nil {
			lastErr = err
			continue
		}
		return feed, nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("search failed")
}

// Download opens an acquisition URL for streaming (caller closes Body).
func (c *Client) Download(href string) (*http.Response, error) {
	abs, err := c.Resolve(href)
	if href != "" && (strings.HasPrefix(href, "http://") || strings.HasPrefix(href, "https://")) {
		abs = href
		err = nil
	}
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodGet, abs, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Folio-OPDS/1.0")
	if c.Username != "" {
		req.SetBasicAuth(c.Username, c.Password)
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		res.Body.Close()
		return nil, fmt.Errorf("download HTTP %d", res.StatusCode)
	}
	return res, nil
}

func resolveAgainst(base *url.URL, href string) string {
	href = strings.TrimSpace(href)
	if href == "" || base == nil {
		return href
	}
	if strings.HasPrefix(href, "http://") || strings.HasPrefix(href, "https://") {
		return href
	}
	ref, err := url.Parse(href)
	if err != nil {
		return href
	}
	return base.ResolveReference(ref).String()
}

// --- Atom / OPDS models ---------------------------------------------------

// Feed is one page of an OPDS catalog.
type Feed struct {
	Title   string
	SelfURL string
	NextURL string
	Entries []Entry
}

// BookCount returns entries that have at least one acquisition link.
func (f *Feed) BookCount() int {
	n := 0
	for _, e := range f.Entries {
		if len(e.Acquisitions) > 0 {
			n++
		}
	}
	return n
}

// FindBooksNavHref looks for a navigation entry that points at a book index.
func (f *Feed) FindBooksNavHref() string {
	for _, e := range f.Entries {
		if !e.IsNavigation || e.NavURL == "" {
			continue
		}
		low := strings.ToLower(e.Title + " " + e.NavURL)
		if strings.Contains(low, "book") || strings.Contains(low, "/books") {
			return e.NavURL
		}
	}
	// Fallback: first navigation link
	for _, e := range f.Entries {
		if e.IsNavigation && e.NavURL != "" {
			return e.NavURL
		}
	}
	return ""
}

// Entry is a navigation node or a book.
type Entry struct {
	ID           string
	Title        string
	Authors      []string
	Summary      string
	CoverURL     string
	ThumbnailURL string
	Acquisitions []Acquisition
	IsNavigation bool
	NavURL       string
}

// Acquisition is a downloadable format link.
type Acquisition struct {
	Href   string
	Type   string // MIME, e.g. application/epub+zip
	Length int64  // bytes if known
	Title  string
}

// FormatLabel returns pdf / epub / other from MIME type.
func (a Acquisition) FormatLabel() string {
	t := strings.ToLower(a.Type)
	switch {
	case strings.Contains(t, "epub"):
		return "epub"
	case strings.Contains(t, "pdf"):
		return "pdf"
	default:
		return "bin"
	}
}

// PreferredAcquisition picks EPUB over PDF over first link.
func (e Entry) PreferredAcquisition() (Acquisition, bool) {
	if len(e.Acquisitions) == 0 {
		return Acquisition{}, false
	}
	var pdf *Acquisition
	for i := range e.Acquisitions {
		a := &e.Acquisitions[i]
		lab := a.FormatLabel()
		if lab == "epub" {
			return *a, true
		}
		if lab == "pdf" && pdf == nil {
			pdf = a
		}
	}
	if pdf != nil {
		return *pdf, true
	}
	return e.Acquisitions[0], true
}

// --- XML parsing ----------------------------------------------------------

type atomFeed struct {
	XMLName xml.Name    `xml:"feed"`
	Title   string      `xml:"title"`
	Links   []atomLink  `xml:"link"`
	Entries []atomEntry `xml:"entry"`
}

type atomEntry struct {
	ID      string         `xml:"id"`
	Title   string         `xml:"title"`
	Summary string         `xml:"summary"`
	Content atomText       `xml:"content"`
	Authors []atomAuthor   `xml:"author"`
	Links   []atomLink     `xml:"link"`
}

type atomAuthor struct {
	Name string `xml:"name"`
}

type atomText struct {
	Type string `xml:"type,attr"`
	Body string `xml:",chardata"`
}

type atomLink struct {
	Rel    string `xml:"rel,attr"`
	Href   string `xml:"href,attr"`
	Type   string `xml:"type,attr"`
	Length string `xml:"length,attr"`
	Title  string `xml:"title,attr"`
}

// ParseFeed parses Atom XML into a Feed.
func ParseFeed(data []byte) (*Feed, error) {
	var raw atomFeed
	if err := xml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse OPDS feed: %w", err)
	}
	out := &Feed{
		Title:   strings.TrimSpace(raw.Title),
		Entries: make([]Entry, 0, len(raw.Entries)),
	}
	for _, l := range raw.Links {
		if linkIsNext(l.Rel) {
			out.NextURL = strings.TrimSpace(l.Href)
		}
	}
	for _, ae := range raw.Entries {
		out.Entries = append(out.Entries, parseEntry(ae))
	}
	return out, nil
}

func parseEntry(ae atomEntry) Entry {
	e := Entry{
		ID:      strings.TrimSpace(ae.ID),
		Title:   strings.TrimSpace(ae.Title),
		Summary: strings.TrimSpace(ae.Summary),
	}
	if e.Summary == "" {
		e.Summary = strings.TrimSpace(ae.Content.Body)
	}
	for _, a := range ae.Authors {
		n := strings.TrimSpace(a.Name)
		if n != "" {
			e.Authors = append(e.Authors, n)
		}
	}
	for _, l := range ae.Links {
		rel := strings.TrimSpace(l.Rel)
		href := strings.TrimSpace(l.Href)
		typ := strings.TrimSpace(l.Type)
		if href == "" {
			continue
		}
		switch {
		case rel == RelImage || strings.HasSuffix(rel, "/image"):
			e.CoverURL = href
		case rel == RelThumbnail || strings.Contains(rel, "thumbnail"):
			e.ThumbnailURL = href
		case isAcquisitionRel(rel):
			e.Acquisitions = append(e.Acquisitions, Acquisition{
				Href:   href,
				Type:   typ,
				Length: parseLength(l.Length),
				Title:  strings.TrimSpace(l.Title),
			})
		case rel == RelSubsection || isNavCatalogType(typ):
			e.IsNavigation = true
			if e.NavURL == "" {
				e.NavURL = href
			}
		case rel == "" && isNavCatalogType(typ):
			e.IsNavigation = true
			if e.NavURL == "" {
				e.NavURL = href
			}
		}
	}
	// Entry with only nav and no acquisitions is navigation.
	if len(e.Acquisitions) == 0 && e.NavURL != "" {
		e.IsNavigation = true
	}
	if len(e.Acquisitions) > 0 {
		e.IsNavigation = false
	}
	return e
}

func isAcquisitionRel(rel string) bool {
	rel = strings.ToLower(rel)
	return rel == RelAcquisition ||
		strings.Contains(rel, "opds-spec.org/acquisition") ||
		rel == "http://opds-spec.org/acquisition/open-access"
}

func isNavCatalogType(typ string) bool {
	t := strings.ToLower(typ)
	return strings.Contains(t, "opds-catalog") ||
		strings.Contains(t, "application/atom+xml")
}

func linkIsNext(rel string) bool {
	return strings.EqualFold(strings.TrimSpace(rel), RelNext)
}

func parseLength(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	var n int64
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			break
		}
		n = n*10 + int64(ch-'0')
	}
	return n
}
