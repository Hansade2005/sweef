package engine

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A path-only URL (how snapshots show same-origin links) navigates relative to the current page.
func TestNavigatePathRelative(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, "<title>%s</title>", r.URL.Path)
	}))
	defer srv.Close()
	tab := newTabForLoad(t)
	if err := tab.Navigate(srv.URL+"/a/b", nil); err != nil {
		t.Fatal(err)
	}
	if err := tab.Navigate("/c?x=1", nil); err != nil {
		t.Fatal(err)
	}
	if got := tab.URL(); got != srv.URL+"/c?x=1" {
		t.Fatalf("url %s", got)
	}
}
