// Command mediasite loads a page that streams video and reports what the engine does with it: which
// formats the player probes, whether MediaSource is fed segments, whether a picture is decoded, whether
// currentTime advances, and what breaks. It is a manual check (real sites change, need a network and a
// proxy that allows them); docs/media-streaming.md records the results with their dates.
//
//	go run ./bench/mediasite -url 'https://www.youtube.com/watch?v=aqz-KE-bpKQ' -seconds 40
//	go run ./bench/mediasite -url https://developer.mozilla.org/en-US/docs/Web/HTML/Element/video -click 'video'
//
// The engine plays through the null audio sink (SWEEF_AUDIO=null is implied when no device is found), so
// nothing is heard; the report says whether audio was decoded and queued.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sweefbrowser/sweef/internal/engine"
)

const probeJS = `(function () {
  var L = window.__mediaProbe = { isType: {}, appended: {}, appendCalls: 0, appendErr: [], play: [], jsErr: [], decInfo: [], msOpen: 0, sbTypes: [], fetchBin: 0, xhrBin: 0 };
  function safe(f) { try { f(); } catch (e) { L.jsErr.push('probe: ' + e); } }
  safe(function () {
    var o = MediaSource.isTypeSupported;
    MediaSource.isTypeSupported = function (t) { var r = o.apply(this, arguments); L.isType[String(t)] = r; return r; };
    var oa = MediaSource.prototype.addSourceBuffer;
    MediaSource.prototype.addSourceBuffer = function (t) { L.sbTypes.push(String(t)); return oa.apply(this, arguments); };
    var ab = SourceBuffer.prototype.appendBuffer;
    SourceBuffer.prototype.appendBuffer = function (b) {
      L.appendCalls++;
      var k = (this.__probeId || (this.__probeId = 'sb' + Object.keys(L.appended).length));
      L.appended[k] = (L.appended[k] || 0) + (b.byteLength || 0);
      try { return ab.apply(this, arguments); } catch (e) { L.appendErr.push(k + ': ' + e.name + ' ' + e.message); throw e; }
    };
    var dec = navigator.mediaCapabilities && navigator.mediaCapabilities.decodingInfo;
    if (dec) navigator.mediaCapabilities.decodingInfo = function (c) {
      var p = dec.apply(this, arguments);
      p.then(function (r) { L.decInfo.push([JSON.stringify(c.video && c.video.contentType || c.audio && c.audio.contentType), r.supported, r.smooth]); });
      return p;
    };
    var pl = HTMLMediaElement.prototype.play;
    HTMLMediaElement.prototype.play = function () {
      var p = pl.apply(this, arguments);
      p.then(function () { L.play.push('ok'); }, function (e) { L.play.push(e.name); });
      return p;
    };
  });
  safe(function () {
    var of = window.fetch;
    L.fetchLog = [];
    window.fetch = function (input, init) {
      var url = String(input && input.url || input), p = of.apply(this, arguments);
      if (/videoplayback|googlevideo/.test(url)) {
        var rec = { url: url.replace(/\?.*/, '').slice(-40), reqBytes: init && init.body && (init.body.byteLength || init.body.length) || 0 };
        L.fetchLog.push(rec);
        p.then(function (r) {
          rec.status = r.status; rec.type = r.headers.get('content-type');
          r.clone().arrayBuffer().then(function (b) { rec.bytes = b.byteLength; var u = new Uint8Array(b.slice(0, 8)); rec.head = Array.prototype.join.call(u, ','); var all = new Uint8Array(b), t = ''; for (var i = 0; i < Math.min(all.length, 400); i++) t += (all[i] >= 32 && all[i] < 127) ? String.fromCharCode(all[i]) : '.'; rec.text = t; }, function (e) { rec.err = String(e); });
        }, function (e) { rec.err = String(e); });
      }
      return p;
    };
  });
  window.addEventListener('error', function (e) { L.jsErr.push(String(e.message) + ' @' + (e.filename || '') + ':' + e.lineno); });
  window.addEventListener('unhandledrejection', function (e) { L.jsErr.push('rejection: ' + String(e.reason && (e.reason.stack || e.reason))); });
})();`

