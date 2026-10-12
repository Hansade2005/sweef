package state

import (
	"encoding/json"
	"math/rand"
	"reflect"
	"testing"
)

func newModel() (*Model, int) {
	m := New()
	return m, m.AddWindow(1280, 800, 1, false)
}

func TestTabLifecycleAndOrdering(t *testing.T) {
	m, w := newModel()
	m.AddTab(w, Tab{ID: "a", URL: "https://a.test/"}, -1, true)
	m.AddTab(w, Tab{ID: "b", URL: "https://b.test/"}, -1, false)
	m.AddTab(w, Tab{ID: "c", URL: "https://c.test/"}, -1, true)
	if got := m.Order(w); !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Fatalf("order %v", got)
	}
	if m.Active(w) != "c" {
		t.Fatalf("active %s", m.Active(w))
	}
	// pinning moves a tab to the front, keeping relative order of pinned tabs
	m.Update("c", func(x *Tab) { x.Pinned = true })
	m.Update("b", func(x *Tab) { x.Pinned = true })
	if got := m.Order(w); !reflect.DeepEqual(got, []string{"c", "b", "a"}) {
		t.Fatalf("pinned order %v", got)
	}
	// an unpinned tab cannot be moved in front of pinned ones
	m.Move("a", 0)
	if got := m.Order(w); got[0] == "a" {
		t.Fatalf("unpinned tab jumped the pinned ones: %v", got)
	}
	// closing the active tab activates its neighbour
	next, ok := m.CloseTab("c")
	if !ok || next == "" || next == "c" {
		t.Fatalf("next=%q", next)
	}
	if m.Active(w) != next {
		t.Fatal("active not updated")
	}
	if m.TabCount() != 2 {
		t.Fatal("count")
	}
}

func TestClosedStack(t *testing.T) {
	m, w := newModel()
	m.AddTab(w, Tab{ID: "a", URL: "https://a.test/", Title: "A"}, -1, true)
	m.AddTab(w, Tab{ID: "b", URL: "https://b.test/", Title: "B"}, -1, true)
	m.Navigated("b", "https://b.test/2", false)
	m.CloseTab("b")
	m.CloseTab("a")
	c, ok := m.PopClosed()
	if !ok || c.URL != "https://a.test/" {
		t.Fatalf("pop: %+v", c)
	}
	c, ok = m.PopClosed()
	if !ok || c.URL != "https://b.test/2" || len(c.History) != 2 || c.HIdx != 1 {
		t.Fatalf("pop2: %+v", c)
	}
	if _, ok := m.PopClosed(); ok {
		t.Fatal("stack should be empty")
	}
	// incognito tabs leave no trace
	m.AddTab(w, Tab{ID: "i", URL: "https://secret.test/", Incognito: true}, -1, true)
	m.CloseTab("i")
	if m.ClosedCount() != 0 {
		t.Fatal("incognito tab was pushed on the reopen stack")
	}
}

func TestClosedStackBounded(t *testing.T) {
	m, w := newModel()
	for i := 0; i < 60; i++ {
		id := string(rune('A'+i%26)) + string(rune('a'+i/26))
		m.AddTab(w, Tab{ID: id, URL: "https://x.test/" + id}, -1, true)
		m.CloseTab(id)
	}
	if m.ClosedCount() != maxClosed {
		t.Fatalf("closed %d want %d", m.ClosedCount(), maxClosed)
	}
}

func TestHistoryTraversal(t *testing.T) {
	m, w := newModel()
	m.AddTab(w, Tab{ID: "a", URL: "https://a.test/1"}, -1, true)
	m.Navigated("a", "https://a.test/2", false)
	m.Navigated("a", "https://a.test/3", false)
	tab, _ := m.Tab("a")
	if !tab.CanBack || tab.CanForward {
		t.Fatalf("flags %+v", tab)
	}
	u, ok := m.Traverse("a", -1)
	if !ok || u != "https://a.test/2" {
		t.Fatalf("back -> %q", u)
	}
	tab, _ = m.Tab("a")
	if !tab.CanBack || !tab.CanForward {
		t.Fatalf("flags after back %+v", tab)
	}
	// navigating from the middle drops the forward entries
	m.Navigated("a", "https://a.test/9", false)
	tab, _ = m.Tab("a")
	if len(tab.History) != 3 || tab.History[2] != "https://a.test/9" || tab.CanForward {
		t.Fatalf("history %v forward=%v", tab.History, tab.CanForward)
	}
	// reload/redirect replaces
	m.Navigated("a", "https://a.test/10", true)
	tab, _ = m.Tab("a")
	if len(tab.History) != 3 || tab.History[2] != "https://a.test/10" {
		t.Fatalf("replace: %v", tab.History)
	}
	if _, ok := m.Traverse("a", +1); ok {
		t.Fatal("forward past the end must fail")
	}
	if _, ok := m.Traverse("a", -5); ok {
		t.Fatal("back past the start must fail")
	}
}

