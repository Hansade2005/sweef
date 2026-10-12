package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// ---- profile / incognito -----------------------------------------------------------

func TestDefaultDirPerOS(t *testing.T) {
	env := map[string]string{"XDG_DATA_HOME": "/x/data", "APPDATA": `C:\Users\u\AppData\Roaming`}
	get := func(k string) string { return env[k] }
	home := func() (string, error) { return "/home/u", nil }
	if got := defaultDirFor("linux", get, home); got != "/x/data/sweef" {
		t.Fatal(got)
	}
	if got := defaultDirFor("linux", func(string) string { return "" }, home); got != "/home/u/.local/share/sweef" {
		t.Fatal(got)
	}
	if got := defaultDirFor("darwin", get, home); got != "/home/u/Library/Application Support/Sweef" {
		t.Fatal(got)
	}
	if got := defaultDirFor("windows", get, home); !strings.HasSuffix(got, `Roaming/Sweef`) && !strings.HasSuffix(got, `Roaming\Sweef`) {
		t.Fatal(got)
	}
}

func TestProfilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permissions")
	}
	dir := filepath.Join(t.TempDir(), "prof")
	p, err := OpenProfile(dir)
	if err != nil {
		t.Fatal(err)
	}
	p.History.Visit("https://a.test/", "A", false)
	p.Bookmarks.Add("", "A", "https://a.test/")
	p.Settings.Set("theme", "dark")
	p.Sites.SetAgent("https://a.test/", VisBlocked)
	p.Session.Save(Session{Windows: []SessionWindow{{Tabs: []SessionTab{{URL: "https://a.test/"}}}}})
	p.Downloads.Record("https://a.test/f", "f", "/tmp/f", 1, "x")
	p.Close()
	st, err := os.Stat(dir)
	if err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("dir perms %v %v", st, err)
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) < 5 {
		t.Fatalf("expected several files, got %v", ents)
	}
	for _, e := range ents {
		fi, _ := e.Info()
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("%s has mode %v, want 0600", e.Name(), fi.Mode().Perm())
		}
	}
}

// Incognito must not create or touch a single file. The profile dir is empty
// and a recording backend proves no disk code ran.
func TestIncognitoTouchesNoDisk(t *testing.T) {
	cwd := t.TempDir()
	old, _ := os.Getwd()
	os.Chdir(cwd)
	defer os.Chdir(old)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "xdg"))
	p := OpenIncognito()
	if p.Dir != "" || p.B.Persistent() {
		t.Fatal("incognito profile must be in-memory")
	}
	if _, ok := p.B.(*MemBackend); !ok {
		t.Fatalf("backend is %T", p.B)
	}
	p.History.Visit("https://secret.test/", "S", true)
	p.Bookmarks.Add("", "S", "https://secret.test/")
	p.Settings.Set("theme", "dark")
	p.Sites.SetAgent("https://secret.test/", VisBlocked)
	p.Session.Save(Session{Windows: []SessionWindow{{Tabs: []SessionTab{{URL: "https://secret.test/"}}}}})
	p.Session.Schedule(func() Session { return Session{} })
	p.Downloads.Record("https://secret.test/f", "f", "/nonexistent/f", 1, "x")
	p.Session.Flush()
	if p.History.Len() != 1 {
		t.Fatal("history should work in memory")
	}
	p.Close()
	for _, dir := range []string{cwd, home} {
		var found []string
		filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err == nil && path != dir {
				found = append(found, path)
			}
			return nil
		})
		if len(found) > 0 {
			t.Fatalf("incognito created files under %s: %v", dir, found)
		}
	}
	// wiped on close: a new incognito profile starts empty
	if OpenIncognito().History.Len() != 0 {
		t.Fatal("incognito state leaked into the next session")
	}
	// and the wipe really dropped the old backend's data
	if _, err := p.B.ReadFile(historyFile); err == nil {
		t.Fatal("incognito data survived Close")
	}
}

// ---- bookmarks ---------------------------------------------------------------------

