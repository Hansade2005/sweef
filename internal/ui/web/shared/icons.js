/* Icons (24 grid, stroke style) and the Sweef logo. Inline, no network. */
(function () {
  var P = function (d) { return '<path d="' + d + '"/>'; };
  var I = {
    back: P("M15 5l-7 7 7 7"), forward: P("M9 5l7 7-7 7"),
    reload: P("M19.5 12a7.5 7.5 0 1 1-2.4-5.5M19.5 4.5v4.5H15"), close: P("M6 6l12 12M18 6L6 18"), plus: P("M12 5v14M5 12h14"), minus: P("M5 12h14"),
    sidebar: '<rect x="3.5" y="4.5" width="17" height="15" rx="3.5"/>' + P("M14.5 4.5v15"),
    star: P("M12 3.8l2.5 5.2 5.7.8-4.1 4 1 5.7-5.1-2.7-5.1 2.7 1-5.7-4.1-4 5.7-.8z"),
    starfill: '<path fill="currentColor" d="M12 3.8l2.5 5.2 5.7.8-4.1 4 1 5.7-5.1-2.7-5.1 2.7 1-5.7-4.1-4 5.7-.8z"/>',
    lock: '<rect x="5.5" y="10.5" width="13" height="9.5" rx="2.5"/>' + P("M8.5 10.5V8a3.5 3.5 0 0 1 7 0v2.5"),
    unlock: '<rect x="5.5" y="10.5" width="13" height="9.5" rx="2.5"/>' + P("M8.5 10.5V8a3.5 3.5 0 0 1 6.6-1.6"),
    warn: P("M12 4.2l8.6 15H3.4z") + P("M12 10v4.2M12 17v.1"),
    dots: '<circle cx="5.5" cy="12" r="1.3" fill="currentColor"/><circle cx="12" cy="12" r="1.3" fill="currentColor"/><circle cx="18.5" cy="12" r="1.3" fill="currentColor"/>',
    menu: P("M4.5 7h15M4.5 12h15M4.5 17h15"),
    download: P("M12 4.5v10M7.5 10.5l4.5 4.5 4.5-4.5M5 19.5h14"),
    search: '<circle cx="11" cy="11" r="6.2"/>' + P("M20 20l-4.4-4.4"),
    bell: P("M6.500 16.500V11a5.500 5.500 0 0 1 11 0v5.500l1.500 1.500h-14z") + P("M10 20.500a2.200 2.200 0 0 0 4 0"),
    belloff: P("M6.500 16.500V11a5.500 5.500 0 0 1 11 0v5.500l1.500 1.500h-14z") + P("M10 20.500a2.200 2.200 0 0 0 4 0M4 4l16 16"),
    eye: P("M2.8 12S6 5.8 12 5.8 21.2 12 21.2 12 18 18.2 12 18.2 2.8 12 2.8 12z") + '<circle cx="12" cy="12" r="2.8"/>',
    eyeoff: P("M4 4l16 16M9.9 6a8.700 8.700 0 0 1 2.100-.2c6 0 9.200 6.200 9.200 6.200a15 15 0 0 1-2.900 3.700M6.500 7.800A15 15 0 0 0 2.800 12S6 18.200 12 18.200a8.600 8.600 0 0 0 3.500-.7M10 10.100a2.800 2.800 0 0 0 3.900 3.900"),
    spark: P("M12 3.500l1.900 5.100 5.100 1.900-5.100 1.900L12 17.500l-1.900-5.100L5 10.500l5.100-1.900z") + P("M18.500 16.500l.7 1.800 1.800.7-1.800.7-.7 1.800-.7-1.800-1.800-.7 1.800-.7z"),
    send: P("M12 19V5.500M6.500 11L12 5.500 17.500 11"), stop: '<rect x="7" y="7" width="10" height="10" rx="2.200" fill="currentColor" stroke="none"/>',
    play: '<path fill="currentColor" stroke="none" d="M8 5.500v13l10.500-6.500z"/>', pause: '<rect x="6.500" y="5.500" width="4" height="13" rx="1" fill="currentColor" stroke="none"/><rect x="13.500" y="5.500" width="4" height="13" rx="1" fill="currentColor" stroke="none"/>',
    skipprev: '<path fill="currentColor" stroke="none" d="M17.500 5.500v13L8.500 12z"/>' + P("M6.500 5.500v13"), skipnext: '<path fill="currentColor" stroke="none" d="M6.500 5.500v13l9-6.500z"/>' + P("M17.500 5.500v13"),
    music: P("M9 17.500V6.500l10-2v11") + '<circle cx="6.500" cy="17.500" r="2.500"/><circle cx="16.500" cy="15.500" r="2.500"/>',
    mic: '<rect x="9" y="3.500" width="6" height="11" rx="3"/>' + P("M5.500 11.500a6.500 6.500 0 0 0 13 0M12 18v2.500"),
    clip: P("M19.500 11.200l-7.700 7.700a4.600 4.600 0 0 1-6.500-6.500l8-8a3.100 3.100 0 0 1 4.400 4.400l-8 8a1.500 1.500 0 0 1-2.200-2.200l7.300-7.300"),
    globe: '<circle cx="12" cy="12" r="8.500"/>' + P("M3.500 12h17M12 3.500c2.500 2.600 3.600 5.400 3.600 8.500S14.500 17.900 12 20.500C9.500 17.900 8.400 15 8.400 12S9.500 6.100 12 3.500z"),
    clock: '<circle cx="12" cy="12" r="8.500"/>' + P("M12 7.500V12l3 2"),
    chev: P("M6.500 9.500l5.500 5.500 5.500-5.500"), chevr: P("M9.500 6.500l5.500 5.500-5.500 5.500"), chevu: P("M6.500 14.500l5.500-5.500 5.500 5.500"),
    check: P("M5 12.500l4.500 4.500L19 7.500"), copy: '<rect x="8.500" y="8.500" width="11" height="11" rx="2.500"/>' + P("M15.500 8.500V6.500a2 2 0 0 0-2-2h-7a2 2 0 0 0-2 2v7a2 2 0 0 0 2 2h2"),
    trash: P("M5 7h14M10 7V5h4v2M7 7l.8 12h8.400L17 7M10.500 11v5M13.500 11v5"),
    folder: P("M3.500 7.500a2 2 0 0 1 2-2h3.600l2 2.200h7.400a2 2 0 0 1 2 2v7.800a2 2 0 0 1-2 2h-13a2 2 0 0 1-2-2z"),
    gear: '<circle cx="12" cy="12" r="3"/>' + P("M12 3.500v2.200M12 18.300v2.200M3.500 12h2.200M18.300 12h2.200M6 6l1.500 1.500M16.500 16.500L18 18M18 6l-1.500 1.500M7.500 16.500L6 18"),
    shield: P("M12 3.500l7 2.500v5.500c0 4.300-2.900 7.600-7 9-4.100-1.400-7-4.700-7-9V6z"), shieldok: P("M12 3.500l7 2.500v5.500c0 4.300-2.900 7.600-7 9-4.100-1.400-7-4.700-7-9V6zM8.800 12l2.200 2.200 4.200-4.400"),
    hat: P("M7 11l1.300-5.200a1.500 1.500 0 0 1 1.800-1.100L12 5.200l1.900-.5a1.500 1.500 0 0 1 1.800 1.100L17 11M3.500 11.500h17") + '<circle cx="8" cy="16.300" r="2.700"/><circle cx="16" cy="16.300" r="2.700"/>' + P("M10.700 16.300h2.600"),
    speaker: P("M4.500 9.500v5h3.200L12 18.500v-13L7.700 9.500zM15.500 9a4 4 0 0 1 0 6M17.800 6.500a7.500 7.500 0 0 1 0 11"), speakeroff: P("M4.500 9.500v5h3.200L12 18.500v-13L7.700 9.500zM16 9.500l5 5M21 9.500l-5 5"),
    pin: P("M9 4.500h6l-1 5 3 3.500H7l3-3.500zM12 13v6.500"), ext: P("M10 6H6.500A2 2 0 0 0 4.500 8v9.500a2 2 0 0 0 2 2H16a2 2 0 0 0 2-2V14M13.500 4.500h6v6M19.500 4.500L11 13"),
    bolt: P("M13 3.500L5.500 13.500h5.500l-1 7 7.500-10h-5.500z"), hand: P("M8 12V6.500a1.500 1.500 0 0 1 3 0V11M11 10V5a1.500 1.500 0 0 1 3 0v5.500M14 10.500V6.500a1.500 1.500 0 0 1 3 0V13c0 4-2 6.500-5.500 6.500S6 17 5.200 14.200L4.500 12a1.500 1.500 0 0 1 2.600-.9L8 12.500"),
    cursor: '<path fill="currentColor" stroke="none" d="M5 3.200l13.500 7.200-5.600 1.700-2.500 5.700z"/>',
    list: P("M8.500 7h11M8.500 12h11M8.500 17h11M4.500 7h.1M4.500 12h.1M4.500 17h.1"), tab: '<rect x="3.500" y="5.500" width="17" height="13" rx="3"/>' + P("M3.500 9.500h17"),
    cmd: P("M9 9V6.500A2.500 2.500 0 1 0 6.500 9H9zm0 0v6m0-6h6m-6 6v2.500A2.500 2.500 0 1 1 6.500 15H9zm0 0h6m0-6V6.500A2.500 2.500 0 1 1 17.500 9H15zm0 0v6m0 0v2.500a2.500 2.500 0 1 0 2.500-2.500H15z"),
    info: '<circle cx="12" cy="12" r="8.500"/>' + P("M12 11v5M12 8v.1"), alert: '<circle cx="12" cy="12" r="8.500"/>' + P("M12 7.500v5M12 16v.1"),
    cert: P("M7 3.500h10a1.500 1.500 0 0 1 1.500 1.500v14.500l-3-1.600-3 1.600-3-1.600-3 1.600V5A1.500 1.500 0 0 1 7 3.500zM9 8.500h6M9 12h6"),
    cookie: P("M20.500 12.500a8.500 8.500 0 1 1-9-9 3.500 3.500 0 0 0 4.500 4.500 3 3 0 0 0 4.500 4.500z") + P("M9 10v.1M13 14v.1M8.500 14.500v.1"),
    image: '<rect x="3.500" y="4.500" width="17" height="15" rx="3"/><circle cx="9" cy="10" r="1.700"/>' + P("M4 17l5-4.500 3.500 3 3-2.500 4.500 4"),
    wifioff: P("M4 4l16 16M2.500 9.500A15 15 0 0 1 8 6.200M21.500 9.500A15 15 0 0 0 14 5.800M5.800 13A10 10 0 0 1 9 11M18.200 13a10 10 0 0 0-3-2M9 16.500a4.500 4.500 0 0 1 6 0M12 20h.1"),
    bookmark: P("M7 4.500h10a1.500 1.500 0 0 1 1.500 1.500v14L12 16.500 5.500 20V6A1.500 1.500 0 0 1 7 4.500z"),
    history: P("M4 12a8 8 0 1 0 2.500-5.800M4 4.500v4h4M12 8v4.200l2.800 1.800"), edit: P("M5 19l1-4L16.500 4.500a2 2 0 0 1 3 3L9 18zM14.500 6.500l3 3"),
    window: '<rect x="3.500" y="5.500" width="17" height="13" rx="3"/>', refresh: P("M19.500 12a7.500 7.500 0 1 1-2.400-5.500M19.500 4.500v4.500H15"),
    user: '<circle cx="12" cy="8.500" r="3.500"/>' + P("M5 20c.8-3.500 3.600-5.500 7-5.500s6.200 2 7 5.500"), up: P("M12 19V6M7 11l5-5 5 5"), layers: P("M12 4l8.500 4.500L12 13 3.500 8.500zM3.500 12.500L12 17l8.500-4.500M3.500 16L12 20.500 20.500 16"),
    brush: P("M14 5.500l4.500 4.500L10 18.500H5.500V14zM12 7.500l4.500 4.500"), zap: P("M13 3.500L5.500 13.500h5.500l-1 7 7.500-10h-5.500z"), play: '<path fill="currentColor" stroke="none" d="M8 5.500v13l11-6.500z"/>',
    maxi: '<rect x="6" y="6" width="12" height="12" rx="1.500"/>', restore: '<rect x="5.500" y="8.500" width="10" height="10" rx="1.500"/>' + P("M9 8.500V6.500A1.500 1.500 0 0 1 10.500 5h7A1.500 1.500 0 0 1 19 6.500v7a1.500 1.500 0 0 1-1.500 1.500H15.500"), mini: P("M6 17.500h12"),
    drop: P("M12 3.800c3.300 3.900 5.500 6.800 5.500 9.800a5.500 5.500 0 0 1-11 0c0-3 2.200-5.900 5.500-9.800z"),
    key: '<circle cx="8" cy="15" r="3.500"/>' + P("M10.500 12.500L19 4M16 7l2.500 2.500M14 9l2 2"), flag: P("M6 20V4.500M6 5h11l-2 4 2 4H6")
  };
  var cache = {};
  // px returns a finite positive number for markup sizes (never the caller's string).
  function px(v, d) { v = +v; return isFinite(v) && v > 0 ? Math.min(v, 4096) : d; }
  function icon(name, size, cls) {
    var s = document.createElement("span");
    s.className = "ic" + (cls ? " " + cls : "");
    var n = px(size, 18); // numbers only: these strings become markup
    s.innerHTML = '<svg width="' + n + '" height="' + n + '" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">' + (Object.prototype.hasOwnProperty.call(I, name) ? I[name] : "") + "</svg>";
    return s;
  }
  /* Logo mark: an "S" built from two speed streaks, with motion lines trailing behind it. */
  function logoSVG(size, id) {
    size = px(size, 20);
    id = String(id || "lg").replace(/[^\w-]/g, "");
    return '<svg width="' + size + '" height="' + size + '" viewBox="0 0 32 32" fill="none" aria-hidden="true"><defs><linearGradient id="' + id + '" x1="3" y1="4" x2="29" y2="29" gradientUnits="userSpaceOnUse"><stop offset="0" stop-color="#5B5BFF"/><stop offset=".55" stop-color="#8F8BFF"/><stop offset="1" stop-color="#3DF5C4"/></linearGradient></defs>' +
      '<path d="M25.500 8H15a5 5 0 0 0 0 10h2a5 5 0 0 1 0 10H6.500" stroke="url(#' + id + ')" stroke-width="3.800" stroke-linecap="round" stroke-linejoin="round"/>' +
      '<path d="M3 8h6.500M5.500 13h2.500" stroke="url(#' + id + ')" stroke-width="2.200" stroke-linecap="round" opacity=".55"/><path d="M29 28h-2.500" stroke="url(#' + id + ')" stroke-width="2.200" stroke-linecap="round" opacity=".55"/></svg>';
  }
  function logo(size, id) { var s = document.createElement("span"); s.className = "ic logo"; s.innerHTML = logoSVG(size || 20, id); return s; }
  function wordmark(h, id) {
    var s = document.createElement("span"); s.className = "wordmark";
    s.style.cssText = "display:inline-flex;align-items:center;gap:" + Math.round(h * 0.28) + "px;color:var(--text);font-weight:800;letter-spacing:-0.045em;font-size:" + Math.round(h * 0.86) + "px;line-height:1";
    s.innerHTML = logoSVG(h, id || "wm") + "<span>sweef</span>";
    return s;
  }
  /* Progress ring: pct 0..100, drawn in currentColor so the wrapper's colour is the tone. */
  function dataRing(pct, size, sw) {
    var n = px(size, 18), w = px(sw, 4), c = 2 * Math.PI * 14, f = Math.max(0, Math.min(100, +pct || 0)) / 100, s = document.createElement("span");
    s.className = "dring";
    s.innerHTML = '<svg width="' + n + '" height="' + n + '" viewBox="0 0 32 32" aria-hidden="true"><circle cx="16" cy="16" r="14" fill="none" stroke="currentColor" stroke-opacity=".2" stroke-width="' + w + '"/>' +
      (f > 0 ? '<circle cx="16" cy="16" r="14" fill="none" stroke="currentColor" stroke-width="' + w + '" stroke-linecap="round" stroke-dasharray="' + (c * f).toFixed(1) + " " + c.toFixed(1) + '" transform="rotate(-90 16 16)"/>' : "") + "</svg>";
    return s;
  }
  window.SW = window.SW || {};
  SW.dataRing = dataRing;
  /* Meter tone from the budget: calm below the first warn threshold, warmer after, never red until it is used up. */
  SW.dataTone = function (b) {
    if (!b || !b.enabled) return ""; var w = b.warnAt || [70, 90], p = +b.pct || 0;
    return b.exhausted ? "x" : p >= (w[1] || 90) ? "w2" : p >= (w[0] || 70) ? "w1" : "";
  };
  SW.I = I; SW.icon = icon; SW.logo = logo; SW.wordmark = wordmark; SW.logoSVG = logoSVG;
})();
