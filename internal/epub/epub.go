package epub

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"io"
	"path"
	"strings"
)

// Book is a parsed EPUB.
type Book struct {
	Title    string
	Language string
	Spine    []SpineItem
	// raw zip for resource reads
	files map[string][]byte
	opfDir string
}

// SpineItem is one reading document in order.
type SpineItem struct {
	ID       string `json:"id"`
	Href     string `json:"href"`     // path inside epub (posix)
	MediaType string `json:"mediaType"`
	Label    string `json:"label"`
}

// Chapter is HTML content ready for the reader.
type Chapter struct {
	Index   int    `json:"index"`
	Label   string `json:"label"`
	HTML    string `json:"html"`
	BaseDir string `json:"baseDir"` // for resolving relative assets (unused if inlined)
}

// Open reads an EPUB from disk.
func Open(filePath string) (*Book, error) {
	r, err := zip.OpenReader(filePath)
	if err != nil {
		return nil, fmt.Errorf("open epub: %w", err)
	}
	defer r.Close()

	files := make(map[string][]byte)
	for _, f := range r.File {
		name := path.Clean(strings.ReplaceAll(f.Name, "\\", "/"))
		rc, err := f.Open()
		if err != nil {
			continue
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			continue
		}
		files[name] = data
		// also store lower-case key for lookups
		files[strings.ToLower(name)] = data
	}

	container, ok := readFile(files, "META-INF/container.xml")
	if !ok {
		return nil, fmt.Errorf("missing META-INF/container.xml")
	}

	opfPath, err := parseRootfile(container)
	if err != nil {
		return nil, err
	}
	opfPath = path.Clean(opfPath)
	opfDir := path.Dir(opfPath)
	if opfDir == "." {
		opfDir = ""
	}

	opfData, ok := readFile(files, opfPath)
	if !ok {
		return nil, fmt.Errorf("missing OPF: %s", opfPath)
	}

	meta, manifest, spineIDs, err := parseOPF(opfData)
	if err != nil {
		return nil, err
	}

	book := &Book{
		Title:    meta.Title,
		Language: meta.Language,
		files:    files,
		opfDir:   opfDir,
	}

	for _, id := range spineIDs {
		item, ok := manifest[id]
		if !ok {
			continue
		}
		href := item.Href
		if opfDir != "" && !strings.HasPrefix(href, "/") {
			href = path.Join(opfDir, href)
		}
		href = path.Clean(href)
		book.Spine = append(book.Spine, SpineItem{
			ID:        id,
			Href:      href,
			MediaType: item.MediaType,
			Label:     "",
		})
	}

	if book.Title == "" {
		book.Title = "Untitled"
	}

	// Prefer real TOC labels from nav.xhtml / toc.ncx over bare filenames
	labels := collectTOCLabels(files, opfDir, manifest)
	for i := range book.Spine {
		s := &book.Spine[i]
		if lab := lookupLabel(labels, s.Href); lab != "" && !sameTitle(lab, book.Title) {
			s.Label = lab
			continue
		}
		// Heading inside chapter (skip if it is just the book title)
		if raw, ok := readFile(files, s.Href); ok {
			if h := firstHeading(string(raw)); h != "" && !sameTitle(h, book.Title) {
				s.Label = h
				continue
			}
		}
		base := strings.TrimSuffix(path.Base(s.Href), path.Ext(s.Href))
		base = strings.ReplaceAll(base, "_", " ")
		base = strings.ReplaceAll(base, "-", " ")
		if base != "" && !sameTitle(base, book.Title) {
			s.Label = strings.TrimSpace(base)
		} else {
			s.Label = fmt.Sprintf("Chapter %d", i+1)
		}
	}

	return book, nil
}

// ChapterCount returns spine length.
func (b *Book) ChapterCount() int {
	return len(b.Spine)
}

// TOCItem is a chapter entry for the chapter list UI.
type TOCItem struct {
	Index int    `json:"index"`
	Label string `json:"label"`
	Href  string `json:"href"`
}

// TOC returns spine items with friendly labels.
func (b *Book) TOC() []TOCItem {
	out := make([]TOCItem, 0, len(b.Spine))
	for i, s := range b.Spine {
		label := strings.TrimSpace(s.Label)
		if label == "" || sameTitle(label, b.Title) {
			label = fmt.Sprintf("Chapter %d", i+1)
		}
		out = append(out, TOCItem{Index: i, Label: label, Href: s.Href})
	}
	return out
}