func TestBookmarks(t *testing.T) {
	dir := t.TempDir()
	b, _ := NewDiskBackend(dir)
	bm := OpenBookmarks(b)
	f, err := bm.AddFolder(FolderBar, "Dev")
	if err != nil {
		t.Fatal(err)
	}
	x, _ := bm.Add(f.ID, "Go", "https://go.dev/")
	bm.Add(FolderOther, "Rust", "https://rust-lang.org/")
	if !bm.IsBookmarked("https://go.dev/") || bm.IsBookmarked("https://nope/") {
		t.Fatal("IsBookmarked")
	}
	if on, _ := bm.Toggle("Go", "https://go.dev/"); on {
		t.Fatal("toggle should remove an existing bookmark")
	}
	if bm.IsBookmarked("https://go.dev/") {
		t.Fatal("still bookmarked after toggle")
	}
	if on, _ := bm.Toggle("Go", "https://go.dev/"); !on {
		t.Fatal("toggle should add")
	}
	if got := bm.Search("rust", 5); len(got) != 1 {
		t.Fatalf("search: %v", got)
	}
	// nested removal drops children
	bm.Remove(f.ID)
	if bm.IsBookmarked("https://go.dev/") && len(bm.Children(f.ID)) != 0 {
		t.Fatal("folder removal left children")
	}
	_ = x
	// cycle protection
	f1, _ := bm.AddFolder(FolderBar, "A")
	f2, _ := bm.AddFolder(f1.ID, "B")
	if err := bm.Move(f1.ID, f2.ID, 0); err == nil {
		t.Fatal("moving a folder into its own subtree must fail")
	}
	// built-in folders are permanent
	if bm.Remove(FolderBar) {
		t.Fatal("bar removed")
	}
	// persistence
	b.Close()
	b2, _ := NewDiskBackend(dir)
	bm2 := OpenBookmarks(b2)
	if !bm2.IsBookmarked("https://rust-lang.org/") || len(bm2.Children(f1.ID)) != 1 {
		t.Fatal("reload lost bookmarks")
	}
	// ids never collide after reload
	n, _ := bm2.Add("", "New", "https://new.test/")
	for _, c := range bm2.All() {
		if c.ID == n.ID && c.URL != "https://new.test/" {
			t.Fatal("id collision")
		}
	}
}

// ---- settings ---------------------------------------------------------------------------

func TestSettingsTypedDefaultsMigration(t *testing.T) {
	m := NewMemBackend()
	s, _ := OpenSettings(m)
	if s.Str("searchEngine") != "duckduckgo" || !s.Bool("blockTrackers") || s.Int("sidebarWidth") != 360 {
		t.Fatalf("defaults wrong: %v", s.All())
	}
	if err := s.Set("sidebarWidth", 5000); err == nil {
		t.Fatal("range not enforced")
	}
	if err := s.Set("theme", "neon"); err == nil {
		t.Fatal("enum not enforced")
	}
	if err := s.Set("blockTrackers", "yes"); err == nil {
		t.Fatal("type not enforced")
	}
	if err := s.Set("nope", 1); err == nil {
		t.Fatal("unknown key accepted")
	}
	if err := s.Set("sidebarWidth", float64(400)); err != nil { // JSON numbers are float64
		t.Fatal(err)
	}
	if err := s.Set("sidebarWidth", 400.5); err == nil {
		t.Fatal("fractional int accepted")
	}
	var fired atomic.Int32
	s.OnChange(func(k string, v any) { fired.Add(1) })
	s.Set("theme", "dark")
	s.Set("theme", "dark") // unchanged: no callback
	if fired.Load() != 1 {
		t.Fatalf("callbacks %d", fired.Load())
	}
	s2, _ := OpenSettings(m)
	if s2.Str("theme") != "dark" || s2.Int("sidebarWidth") != 400 {
		t.Fatal("not persisted")
	}
	// v1 file migrates
	m2 := NewMemBackend()
	m2.WriteAtomic(settingsFile, []byte(`{"v":1,"values":{"adblock":false,"doNotTrack":false,"theme":"light","bogus":3}}`))
	s3, _ := OpenSettings(m2)
	if s3.Bool("blockTrackers") || s3.Bool("sendGPC") || s3.Str("theme") != "light" {
		t.Fatalf("migration failed: %v", s3.All())
	}
	raw, _ := m2.ReadFile(settingsFile)
	var doc settingsDoc
	json.Unmarshal(raw, &doc)
	if doc.V != SettingsVersion {
		t.Fatalf("version not upgraded: %d", doc.V)
	}
	// corrupt file never blocks start-up
	m3 := NewMemBackend()
	m3.WriteAtomic(settingsFile, []byte(`{not json`))
	s4, err := OpenSettings(m3)
	if err != nil || s4.Str("theme") != "system" {
		t.Fatal("corrupt settings must fall back to defaults")
	}
	// invalid stored values fall back to defaults
	m4 := NewMemBackend()
	m4.WriteAtomic(settingsFile, []byte(`{"v":2,"values":{"theme":"neon","sidebarWidth":1}}`))
	s5, _ := OpenSettings(m4)
	if s5.Str("theme") != "system" || s5.Int("sidebarWidth") != 360 {
		t.Fatal("invalid stored values must not load")
	}
}

