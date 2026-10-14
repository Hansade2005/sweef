//go:build linux

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// The release binary has a size budget (scripts/release/build.sh, SWEEF_MAX_BINARY_BYTES) and, below it, a ratchet:
// scripts/release/size-baseline.txt holds the size of the last accepted linux/amd64 build. This test builds the
// browser the way the release script does and fails when
//
//   - the stripped binary is over the budget, or
//   - it is more than baselineDrift bytes bigger than the baseline.
//
// It prints the sections and, when it fails or SWEEF_SIZE_TABLE=1, the per-package table (tools/sizeprof). To see
// where bytes go, or to add data without breaking the budget, read "Binary size budget" in docs/development.md.
//
// Run: go test ./cmd/sweef -run BinarySizeBudget -v   (about a minute; skipped with -short)
const baselineDrift = 1 << 20

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Dir(filepath.Dir(wd)) // cmd/sweef -> repository root
}

// noinlineFlags is the release script's -gcflags list (scripts/release/noinline-packages.txt).
func noinlineFlags(t *testing.T, root string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, "scripts/release/noinline-packages.txt"))
	if err != nil {
		t.Fatal(err)
	}
	var fl []string
	for _, l := range strings.Split(string(b), "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
			fl = append(fl, "-gcflags="+l+"=-l")
		}
	}
	if len(fl) == 0 {
		t.Fatal("scripts/release/noinline-packages.txt lists no package")
	}
	return fl
}

// releaseBuild builds cmd/sweef with the release script's flags and returns the file size.
func releaseBuild(t *testing.T, root, out string, strip bool) int64 {
	t.Helper()
	ld := "-buildid= -funcalign=16" // as scripts/release/build.sh
	if strip {
		ld = "-s -w -buildid= -funcalign=16"
	}
	args := append([]string{"build", "-trimpath", "-buildvcs=false"}, noinlineFlags(t, root)...)
	args = append(args, "-ldflags", ld, "-o", out, "./cmd/sweef")
	cmd := exec.Command("go", args...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, b)
	}
	fi, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Size()
}

func TestLinuxBinarySizeBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the browser; skipped with -short")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go tool in PATH")
	}
	if runtime.GOARCH != "amd64" && os.Getenv("SWEEF_FORCE_SIZE_TEST") == "" {
		t.Skip("the budget and the baseline are for linux/amd64")
	}
	root := repoRoot(t)
	script, err := os.ReadFile(filepath.Join(root, "scripts/release/build.sh"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`SWEEF_MAX_BINARY_BYTES:-(\d+)`).FindSubmatch(script)
	if m == nil {
		t.Fatal("cannot find the budget in scripts/release/build.sh")
	}
	budget, _ := strconv.ParseInt(string(m[1]), 10, 64)
	if v := os.Getenv("SWEEF_MAX_BINARY_BYTES"); v != "" {
		budget, _ = strconv.ParseInt(v, 10, 64)
	}
	bb, err := os.ReadFile(filepath.Join(root, "scripts/release/size-baseline.txt"))
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := strconv.ParseInt(strings.Fields(string(bb))[0], 10, 64)
	if err != nil {
		t.Fatalf("scripts/release/size-baseline.txt: %v", err)
	}

	dir := t.TempDir()
	stripped := filepath.Join(dir, "sweef")
	size := releaseBuild(t, root, stripped, true)
	t.Logf("stripped linux/amd64 binary: %d bytes (%.2f MiB); budget %d (%.2f MiB), headroom %d bytes (%.2f MiB); baseline %d (%+d)",
		size, float64(size)/1048576, budget, float64(budget)/1048576, budget-size, float64(budget-size)/1048576, baseline, size-baseline)

	over := size > budget
	drift := size > baseline+baselineDrift
	if over || drift || os.Getenv("SWEEF_SIZE_TABLE") == "1" {
		full := filepath.Join(dir, "sweef.full")
		releaseBuild(t, root, full, false)
		cmd := exec.Command("go", "run", "./tools/sizeprof", "-bin", full, "-stripped", stripped, "-top", "40")
		cmd.Dir = root
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Logf("sizeprof: %v", err)
		}
		t.Logf("where the bytes are:\n%s", out)
	}
	if over {
		t.Errorf("the binary is %d bytes, %d over the budget of %d. Shrink it (docs/development.md, \"Binary size budget\"); raising the budget needs a CHANGELOG line and a reason", size, size-budget, budget)
	}
	if drift {
		t.Errorf("the binary grew by %d bytes since scripts/release/size-baseline.txt (%d), more than the %d bytes a change may add without a decision. Shrink the addition (compress embedded data: internal/zpack), or, if the growth is the point of the change, update size-baseline.txt in the same commit and say why in the CHANGELOG", size-baseline, baseline, baselineDrift)
	}
}
