package cdm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// WidevineOptions configures the Widevine key system.
type WidevineOptions struct {
	// Path is an explicit CDM library or directory (the setting behind SWEEF_WIDEVINE_CDM).
	Path string
	// HelperPath overrides the sweef-cdm location (default: $SWEEF_CDM_HELPER, then next to the executable).
	HelperPath string
	// Stderr receives the helper's diagnostics; nil discards them.
	Stderr io.Writer
	// Discover overrides the search (tests).
	Discover DiscoverOptions
	// Connect replaces starting the helper process (tests): it must return a Remote already initialized with init.
	Connect func(ctx context.Context, in *WidevineInstall, init InitParams) (*Remote, error)
}

type widevineProvider struct {
	opts WidevineOptions

	mu      sync.Mutex
	probed  bool
	install *WidevineInstall
	helper  string
	err     error
}

// NewWidevineProvider returns the com.widevine.alpha key system. It reports itself unsupported unless a Widevine
// CDM the user already has is found (DiscoverWidevine) and the helper binary exists; it never downloads or bundles
// a CDM.
func NewWidevineProvider(opts WidevineOptions) Provider { return &widevineProvider{opts: opts} }

func (p *widevineProvider) KeySystem() string { return WidevineKeySystem }

func (p *widevineProvider) probe() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.probed {
		return
	}
	p.probed = true
	d := p.opts.Discover
	if d.Path == "" {
		d.Path = p.opts.Path
	}
	in, err := DiscoverWidevine(d)
	if err != nil {
		p.err = err
		return
	}
	p.install = in
	if p.opts.Connect != nil {
		return
	}
	if p.opts.HelperPath != "" {
		p.helper = p.opts.HelperPath
		if st, err := os.Stat(p.helper); err != nil || st.IsDir() {
			p.err = fmt.Errorf("CDM host helper %q not found", p.helper)
		}
		return
	}
	h, err := FindHelper()
	if err != nil {
		p.err = err
		return
	}
	p.helper = h
}

// Refresh forgets the cached discovery result (after the user changed the setting or installed Chrome).
func (p *widevineProvider) Refresh() {
	p.mu.Lock()
	p.probed, p.install, p.err, p.helper = false, nil, nil, ""
	p.mu.Unlock()
}

func (p *widevineProvider) Availability() Availability {
	p.probe()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return Availability{Reason: p.err.Error()}
	}
	return Availability{Supported: true}
}

// Install returns what was discovered (nil when unavailable).
func (p *widevineProvider) Install() *WidevineInstall {
	p.probe()
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.install
}

// WidevineInstallOf returns the discovered installation behind a Widevine provider, or nil.
func WidevineInstallOf(p Provider) *WidevineInstall {
	if w, ok := p.(*widevineProvider); ok {
		return w.Install()
	}
	return nil
}

// RefreshProvider re-probes providers that cache availability (Widevine).
func RefreshProvider(p Provider) {
	if w, ok := p.(*widevineProvider); ok {
		w.Refresh()
	}
}

func (p *widevineProvider) Capabilities() Capabilities {
	p.probe()
	p.mu.Lock()
	in := p.install
	p.mu.Unlock()
	return widevineCapabilities(in)
}

// widevineCapabilities derives the capability set from the CDM's manifest. Codecs the manifest does not list are
// not offered; audio is always decrypted only (Sweef decodes it).
func widevineCapabilities(in *WidevineInstall) Capabilities {
	c := Capabilities{
		InitDataTypes: []string{initDataTypeCenc, initDataTypeKeyIDs, initDataTypeWebM},
		SessionTypes:  []SessionType{SessionTemporary},
		AudioCodecs:   []string{"mp4a", "opus", "vorbis", "flac"},
		Containers:    []string{"video/mp4", "audio/mp4", "video/webm", "audio/webm"},
		Schemes:       []Scheme{SchemeCenc, SchemeCbcs},
		// The desktop CDM is a software CDM (Widevine L3): SW_SECURE_CRYPTO. Services cap L3 video (typically at
		// 720p; some at 540p or 480p). That is the license server's policy, not something Sweef enforces.
		Robustness: []RobustnessLevel{
			{Name: ""},
			{Name: "SW_SECURE_CRYPTO", MaxHeight: 720},
			{Name: "SW_SECURE_DECODE", MaxHeight: 720},
		},
		DistinctiveIdentifier: NotAllowed,
		PersistentState:       Optional,
		Mode:                  ModeDecrypt,
		Notes:                 "software CDM (L3); VMP-gated services may refuse unsigned hosts, see docs/drm.md",
	}
	if in == nil {
		return c
	}
	c.Version = in.Version
	m := in.Manifest
	seen := map[string]bool{}
	for _, raw := range m.Codecs {
		kind, fam := CodecFamily(raw)
		if kind == "video" && !seen[fam] {
			seen[fam] = true
			c.VideoCodecs = append(c.VideoCodecs, fam)
		}
	}
	if len(m.Codecs) == 0 {
		// a library given by path without a manifest: Chrome's CDMs have always decrypted these
		c.VideoCodecs = []string{"avc1", "vp8", "vp9", "av01"}
	}
	if m.PersistentLicense {
		c.SessionTypes = append(c.SessionTypes, SessionPersistentLicense)
	}
	if len(m.Schemes) > 0 {
		c.Schemes = nil
		for _, s := range m.Schemes {
			if s == SchemeCenc || s == SchemeCbcs {
				c.Schemes = append(c.Schemes, s)
			}
		}
	}
	return c
}

func (p *widevineProvider) New(ctx context.Context, cfg InstanceConfig) (Backend, error) {
	if av := p.Availability(); !av.Supported {
		return nil, fmt.Errorf("%w: %s", ErrUnsupported, av.Reason)
	}
	p.mu.Lock()
	in, helper := p.install, p.helper
	p.mu.Unlock()
	init := InitParams{
		DistinctiveIdentifier: false,
		// Widevine keeps its device certificate in per-origin storage; Chrome always allows that on desktop.
		PersistentState: cfg.StorageDir != "",
		HardwareSecure:  cfg.HardwareSecure,
		Origin:          cfg.Origin,
	}
	if cfg.StorageDir != "" {
		init.StorageDir = filepath.Join(cfg.StorageDir, "widevine", originDirName(cfg.Origin))
	}
	if p.opts.Connect != nil {
		return p.opts.Connect(ctx, in, init)
	}
	args := []string{"-key-system", WidevineKeySystem, "-cdm", in.Library}
	if v := interfaceVersionArg(in.Manifest.InterfaceVersions); v != "" {
		args = append(args, "-interface-versions", v)
	}
	r, err := StartHelper(ctx, helper, args, nil, p.opts.Stderr, init)
	if err != nil {
		return nil, err
	}
	return r, nil
}

func interfaceVersionArg(v []int) string {
	var s []string
	for _, n := range v {
		s = append(s, strconv.Itoa(n))
	}
	return strings.Join(s, ",")
}

// originDirName maps an origin to a directory name that cannot escape the storage root.
func originDirName(origin string) string {
	if origin == "" {
		return "default"
	}
	sum := sha256.Sum256([]byte(origin))
	return hex.EncodeToString(sum[:8])
}