// ---- sites / agent visibility -------------------------------------------------------------

func TestSitesVisibility(t *testing.T) {
	m := NewMemBackend()
	s := OpenSites(m)
	if s.Agent("https://a.test/page") != VisAsk {
		t.Fatal("default must be ask")
	}
	s.SetAgent("https://a.test/some/page?q=1", VisBlocked)
	if s.Agent("https://a.test/other") != VisBlocked {
		t.Fatal("rule is per origin, not per URL")
	}
	if s.Agent("http://a.test/") != VisAsk {
		t.Fatal("scheme is part of the origin")
	}
	if s.Agent("https://a.test:8443/") != VisAsk {
		t.Fatal("port is part of the origin")
	}
	s.SetDefault(VisAllowed)
	if s.Agent("https://new.test/") != VisAllowed || s.Agent("https://a.test/") != VisBlocked {
		t.Fatal("default / rule precedence")
	}
	s.SetPrivacyException("https://a.test/", true)
	if !s.Rule("https://a.test/x").NoBlock {
		t.Fatal("exception")
	}
	s2 := OpenSites(m)
	if s2.Agent("https://a.test/") != VisBlocked || s2.Default != VisAllowed || !s2.Rule("https://a.test/").NoBlock {
		t.Fatal("not persisted")
	}
	s.SetAgent("https://a.test/", "")
	s.SetPrivacyException("https://a.test/", false)
	if len(s.All()) != 0 {
		t.Fatalf("empty rules should be dropped: %v", s.All())
	}
	if OriginOf("sweef://settings/x") != "sweef://settings" || OriginOf("about:blank") != "" {
		t.Fatal("OriginOf")
	}
}

// ---- session restore: crash safety -----------------------------------------------------------

func bigSession(n int, tag string) Session {
	var tabs []SessionTab
	for i := 0; i < n; i++ {
		tabs = append(tabs, SessionTab{URL: fmt.Sprintf("https://%s.test/%d", tag, i), Title: strings.Repeat("t", 200), Scroll: i,
			History: []string{"https://h.test/1", "https://h.test/2"}})
	}
	return Session{Windows: []SessionWindow{{Tabs: tabs, Active: n / 2}}}
}

type faultBackend struct {
	Backend
	failAfter int // bytes written into the temp file before the "crash"
	armed     bool
}

var errCrash = errors.New("simulated crash")

func (f *faultBackend) WriteAtomic(name string, data []byte) error {
	if f.armed && strings.HasPrefix(name, "session") {
		// emulate dying halfway through writing the temp file: the temp file
		// is torn and never renamed over the real one
		if d, ok := f.Backend.(*DiskBackend); ok {
			os.WriteFile(d.path(name)+".tmp", data[:min(f.failAfter, len(data))], 0o600)
		}
		return errCrash
	}
	return f.Backend.WriteAtomic(name, data)
}

func TestSessionSurvivesCrashMidWrite(t *testing.T) {
	dir := t.TempDir()
	d, _ := NewDiskBackend(dir)
	fb := &faultBackend{Backend: d}
	s := OpenSession(fb)
	if err := s.Save(bigSession(5, "first")); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(bigSession(6, "second")); err != nil {
		t.Fatal(err)
	}
	fb.armed, fb.failAfter = true, 100
	if err := s.Save(bigSession(7, "third")); err == nil {
		t.Fatal("expected the simulated crash")
	}
	got, ok := OpenSession(d).Load()
	if !ok || len(got.Windows[0].Tabs) != 6 || !strings.Contains(got.Windows[0].Tabs[0].URL, "second") {
		t.Fatalf("a torn write must leave the last complete session: ok=%v %+v", ok, got)
	}
	// a torn primary file (disk corruption, not just a torn temp) falls back to the previous generation
	os.WriteFile(filepath.Join(dir, sessionFile), []byte(`{"v":1,"windows":[{"tabs":[{"url":"https://to`), 0o600)
	got, ok = OpenSession(d).Load()
	if !ok || len(got.Windows[0].Tabs) == 0 {
		t.Fatalf("fallback to .prev failed: %v %+v", ok, got)
	}
}

