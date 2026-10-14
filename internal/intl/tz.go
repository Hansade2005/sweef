package intl

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	_ "time/tzdata" // the IANA database travels with the binary: Windows and minimal containers have none
)

var (
	locMu sync.Mutex
	locs  = map[string]*time.Location{}
)

func location(name string) *time.Location {
	locMu.Lock()
	defer locMu.Unlock()
	if l, ok := locs[name]; ok {
		return l
	}
	l, err := time.LoadLocation(name)
	if err != nil {
		l = nil
	}
	locs[name] = l
	return l
}

// ValidZone reports whether name is a zone of the IANA database.
func ValidZone(name string) bool { return name == "UTC" || location(name) != nil }

// ZoneOffset returns the UTC offset in seconds of a zone at an instant (milliseconds since the epoch) and whether
// daylight saving time is in effect.
func ZoneOffset(name string, ms int64) (offset int, dst bool, ok bool) {
	if name == "UTC" {
		return 0, false, true
	}
	l := location(name)
	if l == nil {
		return 0, false, false
	}
	t := time.UnixMilli(ms).In(l)
	_, off := t.Zone()
	return off, t.IsDST(), true
}

// LocalZone is the IANA name of the operating system's time zone ("UTC" when it cannot be told).
func LocalZone() string {
	if tz := os.Getenv("TZ"); tz != "" {
		tz = strings.TrimPrefix(tz, ":")
		if ValidZone(tz) {
			return tz
		}
	}
	if p, err := filepath.EvalSymlinks("/etc/localtime"); err == nil {
		if i := strings.Index(p, "zoneinfo/"); i >= 0 {
			if n := p[i+len("zoneinfo/"):]; ValidZone(n) {
				return n
			}
		}
	}
	if b, err := os.ReadFile("/etc/timezone"); err == nil {
		if n := strings.TrimSpace(string(b)); n != "" && ValidZone(n) {
			return n
		}
	}
	if n := time.Local.String(); n != "Local" && ValidZone(n) {
		return n
	}
	return windowsZone()
}
