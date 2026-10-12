package update

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

func sum(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func mkManifest(t *testing.T, version string, payload []byte, url string) []byte {
	t.Helper()
	m := Manifest{
		Schema: 1, Channel: "alpha", Version: version, Published: t0, Expires: t0.Add(30 * 24 * time.Hour),
		Artifacts: []Artifact{{OS: "linux", Arch: "amd64", Kind: "binary", URL: url, SHA256: sum(payload), Size: int64(len(payload))}},
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestVerifyAndParse(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	pub2, _, _ := ed25519.GenerateKey(rand.Reader)
	data := mkManifest(t, "0.1.0-alpha.2", []byte("x"), "https://example.com/sweef")
	sig := Sign(priv, data)

	if _, err := VerifyAndParse(data, sig, []ed25519.PublicKey{pub2, pub}, t0); err != nil {
		t.Fatalf("valid (second key, rotation): %v", err)
	}
	if _, err := VerifyAndParse(data, sig, []ed25519.PublicKey{pub2}, t0); err != ErrBadSignature {
		t.Fatalf("wrong key: %v", err)
	}
	tampered := []byte(strings.Replace(string(data), "alpha.2", "alpha.3", 1))
	if _, err := VerifyAndParse(tampered, sig, []ed25519.PublicKey{pub}, t0); err != ErrBadSignature {
		t.Fatalf("tampered: %v", err)
	}
	if _, err := VerifyAndParse(data, "!!notbase64", []ed25519.PublicKey{pub}, t0); err != ErrBadSignature {
		t.Fatalf("garbage sig: %v", err)
	}
	if _, err := VerifyAndParse(data, sig, nil, t0); err != ErrBadSignature {
		t.Fatalf("no keys must fail closed: %v", err)
	}
	if _, err := VerifyAndParse(data, sig, []ed25519.PublicKey{pub}, t0.Add(31*24*time.Hour)); err != ErrExpired {
		t.Fatalf("expired: %v", err)
	}
}

func TestParseRejects(t *testing.T) {
	good := `{"schema":1,"channel":"alpha","version":"0.1.0","published":"2026-10-01T00:00:00Z","expires":"2027-01-01T00:00:00Z","artifacts":[{"os":"linux","arch":"amd64","kind":"binary","url":"https://e.com/a","sha256":"` + strings.Repeat("a", 64) + `","size":1}]}`
	if _, err := Parse([]byte(good)); err != nil {
		t.Fatal(err)
	}
	bad := map[string]string{
		"schema":  strings.Replace(good, `"schema":1`, `"schema":2`, 1),
		"version": strings.Replace(good, `"0.1.0"`, `"banana"`, 1),
		"url":     strings.Replace(good, "https://e.com/a", "file:///etc/passwd", 1),
		"sha":     strings.Replace(good, strings.Repeat("a", 64), "abc", 1),
		"upper":   strings.Replace(good, strings.Repeat("a", 64), strings.Repeat("A", 64), 1),
		"size":    strings.Replace(good, `"size":1`, `"size":0`, 1),
		"unknown": strings.Replace(good, `"schema":1,`, `"schema":1,"evil":true,`, 1),
		"expiry":  strings.Replace(good, `"expires":"2027-01-01T00:00:00Z",`, "", 1),
	}
	for name, doc := range bad {
		if _, err := Parse([]byte(doc)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestVersionOrdering(t *testing.T) {
	order := []string{"0.1.0-alpha.1", "0.1.0-alpha.2", "0.1.0-alpha.10", "0.1.0-beta", "0.1.0-rc.1", "0.1.0", "0.1.1", "0.2.0", "1.0.0"}
	for i := range order {
		for j := range order {
			a, _ := ParseVersion(order[i])
			b, _ := ParseVersion(order[j])
			want := 0
			if i < j {
				want = -1
			} else if i > j {
				want = 1
			}
			if got := a.Compare(b); got != want {
				t.Errorf("Compare(%s,%s)=%d want %d", order[i], order[j], got, want)
			}
		}
	}
	if !IsNewer("v0.1.0", "0.1.0-alpha.9") || IsNewer("0.1.0", "0.1.0") || IsNewer("junk", "0.1.0") || IsNewer("0.2.0", "junk") {
		t.Error("IsNewer")
	}
	for _, bad := range []string{"", "1.2", "1.2.3.4", "01.2.3", "1.2.3-", "a.b.c"} {
		if _, err := ParseVersion(bad); err == nil {
			t.Errorf("ParseVersion(%q) should fail", bad)
		}
	}
}

func TestSelect(t *testing.T) {
	m := &Manifest{Artifacts: []Artifact{
		{OS: "darwin", Arch: "universal", Kind: "binary"}, {OS: "linux", Arch: "arm64", Kind: "binary"}, {OS: "linux", Arch: "arm64", Kind: "installer"}}}
	if _, err := m.Select("darwin", "arm64", "binary"); err != nil {
		t.Error(err)
	}
	if a, err := m.Select("linux", "arm64", "installer"); err != nil || a.Kind != "installer" {
		t.Error(err)
	}
	if _, err := m.Select("linux", "amd64", "binary"); err != ErrNoArtifact {
		t.Error("want ErrNoArtifact")
	}
	if _, err := m.Select("windows", "arm64", "binary"); err != ErrNoArtifact {
		t.Error("universal must be darwin only")
	}
}

func TestCheck(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	keys := []ed25519.PublicKey{pub}
	payload := []byte("new binary")
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data := mkManifest(t, "0.1.0-alpha.5", payload, srv.URL+"/bin")
		switch r.URL.Path {
		case "/latest.json":
			w.Write(data)
		case "/latest.json.sig":
			w.Write([]byte(Sign(priv, data)))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	now := func() time.Time { return t0 }
	m, err := Check(context.Background(), srv.Client(), srv.URL+"/latest.json", keys, "0.1.0-alpha.4", now)
	if err != nil || m.Version != "0.1.0-alpha.5" {
		t.Fatalf("%v %+v", err, m)
	}
	if _, err := Check(context.Background(), srv.Client(), srv.URL+"/latest.json", keys, "0.1.0-alpha.5", now); err != ErrNotNewer {
		t.Fatalf("same version: %v", err)
	}
	if _, err := Check(context.Background(), srv.Client(), srv.URL+"/latest.json", keys, "0.2.0", now); err != ErrNotNewer {
		t.Fatalf("downgrade must be refused: %v", err)
	}
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	if _, err := Check(context.Background(), srv.Client(), srv.URL+"/latest.json", []ed25519.PublicKey{other}, "0.1.0", now); err != ErrBadSignature {
		t.Fatalf("wrong key: %v", err)
	}
	if _, err := Check(context.Background(), srv.Client(), srv.URL+"/missing.json", keys, "0.1.0", now); err == nil {
		t.Fatal("404 must fail")
	}
}

// rangeServer serves payload honouring Range, optionally dropping the first
// response after cut bytes to simulate an interrupted transfer.
func rangeServer(payload []byte, cut int, ignoreRange bool, hits *int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*hits++
		start := 0
		if rg := r.Header.Get("Range"); rg != "" && !ignoreRange {
			n, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(rg, "bytes="), "-"))
			start = n
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", n, len(payload)-1, len(payload)))
			w.WriteHeader(http.StatusPartialContent)
		}
		body := payload[start:]
		if cut > 0 && *hits == 1 {
			body = body[:cut]
		}
		w.Write(body)
	}))
}

func TestDownloadResume(t *testing.T) {
	payload := []byte(strings.Repeat("0123456789", 1000))
	hits := 0
	srv := rangeServer(payload, 3000, false, &hits)
	defer srv.Close()
	dest := filepath.Join(t.TempDir(), "sweef")
	a := Artifact{URL: srv.URL, SHA256: sum(payload), Size: int64(len(payload))}

	if err := Download(context.Background(), nil, a, dest); err == nil {
		t.Fatal("first attempt is truncated and must fail")
	}
	if st, err := os.Stat(dest + ".part"); err != nil || st.Size() != 3000 {
		t.Fatalf("partial file not kept: %v", err)
	}
	if err := Download(context.Background(), nil, a, dest); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(dest)
	if string(got) != string(payload) {
		t.Fatal("content mismatch after resume")
	}
	if _, err := os.Stat(dest + ".part"); !os.IsNotExist(err) {
		t.Fatal(".part should be renamed away")
	}
}

func TestDownloadServerIgnoresRange(t *testing.T) {
	payload := []byte(strings.Repeat("abc", 5000))
	hits := 100 // never trigger the cut
	srv := rangeServer(payload, 0, true, &hits)
	defer srv.Close()
	dest := filepath.Join(t.TempDir(), "f")
	os.WriteFile(dest+".part", payload[:1234], 0o600)
	a := Artifact{URL: srv.URL, SHA256: sum(payload), Size: int64(len(payload))}
	if err := Download(context.Background(), nil, a, dest); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(dest); string(got) != string(payload) {
		t.Fatal("must restart from zero on a 200 response")
	}
}

func TestDownloadChecksumMismatch(t *testing.T) {
	payload := []byte("hello world")
	hits := 100
	srv := rangeServer([]byte("HELLO WORLD"), 0, false, &hits) // same size, different bytes
	defer srv.Close()
	dest := filepath.Join(t.TempDir(), "f")
	a := Artifact{URL: srv.URL, SHA256: sum(payload), Size: int64(len(payload))}
	err := Download(context.Background(), nil, a, dest)
	if err == nil || !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Fatalf("want checksum error, got %v", err)
	}
	for _, p := range []string{dest, dest + ".part"} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s must not exist after a bad download", p)
		}
	}
}

