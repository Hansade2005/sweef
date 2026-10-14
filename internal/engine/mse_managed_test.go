package engine

import (
	"testing"
	"time"
)

// ManagedMediaSource: a MediaSource that announces streaming and whose SourceBuffers report bufferedchange.
func TestManagedMediaSource(t *testing.T) {
	tab, _ := openPlayerPage(t, "fam=h264&manual=1&mms=1")
	waitTrue(t, tab, "T.ready || T.error", 15*time.Second)
	var r map[string]any
	evalJSON(t, tab, `({ctor: T.ms.constructor.name, isMS: T.ms instanceof MediaSource, streaming: T.ms.streaming, tag: Object.prototype.toString.call(T.ms),
	  started: T.log.some(function(l){return l[0]==='startstreaming'}), supported: ManagedMediaSource.isTypeSupported('video/mp4; codecs="avc1.4d400c"'),
	  ev: typeof BufferedChangeEvent})`, &r)
	if r["ctor"] != "ManagedMediaSource" || r["isMS"] != true || r["streaming"] != true || r["tag"] != "[object ManagedMediaSource]" || r["supported"] != true || r["ev"] != "function" {
		t.Fatalf("managed media source: %v", r)
	}
	if r["started"] != true {
		t.Errorf("no startstreaming: %v", r)
	}
	evalJSON(t, tab, `T.get('h264/init-0.m4s').then(function(b){return T.append(T.sbv, b)}).then(function(){return T.get('h264/chunk-0-00001.m4s')}).then(function(b){return T.append(T.sbv, b)}).then(function(){return 1})`, nil)
	waitTrue(t, tab, "T.bc.length > 0", 5*time.Second)
	var bc [][]any
	evalJSON(t, tab, "T.bc", &bc)
	if bc[0][0] != "BufferedChangeEvent" || bc[0][2].(float64) <= bc[0][1].(float64) {
		t.Errorf("bufferedchange: %v", bc)
	}
	// removing the buffered range reports it as removed
	evalJSON(t, tab, `new Promise(function(res){ T.sbv.addEventListener('updateend', function(){ res(1); }, {once:true}); T.sbv.remove(0, 6); })`, nil)
	waitTrue(t, tab, "T.bc.some(function(e){return e[3] > 0})", 5*time.Second)
}
