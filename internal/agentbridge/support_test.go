package agentbridge

import (
	"os"
	"testing"
	"time"
)

// rebase points the host at another API origin and drops the agent (and so the
// conversation); tests use it to recycle one GUI across scenarios.
func (h *Host) rebase(baseURL string) {
	h.mu.Lock()
	ag := h.ag
	h.ag = nil
	h.opts.BaseURL = baseURL
	h.needReset = false
	h.lastWarm = h.lastWarm.Add(-time.Hour)
	h.mu.Unlock()
	if ag != nil {
		ag.Stop()
		ag.Close()
	}
}

func TestMain(m *testing.M) {
	code := m.Run()
	if shared.e != nil {
		shared.e.close()
	}
	if shared.dir != "" {
		_ = os.RemoveAll(shared.dir)
	}
	os.Exit(code)
}

var _ = testing.Short
