/* sweef://settings. Every control writes with settings.set {key, value}; Go is the source of truth. */
(function () {
  var SW = window.SW, S = window.S, h = SW.h, icon = SW.icon, post = SW.post, $ = function (id) { return document.getElementById(id); };
  var cur = (SW.query("s") || (location.hash || "").slice(1) || "general").split("/")[0], q = "";
  var SECS = [["general", "globe", S.set.nav.general], ["search", "search", S.set.nav.search], ["privacy", "shield", S.set.nav.privacy], ["agent", "spark", S.f(S.set.nav.agent)], ["appearance", "brush", S.set.nav.appearance], ["downloads", "download", S.set.nav.downloads], ["about", "info", S.f(S.set.nav.about)]];
  $("ttl").textContent = S.set.title; $("sic").appendChild(icon("search", 16)); $("sq").setAttribute("placeholder", S.set.search); $("sq").setAttribute("aria-label", S.set.search);
  var val = function (k, d) { return SW.setting(k, d); };
  function toggle(key, def) {
    var on = !!val(key, def), b = h("button.switch", { role: "switch", "aria-checked": on ? "true" : "false", onclick: function () { var n = b.getAttribute("aria-checked") !== "true"; b.setAttribute("aria-checked", n ? "true" : "false"); SW.set(key, n); } }); return b;
  }
  function select(key, opts, def) {
    var s = h("select.field", { "aria-label": key, onchange: function () { SW.set(key, s.value); } }, opts.map(function (o) { return h("option", { value: o[0], text: o[1], selected: String(val(key, def)) === o[0] ? true : null }); })); s.value = String(val(key, def)); return s;
  }
  function seg(key, opts, def) {
    var cur = String(val(key, def)), el = h("div.seg", { role: "radiogroup" });
    opts.forEach(function (o) { var b = h("button", { role: "radio", "aria-checked": cur === o[0] ? "true" : "false", onclick: function () { SW.set(key, typeof def === "number" ? +o[0] : o[0]); SW.$$("button", el).forEach(function (x) { x.setAttribute("aria-checked", x === b ? "true" : "false"); }); } }, o[1]); el.appendChild(b); }); return el;
  }
  function row(title, desc, ctl) { return h("div.srow", h("div.st", h("b", { text: title }), desc ? h("div.d", { text: S.f(desc) }) : null), ctl ? h("div.ctl", ctl) : null); }
  function group(title, rows) { return h("div.group", title ? h("div.gh", { text: title }) : null, rows); }
  var btn = function (t, f, cls) { return h("button.btn.sm" + (cls ? "." + cls : ""), { text: t, onclick: f }); };

  var P = {
    general: function () {
      var ds = SW.state.defaultBrowser;
      return [h("h2", S.set.nav.general), h("p.lead", "The basics."),
        group("On startup", [row("When Sweef opens", "", select("startup.mode", [["newtab", "Open the new tab page"], ["restore", "Continue where I left off"], ["pages", "Open specific pages"]], "newtab")), row("Startup pages", "Used when you pick \"Open specific pages\". One address per line.", h("input.field.w", { value: String(val("startup.pages", "")), placeholder: "https://example.com", onchange: function (e) { SW.set("startup.pages", e.target.value); } }))]),
        group("Default browser", [row(ds ? "Sweef is your default browser" : "Sweef is not your default browser", "Links from other apps open here.", ds ? h("span.badge.ok", "Default") : btn("Make default", function () { post("defaultBrowser.set"); }, "primary"))])];
    },
    search: function () {
      return [h("h2", S.set.nav.search), h("p.lead", "What the address bar uses when you type something that is not a web address."),
        group("", [row("Search engine", "Used for searches from the address bar and new tab page.", select("search.engine", [["duckduckgo", "DuckDuckGo"], ["google", "Google"], ["bing", "Bing"], ["brave", "Brave Search"], ["startpage", "Startpage"], ["ecosia", "Ecosia"]], "duckduckgo")),
          row("Show search suggestions", "Sends what you type to your search engine so it can suggest completions.", toggle("search.suggest", true)),
          row("Show \"Ask {brand}\" in the address bar", "A row that sends your text to {agent} instead of a search engine.", toggle("omni.askRow", true))])];
    },
    privacy: function () {
      return [h("h2", S.set.nav.privacy), h("p.lead", "Sweef blocks known trackers and tells you plainly what it does and does not do."),
        group("Tracking", [row("Tracker blocking", "Standard blocks known trackers. Strict also blocks more third-party content and can break some sites.", seg("privacy.trackers", [["standard", "Standard"], ["strict", "Strict"], ["off", "Off"]], "standard")),
          row("Send Global Privacy Control", "Tells sites you do not want your data sold or shared.", toggle("privacy.gpc", true)), row("Upgrade to HTTPS when possible", "Tries the secure version of a site first.", toggle("privacy.httpsUpgrade", true))]),
        group("Your data", [row("Clear browsing data on exit", "History, cookies and cache are removed when you quit.", toggle("privacy.clearOnExit", false)), row("Clear browsing data now", "Choose what to remove and for how long.", btn("Clear data...", function () { post("data.clear"); })), row("Site settings", "Per-site permissions and what each site stored.", btn("Open", function () { post("tab.new", { url: "sweef://settings#site" }); }))])];
    },
    agent: function () {
      var ag = SW.state.agent || {}, hasKey = ag.hasKey !== false, key = h("input.field.w", { type: "password", placeholder: S.ob.keyPlaceholder, "aria-label": "API key", autocomplete: "off" });
      var sites = (ag.sites || []).slice(0, 8);
      return [h("h2", S.f(S.set.nav.agent)), h("p.lead", S.f("{agent} reads the page you are on, answers, and acts in the browser. You stay in control.")),
        group("Model", [row("Model", "Chosen by {brand}. It cannot be changed here.", h("span.badge.acc", ag.model || "claude")), row("Anthropic API key", hasKey ? "A key is stored on this device (OS keychain, or a private file only you can read). It is only ever sent to Anthropic." : "No key yet. Get one at console.anthropic.com. It stays on this device.", h("div.ctl", [key, btn("Save", function () { if (/^sk-ant-/.test(key.value.trim())) { post("agent.setKey", { key: key.value.trim() }); key.value = ""; } }, "primary"), hasKey ? btn("Remove", function () { post("agent.clearKey"); }) : null]))]),
        group("Privacy and safety", [row("Default page visibility", "Whether {agent} can see the pages you visit. You can change this per site from the address bar.", select("agent.visibilityDefault", [["ask", "Ask me per site"], ["allowed", "Always allowed"], ["blocked", "Never"]], "ask")),
          row("Ask before sensitive actions", "{agent} stops and shows you the exact action before buying, sending or deleting anything.", toggle("agent.confirmations", true)), row("Default mode", "Ask only answers. Agent can click, type and navigate.", seg("agent.mode", [["ask", "Ask"], ["agent", "Agent"]], "ask"))]),
        group("Display", [row("Show speed readout", "Shows how long the first word took, measured on your machine.", toggle("agent.latencyStrip", true))]),
        sites.length ? group("Sites with a saved choice", sites.map(function (s) { return row(SW.host(s.origin), s.allowed ? "Allowed" : "Not allowed", btn("Forget", function () { post("agent.pageVisibility", { origin: s.origin, allowed: null }); })); })) : null];
    },
    appearance: function () {
      return [h("h2", S.set.nav.appearance), h("p.lead", "Light, dark or match your system."),
        group("", [row("Theme", "", seg("theme", [["light", "Light"], ["dark", "Dark"], ["system", "System"]], "system")), row("Density", "Compact makes the tab strip and toolbar slightly smaller.", seg("density", [["comfortable", "Comfortable"], ["compact", "Compact"]], "comfortable")),
          row("{agent} width", "Drag the panel edge to resize, or pick a preset.", seg("sidebar.width", [["320", "Narrow"], ["380", "Default"], ["480", "Wide"]], 380)), row("Show bookmarks bar", "", toggle("bookmarks.bar", false))])];
    },
    downloads: function () {
      return [h("h2", S.set.nav.downloads), h("p.lead", "Where your files go."),
        group("", [row("Location", String(val("downloads.dir", "~/Downloads")), btn("Change...", function () { post("downloads.chooseFolder"); })), row("Ask where to save each file", "", toggle("downloads.ask", false))])];
    },
    about: function () {
      var s = SW.state;
      return [h("h2", S.f(S.set.nav.about)), h("p.lead", "A fast, light browser with an agent built in."),
        h("div.group", h("div.about", SW.logo(56, "abl"), h("div", SW.wordmark(26, "abw2"), h("div.vv", S.set.version + " " + (s.version || "dev") + " "), h("span.badge", { text: S.set.alpha })))),
        group("", [row("Status: alpha", "Sweef is early. Some sites will not work yet, and we show a banner when we know that. If something breaks, open it in your system browser and tell us.", null), row("Report a problem", "", btn("Open issue tracker", function () { post("openExternal", { url: "https://github.com/sweefbrowser/sweef/issues" }); })), row("Engine", "Sweef draws pages and its own interface with a pure Go engine. No Chromium, no Electron.", null)])];
    }
  };
  var sc = SW.scroller($("main"), $("col"));
  function render() {
    var nav = $("nav"), col = $("col"); SW.clear(nav);
    SECS.forEach(function (s) { nav.appendChild(h("button", { "aria-current": s[0] === cur ? "true" : "false", onclick: function () { cur = s[0]; q = ""; $("sq").value = ""; sc.set(0); try { history.replaceState(null, "", "#" + cur); } catch (e) {} render(); } }, icon(s[1], 18), s[2])); });
    SW.clear(col);
    if (q) {
      var any = false; SECS.forEach(function (s) { var d = h("div", P[s[0]]()); if (d.textContent.toLowerCase().indexOf(q) >= 0) { any = true; SW.$$(".group", d).forEach(function (g) { if (g.textContent.toLowerCase().indexOf(q) >= 0) { col.appendChild(h("div.gh2", { style: { margin: "6px 4px", color: "var(--text-3)", fontWeight: 700, fontSize: "12px" }, text: s[2] })); col.appendChild(g); } }); } });
      if (!any) col.appendChild(h("div.empty2", S.f(S.page.noResults, { q: q })));
    } else SW.add(col, P[cur] ? P[cur]() : P.general());
    sc.refresh();
  }
  $("sq").addEventListener("input", function (e) { q = e.target.value.trim().toLowerCase(); render(); });
  SW.on("render", function () { var a = document.activeElement; if (a && a !== $("sq") && $("col").contains(a) && /INPUT|SELECT/.test(a.tagName)) return; render(); }); render(); SW.ready("settings");
})();
