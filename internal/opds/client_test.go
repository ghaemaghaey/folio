package opds

import "testing"

func TestParseFeedBookAndNext(t *testing.T) {
	const xml = `<?xml version="1.0" encoding="UTF-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <title>Books</title>
  <link rel="next" href="/opds/books/letter/00?offset=60" type="application/atom+xml"/>
  <entry>
    <title>Sample Book</title>
    <id>urn:uuid:abc</id>
    <author><name>Ada Lovelace</name></author>
    <author><name>Grace Hopper</name></author>
    <summary>A short summary</summary>
    <link rel="http://opds-spec.org/image" href="/cover/1" type="image/jpeg"/>
    <link rel="http://opds-spec.org/acquisition" href="/download/1.epub"
          type="application/epub+zip" length="12345"/>
    <link rel="http://opds-spec.org/acquisition" href="/download/1.pdf"
          type="application/pdf" length="99999"/>
  </entry>
  <entry>
    <title>Authors</title>
    <id>nav:authors</id>
    <link rel="subsection" href="/opds/authors" type="application/atom+xml;profile=opds-catalog"/>
  </entry>
</feed>`

	feed, err := ParseFeed([]byte(xml))
	if err != nil {
		t.Fatal(err)
	}
	if feed.NextURL == "" {
		t.Fatal("expected next URL")
	}
	if len(feed.Entries) != 2 {
		t.Fatalf("entries: %d", len(feed.Entries))
	}
	book := feed.Entries[0]
	if book.Title != "Sample Book" {
		t.Fatalf("title: %q", book.Title)
	}
	if len(book.Authors) != 2 {
		t.Fatalf("authors: %#v", book.Authors)
	}
	if book.CoverURL == "" {
		t.Fatal("cover")
	}
	if len(book.Acquisitions) != 2 {
		t.Fatalf("acq: %d", len(book.Acquisitions))
	}
	pref, ok := book.PreferredAcquisition()
	if !ok || pref.FormatLabel() != "epub" {
		t.Fatalf("preferred: %#v", pref)
	}
	if !feed.Entries[1].IsNavigation {
		t.Fatal("expected navigation entry")
	}
}
