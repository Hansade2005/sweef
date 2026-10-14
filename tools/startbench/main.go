// Command startbench compares the start-up cost of two sweef binaries, for the size-budget work: a change that
// shrinks the binary (compressed embeds, fewer tables) must not make the browser start or load a first page
// slower, and must not use more memory.
//
//	go run ./tools/startbench -a /tmp/sweef.before -b /tmp/sweef.after [-n 30]
//
// Each case runs the two binaries alternately (so machine drift hits both), n times, and prints the median, mean and
// p90 of the wall time, the median user+system CPU time (steadier than wall time when the machine is shared) and the peak
// resident set size (getrusage ru_maxrss of the child), and the difference of the medians. Cases:
//
//	version      sweef version                              process start: runtime init, package init, paging in the image
//	page-cold    sweef eval <local page> <expr>             first page, empty SWEEF_CACHE_DIR: compiles the JS prelude
//	                                                        (inflates every embedded script) and loads Intl + time zone data
//	page-warm    the same with the prelude bytecode cached   what the second and later starts cost
//	gui-first    sweef gui --headless --no-restore --out D   start to the first composited chrome frame (UI assets)
//
// The page case needs no network: the tool serves the page itself on a loopback port. Peak RSS and CPU time are read
// on Linux and macOS; elsewhere they are reported as 0.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const page = `<!doctype html><meta charset=utf-8><title>t</title><body><p id=x>hello</p>
<script>
var d = new Intl.DateTimeFormat('de-DE', {timeZone: 'Asia/Tokyo', dateStyle: 'full', timeStyle: 'long'}).format(1700000000000);
var n = new Intl.NumberFormat('ja-JP', {style: 'currency', currency: 'JPY'}).format(1234567.891);
var r = new Intl.RelativeTimeFormat('fr').format(-3, 'day');
var u = new URLPattern({pathname: '/a/:id'}).test('https://x.test/a/42');
var s = new Intl.Segmenter('en', {granularity: 'word'}).segment('hello brave new world')[Symbol.iterator]().next().value.segment;
document.getElementById('x').textContent = [d, n, r, u, s].join('|');
</script>`

type sample struct {
	ms  float64
	rss float64 // MiB
	cpu float64 // user+system CPU time of the child, ms: steadier than wall time on a shared machine
	out string  // stdout
}

func run(bin string, args []string, env []string) (sample, error) {
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(), env...)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	t0 := time.Now()
	if err := cmd.Run(); err != nil {
		return sample{}, fmt.Errorf("%s %v: %w", bin, args, err)
	}
	el := time.Since(t0)
	rss, cpu := usage(cmd.ProcessState)
	return sample{float64(el.Microseconds()) / 1000, rss, cpu, stdout.String()}, nil
}

func stats(v []float64) (med, mean, p90 float64) {
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	for _, x := range s {
		mean += x
	}
	return s[len(s)/2], mean / float64(len(s)), s[len(s)*9/10]
}

func main() {
	a := flag.String("a", "", "binary before")
	b := flag.String("b", "", "binary after")
	n := flag.Int("n", 30, "runs per case and binary")
	only := flag.String("only", "", "run one case")
	flag.Parse()
	if *a == "" || *b == "" {
		fmt.Fprintln(os.Stderr, "usage: startbench -a before -b after [-n 30] [-only version|page-cold|page-warm|gui-first]")
		os.Exit(2)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	go http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, page)
	}))
	url := "http://" + ln.Addr().String() + "/"
	root, _ := os.MkdirTemp("", "startbench")
	defer os.RemoveAll(root)
	warm := filepath.Join(root, "warm")

	type kase struct {
		name string
		args func(i int) ([]string, []string)
		prep func(bin string) error
	}
	cases := []kase{
		{"version", func(int) ([]string, []string) { return []string{"version"}, nil }, nil},
		{"page-cold", func(i int) ([]string, []string) {
			return []string{"eval", url, "document.getElementById('x').textContent"},
				[]string{"SWEEF_CACHE_DIR=" + filepath.Join(root, fmt.Sprint("cold", i, time.Now().UnixNano())), "SWEEF_HOME=" + filepath.Join(root, "home")}
		}, nil},
		{"page-warm", func(int) ([]string, []string) {
			return []string{"eval", url, "document.getElementById('x').textContent"},
				[]string{"SWEEF_CACHE_DIR=" + warm, "SWEEF_HOME=" + filepath.Join(root, "home")}
		}, func(bin string) error {
			_, err := run(bin, []string{"eval", url, "1"}, []string{"SWEEF_CACHE_DIR=" + warm, "SWEEF_HOME=" + filepath.Join(root, "home")})
			return err
		}},
		{"gui-first", func(i int) ([]string, []string) {
			return []string{"gui", "--headless", "--no-restore", "--out", filepath.Join(root, "frames")},
				[]string{"SWEEF_CACHE_DIR=" + warm, "SWEEF_HOME=" + filepath.Join(root, "home")}
		}, nil},
	}
	fmt.Printf("%-10s %-7s %9s %9s %9s %9s %9s\n", "case", "binary", "ms med", "ms mean", "ms p90", "cpu ms", "RSS MiB")
	for _, c := range cases {
		if *only != "" && *only != c.name {
			continue
		}
		if c.prep != nil {
			for _, bin := range []string{*a, *b} {
				if err := c.prep(bin); err != nil {
					fmt.Fprintln(os.Stderr, "prep:", err)
					os.Exit(1)
				}
			}
		}
		ms := [2][]float64{}
		rss := [2][]float64{}
		cpu := [2][]float64{}
		var last [2]string
		for i := 0; i < *n+2; i++ { // two warm-up rounds are dropped
			for k, bin := range []string{*a, *b} {
				args, env := c.args(i)
				s, err := run(bin, args, env)
				if err != nil {
					fmt.Fprintln(os.Stderr, c.name+":", err)
					os.Exit(1)
				}
				last[k] = s.out
				if i >= 2 {
					ms[k] = append(ms[k], s.ms)
					rss[k] = append(rss[k], s.rss)
					cpu[k] = append(cpu[k], s.cpu)
				}
			}
		}
		var med, rmed, cmed [2]float64
		for k, name := range []string{"before", "after"} {
			m, mean, p90 := stats(ms[k])
			r, _, _ := stats(rss[k])
			cm, _, _ := stats(cpu[k])
			med[k], rmed[k], cmed[k] = m, r, cm
			fmt.Printf("%-10s %-7s %9.1f %9.1f %9.1f %9.1f %9.1f\n", c.name, name, m, mean, p90, cm, r)
		}
		fmt.Printf("%-10s %-7s %+8.1f%% %19s %+8.1f%% %+8.1f%%\n", c.name, "delta", 100*(med[1]-med[0])/med[0], "", 100*(cmed[1]-cmed[0])/cmed[0], 100*(rmed[1]-rmed[0])/rmed[0])
		if last[0] != last[1] {
			fmt.Printf("%-10s OUTPUT DIFFERS:\n  before: %q\n  after:  %q\n", c.name, last[0], last[1])
		} else if last[0] != "" {
			fmt.Printf("%-10s output identical: %q\n", c.name, strings.TrimSpace(last[0]))
		}
	}
}
