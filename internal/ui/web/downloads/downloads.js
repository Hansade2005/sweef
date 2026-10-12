/* sweef://downloads: live from state.downloads / downloads.update. */
(function () {
  var SW = window.SW, S = window.S, h = SW.h, icon = SW.icon, post = SW.post, $ = function (id) { return document.getElementById(id); }, q = "";
  $("ttl").appendChild(icon("download", 22)); $("ttl").appendChild(document.createTextNode(S.page.downloads)); $("sic").appendChild(icon("search", 16)); $("sq").setAttribute("placeholder", S.page.searchDownloads); $("sq").setAttribute("aria-label", S.page.searchDownloads);
  $("acts").appendChild(h("button.btn.sm", { onclick: function () { post("downloads.clear"); } }, icon("trash", 14), S.dl.clear));
  var sc = SW.scroller($("main"), $("col"));
  var btn = function (t, f) { return h("button.btn.sm", { text: t, onclick: f }); };
  function render() {
    var col = $("col"), list = (SW.state.downloads || []).filter(function (d) { return !q || (d.name + " " + (d.url || "")).toLowerCase().indexOf(q) >= 0; }); SW.clear(col);
    if (!list.length) { col.appendChild(h("div.empty2", h("div.big", icon("download", 28)), h("div", { text: q ? S.f(S.page.noResults, { q: q }) : S.dl.empty }))); sc.refresh(); return; }
    var g = h("div.group");
    list.forEach(function (d) {
      var prog = d.state === "progress" || d.state === "inprogress", pct = d.total ? Math.min(1, d.received / d.total) : 0;
      var sub = prog ? SW.fmtBytes(d.received) + (d.total ? " " + S.dl.of + " " + SW.fmtBytes(d.total) : "") + (d.speed ? " · " + SW.fmtBytes(d.speed) + "/s" : "") : d.state === "failed" ? S.dl.failed : d.state === "paused" ? S.dl.paused : SW.fmtBytes(d.total || d.received);
      var acts = prog ? [btn(S.dl.cancel, function () { post("downloads.cancel", { id: d.id }); })] : d.state === "failed" ? [btn(S.dl.retry, function () { post("downloads.retry", { id: d.id }); })] : [btn(S.dl.open, function () { post("downloads.open", { id: d.id }); }), btn(S.dl.openFolder, function () { post("downloads.show", { id: d.id }); })];
      g.appendChild(h("div.dl2", h("span.fi", icon("download", 20)), h("div.tx", h("div.nm.trunc", { text: d.name }), h("div.sb", { text: (d.url ? SW.host(d.url) + " · " : "") + sub }), prog ? h("div.bar", h("i", { style: { width: Math.round(pct * 100) + "%" } })) : null), acts, h("button.ibtn.sm", { "aria-label": S.page.remove, onclick: function () { post("downloads.remove", { id: d.id }); } }, icon("close", 14))));
    });
    col.appendChild(g); sc.refresh();
  }
  $("sq").addEventListener("input", function (e) { q = e.target.value.trim().toLowerCase(); render(); });
  SW.on("render", render); render(); SW.ready("downloads");
})();
