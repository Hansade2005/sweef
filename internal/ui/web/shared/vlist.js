/* Virtual list: only the rows in (and just around) the viewport exist in the DOM.
   rows: [{h: pixelHeight, ...}]. render(row, index) -> Node. */
(function () {
  var SW = window.SW = window.SW || {};
  SW.vlist = function (box, render) {
    var rows = [], off = [0], inner = document.createElement("div"), lo = -1, hi = -1, H = 0, V, sc;
    inner.style.cssText = "position:relative;width:100%"; box.appendChild(inner); sc = SW.scroller(box, inner);
    function find(y) { var a = 0, b = rows.length - 1; while (a < b) { var m = (a + b + 1) >> 1; if (off[m] <= y) a = m; else b = m - 1; } return a; }
    function paint(force) {
      H = box.clientHeight || 600; if (!rows.length) { SW.clear(inner); inner.style.height = "0"; lo = hi = -1; return; }
      var top = sc.y, a = Math.max(0, find(Math.max(0, top - 200))), b = Math.min(rows.length - 1, find(top + H + 200));
      if (!force && a === lo && b === hi) return; lo = a; hi = b; SW.clear(inner);
      for (var i = a; i <= b; i++) { var n = render(rows[i], i); n.style.position = "absolute"; n.style.left = "0"; n.style.right = "0"; n.style.top = off[i] + "px"; n.style.height = rows[i].h + "px"; inner.appendChild(n); }
    }
    sc.onscroll = function (y) { paint(false); if (V.onscroll) V.onscroll(y); };
    V = {
      set: function (r) { rows = r; off = [0]; for (var i = 0; i < r.length; i++) off.push(off[i] + r[i].h); inner.style.height = off[r.length] + "px"; sc.refresh(); paint(true); },
      refresh: function () { sc.refresh(); paint(true); }, count: function () { return rows.length; }, sc: sc, onscroll: null
    };
    return V;
  };
})();