func sameTitle(a, b string) bool {
	na := strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(a))), " ")
	nb := strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(b))), " ")
	return na != "" && na == nb
}

// collectTOCLabels maps cleaned chapter href → label from NCX / nav doc.
func collectTOCLabels(files map[string][]byte, opfDir string, manifest map[string]manifestItem) map[string]string {
	out := make(map[string]string)

	// EPUB3 nav document
	for _, it := range manifest {
		mt := strings.ToLower(it.MediaType)
		props := strings.ToLower(it.Properties)
		if strings.Contains(props, "nav") || mt == "application/xhtml+xml" && strings.Contains(strings.ToLower(it.Href), "nav") {
			href := resolveOPFHref(opfDir, it.Href)
			if data, ok := readFile(files, href); ok {
				mergeLabels(out, parseNavHTML(string(data), path.Dir(href)))
			}
		}
		if mt == "application/x-dtbncx+xml" || strings.HasSuffix(strings.ToLower(it.Href), ".ncx") {
			href := resolveOPFHref(opfDir, it.Href)
			if data, ok := readFile(files, href); ok {
				mergeLabels(out, parseNCX(data, path.Dir(href)))
			}
		}
	}

	// Fallback common paths
	for _, cand := range []string{
		path.Join(opfDir, "toc.ncx"),
		path.Join(opfDir, "nav.xhtml"),
		path.Join(opfDir, "nav.html"),
		"toc.ncx",
		"OEBPS/toc.ncx",
		"OEBPS/nav.xhtml",
	} {
		if data, ok := readFile(files, path.Clean(cand)); ok {
			if strings.HasSuffix(strings.ToLower(cand), ".ncx") {
				mergeLabels(out, parseNCX(data, path.Dir(path.Clean(cand))))
			} else {
				mergeLabels(out, parseNavHTML(string(data), path.Dir(path.Clean(cand))))
			}
		}
	}
	return out
}

func resolveOPFHref(opfDir, href string) string {
	href = path.Clean(strings.ReplaceAll(href, "\\", "/"))
	if opfDir != "" && !strings.HasPrefix(href, "/") {
		return path.Clean(path.Join(opfDir, href))
	}
	return href
}

func mergeLabels(dst, src map[string]string) {
	for k, v := range src {
		if v == "" {
			continue
		}
		if _, exists := dst[k]; !exists {
			dst[k] = v
		}
		// also basename key
		dst[path.Base(k)] = v
	}
}

func lookupLabel(labels map[string]string, href string) string {
	if labels == nil {
		return ""
	}
	href = path.Clean(href)
	if lab, ok := labels[href]; ok {
		return lab
	}
	// strip fragment keys already cleaned
	if lab, ok := labels[path.Base(href)]; ok {
		return lab
	}
	// try without directory mismatch
	for k, v := range labels {
		if path.Base(k) == path.Base(href) {
			return v
		}
	}
	return ""
}

func parseNCX(data []byte, baseDir string) map[string]string {
	out := make(map[string]string)
	// Token walk — NCX namespaces vary
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = false
	var (
		inLabel bool
		inText  bool
		label   string
	)
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			local := strings.ToLower(t.Name.Local)
			switch local {
			case "navlabel":
				inLabel = true
				label = ""
			case "text":
				if inLabel {
					inText = true
				}
			case "content":
				src := ""
				for _, a := range t.Attr {
					if strings.EqualFold(a.Name.Local, "src") {
						src = a.Value
						break
					}
				}
				if src != "" && label != "" {
					src = strings.Split(src, "#")[0]
					full := path.Clean(path.Join(baseDir, src))
					out[full] = strings.TrimSpace(label)
					out[path.Base(full)] = strings.TrimSpace(label)
				}
			}
		case xml.EndElement:
			local := strings.ToLower(t.Name.Local)
			if local == "text" {
				inText = false
			}
			if local == "navlabel" {
				inLabel = false
			}
		case xml.CharData:
			if inText {
				label += string(t)
			}
		}
	}
	return out
}

