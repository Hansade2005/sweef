# internal/shell: native window shells

A shell shows an RGBA surface, delivers input events and exposes a few OS
integrations. It never renders. Everything is pure Go, `CGO_ENABLED=0` on every
target.

| Package | OS | How | Status |
|---|---|---|---|
| `x11` | Linux | hand-rolled X11 wire client, MIT-SHM + core `PutImage` fallback | **VERIFIED** on Xvfb (see below) |
| `win32` | Windows | `golang.org/x/sys/windows` + lazy `user32/gdi32/shcore/dwmapi/shell32` | compiled and vetted only, **UNVERIFIED at runtime** |
| `cocoa` | macOS | `github.com/ebitengine/purego` (Objective-C runtime + CoreGraphics) | compiled and vetted only, **UNVERIFIED at runtime** |
| `auto` | any | `auto.Open(cfg)` picks the right one by GOOS | thin |
| `mainthread` | any | `mainthread.Run/Do/Post`, main-thread handoff | tested with the channel loop; the AppKit loop is **UNVERIFIED** |
| `keymap` | any | key/code tables, UTF-16, DPI math, `CheckOpenURL` | unit-tested on Linux |
| `pixconv` | any | RGBA -> BGRX conversion (word-wise), unpremultiply | unit-tested + benchmarked |
| `evqueue` | any | unbounded, coalescing event queue | unit-tested |

## Usage

```go
func main() {
    mainthread.Run(func() {                 // app code runs on a goroutine
        win, err := auto.Open(shell.Config{Title: "Sweef", Width: 1280, Height: 800, Dark: true})
        ...
        for ev := range win.Events() { ... win.Present(frame, damage) ... }
    })                                      // returns when the func returns
}
```

Use `mainthread.Run` on **every** OS so the program has one shape. It is
mandatory on macOS (Cocoa lives on the process main thread; `cocoa.Open`
returns an error otherwise) and a cheap no-op loop on Linux and Windows.
`mainthread` pins the main goroutine to the main thread in `init()`; `Run` may
be called once per process.

## Threading model

* `Window` methods may be called from any goroutine. `Events()` delivers
  events in order; the OS loop never blocks on the consumer (unbounded queue
  that merges consecutive `EvMouseMove`, `EvResize`/`EvScale` and same-modifier
  `EvWheel`; deltas are summed). Do not rely on seeing every mouse-move.
* Linux: one reader goroutine owns the socket and dispatches events; requests
  are written under a mutex. Replies are matched by sequence number.
* Windows: `Open` starts a goroutine locked to an OS thread
  (`runtime.LockOSThread`) which creates the window and runs
  `GetMessage/DispatchMessage` for its whole life. Calls that must run on the
  window's thread (title, cursor, fullscreen, destroy, icon) are posted to it
  with a private `WM_APP` message. `Present` runs on the caller's goroutine
  (GDI draws into the window DC are thread-safe) and serialises with `WM_PAINT`
  through a mutex.
* macOS: everything touching AppKit runs on the main thread through
  `mainthread.Do/Post` (libdispatch main queue, serviced by `[NSApp run]`).
  Never call a blocking `Window` method (`Clipboard`, `SetClipboard`,
  `OpenURL`, `Close`) from inside an AppKit callback; they are only called
  from application goroutines.
* `Close()` is idempotent, closes `Events()` and releases native resources.
  A user closing the window does **not** destroy it: the shell sends
  `EvClose` and the app decides (and calls `Close`). On macOS Cmd+Q and Dock
  "Quit" also arrive as `EvClose`.

## Coordinates, scale, size

* `Config.Width/Height`, `Size()`, `Event.X/Y/W/H/DX/DY` are **logical**
  pixels. `Present` frames are in **device** pixels: `device = round(logical *
  scale)`. `Size()` returns `(logicalW, logicalH, scale)`; the frame the app
  paints should be `round(w*scale) x round(h*scale)`.
