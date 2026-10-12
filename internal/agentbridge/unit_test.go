package agentbridge

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sweefbrowser/sweef/internal/agent"
	"github.com/sweefbrowser/sweef/internal/app"
)

func TestKeyStoreFileEnvAndRedaction(t *testing.T) {
	dir := t.TempDir()
	env := map[string]string{}
	get := func(k string) string { return env[k] }

	k := NewKeyStore(dir, get, true)
	if key, src := k.Get(); key != "" || src != "" {
		t.Fatalf("empty store returned %q %q", key, src)
	}
	env["ANTHROPIC_API_KEY"] = "sk-ant-env-0123456789abcdef"
	if key, src := k.Get(); key != env["ANTHROPIC_API_KEY"] || src != "env" {
		t.Fatalf("env fallback: %q %q", key, src)
	}
	if err := k.Save("has space in it 0123456789"); err == nil {
		t.Fatal("a key with spaces must be refused")
	}
	if err := k.Save("sk-ant-file-0123456789abcdef"); err != nil {
		t.Fatal(err)
	}
	// the stored key wins over the environment, and survives a new process
	k2 := NewKeyStore(dir, get, true)
	if key, src := k2.Get(); key != "sk-ant-file-0123456789abcdef" || src != "file" {
		t.Fatalf("persisted key: %q %q", key, src)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(filepath.Join(dir, keyFile))
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("key file: %v %v", fi, err)
		}
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".agent.key") {
			t.Fatalf("temp file left behind: %s", e.Name())
		}
	}
	// no env fallback when disabled
	k3 := NewKeyStore(t.TempDir(), get, false)
	if key, _ := k3.Get(); key != "" {
		t.Fatalf("env used although disabled: %q", key)
	}
	// clear removes file and memory; env stays as the dev fallback
	k2.Clear()
	if _, err := os.Stat(filepath.Join(dir, keyFile)); err == nil {
		t.Fatal("file survived Clear")
	}
	if key, src := k2.Get(); src != "env" || key == "" {
		t.Fatalf("after Clear: %q %q", key, src)
	}
	// memory-only store (no profile directory) never writes
	m := NewKeyStore("", get, false)
	if err := m.Save("sk-ant-mem-0123456789abcdef"); err != nil {
		t.Fatal(err)
	}
	if key, src := m.Get(); key == "" || src != "session" {
		t.Fatalf("memory store: %q %q", key, src)
	}
	// SetDir persists a key saved before the directory was known
	d2 := t.TempDir()
	m.SetDir(d2)
	if _, err := os.Stat(filepath.Join(d2, keyFile)); err != nil {
		t.Fatalf("SetDir did not persist: %v", err)
	}
	if got := redact("boom sk-ant-mem-0123456789abcdef boom", "sk-ant-mem-0123456789abcdef"); strings.Contains(got, "sk-ant") {
		t.Fatalf("not redacted: %s", got)
	}
}

func TestConfirmTablePairsEventAndWaiterInEitherOrder(t *testing.T) {
	req := agent.ConfirmRequest{RunID: "run-1", Kind: agent.ConfirmPayment, Summary: "Click 'Pay'"}
	var tbl confirmTable
	// event first
	ev := tbl.forEvent(req)
	w := tbl.forWait(req)
	if ev != w {
		t.Fatal("event then waiter must share an entry")
	}
	if !tbl.answer(ev.id, true) || !<-w.ans {
		t.Fatal("answer not delivered")
	}
	tbl.drop(w)
	if tbl.answer(ev.id, true) {
		t.Fatal("answer for a dropped entry")
	}
	// waiter first
	w2 := tbl.forWait(req)
	ev2 := tbl.forEvent(req)
	if w2 != ev2 {
		t.Fatal("waiter then event must share an entry")
	}
	// two identical requests at once get two entries
	a, b := tbl.forEvent(req), tbl.forEvent(req)
	if a == b || a == ev2 {
		t.Fatal("concurrent identical confirmations must not collide")
	}
	if tbl.answer("nope", true) {
		t.Fatal("unknown id accepted")
	}
}

func TestClassifyErrors(t *testing.T) {
	key := "sk-ant-secret-0123456789"
	cases := []struct {
		err  error
		code string
		retr int
	}{
		{&agent.APIError{Status: 401, Type: "authentication_error", Message: "bad key " + key}, "auth", 0},
		{&agent.APIError{Status: 429, Type: "rate_limit_error", RetryAfter: 12*time.Second + time.Millisecond}, "rate_limit", 13},
		{&agent.APIError{Status: 529, Type: "overloaded_error"}, "unavailable", 0},
		{&net.OpError{Op: "dial", Err: errors.New("refused")}, "network", 0},
		{context.DeadlineExceeded, "network", 0},
		{errors.New("something odd"), "agent", 0},
	}
	for _, c := range cases {
		code, msg, retry := classify(c.err, key)
		if code != c.code || retry != c.retr {
			t.Errorf("%v -> %s %d (want %s %d)", c.err, code, retry, c.code, c.retr)
		}
		if strings.Contains(msg, key) {
			t.Errorf("key in message: %s", msg)
		}
	}
}

