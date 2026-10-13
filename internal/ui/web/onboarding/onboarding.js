/* First run: three steps, always skippable. */
(function () {
  var SW = window.SW, S = window.S, h = SW.h, icon = SW.icon, post = SW.post, $ = function (id) { return document.getElementById(id); };
  var step = 1, TOTAL = 4, imp = {}, keyOK = false, demoTimers = [], pr = { trackers: "standard", vis: "ask" }, plan = null, deflt = false;
  var reduce = false; try { reduce = !!(window.matchMedia && matchMedia("(prefers-reduced-motion: reduce)").matches); } catch (e) {}
  $("logo").appendChild(SW.wordmark(24, "obw")); $("skip").textContent = S.ob.skipAll; $("skip").addEventListener("click", function () { post("onboarding.skip"); });
  function dots() { var d = $("dots"); SW.clear(d); for (var i = 1; i <= TOTAL; i++) d.appendChild(h("i" + (i === step ? ".on" : i < step ? ".done" : ""))); d.setAttribute("aria-valuenow", step); d.setAttribute("aria-valuetext", S.f(S.ob.step, { n: step, total: TOTAL })); }
  function foot() {
    var f = $("foot"); SW.clear(f);
    if (step > 1) f.appendChild(h("button.btn.ghost", { onclick: function () { go(step - 1); } }, icon("back", 16), S.ob.back));
    f.appendChild(h("span.sp")); f.appendChild(h("span.hint", { text: S.f(S.ob.step, { n: step, total: TOTAL }) }));
    f.appendChild(h("button.btn.primary.lg", { onclick: function () { if (step < TOTAL) go(step + 1); else { post("onboarding.done", { trackers: pr.trackers, visibility: pr.vis }); } } }, step < TOTAL ? S.ob.next : S.ob.finish, icon(step < TOTAL ? "forward" : "bolt", 16)));
  }
  function go(n) { step = n; demoTimers.forEach(clearTimeout); demoTimers = []; render(); }
  var SRC = [["chrome", S.ob.srcChrome, "#4C8BF5"], ["edge", S.ob.srcEdge, "#0FA3B1"], ["firefox", S.ob.srcFirefox, "#F2762E"], ["safari", S.ob.srcSafari, "#3AA0FF"]];
  function srcCard(s) {
    var st = imp[s[0]] || {}, cls = "src" + (st.busy ? " busy" : "") + (st.done ? " done" : "") + (st.off ? " off" : "");
    var sub = st.off ? S.ob.importOff : st.done ? S.ob.imported + (st.counts ? " · " + st.counts : "") : st.busy ? S.ob.importing + " " + Math.round((st.pct || 0) * 100) + "%" : S.ob.importTry;
    var el = h("button." + cls.split(" ").join("."), { "aria-label": S.ob.importTry + " " + s[1], onclick: function () { if (st.off || st.busy || st.done) return; imp[s[0]] = { busy: true, pct: 0 }; post("import.start", { source: s[0] }); render(); } },
      h("span.lg", { style: { background: s[2] }, text: s[1][0] }), h("div", h("b", { text: s[1] }), h("div.st", { text: sub })));
    if (st.busy) el.appendChild(h("div.bar", h("i", { style: { width: Math.round((st.pct || 0) * 100) + "%" } })));
    if (st.off) el.setAttribute("aria-disabled", "true"); return el;
  }
  function s1() {
    var m = h("div", h("h1", S.f(S.ob.s1Title).replace(S.brand.name, ""), h("em", { text: S.brand.name.toLowerCase() })), h("p.lead", { text: S.f(S.ob.s1Sub) }),
      h("div.sect", h("h2", { text: S.ob.importTitle }), h("div.sub", { text: S.ob.importSub }), h("div.grid4", SRC.map(srcCard))));
    var theme = SW.setting("theme", "system");
    var seg = h("div.seg", { role: "radiogroup", "aria-label": S.ob.themeTitle }); [["light", S.ob.themeLight], ["dark", S.ob.themeDark], ["system", S.ob.themeSystem]].forEach(function (t) { seg.appendChild(h("button", { role: "radio", "aria-checked": theme === t[0] ? "true" : "false", onclick: function () { SW.set("theme", t[0]); SW.state.theme = t[0]; SW.applyTheme(); render(); } }, t[1])); });
    m.appendChild(h("div.opts", h("div.opt", h("span.oi", icon("globe", 18)), h("div.ot", h("b", { text: S.f(S.ob.defaultTitle) }), h("div.d", { text: S.ob.defaultSub })), deflt ? h("span.chip.accent", icon("check", 14), S.ob.defaultDone) : h("button.btn.sm", { text: S.ob.defaultBtn, onclick: function () { post("defaultBrowser.set"); deflt = true; render(); } })),
      h("div.opt", h("span.oi", icon("brush", 18)), h("div.ot", h("b", { text: S.ob.themeTitle })), seg)));
    m.appendChild(h("div.trio", [["bolt", S.ob.t1, S.ob.t1d], ["spark", S.ob.t2, S.ob.t2d], ["shield", S.ob.t3, S.ob.t3d]].map(function (t) { return h("div", icon(t[0], 20), h("div", h("b", { text: S.f(t[1]) }), S.f(t[2]))); })));
    return m;
  }
  function demo() {
    var sk = h("div.sk"), pg = h("div.pg", h("div.tt", { text: "grocer" }), h("div.it", h("i", { style: { background: "#f4a261" } }), "Mineral Sunscreen  $9.99"), h("div.it", h("i", { style: { background: "#e9c46a" } }), "Pickleballs 3-pack  $9.99"), h("div.it", h("i", { style: { background: "#2a9d8f" } }), "Beach Towel  $29.99"), h("div.it#hat", h("i", { style: { background: "#264653" } }), "Bucket Hat  $19.99"));
    var rep = h("button.btn.sm.ghost", { onclick: function () { play(); } }, icon("refresh", 14), S.ob.demoReplay);
    var root = h("div.demo", h("div.bar0", h("i"), h("i"), h("i"), h("span", { text: "grocer.example.com" })), h("div.body", pg, sk), h("div.foot", icon("bolt", 14), h("span", { text: S.f(S.brand.agentFull) }), h("span", { style: { flex: "1" } }), rep));
    function add(n) { sk.appendChild(n); return n; }
    function play(instant) {
      demoTimers.forEach(clearTimeout); demoTimers = []; SW.clear(sk); var hat = pg.querySelector("#hat"); hat.classList.remove("hl");
      var seq = [
        function () { add(h("div.u", { text: S.ob.demoUser })); }, function () { add(h("div.th", icon("spark", 12), S.ob.demoThink)); },
        function () { add(h("div.st.run", h("span.spin"), S.ob.demoA1)); }, function () { var l = sk.querySelectorAll(".st"); l[l.length - 1].className = "st"; l[l.length - 1].replaceChild(icon("check", 12), l[l.length - 1].firstChild); add(h("div.st", icon("check", 12), S.ob.demoA2)); },
        function () { hat.classList.add("hl"); add(h("div.st", icon("check", 12), S.ob.demoA3)); }, function () { add(h("div.a", { text: S.ob.demoDone })); }
      ];
      if (instant || reduce) { seq.forEach(function (f) { f(); }); return; }
      seq.forEach(function (f, i) { demoTimers.push(setTimeout(f, 500 + i * 1100)); });
    }
    root._play = play; window.__demoFinish = function () { play(true); }; play(reduce || SW.query("demo") === "done"); return root;
  }
  function s2() {
    var inp = h("input.field", { type: "password", placeholder: S.ob.keyPlaceholder, "aria-label": S.ob.keyTitle, spellcheck: "false", autocomplete: "off" }), msg = h("div.msg", { role: "status" });
    var save = function () {
      var v = inp.value.trim();
      if (!/^sk-ant-[\w-]{10,}$/.test(v)) { msg.className = "msg bad"; msg.textContent = S.ob.keyBad; return; }
      post("agent.setKey", { key: v }); msg.className = "msg ok"; msg.textContent = S.ob.keySaved; keyOK = true; inp.value = "";
    };
    inp.addEventListener("keydown", function (e) { if (e.key === "Enter") save(); });
    if (keyOK) { msg.className = "msg ok"; msg.textContent = S.ob.keySaved; }
    return h("div", h("h1", S.f(S.ob.s2Title)), h("p.lead", { text: S.f(S.ob.s2Sub) }),
      h("div.two", demo(), h("div.keybox", h("h2", { text: S.f(S.ob.keyTitle) }), h("div.sub", { text: S.f(S.ob.keySub) }),
        h("div.row", inp, h("button.btn.primary", { text: S.ob.keySave, onclick: save })), msg,
        h("div.note", icon("shield", 18), h("span", { text: S.f(S.ob.keyHow) })),
        h("div", { style: { display: "flex", gap: "12px", marginTop: "14px", alignItems: "center" } }, h("a", { href: "https://console.anthropic.com/settings/keys", onclick: function (e) { e.preventDefault(); post("openExternal", { url: "https://console.anthropic.com/settings/keys" }); }, text: S.ob.keyGet }), h("span", { style: { flex: "1" } }), h("button.btn.ghost.sm", { text: S.ob.keyLater, onclick: function () { go(3); } })))));
  }
  function choice(group, key, o) {
    var on = pr[key] === o[0];
    return h("button.ch", { role: "radio", "aria-checked": on ? "true" : "false", onclick: function () { pr[key] = o[0]; SW.set(group, o[0]); render(); } }, h("span.rd"), h("div", h("b", o[1], o[3] ? h("span.rec", { text: S.ob.recommended }) : null), h("div.d", { text: S.f(o[2]) })));
  }
  function s3() {
    return h("div", h("h1", S.f(S.ob.s3Title)), h("p.lead", { text: S.f(S.ob.s3Sub) }),
      h("div.two", { style: { gridTemplateColumns: "1fr 1fr" } },
        h("div", h("h2", { text: S.ob.trackTitle }), h("div.choices", { role: "radiogroup", "aria-label": S.ob.trackTitle }, [["standard", S.ob.trackStd, S.ob.trackStdSub, 1], ["strict", S.ob.trackStrict, S.ob.trackStrictSub], ["off", S.ob.trackOff, S.ob.trackOffSub]].map(function (o) { return choice("privacy.trackers", "trackers", o); }))),
        h("div", h("h2", { text: S.f(S.ob.visTitle) }), h("div.choices", { role: "radiogroup", "aria-label": S.f(S.ob.visTitle) }, [["ask", S.ob.visAsk, S.ob.visAskSub, 1], ["allowed", S.ob.visAllowed, S.ob.visAllowedSub], ["blocked", S.ob.visBlocked, S.ob.visBlockedSub]].map(function (o) { return choice("agent.visibilityDefault", "vis", o); })))),
      h("div.note", { style: { marginTop: "22px" } }, icon("shield", 18), h("span", { text: S.f(S.ob.privNote) })));
  }
  /* Step 3: "Do you have a data plan?" Picking a size sets a sensible level; skipping changes nothing. */
  var PLAN_LVL = { "100": "strict", "500": "standard", "1024": "standard", "0": "off" };
  function pickPlan(mb) {
    plan = mb; var n = +mb;
    SW.post("data.setBudget", n > 0 ? { enabled: true, limitMB: n, period: "month", resetDay: 1 } : { enabled: false });
    SW.post("data.setLevel", { level: PLAN_LVL[mb] }); render();
  }
  function s3data() {
    var T = S.data.ob, grid = h("div.plans", { role: "radiogroup", "aria-label": T.title });
    T.opts.forEach(function (o) {
      var on = plan === o[0];
      grid.appendChild(h("button.plan", { role: "radio", "aria-checked": on ? "true" : "false", onclick: function () { pickPlan(o[0]); } },
        h("b", { text: o[1] }), h("span.per", { text: o[2] }), h("span.pl", icon("drop", 14), o[3]), h("span.pd", { text: o[4] })));
    });
    var line = plan != null ? S.f(T.picked, { lvl: S.data.lvl[PLAN_LVL[plan]] }) : "";
    return h("div", h("h1", { text: T.title }), h("p.lead", { text: S.f(T.sub) }), grid,
      h("div.plannote", { role: "status" }, line ? icon("check", 16) : null, h("span", { text: line || (plan === null && skippedPlan ? T.skipped : "") })),
      h("div", { style: { marginTop: "6px" } }, h("button.btn.ghost.sm", { text: T.notSure, onclick: function () { plan = null; skippedPlan = true; SW.post("data.setBudget", { enabled: false }); SW.post("data.setLevel", { level: "auto" }); render(); } })));
  }
  var skippedPlan = false;
  function render() { dots(); var m = $("step"); SW.clear(m); m.appendChild(step === 1 ? s1() : step === 2 ? s2() : step === 3 ? s3data() : s3()); foot(); }
  SW.on("import.progress", function (p) { imp[p.source] = { busy: true, pct: p.pct || 0 }; if (step === 1) render(); });
  SW.on("import.done", function (p) { imp[p.source] = { done: true, counts: p.summary || (p.bookmarks != null ? p.bookmarks + " bookmarks" : "") }; if (step === 1) render(); });
  SW.on("import.unavailable", function (p) { imp[p.source] = { off: true }; if (step === 1) render(); });
  SW.on("defaultBrowser.result", function (p) { deflt = !!(p && p.ok); if (step === 1) render(); });
  SW.on("agent.keyResult", function (p) { keyOK = !!(p && p.ok); if (step === 2) render(); });
  SW.on("onboarding.step", function (p) { go(SW.clamp(+p.n || 1, 1, TOTAL)); });
  SW.on("render", function () { var s = SW.state.settings || {}; if (s["privacy.trackers"]) pr.trackers = s["privacy.trackers"]; if (s["agent.visibilityDefault"]) pr.vis = s["agent.visibilityDefault"]; });
  render(); SW.ready("onboarding");
})();
