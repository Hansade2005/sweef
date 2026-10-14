package intl

import (
	_ "embed"
	"errors"
	"sync"
	"time"

	"github.com/sweefbrowser/sweef/internal/zpack"
)

// The IANA time zone database travels with the binary (Windows and minimal containers have none). It used to come
// from time/tzdata, which embeds the toolchain's zoneinfo.zip uncompressed (408 KB, each alias a full copy).
// zoneinfo.zar is the same files in an internal/zpack archive: deflated, aliases stored once. time.LoadLocation
// still looks at the operating system's database first, exactly as before; only the fallback changed.
//
// zoneinfo.zar is made from $GOROOT/lib/time/zoneinfo.zip by
//
//	go generate ./internal/intl
//
// TestZoneArchive checks it against that file. Regenerate it when the toolchain's tzdata is newer (the test says so).
//
//go:generate go run ../../tools/zpack tz -o zoneinfo.zar
//go:embed zoneinfo.zar
var zoneinfoZ string

var errUnknownZone = errors.New("unknown time zone")

var (
	zoneOnce sync.Once
	zoneArc  *zpack.Archive
)

// loadEmbeddedZone returns the zone from the embedded database.
func loadEmbeddedZone(name string) (*time.Location, error) {
	zoneOnce.Do(func() { zoneArc, _ = openZones() })
	if zoneArc != nil {
		if b, ok := zoneArc.Read(name); ok {
			return time.LoadLocationFromTZData(name, b)
		}
	}
	return nil, errUnknownZone
}

func openZones() (*zpack.Archive, error) { return zpack.Open(zoneinfoZ) }
