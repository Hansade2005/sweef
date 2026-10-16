package main

import (
	"os/exec"
	"strings"
	"testing"
)

// sweef-cli exists to start fast: it must not link the engine (or anything else that initializes heavily).
// Its whole dependency graph inside this module is the session client, its batch formatter and the protocol.
func TestSweefCLIStaysEngineFree(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Skip("go list unavailable:", err)
	}
	allowed := map[string]bool{
		"github.com/sweefbrowser/sweef/cmd/sweef-cli":      true,
		"github.com/sweefbrowser/sweef/internal/cliclient": true,
		"github.com/sweefbrowser/sweef/internal/batchfmt":  true,
		"github.com/sweefbrowser/sweef/internal/protocol":  true,
	}
	for _, p := range strings.Fields(string(out)) {
		if strings.Contains(p, ".") && !allowed[p] && !strings.HasPrefix(p, "vendor/") && !strings.HasPrefix(p, "golang.org/x/sys") {
			t.Errorf("sweef-cli links %s", p)
		}
	}
}
