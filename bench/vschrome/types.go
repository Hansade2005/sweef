//go:build linux

package main

// types.go: the JSON written to bench/vschrome/results/<label>.json. report.go turns exactly this file into
// docs/vs-chrome.md, so nothing in the document is typed by hand.

type Meta struct {
	Label      string               `json:"label"`
	Date       string               `json:"date"`
	Commit     string               `json:"sweef_commit"`
	Dirty      bool                 `json:"sweef_tree_dirty"`
	SrcMode    string               `json:"sweef_source"`
	CPU        string               `json:"cpu"`
	Cores      int                  `json:"cores"`
	MemMiB     int                  `json:"mem_mib"`
	Kernel     string               `json:"kernel"`
	Xvfb       string               `json:"xvfb"`
	XvfbArgs   string               `json:"xvfb_args"`
	Go         string               `json:"go"`
	Load       string               `json:"loadavg_at_start"`
	Browsers   map[string]BInfo     `json:"browsers"`
	Runs       int                  `json:"runs"`
	Quick      bool                 `json:"quick"`
	IdleSecs   int                  `json:"idle_cpu_seconds"`
	SettleSecs int                  `json:"memory_settle_seconds"`
	Window     string               `json:"window"`
	Tabs       int                  `json:"tabs_for_memory"`
	Sites      []string             `json:"real_sites"`
	SitesNote  []string             `json:"real_sites_notes,omitempty"`
	Calib      *CalibOut            `json:"calibration,omitempty"`
	Cross      map[string]*CrossOut `json:"detector_cross_check,omitempty"`
	Disk       map[string]Disk      `json:"disk,omitempty"`
	Env        map[string]string    `json:"env_notes,omitempty"`
	StartedAt  string               `json:"started_at"`
	FinishedAt string               `json:"finished_at,omitempty"`
}

type BInfo struct {
	Name    string   `json:"name"`
	Version string   `json:"version"`
	Binary  string   `json:"binary"`
	Args    []string `json:"args"`
	Note    string   `json:"note"`
}

type CrossOut struct {
	Damage []float64 `json:"damage_ms"`
	Poll   []float64 `json:"poll_ms"`
}

type CalibOut struct {
	DamageP50 float64 `json:"damage_p50_ms"`
	DamageP99 float64 `json:"damage_p99_ms"`
	PollP50   float64 `json:"poll_p50_ms"`
	PollP99   float64 `json:"poll_p99_ms"`
	N         int     `json:"n"`
}

type Disk struct {
	Path         string `json:"path"`
	Files        int    `json:"files"`
	ApparentB    int64  `json:"apparent_bytes"`
	OnDiskB      int64  `json:"on_disk_bytes"`
	GzipB        int64  `json:"gzip6_bytes"`
	XzB          int64  `json:"xz6_bytes"`
	Note         string `json:"note"`
	StrippedNote string `json:"stripped_note,omitempty"`
}

type StartupSample struct {
	OK        bool    `json:"ok"`
	Err       string  `json:"err,omitempty"`
	WindowMs  float64 `json:"window_mapped_ms"`
	FirstUIMs float64 `json:"first_ui_ms"`
	NTPMs     float64 `json:"newtab_ready_ms"`
}

type LoadSample struct {
	OK           bool           `json:"ok"`
	Err          string         `json:"err,omitempty"`
	FirstMs      float64        `json:"first_content_ms"`
	LoadMs       float64        `json:"load_event_ms"` // -1 for real sites (no beacon possible)
	RenderedMs   float64        `json:"rendered_ms,omitempty"`
	SettleMs     float64        `json:"visually_settled_ms"`
	SettledEarly bool           `json:"settle_capped,omitempty"`
	WireIn       int64          `json:"wire_in_bytes"`
	WireOut      int64          `json:"wire_out_bytes"`
	Conns        int64          `json:"conns"`
	Reqs         int64          `json:"requests,omitempty"` // local pages: HTTP requests the fixture server saw
	ReqPaths     map[string]int `json:"request_paths,omitempty"`
}

type MemSnap struct {
	memSample
	Tabs int `json:"tabs"`
}

type IdleCPU struct {
	OK        bool    `json:"ok"`
	Seconds   float64 `json:"window_s"`
	TreeCPUs  float64 `json:"tree_cpu_s"`    // user+sys seconds of the whole browser process tree
	XvfbCPUs  float64 `json:"xvfb_cpu_s"`    // user+sys seconds of the X server in the same window
	OtherCPUs float64 `json:"foreign_cpu_s"` // everything else on the machine (should be ~0)
	MaxProcs  int     `json:"max_procs"`
}

type Iter struct {
	Index       int                      `json:"index"`
	Position    int                      `json:"position"` // 0 = this browser ran first in this iteration, 1 = second
	Load        string                   `json:"loadavg_at_start"`
	CPUStallPct float64                  `json:"cpu_stall_pct"` // % of this iteration during which some runnable task waited for a CPU (machine-wide, /proc/pressure/cpu)
	Noisy       bool                     `json:"noisy,omitempty"`
	Startup     map[string]StartupSample `json:"startup"`
	Mem1        *MemSnap                 `json:"mem_1_tab,omitempty"`
	Mem10       *MemSnap                 `json:"mem_10_tabs,omitempty"`
	Idle        *IdleCPU                 `json:"idle_cpu,omitempty"`
	Lat         map[string][]float64     `json:"latency_ms"`
	LatFail     map[string]int           `json:"latency_missed"`
	Loads       map[string]LoadSample    `json:"loads"`
	Errors      []string                 `json:"errors,omitempty"`
	Complete    bool                     `json:"complete"`
}

type BResults struct {
	Iters []*Iter `json:"iterations"`
}

type Results struct {
	Meta     Meta                 `json:"meta"`
	Browsers map[string]*BResults `json:"results"`
	Complete bool                 `json:"complete"`
	Problems []string             `json:"problems,omitempty"`
}
