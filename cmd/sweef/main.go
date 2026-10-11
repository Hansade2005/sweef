// Command sweef is the single static binary: browser engine, MCP server,
// SDK socket server, CDP endpoint and CLI — all in one ~4-8 MB executable.
//
//	sweef mcp                     speak Model Context Protocol on stdio
//	sweef serve --addr 127.0.0.1:9222   TCP RPC for Node/Python SDKs
//	sweef fetch <url> [--text|--html]   one-shot page grab (agent-friendly)
//	sweef call <Method> ['{"json"}']    raw RPC over a running server
//	sweef snapshot <url>            aria tree of a URL, JSON to stdout
//	sweef eval <url> <expr>         run JS in a throwaway tab
//	sweef version / stats
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/sweefbrowser/sweef/internal/engine"
	"github.com/sweefbrowser/sweef/internal/mcp"
	"github.com/sweefbrowser/sweef/internal/rpc"
)

const version = "0.1.0"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd := os.Args[1]
	args := os.Args[2:]

	switch cmd {
	case "mcp":
		cmdMCP(args)
	case "serve":
		cmdServe(args)
	case "fetch":
		cmdFetch(args)
	case "snapshot":
		cmdSnapshot(args)
	case "eval":
		cmdEval(args)
	case "call":
		cmdCall(args)
	case "version", "--version":
		fmt.Println("sweef", version)
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", cmd)
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `sweef - the lightweight agent-first browser

usage:
  sweef mcp                          MCP server over stdio (for AI agents)
  sweef serve [--addr host:port]     RPC server for SDKs (Node/Python)
  sweef fetch <url> [--html|--text]  print page text (default) or HTML
  sweef snapshot <url>               accessibility tree as JSON
  sweef eval <url> <js-expression>   evaluate JS after load
  sweef call <Method> [params-json]  invoke RPC against --addr server
  sweef version
`)
}

func newEngine(fs *flag.FlagSet) *engine.Engine {
	opts := engine.DefaultOptions()
	maxRSS := fs.Int("max-rss-mib", opts.MaxRSSMiB, "soft resident-memory budget")
	noJS := fs.Bool("no-js", false, "disable JavaScript entirely (fetch+DOM only)")
	ua := fs.String("user-agent", opts.DefaultUserAgent, "default UA string")
	fs.Parse(os.Args[2:])
	opts.MaxRSSMiB = *maxRSS
	opts.DisableJS = *noJS
	opts.DefaultUserAgent = *ua
	return engine.New(opts)
}

func withSignals(ctx context.Context) (context.Context, context.CancelFunc) {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	c, cancel := context.WithCancel(ctx)
	go func() {
		select {
		case <-sigCh:
			cancel()
		case <-c.Done():
		}
	}()
	return c, cancel
}

func cmdMCP(args []string) {
	fs := flag.NewFlagSet("mcp", flag.ExitOnError)
	addCommonFlags(fs)
	_ = fs.Parse(args)
	e := engine.New(engineOptsFrom(fs))
	defer e.Close()
	srv := mcp.New(e, os.Stdout)
	ctx, cancel := withSignals(context.Background())
	defer cancel()
	if err := srv.Serve(ctx, os.Stdin); err != nil {
		fmt.Fprintln(os.Stderr, "mcp:", err)
		os.Exit(1)
	}
}

func cmdServe(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:9222", "listen address")
	_ = fs.Parse(args)
	e := engine.New(engineOptsFrom(fs))
	defer e.Close()
	srv := rpc.NewServer(e)
	ctx, cancel := withSignals(context.Background())
	defer cancel()
	fmt.Fprintf(os.Stderr, "sweef serving %s (SDK protocol SWF1)\n", *addr)
	if err := srv.ServeTCP(ctx, *addr); err != nil {
		fmt.Fprintln(os.Stderr, "serve:", err)
		os.Exit(1)
	}
}

func cmdFetch(args []string) {
	fs := flag.NewFlagSet("fetch", flag.ExitOnError)
	html := fs.Bool("html", false, "print raw HTML instead of text")
	width := fs.Int("width", 0, "if >0, print structured render at this width")
	rest := parseSkipURL(fs, args)
	if len(rest) == 0 {
		usage()
		os.Exit(2)
	}
	e := engine.New(engineOptsFrom(fs))
	defer e.Close()
	t, err := e.NewTab(rest[0])
	if err != nil {
		fail(err)
	}
	switch {
	case *html:
		fmt.Println(t.HTML(true))
	case *width > 0:
		fmt.Println(t.Screenshot(*width))
	default:
		txt, err := t.Text("body")
		if err != nil {
			fail(err)
		}
		fmt.Println(txt)
	}
}

func cmdSnapshot(args []string) {
	fs := flag.NewFlagSet("snapshot", flag.ExitOnError)
	rest := parseSkipURL(fs, args)
	if len(rest) == 0 {
		usage()
		os.Exit(2)
	}
	e := engine.New(engineOptsFrom(fs))
	defer e.Close()
	t, err := e.NewTab(rest[0])
	if err != nil {
		fail(err)
	}
	nodes, err := t.Snapshot()
	if err != nil {
		fail(err)
	}
	b, _ := json.MarshalIndent(nodes, "", " ")
	fmt.Println(string(b))
}

func cmdEval(args []string) {
	fs := flag.NewFlagSet("eval", flag.ExitOnError)
	rest := parseSkipURL(fs, args)
	if len(rest) < 2 {
		fmt.Fprintln(os.Stderr, "usage: sweef eval <url> <expression>")
		os.Exit(2)
	}
	e := engine.New(engineOptsFrom(fs))
	defer e.Close()
	t, err := e.NewTab(rest[0])
	if err != nil {
		fail(err)
	}
	res, err := t.Evaluate(strings.Join(rest[1:], " "))
	if err != nil {
		fail(err)
	}
	fmt.Println(string(res))
}

// cmdCall connects to a running `sweef serve` and issues one method.
func cmdCall(args []string) {
	fs := flag.NewFlagSet("call", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:9222", "server address")
	_ = fs.Parse(args)
	rest := fs.Args()
	if len(rest) == 0 {
		fmt.Fprintln(os.Stderr, "usage: sweef call <Method> [params-json]")
		os.Exit(2)
	}
	params := "{}"
	if len(rest) > 1 {
		params = rest[1]
	}
	if err := callRemote(*addr, rest[0], params); err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
