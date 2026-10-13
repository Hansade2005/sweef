package engine

import (
	"testing"
	"time"
)

// TestServiceWorkerStartupTimes logs how long a registration takes to become active and how long a persisted worker needs
// for its first answer after a restart (go test -run StartupTimes -v).
func TestServiceWorkerStartupTimes(t *testing.T) {
	if testing.Short() {
		t.Skip("timing measurement")
	}
	srv := newSWServer(t, pagesSW)
	setupPagesSW(srv)
	dir := t.TempDir()
	mk := func() *Engine { o := DefaultOptions(); o.StorageDir = dir; return New(o) }
	e1 := mk()
	tab := openTab(t, e1, srv.URL+"/")
	t0 := time.Now()
	js(t, tab, swHelpers+`await regReady('/sw.js'); return 1;`)
	reg := time.Since(t0)
	e1.Close()
	e2 := mk()
	defer e2.Close()
	tab2 := openTab(t, e2, "about:blank")
	t0 = time.Now()
	if err := tab2.Navigate(srv.URL+"/page2.html", nil); err != nil {
		t.Fatal(err)
	}
	first := time.Since(t0)
	t0 = time.Now()
	if err := tab2.Navigate(srv.URL+"/page2.html", nil); err != nil {
		t.Fatal(err)
	}
	second := time.Since(t0)
	t.Logf("register to activated (script + 3 URLs cached): %v; first navigation after restart (worker VM starts): %v; second: %v", reg, first, second)
}
