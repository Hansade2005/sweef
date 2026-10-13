# Web fonts

`@font-face` fonts in WOFF2, WOFF, TrueType and OpenType (glyf outlines) load, are decoded in pure Go
(no CGO) and are used by layout, painting, canvas text and the `document.fonts` / `FontFace` API. Text is
never invisible and never held back by a font: it is laid out with the next family of the list first
(`font-display: swap` for every face, whatever the page asks for) and laid out again when the font arrives.

Code: `internal/font` (decoders, sanitiser, variable fonts, the per document `WebSet`, font matching),
`internal/style/fontface.go` (the rules), `internal/engine/webfonts.go` (fetching, policy),
`jsfonts.go` + `fonts_prelude.js` (page API), `cssimport.go` (`@import`, which web fonts depend on).

## How a font reaches the screen

1. **Declare.** Each layout pass hands the document's style sheets to `webFonts.sync`; the sheets that
   contain `@font-face` are parsed once (`style.ParseFontFaces`: `font-family`, `src` with `url()`,
   `format()`, `tech()`, `local()`, `font-weight` and `font-stretch` ranges, `font-style`, `unicode-range`,
   `font-display`, `font-feature-settings`, `font-variation-settings`, `size-adjust`, `ascent-override`,
   `descent-override`, `line-gap-override`) and put into the document's `font.WebSet`. A set exists only
   for documents that declare a face (or use the script API); the others pay nothing.
2. **Scope.** The style engine replaces every family of a `font-family` list that the set declares by a
   scoped name (`roboto\x00w7`). Layout, rasterisation and canvas resolve families by name, so they find
   the right document's faces without a parameter, and two tabs with different `Roboto`s never mix.
   Caches key on the set (`style.Persist.SetWebFonts`: the set and its declared families are part of the key;
   a font merely finishing to load is not, the styles stay valid).
3. **Need.** When text needs a character that no loaded face of a scoped family has, layout calls
   `font.DemandFamily`: CSS font matching (stretch, then style, then weight, ranges of variable fonts
   included) picks the group of faces; the faces of a group that differ only in `unicode-range` form one
   composite face (later declarations first); the first one whose range covers the character and is not
   loaded starts loading, once. Later families are asked only when this one has failed or loaded without the
   glyph. So a Google Fonts page with ten `unicode-range` subsets of a family fetches the subsets its text uses.
4. **Fetch.** `webFonts.run` orders the usable sources (local() before the first url(); then WOFF2, WOFF,
   TrueType/OpenType, the first listed among equals), skips formats it cannot read (`embedded-opentype`,
   `svg`, `collection`) and `tech()` it cannot draw (`color-COLRv0/v1`, `color-SVG`, `-sbix`, `-CBDT`,
   `palettes`, `incremental`, `features-aat`, `features-graphite`), and tries them in turn until one decodes.
   The request is made by `Tab.httpDo` (counted client, HTTP cache and its disk store, cookies, route
   interception, `Sec-Fetch-Dest: font`), kind `font`, at most six at a time, 20 s timeout, 8 MB per file,
   64 MB and 96 faces per page. One URL is one request however many faces use it (a variable font declared
   for several weights). Decoded fonts are kept per browser context by content hash (24 MB).
5. **CORS.** A font from another origin is used only if its response has `Access-Control-Allow-Origin: *` or
   the page's origin, as in browsers; the request carries `Origin`. Same-origin fonts send no `Origin`,
   `data:` URLs and `file:` pages are exempt. A refused font logs the usual console error.
6. **Decode** (below), then `WebSet` bumps its generation and the document lays out again: a window
   repaints (the update hook, at most every 40 ms), a headless document waits.
