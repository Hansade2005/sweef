package agent

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// TestRunCLIJSON checks the -json output used by the real-site benchmark
// (bench/realsites): exactly one JSON object on stdout with the done summary
// and run stats. It runs against the local fake of the Messages API.
func TestRunCLIJSON(t *testing.T) {
	api := newFakeAPI(t, func(n int, s *sw, body []byte) {
		s.start(100, 0, 0)
		s.tool(0, "toolu_1", "done", `{"summary":"answer 42","success":true}`)
		s.end("tool_use", 7)
	})
	t.Setenv("ANTHROPIC_API_KEY", "test-key-not-real")
	var out bytes.Buffer
	code := RunCLI([]string{"-json", "-base-url", api.srv.URL, "say 42"}, strings.NewReader(""), &out)
	if code != 0 {
		t.Fatalf("exit %d, out %q", code, out.String())
	}
	var got struct {
		Answer string   `json:"answer"`
		Stop   string   `json:"stop"`
		Stats  RunStats `json:"stats"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &got); err != nil {
		t.Fatalf("stdout is not a single JSON object: %v: %q", err, out.String())
	}
	if got.Answer != "answer 42" || got.Stats.Turns != 1 || got.Stats.Usage.OutputTokens == 0 {
		t.Fatalf("unexpected result %+v", got)
	}
}
