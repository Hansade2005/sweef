// Package auto opens the native window shell for the current OS: X11 on
// Linux, Win32 on Windows, Cocoa on macOS. It is the only package the app
// layer needs to import.
//
// Threading: call auto.Open from inside mainthread.Run on every OS (it is
// mandatory on macOS and harmless elsewhere) so the program has one shape.
package auto
