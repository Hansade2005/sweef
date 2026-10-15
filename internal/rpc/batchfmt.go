package rpc

import (
	"encoding/json"
	"fmt"
	"strings"
)

// FormatBatch renders a Batch.run result as the step report the session CLI
// prints and the MCP browser_batch tool returns: one line per step, a summary
// line, then the observation. Step results longer than maxResult are cut.
// ok is false when a step failed.
func FormatBatch(raw json.RawMessage, maxResult int) (text string, ok bool) {
	var out struct {
		OK      bool   `json:"ok"`
		TotalMs int64  `json:"totalMs"`
		Ran     int    `json:"ran"`
		Total   int    `json:"total"`
		URL     string `json:"url"`
		Title   string `json:"title"`
		Obs     string `json:"observation"`
		ObsErr  string `json:"observationError"`
		Steps   []struct {
			Cmd   string `json:"cmd"`
			OK    bool   `json:"ok"`
			Ms    int64  `json:"ms"`
			Error string `json:"error"`
			Res   any    `json:"result"`
		} `json:"steps"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return string(raw), false
	}
	var sb strings.Builder
	for _, st := range out.Steps {
		if st.OK {
			fmt.Fprintf(&sb, "✓ %s (%dms)", st.Cmd, st.Ms)
			if v := shortResult(st.Res, maxResult); v != "" {
				sb.WriteString("  → " + v)
			}
			sb.WriteString("\n")
		} else {
			fmt.Fprintf(&sb, "✗ %s (%dms): %s\n", st.Cmd, st.Ms, st.Error)
		}
	}
	fmt.Fprintf(&sb, "%d/%d steps in %dms  %s  %s\n", out.Ran, out.Total, out.TotalMs, out.Title, out.URL)
	if out.Obs != "" {
		sb.WriteString(out.Obs)
	} else if out.ObsErr != "" {
		sb.WriteString("(observation failed: " + out.ObsErr + ")")
	}
	return sb.String(), out.OK
}

// shortResult renders a step result compactly.
func shortResult(v any, n int) string {
	switch x := v.(type) {
	case nil:
		return ""
	case map[string]any:
		for _, k := range []string{"text", "title", "url", "value", "count", "visible", "enabled", "checked", "result", "passed"} {
			if val, ok := x[k]; ok {
				if s, isStr := val.(string); isStr {
					return shortResult(s, n)
				}
				b, _ := MarshalCompact(val)
				return truncateStr(string(b), n)
			}
		}
		if len(x) == 1 {
			if _, ok := x["ok"]; ok {
				return ""
			}
		}
		b, _ := MarshalCompact(x)
		return truncateStr(string(b), n)
	case string:
		if strings.Contains(x, "\n") {
			return "\n" + truncateStr(x, n)
		}
		return truncateStr(x, n)
	}
	b, _ := MarshalCompact(v)
	return truncateStr(string(b), n)
}

func truncateStr(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	for n > 0 && s[n]&0xC0 == 0x80 { // do not cut a UTF-8 sequence
		n--
	}
	return s[:n] + "…"
}
