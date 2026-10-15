package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"strconv"
	"time"

	"github.com/sweefbrowser/sweef/internal/engine"
	"github.com/sweefbrowser/sweef/internal/protocol"
)

// addCommonFlags registers the shared engine-tuning flags on a subcommand
// FlagSet so every entry point (mcp/serve/fetch/snapshot/eval) exposes the
// same knobs: --max-rss-mib, --no-js, --user-agent.
func addCommonFlags(fs *flag.FlagSet) {
	opts := engine.DefaultOptions()
	fs.Int("max-rss-mib", opts.MaxRSSMiB, "soft resident-memory budget")
	fs.Bool("no-js", false, "disable JavaScript entirely (fetch+DOM only)")
	fs.String("user-agent", opts.DefaultUserAgent, "default UA string")
	fs.Int("settle-ms", opts.SettleMs, "JavaScript event-loop budget after load and after each action")
	fs.Int("js-mem-mib", opts.JSMemoryMiB, "QuickJS heap cap per tab")
	fs.Int("js-timeout-ms", opts.JSTimeoutMs, "single script evaluation timeout")
	fs.Bool("allow-trackers", false, "load third-party ad, analytics and tracking requests (blocked by default for agents)")
}

// engineOptsFrom builds Options from the common flags registered by
// addCommonFlags (see main.go). Values are read through the generic
// flag.Value interface via strconv — no unsafe type assertions.
func engineOptsFrom(fs *flag.FlagSet) engine.Options {
	opts := engine.DefaultOptions()
	if f := fs.Lookup("max-rss-mib"); f != nil {
		if n, err := strconv.Atoi(f.Value.String()); err == nil {
			opts.MaxRSSMiB = n
		}
	}
	if f := fs.Lookup("no-js"); f != nil {
		opts.DisableJS = f.Value.String() == "true"
	}
	if f := fs.Lookup("user-agent"); f != nil {
		opts.DefaultUserAgent = f.Value.String()
	}
	// agent-facing entry points block known trackers unless asked not to (engine trackers.go)
	opts.BlockTrackers = os.Getenv("SWEEF_ALLOW_TRACKERS") == ""
	if f := fs.Lookup("allow-trackers"); f != nil && f.Value.String() == "true" {
		opts.BlockTrackers = false
	}
	for name, dst := range map[string]*int{"settle-ms": &opts.SettleMs, "js-mem-mib": &opts.JSMemoryMiB, "js-timeout-ms": &opts.JSTimeoutMs} {
		if f := fs.Lookup(name); f != nil {
			if n, err := strconv.Atoi(f.Value.String()); err == nil {
				*dst = n
			}
		}
	}
	return opts
}

func parseSkipURL(fs *flag.FlagSet, args []string) []string {
	// split args at first non-flag token so `fetch <url> --html` and
	// `fetch --html <url>` both work; then Parse the flags we know.
	var flags, rest []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if len(a) > 1 && a[0] == '-' {
			flags = append(flags, a)
			// consume value for flags that take one (heuristic: next token not starting with '-')
			if i+1 < len(args) && len(args[i+1]) > 0 && args[i+1][0] != '-' && needsValue(fs, a) {
				flags = append(flags, args[i+1])
				i++
			}
		} else {
			rest = append(rest, a)
		}
	}
	fs.Parse(append(flags, "--"))
	return rest
}

func needsValue(fs *flag.FlagSet, name string) bool {
	f := fs.Lookup(trimDashes(name))
	if f == nil {
		return false
	}
	// bool flags don't consume a value
	_, isBool := f.Value.(interface{ IsBoolFlag() bool })
	return !isBool
}

func trimDashes(s string) string {
	for len(s) > 0 && s[0] == '-' {
		s = s[1:]
	}
	return s
}

// callRemote dials a running `sweef serve`, sends one framed request, prints result.
func callRemote(addr, method, paramsJSON string) error {
	d := net.Dialer{Timeout: 5 * time.Second}
	c, err := d.Dial("tcp", addr)
	if err != nil {
		return err
	}
	defer c.Close()
	conn := protocol.NewConn(c)
	msg := &protocol.Message{JSONRPC: "2.0", ID: 1, Method: method, Params: json.RawMessage(paramsJSON)}
	if err := conn.Write(msg); err != nil {
		return err
	}
	// The server multiplexes event.* notifications on the same socket; skip
	// frames until we get the reply that carries our request ID.
	var resp *protocol.Message
	for {
		m, _, err := conn.Read(make([]byte, 4096))
		if err != nil {
			return err
		}
		if m.ID == 1 {
			resp = m
			break
		}
	}
	if resp.Error != nil {
		return resp.Error
	}
	// pretty print
	var v any
	json.Unmarshal(resp.Result, &v)
	out, _ := json.MarshalIndent(v, "", " ")
	fmt.Println(string(out))
	return nil
}