7. **Headless commands** (`shot`, `snapshot`, `batch`, MCP, `PaintRGBA` without `SetAsyncImages`) wait inside
   the layout provider for the fonts their layout asked for, within the settle budget (at least 500 ms), and
   lay out again; a font that arrives during a layout makes it run again (up to 6 rounds). A font slower
   than the budget does not hang the command: it is given up for that document, lands later and the document
   is invalidated. `Tab.FontsPending()` tells an owner of a window that something is still coming.
   The `load` event is never delayed by fonts.

## @import

Web fonts are often imported (`@import url(https://fonts.googleapis.com/css2?...)`) rather than linked, and the
engine did not read `@import` at all. `cssimport.go` now reads the leading `@import` rules of every `<style>` and
linked sheet (after `@charset` and comments), fetches the sheets through the usual style path (KindStyle: cache,
Data Saver, counted), puts them before the importing sheet in import order and strips the rules from the text. At
most 8 imports per sheet and 4 levels, cycles are cut, the import's media list applies (the parent's when it has none),
`layer()` and `supports()` are accepted and ignored. This changes the pages that use `@import` for the better (the
Python docs theme, for one, was missing its imported base sheet) and costs the bytes of those sheets.

## Decoders (`internal/font`)

* **WOFF 1.0** (`woff.go`): header, directory (sorted, no duplicates, 4-byte aligned, inside the file, no
  overlap, sizes capped), per table zlib with a hard cap equal to the declared length which must come out
  exactly, sfnt rebuilt with fresh checksums. Directory checksums are not trusted (real fonts have wrong ones;
  OTS does not check them either).
* **WOFF 2.0** (`woff2.go`): the variable length `UIntBase128` and `255UInt16` forms, the brotli stream
  (`github.com/dsnet/compress/brotli`, already a dependency for `Content-Encoding: br`) read with a cap of the
  announced size, the **glyf/loca transform** (triplet coordinates, flags, composite glyphs, bounding box bitmap,
  instructions, the OVERLAP_SIMPLE bitmap) and the **hmtx transform** reversed from the specification.
  Collections (`ttcf` flavor) are not supported.
* **Sanitiser** (`sanitize.go`), in the spirit of OTS: table directory, required tables (`head`, `hhea`,
  `maxp`, `hmtx`, `cmap` and `glyf`+`loca` or `CFF `), `head` magic and `unitsPerEm`, `numGlyphs` against `hmtx` and
  `loca`, monotonic `loca`, every glyph record parsed inside its interval (flags, coordinates, composite
  components and glyph references), no overlapping tables; then `golang.org/x/image/font/sfnt` parses the
  result (cmap, CFF). A font whose tables contradict each other is refused, not drawn.
* Hardening: every read is bounds checked (`rd`, sticky error), every count is checked against the bytes left,
  decompressed and rebuilt sizes are capped (`Limits`, 32 MB), `recover` guards each decoder and the glyph code
  of faces, and all errors wrap `font.ErrBadFont`. Fuzz targets: `FuzzDecodeWOFF`, `FuzzDecodeWOFF2`,
  `FuzzGlyfTransform`, `FuzzVariableFont` (`go test ./internal/font -fuzz FuzzDecodeWOFF2`).
* Start-up: nothing is initialised before the first font is decoded (no tables, no maps beyond a 63 entry array).

## Variable fonts

`fvar`, `avar`, `gvar` (shared and private tuples, intermediate regions, sparse deltas with inferred
deltas, composite glyphs) and `HVAR` (or the phantom points of `gvar`) are implemented for TrueType outlines
(`variation*.go`). A face is instantiated once per requested weight: `font-weight` (clamped to the declared
range and the axis) sets `wght`, `font-style: italic` sets `ital`/`slnt`, `wdth` stays 100, and the
descriptor `font-variation-settings` overrides any axis. Not supported: CFF2 variable fonts and `MVAR`/`VVAR`/
`cvar` (they draw their default instance), the element property `font-variation-settings`, `font-stretch`
steering `wdth`, optical sizing (`opsz` stays at its default).

## What is not supported