func parseNavHTML(html, baseDir string) map[string]string {
	out := make(map[string]string)
	// Find toc nav block if present
	lower := strings.ToLower(html)
	segment := html
	if i := strings.Index(lower, `epub:type="toc"`); i >= 0 {
		// from this nav tag
		start := strings.LastIndex(lower[:i], "<nav")
		if start >= 0 {
			end := strings.Index(lower[i:], "</nav>")
			if end >= 0 {
				segment = html[start : i+end+6]
			}
		}
	} else if i := strings.Index(lower, `epub:type='toc'`); i >= 0 {
		start := strings.LastIndex(lower[:i], "<nav")
		if start >= 0 {
			end := strings.Index(lower[i:], "</nav>")
			if end >= 0 {
				segment = html[start : i+end+6]
			}
		}
	}

	// Extract <a href="...">label</a>
	rest := segment
	for {
		low := strings.ToLower(rest)
		a := strings.Index(low, "<a ")
		if a < 0 {
			a = strings.Index(low, "<a>")
		}
		if a < 0 {
			break
		}
		rest = rest[a:]
		gt := strings.Index(rest, ">")
		if gt < 0 {
			break
		}
		openTag := rest[:gt+1]
		rest = rest[gt+1:]
		end := strings.Index(strings.ToLower(rest), "</a>")
		if end < 0 {
			break
		}
		label := strings.TrimSpace(stripTags(rest[:end]))
		rest = rest[end+4:]
		href := attrValue(openTag, "href")
		if href == "" || label == "" {
			continue
		}
		if strings.HasPrefix(strings.ToLower(href), "http") {
			continue
		}
		href = strings.Split(href, "#")[0]
		if href == "" {
			continue
		}
		full := path.Clean(path.Join(baseDir, href))
		out[full] = label
		out[path.Base(full)] = label
	}
	return out
}

func attrValue(tag, name string) string {
	low := strings.ToLower(tag)
	key := strings.ToLower(name) + "="
	i := strings.Index(low, key)
	if i < 0 {
		return ""
	}
	rest := tag[i+len(key):]
	if rest == "" {
		return ""
	}
	q := rest[0]
	if q == '"' || q == '\'' {
		rest = rest[1:]
		j := strings.IndexByte(rest, q)
		if j < 0 {
			return ""
		}
		return rest[:j]
	}
	// unquoted
	j := strings.IndexAny(rest, " \t>")
	if j < 0 {
		return rest
	}
	return rest[:j]
}

// ResolveHref maps an internal relative href to a spine index and optional fragment.
func (b *Book) ResolveHref(fromChapter int, href string) (spineIndex int, fragment string, ok bool) {
	href = strings.TrimSpace(href)
	if href == "" {
		return 0, "", false
	}
	if strings.HasPrefix(href, "#") {
		return fromChapter, strings.TrimPrefix(href, "#"), true
	}
	// strip fragment
	frag := ""
	if i := strings.IndexByte(href, '#'); i >= 0 {
		frag = href[i+1:]
		href = href[:i]
	}
	base := ""
	if fromChapter >= 0 && fromChapter < len(b.Spine) {
		base = path.Dir(b.Spine[fromChapter].Href)
	}
	target := path.Clean(path.Join(base, href))
	target = strings.TrimPrefix(target, "/")
	for i, s := range b.Spine {
		if s.Href == target || strings.EqualFold(s.Href, target) {
			return i, frag, true
		}
		// match by basename
		if path.Base(s.Href) == path.Base(target) {
			return i, frag, true
		}
	}
	return 0, "", false
}

func firstHeading(xhtml string) string {
	lower := strings.ToLower(xhtml)
	// Prefer body headings; <title> is often the book name on every file
	for _, tag := range []string{"h1", "h2", "h3"} {
		open := "<" + tag
		i := strings.Index(lower, open)
		if i < 0 {
			continue
		}
		gt := strings.Index(xhtml[i:], ">")
		if gt < 0 {
			continue
		}
		start := i + gt + 1
		close := strings.Index(lower[start:], "</"+tag)
		if close < 0 {
			continue
		}
		text := stripTags(xhtml[start : start+close])
		text = strings.Join(strings.Fields(text), " ")
		if len(text) > 80 {
			text = text[:80] + "…"
		}
		if text != "" {
			return text
		}
	}
	return ""
}

