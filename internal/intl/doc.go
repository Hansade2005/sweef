// Package intl holds the data and the few natives behind the ECMAScript Intl API of Sweef's JavaScript engine
// (QuickJS ships no Intl): the locale tables generated from Unicode CLDR by tools/intlgen, time zone arithmetic on
// the embedded IANA database, and the Unicode text segmentation rules (grapheme, word, sentence). The
// formatting itself is JavaScript (internal/engine/platformjs/intl*.js); see docs/web-platform.md.
package intl
