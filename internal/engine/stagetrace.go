package engine

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// SWEEF_STAGES=1 prints one line per Tab.Evaluate and per paint to stderr with
// the time spent and what ran (layouts counted, including those forced from
// JavaScript). A development aid for the keystroke-latency work; off by default
// and then costs one bool test.
var stageTrace = os.Getenv("SWEEF_STAGES") != ""

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

func stageLog(kind, tab string, d time.Duration, format string, args ...any) {
	fmt.Fprintf(os.Stderr, "STAGE %s %s %.2fms "+format+"\n", append([]any{kind, tab, ms(d)}, args...)...)
}

// host-call statistics (SWEEF_STAGES): count and time per bridge function
// since the last hostStatTake.
var (
	hostMu  sync.Mutex
	hostCnt = map[string]int{}
	hostDur = map[string]time.Duration{}
)

func hostStat(name string, d time.Duration) {
	hostMu.Lock()
	hostCnt[name]++
	hostDur[name] += d
	hostMu.Unlock()
}

// hostStatTake returns "name=count/time ..." for the calls since the last take
// and resets the counters.
func hostStatTake() string {
	hostMu.Lock()
	defer hostMu.Unlock()
	type kv struct {
		n string
		c int
		d time.Duration
	}
	var all []kv
	tot, totc := time.Duration(0), 0
	for n, c := range hostCnt {
		all = append(all, kv{n, c, hostDur[n]})
		tot += hostDur[n]
		totc += c
	}
	sort.Slice(all, func(i, j int) bool { return all[i].d > all[j].d })
	var sb strings.Builder
	fmt.Fprintf(&sb, "host=%d calls %.2fms:", totc, ms(tot))
	for i, x := range all {
		if i >= 6 {
			break
		}
		fmt.Fprintf(&sb, " %s=%d/%.2fms", x.n, x.c, ms(x.d))
	}
	clear(hostCnt)
	clear(hostDur)
	return sb.String()
}
