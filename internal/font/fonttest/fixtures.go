package fonttest

import _ "embed"

// BoxesWOFF2 and IconsWOFF2 are Boxes("Boxy", "Regular", 400, false) and
// Icons("Icony") as WOFF2 files compressed by a real brotli encoder (see
// TestGenerateFixtures).
var (
	//go:embed testdata/boxes.woff2
	BoxesWOFF2 []byte
	//go:embed testdata/icons.woff2
	IconsWOFF2 []byte
)
