/* sweef://newtab */
(function () {
  var SW = window.SW, S = window.S, h = SW.h, icon = SW.icon, post = SW.post, $ = function (id) { return document.getElementById(id); };
  var q = $("q"), pill = $("pill"), forceInc = SW.query("incognito") === "1", built = "";
  $("pi").appendChild(icon("spark", 22)); $("mic").appendChild(icon("mic", 18)); $("mic").setAttribute("title", S.agent.voice); $("mic").setAttribute("aria-label", S.agent.voice);
  $("go").appendChild(icon("send", 20)); $("go").addEventListener("click", function () { om.commit(om.rows[Math.max(0, om.sel)] || null); });
  q.setAttribute("placeholder", S.nt.placeholder);
  function ask(text, mode) {
    post("agent.send", { text: text, mode: mode || "ask", source: "newtab" }); SW.post("settings.set", { key: "sidebar.open", value: true }); q.value = ""; om.typed = ""; pill.classList.remove("has");
  }
  var om = new SW.Omni({
    input: q, list: $("od"), ghost: $("ghost"), scope: "newtab", footer: false,
    getTab: function () { return ""; },
    defaultAsk: function (t) { return /\?\s*$/.test(t) || t.trim().split(/\s+/).length >= 3; },
    onAsk: function (t) { ask(t, "ask"); },
    onToggle: function (open) { $("rows").classList.toggle("hidden", open); $("sites").classList.toggle("hidden", open); $("stats").classList.toggle("hidden2", open); }
  });
  q.addEventListener("focus", function () { pill.classList.add("focus"); });
  q.addEventListener("blur", function () { pill.classList.remove("focus"); });
  q.addEventListener("input", function () { pill.classList.toggle("has", !!q.value.trim()); });
  document.addEventListener("keydown", function (e) { if (e.key === "/" && document.activeElement !== q) { e.preventDefault(); q.focus(); } });
  try { q.focus(); } catch (e) {}

  function pick() {  // four rows, always including one Agent mode row; rotates per visit
    var seed = window.__sweefSeed != null ? window.__sweefSeed : Math.floor(Date.now() / 36e5), pool = [1, 2, 3, 4, 5, 6, 7, 8].map(function (i) { return { i: i, v: S.nt["r" + i], agent: i === 3 || i === 7 }; });
    var ag = pool.filter(function (x) { return x.agent; }), ot = pool.filter(function (x) { return !x.agent; }), out = [ag[seed % ag.length]];
    for (var k = 0; k < 3; k++) out.push(ot[(seed + k * 2) % ot.length]); 
    return [out[1], out[2], out[0], out[3]];
  }
  function rows() {
    var box = $("rows"); SW.clear(box);
    pick().forEach(function (x) {
      var ag = x.agent;
      box.appendChild(h("button.row" + (ag ? ".agent" : ""), { role: "listitem", onclick: function () { ask(x.v[1], ag ? "agent" : "ask"); } }, h("span.ri", icon(ag ? "bolt" : x.i === 1 ? "list" : x.i === 2 ? "search" : "spark", 14)), h("span.rt", h("b", { text: x.v[0] }), "— " + x.v[1])));
    });
  }
  function sites() {
    var box = $("sites"), list = (SW.state.topSites || []).slice(0, 8), k = JSON.stringify(list); if (box._k === k) return; box._k = k; SW.clear(box);
    list.forEach(function (s) { box.appendChild(h("button.site", { role: "listitem", title: s.url, oncontextmenu: function (e) { tileMenu(e, s); }, onclick: function () { post("nav.go", { tab: "", input: s.url }); } }, h("span.tile", SW.favicon(s.url, s.favicon, s.title)), h("span.nm", { text: s.title || SW.host(s.url) }))); });
  }
  /* context menus: a tile (open, edit, remove) and the page itself (customize, reload) */
  function tileMenu(e, s) {
    SW.ctxMenu(e, [{ label: S.ctx.open, on: function () { post("nav.go", { tab: "", input: s.url }); } }, { label: S.ctx.openNew, on: function () { post("tab.new", { url: s.url }); } }, { sep: true },
      { label: S.ctx.editShortcut, on: function () { editTile(s); } }, { label: S.ctx.remove, danger: true, on: function () { post("newtab.site", { key: s.key || s.url, hide: true }); } }]);
  }
  function editTile(s) {
    var name = h("input.field", { value: s.title || "", "aria-label": S.ctx.name }), url = h("input.field", { value: s.url || "", "aria-label": S.ctx.url }), m;
    var done = function () { if (m.parentNode) m.parentNode.removeChild(m); };
    var save = function () { post("newtab.site", { key: s.key || s.url, hide: false, title: name.value, url: url.value }); done(); };
    m = h("div.ntmodal", { role: "dialog", "aria-label": S.ctx.editShortcut, onkeydown: function (e) { if (e.key === "Escape") done(); else if (e.key === "Enter") save(); } },
      h("div.card", h("h3", { text: S.ctx.editShortcut }), h("label", { text: S.ctx.name }), name, h("label", { text: S.ctx.url }), url,
        h("div.btns", h("button.btn.sm", { text: S.ctx.cancel, onclick: done }), h("button.btn.sm.primary", { text: S.ctx.save, onclick: save }))));
    document.body.appendChild(m); name.focus();
  }
  document.addEventListener("contextmenu", function (e) {
    var t = e.target; if (e.defaultPrevented || (t.closest && t.closest(".ntmodal")) || (t.localName === "input" || t.localName === "textarea")) return;
    SW.ctxMenu(e, [{ label: S.ctx.customize, on: function () { post("tab.new", { url: "sweef://settings#appearance" }); } }, { label: S.ctx.reload, kbd: SW.mod + "+R", on: function () { post("nav.reload"); } }]);
  });
  function stats() {
    var st = SW.state.stats, el = $("stats"); if (!st || st.memMB == null || st.tabs == null || SW.state.incognito || forceInc) { el.classList.add("hide"); return; }
    el.classList.remove("hide"); SW.clear(el); el.appendChild(icon("bolt", 14));
    var m = S.nt.statsLine.split(/(\{mem\}|\{tabs\}|\{brand\})/);
    m.forEach(function (p) { if (p === "{mem}") el.appendChild(h("b", { text: st.memMB >= 1024 ? (st.memMB / 1024).toFixed(1) + " GB" : Math.round(st.memMB) + " MB" })); else if (p === "{tabs}") el.appendChild(h("b", { text: String(st.tabs) })); else el.appendChild(document.createTextNode(S.f(p))); });
  }
  /* "61 MB left this month": one calm line under the pill, only when a budget is set */
  function dataline() {
    var d = SW.state.data || {}, b = d.budget || {}, el = $("dataline"), T = S.data;
    if (!b.enabled || d.available === false) { el.classList.add("hide"); el._k = ""; return; }
    var left = Math.max(0, (b.limitMB || 0) * 1048576 - (b.usedBytes != null ? b.usedBytes : d.used || 0)), per = T.period[b.period] || T.period.month, n = b.resetsInDays || 1;
    var lim = b.limitMB >= 1024 && b.limitMB % 1024 === 0 ? (b.limitMB / 1024) + " GB" : b.limitMB + " MB";
    var text = b.exhausted ? S.f(T.nt.used, { limit: lim, period: per, when: n <= 1 ? T.nt.tomorrow : S.f(T.nt.inDays, { n: n }) }) : S.f(T.nt.left, { n: SW.fmtBytes(left), period: per });
    var tone = SW.dataTone(b), k = text + tone; if (el._k === k) return; el._k = k;
    el.classList.remove("hide"); el.className = "dataline" + (tone ? " " + tone : ""); el.id = "dataline"; SW.clear(el);
    el.appendChild(icon("drop", 15)); el.appendChild(h("span", { text: text }));
  }
  $("dataline").addEventListener("click", function () { post("tab.new", { url: "sweef://settings#data" }); });
  function brand(inc) {
    var key = inc ? "i" : "n"; if (built === key) return; built = key; var b = $("brand"); SW.clear(b);
    if (inc) { b.appendChild(h("div.hat", icon("hat", 40))); } else b.appendChild(SW.wordmark(44, "ntw"));
    var box = $("inc"); SW.clear(box); box.classList.toggle("hide", !inc); $("rows").classList.toggle("hide", inc); $("sites").classList.toggle("hide", inc);
    if (inc) {
      box.appendChild(h("p.intro", S.f(S.nt.incIntro)));
      var col = function (a) { var c = h("div.col"); a.forEach(function (x) { c.appendChild(h("p", h("b", { text: S.f(x[0]) }), S.f(x[1]))); }); return c; };
      box.appendChild(h("div.cols", col([[S.nt.incNotSaved, S.nt.incNotSavedBody], [S.nt.incAgent, S.nt.incAgentBody]]), col([[S.nt.incNotPrivate, S.nt.incNotPrivateBody], [S.nt.incDownloads, S.nt.incDownloadsBody]])));
    }
  }
  SW.on("render", function () { var inc = !!SW.state.incognito || forceInc; if (forceInc) document.documentElement.setAttribute("data-theme", "incognito"); brand(inc); sites(); stats(); dataline(); });
  rows(); brand(forceInc); SW.ready("newtab");
})();
