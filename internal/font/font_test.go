package font

import "testing"

func TestResolveAndMetrics(t *testing.T) {
	f := Resolve([]string{"Helvetica", "sans-serif"}, Style{Weight: 400})
	if f == nil {
		t.Fatal("no face")
	}
	m := f.Metrics(16)
	if m.Ascent <= 0 || m.Descent <= 0 {
		t.Fatalf("metrics: %+v", m)
	}
	a, b := f.Advance('i', 16), f.Advance('W', 16)
	if a <= 0 || b <= a {
		t.Fatalf("advances i=%v W=%v", a, b)
	}
	if len(f.Outline('A', 16)) == 0 {
		t.Fatal("no outline for A")
	}
	bold := Resolve([]string{"sans-serif"}, Style{Weight: 700})
	if bold == nil || bold.Style.Weight < 600 {
		t.Fatalf("bold face: %+v", bold)
	}
	if mono := Resolve([]string{"monospace"}, Style{Weight: 400}); mono == nil || mono.Advance('i', 16) != mono.Advance('W', 16) {
		t.Fatal("monospace is not fixed pitch")
	}
	t.Logf("sans-serif -> %s, serif -> %s, monospace -> %s",
		Lookup("sans-serif", Style{400, false}).Family, Lookup("serif", Style{400, false}).Family, Lookup("monospace", Style{400, false}).Family)
}

func TestEmbeddedFallbackWorks(t *testing.T) {
	if Fallback('A', []string{"definitely-not-installed"}, Style{Weight: 400}) == nil {
		t.Fatal("no fallback face")
	}
}
