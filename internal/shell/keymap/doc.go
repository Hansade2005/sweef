// Package keymap holds the OS-independent tables and helpers the native
// shells share: X11 keysym, evdev, Windows virtual-key and macOS keyCode
// translation to DOM KeyboardEvent key/code names, UTF-16 helpers and DPI
// math. It has no OS dependencies so every table is unit-tested on Linux.
package keymap
