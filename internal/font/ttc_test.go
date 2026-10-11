package font

import (
	"os"
	"testing"
)

func TestTTCWenQuanYi(t *testing.T) {
	const p = "/usr/share/fonts/truetype/wqy/wqy-zenhei.ttc"
	if _, err := os.Stat(p); err != nil {
		t.Skip("no wenquanyi")
	}
	f := Lookup("wenquanyi zen hei", Style{Weight: 400})
	if f == nil {
		t.Fatal("wenquanyi zen hei not found")
	}
	if !f.HasGlyph('中') || f.AdvanceF('中', 16) <= 0 {
		t.Fatal("CJK glyph missing")
	}
	if len(f.Outline('中', 16)) == 0 {
		t.Fatal("no outline")
	}
}
