//go:build linux && (amd64 || arm64)

package v4l2

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"sync"
	"syscall"

	"github.com/sweefbrowser/sweef/internal/capture"
)

// Provider enumerates and opens V4L2 cameras. The zero value is not usable: use NewProvider.
type Provider struct {
	// Glob lists candidate device nodes (default: /dev/video*).
	Glob func() ([]string, error)
	// Open opens a node (default: the real device).
	Open func(path string) (Device, error)
	// Stamp identifies a node's incarnation; when it changes the node is probed again (default: device number,
	// inode and ctime from stat, which change when a camera is unplugged and plugged back in).
	Stamp func(path string) string

	mu      sync.Mutex
	entries map[string]*entry
}

type entry struct {
	stamp string
	ok    bool
	info  devInfo
	id    string // assigned by list
}

// NewProvider returns a provider for the real /dev/video* nodes.
func NewProvider() *Provider {
	return &Provider{
		Glob:    func() ([]string, error) { return filepath.Glob("/dev/video*") },
		Open:    OpenDevice,
		Stamp:   statStamp,
		entries: map[string]*entry{},
	}
}

func statStamp(path string) string {
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		return "gone"
	}
	return fmt.Sprintf("%d/%d/%d.%d", st.Rdev, st.Ino, st.Ctim.Sec, st.Ctim.Nsec)
}

func (p *Provider) Name() string { return "v4l2" }

var numRe = regexp.MustCompile(`(\d+)$`)

// naturalLess orders /dev/video2 before /dev/video10.
func naturalLess(a, b string) bool {
	ma, mb := numRe.FindStringSubmatch(a), numRe.FindStringSubmatch(b)
	if ma != nil && mb != nil && a[:len(a)-len(ma[1])] == b[:len(b)-len(mb[1])] {
		na, _ := strconv.Atoi(ma[1])
		nb, _ := strconv.Atoi(mb[1])
		return na < nb
	}
	return a < b
}

// list refreshes the entry cache and returns the usable nodes in order. Probing opens each new node briefly (no
// exclusive access is needed to enumerate); known nodes cost one stat.
func (p *Provider) list() []string {
	paths, _ := p.Glob()
	sort.Slice(paths, func(i, j int) bool { return naturalLess(paths[i], paths[j]) })
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.entries == nil {
		p.entries = map[string]*entry{}
	}
	live := map[string]bool{}
	for _, path := range paths {
		live[path] = true
		st := p.Stamp(path)
		if e := p.entries[path]; e != nil && e.stamp == st {
			continue
		}
		e := &entry{stamp: st}
		if dev, err := p.Open(path); err == nil {
			info, ok, perr := probe(dev)
			_ = dev.Close()
			e.info, e.ok = info, ok && perr == nil
		}
		p.entries[path] = e
	}
	for path := range p.entries {
		if !live[path] {
			delete(p.entries, path)
		}
	}
	// ids: bus_info + card names a physical camera; two capture nodes of one device get a #n suffix
	var out []string
	seen := map[string]int{}
	for _, path := range paths {
		e := p.entries[path]
		if e == nil || !e.ok {
			continue
		}
		key := "v4l2:" + e.info.bus + "|" + e.info.card
		seen[key]++
		e.id = key
		if seen[key] > 1 {
			e.id = key + "#" + strconv.Itoa(seen[key])
		}
		out = append(out, path)
	}
	return out
}

// Refresh forgets everything probed so far (the next Devices call probes again).
func (p *Provider) Refresh() {
	p.mu.Lock()
	p.entries = map[string]*entry{}
	p.mu.Unlock()
}

func (p *Provider) Devices() []capture.DeviceInfo {
	var out []capture.DeviceInfo
	for _, path := range p.list() {
		p.mu.Lock()
		e := p.entries[path]
		label := e.info.card
		if label == "" {
			label = filepath.Base(path)
		}
		out = append(out, capture.DeviceInfo{ID: e.id, GroupID: e.info.bus, Label: label, Kind: capture.VideoInput})
		p.mu.Unlock()
	}
	return out
}

func (p *Provider) lookup(id string) (string, *entry) {
	for _, path := range p.list() {
		p.mu.Lock()
		e := p.entries[path]
		p.mu.Unlock()
		if e.id == id {
			return path, e
		}
	}
	return "", nil
}

// VideoCaps implements capture.CapsProvider.
func (p *Provider) VideoCaps(id string) (capture.VideoCaps, bool) {
	_, e := p.lookup(id)
	if e == nil {
		return capture.VideoCaps{}, false
	}
	return capsFromModes(e.info.modes), true
}

func (p *Provider) AudioCaps(string) (capture.AudioCaps, bool) { return capture.AudioCaps{}, false }

func (p *Provider) OpenVideo(id string) (capture.VideoSource, error) {
	path, e := p.lookup(id)
	if e == nil {
		return nil, capture.ErrNotFound
	}
	label := e.info.card
	if label == "" {
		label = filepath.Base(path)
	}
	return &Source{
		info:  capture.DeviceInfo{ID: id, GroupID: e.info.bus, Label: label, Kind: capture.VideoInput},
		path:  path,
		open:  p.Open,
		modes: e.info.modes,
	}, nil
}

func (p *Provider) OpenAudio(string) (capture.AudioSource, error) { return nil, capture.ErrUnsupported }