func stripTags(s string) string {
	var b strings.Builder
	in := false
	for _, r := range s {
		switch {
		case r == '<':
			in = true
		case r == '>':
			in = false
		case !in:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// GetChapter returns reflowable HTML for spine index (0-based).
func (b *Book) GetChapter(index int) (*Chapter, error) {
	if index < 0 || index >= len(b.Spine) {
		return nil, fmt.Errorf("chapter out of range")
	}
	item := b.Spine[index]
	raw, ok := readFile(b.files, item.Href)
	if !ok {
		return nil, fmt.Errorf("missing chapter file: %s", item.Href)
	}

	html := extractBody(string(raw))
	html = rewriteImageSources(html, path.Dir(item.Href), b.files)
	label := item.Label
	if label == "" || sameTitle(label, b.Title) {
		if h := firstHeading(string(raw)); h != "" && !sameTitle(h, b.Title) {
			label = h
		} else {
			label = fmt.Sprintf("Chapter %d", index+1)
		}
	}

	return &Chapter{
		Index:   index,
		Label:   label,
		HTML:    html,
		BaseDir: path.Dir(item.Href),
	}, nil
}

// ResourceDataURL returns a data URL for an internal path (images, css).
func (b *Book) ResourceDataURL(href string) (string, bool) {
	data, ok := readFile(b.files, path.Clean(href))
	if !ok {
		return "", false
	}
	mt := mimeFromPath(href)
	return "data:" + mt + ";base64," + b64(data), true
}

// --- OPF / container parsing ---

type metaInfo struct {
	Title    string
	Language string
}

type manifestItem struct {
	ID         string
	Href       string
	MediaType  string
	Properties string
}

func parseRootfile(containerXML []byte) (string, error) {
	type rootfile struct {
		FullPath string `xml:"full-path,attr"`
	}
	type container struct {
		Rootfiles []rootfile `xml:"rootfiles>rootfile"`
	}
	var c container
	if err := xml.Unmarshal(containerXML, &c); err != nil {
		return "", err
	}
	if len(c.Rootfiles) == 0 {
		return "", fmt.Errorf("no rootfile in container")
	}
	return c.Rootfiles[0].FullPath, nil
}

func parseOPF(data []byte) (metaInfo, map[string]manifestItem, []string, error) {
	// Loose struct covering EPUB2/3 package
	type item struct {
		ID         string `xml:"id,attr"`
		Href       string `xml:"href,attr"`
		MediaType  string `xml:"media-type,attr"`
		Properties string `xml:"properties,attr"`
	}
	type itemref struct {
		IDRef string `xml:"idref,attr"`
	}
	type meta struct {
		Name    string `xml:"name,attr"`
		Content string `xml:"content,attr"`
	}
	type packageDoc struct {
		Metadata struct {
			Title    []string `xml:"title"`
			Language []string `xml:"language"`
			// Dublin Core with namespace variants
			DCTitle []string `xml:"http://purl.org/dc/elements/1.1/ title"`
			DCLang  []string `xml:"http://purl.org/dc/elements/1.1/ language"`
		} `xml:"metadata"`
		Manifest struct {
			Items []item `xml:"item"`
		} `xml:"manifest"`
		Spine struct {
			Itemrefs []itemref `xml:"itemref"`
		} `xml:"spine"`
	}

	// encoding/xml is picky about namespaces; normalize common prefixes
	normalized := bytes.ReplaceAll(data, []byte("dc:title"), []byte("title"))
	normalized = bytes.ReplaceAll(normalized, []byte("dc:language"), []byte("language"))
	normalized = bytes.ReplaceAll(normalized, []byte("opf:"), []byte(""))

	var pkg packageDoc
	dec := xml.NewDecoder(bytes.NewReader(normalized))
	dec.Strict = false
	if err := dec.Decode(&pkg); err != nil {
		// fallback: try raw
		if err2 := xml.Unmarshal(data, &pkg); err2 != nil {
			return metaInfo{}, nil, nil, fmt.Errorf("parse opf: %v", err)
		}
	}

	m := metaInfo{}
	if len(pkg.Metadata.Title) > 0 {
		m.Title = strings.TrimSpace(pkg.Metadata.Title[0])
	}
	if m.Title == "" && len(pkg.Metadata.DCTitle) > 0 {
		m.Title = strings.TrimSpace(pkg.Metadata.DCTitle[0])
	}
	if len(pkg.Metadata.Language) > 0 {
		m.Language = pkg.Metadata.Language[0]
	}

	// Secondary pass for dc elements with namespaces using token walk
	if m.Title == "" || m.Language == "" {
		t, l := extractDC(data)
		if m.Title == "" {
			m.Title = t
		}
		if m.Language == "" {
			m.Language = l
		}
	}

	manifest := make(map[string]manifestItem)
	for _, it := range pkg.Manifest.Items {
		manifest[it.ID] = manifestItem{
			ID: it.ID, Href: it.Href, MediaType: it.MediaType, Properties: it.Properties,
		}
	}
	var spine []string
	for _, ir := range pkg.Spine.Itemrefs {
		spine = append(spine, ir.IDRef)
	}
	return m, manifest, spine, nil
}

func extractDC(data []byte) (title, lang string) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = false
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		local := strings.ToLower(se.Name.Local)
		if local == "title" && title == "" {
			var t string
			_ = dec.DecodeElement(&t, &se)
			title = strings.TrimSpace(t)
		}
		if local == "language" && lang == "" {
			var t string
			_ = dec.DecodeElement(&t, &se)
			lang = strings.TrimSpace(t)
		}
	}
	return title, lang
}

func extractBody(xhtml string) string {
	lower := strings.ToLower(xhtml)
	start := strings.Index(lower, "<body")
	if start < 0 {
		// strip xml declaration / html shell lightly
		return sanitizeFragment(xhtml)
	}
	gt := strings.Index(xhtml[start:], ">")
	if gt < 0 {
		return sanitizeFragment(xhtml)
	}
	start = start + gt + 1
	end := strings.LastIndex(lower, "</body>")
	if end < start {
		return sanitizeFragment(xhtml[start:])
	}
	return sanitizeFragment(xhtml[start:end])
}

func sanitizeFragment(s string) string {
	// Remove scripts
	for {
		l := strings.ToLower(s)
		a := strings.Index(l, "<script")
		if a < 0 {
			break
		}
		b := strings.Index(l[a:], "</script>")
		if b < 0 {
			s = s[:a]
			break
		}
		s = s[:a] + s[a+b+9:]
	}
	return strings.TrimSpace(s)
}

func rewriteImageSources(html, base string, files map[string][]byte) string {
	// naive src="..." rewrite for relative images
	var out strings.Builder
	rest := html
	for {
		i := strings.Index(strings.ToLower(rest), "src=")
		if i < 0 {
			out.WriteString(rest)
			break
		}
		out.WriteString(rest[:i+4])
		rest = rest[i+4:]
		if len(rest) == 0 {
			break
		}
		quote := rest[0]
		if quote != '"' && quote != '\'' {
			continue
		}
		rest = rest[1:]
		j := strings.IndexByte(rest, quote)
		if j < 0 {
			out.WriteByte(quote)
			out.WriteString(rest)
			break
		}
		src := rest[:j]
		rest = rest[j:] // includes closing quote
		if strings.HasPrefix(src, "data:") || strings.HasPrefix(src, "http://") || strings.HasPrefix(src, "https://") {
			out.WriteByte(quote)
			out.WriteString(src)
			continue
		}
		full := path.Clean(path.Join(base, src))
		if data, ok := readFile(files, full); ok {
			out.WriteByte(quote)
			out.WriteString("data:")
			out.WriteString(mimeFromPath(full))
			out.WriteString(";base64,")
			out.WriteString(b64(data))
		} else {
			out.WriteByte(quote)
			out.WriteString(src)
		}
	}
	return out.String()
}

func readFile(files map[string][]byte, p string) ([]byte, bool) {
	p = path.Clean(strings.ReplaceAll(p, "\\", "/"))
	p = strings.TrimPrefix(p, "/")
	if d, ok := files[p]; ok {
		return d, true
	}
	if d, ok := files[strings.ToLower(p)]; ok {
		return d, true
	}
	return nil, false
}

func mimeFromPath(p string) string {
	switch strings.ToLower(path.Ext(p)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".svg":
		return "image/svg+xml"
	case ".webp":
		return "image/webp"
	case ".css":
		return "text/css"
	default:
		return "application/octet-stream"
	}
}

func b64(data []byte) string {
	return base64.StdEncoding.EncodeToString(data)
}
