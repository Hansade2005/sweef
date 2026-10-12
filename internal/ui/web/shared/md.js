/* Small safe markdown renderer. Builds DOM nodes; model text is NEVER passed to innerHTML.
   Supports paragraphs, headings, bold, italic, strike, inline and fenced code, lists (nested one level),
   blockquotes, tables, rules and links (http, https, mailto only). Tolerates half-written (streaming) input. */
(function () {
  var SW = window.SW = window.SW || {};
  var h = function (t, a) { var e = document.createElement(t); if (a) for (var k in a) e.setAttribute(k, a[k]); return e; };
  var INLS = /(`+)([^`]|[^`][\s\S]*?[^`])\1(?!`)|\*\*([^*\n]+?)\*\*|__([^_\n]+?)__|\*([^*\s][^*\n]*?)\*|(?:^|\b)_([^_\s][^_\n]*?)_(?=\b|$)|~~([^~\n]+?)~~|\[([^\]\n]+)\]\(([^)\s]+)\)|(https?:\/\/[^\s<)]+[^\s<).,;:!?])/;
  function safeHref(u) { return /^(https?:\/\/|mailto:)/i.test(u) ? u : ""; }
  function inline(s, parent) {
    var last = 0, m, INL = new RegExp(INLS.source, "g");
    while ((m = INL.exec(s))) {
      if (m.index > last) parent.appendChild(document.createTextNode(s.slice(last, m.index)));
      last = INL.lastIndex;
      var el;
      if (m[2] != null) { el = h("code"); el.textContent = m[2].trim(); }
      else if (m[3] != null || m[4] != null) { el = h("strong"); inline(m[3] != null ? m[3] : m[4], el); }
      else if (m[5] != null || m[6] != null) { el = h("em"); inline(m[5] != null ? m[5] : m[6], el); }
      else if (m[7] != null) { el = h("del"); inline(m[7], el); }
      else if (m[8] != null) {
        var href = safeHref(m[9]);
        if (href) { el = h("a", { href: href, "data-href": href, rel: "noopener noreferrer" }); inline(m[8], el); }
        else el = document.createTextNode(m[8] + " (" + m[9] + ")");
      } else { el = h("a", { href: m[10], "data-href": m[10], rel: "noopener noreferrer" }); el.textContent = m[10]; }
      parent.appendChild(el);
    }
    if (last < s.length) parent.appendChild(document.createTextNode(s.slice(last)));
  }
  var rLi = /^(\s*)([-*+]|\d+[.)])\s+(.*)$/;
  function cells(l) { return l.replace(/^\s*\|/, "").replace(/\|\s*$/, "").split("|").map(function (c) { return c.trim(); }); }
  function render(text) {
    var out = document.createDocumentFragment(), lines = String(text || "").replace(/\r/g, "").split("\n"), i = 0, m;
    while (i < lines.length) {
      var l = lines[i];
      if (/^\s*$/.test(l)) { i++; continue; }
      if ((m = /^\s*(```+|~~~+)\s*([\w+-]*)/.exec(l))) {
        var code = []; i++;
        while (i < lines.length && !/^\s*(```+|~~~+)\s*$/.test(lines[i])) code.push(lines[i++]);
        i++;
        var pre = h("pre"), c = h("code"); if (m[2]) pre.setAttribute("data-lang", m[2]);
        c.textContent = code.join("\n"); pre.appendChild(c); out.appendChild(pre); continue;
      }
      if ((m = /^(#{1,4})\s+(.*)$/.exec(l))) { var hd = h("h" + Math.min(6, m[1].length + 2)); inline(m[2].replace(/\s#+\s*$/, ""), hd); out.appendChild(hd); i++; continue; }
      if (/^\s*([-*_])(\s*\1){2,}\s*$/.test(l)) { out.appendChild(h("hr")); i++; continue; }
      if (/^\s*>/.test(l)) {
        var q = []; while (i < lines.length && /^\s*>/.test(lines[i])) q.push(lines[i++].replace(/^\s*>\s?/, ""));
        var bq = h("blockquote"); bq.appendChild(render(q.join("\n"))); out.appendChild(bq); continue;
      }
      if (/^\s*\|.*\|\s*$/.test(l) && i + 1 < lines.length && /^\s*\|?\s*:?-{2,}:?\s*(\|\s*:?-{2,}:?\s*)*\|?\s*$/.test(lines[i + 1])) {
        var head = cells(l), tb = h("table"), th = h("thead"), tr = h("tr");
        head.forEach(function (c) { var e = h("th"); inline(c, e); tr.appendChild(e); }); th.appendChild(tr); tb.appendChild(th);
        var body = h("tbody"); i += 2;
        while (i < lines.length && /^\s*\|/.test(lines[i])) { var r = h("tr"); cells(lines[i++]).forEach(function (c) { var e = h("td"); inline(c, e); r.appendChild(e); }); body.appendChild(r); }
        tb.appendChild(body); var wrap = h("div", { "class": "tbl" }); wrap.appendChild(tb); out.appendChild(wrap); continue;
      }
      if ((m = rLi.exec(l))) {
        var ordered = /\d/.test(m[2]), root = h(ordered ? "ol" : "ul"), cur = root, stack = [], base = m[1].length, li = null;
        while (i < lines.length && (m = rLi.exec(lines[i]))) {
          var ind = m[1].length;
          if (ind >= base + 2 && li) { var sub = li.lastChild && /^(ul|ol)$/i.test(li.lastChild.tagName || "") ? li.lastChild : null; if (!sub) { sub = h(/\d/.test(m[2]) ? "ol" : "ul"); li.appendChild(sub); } cur = sub; }
          else cur = root;
          li = h("li"); inline(m[3], li); cur.appendChild(li); i++;
        }
        out.appendChild(root); continue;
      }
      var para = [l]; i++;
      while (i < lines.length && !/^\s*$/.test(lines[i]) && !/^\s*(```|~~~|#{1,4}\s|>|[-*+]\s|\d+[.)]\s|\|)/.test(lines[i])) para.push(lines[i++]);
      var p = h("p");
      para.forEach(function (t, k) { if (k) p.appendChild(h("br")); inline(t.replace(/\s+$/, ""), p); });
      out.appendChild(p);
    }
    return out;
  }
  SW.md = { render: render };
})();
