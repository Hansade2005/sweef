package intl

import (
	"archive/zip"
	"bytes"
	"io"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// The embedded database must answer like the one the toolchain ships: the same bytes for every zone, and the same
// offsets from the loader.
func TestZoneArchive(t *testing.T) {
	a, err := openZones()
	if err != nil {
		t.Fatal(err)
	}
	if n := len(a.Names()); n < 500 {
		t.Fatalf("only %d zones", n)
	}
	zr, err := zip.OpenReader(filepath.Join(runtime.GOROOT(), "lib", "time", "zoneinfo.zip"))
	if err != nil {
		t.Skipf("no toolchain database to compare with: %v", err)
	}
	defer zr.Close()
	missing := 0
	seen := 0
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		rc, _ := f.Open()
		want, _ := io.ReadAll(rc)
		rc.Close()
		got, ok := a.Read(f.Name)
		if !ok {
			missing++
			continue
		}
		seen++
		if !bytes.Equal(got, want) {
			t.Logf("zone %s differs from the toolchain's database: regenerate with `go generate ./internal/intl`", f.Name)
			continue
		}
		// Semantic equality: both load, and agree on a handful of instants across the years.
		l1, err1 := time.LoadLocationFromTZData(f.Name, want)
		l2, err2 := time.LoadLocationFromTZData(f.Name, got)
		if err1 != nil || err2 != nil {
			t.Errorf("%s: load errors %v / %v", f.Name, err1, err2)
			continue
		}
		for _, ms := range []int64{0, 631152000000, 1100000000000, 1720000000000, 1752000000000, 2500000000000} {
			if o1, o2 := offsetAt(l1, ms), offsetAt(l2, ms); o1 != o2 {
				t.Errorf("%s at %d: %d vs %d", f.Name, ms, o1, o2)
			}
		}
	}
	if seen < 500 {
		t.Errorf("only %d zones compared", seen)
	}
	if missing > 0 {
		t.Logf("%d zones of the toolchain's database are not embedded: regenerate with `go generate ./internal/intl`", missing)
	}
	if missing > 5 {
		t.Errorf("the embedded database misses %d zones", missing)
	}
}

func offsetAt(l *time.Location, ms int64) int {
	_, off := time.UnixMilli(ms).In(l).Zone()
	return off
}

// Known facts, independent of any file: the fallback loader must give the right answers even where the operating
// system has no database (Windows, scratch containers). TZ lookups go through the embedded copy here.
func TestEmbeddedZoneFacts(t *testing.T) {
	cases := []struct {
		zone string
		ms   int64 // 2025-01-15T12:00Z and 2025-07-15T12:00Z
		off  int
	}{
		{"America/New_York", 1736942400000, -5 * 3600},
		{"America/New_York", 1752580800000, -4 * 3600},
		{"Europe/London", 1752580800000, 3600},
		{"Asia/Kolkata", 1736942400000, 5*3600 + 1800},
		{"Australia/Sydney", 1736942400000, 11 * 3600},
		{"Asia/Kathmandu", 1736942400000, 5*3600 + 2700},
		{"US/Pacific", 1752580800000, -7 * 3600},
	}
	for _, c := range cases {
		l, err := loadEmbeddedZone(c.zone)
		if err != nil {
			t.Errorf("%s: %v", c.zone, err)
			continue
		}
		if got := offsetAt(l, c.ms); got != c.off {
			t.Errorf("%s: offset %d, want %d", c.zone, got, c.off)
		}
	}
	if _, err := loadEmbeddedZone("Mars/Olympus_Mons"); err == nil {
		t.Error("an unknown zone loaded")
	}
	if !ValidZone("Europe/Paris") || ValidZone("Not/AZone") {
		t.Error("ValidZone disagrees with the database")
	}
	if off, _, ok := ZoneOffset("Asia/Tokyo", 1736942400000); !ok || off != 9*3600 {
		t.Errorf("ZoneOffset(Asia/Tokyo) = %d, %v", off, ok)
	}
}
