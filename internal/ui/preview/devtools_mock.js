/* Preview/test harness only (NOT embedded in ui.FS): answers devtools.req the way internal/app/devtools.go would,
   from a fixed page ("Cart" on shop.example.com), so the panel code is exercised without an app behind it. */
(function () {
  var base = window.sweef.post;
  var NOW = 1760000000000;
  var png = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAgAAAAICAYAAADED76LAAAAIklEQVQYV2P8z8Dwn4EIwDiqEF8gGRgY/jMQAUZVoQEAAHq4BAHa+x8bAAAAAElFTkSuQmCC";
  function ent(id, o) {
    var e = { id: id, nav: 2, method: "GET", url: "", type: "other", status: 200, start: NOW, dur: 10, transfer: 0, size: 0, mime: "", reqHeaders: { "Accept": "*/*", "User-Agent": "Sweef/0.1", "Accept-Language": "en-US,en;q=0.9" }, respHeaders: { "Content-Type": "text/plain" } };
    for (var k in o) e[k] = o[k];
    return e;
  }
  var H = "https://shop.example.com";
  var net = [
    ent(1, { nav: 1, url: H + "/old", type: "document", start: NOW - 9000, dur: 80, size: 1200, transfer: 900, mime: "text/html" }),
    ent(2, { url: H + "/cart", type: "document", start: NOW, dur: 120, size: 20480, transfer: 6100, mime: "text/html", respHeaders: { "Content-Type": "text/html; charset=utf-8", "Cache-Control": "no-cache", "Server": "nginx" }, hasBody: true }),
    ent(3, { url: H + "/assets/app.min.css", type: "style", start: NOW + 130, dur: 40, size: 18200, transfer: 4200, mime: "text/css", cache: "revalidated", hasBody: true, respHeaders: { "Content-Type": "text/css", "Cache-Control": "max-age=3600" } }),
    ent(4, { url: H + "/assets/app.min.js", type: "script", start: NOW + 131, dur: 90, size: 64000, transfer: 20100, mime: "application/javascript", cache: "miss: stored lifetime=3600s", hasBody: true }),
    ent(5, { url: H + "/img/hero.png", type: "image", start: NOW + 200, dur: 25, size: 148, transfer: 0, mime: "image/png", cache: "hit", hasBody: true }),
    ent(6, { url: H + "/fonts/inter.woff2", type: "font", start: NOW + 230, dur: 60, size: 98000, transfer: 98000, mime: "font/woff2" }),
    ent(7, { url: H + "/api/cart?currency=usd", type: "xhr", start: NOW + 260, dur: 140, size: 52, transfer: 310, mime: "application/json", hasBody: true, respHeaders: { "Content-Type": "application/json" } }),
    ent(8, { url: "https://tracker.adnet.example/pixel.gif?id=77", type: "image", status: 0, start: NOW + 265, dur: 0, blocked: true, reason: "blocked by the tracker blocker (tracker.adnet.example)", error: "net::ERR_FAILED (aborted by route)" }),
    ent(9, { url: H + "/api/recommendations", type: "xhr", status: 404, start: NOW + 270, dur: 70, size: 30, transfer: 200, mime: "text/plain", hasBody: true }),
    ent(10, { url: H + "/api/slow", type: "xhr", status: 0, start: NOW + 300, dur: -1, pending: true })
  ];
  var bodies = {
    2: { kind: "text", mime: "text/html", size: 20480, text: "<!doctype html>\n<html>\n<head><title>Cart</title></head>\n<body><h1>Your cart</h1></body>\n</html>" },
    5: { kind: "image", mime: "image/png", size: 148, data: png },
    7: { kind: "json", mime: "application/json", size: 52, text: '{"items":[{"sku":"A1","qty":2}],"total":39.98}' },
    9: { kind: "text", mime: "text/plain", size: 30, text: "Not found: /api/recommendations" }
  };
  var cons = [
    { sid: 1, level: "log", text: "app booted in 41ms", at: NOW, loc: H + "/assets/app.min.js:1:5" },
    { sid: 2, level: "log", text: "app booted in 41ms", at: NOW + 1, loc: H + "/assets/app.min.js:1:5" },
    { sid: 3, level: "warning", text: "Deprecated API: use cart.items instead of cart.list", at: NOW + 2, loc: H + "/assets/app.min.js:1:5" },
    { sid: 4, level: "info", text: "analytics disabled", at: NOW + 3 },
    { sid: 5, level: "error", text: "TypeError: cannot read properties of undefined (reading 'price')\n    at render (<eval>:12:21)\n    at <eval> (<eval>:30:3)\n    in " + H + "/assets/app.min.js", at: NOW + 4, loc: H + "/assets/app.min.js:12:21" },
    { sid: 6, level: "error", text: "Failed to load resource: the server responded with a status of 404 (/api/recommendations)", at: NOW + 5 }
  ];
  function el(id, name, attrs, kids, extra) { var n = { id: id, type: 1, name: name, attrs: attrs || [], kids: kids || 0 }; for (var k in (extra || {})) n[k] = extra[k]; return n; }
  function tx(id, v) { return { id: id, type: 3, name: "#text", value: v, kids: 0 }; }
  var tree = {
    2: [{ id: 3, type: 10, name: "#doctype", value: "html", kids: 0 }, el(4, "html", [["lang", "en"]], 2)],
    4: [el(5, "head", [], 3), el(6, "body", [["class", "cart"]], 3)],
    5: [el(7, "meta", [["charset", "utf-8"]]), el(8, "title", [], 1), el(9, "link", [["rel", "stylesheet"], ["href", "/assets/app.min.css"]])],
    8: [tx(10, "Cart")],
    6: [el(11, "div", [["id", "cart"], ["class", "card big"], ["style", "margin-top: 7px; color: red"], ["data-count", "2"]], 3), el(12, "p", [["class", "note"]], 1), el(13, "script", [["src", "/assets/app.min.js"]])],
    11: [el(14, "h1", [], 1), el(15, "button", [["class", "buy"], ["disabled", ""]], 1), { id: 16, type: 8, name: "#comment", value: " promo slot ", kids: 0 }],
    14: [tx(17, "Your cart")], 15: [tx(18, "Buy now")], 12: [tx(19, "Free shipping over $35.")]
  };
  function kids(id) { return (tree[id] || []).map(function (n) { var c = JSON.parse(JSON.stringify(n)); return c; }); }
  function withKids(n, depth) {
    var c = JSON.parse(JSON.stringify(n)), k = tree[n.id];
    if (k && (depth > 0 || (k.length === 1 && k[0].type === 3))) c.children = k.map(function (x) { return withKids(x, depth - 1); }); /* a lone text child always comes along: it shows inline */
    return c;
  }
  var doc = { id: 2, type: 9, name: "#document", kids: 2 };
  var style = {
    rules: [
      { selector: "element.style", source: "element.style", inline: true, decls: [{ name: "margin-top", value: "7px" }, { name: "color", value: "red", overridden: true }] },
      { selector: ".big", source: H + "/assets/app.min.css", line: 14, decls: [{ name: "color", value: "purple", important: true }, { name: "font-size", value: "20px" }], at: ["@media (min-width: 100px)"] },
      { selector: ".card", source: H + "/assets/app.min.css", line: 3, decls: [{ name: "padding", value: "4px" }, { name: "border", value: "1px solid #000" }, { name: "color", value: "blue", overridden: true }] },
      { selector: "#cart", source: "<style> 1", line: 2, decls: [{ name: "background", value: "#fff7e0" }] }
    ],
    computed: [["background-color", "rgb(255, 247, 224)"], ["color", "rgb(128, 0, 128)"], ["display", "block"], ["font-size", "20px"], ["margin-top", "7px"], ["padding-top", "4px"], ["width", "1202px"]],
    box: { margin: [7, 0, 0, 0], border: [1, 1, 1, 1], padding: [4, 4, 4, 4], w: 1190, h: 88, x: 0, y: 7, bw: 1200, bh: 98 }
  };
  var minJS = 'var cart={items:[],total:0};function render(c){var h="";for(var i=0;i<c.items.length;i++){h+="<li>"+c.items[i].name+"</li>"}document.body.innerHTML=h}cart.items.push({name:"A1",price:19.99});if(cart.items.length>0){render(cart)}else{console.log("empty")}';
  var srcs = [
    { key: "net:2", url: H + "/cart", kind: "document", size: 20480, status: 200 },
    { key: "net:4", url: H + "/assets/app.min.js", kind: "script", size: 64000, status: 200 },
    { key: "net:3", url: H + "/assets/app.min.css", kind: "style", size: 18200, status: 200 },
    { key: "inline:s1", url: "(inline script 1)", kind: "script", size: 90, inline: true },
    { key: "inline:c1", url: "(inline style 1)", kind: "style", size: 40, inline: true }
  ];
  var srcText = {
    "net:2": "<!doctype html>\n<html lang=\"en\">\n<head><title>Cart</title>\n<link rel=\"stylesheet\" href=\"/assets/app.min.css\"></head>\n<body class=\"cart\"><div id=\"cart\"></div>\n<script src=\"/assets/app.min.js\"></script></body>\n</html>\n",
    "net:4": minJS, "net:3": ".card{padding:4px;border:1px solid #000}.big{color:purple!important;font-size:20px}@media (min-width:100px){.x{margin:0}}",
    "inline:s1": "console.log('inline');", "inline:c1": "#cart { background: #fff7e0 }"
  };
  function answer(m, p) {
    switch (m) {
      case "info": return { url: H + "/cart", title: "Cart", gen: window.__dtGen || 1, nav: 2, readOnly: false, loading: false };
      case "console.get": return cons.filter(function (c) { return c.sid > (p.since || 0); });
      case "console.eval": return /throw|nope/.test(p.expr) ? { ok: false, type: "error", text: "ReferenceError: nope is not defined" } : { ok: true, type: "number", text: p.expr === "1 + 2" ? "3" : "42" };
      case "net.get": return { entries: net.filter(function (e) { return e.id > (p.since || 0) || (p.pending || []).indexOf(e.id) >= 0; }), now: NOW + 500 };
      case "net.clear": return true;
      case "net.body": return bodies[p.id] || { kind: "none", size: 0 };
      case "dom.root": var r = JSON.parse(JSON.stringify(doc)); r.children = tree[2].map(function (k) { return withKids(k, 3); }); return r;
      case "dom.children": return (tree[p.id] || []).map(function (k) { return withKids(k, p.depth || 0); });
      case "dom.node": var f = null; Object.keys(tree).forEach(function (k) { tree[k].forEach(function (n) { if (n.id === p.id) f = n; }); }); return f ? withKids(f, 0) : doc;
      case "dom.style": return style;
      case "dom.highlight": window.__dtHL = p.id; return true;
      case "dom.setAttr": case "dom.removeAttr": case "dom.setText": case "dom.remove": window.__dtGen = (window.__dtGen || 1) + 1; return true;
      case "src.list": return srcs;
      case "src.get": return { text: srcText[p.key] || "" };
      case "app.get": return { origin: H + "/cart", cookies: [{ name: "session", value: "abc123", domain: "shop.example.com", path: "/", expires: 0, httpOnly: true, secure: true, sameSite: "Lax" }, { name: "currency", value: "usd", domain: "shop.example.com", path: "/", expires: 1790000000, httpOnly: false, secure: false, sameSite: "" }], local: [["theme", "dark"], ["cart.v2", '{"items":2}']], session: [["tabId", "t-91"]] };
    }
    throw new Error("unknown devtools method " + m);
  }
  window.sweef.post = function (t, p) {
    base(t, p);
    if (t === "devtools.req") {
      var res;
      try { res = { id: p.id, result: answer(p.m, p.p || {}) }; } catch (e) { res = { id: p.id, error: String(e.message || e) }; }
      setTimeout(function () { window.__send("devtools.res", res); }, 0);
    }
  };
})();
