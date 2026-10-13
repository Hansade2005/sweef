package preview

import (
	"encoding/json"
	"fmt"
	"strings"
)

type M = map[string]any

func tab(id, title, url string, kv ...any) M {
	t := M{"id": id, "title": title, "url": url}
	for i := 0; i+1 < len(kv); i += 2 {
		t[kv[i].(string)] = kv[i+1]
	}
	return t
}

// BaseState is a realistic full "state" snapshot.
func BaseState(platform string) M {
	return M{
		"platform": platform, "version": "0.1.0-alpha.3", "channel": "alpha", "theme": "light", "incognito": false,
		"window": M{"w": 1280, "h": 800, "scale": 1, "maximized": false},
		"active": "t1", "url": "https://www.wsj.com/personal-finance/taxes/2026-tax-brackets", "title": "What the 2026 tax brackets mean for your paycheck",
		"loading": false, "canBack": true, "canForward": false, "security": M{"level": "secure", "certValid": true}, "bookmarked": false,
		"tabs": []M{
			tab("p1", "Mail", "https://mail.example.com/inbox", "pinned", true),
			tab("p2", "Calendar", "https://calendar.example.com/", "pinned", true),
			tab("t1", "What the 2026 tax brackets mean for your paycheck", "https://www.wsj.com/personal-finance/taxes/2026-tax-brackets"),
			tab("t2", "Power Sporting Goods - Grocer", "https://grocer.example.com/store/power-sporting-goods", "loading", false),
			tab("t3", "Lo-fi beats to refactor to", "https://www.youtube.com/watch?v=abc", "audible", true),
			tab("t4", "New Tab", "sweef://newtab"),
			tab("t5", "sweef/sweef: a fast, light browser written in Go", "https://github.com/sweefbrowser/sweef"),
		},
		"sidebar":  M{"open": true, "width": 380},
		"settings": M{"theme": "light", "density": "comfortable", "agent.mode": "ask", "agent.latencyStrip": true, "agent.sources.page": true},
		"agent":    M{"state": "idle", "hasKey": true, "model": "claude-sonnet-5-5", "pageVisibility": "allowed", "chats": []M{{"id": "c1", "title": "Tax brackets explained", "when": "Today"}, {"id": "c2", "title": "Compare noise-cancelling headphones", "when": "Yesterday"}}},
		"page":     M{"type": "article"},
		"downloads": []M{
			{"id": "d1", "name": "sweef-0.1.0-alpha.3-linux-amd64.tar.gz", "state": "progress", "received": 18_400_000, "total": 31_000_000, "speed": 4_200_000},
			{"id": "d2", "name": "2026-tax-guide.pdf", "state": "done", "received": 2_100_000, "total": 2_100_000},
		},
		"topSites": []M{{"title": "GitHub", "url": "https://github.com/"}, {"title": "Hacker News", "url": "https://news.ycombinator.com/"}, {"title": "Wikipedia", "url": "https://en.wikipedia.org/"}, {"title": "YouTube", "url": "https://www.youtube.com/"}, {"title": "Grocer", "url": "https://grocer.example.com/"}, {"title": "Maps", "url": "https://maps.example.com/"}},
		"stats":    M{"memMB": 38, "tabs": 12},
	}
}

func with(base M, kv ...any) M {
	b, _ := json.Marshal(base)
	var c M
	_ = json.Unmarshal(b, &c)
	for i := 0; i+1 < len(kv); i += 2 {
		path := strings.Split(kv[i].(string), ".")
		cur := c
		for _, p := range path[:len(path)-1] {
			n, ok := cur[p].(M)
			if !ok {
				n = M{}
				cur[p] = n
			}
			cur = n
		}
		cur[path[len(path)-1]] = kv[i+1]
	}
	return c
}

func js(v any) string { b, _ := json.Marshal(v); return string(b) }

