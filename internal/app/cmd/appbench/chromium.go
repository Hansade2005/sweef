package main

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type memSample struct {
	rssKiB, pssKiB int
	procs          int
}

// sampleTree sums RSS and PSS over a process and all its descendants.
func sampleTree(root int) memSample {
	ents, _ := filepath.Glob("/proc/[0-9]*/stat")
	kids := map[int][]int{}
	for _, e := range ents {
		b, err := os.ReadFile(e)
		if err != nil {
			continue
		}
		s := string(b)
		i := strings.LastIndex(s, ")")
		if i < 0 {
			continue
		}
		f := strings.Fields(s[i+2:])
		if len(f) < 2 {
			continue
		}
		ppid, _ := strconv.Atoi(f[1])
		pid, _ := strconv.Atoi(strings.Split(strings.TrimPrefix(e, "/proc/"), "/")[0])
		kids[ppid] = append(kids[ppid], pid)
	}
	var out memSample
	queue := []int{root}
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		queue = append(queue, kids[pid]...)
		rss, pss := procMem(pid)
		if rss == 0 {
			continue
		}
		out.procs++
		out.rssKiB += rss
		out.pssKiB += pss
	}
	return out
}

func procMem(pid int) (rss, pss int) {
	if b, err := os.ReadFile(fmt.Sprintf("/proc/%d/smaps_rollup", pid)); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			f := strings.Fields(l)
			if len(f) >= 2 && f[0] == "Pss:" {
				pss, _ = strconv.Atoi(f[1])
			}
			if len(f) >= 2 && f[0] == "Rss:" {
				rss, _ = strconv.Atoi(f[1])
			}
		}
	}
	return
}

func findChromium(given string) string {
	if given != "" {
		return given
	}
	for _, pat := range []string{"/opt/pw-browsers/chromium-*/chrome-linux/chrome", "/opt/pw-browsers/chromium/chrome-linux/chrome", "/usr/bin/chromium", "/usr/bin/chromium-browser", "/usr/bin/google-chrome"} {
		if m, _ := filepath.Glob(pat); len(m) > 0 {
			return m[0]
		}
	}
	return ""
}

func freePort() int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 9333
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func compareChromium(r *report, sweef, site, given string, runs int) {
	chrome := findChromium(given)
	r.f("## Comparison with Chromium (same machine, same local pages)\n\n")
	if chrome == "" || runtime.GOOS != "linux" {
		r.f("Chromium was not found (looked in /opt/pw-browsers and the usual system paths) or this is not Linux, so no comparison was run. Pass `-chromium /path/to/chrome`.\n\n")
		return
	}
	fmt.Println("chromium comparison with", chrome)
	ver, _ := exec.Command(chrome, "--version").Output()
	r.f("Chromium binary: `%s` (%s), launched as `--headless=new --no-sandbox --disable-gpu`.\n\n", chrome, strings.TrimSpace(string(ver)))
	r.f("### Time to a screenshot of a local article page\n\n")
	r.f("Wall time from exec to a finished 1280x800 screenshot of `%s/` (%d runs, median). Sweef's figure includes its 200 ms idle wait before it captures; its \"first frame\" figure from the cold start section excludes it. Cold = a fresh profile directory, warm = the same directory reused.\n\n", site, runs)
	r.f("| browser | cold (new profile) | warm (profile reused) |\n|---|---|---|\n")

	shot := func(args func(profile, out string) *exec.Cmd, outName string) (cold, warm time.Duration) {
		var cs, ws []time.Duration
		for i := 0; i < runs; i++ {
			prof, _ := os.MkdirTemp("", "appbench-cmp-")
			out := filepath.Join(prof, outName)
			c := args(prof, out)
			t0 := time.Now()
			_ = c.Run()
			if _, err := os.Stat(out); err == nil {
				cs = append(cs, time.Since(t0))
			}
			c = args(prof, out)
			os.Remove(out)
			t0 = time.Now()
			_ = c.Run()
			if _, err := os.Stat(out); err == nil {
				ws = append(ws, time.Since(t0))
			}
			os.RemoveAll(prof)
		}
		return percentile(cs, .5), percentile(ws, .5)
	}
	cc, cw := shot(func(prof, out string) *exec.Cmd {
		return exec.Command(chrome, "--headless=new", "--no-sandbox", "--disable-gpu", "--hide-scrollbars", "--user-data-dir="+prof,
			"--window-size=1280,800", "--screenshot="+out, site+"/")
	}, "shot.png")
	sc, sw := shot(func(prof, out string) *exec.Cmd {
		return exec.Command(sweef, "gui", "--headless", "--size", "1280x800", "--profile", filepath.Join(prof, "p"), "--no-restore", "--out", filepath.Dir(out), site+"/")
	}, "frame.png")
	r.f("| Chromium (headless, page only) | %s | %s |\n| Sweef (full browser chrome + page) | %s | %s |\n\n", ms(cc), ms(cw), ms(sc), ms(sw))

	r.f("### Memory with 1 and 10 tabs\n\n")
	r.f("Process tree summed after load: RSS counts shared pages once per process, PSS divides them fairly, so PSS is the figure to compare. Median of the last 10 samples (100 ms apart) after the pages settled.\n\n")
	r.f("| browser | tabs | processes | RSS (tree) | PSS (tree) |\n|---|---|---|---|---|\n")
	for _, n := range []int{1, 10} {
		m := chromiumMem(chrome, site, n)
		r.f("| Chromium (headless) | %d | %d | %.0f MiB | %.0f MiB |\n", n, m.procs, float64(m.rssKiB)/1024, float64(m.pssKiB)/1024)
	}
	for _, n := range []int{1, 10} {
		m := sweefMem(sweef, site, n)
		r.f("| Sweef (headless shell, full chrome) | %d | %d | %.0f MiB | %.0f MiB |\n", n, m.procs, float64(m.rssKiB)/1024, float64(m.pssKiB)/1024)
	}
	r.f("\nNote: headless Chromium has no browser UI and renders pages only when asked; Sweef paints its own tab strip, toolbar and overlay on every state change and keeps one page texture in memory.\n\n")
}

