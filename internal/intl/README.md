# internal/intl: how the data is packaged

| file | what | made by |
|---|---|---|
| `data/<tag>.json.gz`, `global`, `plurals` | CLDR locale tables, one gzipped JSON document per locale, loaded on first use (`data.go`); the format is described in `tools/intlgen/gen.js` | `node tools/intlgen/gen.js` |
| `zoneinfo.zar` | the IANA time zone database: an `internal/zpack` archive (name -> TZif file, each deflated, aliases stored once), read by `tzdata.go` when the operating system has no database | `go generate ./internal/intl` (reads the toolchain's `$GOROOT/lib/time/zoneinfo.zip`) |
| `segment/` | UAX #29 text segmentation tables (Go source) | `tools/intlgen/ucdgen.js` |

The Go API of the package is unchanged by how the data is stored. The archive format ("ZAR1") is documented in
`internal/zpack/zpack.go`. The reasons for deflating embedded data, and what else was measured for the locale files
(a preset dictionary: 6% smaller; a shared string table: 4%), are in `docs/development.md`, "Binary size budget".
