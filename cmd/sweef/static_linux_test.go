//go:build linux

package main

import (
	"debug/elf"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// Sweef is one static binary: no libc, no dynamic loader (it runs on Alpine/musl and in scratch containers). A
// dependency that loads shared libraries at run time (purego's Linux shim, pulled in by an audio or GUI library)
// turns a CGO_ENABLED=0 build into a dynamically linked one, which this catches.
func TestLinuxBinaryIsStaticallyLinked(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the browser (about 30 s cold); skipped with -short")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go tool in PATH")
	}
	out := filepath.Join(t.TempDir(), "sweef")
	cmd := exec.Command(goBin, "build", "-ldflags=-s -w", "-o", out, ".")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+runtime.GOARCH)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, b)
	}
	f, err := elf.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, p := range f.Progs {
		if p.Type == elf.PT_INTERP {
			t.Errorf("the binary has a program interpreter (PT_INTERP): it is dynamically linked")
		}
	}
	if libs, err := f.ImportedLibraries(); err != nil || len(libs) > 0 {
		t.Errorf("the binary needs shared libraries %v (err %v): it is dynamically linked", libs, err)
	}
	if s := f.Section(".dynamic"); s != nil {
		t.Errorf("the binary has a .dynamic section")
	}
}