func chromiumMem(chrome, site string, tabs int) memSample {
	prof, _ := os.MkdirTemp("", "appbench-cdp-")
	defer os.RemoveAll(prof)
	port := freePort()
	c := exec.Command(chrome, "--headless=new", "--no-sandbox", "--disable-gpu", "--user-data-dir="+prof, "--remote-debugging-port="+strconv.Itoa(port), "about:blank")
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := c.Start(); err != nil {
		return memSample{}
	}
	defer func() { syscall.Kill(-c.Process.Pid, syscall.SIGKILL); c.Wait() }()
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	up := false
	for i := 0; i < 100; i++ {
		if resp, err := http.Get(base + "/json/version"); err == nil {
			resp.Body.Close()
			up = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !up {
		return memSample{}
	}
	cli := &http.Client{Timeout: 10 * time.Second}
	for i := 0; i < tabs; i++ {
		u := site + "/"
		if i > 0 {
			u = fmt.Sprintf("%s/a/%d", site, i)
		}
		req, _ := http.NewRequest("PUT", base+"/json/new?"+url.QueryEscape(u), nil)
		if resp, err := cli.Do(req); err == nil {
			resp.Body.Close()
		}
	}
	return settle(c.Process.Pid, 4*time.Second)
}

func sweefMem(sweef, site string, tabs int) memSample {
	steps := []string{`{"wait":"idle"}`}
	for k := 1; k < tabs; k++ {
		steps = append(steps, fmt.Sprintf(`{"newtab":"%s/a/%d"}`, site, k))
	}
	steps = append(steps, `{"wait":"idle"}`, `{"wait":"5s"}`)
	f := filepath.Join(os.TempDir(), "appbench-cmp-script.json")
	_ = os.WriteFile(f, []byte(`{"steps":[`+strings.Join(steps, ",")+`]}`), 0o644)
	c := exec.Command(sweef, "gui", "--headless", "--size", "1280x800", "--script", f, site+"/")
	if err := c.Start(); err != nil {
		return memSample{}
	}
	defer func() { c.Process.Kill(); c.Wait() }()
	return settle(c.Process.Pid, 4*time.Second)
}

// settle waits for pages to load, then samples the tree for a second.
func settle(pid int, wait time.Duration) memSample {
	time.Sleep(wait)
	var samples []memSample
	for i := 0; i < 10; i++ {
		samples = append(samples, sampleTree(pid))
		time.Sleep(100 * time.Millisecond)
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i].pssKiB < samples[j].pssKiB })
	return samples[len(samples)/2]
}
