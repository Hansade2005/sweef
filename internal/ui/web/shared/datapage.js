/* History and Bookmarks pages: a searchable, day-grouped, virtualised list fed by data.query / data.result. */
(function () {
  var SW = window.SW, S = window.S, h = SW.h, icon = SW.icon, post = SW.post, $ = function (id) { return document.getElementById(id); };
  SW.dataPage = function (c) {
    var items = [], total = 0, q = "", reqId = 0, box = $("list"), V = null, loading = false;
    $("ttl").appendChild(icon(c.icon, 22)); $("ttl").appendChild(document.createTextNode(c.title)); $("sq").setAttribute("placeholder", c.search); $("sq").setAttribute("aria-label", c.search); $("sic").appendChild(icon("search", 16));
    if (c.clear) { var cb = h("button.btn.sm", { onclick: function () { post(c.clear); } }, icon("trash", 14), c.clearLabel); $("acts").appendChild(cb); }
    function day(ts) {
      var d = new Date(ts), n = new Date(), a = new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime(), b = new Date(n.getFullYear(), n.getMonth(), n.getDate()).getTime(), diff = Math.round((b - a) / 864e5);
      return diff <= 0 ? S.page.today : diff === 1 ? S.page.yesterday : d.toLocaleDateString(undefined, { weekday: "long", month: "long", day: "numeric" });
    }
    function hm(ts) { var d = new Date(ts), H = d.getHours(), m = d.getMinutes(); return (H % 12 || 12) + ":" + (m < 10 ? "0" : "") + m + " " + (H < 12 ? "AM" : "PM"); }
    function build() {
      var rows = [], lastDay = "";
      items.forEach(function (it) {
        if (c.group && it.ts) { var dd = day(it.ts); if (dd !== lastDay) { lastDay = dd; rows.push({ h: 40, head: dd }); } }
        rows.push({ h: 46, it: it });
      });
      var empty = $("empty"); empty.classList.toggle("hide", rows.length > 0); box.classList.toggle("hide", rows.length === 0);
      if (!rows.length) { SW.clear(empty); empty.appendChild(h("div.big", icon(c.icon, 28))); empty.appendChild(h("div", { text: q ? S.f(S.page.noResults, { q: q }) : c.empty })); }
      V.set(rows);
    }
    function renderRow(r) {
      if (r.head) return h("div.dayh", { text: r.head });
      var it = r.it, x = h("button.ibtn.sm.x", { "aria-label": S.page.remove, title: S.page.remove, onclick: function (e) { e.stopPropagation(); post("data.remove", { kind: c.kind, id: it.id }); items = items.filter(function (z) { return z !== it; }); build(); } }, icon("close", 14));
      var rm = function () { post("data.remove", { kind: c.kind, id: it.id }); items = items.filter(function (z) { return z !== it; }); build(); };
      return h("div.lrow", { role: "link", tabindex: "0", oncontextmenu: function (e) {
        SW.ctxMenu(e, [{ label: S.ctx.open, on: function () { post("nav.go", { tab: "", input: it.url }); } }, { label: S.ctx.openNew, on: function () { post("tab.new", { url: it.url }); } },
          { label: S.ctx.copyLink, on: function () { post("clipboard.write", { text: it.url }); } }, { sep: true },
          { label: c.kind === "history" ? S.ctx.removeHist : S.ctx.del2, danger: true, on: rm }]);
      }, onclick: function () { post("nav.go", { tab: "", input: it.url }); }, onmousedown: function (e) { if (e.button === 1) { e.preventDefault(); post("tab.new", { url: it.url }); } }, onkeydown: function (e) { if (e.key === "Enter") post("nav.go", { tab: "", input: it.url }); } },
        c.group ? h("span.tm", { text: it.ts ? hm(it.ts) : "" }) : null, SW.favicon(it.url, it.favicon, it.title), h("div.tt.trunc", h("b", { text: it.title || SW.host(it.url) }), h("span", { text: SW.host(it.url) })), x);
    }
    V = SW.vlist(box, renderRow);
    function query(more) { reqId++; post("data.query", { kind: c.kind, q: q, offset: more ? items.length : 0, limit: 300, id: reqId }); }
    SW.on("data.result", function (p) {
      if (!p || p.kind !== c.kind) return; if (p.id && p.id < reqId) return;
      items = (p.offset ? items : []).concat(p.items || []); total = p.total != null ? p.total : items.length; build();
    });
    $("sq").addEventListener("input", SW.debounce(function (e) { q = e.target.value.trim(); query(false); }, 120));
    V.onscroll = function () { if (items.length < total && V.sc.max() - V.sc.y < 600) query(true); };
    query(false); SW.ready(c.kind);
  };
})();