const sampleJS = `(function () {
  var v = document.querySelector('video'), L = window.__mediaProbe || {};
  var o = { videos: document.querySelectorAll('video').length, appended: L.appended, calls: L.appendCalls, play: L.play, err: (L.appendErr || []).slice(-3) };
  if (v) {
    var b = []; for (var i = 0; i < v.buffered.length; i++) b.push([+v.buffered.start(i).toFixed(2), +v.buffered.end(i).toFixed(2)]);
    o.v = { t: +v.currentTime.toFixed(2), dur: v.duration, rs: v.readyState, ns: v.networkState, paused: v.paused, ended: v.ended, w: v.videoWidth, h: v.videoHeight, buffered: b, err: v.error && v.error.code, src: (v.currentSrc || '').slice(0, 40), muted: v.muted, vol: v.volume };
    try { var q = v.getVideoPlaybackQuality(); o.q = [q.totalVideoFrames, q.droppedVideoFrames, q.corruptedVideoFrames]; } catch (e) { /* ignore */ }
  }
  return JSON.stringify(o);
})()`

func main() {
	var (
		url     = flag.String("url", "", "page to load")
		seconds = flag.Int("seconds", 30, "how long to watch after the load")
		clicks  = flag.String("click", "", "comma separated selectors to click (the first that exists, once) after the load, e.g. '.ytp-play-button'")
		settle  = flag.Int("settle-ms", 100, "engine settle budget per step")
		console = flag.Bool("console", true, "print the page's console messages at the end")
		ua      = flag.String("ua", "chrome", `user agent: "chrome" (a desktop Chrome string), "engine" (the engine's default) or a full string`)
		eval    = flag.String("eval", "", "JavaScript to evaluate after the load and print (a diagnostic aid)")
		evalEnd = flag.String("eval-end", "", "JavaScript to evaluate when the watch time is over and print")
		serve   = flag.String("serve", "", "serve this HTML file at a local URL and load it (the -url flag is then ignored)")
		referer = flag.String("referer", "", "Referer header for the first request (YouTube's embedded player refuses to run without one)")
		jsMem   = flag.Int("js-mem", 0, "JavaScript heap limit in MiB (0 = the engine default)")
		play    = flag.Bool("play", true, "call play() on the first <video> when nothing starts by itself")
	)
	flag.Parse()
	if *serve != "" {
		page, err := os.ReadFile(*serve)
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write(page)
		}))
		defer srv.Close()
		*url = srv.URL + "/"
	}
	if *url == "" {
		flag.Usage()
		os.Exit(2)
	}
	if os.Getenv("SWEEF_AUDIO") == "" {
		os.Setenv("SWEEF_AUDIO", "null")
	}
	o := engine.DefaultOptions()
	o.ViewportWidth, o.ViewportHeight = 1280, 760
	o.SettleMs = *settle
	if *jsMem > 0 {
		o.JSMemoryMiB = *jsMem
	}
	e := engine.New(o)
	defer e.Close()
	t, err := e.NewTab("about:blank")
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	switch *ua {
	case "engine":
	case "chrome":
		t.SetUserAgent("Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36")
	default:
		t.SetUserAgent(*ua)
	}
	t.AddInitScript(probeJS)
	t0 := time.Now()
	var hdr map[string]string
	if *referer != "" {
		hdr = map[string]string{"Referer": *referer}
	}
	if err := t.Navigate(*url, hdr); err != nil {
		fmt.Println("navigate:", err)
	}
	fmt.Printf("loaded in %.1fs: %q\n", time.Since(t0).Seconds(), t.Title())
	if os.Getenv("MEDIASITE_TIMELINE") != "" {
		for _, m := range t.LoadTimeline() {
			if strings.HasPrefix(m.Name, "script") {
				fmt.Printf("  %6.0fms +%5.0fms %s %s\n", float64(m.At.Milliseconds()), float64(m.Dur.Milliseconds()), m.Name, trunc(m.Detail, 110))
			}
		}
	}
	if *eval != "" {
		raw, err := t.Evaluate(*eval)
		fmt.Printf("eval: %s %v\n", trunc(string(raw), 3000), err)
	}
	clicked := false
	played := false
	deadline := time.Now().Add(time.Duration(*seconds) * time.Second)
	var last string
	for tick := 0; time.Now().Before(deadline); tick++ {
		t.Settle(time.Duration(*settle) * time.Millisecond)
		time.Sleep(150 * time.Millisecond)
		if tick%4 != 0 {
			continue
		}
		raw, err := t.Evaluate(sampleJS)
		var s string
		if err == nil {
			_ = json.Unmarshal(raw, &s)
		}
		if s != last {
			fmt.Printf("%5.1fs %s\n", time.Since(t0).Seconds(), s)
			last = s
		}
		if !clicked && time.Since(t0) > 4*time.Second && *clicks != "" {
			for _, sel := range strings.Split(*clicks, ",") {
				if err := t.Click(strings.TrimSpace(sel)); err == nil {
					fmt.Printf("%5.1fs clicked %s\n", time.Since(t0).Seconds(), sel)
					clicked = true
					break
				}
			}
			if !clicked {
				clicked = true
				fmt.Printf("%5.1fs none of the click selectors exists\n", time.Since(t0).Seconds())
			}
		}
		if !played && *play && time.Since(t0) > 8*time.Second {
			played = true
			if raw, err := t.Evaluate(`(function(){var v=document.querySelector('video');if(!v)return 'no video';if(!v.paused)return 'already playing';v.play();return 'play() called'})()`); err == nil {
				fmt.Printf("%5.1fs %s\n", time.Since(t0).Seconds(), raw)
			}
		}
	}
	if *evalEnd != "" {
		raw, err := t.Evaluate(*evalEnd)
		fmt.Printf("eval-end: %s %v\n", trunc(string(raw), 3000), err)
	}
	raw, _ := t.Evaluate(`JSON.stringify(window.__mediaProbe || {})`)
	var js string
	_ = json.Unmarshal(raw, &js)
	var p struct {
		IsType map[string]bool `json:"isType"`
		SB     []string        `json:"sbTypes"`
		Dec    [][]any         `json:"decInfo"`
		Errs   []string        `json:"jsErr"`
	}
	_ = json.Unmarshal([]byte(js), &p)
	var types []string
	for k, v := range p.IsType {
		types = append(types, fmt.Sprintf("%v\t%s", v, k))
	}
	sort.Strings(types)
	fmt.Printf("\nMediaSource.isTypeSupported probes (%d):\n", len(types))
	for _, s := range types {
		fmt.Println("  " + s)
	}
	if raw, err := t.Evaluate(`JSON.stringify((window.__mediaProbe||{}).fetchLog||[])`); err == nil {
		var fl string
		_ = json.Unmarshal(raw, &fl)
		if fl != "" && fl != "[]" {
			fmt.Printf("media fetches: %s\n", trunc(fl, 2500))
		}
	}
	fmt.Printf("addSourceBuffer types: %v\n", p.SB)
	fmt.Printf("mediaCapabilities.decodingInfo calls: %d\n", len(p.Dec))
	for _, d := range p.Dec {
		fmt.Println("  ", d)
	}
	fmt.Printf("script errors (%d):\n", len(p.Errs))
	for i, e := range p.Errs {
		if i >= 12 {
			break
		}
		fmt.Println("  ", trunc(e, 300))
	}
	if *console {
		fmt.Println("console:")
		n := 0
		for _, ev := range t.ConsoleLog(0) {
			if d, ok := ev.Data.(map[string]string); ok {
				if n++; n > 25 {
					break
				}
				fmt.Printf("  %s: %s\n", d["level"], trunc(strings.ReplaceAll(d["text"], "\n", " | "), consoleWidth))
			}
		}
	}
	if os.Getenv("MEDIASITE_NET") != "" {
		fmt.Println("network:")
		width, _ := strconv.Atoi(os.Getenv("MEDIASITE_NET"))
		if width < 200 {
			width = 200
		}
		for _, ev := range t.NetworkLog(0) {
			fmt.Printf("  %v %v\n", ev.Kind, trunc(fmt.Sprint(ev.Data), width))
		}
	}
	fmt.Println("failed requests:")
	n := 0
	for _, ev := range t.NetworkLog(0) {
		if ev.Kind == engine.EvFailure {
			if n++; n > 15 {
				break
			}
			fmt.Printf("  %v\n", trunc(fmt.Sprint(ev.Data), 220))
		}
	}
}

var consoleWidth = func() int {
	if n, _ := strconv.Atoi(os.Getenv("MEDIASITE_CONSOLE")); n > 0 {
		return n
	}
	return 300
}()

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n-1] + "…"
	}
	return s
}
