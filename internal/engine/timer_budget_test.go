package engine

import (
	"testing"
	"time"
)

// A batch of due timers stops at the settle deadline: fifty animation-frame callbacks of 40 ms each used to run
// as one 2 s batch however small the budget.
func TestTimerBatchHonoursSettleDeadline(t *testing.T) {
	e := New(Options{CacheDir: "off", SettleMs: 300})
	defer e.Close()
	tab, _ := e.NewTab("about:blank")
	t0 := time.Now()
	tab.SetDocument(`<!doctype html><body><p id="n">0</p><script>
var n = 0;
function busy() { var s = Date.now(); while (Date.now() - s < 40) {} document.getElementById('n').textContent = ++n; }
for (var i = 0; i < 50; i++) requestAnimationFrame(busy);
</script></body>`, "https://timers.test/")
	if d := time.Since(t0); d > 1500*time.Millisecond {
		t.Fatalf("load took %v with a 300 ms settle budget", d)
	}
}
