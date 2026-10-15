package agent

import "testing"

func TestResolveAgainst(t *testing.T) {
	for _, c := range [][3]string{
		{"https://en.wikipedia.org/wiki/Main_Page", "/wiki/Ada_Lovelace", "https://en.wikipedia.org/wiki/Ada_Lovelace"},
		{"https://news.ycombinator.com/", "?p=2", "https://news.ycombinator.com/?p=2"},
		{"https://a.test/x", "https://b.test/y", "https://b.test/y"},
		{"https://a.test/x", "//cdn.test/z", "//cdn.test/z"},
		{"https://a.test/x", "example.com", "example.com"},
	} {
		if got := resolveAgainst(c[0], c[1]); got != c[2] {
			t.Errorf("resolveAgainst(%q, %q) = %q, want %q", c[0], c[1], got, c[2])
		}
	}
}
