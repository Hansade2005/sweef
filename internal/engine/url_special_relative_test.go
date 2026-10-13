package engine

import "testing"

// "https:../x" against an https base is a relative reference. Shaka Player builds such strings (it puts the
// page's scheme in front of a relative URI) and the locale files of its UI were requested from "https://..".
func TestSpecialSchemeRelativeReferences(t *testing.T) {
	base := "https://h.example/x/demo/"
	for ref, want := range map[string]string{
		"https:../ui/a.json":      "https://h.example/x/ui/a.json",
		"https:foo/b.json":        "https://h.example/x/demo/foo/b.json",
		"https:/c.json":           "https://h.example/c.json",
		"HTTPS:d.json":            "https://h.example/x/demo/d.json",
		"https://other.example/e": "https://other.example/e",
		"data:text/plain,hi":      "data:text/plain,hi",
	} {
		got, err := resolveURL(base, ref)
		if err != nil || got != want {
			t.Errorf("resolveURL(%q, %q) = %q, %v; want %q", base, ref, got, err, want)
		}
	}

	e := New(DefaultOptions())
	defer e.Close()
	tab, err := e.NewTab("about:blank")
	if err != nil {
		t.Fatal(err)
	}
	var r []string
	evalJSON(t, tab, `["https:../ui/a.json", "https:foo/b.json", "https:/c.json", "HTTPS:d.json", "https://other.example/e", "http:f"].map(function (u) { return new URL(u, "https://h.example/x/demo/").href; })`, &r)
	want := []string{"https://h.example/x/ui/a.json", "https://h.example/x/demo/foo/b.json", "https://h.example/c.json", "https://h.example/x/demo/d.json", "https://other.example/e", "http://f/"}
	for i := range want {
		if i >= len(r) || r[i] != want[i] {
			t.Errorf("new URL case %d: got %v, want %v", i, r, want)
			break
		}
	}
}