func TestSnapshotShape(t *testing.T) {
	m, w := newModel()
	m.AddTab(w, Tab{ID: "a", URL: "https://a.test/", Title: "A", Security: Security{Level: "secure", HTTPS: true, CertValid: true}}, -1, true)
	m.Bookmarked = func(u string) bool { return u == "https://a.test/" }
	snap := Normalize(m.Snapshot(w))
	for _, k := range []string{"window", "tabs", "active", "url", "title", "loading", "canBack", "canForward", "security", "bookmarked", "incognito", "theme", "sidebar", "settings"} {
		if _, ok := snap[k]; !ok {
			t.Errorf("snapshot lacks %q", k)
		}
	}
	if snap["bookmarked"] != true || snap["url"] != "https://a.test/" {
		t.Fatalf("active mirrors wrong: %v", snap)
	}
	sec := snap["security"].(map[string]any)
	if sec["level"] != "secure" || sec["certValid"] != true {
		t.Fatalf("security %v", sec)
	}
	if _, err := json.Marshal(snap); err != nil {
		t.Fatal(err)
	}
}

// RFC 7386 appendix A examples.
func TestMergePatchRFCExamples(t *testing.T) {
	cases := []struct{ target, patch, want string }{
		{`{"a":"b"}`, `{"a":"c"}`, `{"a":"c"}`},
		{`{"a":"b"}`, `{"b":"c"}`, `{"a":"b","b":"c"}`},
		{`{"a":"b"}`, `{"a":null}`, `{}`},
		{`{"a":"b","b":"c"}`, `{"a":null}`, `{"b":"c"}`},
		{`{"a":["b"]}`, `{"a":"c"}`, `{"a":"c"}`},
		{`{"a":"c"}`, `{"a":["b"]}`, `{"a":["b"]}`},
		{`{"a":{"b":"c"}}`, `{"a":{"b":"d","c":null}}`, `{"a":{"b":"d"}}`},
		{`{"a":[{"b":"c"}]}`, `{"a":[1]}`, `{"a":[1]}`},
		{`{"e":null}`, `{"a":1}`, `{"e":null,"a":1}`},
		{`{}`, `{"a":{"bb":{"ccc":null}}}`, `{"a":{"bb":{}}}`},
	}
	for _, c := range cases {
		var tg, pt, want map[string]any
		json.Unmarshal([]byte(c.target), &tg)
		json.Unmarshal([]byte(c.patch), &pt)
		json.Unmarshal([]byte(c.want), &want)
		got := Apply(tg, pt)
		// RFC examples keep an explicit null already present in the target
		if !reflect.DeepEqual(got, want) {
			t.Errorf("apply(%s, %s) = %v, want %v", c.target, c.patch, got, want)
		}
	}
}

func randJSON(r *rand.Rand, depth int) map[string]any {
	out := map[string]any{}
	n := r.Intn(5)
	for i := 0; i < n; i++ {
		k := string(rune('a' + r.Intn(6)))
		switch r.Intn(5) {
		case 0:
			out[k] = r.Intn(10)
		case 1:
			out[k] = r.Intn(2) == 0
		case 2:
			out[k] = string(rune('p' + r.Intn(5)))
		case 3:
			out[k] = []any{r.Intn(3), r.Intn(3)}
		case 4:
			if depth < 3 {
				out[k] = randJSON(r, depth+1)
			}
		}
	}
	return out
}

// Property: Apply(old, Diff(old, new)) == new for random documents.
func TestDiffApplyRoundTrip(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	for i := 0; i < 2000; i++ {
		a := Normalize(randJSON(r, 0))
		b := Normalize(randJSON(r, 0))
		p := Diff(a, b)
		got := Apply(Normalize(a), p)
		if !reflect.DeepEqual(got, b) {
			t.Fatalf("iteration %d\nold   %v\nnew   %v\npatch %v\ngot   %v", i, a, b, p, got)
		}
		if reflect.DeepEqual(a, b) && p != nil {
			t.Fatalf("equal documents produced a patch: %v", p)
		}
	}
}

func TestPatchIsMinimal(t *testing.T) {
	m, w := newModel()
	m.AddTab(w, Tab{ID: "a", URL: "https://a.test/"}, -1, true)
	old := Normalize(m.Snapshot(w))
	m.Update("a", func(x *Tab) { x.Loading = true })
	nw := Normalize(m.Snapshot(w))
	p := Diff(old, nw)
	b, _ := json.Marshal(p)
	if len(b) > 400 {
		t.Fatalf("patch for one flag is %d bytes: %s", len(b), b)
	}
	if _, ok := p["window"]; ok {
		t.Fatal("patch repeats unchanged window")
	}
}
