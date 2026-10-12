/* JS-driven scrolling. The Sweef engine paints overflow containers at offset 0 and has no native
   element scrolling, so scroll areas are moved with transform and driven by wheel/keyboard/thumb.
   box: clipping element (overflow hidden). inner: its single content child. */
(function () {
  var SW = window.SW = window.SW || {};
  SW.scroller = function (box, inner, o) {
    o = o || {}; var horiz = !!o.horizontal, pos = 0, S = { y: 0, onscroll: null }, thumb = document.createElement("div"), tt = 0;
    box.style.overflow = "hidden"; if (!/relative|absolute|fixed/.test(box.style.position || "")) box.style.position = "relative";
    inner.style.willChange = "transform"; thumb.className = "sbar" + (horiz ? " h" : ""); thumb.style.display = "none"; if (!o.noThumb) box.appendChild(thumb);
    function size() { return horiz ? [inner.offsetWidth, box.clientWidth] : [inner.offsetHeight, box.clientHeight]; }
    function max() { var s = size(); return Math.max(0, s[0] - s[1]); }
    function apply() {
      inner.style.transform = pos ? (horiz ? "translateX(" : "translateY(") + (-pos) + "px)" : "";
      var s = size(), m = Math.max(0, s[0] - s[1]);
      if (m <= 0) { thumb.style.display = "none"; } else {
        var len = Math.max(28, Math.round(s[1] * s[1] / s[0])), at = Math.round((s[1] - len) * (pos / m));
        thumb.style.display = ""; if (horiz) { thumb.style.width = len + "px"; thumb.style.left = at + "px"; } else { thumb.style.height = len + "px"; thumb.style.top = at + "px"; }
        thumb.classList.add("on"); clearTimeout(tt); tt = setTimeout(function () { thumb.classList.remove("on"); }, 900);
      }
      S.y = pos; if (S.onscroll) S.onscroll(pos);
    }
    S.max = max;
    S.set = function (v) { v = Math.max(0, Math.min(max(), v)); if (v !== pos || true) { pos = v; apply(); } };
    S.by = function (d) { S.set(pos + d); };
    S.refresh = function () { if (pos > max()) pos = max(); apply(); };
    S.toBottom = function () { S.set(max()); };
    S.atBottom = function () { return max() - pos < 48; };
    S.reveal = function (el) {
      var a = el.getBoundingClientRect(), b = box.getBoundingClientRect(), pad = 8;
      if (horiz) { if (a.left < b.left + pad) S.by(a.left - b.left - pad); else if (a.right > b.right - pad) S.by(a.right - b.right + pad); }
      else { if (a.top < b.top + pad) S.by(a.top - b.top - pad); else if (a.bottom > b.bottom - pad) S.by(a.bottom - b.bottom + pad); }
    };
    box.addEventListener("wheel", function (e) {
      var d = horiz ? (Math.abs(e.deltaX) > Math.abs(e.deltaY) ? e.deltaX : e.deltaY) : e.deltaY; if (e.deltaMode === 1) d *= 16;
      var m = max(); if (m <= 0 || (d < 0 && pos <= 0) || (d > 0 && pos >= m)) return; e.preventDefault(); S.by(d);
    }, { passive: false });
    box.addEventListener("keydown", function (e) {
      if (horiz || /INPUT|TEXTAREA|SELECT/.test((e.target.tagName || ""))) return; var h = box.clientHeight, k = e.key, d = 0;
      if (k === "PageDown" || (k === " " && !e.shiftKey)) d = h * 0.9; else if (k === "PageUp" || (k === " " && e.shiftKey)) d = -h * 0.9; else if (k === "ArrowDown") d = 48; else if (k === "ArrowUp") d = -48;
      else if (k === "Home") { e.preventDefault(); return S.set(0); } else if (k === "End") { e.preventDefault(); return S.toBottom(); } else return;
      if (e.target.closest && e.target.closest("button,a,[role=tab]") && (k === " ")) return; e.preventDefault(); S.by(d);
    });
    var drag = null;
    thumb.addEventListener("mousedown", function (e) { e.preventDefault(); e.stopPropagation(); drag = { p: horiz ? e.clientX : e.clientY, at: pos }; window.addEventListener("mousemove", mv); window.addEventListener("mouseup", up); });
    function mv(e) { if (!drag) return; var s = size(), len = Math.max(28, s[1] * s[1] / s[0]), per = (s[0] - s[1]) / Math.max(1, s[1] - len); S.set(drag.at + ((horiz ? e.clientX : e.clientY) - drag.p) * per); }
    function up() { drag = null; window.removeEventListener("mousemove", mv); window.removeEventListener("mouseup", up); }
    return S;
  };
})();