func TestDownloadOversizedIsTruncatedAndRejected(t *testing.T) {
	signed := []byte("12345")
	hits := 100
	srv := rangeServer([]byte("12345-and-a-lot-more-junk"), 0, false, &hits)
	defer srv.Close()
	dest := filepath.Join(t.TempDir(), "f")
	err := Download(context.Background(), nil, Artifact{URL: srv.URL, SHA256: sum(signed), Size: 5}, dest)
	if err == nil {
		t.Fatal("expected failure for oversized body")
	}
}

func TestDownloadHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	err := Download(context.Background(), nil, Artifact{URL: srv.URL, SHA256: sum(nil), Size: 1}, filepath.Join(t.TempDir(), "f"))
	if err == nil {
		t.Fatal("want error")
	}
}

func writeExe(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

func readStr(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestReplaceCommit(t *testing.T) {
	dir := t.TempDir()
	target, nf := filepath.Join(dir, "sweef"), filepath.Join(dir, "download")
	writeExe(t, target, "old")
	writeExe(t, nf, "new")
	r, err := Replace(target, nf)
	if err != nil {
		t.Fatal(err)
	}
	if readStr(t, target) != "new" || readStr(t, target+".old") != "old" {
		t.Fatal("swap wrong")
	}
	if st, _ := os.Stat(target); st.Mode().Perm()&0o100 == 0 {
		t.Fatal("new binary must be executable")
	}
	if err := r.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(target + ".old"); !os.IsNotExist(err) {
		t.Fatal("backup should be gone after commit")
	}
	if r.Commit() == nil || r.Rollback() == nil {
		t.Fatal("double finalise must error")
	}
}

func TestReplaceRollback(t *testing.T) {
	dir := t.TempDir()
	target, nf := filepath.Join(dir, "sweef"), filepath.Join(dir, "download")
	writeExe(t, target, "old")
	writeExe(t, nf, "broken")
	r, err := Replace(target, nf)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Rollback(); err != nil {
		t.Fatal(err)
	}
	if readStr(t, target) != "old" {
		t.Fatal("rollback did not restore")
	}
	for _, s := range []string{".old", ".new", ".bad"} {
		if _, err := os.Stat(target + s); !os.IsNotExist(err) {
			t.Errorf("leftover %s", s)
		}
	}
}

func TestReplaceMissingSourceLeavesTargetUntouched(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "sweef")
	writeExe(t, target, "old")
	if _, err := Replace(target, filepath.Join(dir, "nope")); err == nil {
		t.Fatal("want error")
	}
	if readStr(t, target) != "old" {
		t.Fatal("target changed")
	}
	if _, err := os.Stat(target + ".new"); !os.IsNotExist(err) {
		t.Fatal("staged file left behind")
	}
	if _, err := Replace(filepath.Join(dir, "absent"), target); err == nil {
		t.Fatal("missing target must error")
	}
}

func TestRecoverInterruptedSwap(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "sweef")
	writeExe(t, target+".old", "old")
	writeExe(t, target+".new", "half")
	if err := Recover(target); err != nil {
		t.Fatal(err)
	}
	if readStr(t, target) != "old" {
		t.Fatal("not restored")
	}
	if _, err := os.Stat(target + ".new"); !os.IsNotExist(err) {
		t.Fatal(".new not cleaned")
	}
	// healthy install: Recover must not touch the binary
	writeExe(t, target, "current")
	writeExe(t, target+".old", "older")
	Recover(target)
	if readStr(t, target) != "current" {
		t.Fatal("Recover clobbered a healthy install")
	}
}