* `scale` sources: X11 `SWEEF_SCALE` > `GDK_SCALE` > `Xft.dpi/96` from
  `RESOURCE_MANAGER` (re-read live: `EvScale`); Windows `GetDpiForWindow/96`
  (per-monitor-v2 awareness is set without a manifest; `WM_DPICHANGED` gives
  `EvScale` and the window moves to the suggested rect); macOS
  `backingScaleFactor` (`EvScale` on `windowDidChangeBackingProperties:`).
* An initial `EvResize` is queued by `Open`. `EvResize.W/H` is the logical
  client size, `EvScale` also carries `W/H`.

## Events

| Kind | Fields | Notes |
|---|---|---|
| `EvResize` | `W,H,Scale` | client area; also after fullscreen toggles |
| `EvScale` | `Scale,W,H` | DPI/monitor change |
| `EvMouseMove` | `X,Y,Mods` | also when the pointer leaves the window (coordinates outside it) so hover can be cleared; includes drags |
| `EvMouseDown/Up` | `X,Y,Button,Mods` | `Button`: 0 left, 1 middle, 2 right, **3 back, 4 forward** (extension of the documented 0-2) |
| `EvWheel` | `X,Y,DX,DY,Mods,Precise` | logical px; **positive DY = content moves up** (DOM `deltaY` sign), positive DX = content moves left. Mouse wheels: one notch = 53 px (X11, Chromium's value), 100 px per 3 lines (Windows, honours the system line count), ~10 px per line (macOS non-precise). `Precise` is set for trackpads/high-resolution wheels (Windows when delta is not a multiple of 120, macOS `hasPreciseScrollingDeltas`); X11 never sets it |
| `EvKeyDown/Up` | `Key,Code,Mods,Repeat` | see below |
| `EvText` | `Text,Mods` | committed text, UTF-8 |
| `EvFocus` | `Focus` | key window / active window |
| `EvClose` | | user asked to close |
| `EvDropFiles` | `Files` | absolute local paths |

`Mods`: `ModShift`, `ModCtrl`, `ModAlt` (Option on macOS), `ModMeta` (Windows
key / Super / **Command**). The modifier state includes the key of the event
itself for key-down of a modifier and excludes it for key-up, like DOM.
AltGr is reported as the OS reports it (Windows: Ctrl+Alt; X11: none of the
four, it only changes `Key`).

### Key naming (DOM `KeyboardEvent`)

* `Code` is the physical key, layout independent (`KeyA`, `Digit1`, `Enter`,
  `ShiftLeft`, `ArrowLeft`, `NumpadEnter`, ...). Sources: X11 keycode-8 =
  evdev scan code, Windows scan code + extended flag, macOS `keyCode`.
* `Key` is layout, shift and caps-lock aware: `"a"`, `"A"`, `"é"`, `"Enter"`,
  `"Backspace"`, `"ArrowLeft"`, `"F5"`, `"Shift"`, `"Control"`, `"Alt"`,
  `"Meta"`, `"AltGraph"`, `"CapsLock"`, `"Dead"` (accent keys),
  `"Process"` (key consumed by an IME), `"Unidentified"`. Ctrl does not turn
  the key into a control character (`Ctrl+A` is `"a"`).
* `EvKeyDown` is sent for every press, including shortcuts. `EvText` follows
  only when the key typed text and no Ctrl/Alt/Meta shortcut modifier is held
  (AltGr text is delivered). Enter, Tab, Backspace, Escape never produce
  `EvText`; the page layer maps `EvKeyDown{Enter}` itself.
* `Repeat` is true for OS auto-repeat. X11 asks XKB for detectable auto-repeat;
  without the XKB extension repeats would appear as up/down pairs.
* Dead keys: X11 composes them itself (`dead_acute`+`e` -> `é` via Unicode NFC,
  dead+space -> the accent, unknown combination -> accent then character);
  Windows lets `WM_CHAR` deliver the composed character; macOS uses
  `interpretKeyEvents:` / `NSTextInputClient`.
* IME: Windows (`WM_IME_*` default handling, committed text arrives through
  `WM_CHAR`, surrogate pairs joined) and macOS (`NSTextInputClient`
  `insertText:`; `SetIMERect` positions the candidate window) deliver
  **committed** text only; the preedit string is not surfaced yet. X11 has no
  XIM/IBus support (compose via X keymap only).

## Damage

`Present(frame, damage)` copies only `damage ∩ frame.Rect` (device pixels,
frame coordinates equal window coordinates). Empty damage means the whole
frame. The shell keeps its **own copy** of the last presented pixels, so the
frame can be reused immediately and exposure/repaint requests from the OS
(X11 `Expose`, Windows `WM_PAINT`, macOS `drawRect:`) are answered from that
copy without involving the app. Parts of the window not covered by any
presented frame show black. Cost is proportional to the damaged area: the
RGBA to native pixel conversion runs only on those rows.

## Other services

* Clipboard: text only. X11 `CLIPBOARD` (owner + requestor, `UTF8_STRING`,
  `STRING`, `TARGETS`, INCR both ways, verified with 9 MB), Windows
  `CF_UNICODETEXT` (LF <-> CRLF converted), macOS `NSPasteboard`
  `public.utf8-plain-text`.
* Cursors: all nine `shell.Cursor` values; macOS has no public busy cursor
  (falls back to the arrow).
* `OpenURL` goes through `keymap.CheckOpenURL`: absolute URL with a real
  scheme only; `file:`, `javascript:`, `data:`, `about:`, `blob:`, drive
  letters, options and control characters are refused. Backends: `xdg-open`,
  `ShellExecuteW`, `NSWorkspace openURL:`.
* Fullscreen: EWMH `_NET_WM_STATE` (X11, with a resize-yourself fallback when
  no window manager runs), borderless monitor-sized window (Windows),
  `toggleFullScreen:` (macOS).
* Dark title bar: `_GTK_THEME_VARIANT=dark` hint (X11),
  `DWMWA_USE_IMMERSIVE_DARK_MODE` (Windows), dark Aqua appearance (macOS).
* Window icon: `Config.Icon` / `SetIcon(*image.RGBA)` (`shell.IconSetter`):
  `_NET_WM_ICON`, `WM_SETICON`, Dock icon.
* macOS only: `Config.UnifiedTitleBar` extends the content under a
  transparent title bar (traffic lights stay on top of the content).
* Drag and drop of files: XDND v5 target, `WM_DROPFILES`, `NSFilenamesPboardType`.

### Extensions to `shell.go` (all backwards compatible)

`Config.Icon`, `Config.UnifiedTitleBar`, `Event.Repeat`, `Event.Precise`,
`Button` values 3/4, `shell.IconSetter`. Concrete window types also export
optional methods (`SetIcon`, `x11.Window.Sync/UsesSHM`, `cocoa.Window.SetIMERect`).

## Status: what was verified

### Linux / X11: VERIFIED on Xvfb (`go test ./internal/shell/...`, also under `xvfb-run`)

Tests start their own Xvfb (never the ambient `DISPLAY`), use XTEST through a
second wire connection to inject input, and `GetImage` to compare pixels:

* Present full frame, damage rectangle (outside stays untouched), clipping of
  oversized frames, multi-strip core `PutImage` (1900x1000), MIT-SHM path and
  forced non-SHM path (`SWEEF_X11_NOSHM=1`), resize keeping the picture,
  Expose repaint after another client scribbled over the window.
* Mouse buttons, wheel (4/5/6/7), pointer coordinates under `GDK_SCALE=2`,
  keyboard: letters, Shift, Ctrl (no text), Enter, arrows, auto-repeat flag,
  focus in/out, WM_DELETE_WINDOW, title (`_NET_WM_NAME`), icon property,
  nine cursors without protocol errors, fullscreen fallback, `Xft.dpi`
  scale and live `EvScale`, `OpenURL` refusals.
* Clipboard between two clients including a 9 MB INCR transfer, `TARGETS`,
  latin-1 `STRING`, refusal of unknown targets; XDND file drop with a
  simulated source (`XdndEnter/Position/Drop/Finished`, `text/uri-list`).
* Pure logic: dead-key composition, keysym lookup with Shift/Caps/AltGr/
  keypad/NumLock/second XKB group, URI list parsing, word-wise pixel swap.
* `-race` clean (`go test -race` was run 5x).

Measured with `go run ./internal/shell/x11/cmd/shelldemo` on a 4-vCPU Xeon
VM under Xvfb 1920x1080x24 (software X server, no display refresh; numbers
are a *floor for the shell's own overhead*, not what a GPU desktop will show):

| Case | Mode | Throughput | `Present` median / p95 |
|---|---|---|---|
| full frame | MIT-SHM | **320 fps** | 3.1 ms / 3.7 ms |
| full frame | core PutImage | 214 fps | 4.5 ms / 5.6 ms |
| 10 % damage | MIT-SHM | **1319 fps** | 0.69 ms / 1.04 ms |
| 1 % damage | MIT-SHM | 6678 fps | 0.14 ms / 0.21 ms |

"Synced" (Present followed by a server round trip, i.e. time until the X
server holds the pixels) is within a few percent of those numbers. The pixel
conversion alone is 1.4 ms per 1080p frame (5.8 GB/s, `BenchmarkSwapRB1080p`).

### Windows: UNVERIFIED (never executed)

Compiled and vetted for `GOOS=windows` on `amd64`, `arm64` and `386` with
`CGO_ENABLED=0`. Verified on Linux: every key/scan/VK table, wheel math,
UTF-16 helpers, surrogate joining, DPI math, the URL policy and the exact size
and field offsets of all structs passed to Win32 (`structs_test.go`, checked
against the documented 32- and 64-bit layouts). **Not verified by running:**
window creation, message loop, DIB/BitBlt painting, DPI changes, IME, drop
files, clipboard, dark title bar, fullscreen, anything involving a real
`user32`. Expect first-run bugs in those paths.

### macOS: UNVERIFIED (never executed)

Compiled and vetted for `GOOS=darwin` on `amd64` and `arm64` with
`CGO_ENABLED=0`; a test program importing `auto` and `mainthread` links for
both. The key-code and scroll tables are unit-tested. **Not verified by
running:** the `[NSApp run]` handoff, runtime class registration (the
`NSView` subclass, `NSTextInputClient`, delegates) with purego callbacks,
struct passing (`NSRect`, `NSRange`) on both ABIs, `CGImage` drawing and its
orientation, tracking areas, cursor rects, pasteboard, dark appearance,
fullscreen, drag and drop. purego's callback and struct support was read in
its source (v0.11.1), not exercised.

## Known gaps

* X11: no XInput2 smooth scrolling (buttons 4-7 only, `Precise` never set),
  no XIM/IBus input methods, no primary selection, no clipboard manager
  hand-off (text is lost when the app exits), no Xcursor themes (core font
  cursors), no `_NET_WM_SYNC_REQUEST`, DPI is not per monitor.
* Windows: no preedit text, no touch/pen, no `WM_GETDPISCALEDSIZE` tweaks,
  no `ChangeWindowMessageFilterEx` (dropping from an elevated Explorer into a
  non-elevated app is blocked by UIPI).
* macOS: no preedit text, no native menus beyond Hide/Minimize/Quit, no wait
  cursor, text-only pasteboard, no per-screen color management (device RGB).
* All: no file dialogs (the comment in `shell.go` mentions them but the
  interface has none), single window per `Open` call is the tested shape.

## Running

```
go test ./internal/shell/...                       # starts its own Xvfb if installed
xvfb-run -a go test -race ./internal/shell/...
xvfb-run -a -s "-screen 0 1920x1080x24" go run ./internal/shell/x11/cmd/shelldemo
go run ./internal/shell/x11/cmd/shelldemo -live    # animate and print input events
GOOS=windows GOARCH=arm64 CGO_ENABLED=0 go vet ./internal/shell/...
GOOS=darwin  GOARCH=arm64 CGO_ENABLED=0 go vet ./internal/shell/...
```