// ev builds a JS statement delivering an agent event.
func ev(kind string, kv ...any) string {
	e := M{"runId": "r1", "kind": kind}
	for i := 0; i+1 < len(kv); i += 2 {
		e[kv[i].(string)] = kv[i+1]
	}
	return fmt.Sprintf(`__send("agent.event", %s);`, js(e))
}

const answerMD = "Sure, here is the simple version of what this article says about the **2026 tax brackets**.\n\n### What is changing\n- Brackets are moving up, so you can earn a bit more before hitting a higher tax rate.\n- The changes come from inflation adjustments and the new tax law.\n\n### How much\n- Lower brackets: about a **4% bump**.\n- Higher brackets: about 2.3%.\n- More of your income is taxed at lower rates.\n\n| Bracket | Single | Married |\n|---|---|---|\n| 12% | up to $50,400 | up to $100,800 |\n| 22% | up to $105,700 | up to $211,400 |\n\nUse `standard deduction` unless you itemise. Want a worked example with your numbers?"

func chatScript() string {
	var b strings.Builder
	b.WriteString(`SW.sk.send("Can you explain this in simple terms?");`)
	b.WriteString(ev("thought", "text", "Reading the article, then I will pull out what changed and what it means for a normal paycheck.", "done", true))
	b.WriteString(ev("action", "id", "a1", "label", "Read the page", "detail", "wsj.com", "status", "ok", "ms", 38))
	b.WriteString(ev("text", "text", answerMD))
	b.WriteString(ev("done", "ttft", 412, "tps", 78, "sources", []M{{"title": "IRS: 2026 inflation adjustments", "url": "https://www.irs.gov/newsroom/2026"}, {"title": "Tax Foundation", "url": "https://taxfoundation.org/2026-brackets"}}))
	return b.String()
}

func agentRunScript() string {
	var b strings.Builder
	b.WriteString(`SW.set("agent.mode","agent");SW.sk.send("Heading to the beach with the kids tomorrow. Can you grab the usual beach-day stuff?");`)
	b.WriteString(ev("thought", "text", "I will start with sunscreen, then a towel and a hat. SPF 50 for the kids, and I will stop before checkout."))
	b.WriteString(ev("text", "text", "On it. I am adding the well-rated **SPF 50** sunscreen, a towel and a bucket hat."))
	b.WriteString(ev("action", "id", "a1", "label", "Searched for \"SPF 50 sunscreen\"", "status", "ok", "ms", 412))
	b.WriteString(ev("action", "id", "a2", "label", "Clicked Add on Mineral Sunscreen", "detail", "button.add-to-cart", "status", "ok", "ms", 187))
	b.WriteString(ev("action", "id", "a3", "label", "Typed in Search", "detail", "beach towel", "status", "ok", "ms", 96))
	b.WriteString(ev("action", "id", "a4", "label", "Clicking Add on Beach Towel", "status", "running"))
	b.WriteString(ev("confirm", "id", "c1", "title", "Place the order", "risk", "Spends money", "action", M{"click": "Place order (button)", "on": "grocer.example.com/checkout", "total": "$39.98"}))
	return b.String()
}

