package ui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/sweefbrowser/sweef/internal/engine"
)

func TestProbe(t *testing.T) {
	srv := httptest.NewServer(http.FileServer(http.FS(FS)))
	defer srv.Close()
	e := engine.New(engine.Options{ViewportWidth: 900, ViewportHeight: 400, ConsoleRingSize: 100, NetworkRingSize: 100, StateRingSize: 100, CacheDir: "off"})
	defer e.Close()
	tab, err := e.NewTab(srv.URL + "/probe.html")
	if err != nil {
		t.Fatal(err)
	}
	tab.SetViewport(900, 400, 2, false, false)
	for _, ev := range tab.ConsoleLog(0) {
		t.Log(ev.Data)
	}
	os.WriteFile(os.Getenv("OUT"), tab.PNGScreenshot(900, 400, 1), 0644)
}
