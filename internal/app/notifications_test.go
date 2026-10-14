package app

import (
	"testing"
	"time"

	"github.com/sweefbrowser/sweef/internal/notify"
)

func notifHarness(t *testing.T) (*harness, *notify.Recorder, string) {
	srv := pageServer(t)
	rec := notify.NewRecorder()
	h := newHarness(t, hopts{cfg: Config{NotifySink: rec, InitialURLs: []string{srv.URL + "/"}}})
	h.waitTitle("Home Page")
	return h, rec, srv.URL
}

func (h *harness) promptID() (id string) {
	h.loop(func() {
		if len(h.app.pw.prompts) > 0 {
			id = h.app.pw.prompts[0].id
		}
	})
	return
}

func TestNotificationPermissionPromptFlow(t *testing.T) {
	h, rec, base := notifHarness(t)
	e := h.app.activePage()
	var perm string
	h.pageEval(`Notification.permission`, &perm)
	if perm != "default" {
		t.Fatalf("initial permission %q", perm)
	}
	// no gesture: the question is not put to the user
	h.pageEval(`window.__r=null; Notification.requestPermission().then(function(v){window.__r=v}); 1`, nil)
	var r any
	h.waitFor("quiet answer", func() bool { h.pageEval(`window.__r`, &r); return r == "default" })
	if h.promptID() != "" {
		t.Fatal("prompted without a user gesture")
	}
	// with a gesture a prompt appears, in the state of the active tab
	e.t.MarkUserActivation()
	h.pageEval(`window.__r=null; Notification.requestPermission().then(function(v){window.__r=v}); 1`, nil)
	h.waitFor("prompt", func() bool { return h.promptID() != "" })
	h.app.post(h.app.markState)
	h.idle()
	h.loop(func() {
		p, _ := h.app.lastState["perm"].(map[string]any)["prompt"].(map[string]any)
		if p == nil || p["origin"] != base {
			t.Errorf("state prompt: %v", h.app.lastState["perm"])
		}
	})
	h.uiPost("perm.answer", map[string]any{"id": h.promptID(), "answer": "allow"})
	h.waitFor("granted", func() bool { h.pageEval(`window.__r`, &r); return r == "granted" })
	if h.app.prof.Sites.Notify(base) != "granted" {
		t.Fatal("the decision is not stored in the site settings")
	}
	// the page can now show a notification, which reaches the sink
	h.pageEval(`new Notification('hi',{body:'there'}); 1`, nil)
	h.waitFor("shown", func() bool { return len(rec.Shown()) == 1 })
	// do not disturb holds it back
	_ = h.app.prof.Settings.Set("notifications.dnd", true)
	h.pageEval(`new Notification('quiet'); 1`, nil)
	time.Sleep(200 * time.Millisecond)
	if len(rec.Shown()) != 1 {
		t.Fatal("do not disturb let a notification through")
	}
	// blocking from the site info
	h.uiPost("perm.set", map[string]any{"origin": base, "state": "denied"})
	h.pageEval(`Notification.permission`, &perm)
	if perm != "denied" {
		t.Fatalf("after block: %q", perm)
	}
}

func TestNotificationDismissEmbargo(t *testing.T) {
	h, _, base := notifHarness(t)
	e := h.app.activePage()
	for i := 0; i < 3; i++ {
		e.t.MarkUserActivation()
		h.pageEval(`Notification.requestPermission(); 1`, nil)
		h.waitFor("prompt", func() bool { return h.promptID() != "" })
		h.uiPost("perm.answer", map[string]any{"id": h.promptID(), "answer": "dismiss"})
	}
	if !h.app.prof.Sites.Embargoed(base, time.Now()) {
		t.Fatal("three dismissals must pause the prompt")
	}
	e.t.MarkUserActivation()
	var r any
	h.pageEval(`window.__r=null; Notification.requestPermission().then(function(v){window.__r=v}); 1`, nil)
	h.waitFor("denied without asking", func() bool { h.pageEval(`window.__r`, &r); return r == "denied" })
	if h.promptID() != "" {
		t.Fatal("prompted during the embargo")
	}
}

func TestIncognitoHasNoNotifications(t *testing.T) {
	srv := pageServer(t)
	h := newHarness(t, hopts{cfg: Config{Incognito: true, InitialURLs: []string{srv.URL + "/"}}})
	h.waitTitle("Home Page")
	var perm string
	h.pageEval(`Notification.permission`, &perm)
	if perm != "denied" || h.app.pw != nil {
		t.Fatalf("private window permission %q", perm)
	}
}
