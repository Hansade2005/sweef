/* Data Saver helpers shared by the popover, Settings, the new tab and the hard-stop page. Loaded on demand. */
(function () {
  var SW = window.SW, S = window.S, h = SW.h, T = S.data;
  var ds = SW.ds = {};
  ds.d = function () { return SW.state.data || {}; };
  ds.limitText = function (mb) { return mb >= 1024 && mb % 1024 === 0 ? (mb / 1024) + " GB" : mb + " MB"; };
  ds.period = function (p) { return T.period[p] || T.period.month; };
  ds.resets = function (b) {
    var n = b.resetsInDays || 1; return n <= 1 ? T.resetsTomorrow : S.f(T.resetsIn, { n: n });
  };
  ds.when = function (b) { var n = b.resetsInDays || 1; return n <= 1 ? T.nt.tomorrow : S.f(T.nt.inDays, { n: n }); };
  /* "Measured" and "Estimate" pills: two looks, never mixed */
  ds.pill = function (kind, tip) {
    var m = kind === "m"; return h("span.dpill." + kind, { text: m ? T.measured : T.estimate, title: tip || (m ? T.savedMTip : T.estTip) });
  };
  ds.host = function (o) { return SW.host(o) || o; };
  ds.siteRow = function (o, bytes, max) {
    return h("div.tsite", SW.favicon(o, "", ds.host(o)), h("span.tn.trunc", { text: ds.host(o) }), h("span.tb.num", { text: SW.fmtBytes(bytes) }), h("span.tbar", h("i", { style: { width: Math.max(4, Math.round(100 * bytes / (max || bytes || 1))) + "%" } })));
  };
  /* a calm summary line for the budget, used by the popover and the new tab */
  ds.leftText = function (b) { return SW.fmtBytes(Math.max(0, (b.limitMB || 0) * 1048576 - (b.usedBytes || 0))); };
})();