// The real thing: a child process writes sessions in a tight loop and is
// SIGKILLed at random moments; after each kill the parent must still load a
// complete, valid session.
func TestSessionKillMidWrite(t *testing.T) {
	if os.Getenv("SWEEF_SESSION_WRITER") != "" {
		return
	}
	if runtime.GOOS == "windows" {
		t.Skip("needs SIGKILL")
	}
	dir := t.TempDir()
	valid := 0
	for round := 0; round < 12; round++ {
		cmd := exec.Command(os.Args[0], "-test.run=^TestSessionWriterHelper$")
		cmd.Env = append(os.Environ(), "SWEEF_SESSION_WRITER="+dir)
		out, _ := cmd.StdoutPipe()
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		// wait until the child has completed at least one write, then kill at a random time
		buf := make([]byte, 1)
		if _, err := io.ReadFull(out, buf); err != nil {
			t.Fatalf("writer did not start: %v", err)
		}
		time.Sleep(time.Duration(round*7%23+1) * time.Millisecond)
		cmd.Process.Kill()
		cmd.Wait()
		d, _ := NewDiskBackend(dir)
		ss, ok := OpenSession(d).Load()
		if !ok {
			t.Fatalf("round %d: no loadable session after kill", round)
		}
		if len(ss.Windows) != 1 || len(ss.Windows[0].Tabs) < 3 {
			t.Fatalf("round %d: incomplete session %+v", round, ss)
		}
		for _, tb := range ss.Windows[0].Tabs {
			if !strings.HasPrefix(tb.URL, "https://w") {
				t.Fatalf("round %d: garbage tab %q", round, tb.URL)
			}
		}
		valid++
	}
	t.Logf("%d/12 kills left a valid session", valid)
}

func TestSessionWriterHelper(t *testing.T) {
	dir := os.Getenv("SWEEF_SESSION_WRITER")
	if dir == "" {
		t.Skip("helper process")
	}
	d, _ := NewDiskBackend(dir)
	s := OpenSession(d)
	for i := 0; ; i++ {
		if err := s.Save(bigSession(300+i%50, "w"+strconv.Itoa(i))); err != nil {
			os.Exit(2)
		}
		if i == 0 {
			os.Stdout.Write([]byte("x"))
		}
	}
}

func TestSessionDebounce(t *testing.T) {
	m := NewMemBackend()
	s := OpenSession(m)
	s.Debounce = 40 * time.Millisecond
	var n atomic.Int32
	for i := 0; i < 50; i++ {
		s.Schedule(func() Session { n.Add(1); return bigSession(1, "d") })
		time.Sleep(time.Millisecond)
	}
	time.Sleep(150 * time.Millisecond)
	if s.Writes() == 0 || s.Writes() > 3 {
		t.Fatalf("50 changes in ~50ms produced %d writes, want 1-3", s.Writes())
	}
	if n.Load() > 3 {
		t.Fatalf("snapshot function ran %d times", n.Load())
	}
	// Flush writes a pending snapshot immediately
	s.Schedule(func() Session { return bigSession(2, "flush") })
	s.Flush()
	ss, _ := s.Load()
	if len(ss.Windows[0].Tabs) != 2 {
		t.Fatal("flush did not write")
	}
	// Stop prevents later schedules
	s.Stop()
	w := s.Writes()
	s.Schedule(func() Session { return bigSession(9, "late") })
	time.Sleep(80 * time.Millisecond)
	if s.Writes() != w {
		t.Fatal("scheduled after Stop")
	}
}

// ---- downloads ----------------------------------------------------------------------------------