// Scenarios is the full preview/test matrix.
func Scenarios() []Scenario {
	out := []Scenario{}
	add := func(s Scenario) { out = append(out, s) }
	light := BaseState("linux")
	dark := with(light, "theme", "dark", "settings.theme", "dark")
	inc := with(light, "incognito", true, "tabs", []M{tab("t1", "New Tab", "sweef://newtab")}, "active", "t1", "url", "sweef://newtab", "title", "New Tab", "security", M{"level": "internal"})

	add(Scenario{Name: "chrome-light-chat", Page: "chrome/index.html", W: 1280, H: 800, State: light, Setup: chatScript(), Backdrop: "article"})
	add(Scenario{Name: "chrome-light-empty", Page: "chrome/index.html", W: 1280, H: 800, State: with(light, "page.type", "shop", "url", "https://grocer.example.com/store/power-sporting-goods", "title", "Power Sporting Goods - Grocer", "active", "t2"), Backdrop: "shop"})
	add(Scenario{Name: "chrome-dark-agent", Page: "chrome/index.html", W: 1280, H: 800, State: with(dark, "agent.state", "waiting", "page.type", "shop", "url", "https://grocer.example.com/store/power-sporting-goods", "active", "t2"), Setup: agentRunScript(), Backdrop: "shop"})
	omniItems := `[{"type":"history","title":"sweef/sweef: a fast, light browser written in Go","url":"https://github.com/sweefbrowser/sweef"},{"type":"history","title":"GitHub: Let's build from here","url":"https://github.com/"},{"type":"bookmark","title":"Pro Git book","url":"https://git-scm.com/book/en/v2"},{"type":"tab","title":"Lo-fi beats to refactor to","url":"https://www.youtube.com/watch?v=abc","tab":"t3"},{"type":"search","text":"git rebase vs merge"},{"type":"search","text":"git stash pop"}]`
	add(Scenario{Name: "chrome-omnibox", Page: "chrome/index.html", W: 1280, H: 800, State: with(light, "sidebar.open", false), Backdrop: "article",
		Setup: `var u=document.getElementById('url');u.focus();u.value='git';u.setSelectionRange(3,3);u.dispatchEvent(new Event('input',{bubbles:true}));__send('omni.suggestions',{id:SW.omniSeq,items:` + omniItems + `});`})
	add(Scenario{Name: "chrome-siteinfo", Page: "chrome/index.html", W: 1280, H: 800, State: with(light, "sidebar.open", false), Backdrop: "article",
		Setup: `document.getElementById('secbtn').click();document.querySelector('.pop .mi').click();`})
	add(Scenario{Name: "chrome-menu-dark", Page: "chrome/index.html", W: 1280, H: 800, State: with(dark, "sidebar.open", false), Backdrop: "article", Setup: `document.getElementById('menubtn').click();`})
	add(Scenario{Name: "chrome-palette", Page: "chrome/index.html", W: 1280, H: 800, State: with(light, "sidebar.open", false), Backdrop: "article",
		Setup: `SW.palette.open();`, Then: `var i=document.querySelector('#pal input');i.value='tax';i.dispatchEvent(new Event('input',{bubbles:true}));__send('omni.suggestions',{id:SW.omniSeq,items:[{"type":"history","title":"IRS: tax brackets 2026","url":"https://www.irs.gov/newsroom/2026"},{"type":"bookmark","title":"Tax Foundation","url":"https://taxfoundation.org/"}]});`})
	add(Scenario{Name: "chrome-find-downloads", Page: "chrome/index.html", W: 1280, H: 800, State: with(light, "sidebar.open", false), Backdrop: "article",
		Setup: `SW.chrome.run('find.open');var f=document.querySelector('#find input');f.value='bracket';f.dispatchEvent(new Event('input',{bubbles:true}));__send('find.result',{count:7,index:2});document.getElementById('dlbtn').click();`})
	add(Scenario{Name: "chrome-compat-darwin", Page: "chrome/index.html", W: 1280, H: 800, State: with(light, "platform", "darwin", "sidebar.open", false, "compat", M{"url": "https://www.wsj.com/personal-finance"}, "loading", true, "progress", 0.62), Backdrop: "article"})
	many := []M{}
	for i := 0; i < 22; i++ {
		many = append(many, tab(fmt.Sprintf("m%d", i), fmt.Sprintf("Research tab number %d about browsers", i+1), fmt.Sprintf("https://site%d.example.com/page", i)))
	}
	add(Scenario{Name: "chrome-windows-overflow", Page: "chrome/index.html", W: 1000, H: 640, State: with(light, "platform", "windows", "tabs", many, "active", "m9", "url", "https://site9.example.com/page", "sidebar.open", false), Backdrop: "article"})
	add(Scenario{Name: "newtab-light-page", Page: "newtab/index.html", W: 1280, H: 712, State: light, Skip: true})
	add(Scenario{Name: "chrome-newtab-light", Page: "chrome/index.html", W: 1280, H: 800, State: with(light, "sidebar.open", false, "url", "sweef://newtab", "title", "New Tab", "active", "t4", "security", M{"level": "internal"}, "bookmarksBar", []M{{"title": "Hacker News", "url": "https://news.ycombinator.com/"}, {"title": "GitHub", "url": "https://github.com/"}, {"title": "Pro Git", "url": "https://git-scm.com/book"}, {"title": "Maps", "url": "https://maps.example.com/"}}, "settings", M{"theme": "light", "density": "comfortable", "bookmarks.bar": true, "agent.mode": "ask", "agent.latencyStrip": true}), BackdropFrom: "newtab-light-page"})
	add(Scenario{Name: "chrome-dark-streaming", Page: "chrome/index.html", W: 1280, H: 800, State: with(dark, "agent.state", "working"), Backdrop: "article",
		Setup: `SW.sk.send("Fact-check the claim about the standard deduction");` + ev("thought", "text", "Looking up the 2026 standard deduction on irs.gov, then comparing it with the article.") + ev("action", "id", "a1", "label", "Searched the web", "detail", "2026 standard deduction irs", "status", "ok", "ms", 640) + ev("action", "id", "a2", "label", "Opened irs.gov/newsroom", "status", "running") + ev("text", "text", "The article says the **single** standard deduction is $16,100. Checking that against the IRS notice\u2026")})
	add(Scenario{Name: "chrome-light-rate-toasts", Page: "chrome/index.html", W: 1280, H: 800, State: with(light, "agent.state", "error"), Backdrop: "article",
		Setup: `SW.sk.send("Summarise this page");` + ev("error", "code", "rate_limit", "retryAfter", 24) + `SW.chrome.toast("Bookmarked");SW.chrome.toast("Download finished","ok",{label:"Open",type:"downloads.open",payload:{id:"d2"}});SW.chrome.toast("Sidekick can no longer see grocer.example.com","warn");`})
	add(Scenario{Name: "chrome-incognito", Page: "chrome/index.html", W: 1280, H: 800, State: with(inc, "sidebar.open", false), BackdropFrom: "newtab-incognito-page"})
	add(Scenario{Name: "design-system", Page: "__sheet/system.html", W: 1280, H: 1500, NoState: true})
	add(Scenario{Name: "design-system-dark", Page: "__sheet/system.html", Query: "?theme=dark", W: 1280, H: 1500, NoState: true})
	// ---- overlay
	ov := func(st string, extra M) string {
		m := M{"state": st, "viewport": M{"x": 0, "y": 88, "w": 900, "h": 712}, "task": "beach essentials", "step": "Clicking Add on Beach Towel", "cursor": M{"x": 585, "y": 172, "label": "Sidekick", "pressed": false}}
		for k, v := range extra {
			m[k] = v
		}
		return `__send("overlay", ` + js(m) + `);`
	}
	add(Scenario{Name: "overlay-acting", Page: "chrome/overlay.html", W: 1280, H: 800, NoState: true, Backdrop: "shopband", Setup: ov("acting", nil)})
	add(Scenario{Name: "overlay-control", Page: "chrome/overlay.html", W: 1280, H: 800, NoState: true, Backdrop: "shopband", Setup: ov("control", nil)})
	// ---- new tab
	add(Scenario{Name: "newtab-light", Page: "newtab/index.html", W: 1280, H: 800, State: light})
	add(Scenario{Name: "newtab-dark-typing", Page: "newtab/index.html", W: 1280, H: 800, State: dark, Setup: `var u=document.getElementById('q');u.focus();u.value='best ramen near me';u.setSelectionRange(18,18);u.dispatchEvent(new Event('input',{bubbles:true}));__send('omni.suggestions',{id:SW.omniSeq,items:[{"type":"search","text":"best ramen near me open now"},{"type":"search","text":"best ramen recipe"},{"type":"history","title":"Ichiran Ramen - Menu","url":"https://ichiran.example.com/menu"}]});`})
	add(Scenario{Name: "newtab-incognito-page", Page: "newtab/index.html", Query: "?incognito=1", W: 1280, H: 712, State: inc, Skip: true})
	add(Scenario{Name: "newtab-incognito", Page: "newtab/index.html", W: 1280, H: 800, State: inc})
	// ---- onboarding
	for i := 1; i <= 4; i++ {
		setup := fmt.Sprintf(`__send("onboarding.step",{n:%d});`, i)
		if i == 1 {
			setup += `__send("import.progress",{source:"chrome",pct:0.64});__send("import.done",{source:"firefox",bookmarks:212});__send("import.unavailable",{source:"safari"});`
		}
		if i == 2 {
			setup += `window.__demoFinish&&window.__demoFinish();`
		}
		add(Scenario{Name: fmt.Sprintf("onboarding-%d", i), Page: "onboarding/index.html", Query: "?demo=done", W: 1280, H: 800, State: light, Setup: setup})
	}
	add(Scenario{Name: "onboarding-4-dark", Page: "onboarding/index.html", W: 1280, H: 800, State: dark, Setup: `__send("onboarding.step",{n:4});`})
	add(Scenario{Name: "onboarding-3-data-picked", Page: "onboarding/index.html", W: 1280, H: 800, State: light, Setup: `__send("onboarding.step",{n:3});document.querySelectorAll(".plan")[0].click();`})
	add(Scenario{Name: "onboarding-3-data-dark", Page: "onboarding/index.html", W: 1280, H: 800, State: dark, Setup: `__send("onboarding.step",{n:3});document.querySelectorAll(".plan")[1].click();`})
	add(Scenario{Name: "onboarding-3-data-incognito", Page: "onboarding/index.html", W: 1280, H: 800, State: inc, Setup: `__send("onboarding.step",{n:3});`})
	// ---- internal pages
	add(Scenario{Name: "settings-agent", Page: "settings/index.html", Query: "?s=agent", W: 1280, H: 800, State: with(light, "agent.sites", []M{{"origin": "https://www.wsj.com", "allowed": true}, {"origin": "https://bank.example.com", "allowed": false}})})
	add(Scenario{Name: "settings-privacy-dark", Page: "settings/index.html", Query: "?s=privacy", W: 1280, H: 800, State: dark})
	add(Scenario{Name: "settings-about", Page: "settings/index.html", Query: "?s=about", W: 1280, H: 800, State: light})
	hist := `var n=Date.now(),m=60000;__send("data.result",{kind:"history",id:1,total:9,items:[{id:"1",title:"What the 2026 tax brackets mean for your paycheck",url:"https://www.wsj.com/personal-finance/taxes",ts:n-5*m},{id:"2",title:"sweef/sweef: a fast, light browser written in Go",url:"https://github.com/sweefbrowser/sweef",ts:n-22*m},{id:"3",title:"Power Sporting Goods - Grocer",url:"https://grocer.example.com/store/power",ts:n-70*m},{id:"4",title:"Hacker News",url:"https://news.ycombinator.com/",ts:n-95*m},{id:"5",title:"Lo-fi beats to refactor to",url:"https://www.youtube.com/watch?v=abc",ts:n-26*60*m},{id:"6",title:"IRS: Inflation adjustments for 2026",url:"https://www.irs.gov/newsroom/2026",ts:n-27*60*m},{id:"7",title:"Wikipedia: Heat pump",url:"https://en.wikipedia.org/wiki/Heat_pump",ts:n-28*60*m}]});`
	add(Scenario{Name: "history", Page: "history/index.html", W: 1280, H: 800, State: light, Setup: hist})
	add(Scenario{Name: "bookmarks-dark", Page: "bookmarks/index.html", W: 1280, H: 800, State: dark, Setup: `__send("data.result",{kind:"bookmarks",id:1,total:4,items:[{id:"1",title:"Pro Git book",url:"https://git-scm.com/book/en/v2"},{id:"2",title:"Tax Foundation",url:"https://taxfoundation.org/"},{id:"3",title:"Hacker News",url:"https://news.ycombinator.com/"},{id:"4",title:"Recipes: weeknight ramen",url:"https://recipes.example.com/ramen"}]});`})
	add(Scenario{Name: "downloads", Page: "downloads/index.html", W: 1280, H: 800, State: with(light, "downloads", []M{{"id": "d1", "name": "sweef-0.1.0-alpha.3-linux-amd64.tar.gz", "url": "https://sweef.dev/dl/sweef.tar.gz", "state": "progress", "received": 18_400_000, "total": 31_000_000, "speed": 4_200_000}, {"id": "d2", "name": "2026-tax-guide.pdf", "url": "https://www.irs.gov/pub/guide.pdf", "state": "done", "received": 2_100_000, "total": 2_100_000}, {"id": "d3", "name": "installer.exe", "url": "https://files.example.com/installer.exe", "state": "failed", "received": 0, "total": 0}})})
	// ---- error pages

	// ---- Data Saver
	dsLight := with(light, "data", dataState(0.394, "standard"), "sidebar.open", false)
	dsDark := with(dark, "data", dataState(0.394, "standard"), "sidebar.open", false)
	dsInc := with(inc, "data", dataState(0.394, "standard"), "sidebar.open", false)
	dsWarn := with(light, "data", dataState(0.78, "strict"), "sidebar.open", false)
	dsWarn2 := with(dark, "data", dataState(0.93, "strict"), "sidebar.open", false)
	dsOut := with(light, "data", dataState(1.04, "strict"), "sidebar.open", false)
	dsFree := with(light, "data", dataFree(), "sidebar.open", false)
	pop := `document.getElementById('databtn').click();`
	add(Scenario{Name: "data-popover-light", Page: "chrome/index.html", W: 1280, H: 800, State: dsLight, Backdrop: "article", Setup: pop})
	add(Scenario{Name: "data-popover-dark", Page: "chrome/index.html", W: 1280, H: 800, State: dsDark, Backdrop: "article", Setup: pop})
	add(Scenario{Name: "data-popover-incognito", Page: "chrome/index.html", W: 1280, H: 800, State: dsInc, BackdropFrom: "newtab-incognito-page", Setup: pop})
	add(Scenario{Name: "data-popover-warn", Page: "chrome/index.html", W: 1280, H: 800, State: dsWarn, Backdrop: "article", Setup: pop})
	add(Scenario{Name: "data-popover-exhausted", Page: "chrome/index.html", W: 1280, H: 800, State: dsOut, Backdrop: "article", Setup: pop})
	add(Scenario{Name: "data-popover-nolimit", Page: "chrome/index.html", W: 1280, H: 800, State: dsFree, Backdrop: "article", Setup: pop})
	add(Scenario{Name: "data-chip-toast-light", Page: "chrome/index.html", W: 1280, H: 800, State: dsWarn, Backdrop: "article",
		Setup: holdToast(`SW.chrome.toast({text:"You have used 70% of your 100 MB this month. 22 MB left.",kind:"info",action:{label:"Switch to Text first",type:"data.setLevel",payload:{level:"strict"}}});`)})
	add(Scenario{Name: "data-chip-toast-dark", Page: "chrome/index.html", W: 1280, H: 800, State: dsWarn2, Backdrop: "article",
		Setup: holdToast(`SW.chrome.toast({text:"About 7 MB left this month. It resets in 19 days.",kind:"warn"});`)})
	add(Scenario{Name: "data-chip-exhausted", Page: "chrome/index.html", W: 1280, H: 800, State: dsOut, Backdrop: "article",
		Setup: holdToast(`SW.chrome.toast({text:"You have used your 100 MB this month. It resets in 19 days.",kind:"warn",action:{label:"Raise limit",type:"tab.new",payload:{url:"sweef://settings#data"}}});`)})
	add(Scenario{Name: "data-settings-light", Page: "settings/index.html", Query: "?s=data", W: 1280, H: 2000, State: dsLight})
	add(Scenario{Name: "data-settings-dark", Page: "settings/index.html", Query: "?s=data", W: 1280, H: 2000, State: dsDark})
	add(Scenario{Name: "data-settings-incognito", Page: "settings/index.html", Query: "?s=data", W: 1280, H: 2000, State: dsInc})
	add(Scenario{Name: "data-settings-nolimit", Page: "settings/index.html", Query: "?s=data", W: 1280, H: 2000, State: with(dsFree, "data.available", true)})
	add(Scenario{Name: "data-newtab-light", Page: "newtab/index.html", W: 1280, H: 800, State: with(dsLight, "url", "sweef://newtab")})
	add(Scenario{Name: "data-newtab-dark-warn", Page: "newtab/index.html", W: 1280, H: 800, State: with(dsWarn2, "theme", "dark")})
	add(Scenario{Name: "data-newtab-incognito", Page: "newtab/index.html", W: 1280, H: 800, State: dsInc})
	add(Scenario{Name: "data-stop-light", Page: "datastop/index.html", Query: "?url=https%3A%2F%2Fgrocer.example.com%2Fstore%2Fpower-sporting-goods", W: 1280, H: 800, State: dsOut})
	add(Scenario{Name: "data-stop-dark", Page: "datastop/index.html", Query: "?url=https%3A%2F%2Fgrocer.example.com%2Fstore%2Fpower-sporting-goods", W: 1280, H: 800, State: with(dark, "data", dataState(1.04, "strict")), Setup: `document.querySelectorAll(".acts .btn")[1].click();`})
	add(Scenario{Name: "data-stop-incognito", Page: "datastop/index.html", Query: "?url=https%3A%2F%2Fgrocer.example.com%2F", W: 1280, H: 800, State: with(inc, "data", dataState(1.04, "strict"))})
	add(Scenario{Name: "data-sidekick-light", Page: "chrome/index.html", W: 1280, H: 800, State: with(light, "data", dataState(0.394, "standard")), Backdrop: "article",
		Setup: `SW.sk.send("Compare the three best-reviewed trail shoes under $120");` + ev("action", "id", "a1", "label", "Searched the web", "detail", "best trail shoes under $120", "status", "ok", "ms", 842) + ev("action", "id", "a2", "label", "Read runnersworld.com", "status", "ok", "ms", 311) + ev("text", "text", "I compared three well-reviewed pairs: the **Speedgoat 5** at $119, the **Peregrine 13** at $110 and the **Challenger 7** at $104. The Challenger is the best all-rounder at the lowest price.") + ev("done", "ttft", 388, "tps", 82, "data", M{"bytes": 421_888, "savedEstimated": 1_310_720, "skipped": 41, "lite": true, "available": true})})
	add(Scenario{Name: "data-sidekick-dark", Page: "chrome/index.html", W: 1280, H: 800, State: with(dark, "data", dataState(0.394, "standard")), Backdrop: "article",
		Setup: `SW.sk.send("Summarise the pricing page");` + ev("text", "text", "The **Team** plan is $12 per seat per month, billed yearly.") + ev("done", "ttft", 402, "tps", 77, "data", M{"bytes": 96_256, "skipped": 6, "lite": true, "available": true})})
	add(Scenario{Name: "error-dns", Page: "error/index.html", Query: "?kind=dns&url=https%3A%2F%2Fexample-typo.test%2Fhello", W: 1280, H: 800, State: light})
	add(Scenario{Name: "error-cert-dark", Page: "error/index.html", Query: "?kind=cert&url=https%3A%2F%2Fexpired.example.com%2F&detail=certificate+expired+2026-03-01&open=1", W: 1280, H: 800, State: dark})
	add(Scenario{Name: "error-offline", Page: "error/index.html", Query: "?kind=offline&url=https%3A%2F%2Fwww.wsj.com%2F", W: 1280, H: 800, State: light})
	add(Scenario{Name: "error-crash", Page: "error/index.html", Query: "?kind=crash&url=https%3A%2F%2Fheavy.example.com%2F", W: 1280, H: 800, State: light})
	return out
}

