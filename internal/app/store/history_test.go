package store

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

func urls(es []HistoryEntry) []string {
	var out []string
	for _, e := range es {
		out = append(out, e.URL)
	}
	return out
}

func TestHistoryRankingTiers(t *testing.T) {
	h := OpenHistory(NewMemBackend())
	now := time.Now()
	h.VisitAt("https://news.example.com/world", "World news", false, now)
	h.VisitAt("https://example.com/", "Example Domain", false, now)
	h.VisitAt("https://other.org/about-example", "About the example", false, now)
	h.VisitAt("https://foo.net/x", "Something with an EXAMPLE inside", false, now)
	got := urls(h.Search("example", 10))
	// host prefix first, then word/substring matches
	if got[0] != "https://example.com/" {
		t.Fatalf("host-prefix match must rank first: %v", got)
	}
	if len(got) != 4 {
		t.Fatalf("want 4 matches, got %v", got)
	}
	// multi-term: all terms must match
	got = urls(h.Search("example world", 10))
	if len(got) != 1 || got[0] != "https://news.example.com/world" {
		t.Fatalf("multi-term: %v", got)
	}
	if len(h.Search("zzzzqq", 10)) != 0 {
		t.Fatal("no match expected")
	}
}

func TestHistoryFrecency(t *testing.T) {
	h := OpenHistory(NewMemBackend())
	now := time.Now()
	for i := 0; i < 20; i++ {
		h.VisitAt("https://often.test/", "often", false, now.Add(-time.Duration(i)*time.Hour))
	}
	h.VisitAt("https://once.test/", "once", false, now)
	h.VisitAt("https://ancient.test/", "ancient", false, now.AddDate(-2, 0, 0))
	for i := 0; i < 20; i++ {
		h.VisitAt("https://ancient.test/", "ancient", false, now.AddDate(-2, 0, -i))
	}
	top := urls(h.Top(3))
	if top[0] != "https://often.test/" {
		t.Fatalf("most-visited recent site should lead: %v", top)
	}
	if top[len(top)-1] != "https://ancient.test/" && top[1] != "https://once.test/" {
		t.Fatalf("old visits must decay below a fresh one: %v", top)
	}
	// typed visits weigh more than clicked ones
	h2 := OpenHistory(NewMemBackend())
	h2.VisitAt("https://typed.test/", "t", true, now)
	h2.VisitAt("https://clicked.test/", "t", false, now)
	if urls(h2.Top(1))[0] != "https://typed.test/" {
		t.Fatal("typed visit should outrank a clicked one")
	}
}

func TestHistoryPersistenceAndCompaction(t *testing.T) {
	dir := t.TempDir()
	b, _ := NewDiskBackend(dir)
	h := OpenHistory(b)
	now := time.Now().Truncate(time.Second)
	for i := 0; i < 50; i++ {
		h.VisitAt("https://a.test/", "A", i%2 == 0, now.Add(-time.Duration(i)*time.Minute))
	}
	h.VisitAt("https://b.test/x", "B page", false, now)
	h.SetTitle("https://b.test/x", "B renamed")
	h.Delete("https://gone.test/")
	want, _ := h.Get("https://a.test/")
	b.Close()

	b2, _ := NewDiskBackend(dir)
	h2 := OpenHistory(b2)
	got, ok := h2.Get("https://a.test/")
	if !ok || got.Visits != 50 || got.Typed != 25 {
		t.Fatalf("reload lost counters: %+v", got)
	}
	if d := got.Frec - want.Frec; d > 1e-6 || d < -1e-6 {
		t.Fatalf("frecency changed across reload: %v vs %v", got.Frec, want.Frec)
	}
	if e, _ := h2.Get("https://b.test/x"); e.Title != "B renamed" {
		t.Fatalf("title update not persisted: %q", e.Title)
	}
	// explicit compaction keeps everything
	h2.compact()
	b2.Close()
	b3, _ := NewDiskBackend(dir)
	h3 := OpenHistory(b3)
	g3, _ := h3.Get("https://a.test/")
	if g3.Visits != 50 || g3.Typed != 25 || h3.Len() != 2 {
		t.Fatalf("compaction lost data: %+v len=%d", g3, h3.Len())
	}
	if d := g3.Frec - want.Frec; d > 1e-6 || d < -1e-6 {
		t.Fatalf("compaction changed frecency")
	}
	// file permissions
	if st, err := os.Stat(filepath.Join(dir, historyFile)); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("history file perms: %v %v", st, err)
	}
}

