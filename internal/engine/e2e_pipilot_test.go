package engine

import (
	"strings"
	"testing"
)

func TestScreenshotPipilotE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("network test")
	}
	e := New(DefaultOptions())
	defer e.Close()
	tab, err := e.NewTab("https://pipilot.dev")
	if tab == nil {
		t.Fatalf("no tab: %v", err)
	}
	shot := tab.Screenshot(80)
	if strings.TrimSpace(shot) == "" {
		t.Fatalf("empty screenshot; nav err=%v url=%q htmlLen=%d", err, tab.URL(), len(tab.HTML(false)))
	}
	t.Logf("screenshot OK: %d lines, %d bytes", len(strings.Split(shot, "\n")), len(shot))
}