func TestDownloadProgressPauseResumeCancel(t *testing.T) {
	payload := []byte(strings.Repeat("0123456789abcdef", 1<<14)) // 256 KiB
	slow := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := 0
		if rg := r.Header.Get("Range"); rg != "" {
			fmt.Sscanf(rg, "bytes=%d-", &start)
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(payload)-1, len(payload)))
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)-start))
			w.WriteHeader(206)
		} else {
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		}
		fl := w.(http.Flusher)
		for off := start; off < len(payload); off += 32 << 10 {
			end := min(off+32<<10, len(payload))
			if r.URL.Path == "/slow" {
				select {
				case <-slow:
				case <-r.Context().Done():
					return
				}
			}
			w.Write(payload[off:end])
			fl.Flush()
		}
	}))
	defer srv.Close()
	b := NewMemBackend()
	d := OpenDownloads(b)
	dir := t.TempDir()
	var updates atomic.Int32
	d.OnUpdate = func(DownloadItem) { updates.Add(1) }

	it, err := d.Start(srv.URL+"/file.bin", "", dir, "", "")
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, d, it.ID, DLDone)
	got, _ := os.ReadFile(filepath.Join(dir, "file.bin"))
	if string(got) != string(payload) {
		t.Fatalf("content mismatch: %d bytes", len(got))
	}
	fi, _ := d.Get(it.ID)
	if fi.Received != int64(len(payload)) || fi.Total != int64(len(payload)) {
		t.Fatalf("progress counters: %+v", fi)
	}
	if updates.Load() == 0 {
		t.Fatal("no updates")
	}

	// pause and resume with a Range request
	it2, _ := d.Start(srv.URL+"/slow", "slow.bin", dir, "", "")
	for i := 0; i < 3; i++ {
		slow <- struct{}{}
	}
	waitFor(t, func() bool { x, _ := d.Get(it2.ID); return x.Received >= 3*32<<10 })
	if !d.Pause(it2.ID) {
		t.Fatal("pause refused")
	}
	waitFor(t, func() bool { x, _ := d.Get(it2.ID); return x.State == DLPaused })
	mid, _ := d.Get(it2.ID)
	if mid.Received <= 0 || mid.Received >= int64(len(payload)) {
		t.Fatalf("paused at %d", mid.Received)
	}
	if !d.Resume(it2.ID) {
		t.Fatal("resume refused")
	}
	go func() {
		for i := 0; i < 20; i++ {
			slow <- struct{}{}
		}
	}()
	waitState(t, d, it2.ID, DLDone)
	got, _ = os.ReadFile(filepath.Join(dir, "slow.bin"))
	if string(got) != string(payload) {
		t.Fatalf("resumed file corrupt: %d bytes", len(got))
	}

	// cancel deletes the partial file
	it3, _ := d.Start(srv.URL+"/slow", "cancel.bin", dir, "", "")
	slow <- struct{}{}
	waitFor(t, func() bool { x, _ := d.Get(it3.ID); return x.Received > 0 })
	if !d.Cancel(it3.ID) {
		t.Fatal("cancel refused")
	}
	waitFor(t, func() bool { _, err := os.Stat(filepath.Join(dir, "cancel.bin")); return err != nil })
	if x, _ := d.Get(it3.ID); x.State != DLCanceled {
		t.Fatalf("state %s", x.State)
	}
	// unique names, unsafe names, bad schemes
	it4, _ := d.Start(srv.URL+"/file.bin", "../../etc/passwd", dir, "", "")
	if filepath.Dir(it4.Path) != dir || strings.Contains(it4.Name, "/") {
		t.Fatalf("path traversal: %s", it4.Path)
	}
	if _, err := d.Start("file:///etc/passwd", "", dir, "", ""); err == nil {
		t.Fatal("file:// download accepted")
	}
	// list persists (interrupted downloads come back paused)
	d2 := OpenDownloads(b)
	if len(d2.List()) < 3 {
		t.Fatalf("persisted list: %d", len(d2.List()))
	}
}

func waitState(t *testing.T, d *Downloads, id, st string) {
	t.Helper()
	waitFor(t, func() bool { x, _ := d.Get(id); return x.State == st || x.State == DLError })
	if x, _ := d.Get(id); x.State != st {
		t.Fatalf("state %s (%s), want %s", x.State, x.Error, st)
	}
}

func waitFor(t *testing.T, f func() bool) {
	t.Helper()
	for i := 0; i < 500; i++ {
		if f() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timeout")
}

// ---- usage -------------------------------------------------------------------------------------------

func TestUsageIsReal(t *testing.T) {
	u := NewUsage()
	runtime.GC()
	u.AddBlocked(3)
	u.AddPage()
	a := u.Snapshot(4)
	if a.Tabs != 4 || a.Blocked != 3 || a.Pages != 1 {
		t.Fatalf("%+v", a)
	}
	if a.RSSBytes == 0 || a.HeapBytes == 0 || a.Goroutines == 0 {
		t.Fatalf("metrics empty: %+v", a)
	}
	if runtime.GOOS == "linux" && !a.RSSExact {
		t.Fatal("RSS should be exact on linux")
	}
	// memory figure moves with real allocation
	runtime.GC()
	a = u.Snapshot(4)
	sink := make([][]byte, 0, 64)
	for i := 0; i < 64; i++ {
		b := make([]byte, 1<<20)
		for j := range b {
			b[j] = 1
		}
		sink = append(sink, b)
	}
	b := u.Snapshot(4)
	runtime.KeepAlive(sink)
	if b.HeapBytes < a.HeapBytes+32<<20 {
		t.Fatalf("heap did not grow: %d -> %d", a.HeapBytes, b.HeapBytes)
	}
}
