package main

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestMain lets the test binary stand in for the sweef binary: with
// SWEEF_CLI_TEST_MAIN=1 it runs main(), so the session CLI's daemon (which it
// starts with os.Executable()) is this binary too. No build step is needed.
func TestMain(m *testing.M) {
	if os.Getenv("SWEEF_CLI_TEST_MAIN") == "1" {
		main()
		os.Exit(0)
	}
	if os.Getenv("SWEEF_CLI_TEST_PARENT") != "" { // a child started without SWEEF_CLI_TEST_MAIN: never recurse
		os.Exit(3)
	}
	os.Setenv("SWEEF_CLI_TEST_PARENT", "1")
	os.Exit(m.Run())
}

// TestSessionCLISmoke drives the daemon-backed session CLI end to end against
// a local site: open, snapshot refs, click, fill, get, eval, batch, rpc,
// screenshot, dialog, close.
func TestSessionCLISmoke(t *testing.T) {
	if runtime.GOOS == "windows" {
		// the session daemon listens on an AF_UNIX socket, which Windows has since 10 1803 (Go supports it);
		// a runner without it skips rather than fails
		probe := filepath.Join(os.TempDir(), fmt.Sprintf("swprobe-%d.sock", os.Getpid()))
		l, err := net.Listen("unix", probe)
		if err != nil {
			t.Skipf("no AF_UNIX sockets on this Windows: %v", err)
		}
		l.Close()
		os.Remove(probe)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, `<title>Acme</title><a href="/login">Sign in</a><h1>Home</h1>
<button id=ask onclick="document.title = confirm('ok?') ? 'yes' : 'no'">Ask</button>`)
	})
	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, `<title>Sign in</title><form action="/welcome"><input id=email name=email><button type=submit>Go</button></form>`)
	})
	mux.HandleFunc("/welcome", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "<title>Welcome</title><h1>Hi %s</h1>", r.URL.Query().Get("email"))
	})
	site := httptest.NewServer(mux)
	defer site.Close()

	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	home, err := os.MkdirTemp("", "swcli") // short: unix socket paths are limited to ~104 bytes on macOS
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(home)
	env := append(os.Environ(), "SWEEF_CLI_TEST_MAIN=1", "SWEEF_HOME="+home, "SWEEF_CACHE_DIR="+filepath.Join(home, "cache"))
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(self, args...)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("sweef %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}
	defer func() { // with the env: without it the test binary would run the tests again
		c := exec.Command(self, "close")
		c.Env = env
		c.Run()
	}()

	if out := run("open", site.URL+"/"); !strings.Contains(out, "Acme") {
		t.Errorf("open: %q", out)
	}
	snap := run("snapshot", "-i")
	if !strings.Contains(snap, `link "Sign in" [ref=e1]`) {
		t.Fatalf("snapshot -i: %q", snap)
	}
	run("click", "@e1")
	if out := run("get", "title"); out != "Sign in" {
		t.Errorf("after click @e1: title %q", out)
	}
	run("fill", "#email", "ada@example.com")
	if out := run("get", "value", "#email"); out != "ada@example.com" {
		t.Errorf("get value: %q", out)
	}
	run("press", "Enter", "#email")
	if out := run("eval", "document.querySelector('h1').textContent"); !strings.Contains(out, "Hi ada@example.com") {
		t.Errorf("eval after submit: %q", out)
	}
	out := run("batch", "open "+site.URL+"/login", "fill #email bo@x.io", "click button", "expect url /welcome", "get text h1", "--observe", "none")
	if !strings.Contains(out, "5/5 steps") || !strings.Contains(out, "Hi bo@x.io") {
		t.Errorf("batch: %q", out)
	}
	if out := run("rpc", "Page.title"); out != `{"title":"Welcome"}` {
		t.Errorf("rpc Page.title: %q", out)
	}
	if out := run("--json", "get", "url"); !strings.Contains(out, `/welcome`) {
		t.Errorf("--json get url: %q", out)
	}
	png := filepath.Join(home, "shot.png")
	run("screenshot", png)
	if b, err := os.ReadFile(png); err != nil || len(b) < 100 || string(b[1:4]) != "PNG" {
		t.Errorf("screenshot file: %v (%d bytes)", err, len(b))
	}
	run("open", site.URL+"/")
	run("dialog", "accept")
	run("click", "#ask")
	if out := run("get", "title"); out != "yes" {
		t.Errorf("confirm() after dialog accept: title %q", out)
	}
	if out := run("close"); !strings.Contains(out, "closed") {
		t.Errorf("close: %q", out)
	}
}
