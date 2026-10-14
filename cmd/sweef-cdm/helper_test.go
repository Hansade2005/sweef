package main

import (
	"bytes"
	"context"
	"debug/elf"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/sweefbrowser/sweef/internal/media/cdm"
)

func buildHelper(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("builds the helper")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go tool")
	}
	out := filepath.Join(t.TempDir(), cdm.HelperName())
	cmd := exec.Command(goBin, "build", "-o", out, ".")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, b)
	}
	return out
}

func TestHelperFailsCleanlyWithoutALibrary(t *testing.T) {
	bin := buildHelper(t)

	// no -cdm at all
	var stderr bytes.Buffer
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), cdm.WidevineEnv+"=")
	cmd.Stderr = &stderr
	if err := cmd.Run(); err == nil || !bytes.Contains(stderr.Bytes(), []byte("no CDM library")) {
		t.Fatalf("err=%v stderr=%q", err, stderr.String())
	}

	// a library that is not there: the exit is immediate and the browser side sees a dead helper, not a hang
	stderr.Reset()
	start := time.Now()
	_, err := cdm.StartHelper(context.Background(), bin, []string{"-cdm", filepath.Join(t.TempDir(), "libwidevinecdm.so")}, nil, &stderr, cdm.InitParams{})
	if err == nil || !errors.Is(err, cdm.ErrHelperDied) {
		t.Fatalf("StartHelper: %v", err)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("took too long")
	}
	if !bytes.Contains(stderr.Bytes(), []byte("sweef-cdm:")) {
		t.Fatalf("helper said nothing: %q", stderr.String())
	}

	// a file that is not a shared library
	junk := filepath.Join(t.TempDir(), "libwidevinecdm.so")
	_ = os.WriteFile(junk, []byte("this is not an ELF file"), 0o755)
	stderr.Reset()
	if _, err := cdm.StartHelper(context.Background(), bin, []string{"-cdm", junk}, nil, &stderr, cdm.InitParams{}); !errors.Is(err, cdm.ErrHelperDied) {
		t.Fatalf("junk library: %v", err)
	}
}

// The helper is the one binary that may be dynamically linked (it dlopen's the CDM); on Linux that is the whole
// reason it is separate from the static browser binary.
func TestHelperIsDynamicallyLinkedOnLinux(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("ELF check")
	}
	bin := buildHelper(t)
	f, err := elf.Open(bin)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	interp := false
	for _, p := range f.Progs {
		interp = interp || p.Type == elf.PT_INTERP
	}
	if !interp {
		t.Fatal("the helper has no program interpreter: dlopen would not work")
	}
}
