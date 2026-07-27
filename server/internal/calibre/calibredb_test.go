package calibre

import "testing"

func TestParseAddedBookID(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"Added book ids: 42\n", 42},
		{"Added book id: 7", 7},
		{"Something\nAdded book ids: 100, 101\n", 100},
	}
	for _, c := range cases {
		got, err := parseAddedBookID(c.in)
		if err != nil {
			t.Fatalf("parse %q: %v", c.in, err)
		}
		if got != c.want {
			t.Fatalf("parse %q: got %d want %d", c.in, got, c.want)
		}
	}
}

func TestGuessTitleFormat(t *testing.T) {
	title, format := GuessTitleFormat(`/tmp/My Novel.epub`)
	if title != "My Novel" || format != "epub" {
		t.Fatalf("got %q %q", title, format)
	}
}
