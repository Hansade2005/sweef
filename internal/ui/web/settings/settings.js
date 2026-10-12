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

  var T = S.set, F = S.f;
  var P = {
    general: function () {
      var g = T.general, ds = SW.state.defaultBrowser;
      return [h("h2", T.nav.general), h("p.lead", g.lead),
        group(g.gStart, [row(F(g.when), "", select("startup.mode", [["newtab", g.newtab], ["restore", g.restore], ["pages", g.pages]], "newtab")), row(g.pagesT, g.pagesD, h("input.field.w", { value: String(val("startup.pages", "")), placeholder: "https://example.com", "aria-label": g.pagesT, onchange: function (e) { SW.set("startup.pages", e.target.value); } }))]),
        group(g.gDef, [row(F(ds ? g.isDef : g.notDef), g.defD, ds ? h("span.badge.ok", g.defBadge) : btn(g.make, function () { post("defaultBrowser.set"); }, "primary"))])];
    },
    search: function () {
      var g = T.search_;
      return [h("h2", T.nav.search), h("p.lead", g.lead),
        group("", [row(g.engine, g.engineD, select("search.engine", T.engines, "duckduckgo")), row(g.suggest, g.suggestD, toggle("search.suggest", true)), row(F(g.askRow), g.askRowD, toggle("omni.askRow", true))])];
    },
    privacy: function () {
      var g = T.privacy;
      return [h("h2", T.nav.privacy), h("p.lead", F(g.lead)),
        group(g.gTrack, [row(g.track, g.trackD, seg("privacy.trackers", [["standard", g.std], ["strict", g.strict], ["off", g.off]], "standard")), row(g.gpc, g.gpcD, toggle("privacy.gpc", true)), row(g.https, g.httpsD, toggle("privacy.httpsUpgrade", true))]),
        group(g.gData, [row(g.clearExit, g.clearExitD, toggle("privacy.clearOnExit", false)), row(g.clearNow, g.clearNowD, btn(g.clearBtn, function () { post("data.clear"); })), row(g.site, g.siteD, btn(g.open, function () { post("tab.new", { url: "sweef://settings#site" }); }))])];
    },
    agent: function () {
      var g = T.agent, ag = SW.state.agent || {}, hasKey = ag.hasKey !== false, key = h("input.field.w", { type: "password", placeholder: S.ob.keyPlaceholder, "aria-label": g.key, autocomplete: "off" }), sites = (ag.sites || []).slice(0, 8);
      return [h("h2", F(T.nav.agent)), h("p.lead", F(g.lead)),
        group(g.gModel, [row(g.model, g.modelD, h("span.badge.acc", ag.model || "claude")), row(g.key, hasKey ? g.keyOn : g.keyOff, [key, btn(g.save, function () { if (/^sk-ant-/.test(key.value.trim())) { post("agent.setKey", { key: key.value.trim() }); key.value = ""; } }, "primary"), hasKey ? btn(g.remove, function () { post("agent.clearKey"); }) : null])]),
        group(g.gSafe, [row(g.vis, g.visD, select("agent.visibilityDefault", [["ask", g.visAsk], ["allowed", g.visAllowed], ["blocked", g.visBlocked]], "ask")), row(g.confirm, g.confirmD, toggle("agent.confirmations", true)), row(g.mode, g.modeD, seg("agent.mode", [["ask", S.agent.ask], ["agent", S.agent.agent]], "ask"))]),
        group(g.gDisp, [row(g.latency, g.latencyD, toggle("agent.latencyStrip", true))]),
        sites.length ? group(g.gSites, sites.map(function (s) { return row(SW.host(s.origin), s.allowed ? g.allowed : g.blocked, btn(g.forget, function () { post("agent.pageVisibility", { origin: s.origin, allowed: null }); })); })) : null];
    },
    appearance: function () {
      var g = T.appearance;
      return [h("h2", T.nav.appearance), h("p.lead", g.lead),
        group("", [row(g.theme, "", seg("theme", [["light", g.light], ["dark", g.dark], ["system", g.system]], "system")), row(g.density, g.densityD, seg("density", [["comfortable", g.comfy], ["compact", g.compact]], "comfortable")),
          row(F(g.width), g.widthD, seg("sidebar.width", [["320", g.narrow], ["380", g.dflt], ["480", g.wide]], 380)), row(g.bmbar, "", toggle("bookmarks.bar", false))])];
    },
    downloads: function () {
      var g = T.downloads;
      return [h("h2", T.nav.downloads), h("p.lead", g.lead), group("", [row(g.loc, String(val("downloads.dir", "~/Downloads")), btn(g.change, function () { post("downloads.chooseFolder"); })), row(g.ask, "", toggle("downloads.ask", false))])];
    },
    about: function () {
      var s = SW.state, g = T.aboutP;
      return [h("h2", F(T.nav.about)), h("p.lead", g.lead),
        h("div.group", h("div.about", SW.logo(56, "abl"), h("div", SW.wordmark(26, "abw2"), h("div.vv", T.version + " " + (s.version || "dev") + " "), h("span.badge", { text: T.alpha })))),
        group("", [row(g.status, F(g.statusD), null), row(g.report, "", btn(g.issues, function () { post("openExternal", { url: "https://github.com/sweefbrowser/sweef/issues" }); })), row(g.engine, F(g.engineD), null)])];
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