func TestChatsPersistOnlyWithADirectory(t *testing.T) {
	dir := t.TempDir()
	var c chatStore
	c.setDir(dir)
	c.add("user", "what is the weather in Paris and what should I wear tomorrow", "what is the weather in Paris and what should I wear tomorrow")
	c.add("assistant", "Sunny. A light jacket.", "")
	var c2 chatStore
	c2.setDir(dir)
	l := c2.list()
	if len(l) != 1 || !strings.HasPrefix(l[0].Title, "what is the weather") {
		t.Fatalf("chat list: %+v", l)
	}
	title, msgs, ok := c2.open(l[0].ID)
	if !ok || len(msgs) != 2 || msgs[1]["role"] != "assistant" || title == "" {
		t.Fatalf("open: %v %v %v", title, msgs, ok)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(filepath.Join(dir, chatsFile))
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("chats file: %v %v", fi, err)
		}
	}
	// private: nothing on disk
	var mem chatStore
	mem.add("user", "secret plans", "secret plans")
	if len(mem.list()) != 1 {
		t.Fatal("in-memory chat missing")
	}
	mem.newChat()
	if mem.title() != "" {
		t.Fatal("newChat should clear the current chat")
	}
}

func TestTranslatorCoalescesTextAndOrdersEvents(t *testing.T) {
	h := New(Options{NoEnvKey: true})
	defer h.Close()
	out := make(chan app.AgentEvent, 256)
	r := &run{h: h, mode: modeAgent, ctx: context.Background()}
	tr := newTranslator(h, r, out)
	r.tr = tr
	go tr.pump()
	tr.emit(agent.Event{RunID: "run-9", Kind: agent.EventStatus, Text: "Working on it"})
	for i := 0; i < 200; i++ {
		tr.emit(agent.Event{RunID: "run-9", Kind: agent.EventText, Text: "x"})
	}
	tr.emit(agent.Event{RunID: "run-9", Kind: agent.EventAction, Tool: "act", Text: "Clicking 'Go'"})
	tr.push(item{fin: &finish{stats: agent.RunStats{Stop: "end_turn", ElapsedMs: 1234, OutputTPS: 55.55}}})
	var evs []app.AgentEvent
	for ev := range out {
		evs = append(evs, ev)
	}
	texts, total := 0, 0
	lastTextIdx, actionIdx := -1, -1
	for i, ev := range evs {
		if ev.RunID != "run-9" {
			t.Fatalf("event %d has run id %q", i, ev.RunID)
		}
		switch ev.Kind {
		case "text":
			texts++
			total += len(ev.Text)
			lastTextIdx = i
		case "action":
			if actionIdx < 0 {
				actionIdx = i
			}
		}
	}
	if total != 200 {
		t.Fatalf("text lost: %d of 200 bytes", total)
	}
	if texts > 20 {
		t.Fatalf("text not coalesced: %d messages for 200 deltas", texts)
	}
	if lastTextIdx > actionIdx {
		t.Fatalf("text arrived after the action that followed it")
	}
	last := evs[len(evs)-1]
	if last.Kind != "done" || last.Data["tps"].(float64) != 55.5 && last.Data["tps"].(float64) != 55.6 || last.Data["ms"].(int64) != 1234 {
		t.Fatalf("done event: %+v", last)
	}
	// the action was announced but never ran: it is closed as not run
	var states []string
	for _, ev := range evs {
		if ev.Kind == "action" {
			states = append(states, ev.Data["status"].(string))
		}
	}
	if len(states) != 2 || states[0] != "pending" || states[1] != "error" {
		t.Fatalf("action states %v", states)
	}
}

func TestBaseURLOverrideFromEnv(t *testing.T) {
	h := New(Options{Env: func(k string) string {
		if k == "SWEEF_AGENT_BASE_URL" {
			return " http://127.0.0.1:9 "
		}
		return ""
	}, NoEnvKey: true})
	defer h.Close()
	if h.opts.BaseURL != "http://127.0.0.1:9" {
		t.Fatalf("base url %q", h.opts.BaseURL)
	}
	if h.opts.Thinking != agent.ThinkingDisabled {
		t.Fatal("the sidebar defaults to thinking disabled")
	}
}

func TestSendWithoutAttachOrKeyFailsGracefully(t *testing.T) {
	h := New(Options{NoEnvKey: true})
	defer h.Close()
	ev := <-h.Send("hi", "ask", app.PageContext{})
	if ev.Kind != "error" || ev.Data["code"] != "auth" {
		t.Fatalf("no key: %+v", ev)
	}
	if h.Info().HasKey {
		t.Fatal("HasKey without a key")
	}
	h2 := New(Options{Env: func(k string) string {
		if k == "ANTHROPIC_API_KEY" {
			return testKey
		}
		return ""
	}})
	defer h2.Close()
	ev = <-h2.Send("hi", "ask", app.PageContext{})
	if ev.Kind != "error" || strings.Contains(ev.Text, testKey) {
		t.Fatalf("unattached: %+v", ev)
	}
	if !h2.Info().HasKey {
		t.Fatal("env key not detected")
	}
}
