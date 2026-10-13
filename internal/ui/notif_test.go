package ui_test

import (
	"strings"
	"testing"
)

func TestNotificationPromptIsAccessibleAndPostsAnswers(t *testing.T) {
	r, done := newRig(t)
	defer done()
	s := r.open("notif-prompt-light")
	r.run(s, typeInto)
	// a dialog with a name, a description and three real buttons; it does not take the focus by itself
	got := evalS(t, s, `var p=document.querySelector('.permpop');[p.getAttribute('role'),document.getElementById(p.getAttribute('aria-labelledby')).textContent,document.getElementById(p.getAttribute('aria-describedby')).textContent.length>10,Array.prototype.map.call(p.querySelectorAll('button'),function(b){return b.textContent}).join('/'),p.contains(document.activeElement)].join('|')`)
	if got != "alertdialog|news.example.com wants to|true|Allow/Block/Not now|false" {
		t.Fatalf("prompt: %v", got)
	}
	// the chrome command moves the focus to it, arrow keys move between the buttons, Enter activates
	r.run(s, `__send("ui.command",{name:"perm.focus"});`)
	if got := evalS(t, s, `document.activeElement.textContent`); got != "Allow" {
		t.Fatalf("perm.focus focused %v", got)
	}
	r.run(s, `var p=document.querySelector('.permpop');__key(p,'ArrowRight');`)
	if got := evalS(t, s, `document.activeElement.textContent`); got != "Block" {
		t.Fatalf("ArrowRight focused %v", got)
	}
	s.ClearPosts()
	r.run(s, `document.activeElement.click()`)
	if p := post(s, "perm.answer"); p == nil || p["id"] != "perm1" || p["answer"] != "block" {
		t.Fatalf("Block posts %v", s.Posts())
	}
	if n := evalS(t, s, `document.querySelectorAll('.permpop').length`).(float64); n != 0 {
		t.Fatalf("the prompt must go away once answered, %v left", n)
	}
	// Escape is "Not now"; Allow allows
	for _, c := range []struct{ run, want string }{
		{`document.querySelector('.permpop button.primary').click()`, "allow"},
		{`__key(document.querySelector('.permpop'),'Escape')`, "dismiss"},
	} {
		s = r.open("notif-prompt-dark")
		r.run(s, typeInto)
		r.run(s, c.run)
		if p := post(s, "perm.answer"); p == nil || p["answer"] != c.want {
			t.Fatalf("%s posts %v", c.want, s.Posts())
		}
		if e := s.Errors(); len(e) > 0 {
			t.Errorf("errors: %v", e)
		}
	}
}

func TestNotificationSiteInfoToastMenuAndSettings(t *testing.T) {
	r, done := newRig(t)
	defer done()
	// the site info entry shows the state and posts the change
	s := r.openSet("notif-siteinfo-light")
	if got := evalS(t, s, `Array.prototype.map.call(document.querySelectorAll('.vischoice .mi'),function(b){return b.textContent+':'+b.getAttribute('aria-checked')}).join(',')`); got != "Ask me:false,Allowed:true,Blocked:false" {
		t.Fatalf("site info chooser: %v", got)
	}
	s.ClearPosts()
	r.run(s, `document.querySelectorAll('.vischoice .mi')[2].click()`)
	if p := post(s, "perm.set"); p == nil || p["origin"] != "https://news.example.com" || p["name"] != "notifications" || p["state"] != "denied" {
		t.Fatalf("perm.set: %v", s.Posts())
	}
	// a web notification toast offers Open, its actions and Dismiss, and tells Go what happened
	s = r.openSet("notif-toast-dark")
	if got := evalS(t, s, `Array.prototype.map.call(document.querySelectorAll('.toast.notify')[0].querySelectorAll('button'),function(b){return b.getAttribute('aria-label')||b.textContent}).join(',')`); got != "Open,Reply,Dismiss" {
		t.Fatalf("toast buttons: %v", got)
	}
	s.ClearPosts()
	r.run(s, `document.querySelectorAll('.toast.notify')[0].querySelectorAll('button')[1].click()`)
	if p := post(s, "notify.action"); p == nil || p["id"] != "n1" || p["action"] != "reply" {
		t.Fatalf("notify.action: %v", s.Posts())
	}
	s.ClearPosts()
	r.run(s, `document.querySelector('.toast.notify button.x').click()`)
	if p := post(s, "notify.closed"); p == nil || p["id"] != "n2" || p["user"] != true {
		t.Fatalf("notify.closed: %v", s.Posts())
	}
	s = r.openSet("notif-toast-dark")
	r.run(s, `document.querySelector('.toast.notify').querySelector('button').click()`)
	if p := post(s, "notify.click"); p == nil {
		t.Fatalf("notify.click: %v", s.Posts())
	}
	// Do not disturb is in the main menu and flips the setting
	m := r.openSet("notif-menu-dnd-light")
	if got, _ := evalS(t, m, `Array.prototype.map.call(document.querySelectorAll('.pop .mi'),function(b){return b.textContent}).filter(function(x){return /disturb/i.test(x)}).join('|')`).(string); !strings.Contains(got, "Do not disturb") || !strings.Contains(got, "On:") {
		t.Fatalf("menu: %v", got)
	}
	m.ClearPosts()
	r.run(m, `Array.prototype.filter.call(document.querySelectorAll('.pop .mi'),function(b){return /disturb/i.test(b.textContent)})[0].click()`)
	if p := post(m, "settings.set"); p == nil || p["key"] != "notifications.dnd" || p["value"] != false {
		t.Fatalf("settings.set: %v", m.Posts())
	}
	// the settings section
	st := r.openSet("notif-settings-light")
	if got := evalS(t, st, `document.querySelectorAll('.srow').length`).(float64); got < 6 {
		t.Fatalf("settings rows: %v", got)
	}
	st.ClearPosts()
	r.run(st, `var rows=document.querySelectorAll('.srow');rows[rows.length-1].querySelector('.seg').querySelectorAll('button')[1].click()`)
	if p := post(st, "perm.set"); p == nil || p["state"] != "denied" {
		t.Fatalf("settings perm.set: %v", st.Posts())
	}
	if e := st.Errors(); len(e) > 0 {
		t.Errorf("errors: %v", e)
	}
}
