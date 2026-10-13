package engine

import (
	"testing"
	"time"
)

// getImageData on a large canvas must stay interactive.
func TestCanvasReadbackSpeed(t *testing.T) {
	e := New(DefaultOptions())
	defer e.Close()
	tab, err := e.NewTab("about:blank")
	if err != nil {
		t.Fatal(err)
	}
	evalJSON(t, tab, `document.body.innerHTML='<canvas id=c width=1280 height=720></canvas>';var x=document.getElementById('c').getContext('2d');var g=x.createLinearGradient(0,0,1280,0);g.addColorStop(0,'#f00');g.addColorStop(1,'#00f');x.fillStyle=g;x.fillRect(0,0,1280,720);true`, nil)
	// Wall-clock budget on shared CI machines: the best of three runs must fit (a single run can be 1.7x slower
	// when other test binaries are using the cores).
	var sum int
	el := time.Hour
	for i := 0; i < 3 && el > 900*time.Millisecond; i++ {
		start := time.Now()
		evalJSON(t, tab, `(function(){var d=document.getElementById('c').getContext('2d').getImageData(0,0,1280,720).data;return d.length+d[0]+d[4*1279]})()`, &sum)
		if d := time.Since(start); d < el {
			el = d
		}
		t.Logf("getImageData 1280x720 run %d: %v (sum %d)", i+1, time.Since(start), sum)
	}
	if el > 900*time.Millisecond {
		t.Fatalf("getImageData too slow: best of 3 was %v", el)
	}
	start := time.Now()
	evalJSON(t, tab, `(function(){var x=document.getElementById('c').getContext('2d');var im=x.getImageData(0,0,1280,720);x.putImageData(im,0,0);return 1})()`, nil)
	t.Logf("get+put 1280x720: %v", time.Since(start))
}