// dataState is state.data for a 100 MB monthly budget with pct of it used.
func dataState(pct float64, level string) M {
	used := int64(pct * 100 * (1 << 20))
	left := int64(100*(1<<20)) - used
	if left < 0 {
		left = 0
	}
	return M{
		"level": level, "levelSetting": level, "available": true, "used": used, "usedToday": 4_300_000, "agentLite": true, "showMeter": true, "incognito": false,
		"budget": M{"enabled": true, "limitMB": 100, "period": "month", "resetDay": 1, "warnAt": []int{70, 90}, "hardStop": pct > 1,
			"usedMB": float64(used) / (1 << 20), "leftMB": float64(left) / (1 << 20), "pct": pct * 100, "resetsInDays": 19, "resetsOn": "2026-11-01", "exhausted": pct >= 1, "usedBytes": used, "limitBytes": 100 * (1 << 20)},
		"top":   []M{{"origin": "https://www.wsj.com", "bytes": 11_200_000}, {"origin": "https://grocer.example.com", "bytes": 8_400_000}, {"origin": "https://en.wikipedia.org", "bytes": 5_100_000}, {"origin": "https://github.com", "bytes": 3_300_000}, {"origin": "https://news.ycombinator.com", "bytes": 1_200_000}},
		"saved": M{"measured": 6_100_000, "estimated": 21_400_000},
		"tab": M{"used": 655_360, "origin": "https://www.wsj.com", "bySrc": []M{{"origin": "https://www.wsj.com", "bytes": 412_000}, {"origin": "https://cdn.wsj.net", "bytes": 201_000}}, "skipped": 23, "skippedBy": M{"image": 18, "font": 3, "frame": 2},
			"saved": M{"measured": 317_440, "estimated": 1_992_294}, "approx": true, "rule": "", "once": false, "canLoad": 12},
		"kinds": M{"document": 61_000, "style": 88_000, "script": 190_000, "image": 281_000, "font": 0, "xhr": 35_000},
		"sites": M{"https://news.example.org": "lite", "https://maps.example.com": "full"},
	}
}

// dataFree is state.data with a saver level but no budget (unlimited).
func dataFree() M {
	d := dataState(0.394, "standard")
	d["budget"] = M{"enabled": false, "limitMB": 100, "period": "month", "resetDay": 1, "warnAt": []int{70, 90}, "hardStop": false, "usedMB": 41.3, "leftMB": 0, "pct": 0, "resetsInDays": 19, "resetsOn": "2026-11-01", "exhausted": false, "usedBytes": 43_300_000, "limitBytes": 0}
	d["used"] = 43_300_000
	return d
}

// holdToast runs js (which shows a toast) with the toast's dismiss timer disabled: a toast times out on the wall
// clock, so a scenario that keeps stepping for a few seconds (the paint golden test types into the omnibox
// afterwards) would lose it at a different step depending on how fast the machine is.
func holdToast(js string) string {
	return `(function(st){window.setTimeout=function(f,d){if(d>=3000)return 0;return st.apply(this,arguments)};try{` + js + `}finally{window.setTimeout=st}})(window.setTimeout);`
}
