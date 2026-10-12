/* Omnibox engine shared by the toolbar and the new tab page.
   Instant: every keystroke rebuilds rows synchronously from local knowledge and the previous
   remote results (kept until fresh ones arrive), then asks Go (omni.input) for better ones. */
(function () {
  var SW = window.SW = window.SW || {}, h = SW.h, S = window.S;
  SW.omniSeq = SW.omniSeq || 0; SW.omniLast = null;
  var SEC = { history: S.omni.secHistory, bookmark: S.omni.secBookmarks, tab: S.omni.secTabs, search: S.omni.secSearch, ask: S.f(S.omni.secAsk) };

  function Omni(o) {
    this.o = o; this.input = o.input; this.list = o.list; this.ghost = o.ghost || null;
    this.remote = []; this.rows = []; this.sel = -1; this.req = {}; this.shown = 0; this.open = false;
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
  P.prime = function () {
    this.typed = ""; this.remote = []; this.dirty = false; this.pasted = false;
    var id = ++SW.omniSeq; this.req[id] = 1; SW.omniLast = this;
    SW.post("omni.input", { text: "", id: id, scope: this.o.scope || "bar", limit: 8 });
    this.rebuild(false);
  };
  P.onInput = function () {
    var t = this.input.value; this.typed = t; this.dirty = true;
    var id = ++SW.omniSeq; this.req[id] = 1; SW.omniLast = this;
    var keys = Object.keys(this.req); if (keys.length > 24) delete this.req[keys[0]];
    this.remote = this.remote.filter(function (it) { var q = t.trim().toLowerCase(); if (!q) return true; var s = ((it.title || "") + " " + (it.url || "") + " " + (it.text || "")).toLowerCase(); return q.split(/\s+/).every(function (w) { return s.indexOf(w) >= 0; }); });
    SW.post("omni.input", { text: t, id: id, scope: this.o.scope || "bar", limit: 8 });
    this.rebuild(true);
  };
  P.onRemote = function (p) {
    var mine = (p.id != null && this.req[p.id]) || (p.id == null && SW.omniLast === this);
    if (!mine || (p.id != null && p.id < this.shown)) return;
    if (p.id != null) this.shown = p.id;
    this.remote = (p.items || []).slice(0, 24); this.remoteInline = p.inline || "";
    if (this.open || document.activeElement === this.input) this.rebuild(false);
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
        rows.push({ kind: kind, title: it.title || it.text || it.url, sub: it.url ? SW.prettyURL(it.url) : "", url: it.url, text: it.text, fav: it.favicon, tabId: it.tab || it.tabId, sec: kind });
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
    SW.clear(g);
    if (this.completion) { g.appendChild(h("i", { text: this.typed })); g.appendChild(h("b", { text: this.completion })); }
  };
  P.render = function () {
    var l = this.list, me = this, last = "";
    SW.clear(l);
    var show = this.rows.length > 0 && document.activeElement === this.input;
    if (this.o.alwaysShow) show = this.rows.length > 0;
    this.open = show; l.classList.toggle("open", show);
    this.input.setAttribute("aria-expanded", show ? "true" : "false");
    if (!show) { this.input.removeAttribute("aria-activedescendant"); if (this.o.onToggle) this.o.onToggle(false); return; }
    this.rows.forEach(function (r, i) {
      var secKey = r.sec && r.sec !== "search" || r.sec === "search" && i > 0 ? r.sec : "";
      if (i > 0 && secKey && secKey !== last) l.appendChild(h("div.osec", { role: "presentation", text: SEC[secKey] || "" }));
      last = secKey || last;
      var ic = r.kind === "ask" ? SW.icon("spark", 16, "oi ask") : r.kind === "search" ? SW.icon("search", 16, "oi") : r.kind === "go" ? SW.icon("globe", 16, "oi") : r.kind === "tab" ? SW.icon("tab", 16, "oi") : r.kind === "bookmark" ? SW.icon("starfill", 16, "oi bm") : SW.icon("clock", 16, "oi");
      var title = h("span.ot.trunc", { text: r.title });
      var sub = r.kind === "ask" || r.kind === "go" || r.kind === "search" ? h("span.os", { text: r.sub }) : (r.sub ? h("span.os.trunc", { text: r.sub }) : null);
      if (r.kind === "tab") sub = h("span.os", { text: S.omni.switchTab });
      var el = h("div.orow" + (i === me.sel ? ".sel" : ""), { role: "option", id: me.uid + "r" + i, "data-i": i, "aria-selected": i === me.sel ? "true" : "false" }, ic, h("span.otx", title, sub),
        i === me.sel ? h("span.oact", r.kind === "ask" ? [h("span.kbd", { text: "↵" })] : [h("span.kbd", { text: "↵" })]) : null);
      if (r.kind === "ask") el.classList.add("ask");
      l.appendChild(el);
    });
    var act = this.rows[this.sel]; if (act) this.input.setAttribute("aria-activedescendant", me.uid + "r" + this.sel);
    var foot = h("div.ofoot", h("span", { text: S.omni.hint }), h("span", { text: SW.mod === "Cmd" ? S.omni.hintAskMac : S.omni.hintAsk }));
    if (this.o.footer !== false) l.appendChild(foot);
    if (this.o.onToggle) this.o.onToggle(true);
  };
  P.select = function (i, noText) {
    var n = this.rows.length; if (!n) return;
    i = (i + n) % n; this.sel = i;
    Array.prototype.forEach.call(this.list.querySelectorAll(".orow"), function (el) { var on = +el.getAttribute("data-i") === i; el.classList.toggle("sel", on); el.setAttribute("aria-selected", on ? "true" : "false"); var a = el.querySelector(".oact"); if (a) a.style.display = on ? "" : "none"; });
    var r = this.rows[i];
    if (!noText && r) { this.input.value = r.kind === "ask" || r.kind === "search" ? r.text : (r.url || r.title); this.completion = ""; this.renderGhost(); }
    this.input.setAttribute("aria-activedescendant", this.uid + "r" + i);
  };
  P.close = function (revert) {
    if (revert && this.o.revert) this.o.revert();
    this.open = false; this.list.classList.remove("open"); SW.clear(this.list);
    this.input.setAttribute("aria-expanded", "false"); this.completion = ""; this.renderGhost();
    if (this.o.onToggle) this.o.onToggle(false);
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
