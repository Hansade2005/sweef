/* Developer Tools: the panel docked under the page (Elements, Console, Sources, Network, Application).
   Parsed the first time it is opened (chrome.js). The Go side is internal/app/devtools.go: every call goes out as
   devtools.req {id, m, p} and comes back as devtools.res {id, result | error}. The panel is chrome UI; nothing in it
   is reachable from a web page. Scrolling is JS-driven (shared/scroll.js, vlist.js): the engine has no native
   element scrolling. */
(function () {
  "use strict";
  var SW = window.SW, h = SW.h, icon = SW.icon, post = SW.post, clamp = SW.clamp;
  var root = document.getElementById("devtools");
  if (!root) return;
  var D = SW.devtools = { drag: false };
  var PANELS = [["elements", "Elements"], ["console", "Console"], ["sources", "Sources"], ["network", "Network"], ["application", "Application"]];
  var P = {}, cur = "", tab = null, epoch = 0, ro = false, built = false, body, tabEls = {}, roBanner;

  /* ---- helpers ---------------------------------------------------------------------------------------------- */
  function fmtSize(n) { if (n == null || n < 0) return ""; if (n < 1024) return n + " B"; if (n < 1048576) return (n / 1024).toFixed(n < 10240 ? 1 : 0) + " kB"; return (n / 1048576).toFixed(1) + " MB"; }
  function fmtTime(ms) { return ms == null || ms < 0 ? "" : ms < 1000 ? Math.round(ms) + " ms" : (ms / 1000).toFixed(2) + " s"; }
  function ibtn(ic, label, fn, cls) { return h("button.ibtn.sm" + (cls ? "." + cls : ""), { type: "button", title: label, "aria-label": label, onclick: fn }, icon(ic, 16)); }
  function copy(text) { post("clipboard.write", { text: String(text == null ? "" : text) }); }
  function splitLoc(loc) { var m = /^(.*):(\d+):(\d+)$/.exec(loc || ""); return m ? { url: m[1], line: +m[2], col: +m[3] } : { url: loc || "", line: 0, col: 0 }; }
  function baseName(u) {
    if (!u) return ""; if (u.charAt(0) === "<" || u.charAt(0) === "(") return u;
    var p = u.replace(/[?#].*$/, "").replace(/\/+$/, ""), i = p.lastIndexOf("/"), n = i >= 0 ? p.slice(i + 1) : p;
    return n || SW.host(u) || u;
  }
  function hostOf(u) { return /^[a-z][\w+.-]*:\/\//i.test(u || "") ? SW.host(u) : ""; }
  function scrollBox(cls) { var box = h("div.dt-sb" + (cls ? "." + cls : "")), inner = h("div.dt-in"); box.appendChild(inner); return { box: box, inner: inner, sc: SW.scroller(box, inner) }; }
  function emptyMsg(text) { return h("div.dt-empty", { text: text }); }
  function search(ph, fn) {
    var inp = h("input.dt-filter", { type: "search", placeholder: ph, "aria-label": ph, spellcheck: "false", autocomplete: "off" });
    inp.addEventListener("input", function () { fn(inp.value.trim().toLowerCase()); });
    return inp;
  }

  /* ---- calls to Go -------------------------------------------------------------------------------------------- */
  var seq = 0, waits = {};
  function call(m, p) {
    var ep = epoch;
    return new Promise(function (ok, bad) { var id = ++seq; waits[id] = { ok: ok, bad: bad, ep: ep }; post("devtools.req", { id: id, m: m, p: p || {} }); });
  }
  SW.on("devtools.res", function (r) {
    var w = r && waits[r.id]; if (!w) return; delete waits[r.id];
    if (w.ep !== epoch) return; /* the answer belongs to a tab we have left */
    if (r.error) w.bad(new Error(r.error)); else w.ok(r.result);
  });
  SW.on("devtools.focus", function (p) { if (p && p.panel && tab) show(p.panel); });
  SW.on("devtools.select", function (p) { if (p && p.id && tab) { show("elements"); P.elements.reveal(p.id, p.path || []); } });

  /* ---- shell: tabs, resizer, close -------------------------------------------------------------------------- */
  function build() {
    if (built) return; built = true;
    var rz = h("div.dt-rz", { role: "separator", "aria-orientation": "horizontal", "aria-label": "Resize Developer Tools", tabindex: "0" });
    var start = null;
    rz.addEventListener("mousedown", function (e) {
      e.preventDefault(); start = { y: e.clientY, h: root.offsetHeight }; D.drag = true; rz.classList.add("drag");
      window.addEventListener("mousemove", move); window.addEventListener("mouseup", up);
    });
    function maxH() { return Math.max(160, (window.innerHeight || 700) - 220); }
    function move(e) { if (!start) return; start.cur = clamp(start.h + (start.y - e.clientY), 160, maxH()); root.style.height = start.cur + "px"; }
    function up() {
      window.removeEventListener("mousemove", move); window.removeEventListener("mouseup", up); rz.classList.remove("drag"); D.drag = false;
      if (start && start.cur) SW.set("devtools.height", start.cur);
      start = null;
    }
    rz.addEventListener("keydown", function (e) {
      var d = e.key === "ArrowUp" ? 24 : e.key === "ArrowDown" ? -24 : 0; if (!d) return; e.preventDefault();
      SW.set("devtools.height", clamp(root.offsetHeight + d, 160, maxH()));
    });
    rz.addEventListener("dblclick", function () { SW.set("devtools.height", 320); });
    var bar = h("div.dt-bar", { role: "tablist", "aria-label": "Developer Tools panels" });
    PANELS.forEach(function (p) {
      var b = h("button.dt-tab", { type: "button", role: "tab", "aria-selected": "false", onclick: function () { show(p[0]); } }, h("span", { text: p[1] }));
      tabEls[p[0]] = b; bar.appendChild(b);
    });
    var title = h("span.dt-title.grow");
    roBanner = h("span.dt-ro.hide", { text: "Sweef page: read only" });
    bar.appendChild(title); bar.appendChild(roBanner);
    bar.appendChild(ibtn("close", "Close Developer Tools", function () { post("devtools.close", {}); }));
    D.title = title;
    body = h("div.dt-body");
    SW.clear(root); root.appendChild(rz); root.appendChild(bar); root.appendChild(body);
    P.elements = elementsPanel(); P.console = consolePanel(); P.sources = sourcesPanel(); P.network = networkPanel(); P.application = applicationPanel();
    PANELS.forEach(function (p) { P[p[0]].el.classList.add("dt-panel", "hide"); body.appendChild(P[p[0]].el); });
  }

  function show(name) {
    if (!P[name]) name = "console";
    if (cur === name) { if (P[name].show) P[name].show(); return; }
    if (cur && P[cur] && P[cur].hide) P[cur].hide();
    cur = name;
    PANELS.forEach(function (p) {
      var on = p[0] === name; tabEls[p[0]].setAttribute("aria-selected", on ? "true" : "false"); P[p[0]].el.classList.toggle("hide", !on);
    });
    if (SW.setting("devtools.panel", "console") !== name) SW.set("devtools.panel", name);
    if (P[name].show) P[name].show();
  }

  function attach(t) {
    epoch++; tab = t;
    PANELS.forEach(function (p) { P[p[0]].reset(); });
    var want = SW.setting("devtools.panel", "console"); cur = ""; show(want);
    P.console.poll(); /* the console is always read: its counts are on its tab */
  }
  function detach() {
    epoch++; tab = null;
    if (cur && P[cur] && P[cur].hide) P[cur].hide();
    cur = "";
  }
  /* the app looks at the tab's counters a few times a second and says what changed; this page runs no timers of its own */
  SW.on("devtools.dirty", function (p) {
    if (!tab || !p) return;
    if (p.con) P.console.poll();
    if (cur === "network" && p.net) P.network.poll();
    if (cur === "elements" && (p.dom || p.nav)) P.elements.poll();
    if (cur === "sources" && p.nav) P.sources.poll();
  });
  D.sync = function () {
    var d = SW.state.devtools;
    if (d && d.open) {
      build(); ro = !!d.readOnly; roBanner.classList.toggle("hide", !ro);
      var tl = (SW.state.tabs || []).filter(function (t) { return t.id === d.tab; })[0]; D.title.textContent = tl ? (tl.title || SW.host(tl.url) || "") : "";
      if (d.tab !== tab) attach(d.tab);
    } else if (tab) detach();
  };

  /* ---- Console --------------------------------------------------------------------------------------------- */
  function consolePanel() {
    var ents = [], last = 0, level = "all", text = "", hist = [], hi = 0, dirty = false;
    var el = h("div.dt-col"), sb = scrollBox("dt-con"), list = sb.inner, sc = sb.sc;
    var levels = [["all", "All"], ["error", "Errors"], ["warn", "Warnings"], ["log", "Logs"]], chips = {};
    var badge = h("i.dt-badge.hide"), cnt = h("span.dt-cnt");
    var tools = h("div.dt-tools", ibtn("trash", "Clear console", function () { ents = []; render(); }));
    levels.forEach(function (l) { chips[l[0]] = h("button.dt-chip", { type: "button", "aria-pressed": l[0] === "all" ? "true" : "false", onclick: function () { level = l[0]; levels.forEach(function (x) { chips[x[0]].setAttribute("aria-pressed", x[0] === level ? "true" : "false"); }); render(); } }, l[1]); tools.appendChild(chips[l[0]]); });
    tools.appendChild(search("Filter", function (v) { text = v; render(); })); tools.appendChild(cnt);
    var prompt = h("input.dt-prompt", { type: "text", placeholder: "Run JavaScript in the page", "aria-label": "Console prompt", spellcheck: "false", autocomplete: "off" });
    var pbar = h("div.dt-pbar", h("span.dt-pi", { text: ">" }), prompt);
    prompt.addEventListener("keydown", function (e) {
      if (e.key === "Enter") { e.preventDefault(); run(prompt.value); }
      else if (e.key === "ArrowUp") { e.preventDefault(); if (hist.length) { hi = Math.max(0, hi - 1); prompt.value = hist[hi] || ""; } }
      else if (e.key === "ArrowDown") { e.preventDefault(); if (hi < hist.length) { hi++; prompt.value = hist[hi] || ""; } }
    });
    el.appendChild(tools); el.appendChild(sb.box); el.appendChild(pbar);
    function lv(l) { return l === "error" ? "error" : l === "warning" || l === "warn" ? "warn" : l === "info" ? "info" : l === "debug" ? "debug" : "log"; }
    function okLevel(en) {
      if (en.kind) return true;
      var l = lv(en.level); return level === "all" || (level === "error" && l === "error") || (level === "warn" && l === "warn") || (level === "log" && l !== "error" && l !== "warn");
    }
    function run(src) {
      src = src.trim(); if (!src) return;
      hist.push(src); hi = hist.length; prompt.value = "";
      ents.push({ kind: "in", text: src }); render();
      call("console.eval", { expr: src }).then(function (r) { ents.push({ kind: r.ok ? "out" : "err", text: r.text, type: r.type }); render(); },
        function (e) { ents.push({ kind: "err", text: e.message }); render(); });
    }
    function locEl(en) {
      if (!en.loc) return null;
      var l = splitLoc(en.loc), name = baseName(l.url), inline = l.url.charAt(0) === "<";
      return h("a.dt-loc", { title: en.loc, href: "#", onclick: function (e) { e.preventDefault(); show("sources"); P.sources.open(l.url, l.line); } }, (inline ? "(inline)" : name) + ":" + l.line);
    }
    function row(en, n) {
      if (en.kind === "in") return h("div.dt-cr.in", h("span.dt-g", { text: ">" }), h("div.dt-ct.mono", { text: en.text }));
      if (en.kind === "out" || en.kind === "err") return h("div.dt-cr." + (en.kind === "err" ? "error" : "out"), h("span.dt-g", { text: en.kind === "err" ? "✖" : "←" }), h("div.dt-ct.mono", { text: en.text }));
      var l = lv(en.level), lines = String(en.text).split("\n"), first = lines.shift();
      var msg = h("div.dt-ct.mono", h("span", { text: first }));
      if (lines.length) { msg.appendChild(h("div.dt-stack", { text: lines.join("\n") })); }
      return h("div.dt-cr." + l, h("span.dt-g", { text: l === "error" ? "✖" : l === "warn" ? "⚠" : "" }), n > 1 ? h("span.dt-rep", { text: String(n) }) : null, msg, locEl(en));
    }
    function counts() {
      var e = 0, w = 0; ents.forEach(function (x) { if (x.kind) return; var l = lv(x.level); if (l === "error") e++; else if (l === "warn") w++; });
      return { e: e, w: w };
    }
    function render() {
      dirty = false;
      var stick = sc.atBottom(), rows = [], prev = null, shown = 0;
      var vis = ents.filter(function (en) { return okLevel(en) && (!text || String(en.text).toLowerCase().indexOf(text) >= 0 || String(en.loc || "").toLowerCase().indexOf(text) >= 0); });
      vis = vis.slice(-500);
      vis.forEach(function (en) {
        if (prev && !en.kind && !prev.en.kind && prev.en.level === en.level && prev.en.text === en.text && prev.en.loc === en.loc) { prev.n++; return; }
        prev = { en: en, n: 1 }; rows.push(prev);
      });
      SW.clear(list);
      rows.forEach(function (r) { list.appendChild(row(r.en, r.n)); shown++; });
      if (!rows.length) list.appendChild(emptyMsg(ents.length ? "No messages match the filter." : "No console messages yet."));
      var c = counts(); cnt.textContent = c.e || c.w ? (c.e ? c.e + (c.e === 1 ? " error" : " errors") : "") + (c.e && c.w ? ", " : "") + (c.w ? c.w + (c.w === 1 ? " warning" : " warnings") : "") : "";
      var tb = tabEls.console; if (tb) { badge.textContent = c.e ? String(c.e) : c.w ? String(c.w) : ""; badge.className = "dt-badge" + (c.e ? " err" : c.w ? " warn" : " hide"); if (badge.parentNode !== tb) tb.appendChild(badge); }
      if (stick) sc.toBottom(); else sc.refresh();
    }
    return {
      el: el,
      reset: function () { ents = []; last = 0; hist = hist.slice(-50); hi = hist.length; render(); },
      show: function () { render(); setTimeout(function () { prompt.focus(); }, 0); },
      poll: function () {
        return call("console.get", { since: last }).then(function (l) {
          if (!l || !l.length) return;
          l.forEach(function (x) { if (x.sid > last) last = x.sid; ents.push(x); });
          if (ents.length > 1500) ents = ents.slice(-1000);
          if (cur === "console") render(); else dirty = true;
          if (cur !== "console") { var c = counts(); badge.textContent = c.e ? String(c.e) : c.w ? String(c.w) : ""; badge.className = "dt-badge" + (c.e ? " err" : c.w ? " warn" : " hide"); if (tabEls.console && badge.parentNode !== tabEls.console) tabEls.console.appendChild(badge); }
        }, function () {});
      },
      hide: function () {}
    };
  }

  /* ---- Network --------------------------------------------------------------------------------------------- */
  function networkPanel() {
    var byId = {}, order = [], last = 0, pend = [], sel = 0, type = "all", text = "", preserve = false, now = 0, dtab = "headers";
    var bodies = {};
    var el = h("div.dt-col"), tools = h("div.dt-tools"), main = h("div.dt-row2"), listWrap = h("div.dt-nlist"), head = h("div.dt-nh"), lbox = h("div.dt-sb.dt-nbox"), foot = h("div.dt-nfoot");
    var det = h("div.dt-ndet.hide"), dtabs = h("div.dt-dtabs"), dbox = scrollBox("dt-dbody");
    var V = SW.vlist(lbox, renderRow);
    var types = [["all", "All"], ["document", "Doc"], ["script", "JS"], ["style", "CSS"], ["image", "Img"], ["font", "Font"], ["xhr", "Fetch/XHR"], ["media", "Media"], ["other", "Other"]], chips = {};
    tools.appendChild(ibtn("trash", "Clear network log", function () { call("net.clear").then(function () {}, function () {}); byId = {}; order = []; sel = 0; pend = []; bodies = {}; render(); }));
    var pl = h("label.dt-check", h("input", { type: "checkbox", onchange: function (e) { preserve = !!e.target.checked; render(); } }), h("span", { text: "Preserve log" }));
    tools.appendChild(pl); tools.appendChild(search("Filter", function (v) { text = v; render(); }));
    types.forEach(function (t) { chips[t[0]] = h("button.dt-chip", { type: "button", "aria-pressed": t[0] === "all" ? "true" : "false", onclick: function () { type = t[0]; types.forEach(function (x) { chips[x[0]].setAttribute("aria-pressed", x[0] === type ? "true" : "false"); }); render(); } }, t[1]); tools.appendChild(chips[t[0]]); });
    [["Name", "name"], ["Status", "status"], ["Type", "type"], ["Size", "size"], ["Time", "time"], ["Cache", "cache"], ["Waterfall", "wf"]].forEach(function (c) { head.appendChild(h("span.dt-c.c-" + c[1], { text: c[0] })); });
    listWrap.appendChild(head); listWrap.appendChild(lbox); listWrap.appendChild(foot);
    [["headers", "Headers"], ["preview", "Preview"], ["response", "Response"]].forEach(function (t) {
      dtabs.appendChild(h("button.dt-dtab", { type: "button", "data-t": t[0], "aria-selected": t[0] === dtab ? "true" : "false", onclick: function () { dtab = t[0]; markTabs(); renderDetail(); } }, t[1]));
    });
    dtabs.appendChild(h("span.grow")); dtabs.appendChild(ibtn("close", "Close details", function () { sel = 0; render(); }));
    det.appendChild(dtabs); det.appendChild(dbox.box);
    main.appendChild(listWrap); main.appendChild(det); el.appendChild(tools); el.appendChild(main);
    function markTabs() { SW.$$(".dt-dtab", dtabs).forEach(function (b) { b.setAttribute("aria-selected", b.getAttribute("data-t") === dtab ? "true" : "false"); }); }

    function curNav() { var n = 0; order.forEach(function (id) { if (byId[id].nav > n) n = byId[id].nav; }); return n; }
    function visible() {
      var nv = curNav();
      return order.map(function (id) { return byId[id]; }).filter(function (e) {
        if (!preserve && e.nav < nv) return false;
        if (type !== "all" && e.type !== type && !(type === "document" && e.type === "frame")) return false;
        return !text || e.url.toLowerCase().indexOf(text) >= 0 || String(e.status).indexOf(text) >= 0 || (e.mime || "").toLowerCase().indexOf(text) >= 0;
      });
    }
    function statusText(e) { return e.blocked ? "(blocked)" : e.error ? "(failed)" : e.pending ? "(pending)" : String(e.status); }
    function statusCls(e) { return e.blocked || e.error || e.status >= 400 ? "bad" : e.status >= 300 ? "redir" : e.pending ? "pend" : "ok"; }
    function sizeText(e) { if (e.pending || e.blocked || e.error) return ""; if (e.cache === "hit" || e.cache === "memory") return "(cache)"; return fmtSize(e.transfer > 0 ? e.transfer : e.size); }
    var span = { t0: 0, w: 1 };
    function renderRow(r) {
      var e = r.e, nm = baseName(e.finalUrl || e.url) || e.url, q = (e.url.split("?")[1] || "");
      var left = (e.start - span.t0) / span.w * 100, dur = e.dur >= 0 ? e.dur : Math.max(1, now - e.start), w = Math.max(0.7, dur / span.w * 100);
      var row = h("div.dt-nr" + (e.id === sel ? ".sel" : "") + (e.blocked || e.error || e.status >= 400 ? ".bad" : ""), { onclick: function () { sel = e.id === sel ? 0 : e.id; render(); } },
        h("span.dt-c.c-name", { title: e.url }, h("i.dt-ty.t-" + e.type), h("b", { text: nm }), h("span.dim", { text: (hostOf(e.url) === nm ? "" : hostOf(e.url)) + (q ? " ?" + q.slice(0, 40) : "") })),
        h("span.dt-c.c-status." + statusCls(e), { title: e.blocked ? e.reason : e.error || "", text: statusText(e) }),
        h("span.dt-c.c-type", { text: e.type === "xhr" ? "fetch" : e.type }),
        h("span.dt-c.c-size", { title: "Transferred " + fmtSize(e.transfer) + ", content " + fmtSize(e.size), text: sizeText(e) }),
        h("span.dt-c.c-time", { text: e.pending ? "" : fmtTime(e.dur) }),
        h("span.dt-c.c-cache", { title: e.cache || "", text: e.cache || "" }),
        h("span.dt-c.c-wf", h("i.dt-wb.t-" + e.type + (e.pending ? ".run" : ""), { style: { left: clamp(left, 0, 99.3) + "%", width: Math.min(w, 100 - clamp(left, 0, 99.3)) + "%" } })));
      return row;
    }
    function render() {
      var vis = visible(), t0 = Infinity, t1 = 0, bytes = 0, res = 0;
      vis.forEach(function (e) { t0 = Math.min(t0, e.start); t1 = Math.max(t1, e.start + (e.dur >= 0 ? e.dur : Math.max(1, now - e.start))); bytes += e.transfer > 0 ? e.transfer : 0; res += e.size > 0 ? e.size : 0; });
      span = { t0: t0 === Infinity ? 0 : t0, w: Math.max(1, t1 - (t0 === Infinity ? 0 : t0)) };
      V.set(vis.map(function (e) { return { h: 24, e: e }; }));
      det.classList.toggle("hide", !sel || !byId[sel]); el.classList.toggle("has-det", !!(sel && byId[sel]));
      var blocked = vis.filter(function (e) { return e.blocked; }).length, failed = vis.filter(function (e) { return e.error && !e.blocked; }).length;
      foot.textContent = vis.length ? vis.length + " requests  ·  " + fmtSize(bytes) + " transferred  ·  " + fmtSize(res) + " resources  ·  Finish " + fmtTime(t1 - span.t0) + (blocked ? "  ·  " + blocked + " blocked" : "") + (failed ? "  ·  " + failed + " failed" : "") : "No requests recorded. Reload the page with Developer Tools open to see its network activity.";
      if (sel && byId[sel]) renderDetail();
    }
    function kv(k, v, cls) { return h("div.dt-kv", h("span.k", { text: k }), h("span.v" + (cls ? "." + cls : ""), { text: v })); }
    function section(title, rows) { return h("div.dt-sec", h("div.dt-sech", { text: title }), rows); }
    function headerRows(m) { return Object.keys(m || {}).sort(function (a, b) { return a.toLowerCase() < b.toLowerCase() ? -1 : 1; }).map(function (k) { return kv(k, m[k]); }); }
    function renderDetail() {
      var e = byId[sel]; if (!e) return; SW.clear(dbox.inner);
      if (dtab === "headers") {
        var g = [kv("Request URL", e.url), kv("Request Method", e.method)];
        if (e.finalUrl && e.finalUrl !== e.url) g.push(kv("Final URL", e.finalUrl));
        g.push(kv("Status", e.blocked ? "Blocked" : e.error ? "Failed" : e.pending ? "Pending" : String(e.status), e.blocked || e.error ? "bad" : ""));
        if (e.blocked) g.push(kv("Blocked by", e.reason || "a request route", "bad"));
        if (e.error) g.push(kv("Error", e.error, "bad"));
        if (e.cache) g.push(kv("Cache", e.cache));
        g.push(kv("Type", e.type + (e.mime ? " (" + e.mime + ")" : ""))); g.push(kv("Size", fmtSize(e.transfer) + " transferred, " + fmtSize(e.size) + " content"));
        if (e.dur >= 0) g.push(kv("Time", fmtTime(e.dur)));
        dbox.inner.appendChild(section("General", g));
        if (e.postData) dbox.inner.appendChild(section("Request payload", h("pre.dt-pre", { text: e.postData })));
        dbox.inner.appendChild(section("Response headers (" + Object.keys(e.respHeaders || {}).length + ")", headerRows(e.respHeaders)));
        dbox.inner.appendChild(section("Request headers (" + Object.keys(e.reqHeaders || {}).length + ")", headerRows(e.reqHeaders)));
        dbox.sc.set(0); return;
      }
      var b = bodies[e.id];
      if (!b) {
        dbox.inner.appendChild(emptyMsg(e.pending ? "The response has not arrived yet." : e.blocked || e.error ? "There is no response." : "Loading..."));
        if (!e.pending && !e.blocked && !e.error && b !== null) { bodies[e.id] = null; var id = e.id; call("net.body", { id: id }).then(function (r) { bodies[id] = r; if (sel === id) renderDetail(); }, function (er) { bodies[id] = { kind: "none", err: er.message }; if (sel === id) renderDetail(); }); }
        return;
      }
      if (b.kind === "none") dbox.inner.appendChild(emptyMsg(b.err || (e.size ? "The content of this response was not kept." : "This response has no content.")));
      else if (b.kind === "binary") dbox.inner.appendChild(emptyMsg("Binary content (" + fmtSize(b.size) + ", " + (b.mime || "unknown type") + ")."));
      else if (b.kind === "image" && dtab === "preview") {
        var img = h("img.dt-img", { src: b.data, alt: e.url });
        dbox.inner.appendChild(h("div.dt-imgwrap", img, h("div.dt-dim", { text: (b.mime || "") + ", " + fmtSize(b.size) })));
      } else {
        var txt = b.text || "";
        if (b.kind === "image") txt = "(image data, " + fmtSize(b.size) + ")";
        else if (dtab === "preview" && b.kind === "json") { try { txt = JSON.stringify(JSON.parse(txt), null, 2); } catch (x) { /* shown as it is */ } }
        var shown = txt.length > 60000 ? txt.slice(0, 60000) : txt;
        dbox.inner.appendChild(h("div.dt-bodyhead", h("span.dim", { text: fmtSize(b.size) + (b.truncated || shown.length < txt.length ? ", the first " + fmtSize(shown.length) + " shown" : "") }), h("span.grow"), h("button.dt-chip", { type: "button", onclick: function () { copy(txt); } }, "Copy")));
        dbox.inner.appendChild(h("pre.dt-pre", { text: shown }));
      }
      dbox.sc.set(0);
    }
    return {
      el: el,
      reset: function () { byId = {}; order = []; last = 0; pend = []; sel = 0; bodies = {}; render(); },
      show: function () { render(); this.poll(); },
      hide: function () {},
      poll: function () {
        return call("net.get", { since: last, pending: pend }).then(function (r) {
          if (!r) return; now = r.now || now;
          (r.entries || []).forEach(function (e) { if (!byId[e.id]) order.push(e.id); byId[e.id] = e; if (e.id > last) last = e.id; });
          pend = order.filter(function (id) { return byId[id].pending; });
          if ((r.entries && r.entries.length) || pend.length) render();
        }, function () {});
      }
    };
  }

  /* ---- Elements --------------------------------------------------------------------------------------------- */
  var VOID = { area: 1, base: 1, br: 1, col: 1, embed: 1, hr: 1, img: 1, input: 1, link: 1, meta: 1, source: 1, track: 1, wbr: 1 };
  function elementsPanel() {
    var nodes = {}, rootId = 0, sel = 0, gen = -1, nav = -1, busy = false, editing = false, hl = 0, hlT = 0, styleData = null, stab = "styles", disabled = {}, filter = "";
    var url = "", el = h("div.dt-row2"), left = h("div.dt-etree"), right = h("div.dt-eside"), tbox = h("div.dt-sb.dt-ebox", { tabindex: "0" });
    var V = SW.vlist(tbox, renderRow);
    left.appendChild(tbox);
    var stabs = h("div.dt-dtabs"), sbox = scrollBox("dt-dbody"), sflt = search("Filter", function (v) { filter = v; renderSide(); });
    [["styles", "Styles"], ["computed", "Computed"], ["layout", "Layout"]].forEach(function (t) {
      stabs.appendChild(h("button.dt-dtab", { type: "button", "data-t": t[0], "aria-selected": t[0] === stab ? "true" : "false", onclick: function () { stab = t[0]; SW.$$(".dt-dtab", stabs).forEach(function (b) { b.setAttribute("aria-selected", b.getAttribute("data-t") === stab ? "true" : "false"); }); renderSide(); } }, t[1]));
    });
    stabs.appendChild(h("span.grow")); stabs.appendChild(sflt);
    right.appendChild(stabs); right.appendChild(sbox.box); el.appendChild(left); el.appendChild(right);

    /* tree */
    function ingest(n, parent) {
      var old = nodes[n.id], nd = { n: n, parent: parent, open: old ? old.open : (n.type === 9 || n.name === "html" || n.name === "body"), kids: null };
      nodes[n.id] = nd;
      if (n.children) nd.kids = n.children.map(function (c) { return ingest(c, n.id); });
      else if (!n.kids) nd.kids = [];
      else if (old && old.kids && !n.children) nd.kids = old.kids;
      return n.id;
    }
    function loadKids(id) {
      var nd = nodes[id]; if (!nd) return Promise.resolve();
      return call("dom.children", { id: id, depth: 0 }).then(function (list) { nd.kids = (list || []).map(function (c) { return ingest(c, id); }); });
    }
    function load() {
      busy = true;
      return call("dom.root").then(function (r) {
        nodes = {}; rootId = r.id; ingest(r, 0);
        if (!sel || !nodes[sel]) { var b = findByName("body") || findByName("html"); sel = b || 0; }
        busy = false; render(); if (sel) pick(sel, true);
      }, function (e) { busy = false; rootId = 0; render(e.message); });
    }
    function findByName(name) { for (var id in nodes) if (nodes[id].n.name === name) return +id; return 0; }
    function inlineText(nd) {
      if (!nd.kids || nd.kids.length !== 1) return null; var k = nodes[nd.kids[0]];
      return k && k.n.type === 3 && k.n.value.length < 90 && k.n.value.indexOf("\n") < 0 ? k : null;
    }
    function flat() {
      var rows = [];
      (function walk(id, d) {
        var nd = nodes[id]; if (!nd) return; var n = nd.n;
        if (n.type === 9) { (nd.kids || []).forEach(function (k) { walk(k, 0); }); return; }
        rows.push({ id: id, d: d, k: "open" });
        if (n.type === 1 && nd.open && nd.kids && nd.kids.length && !inlineText(nd)) { nd.kids.forEach(function (k) { walk(k, d + 1); }); if (!VOID[n.name]) rows.push({ id: id, d: d, k: "close" }); }
      })(rootId, 0);
      return rows;
    }
    function render(err) {
      var rows = flat();
      V.set(rows.map(function (r) { r.h = 20; return r; }));
      if (!rows.length) { SW.clear(tbox.firstChild); tbox.firstChild.appendChild(emptyMsg(err || (busy ? "Loading..." : "No document."))); }
    }
    function expandable(nd) { return nd.n.type === 1 && nd.n.kids > 0 && !inlineText(nd); }
    function attrSpan(id, a) {
      var nd = nodes[id];
      var v = h("span.dt-av", { text: a[1].length > 90 ? a[1].slice(0, 90) + "…" : a[1], title: a[1] });
      if (!ro) v.addEventListener("dblclick", function (e) { e.stopPropagation(); editValue(v, a[1], function (nv) { call("dom.setAttr", { id: id, name: a[0], value: nv }).then(function () { refreshNode(id); }, function (er) { flash(er.message); }); }); });
      return h("span.dt-at", { text: " " }, h("span.dt-an", { text: a[0] }), tx("=\""), v, tx("\""));
    }
    function tx(s) { return document.createTextNode(s); }
    function textSpan(id, n, cls) {
      var v = h("span." + cls, { text: n.value });
      if (!ro && n.type === 3) v.addEventListener("dblclick", function (e) { e.stopPropagation(); editValue(v, n.value, function (nv) { call("dom.setText", { id: id, value: nv }).then(function () { refreshNode(nodes[id] ? nodes[id].parent : 0); }, function (er) { flash(er.message); }); }); });
      return v;
    }
    function editValue(span, val, commit) {
      editing = true;
      var inp = h("input.dt-edit", { type: "text", value: val, spellcheck: "false", style: { width: clamp(val.length * 7 + 24, 60, 520) + "px" } });
      var done = false;
      function fin(ok) { if (done) return; done = true; editing = false; if (ok && inp.value !== val) commit(inp.value); else render(); }
      inp.addEventListener("keydown", function (e) { e.stopPropagation(); if (e.key === "Enter") fin(true); else if (e.key === "Escape") fin(false); });
      inp.addEventListener("blur", function () { fin(true); });
      span.parentNode.replaceChild(inp, span); inp.focus(); try { inp.select(); } catch (e) { /* ignore */ }
    }
    function flash(msg) { var d = h("div.dt-flash", { text: msg }); el.appendChild(d); setTimeout(function () { if (d.parentNode) d.parentNode.removeChild(d); }, 3500); }
    function renderRow(r) {
      var nd = nodes[r.id], n = nd.n, pad = 10 + r.d * 14;
      var row = h("div.dt-er" + (r.id === sel ? ".sel" : ""), { style: { paddingLeft: pad + "px" } });
      row.addEventListener("click", function () { pick(r.id); });
      row.addEventListener("mouseenter", function () { highlight(r.id); });
      var arrow = h("span.dt-arrow" + (expandable(nd) ? "" : ".none")); if (expandable(nd)) { arrow.appendChild(icon(nd.open ? "chev" : "chevr", 12)); arrow.addEventListener("click", function (e) { e.stopPropagation(); toggle(r.id); }); }
      row.appendChild(arrow);
      if (r.k === "close") { row.appendChild(h("span.dt-tag", { text: "</" + n.name + ">" })); return row; }
      if (n.type === 1) {
        var open = h("span.dt-tag", tx("<"), h("span.dt-tn", { text: n.name }));
        (n.attrs || []).forEach(function (a) { open.appendChild(attrSpan(r.id, a)); });
        open.appendChild(tx(">")); row.appendChild(open);
        var it = inlineText(nd);
        if (it) { row.appendChild(textSpan(it.n.id, it.n, "dt-tx")); row.appendChild(h("span.dt-tag", { text: "</" + n.name + ">" })); }
        else if (n.kids && !nd.open) { row.appendChild(h("span.dt-dots", { text: "…" })); row.appendChild(h("span.dt-tag", { text: "</" + n.name + ">" })); }
        else if (!n.kids && !VOID[n.name]) row.appendChild(h("span.dt-tag", { text: "</" + n.name + ">" }));
        if (n.frame) row.appendChild(h("span.dim", { text: "  (a frame: its document is not shown)" }));
      } else if (n.type === 3) row.appendChild(textSpan(r.id, n, "dt-tx"));
      else if (n.type === 8) row.appendChild(h("span.dt-cm", { text: "<!--" + n.value + "-->" }));
      else if (n.type === 10) row.appendChild(h("span.dt-cm", { text: "<!DOCTYPE " + n.value + ">" }));
      return row;
    }
    function toggle(id) {
      var nd = nodes[id]; if (!nd) return; nd.open = !nd.open;
      if (nd.open && nd.kids === null) loadKids(id).then(render); else render();
    }
    function highlight(id) { clearTimeout(hlT); hlT = setTimeout(function () { if (hl === id) return; hl = id; call("dom.highlight", { id: id }).then(function () {}, function () {}); }, 40); }
    tbox.addEventListener("mouseleave", function () { highlight(sel); });
    function pick(id, quiet) {
      if (!nodes[id]) return; sel = id; if (!quiet) render(); hl = 0; highlight(id);
      var rows = V.count(); /* keep the row in view */
      var idx = flat().map(function (r) { return r.id + r.k; }).indexOf(id + "open");
      if (idx >= 0) { var top = idx * 20, sc = V.sc; if (top < sc.y) sc.set(top); else if (top + 20 > sc.y + tbox.clientHeight) sc.set(top + 20 - tbox.clientHeight); }
      loadStyle(id);
    }
    function loadStyle(id) {
      var n = nodes[id] && nodes[id].n;
      if (!n || n.type !== 1) { styleData = null; renderSide(); return; }
      call("dom.style", { id: id }).then(function (s) { if (id === sel) { styleData = s; renderSide(); } }, function (e) { if (id === sel) { styleData = { err: e.message }; renderSide(); } });
    }
    function refreshNode(id) {
      if (!id || !nodes[id]) return load();
      return call("dom.children", { id: id, depth: 0 }).then(function (list) { var nd = nodes[id]; nd.kids = list.map(function (c) { return ingest(c, id); }); return call("dom.node", { id: id }); }).then(function (n) { if (nodes[id]) { nodes[id].n.attrs = n.attrs; nodes[id].n.kids = n.kids; } render(); loadStyle(sel); }, function () { load(); });
    }
    tbox.addEventListener("keydown", function (e) {
      if (editing) return;
      var rows = flat(), idx = -1; rows.forEach(function (r, i) { if (r.id === sel && r.k === "open") idx = i; });
      var nd = nodes[sel], k = e.key;
      if (k === "ArrowDown" && idx < rows.length - 1) { e.preventDefault(); var nx = rows[idx + 1]; pick(nx.id); }
      else if (k === "ArrowUp" && idx > 0) { e.preventDefault(); pick(rows[idx - 1].id); }
      else if (k === "ArrowRight" && nd && expandable(nd)) { e.preventDefault(); if (!nd.open) toggle(sel); else if (idx < rows.length - 1) pick(rows[idx + 1].id); }
      else if (k === "ArrowLeft" && nd) { e.preventDefault(); if (nd.open && expandable(nd)) toggle(sel); else if (nd.parent && nodes[nd.parent] && nodes[nd.parent].n.type !== 9) pick(nd.parent); }
      else if ((k === "Delete" || k === "Backspace") && nd && !ro) {
        e.preventDefault(); var par = nd.parent, id = sel;
        call("dom.remove", { id: id }).then(function () { sel = par; refreshNode(par); }, function (er) { flash(er.message); });
      }
    });

    /* side pane */
    function swatch(v) { return /^(#[0-9a-f]{3,8}|rgba?\(|hsla?\()/i.test(v) ? h("i.dt-sw", { style: { background: v } }) : null; }
    function inlineWrite(id, decls) {
      var css = decls.filter(function (d) { return !d.off; }).map(function (d) { return d.name + ": " + d.value + (d.important ? " !important" : "") + ";"; }).join(" ");
      return call("dom.setAttr", { id: id, name: "style", value: css }).then(function () { return call("dom.style", { id: id }); }).then(function (s) { if (id === sel) { styleData = s; renderSide(); } }, function (e) { flash(e.message); });
    }
    function inlineDecls() { var r = styleData && styleData.rules && styleData.rules[0] && styleData.rules[0].inline ? styleData.rules[0].decls : []; return r.map(function (d) { return { name: d.name, value: d.value, important: d.important, overridden: d.overridden, off: false }; }).concat(disabled[sel] || []); }
    function rulesView(q) {
      var out = h("div"), rules = styleData.rules || [];
      var hasInline = rules.length && rules[0].inline;
      if (!hasInline) rules = [{ inline: true, selector: "element.style", source: "", decls: [] }].concat(rules);
      rules.forEach(function (r) {
        var decls = r.inline ? inlineDecls() : r.decls;
        var shownDecls = decls.filter(function (d) { return !q || (d.name + ":" + d.value).toLowerCase().indexOf(q) >= 0; });
        if (q && !shownDecls.length) return;
        var rule = h("div.dt-rule");
        if (r.at && r.at.length) rule.appendChild(h("div.dt-at-rule", { text: r.at.join(" ") }));
        var src = r.inline ? "" : baseName(r.source) + (r.line ? ":" + r.line : "");
        rule.appendChild(h("div.dt-rh", h("span.dt-rsel", { text: r.selector }), tx(" {"), h("span.grow"), src ? h("a.dt-loc", { href: "#", title: r.source + (r.line ? ":" + r.line : ""), onclick: function (e) { e.preventDefault(); show("sources"); P.sources.open(r.source, r.line); } }, src) : null));
        shownDecls.forEach(function (d, i) {
          var line = h("div.dt-decl" + (d.overridden ? ".over" : "") + (d.off ? ".off" : ""));
          if (r.inline && !ro) line.appendChild(h("input.dt-ck", { type: "checkbox", checked: d.off ? null : "checked", title: "Enable or disable this declaration", onchange: function (e) {
            var all = inlineDecls(), cur = all.filter(function (x) { return x.name === d.name && x.value === d.value; })[0]; if (!cur) return;
            cur.off = !e.target.checked; disabled[sel] = all.filter(function (x) { return x.off; }); inlineWrite(sel, all);
          } }));
          var nm = h("span.dt-pn", { text: d.name }), vl = h("span.dt-pv", { text: d.value + (d.important ? " !important" : "") });
          if (r.inline && !ro) vl.addEventListener("click", function () {
            editValue(vl, d.value, function (nv) { var all = inlineDecls(); all.forEach(function (x) { if (x.name === d.name && x.value === d.value) x.value = nv; }); disabled[sel] = all.filter(function (x) { return x.off; }); inlineWrite(sel, all); });
          });
          line.appendChild(nm); line.appendChild(tx(": ")); var sw = swatch(d.value); if (sw) line.appendChild(sw); line.appendChild(vl); line.appendChild(tx(";"));
          rule.appendChild(line);
        });
        if (r.inline && !ro) {
          var add = h("div.dt-decl.add", { text: "+ add a property", title: "Type name: value", onclick: function () {
            var inp = h("input.dt-edit", { type: "text", placeholder: "name: value", style: { width: "220px" } }); var done = false;
            function fin(ok) { if (done) return; done = true; editing = false; var m = /^\s*([\w-]+)\s*:\s*(.*?)\s*;?\s*$/.exec(inp.value); if (ok && m && m[2]) { var all = inlineDecls(); all.push({ name: m[1], value: m[2].replace(/\s*!important$/i, ""), important: /!important$/i.test(m[2]), off: false }); inlineWrite(sel, all); } else renderSide(); }
            editing = true; inp.addEventListener("keydown", function (e) { e.stopPropagation(); if (e.key === "Enter") fin(true); else if (e.key === "Escape") fin(false); }); inp.addEventListener("blur", function () { fin(true); });
            this.parentNode.replaceChild(inp, this); inp.focus();
          } });
          rule.appendChild(add);
        }
        rule.appendChild(h("div.dt-rend", { text: "}" }));
        out.appendChild(rule);
      });
      if (!out.firstChild) out.appendChild(emptyMsg("No matching rules."));
      return out;
    }
    function boxView(b) {
      function n(v) { return h("span.dt-bn", { text: String(Math.round(v * 100) / 100) }); }
      function layer(cls, label, vals, inner) {
        return h("div.dt-bl." + cls, h("span.dt-bt", { text: label }), h("span.dt-bv.t", n(vals[0])), h("span.dt-bv.r", n(vals[1])), h("span.dt-bv.b", n(vals[2])), h("span.dt-bv.l", n(vals[3])), inner);
      }
      var content = h("div.dt-bc", { text: Math.round(b.w * 100) / 100 + " × " + Math.round(b.h * 100) / 100 });
      return h("div.dt-box", layer("margin", "margin", b.margin, layer("border", "border", b.border, layer("padding", "padding", b.padding, content))));
    }
    function renderSide() {
      var inner = sbox.inner; SW.clear(inner);
      sflt.classList.toggle("hide", stab === "layout");
      if (!styleData) { inner.appendChild(emptyMsg(sel && nodes[sel] && nodes[sel].n.type !== 1 ? "Select an element to see its styles." : "Select an element to see its styles.")); sbox.sc.refresh(); return; }
      if (styleData.err) { inner.appendChild(emptyMsg(styleData.err)); return; }
      if (stab === "styles") inner.appendChild(rulesView(filter));
      else if (stab === "computed") {
        var list = h("div.dt-comp"); (styleData.computed || []).forEach(function (kv) { if (filter && (kv[0] + kv[1]).toLowerCase().indexOf(filter) < 0) return; var sw = swatch(kv[1]); list.appendChild(h("div.dt-cp", h("span.dt-pn", { text: kv[0] }), h("span.dt-pv", sw, tx(kv[1])))); });
        if (!list.firstChild) list.appendChild(emptyMsg("No matching properties.")); inner.appendChild(list);
      } else {
        if (styleData.box) inner.appendChild(boxView(styleData.box)); else inner.appendChild(emptyMsg(styleData.hidden ? "This element has display: none, so it has no box." : "This element has no box."));
      }
      sbox.sc.refresh();
    }

    return {
      el: el,
      reset: function () { nodes = {}; rootId = 0; sel = 0; gen = -1; nav = -1; styleData = null; disabled = {}; hl = 0; render(); renderSide(); },
      show: function () { gen = -1; this.poll(); tbox.focus(); },
      hide: function () { if (hl) { hl = 0; call("dom.highlight", { id: 0 }).then(function () {}, function () {}); } },
      reveal: function (id, path) {
        var i = 0;
        function step() {
          if (i >= path.length - 1) { sel = id; if (!nodes[id]) return load(); pick(id); return; }
          var pid = path[i++], nd = nodes[pid];
          if (!nd) { load().then(function () { i = 0; step(); }); return; }
          nd.open = true;
          if (nd.kids === null) loadKids(pid).then(step); else step();
        }
        if (!rootId) { load().then(step); return; } step();
      },
      poll: function () {
        if (busy || editing) return;
        return call("info").then(function (inf) {
          if (!inf) return;
          if (inf.nav !== nav || inf.url !== url) { nav = inf.nav; url = inf.url; gen = inf.gen; sel = 0; styleData = null; renderSide(); return load(); }
          if (inf.gen !== gen) { gen = inf.gen; return refreshOpen(); }
        }, function () {});
      }
    };
    function refreshOpen() {
      /* the page changed: re-read the children of every open node and keep what the person had open */
      var ids = []; for (var id in nodes) if (nodes[id].open && nodes[id].n.kids > 0 && nodes[id].kids) ids.push(+id);
      if (ids.length > 80) return load();
      busy = true;
      return Promise.all(ids.map(function (id) { return call("dom.children", { id: id, depth: 0 }).then(function (list) { return [id, list]; }, function () { return [id, null]; }); })).then(function (rs) {
        rs.forEach(function (r) { var nd = nodes[r[0]]; if (nd && r[1]) { nd.kids = r[1].map(function (c) { return ingest(c, r[0]); }); } });
        busy = false; if (sel && !nodes[sel]) sel = 0;
        render(); if (sel) loadStyle(sel);
      }, function () { busy = false; });
    }
  }

  /* ---- Sources --------------------------------------------------------------------------------------------- */
  function prettyJS(src) {
    var out = [], line = "", ind = 0, paren = 0, brk = 0, i = 0, n = src.length, prev = "", lastWord = "", sp = false;
    function pad() { var s = ""; for (var k = 0; k < ind; k++) s += "  "; return s; }
    function nl() { out.push(line.replace(/\s+$/, "")); line = ""; }
    function put(s) { if (!line) line = pad(); line += s; }
    function wordc(c) { return /[\w$\u0080-￿]/.test(c); }
    while (i < n) {
      var c = src.charAt(i), d = src.charAt(i + 1);
      if (c === "\n" || c === "\r" || c === "\t" || c === " ") {
        var j = i; while (j < n && /\s/.test(src.charAt(j))) j++;
        if (line && wordc(prev) && wordc(src.charAt(j))) put(" "); i = j; sp = true; continue;
      }
      if (c === "/" && d === "/") { var e1 = src.indexOf("\n", i); if (e1 < 0) e1 = n; put(src.slice(i, e1).replace(/\s+$/, "")); nl(); i = e1; prev = ";"; continue; }
      if (c === "/" && d === "*") { var e2 = src.indexOf("*/", i + 2); e2 = e2 < 0 ? n : e2 + 2; if (line) nl(); src.slice(i, e2).split("\n").forEach(function (l2) { put(l2.replace(/^\s+/, " ").replace(/\s+$/, "")); nl(); }); i = e2; continue; }
      if (c === '"' || c === "'" || c === "`") {
        var k2 = i + 1; while (k2 < n && src.charAt(k2) !== c) { if (src.charAt(k2) === "\\") k2++; k2++; }
        put(src.slice(i, k2 + 1)); i = k2 + 1; prev = "a"; continue;
      }
      if (c === "/" && (prev === "" || "(,=:[!&|?{};+-*%<>~^".indexOf(prev) >= 0 || /^(return|typeof|case|in|of|delete|void|throw|new)$/.test(lastWord))) {
        var k3 = i + 1, cls = false; while (k3 < n) { var ch = src.charAt(k3); if (ch === "\\") k3++; else if (ch === "[") cls = true; else if (ch === "]") cls = false; else if (ch === "/" && !cls) break; else if (ch === "\n") break; k3++; }
        k3++; while (k3 < n && /[a-z]/i.test(src.charAt(k3))) k3++;
        put(src.slice(i, k3)); i = k3; prev = "a"; continue;
      }
      if (c === "{") { put((line && prev !== "(" && prev !== "[" ? " " : "") + "{"); ind++; nl(); }
      else if (c === "}") { if (line) nl(); ind = Math.max(0, ind - 1); put("}"); var q2 = i + 1; while (q2 < n && /\s/.test(src.charAt(q2))) q2++; var nx = src.substr(q2, 8); if (/^(else|catch|finally|while)\b/.test(nx)) put(" "); else if (!/^[),;.\]]/.test(nx)) nl(); }
      else if (c === ";") { put(";"); if (paren === 0) nl(); else put(" "); }
      else if (c === ",") { put(","); if (paren === 0 && brk === 0) nl(); else put(" "); }
      else if (c === "(") { paren++; put("("); } else if (c === ")") { paren = Math.max(0, paren - 1); put(")"); }
      else if (c === "[") { brk++; put("["); } else if (c === "]") { brk = Math.max(0, brk - 1); put("]"); }
      else put(c);
      if (!/\s/.test(c)) { lastWord = wordc(c) ? (wordc(prev) && !sp ? lastWord + c : c) : ""; prev = c; sp = false; }
      i++;
    }
    if (line) nl(); return out.join("\n");
  }
  function prettyCSS(src) {
    var out = [], ind = 0, cur = "", i = 0, n = src.length;
    function pad() { var s = ""; for (var k = 0; k < ind; k++) s += "  "; return s; }
    function flush() { var t = cur.replace(/\s+/g, " ").trim(); cur = ""; return t; }
    while (i < n) {
      var c = src.charAt(i);
      if (c === "/" && src.charAt(i + 1) === "*") { var e = src.indexOf("*/", i + 2); e = e < 0 ? n : e + 2; var t0 = flush(); if (t0) out.push(pad() + t0); out.push(pad() + src.slice(i, e)); i = e; continue; }
      if (c === '"' || c === "'") { var k = i + 1; while (k < n && src.charAt(k) !== c) { if (src.charAt(k) === "\\") k++; k++; } cur += src.slice(i, k + 1); i = k + 1; continue; }
      if (c === "(") { var k2 = src.indexOf(")", i); k2 = k2 < 0 ? n : k2 + 1; cur += src.slice(i, k2); i = k2; continue; }
      if (c === "{") { out.push(pad() + flush() + " {"); ind++; }
      else if (c === "}") { var t1 = flush(); if (t1) out.push(pad() + t1 + ";"); ind = Math.max(0, ind - 1); out.push(pad() + "}"); }
      else if (c === ";") { var t2 = flush(); if (t2) out.push(pad() + t2 + ";"); }
      else cur += c;
      i++;
    }
    var t3 = flush(); if (t3) out.push(pad() + t3); return out.join("\n");
  }
  function sourcesPanel() {
    var list = [], selKey = "", textCache = {}, pretty = false, hx = 0, lines = [], wanted = null, status = "";
    var el = h("div.dt-row2"), lb = scrollBox("dt-slist"), right = h("div.dt-svw"), bar = h("div.dt-sbar"), cbox = h("div.dt-sb.dt-code"), V = SW.vlist(cbox, renderLine);
    var path = h("span.dt-spath.grow"), ppBtn = h("button.dt-chip", { type: "button", "aria-pressed": "false", title: "Format minified code", onclick: function () { pretty = !pretty; ppBtn.setAttribute("aria-pressed", pretty ? "true" : "false"); layout(); } }, "{ } Pretty print");
    bar.appendChild(path); bar.appendChild(ppBtn); bar.appendChild(h("button.dt-chip", { type: "button", onclick: function () { var t = textCache[selKey]; if (t != null) copy(t); } }, "Copy"));
    right.appendChild(bar); right.appendChild(cbox);
    el.appendChild(h("div.dt-sleft", h("div.dt-sech.pad", { text: "Page" }), lb.box)); el.appendChild(right);
    var gut = 56;
    cbox.addEventListener("wheel", function (e) { if (e.shiftKey || Math.abs(e.deltaX) > Math.abs(e.deltaY)) { e.preventDefault(); hx = Math.max(0, hx + (e.deltaX || e.deltaY)); V.refresh(); } }, { passive: false });
    function renderLine(r) {
      var t = r.t.length > 3000 ? r.t.slice(0, 3000) + "…" : r.t;
      return h("div.dt-ln", h("span.dt-lno", { text: String(r.n) }), h("span.dt-lt", h("span", { style: { marginLeft: -hx + "px" }, text: t })));
    }
    function layout() {
      var t = textCache[selKey]; if (t == null) { V.set([]); return; }
      var s = t, src = list.filter(function (x) { return x.key === selKey; })[0] || {}, mime = src.kind;
      if (pretty) {
        /* the formatter runs on this page's thread: it formats the first 400 kB and leaves the rest as it is */
        var head = t.length > 400000 ? t.slice(0, 400000) : t, rest = t.length > 400000 ? t.slice(400000) : "";
        try { s = (mime === "style" ? prettyCSS(head) : /\.json(\?|$)/.test(src.url || "") ? JSON.stringify(JSON.parse(t), null, 2) : mime === "document" ? t : prettyJS(head)) + (rest && mime !== "document" ? "\n" + rest : ""); } catch (e) { s = t; }
      }
      lines = s.split("\n"); hx = 0;
      V.set(lines.map(function (l, i) { return { h: 18, n: i + 1, t: l.replace(/\t/g, "    ").replace(/\r$/, "") }; }));
      status = lines.length + " lines  ·  " + fmtSize(t.length);
      path.textContent = (src.url || "") + "   " + status;
      if (wanted && wanted.line) { V.sc.set(Math.max(0, (wanted.line - 4) * 18)); wanted = null; }
    }
    function pickSrc(s) {
      selKey = s.key; renderList(); path.textContent = s.url;
      if (textCache[s.key] != null) return layout();
      V.set([]); SW.clear(cbox.firstChild); cbox.firstChild.appendChild(emptyMsg("Loading..."));
      call("src.get", { key: s.key }).then(function (r) { textCache[s.key] = r.text; if (selKey === s.key) layout(); }, function (e) { if (selKey === s.key) { SW.clear(cbox.firstChild); cbox.firstChild.appendChild(emptyMsg(e.message)); } });
    }
    function renderList() {
      var inner = lb.inner; SW.clear(inner);
      var groups = [["document", "Document"], ["script", "Scripts"], ["style", "Stylesheets"]];
      if (!list.length) inner.appendChild(emptyMsg("No scripts or stylesheets."));
      groups.forEach(function (g) {
        var items = list.filter(function (s) { return s.kind === g[0]; }); if (!items.length) return;
        inner.appendChild(h("div.dt-sech.pad", { text: g[1] + " (" + items.length + ")" }));
        items.forEach(function (s) {
          inner.appendChild(h("div.dt-si" + (s.key === selKey ? ".sel" : "") + (s.missing ? ".miss" : ""), { title: s.url + (s.error ? " — " + s.error : s.missing ? " — text not available" : ""), onclick: function () { pickSrc(s); } },
            h("b", { text: baseName(s.url) }), h("span.dim", { text: s.inline ? "inline" : hostOf(s.url) }), h("span.grow"), h("span.dim", { text: s.error ? "failed" : fmtSize(s.size) })));
        });
      });
      lb.sc.refresh();
    }
    function wantedSrc() { var u = wanted.url; return list.filter(function (s) { return s.url === u; })[0] || (u.charAt(0) === "<" ? list.filter(function (s) { return s.inline && s.kind === "script"; })[0] : null); }
    function refresh(sticky) {
      return call("src.list").then(function (l) {
        list = l || []; renderList();
        if (wanted) { var m = wantedSrc(); if (m) { pickSrc(m); return; } }
        if (!selKey || !list.some(function (s) { return s.key === selKey; })) { var f = list.filter(function (s) { return s.kind === "document"; })[0] || list[0]; if (f) pickSrc(f); else { selKey = ""; V.set([]); path.textContent = ""; } }
      }, function () {});
    }
    return {
      el: el,
      reset: function () { list = []; selKey = ""; textCache = {}; wanted = null; renderList(); V.set([]); path.textContent = ""; },
      show: function () { this.nav = -1; refresh(); },
      hide: function () {},
      open: function (url, line) {
        wanted = { url: url, line: line };
        var m = wantedSrc();
        if (m) { if (m.key === selKey) { layout(); wanted = null; } else pickSrc(m); } else refresh();
      },
      poll: function () { var self = this; return call("info").then(function (inf) { if (inf && inf.nav !== self.nav) { self.nav = inf.nav; textCache = {}; selKey = ""; refresh(); } }, function () {}); }
    };
  }

  /* ---- Application ----------------------------------------------------------------------------------------- */
  function applicationPanel() {
    var el = h("div.dt-col"), sb = scrollBox("dt-app"), info = null, err = "";
    el.appendChild(h("div.dt-tools", ibtn("refresh", "Refresh", function () { load(); }), h("span.dim", { text: "Cookies and storage of this page, read only." }))); el.appendChild(sb.box);
    function table(title, cols, rows, empty) {
      var t = h("div.dt-tbl"); cols.forEach(function (c) { t.appendChild(h("span.dt-th", { text: c })); });
      rows.forEach(function (r) { r.forEach(function (v, i) { t.appendChild(h("span.dt-td", { text: v == null ? "" : String(v), title: String(v == null ? "" : v) })); }); });
      t.style.gridTemplateColumns = cols.length === 2 ? "minmax(0, 1fr) minmax(0, 3fr)" : "minmax(0, 1.2fr) minmax(0, 2fr) minmax(0, 1.4fr) minmax(0, .6fr) minmax(0, 1.2fr) minmax(0, 1fr)";
      return h("div.dt-sec", h("div.dt-sech", { text: title + " (" + rows.length + ")" }), rows.length ? t : h("div.dt-none", { text: empty }));
    }
    function render() {
      var inner = sb.inner; SW.clear(inner);
      if (err) { inner.appendChild(emptyMsg(err)); sb.sc.refresh(); return; }
      if (!info) { inner.appendChild(emptyMsg("Loading...")); return; }
      inner.appendChild(h("div.dt-sech.pad", { text: info.origin }));
      var ck = (info.cookies || []).map(function (c) { return [c.name, c.value, c.domain, c.path, c.expires ? new Date(c.expires * 1000).toISOString().slice(0, 19).replace("T", " ") : "Session", (c.httpOnly ? "HttpOnly " : "") + (c.secure ? "Secure " : "") + (c.sameSite || "")]; });
      inner.appendChild(table("Cookies", ["Name", "Value", "Domain", "Path", "Expires", "Flags"], ck, "No cookies for this page."));
      inner.appendChild(table("Local storage", ["Key", "Value"], info.local || [], "Empty."));
      inner.appendChild(table("Session storage", ["Key", "Value"], info.session || [], "Empty."));
      sb.sc.refresh();
    }
    function load() { return call("app.get").then(function (r) { info = r; err = ""; render(); }, function (e) { err = e.message; render(); }); }
    return { el: el, reset: function () { info = null; err = ""; render(); }, show: function () { load(); }, hide: function () {} };
  }
  D.sync(); /* loaded because the panel was asked for: build it now */
  post("devtools.ready", {}); /* the listeners above exist: the app may now send what waited for them (an Inspect selection) */
})();
