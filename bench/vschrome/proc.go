//go:build linux

package main

import (
	"bytes"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Process-tree accounting. Every process the benchmark starts for a browser carries BENCH_TAG=<unique> in its
// environment; children inherit it (Chromium's zygote, GPU, renderer, utility and crashpad processes included, even
// the ones that are re-parented to init), so the tree is "every process whose /proc/PID/environ has the tag".

// tagPGID maps a run tag to the process group the browser was started in (set by launch).
var (
	tagMu   sync.Mutex
	tagPGID = map[string]int{}
)

func setTagPGID(tag string, pgid int) { tagMu.Lock(); tagPGID[tag] = pgid; tagMu.Unlock() }

type procStat struct{ ppid, pgrp int }

func readStat(pid int) (procStat, bool) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return procStat{}, false
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return procStat{}, false
	}
	f := strings.Fields(s[i+2:]) // f[0]=state f[1]=ppid f[2]=pgrp
	if len(f) < 3 {
		return procStat{}, false
	}
	pp, _ := strconv.Atoi(f[1])
	pg, _ := strconv.Atoi(f[2])
	return procStat{pp, pg}, true
}

// tagged returns the processes of the browser started under tag: every process in the browser's process group,
// every descendant of such a process (also across a setpgid/setsid), and every process that still carries
// BENCH_TAG=tag in its environment. (The environment alone is not enough: Chromium's child processes overwrite their
// environment block with their title.) The harness itself and Xvfb are in other groups and never match.
func tagged(tag string) []int {
	tagMu.Lock()
	pgid := tagPGID[tag]
	tagMu.Unlock()
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	needle := []byte("BENCH_TAG=" + tag)
	stats := map[int]procStat{}
	in := map[int]bool{}
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		st, ok := readStat(pid)
		if !ok {
			continue
		}
		stats[pid] = st
		if pgid != 0 && st.pgrp == pgid {
			in[pid] = true
			continue
		}
		if env, err := os.ReadFile("/proc/" + e.Name() + "/environ"); err == nil {
			for _, kv := range bytes.Split(env, []byte{0}) {
				if bytes.Equal(kv, needle) {
					in[pid] = true
					break
				}
			}
		}
	}
	for changed := true; changed; {
		changed = false
		for pid, st := range stats {
			if !in[pid] && in[st.ppid] && st.ppid > 1 {
				in[pid] = true
				changed = true
			}
		}
	}
	var out []int
	for pid := range in {
		out = append(out, pid)
	}
	return out
}

func readKB(path, key string) int64 {
	b, err := os.ReadFile(path)
	if err != nil {
		return -1
	}
	for _, ln := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(ln, key+":") {
			f := strings.Fields(ln[len(key)+1:])
			if len(f) > 0 {
				v, _ := strconv.ParseInt(f[0], 10, 64)
				return v
			}
		}
	}
	return -1
}

type memSample struct {
	PSSKiB int64            `json:"pss_kib"`
	RSSKiB int64            `json:"rss_kib"`
	Procs  int              `json:"procs"`
	ByComm map[string]int64 `json:"pss_kib_by_process,omitempty"` // diagnostics
}

// commOf names a process for the memory breakdown: Chromium's children are all called "chrome", so they are named
// by their --type (and utility sub-type) from the command line.
func commOf(pid int) string {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/comm")
	if err != nil {
		return "?"
	}
	comm := strings.TrimSpace(string(b))
	if comm != "chrome" {
		return comm
	}
	cl, _ := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	typ, sub := "browser", ""
	for _, a := range strings.Split(string(cl), "\x00") {
		switch {
		case strings.HasPrefix(a, "--type="):
			typ = strings.TrimPrefix(a, "--type=")
		case strings.HasPrefix(a, "--utility-sub-type="):
			sub = strings.TrimPrefix(a, "--utility-sub-type=")
			if i := strings.LastIndexByte(sub, '.'); i >= 0 {
				sub = sub[i+1:]
			}
		}
	}
	if sub != "" {
		return typ + ":" + sub
	}
	return typ
}

// treeMem sums Pss and Rss (from smaps_rollup) over the tagged tree. Processes whose smaps_rollup cannot be read
// (exited meanwhile) are skipped, not guessed.
func treeMem(tag string) memSample {
	s := memSample{ByComm: map[string]int64{}}
	for _, pid := range tagged(tag) {
		p := "/proc/" + strconv.Itoa(pid) + "/smaps_rollup"
		pss := readKB(p, "Pss")
		rss := readKB(p, "Rss")
		if pss < 0 || rss < 0 {
			continue
		}
		s.PSSKiB += pss
		s.RSSKiB += rss
		s.Procs++
		s.ByComm[commOf(pid)] += pss
	}
	return s
}

// cpuJiffies returns utime+stime of one process (jiffies; USER_HZ=100). cutime/cstime are left out on purpose: a
// reaped child's time would be counted again in its parent's cutime.
func cpuJiffies(pid int) (int64, bool) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, false
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0, false
	}
	f := strings.Fields(s[i+2:])
	if len(f) < 13 {
		return 0, false
	}
	var t int64
	for _, k := range []int{11, 12} { // utime, stime (fields 14 and 15 of proc(5); f[0] is field 3)
		v, _ := strconv.ParseInt(f[k], 10, 64)
		t += v
	}
	return t, true
}

// treeCPU samples the tree once per second for d and accumulates per-pid deltas, so processes that start or exit
// during the window are still counted (from their first sample on). Returns jiffies and the max process count.
func treeCPU(tag string, d time.Duration) (jiffies int64, maxProcs int) {
	last := map[int]int64{}
	end := time.Now().Add(d)
	for {
		pids := tagged(tag)
		if len(pids) > maxProcs {
			maxProcs = len(pids)
		}
		for _, pid := range pids {
			j, ok := cpuJiffies(pid)
			if !ok {
				continue
			}
			if prev, seen := last[pid]; seen && j >= prev {
				jiffies += j - prev
			}
			last[pid] = j
		}
		if !time.Now().Before(end) {
			return
		}
		time.Sleep(time.Second)
	}
}

func killTagged(tag string) {
	for i := 0; i < 3; i++ {
		for _, pid := range tagged(tag) {
			syscall.Kill(pid, syscall.SIGKILL)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func loadAvg() string {
	b, _ := os.ReadFile("/proc/loadavg")
	return strings.TrimSpace(string(b))
}

func load1() float64 {
	f := strings.Fields(loadAvg())
	if len(f) == 0 {
		return -1
	}
	v, _ := strconv.ParseFloat(f[0], 64)
	return v
}
