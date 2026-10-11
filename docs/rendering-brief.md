# Rendering stack: shared brief

Goal: close the "geometry and screenshots are approximate; no WebGL, canvas painting or media playback" gap. Sweef becomes a browser whose layout matches Chromium to the pixel for the CSS pages agents actually use, whose screenshots look like Chromium's, and whose canvas, WebGL and media APIs genuinely work. Everything is pure Go (no CGO), single static binary.

## How the pieces fit

```
HTML (golang.org/x/net/html tree, internal/net.Doc)
   │  + stylesheets (inline <style>, fetched <link>)
   ▼
internal/render   Layout(root, Options) -> Result{Boxes, List []Op, Hit}      (agent: layout)
   │                         ▲ Env: images, live bitmaps, intrinsic sizes
   ▼                         │
internal/raster   Paint(List, w, h, dpr) -> *image.RGBA                       (agent: raster+canvas)
                  Canvas2D context implementation shared with JS canvas
internal/gl       software WebGL (GLSL interpreter + rasterizer)             (agent: webgl)
internal/media    container parsers, audio decoders, WebAudio DSP             (agent: media)
internal/font     fonts, metrics, outlines (shared, already written)
internal/engine   Tab, VM, host bridge; extension hooks below
```

The contract between layout and rasterizer is `internal/render/displaylist.go`. Read it first. Additions are welcome (new Op types, new fields) but never change existing field meanings without telling the lead.

## Extension hooks in internal/engine (use these; do not edit jsvm.go/tab.go)

```go
// in your own file, e.g. internal/engine/canvas.go
func init() {
    registerPrelude("canvas", canvasPreludeJS)             // JS evaluated after the base prelude (name order)
    registerHostExtension("canvas", func(j *jsVM) map[string]hostFn {
        return map[string]hostFn{ "canvasFill": func(a []any) (any, error) { ... } }   // appears as __sw.canvasFill(...)
    })
}
```
- `hostFn = func(a []any) (any, error)`. Arguments arrive as int / float64 / string / bool / nil / []any / map[string]any. Return nil, bool, int, float64, string, []any or map[string]any; an error becomes a thrown JS exception. Test how large binary buffers cross the boundary (base64 or latin-1 "binary strings" are the safe options; measure cost for a 1280x720 RGBA buffer, ~3.7 MB) and pick the fastest that works.
- `j.tab` is the owning `*Tab` (nil only while a pooled VM is unbound; guard against it). `j.tab.dom` is the `*net.Doc` (use `Doc.NodeByID(id)` / `Doc.IDOf(node)`); in JS, `__swIdOf(element)` gives the id and `__swWrap(id)` the element. `j.tab.LogConsole(level, text)`, `j.tab.eng.opts`.
- Host functions run on the goroutine that is evaluating JS with `j.mu` held. Never call back into the VM from a host function. Long work should be fast (< a few ms) or chunked.
- Live pixels (canvas, WebGL drawing buffer, video frame) are published through `internal/engine/bitmap_registry.go`: `tab.SetBitmapSource(nodeID, src)`; layout/screenshots read them.
- Prelude conventions: see internal/engine/dom_prelude.js and jsbridge.md. Install globals on `globalThis`; classes should pass `instanceof`; use `Object.defineProperty` with non-enumerable methods; throw `DOMException`s with the right names. Libraries feature-detect aggressively: unsupported-but-present is worse than absent, so implement what you expose.
- HTMLCanvasElement: the **canvas agent owns** `HTMLCanvasElement.prototype.getContext`, `width`/`height`, `toDataURL`, `toBlob`, `captureStream`, `transferControlToOffscreen`. It exposes `globalThis.__swCanvasFactories = {}` (a map from context type to `function(canvas, attrs) -> context`) at the very start of its prelude; the WebGL agent registers `webgl`, `webgl2`, `experimental-webgl` factories there from its own prelude (named so it sorts after "canvas": prelude names are evaluated in alphabetical order: canvas < media < webgl).

## Verifying against Chromium (use it; claims must be measured)

- Chromium 1194 is at /opt/pw-browsers/chromium-1194/chrome-linux/chrome. Vercel's `agent-browser` CLI drives it: install path `/tmp/claude-0/-home-user-sweef/88880b6c-b9c0-5b8b-838a-110015e2aca6/scratchpad/ab/node_modules/.bin/agent-browser`; set `AGENT_BROWSER_EXECUTABLE_PATH` to the Chromium binary. Commands: `open URL`, `set viewport W H`, `eval 'js'` (returns a JSON-encoded string), `screenshot path`, `close`; use `--session NAME` so you do not collide with other agents (pick a unique session name).
- `go run ./tools/fidelity` scores Sweef against cached Chromium references in testdata/fidelity/.ref (element boxes + screenshots). Capture new references with `AB=<agent-browser> CHROME=<chromium> go run ./tools/fidelity -refresh -only NAME`. Add fixture pages to testdata/fidelity/*.html (static, self-contained, no network; use data: URIs for images).
- Fonts: Chromium on this machine resolves sans-serif/Arial to Liberation Sans, serif/Times New Roman to Liberation Serif, monospace to DejaVu Sans Mono (check `fc-match`), so Sweef matches it when it uses the same files. internal/font does the lookup; tune its generic mapping if Chromium disagrees.

## Rules

- Pure Go. Allowed modules: standard library, golang.org/x/{net,image,sys,text}, github.com/hajimehoshi/go-mp3, github.com/jfreymuth/oggvorbis (already pinned in internal/deps). Need another? `go get` it and add a blank import to internal/deps/deps.go (do not run `go mod tidy`; other agents are working at the same time and go.mod is shared).
- Stay inside the files/packages assigned to you. Other agents are editing other packages simultaneously, so a build error in a package you do not own is not yours: build and test only your own packages (`go build ./internal/yourpkg/... && go test ./internal/yourpkg/...`).
- Do not commit, push, or touch git (the lead commits). Run `gofmt -w` and `go vet` on your packages.
- Write real tests. Accuracy claims need numbers from the fidelity tool or from Chromium comparisons.
- When done, report: what exists (API, files), what you verified and the scores, and exactly what is NOT supported. Be blunt about gaps.
