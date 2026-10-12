/* Command palette (Ctrl/Cmd+K): tabs, commands, history, bookmarks, "Ask Sweef: ...". */
(function () {
  var SW = window.SW, S = window.S, h = SW.h, icon = SW.icon, post = SW.post, sc = null, el = null, input, list, rows = [], sel = 0, remote = [], req = {}, mode = "", prevFocus = null;
  function score(q, s) {
    s = s.toLowerCase(); if (!q) return 1; var i = s.indexOf(q); if (i >= 0) return 100 - i + (i === 0 ? 20 : 0);
    var j = 0, n = 0; for (var k = 0; k < s.length && j < q.length; k++) if (s[k] === q[j]) { j++; n++; } return j === q.length ? 20 + n : 0;
  }
  function build() {
    var q = input.value.trim(), lq = q.toLowerCase(), out = [], tabs = (SW.state.tabs || []);
    var push = function (sec, items) { items.forEach(function (it, i) { it.sec = i === 0 ? sec : ""; out.push(it); }); };
    var t = tabs.map(function (x) { return { s: score(lq, (x.title || "") + " " + (x.url || "")), kind: "tab", ic: "tab", title: x.title || SW.host(x.url), sub: SW.host(x.url), run: function () { post("tab.activate", { id: x.id }); } }; }).filter(function (x) { return x.s; }).sort(function (a, b) { return b.s - a.s; }).slice(0, mode === "tabs" ? 12 : 5);
    push(S.cmd.tabs, t);
    if (mode !== "tabs") {
      var c = S.cmds.map(function (x) { var name = S.f(x[0]); return { s: score(lq, name), kind: "cmd", ic: x[1].indexOf("theme") === 0 ? "brush" : x[1].indexOf("page:") === 0 ? "globe" : "zap", title: name, kbd: x[2] ? x[2].replace("Ctrl", SW.mod) : "", run: function () { SW.chrome.run(x[1]); } }; }).filter(function (x) { return x.s; }).sort(function (a, b) { return b.s - a.s; }).slice(0, q ? 5 : 6);
      push(S.cmd.commands, c);
      if (q) {
        push(S.cmd.history, remote.filter(function (r) { return r.type !== "bookmark" && r.url; }).slice(0, 4).map(function (r) { return { kind: "h", ic: "clock", title: r.title || r.url, sub: SW.prettyURL(r.url), run: function () { post("nav.go", { tab: SW.state.active, input: r.url }); } }; }));
        push(S.cmd.bookmarks, remote.filter(function (r) { return r.type === "bookmark"; }).slice(0, 3).map(function (r) { return { kind: "b", ic: "starfill", title: r.title || r.url, sub: SW.prettyURL(r.url), run: function () { post("nav.go", { tab: SW.state.active, input: r.url }); } }; }));
        out.push({ kind: "ask", ic: "spark", title: S.f(S.cmd.ask, { q: q }), sec: "", run: function () { SW.sidekick.ask(q, "palette"); } });
      }
    }
    rows = out; sel = Math.min(sel, Math.max(0, rows.length - 1)); render(); sc.refresh();
  }
  function render() {
    SW.clear(list);
    if (!rows.length) { list.appendChild(h("div", { style: { padding: "20px 14px", color: "var(--text-3)" }, text: S.f(S.cmd.none) })); return; }
    rows.forEach(function (r, i) {
      if (r.sec) list.appendChild(h("div.osec", { text: r.sec }));
      list.appendChild(h("div.orow" + (i === sel ? ".sel" : "") + (r.kind === "ask" ? ".ask" : ""), { role: "option", "aria-selected": i === sel ? "true" : "false", id: "pr" + i, onmouseenter: function () { sel = i; mark(); }, onclick: function () { exec(r); } },
        icon(r.ic, 16, "oi" + (r.kind === "ask" ? " ask" : "")), h("span.otx", h("span.ot.trunc", { text: r.title }), r.sub ? h("span.os.trunc", { text: r.sub }) : null), r.kbd ? h("span.kbd", { text: r.kbd }) : null));
    });
  }
  function mark() { SW.$$(".orow", list).forEach(function (n) { var i = +(n.id || "pr-1").slice(2); n.classList.toggle("sel", i === sel); n.setAttribute("aria-selected", i === sel ? "true" : "false"); }); input.setAttribute("aria-activedescendant", "pr" + sel); var cur = document.getElementById("pr" + sel); if (cur && sc) sc.reveal(cur); }
  function exec(r) { close(); if (r && r.run) r.run(); }
  function close() { if (!el) return; el.parentNode && el.parentNode.removeChild(el); el = null; if (prevFocus && prevFocus.focus) try { prevFocus.focus(); } catch (e) {} }
  function open(m) {
    if (el) return close(); mode = m || ""; sel = 0; remote = []; prevFocus = document.activeElement;
    input = h("input", { type: "text", role: "combobox", "aria-expanded": "true", "aria-controls": "pall", "aria-label": S.f(S.cmd.placeholder), placeholder: mode === "tabs" ? "Search tabs" : S.f(S.cmd.placeholder), spellcheck: "false", autocomplete: "off" });
    var lbox = h("div.pl"); list = h("div", { id: "pall", role: "listbox", style: { padding: "8px" } }); lbox.appendChild(list); sc = SW.scroller(lbox, list);
    el = h("div#pal", { onmousedown: function (e) { if (e.target === el) close(); } }, h("div.panel", { role: "dialog", "aria-modal": "true", "aria-label": "Command palette" }, h("div.ph", icon("search", 18), input, h("span.kbd", { text: "Esc" })), lbox, h("div.pf", h("span", { text: S.cmd.hint }), h("span", { text: "↑↓" }))));
    document.getElementById("layer").appendChild(el);
    input.addEventListener("input", function () { sel = 0; var id = ++SW.omniSeq; req[id] = 1; SW.omniLast = null; post("omni.input", { text: input.value, id: id, scope: "palette", limit: 8 }); build(); });
    input.addEventListener("keydown", function (e) {
      if (e.key === "ArrowDown") { e.preventDefault(); sel = (sel + 1) % rows.length; mark(); } else if (e.key === "ArrowUp") { e.preventDefault(); sel = (sel - 1 + rows.length) % rows.length; mark(); }
      else if (e.key === "Enter") { e.preventDefault(); var q = input.value.trim(); if (e.ctrlKey || e.metaKey) { if (q) { close(); SW.sidekick.ask(q, "palette"); } } else if (rows[sel]) exec(rows[sel]); else if (q) { close(); SW.sidekick.ask(q, "palette"); } }
      else if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); close(); }
    });
    build(); input.focus();
  }
  SW.on("omni.suggestions", function (p) { if (el && p && req[p.id]) { remote = p.items || []; build(); } });
  SW.palette = { open: open, close: close };
})();