func TestHistoryTornTail(t *testing.T) {
	m := NewMemBackend()
	h := OpenHistory(m)
	h.Visit("https://a.test/", "A", false)
	h.Visit("https://b.test/", "B", false)
	raw, _ := m.ReadFile(historyFile)
	// a crash mid-append leaves a partial last line
	m.files[historyFile] = append(raw, []byte(`{"u":"https://torn.test/","t":"tor`)...)
	h2 := OpenHistory(m)
	if h2.Len() != 2 {
		t.Fatalf("torn tail must be ignored, len=%d", h2.Len())
	}
	if _, ok := h2.Get("https://torn.test/"); ok {
		t.Fatal("torn record applied")
	}
	// and the log keeps working afterwards
	h2.Visit("https://c.test/", "C", false)
}

func TestHistoryClear(t *testing.T) {
	h := OpenHistory(NewMemBackend())
	now := time.Now()
	h.VisitAt("https://old.test/", "o", false, now.Add(-48*time.Hour))
	h.VisitAt("https://new.test/", "n", false, now)
	if n := h.ClearSince(now.Add(-time.Hour)); n != 1 {
		t.Fatalf("cleared %d", n)
	}
	if _, ok := h.Get("https://new.test/"); ok {
		t.Fatal("new entry survived")
	}
	if _, ok := h.Get("https://old.test/"); !ok {
		t.Fatal("old entry removed")
	}
	h.Clear()
	if h.Len() != 0 {
		t.Fatal("clear")
	}
}

func TestTopSitesOnePerHost(t *testing.T) {
	h := OpenHistory(NewMemBackend())
	for i := 0; i < 5; i++ {
		h.Visit("https://a.test/p1", "A1", false)
	}
	h.Visit("https://a.test/p2", "A2", false)
	h.Visit("https://b.test/", "B", false)
	h.Visit("sweef://settings", "S", false)
	ts := h.TopSites(10)
	if len(ts) != 2 || ts[0].URL != "https://a.test/p1" {
		t.Fatalf("top sites: %v", urls(ts))
	}
}

func synthHistory(n int) (*History, []string) {
	r := rand.New(rand.NewSource(1))
	words := strings.Fields("alpha beta gamma delta news world sport tech cloud rust golang python recipe travel music film game book science health money weather map mail video photo shop deal review guide how what why")
	h := OpenHistory(NewMemBackend())
	now := time.Now()
	var queries []string
	hosts := make([]string, 5000)
	for i := range hosts {
		hosts[i] = fmt.Sprintf("%s%d.%s", words[r.Intn(len(words))], i, []string{"com", "org", "net", "io"}[r.Intn(4)])
	}
	for i := 0; i < n; i++ {
		host := hosts[r.Intn(len(hosts))]
		path := words[r.Intn(len(words))] + "/" + words[r.Intn(len(words))] + fmt.Sprint(i)
		title := words[r.Intn(len(words))] + " " + words[r.Intn(len(words))] + " " + words[r.Intn(len(words))]
		h.visit("https://"+host+"/"+path, title, now.Add(-time.Duration(r.Intn(90*24))*time.Hour), r.Intn(10) == 0)
	}
	h.dirty = true
	for i := 0; i < 50; i++ {
		w := words[r.Intn(len(words))]
		queries = append(queries, w[:min(len(w), 2+r.Intn(3))])
	}
	queries = append(queries, "zzzz", "qx7", "mail rust", "photo gu", "ww", "alp", "1234", "xyzzy", "golang.org")
	return h, queries
}

// 100k entries: prefix and substring search must stay under 2 ms.
func TestHistorySearch100kUnder2ms(t *testing.T) {
	h, queries := synthHistory(100_000)
	h.Search("warm", 8) // build the order index (done lazily, once)
	var lat []time.Duration
	for rep := 0; rep < 4; rep++ {
		for _, q := range queries {
			t0 := time.Now()
			res := h.Search(q, 8)
			lat = append(lat, time.Since(t0))
			if len(res) > 8 {
				t.Fatal("limit")
			}
		}
	}
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	p50, p99 := lat[len(lat)/2], lat[len(lat)*99/100]
	t.Logf("100k entries: p50=%v p99=%v max=%v (n=%d)", p50, p99, lat[len(lat)-1], len(lat))
	limit := 2 * time.Millisecond
	if raceEnabled {
		limit *= 15
	}
	if os.Getenv("CI") != "" {
		// Shared CI runners show multi-millisecond scheduling hiccups; the p50 stays at tens of microseconds.
		limit = 10 * time.Millisecond
	}
	if p99 > limit {
		t.Fatalf("p99 %v exceeds %v", p99, limit)
	}
}

func BenchmarkHistorySearch100k(b *testing.B) {
	h, queries := synthHistory(100_000)
	h.Search("warm", 8)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h.Search(queries[i%len(queries)], 8)
	}
}

func BenchmarkHistoryColdLoad100k(b *testing.B) {
	h, _ := synthHistory(100_000)
	h.mu.Lock()
	h.compactLocked()
	h.mu.Unlock()
	mem := h.b.(*MemBackend)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		OpenHistory(mem)
	}
}