* Color fonts (COLR/CPAL, CBDT/CBLC, sbix, SVG): `tech()` hints skip them when the CSS offers another source;
  a face that is only a color font draws its monochrome outlines, which are usually empty.
* Font collections (`.ttc`, WOFF2 collections), `embedded-opentype`, SVG fonts.
* OpenType features other than `liga`/`clig`/`rlig` (GSUB type 4, also behind extension lookups; ligatures of up
  to 64 characters, so Material Icons words work) and GPOS pair kerning / `kern`. `calt`, `ss01`, `smcp`, Indic and
  Arabic shaping with web fonts are not applied; `font-feature-settings: "liga" 0` and `font-variant-ligatures:
  none` are honoured.
* Vertical writing metrics (`vhea`/`vmtx`/`VORG`) are not read.
* Synthetic bold and italic are the engine's existing rules (bold when the face is lighter than 600 and 600+ is
  asked for, oblique when the face is not italic and italic is asked for), as browsers do with `font-synthesis` on.
* Text in an inline `<svg>` that sets `font-family` on its own elements does not see web fonts (it does when it
  inherits the family from CSS). Canvas text does (`ctx.font` is scoped, and setting it starts the download of
  the face the first letter needs; text drawn before the font arrives uses the fallback, as in browsers).
* `font-display` values are all treated as `swap`; there is no invisible period and no `block` timeout.

## Page API

`document.fonts` is a `FontFaceSet`: `load(font, text)`, `check(font, text)`, `ready`, `status`, `add`,
`delete`, `clear`, `has`, `size`, iteration over the sheet's faces and the added ones, `onloading`,
`onloadingdone`, `onloadingerror` and `addEventListener`. `FontFace(family, source, descriptors)` takes a CSS `src`
string or an `ArrayBuffer`/typed array and has `load()`, `loaded`, `status` and the descriptor attributes; an
invalid descriptor throws `SyntaxError`, an undecodable source rejects `loaded` with `NetworkError`. Completion is
polled with a 4 ms timer while something loads, so a settling page waits for it like for any timer.
Reading `ready`/`status` lays the document out first (twice at most per document, only when the DOM changed, never
for a document without web fonts) so the fonts its text needs have been requested.

## Matching, sizes and metrics

`font-weight` follows the CSS Fonts 4 algorithm (400-500 look up to 500 first; below 400 go lighter first; above
500 go heavier first; a variable range matches inside it), `font-style` italic > oblique > normal, `font-stretch`
descriptors take part with a requested 100%. `size-adjust` scales glyphs, advances, kerning and metrics, and
`ascent-override`/`descent-override`/`line-gap-override` replace the line metrics (fractions of the adjusted size), which is
what the fallback faces generated by next/font (`src: local(Arial)`) rely on. `local()` finds an installed font
by full name, PostScript name or, as a convenience, family name.

## Data Saver

See `docs/data-saver-engine.md` (the `BlockFonts` and icon font rows). `fonts.googleapis.com` style sheet requests
present a Chrome token in the User-Agent (`fontCSSUserAgent`): Google answers an unknown browser with full TrueType
files of several hundred KB per weight and no subsets, and a modern Chrome with WOFF2 subsets of tens of KB.

## Tests

`internal/font` (`decode_test.go`, `variation_test.go`, `webfont_test.go`) with fonts generated by
`internal/font/fonttest` (a TrueType/WOFF/WOFF2/variable font writer; nothing licensed is committed, see NOTICE),
`internal/style/fontface_test.go`, and in `internal/engine`: `webfonts_test.go` (formats, source choice, unicode-range
requests, CORS, failures, data URLs), `webfonts_load_test.go` (FOUT in a window, headless waiting, pixel
equality with the same font installed, incremental layout under `SWEEF_LAYOUT_CHECK`, cache, icon ligatures, the
JS API, canvas), `webfonts_policy_test.go` (policy modes with byte counts, background tabs), `cssimport_test.go`.
