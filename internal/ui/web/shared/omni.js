/* Omnibox engine shared by the toolbar and the new tab page.
   Instant: every keystroke rebuilds rows synchronously from local knowledge and the previous
   remote results (kept until fresh ones arrive), then asks Go (omni.input) for better ones.
   Hosts that set sweef.sync answer omni.input inside the same event turn (a real result, or {stale:true}
   when they could not make it in time): the rows are then built once, from the answer, and one frame is painted.
   The dropdown is patched in place: rows are reused and a node is only touched when its content changed. */
(function () {
  var SW = window.SW = window.SW || {}, h = SW.h, S = window.S;
  SW.omniSeq = SW.omniSeq || 0; SW.omniLast = null;
  var SEC = { history: S.omni.secHistory, bookmark: S.omni.secBookmarks, tab: S.omni.secTabs, search: S.omni.secSearch, ask: S.f(S.omni.secAsk) };

  function Omni(o) {
    this.o = o; this.input = o.input; this.list = o.list; this.ghost = o.ghost || null;
    this.remote = []; this.rows = []; this.sel = -1; this.req = {}; this.shown = 0; this.open = false;
    this.kids = []; this.foot = null; this.tog = null; this.wait = 0; this.gI = null; this.gB = null; this.actId = "";
    this.typed = ""; this.completion = ""; this.noGhost = false; this.pasted = false; this.uid = "ob" + (++SW.omniSeq);
    var me = this, inp = this.input;
    inp.setAttribute("role", "combobox"); inp.setAttribute("aria-autocomplete", "both"); inp.setAttribute("aria-expanded", "false");
    inp.setAttribute("autocomplete", "off"); inp.setAttribute("spellcheck", "false");
    if (!this.list.id) this.list.id = this.uid + "l"; inp.setAttribute("aria-controls", this.list.id); this.list.setAttribute("role", "listbox"); this.list.setAttribute("aria-label", "Suggestions");
    inp.addEventListener("input", function (e) { me.noGhost = /^delete/.test(e.inputType || ""); me.onInput(); });
    inp.addEventListener("keydown", function (e) { me.onKey(e); });
    inp.addEventListener("paste", function (e) {
      var all = inp.selectionStart === 0 && inp.selectionEnd === inp.value.length;
      var t = ""; try { t = (e.clipboardData && e.clipboardData.getData("text")) || ""; } catch (x) {}
      me.pasted = (all || !inp.value) && /^\s*\S+\s*$/.test(t) && SW.looksLikeURL(t.trim());
    });
    this.list.addEventListener("mousedown", function (e) { e.preventDefault(); });
    this.list.addEventListener("click", function (e) {
      var r = e.target.closest ? e.target.closest(".orow") : null; if (!r) return;
      me.commit(me.rows[+r.getAttribute("data-i")], e);
    });
    this.list.addEventListener("mousemove", function (e) {
      var r = e.target.closest ? e.target.closest(".orow") : null; if (!r) return;
      var i = +r.getAttribute("data-i"); if (i !== me.sel) me.select(i, true);
    });
    SW.on("omni.suggestions", function (p) { me.onRemote(p || {}); });
  }
  var P = Omni.prototype;
  P.natural = function (t) { return /\?\s*$/.test(t) || t.trim().split(/\s+/).length >= 4; };
  P.ask = function (text, id) {
    this.req[id] = 1; delete this.req[id - 24]; SW.omniLast = this;
    SW.post("omni.input", { text: text, id: id, scope: this.o.scope || "bar", limit: 8 });
    return !!window.sweef.sync;
  };
  P.prime = function () {
    this.typed = ""; this.remote = []; this.dirty = false; this.pasted = false;
    var id = ++SW.omniSeq;
    if (this.ask("", id)) { this.wait = id; return; }
    this.rebuild(false);
  };
  P.onInput = function () {
    var t = this.input.value; this.typed = t; this.dirty = true;
    var id = ++SW.omniSeq;
    var q = t.trim().toLowerCase();
    if (q && this.remote.length) {
      var ws = q.split(/\s+/);
      this.remote = this.remote.filter(function (it) {
        var s = it._s || (it._s = ((it.title || "") + " " + (it.url || "") + " " + (it.text || "")).toLowerCase());
        for (var i = 0; i < ws.length; i++) if (s.indexOf(ws[i]) < 0) return false;
        return true;
      });
    }
    if (this.ask(t, id)) { this.wait = id; return; }
    this.rebuild(true);
  };
  P.onRemote = function (p) {
    var mine = (p.id != null && this.req[p.id]) || (p.id == null && SW.omniLast === this);
    if (!mine) return;
    var waited = this.wait && (p.id == null || p.id >= this.wait); if (waited) this.wait = 0;
    var live = this.open || document.activeElement === this.input;
    if (p.stale) { if (waited && live) this.rebuild(true); return; }  // the host could not answer in time: show what we have
    if (p.id != null && p.id < this.shown) { if (waited && live) this.rebuild(true); return; }
    if (p.id != null) this.shown = p.id;
    this.remote = (p.items || []).slice(0, 24); this.remoteInline = p.inline || "";
    if (live) this.rebuild(false);
  };
  P.rebuild = function (keepSel) {
    var t = this.typed.trim(), rows = [], by = { history: [], bookmark: [], tab: [], search: [] }, me = this;
    this.remote.forEach(function (it) { var k = it.type === "nav" ? "history" : it.type; if (by[k]) by[k].push(it); });
    var topURL = t && SW.looksLikeURL(t);
    var go = t ? (topURL ? { kind: "go", title: t, sub: S.omni.goTo, url: t } : { kind: "search", title: t, sub: S.omni.searchFor, text: t }) : null;
    var ask = t ? { kind: "ask", title: t, sub: S.omni.askSub, text: t, sec: "ask" } : null;
    var preferAsk = t && !topURL && this.o.defaultAsk && this.o.defaultAsk(t);
    if (this.pasted && go && topURL) go.sub = S.omni.pasteGo;
    if (preferAsk) { rows.push(ask); ask.sec = ""; }
    if (go) rows.push(go);
    var seen = {}; if (go && go.url) seen[SW.prettyURL(go.url)] = 1;
    function addAll(arr, kind, max) {
      var n = 0; arr.forEach(function (it) {
        if (n >= max) return; var u = it.url ? SW.prettyURL(it.url) : "", key = u || ("s:" + it.text);
        if (seen[key]) return; seen[key] = 1; n++;
        rows.push({ kind: kind, title: it.title || it.text || it.url, sub: it.url ? SW.prettyURL(it.url) : "", url: it.url, text: it.text, tabId: it.tab || it.tabId, sec: kind });
      });
    }
    addAll(by.tab, "tab", 2); addAll(by.history, "history", 4); addAll(by.bookmark, "bookmark", 3); addAll(by.search, "search", 4);
    if (ask && !preferAsk) rows.push(ask);
    var cap = Math.max(5, Math.floor((window.innerHeight - 190) / 40)); if (rows.length > cap) rows = rows.slice(0, cap);
    this.rows = rows;
    var prev = !t ? -1 : keepSel && this.sel >= 0 && this.rows[this.sel] ? this.sel : (rows.length ? 0 : -1);
    this.sel = prev;
    this.computeGhost();
    this.render();
  };
  P.computeGhost = function () {
    this.completion = "";
    var t = this.typed, inp = this.input;
    if (!this.ghost) return;
    if (!t || this.noGhost || inp.selectionStart !== t.length || inp.selectionEnd !== t.length || /\s/.test(t) || /^[a-z]+:\/\//i.test(t) && false) return this.renderGhost();
    var low = t.toLowerCase(), cand = "";
    var pool = []; if (this.remoteInline) pool.push(this.remoteInline);
    this.remote.forEach(function (it) { if (it.inline) pool.push(it.inline); });
    this.remote.forEach(function (it) { if (it.url && (it.type === "history" || it.type === "bookmark" || it.type === "nav")) pool.push(SW.prettyURL(it.url)); });
    var tops = (SW.state.topSites || []); tops.forEach(function (s) { pool.push(SW.prettyURL(s.url)); });
    for (var i = 0; i < pool.length; i++) {
      var c = pool[i]; if (c.toLowerCase().indexOf(low) === 0 && c.length > t.length) { cand = c; break; }
    }
    if (cand) {
      var rest = cand.slice(t.length);
      if (t.indexOf("/") < 0) { var s = rest.indexOf("/"); if (s >= 0) rest = rest.slice(0, s + 1); }
      this.completion = rest;
    }
    this.renderGhost();
  };
  P.renderGhost = function () {
    var g = this.ghost; if (!g) return;
    if (!this.completion) { if (g.firstChild) { SW.clear(g); this.gI = this.gB = null; } return; }
    if (!this.gI || this.gI.parentNode !== g) { SW.clear(g); this.gI = h("i", this.typed); this.gB = h("b", this.completion); g.appendChild(this.gI); g.appendChild(this.gB); this.gI._t = this.typed; this.gB._t = this.completion; }
    if (this.gI._t !== this.typed) { this.gI._t = this.typed; this.gI.firstChild.nodeValue = this.typed; }
    if (this.gB._t !== this.completion) { this.gB._t = this.completion; this.gB.firstChild.nodeValue = this.completion; }
  };
  /* Rows are cloned from a template per kind (cloning is ~10x cheaper than building a row with h() and parsing the
     icon's SVG). The selection hint (the enter key cap) is part of every row and shown by CSS on .sel only, so moving
     the selection changes two class names and nothing else. */
  var ICON_ARGS = { ask: ["spark", "oi ask"], search: ["search", "oi"], go: ["globe", "oi"], tab: ["tab", "oi"], bookmark: ["starfill", "oi bm"], history: ["clock", "oi"] }, TPL = {}, SECTPL = null;
  function plainSub(kind) { return kind === "ask" || kind === "go" || kind === "search" || kind === "tab"; }
  /* the text of the second column: null means the row has no sub element at all */
  function subOf(r) { return r.kind === "tab" ? S.omni.switchTab : plainSub(r.kind) ? (r.sub == null ? "" : r.sub) : (r.sub || null); }
  function str(v) { return v == null ? "" : String(v); }
  function tpl(kind, hasSub) {
    var k = (ICON_ARGS[kind] ? kind : "history") + (hasSub ? 1 : 0), t = TPL[k];
    if (!t) {
      var ia = ICON_ARGS[ICON_ARGS[kind] ? kind : "history"];
      t = TPL[k] = h("div", { role: "option", "aria-selected": "false", class: "orow" + (kind === "ask" ? " ask" : "") }, SW.icon(ia[0], 16, ia[1]),
        h("span.otx", h("span.ot.trunc", ""), hasSub ? h(plainSub(kind) ? "span.os" : "span.os.trunc", "") : null), h("span.oact", [h("span.kbd", { text: "↵" })]));
    }
    return t;
  }
  P.makeRow = function (r) {
    var sub = subOf(r), has = sub !== null, el = tpl(r.kind, has).cloneNode(true), otx = el.childNodes[1];
    return { row: 1, el: el, kind: r.kind, hasSub: has, base: el.className, ot: otx.firstChild.firstChild, os: has ? otx.childNodes[1].firstChild : null, t: "", s: "", i: -1, on: false };
  };
  P.markRow = function (k, on) {
    if (k.on === on) return; k.on = on;
    k.el.className = on ? k.base + " sel" : k.base; k.el.setAttribute("aria-selected", on ? "true" : "false");
  };
  P.patchRow = function (k, r, i) {
    var t = str(r.title); if (k.t !== t) { k.t = t; k.ot.nodeValue = t; }
    if (k.os) { var s = str(subOf(r)); if (k.s !== s) { k.s = s; k.os.nodeValue = s; } }
    if (k.i !== i) { k.i = i; k.el.id = this.uid + "r" + i; k.el.setAttribute("data-i", i); }
    this.markRow(k, i === this.sel);
  };
  P.render = function () {
    var l = this.list, me = this, kids = this.kids, rows = this.rows, last = "";
    var show = rows.length > 0 && document.activeElement === this.input;
    if (this.o.alwaysShow) show = rows.length > 0;
    this.open = show;
    if (this.shownCls !== show) { this.shownCls = show; l.classList.toggle("open", show); }
    if (this.expanded !== show) { this.expanded = show; this.input.setAttribute("aria-expanded", show ? "true" : "false"); }
    if (!show) { if (this.actId) { this.actId = ""; this.input.removeAttribute("aria-activedescendant"); } this.toggle(false); return; }
    var j = 0;
    function put(k) {  // k replaces whatever is at position j
      var old = kids[j];
      if (old) l.replaceChild(k.el, old.el); else l.insertBefore(k.el, me.foot);
      kids[j] = k;
    }
    for (var i = 0; i < rows.length; i++) {
      var r = rows[i], secKey = r.sec && r.sec !== "search" || r.sec === "search" && i > 0 ? r.sec : "", k;
      if (i > 0 && secKey && secKey !== last) {
        var st = SEC[secKey] || ""; k = kids[j];
        if (k && k.sec) { if (k.t !== st) { k.t = st; k.tn.nodeValue = st; } }
        else { var se = (SECTPL || (SECTPL = h("div.osec", { role: "presentation" }, ""))).cloneNode(true); se.firstChild.nodeValue = st; put({ sec: 1, t: st, el: se, tn: se.firstChild }); }
        j++;
      }
      last = secKey || last;
      k = kids[j];
      if (k && k.row && k.kind === r.kind && k.hasSub === (subOf(r) !== null)) this.patchRow(k, r, i);
      else { k = this.makeRow(r); this.patchRow(k, r, i); put(k); }
      j++;
    }
    while (kids.length > j) l.removeChild(kids.pop().el);
    if (this.o.footer !== false) {
      if (!this.foot) this.foot = h("div.ofoot", h("span", { text: S.omni.hint }), h("span", { text: SW.mod === "Cmd" ? S.omni.hintAskMac : S.omni.hintAsk }));
      if (this.foot.parentNode !== l || l.lastChild !== this.foot) l.appendChild(this.foot);
    }
    var aid = this.sel >= 0 && rows[this.sel] ? this.uid + "r" + this.sel : "";
    if (aid && aid !== this.actId) { this.actId = aid; this.input.setAttribute("aria-activedescendant", aid); }
    this.toggle(true);
  };
  P.toggle = function (on) { if (this.tog !== on) { this.tog = on; if (this.o.onToggle) this.o.onToggle(on); } };
  P.select = function (i, noText) {
    var n = this.rows.length; if (!n) return;
    i = (i + n) % n; this.sel = i;
    var kids = this.kids;
    for (var j = 0; j < kids.length; j++) if (kids[j].row) this.markRow(kids[j], kids[j].i === i);
    var r = this.rows[i];
    if (!noText && r) { this.input.value = r.kind === "ask" || r.kind === "search" ? r.text : (r.url || r.title); this.completion = ""; this.renderGhost(); }
    this.actId = this.uid + "r" + i; this.input.setAttribute("aria-activedescendant", this.actId);
  };
  P.close = function (revert) {
    if (revert && this.o.revert) this.o.revert();
    this.open = false; if (this.shownCls !== false) { this.shownCls = false; this.list.classList.remove("open"); }
    this.input.setAttribute("aria-expanded", "false"); this.expanded = false; this.completion = ""; this.renderGhost();
    this.tog = false; if (this.o.onToggle) this.o.onToggle(false);
  };
  P.commit = function (row, ev) {
    var t = this.input.value.trim();
    if (!row) { if (!t) return; row = SW.looksLikeURL(t) ? { kind: "go", url: t } : { kind: "search", text: t }; }
    var tab = this.o.getTab ? this.o.getTab() : "";
    if (row.kind === "ask") { if (this.o.onAsk) this.o.onAsk(row.text || t); }
    else if (row.kind === "tab" && row.tabId) SW.post("tab.activate", { id: row.tabId });
    else if (row.kind === "search") SW.post("nav.go", { tab: tab, input: row.text || t });
    else SW.post("nav.go", { tab: tab, input: row.url || t });
    this.pasted = false; this.remote = []; this.close(false);
    if (this.o.onDone) this.o.onDone(row);
  };
  P.onKey = function (e) {
    var k = e.key, me = this, inp = this.input;
    if (k === "ArrowDown" || k === "ArrowUp") {
      if (!this.rows.length) return; e.preventDefault();
      if (!this.open) { this.render(); return; }
      this.select(this.sel + (k === "ArrowDown" ? 1 : -1)); return;
    }
    if (k === "Enter") {
      e.preventDefault();
      var t = inp.value.trim();
      if (e.ctrlKey || e.metaKey) { if (t && this.o.onAsk) { this.commit({ kind: "ask", text: t }); } return; }
      if (this.completion && this.sel <= 0 && inp.selectionStart === inp.value.length) { this.commit({ kind: "go", url: this.typed + this.completion }); return; }
      if (this.sel < 0) { this.commit(null); return; }
      var r = this.rows[this.sel]; if (r && this.input.value.trim() !== this.typed.trim() && r.kind !== "ask") { this.commit(r); return; }
      this.commit(r || null); return;
    }
    if (k === "Tab" && !e.shiftKey && this.completion) { e.preventDefault(); inp.value = this.typed + this.completion; this.typed = inp.value; this.onInput(); return; }
    if (k === "ArrowRight" && this.completion && inp.selectionStart === inp.value.length) { e.preventDefault(); inp.value = this.typed + this.completion; this.typed = inp.value; this.onInput(); return; }
    if (k === "Escape") {
      if (this.open) { e.preventDefault(); if (this.input.value !== this.typed) { this.input.value = this.typed; this.rebuild(true); } else this.close(true); }
      else if (this.o.onEscape) this.o.onEscape(e);
      return;
    }
  };
  P.focusSelect = function () { this.input.focus(); this.input.select(); };
  SW.Omni = Omni;
})();
