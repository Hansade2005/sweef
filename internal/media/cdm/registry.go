package cdm

import (
	"context"
	"fmt"
	"mime"
	"sort"
	"strings"
	"sync"
)

// Requirement is the MediaKeysRequirement of the EME spec.
type Requirement string

const (
	NotAllowed Requirement = "not-allowed"
	Optional   Requirement = "optional"
	Required   Requirement = "required"
)

// RobustnessLevel is one robustness string a key system understands ("SW_SECURE_CRYPTO", "HW_SECURE_ALL", ...).
// The empty name is the default level every key system accepts.
type RobustnessLevel struct {
	Name     string
	Hardware bool // needs a hardware backed CDM (InstanceConfig.HardwareSecure)
	// MaxHeight is the tallest video the key system normally plays at this level; 0 means no known limit. It is
	// informational: license servers decide, not Sweef.
	MaxHeight int
}

// Capabilities is what a key system can do, as far as it can be known without a license server.
type Capabilities struct {
	InitDataTypes []string      // "cenc", "keyids", "webm", "skd"
	SessionTypes  []SessionType // temporary, persistent-license
	// VideoCodecs and AudioCodecs are normalized codec families, see CodecFamily: avc1, hevc, vp8, vp9, av01,
	// mp4a, opus, vorbis, flac, ac-3, ec-3.
	VideoCodecs []string
	AudioCodecs []string
	// Containers are lower-case MIME types ("video/mp4", "audio/mp4", "video/webm", "audio/webm").
	Containers []string
	Schemes    []Scheme
	// Robustness lists the robustness levels, weakest first. Empty means just the default level.
	Robustness            []RobustnessLevel
	DistinctiveIdentifier Requirement // what the CDM needs; "" is NotAllowed
	PersistentState       Requirement
	Mode                  DecryptMode
	Version               string // CDM version if known
	// Unverified marks a backend that compiles but has never run against the real vendor component.
	Unverified bool
	Notes      string
}

func (c Capabilities) hasInitData(t string) bool { return containsStr(c.InitDataTypes, t) }

// SupportsPersistentLicense reports whether persistent-license sessions work.
func (c Capabilities) SupportsPersistentLicense() bool {
	for _, s := range c.SessionTypes {
		if s == SessionPersistentLicense {
			return true
		}
	}
	return false
}

// MaxHeight returns the usual tallest video at the given robustness, or 0 when unknown or unlimited.
func (c Capabilities) MaxHeight(robustness string) int {
	for _, r := range c.Robustness {
		if r.Name == robustness {
			return r.MaxHeight
		}
	}
	return 0
}

func (c Capabilities) robustness(name string) (RobustnessLevel, bool) {
	if name == "" {
		for _, r := range c.Robustness {
			if r.Name == "" {
				return r, true
			}
		}
		return RobustnessLevel{}, true
	}
	for _, r := range c.Robustness {
		if r.Name == name {
			return r, true
		}
	}
	return RobustnessLevel{}, false
}

// Availability says whether a key system can be used on this machine right now.
type Availability struct {
	Supported bool
	// Reason explains an unsupported key system ("no Widevine CDM found; set SWEEF_WIDEVINE_CDM").
	Reason string
}

// Provider is a key system that can create Backends.
type Provider interface {
	// KeySystem is the canonical key system string.
	KeySystem() string
	// Availability probes the machine (cheap: it must not start a CDM process).
	Availability() Availability
	// Capabilities is only meaningful when Availability().Supported.
	Capabilities() Capabilities
	// New creates a CDM instance (EME createMediaKeys).
	New(ctx context.Context, cfg InstanceConfig) (Backend, error)
}

// Registry maps key system strings to providers.
type Registry struct {
	mu      sync.RWMutex
	byName  map[string]Provider
	aliases map[string]string
}

// NewRegistry returns an empty registry. Most callers want NewDefaultRegistry.
func NewRegistry() *Registry {
	return &Registry{byName: map[string]Provider{}, aliases: map[string]string{}}
}

// NewDefaultRegistry returns a registry holding Clear Key. Platform key systems are added by
// internal/media/cdm/platform.Register.
func NewDefaultRegistry() *Registry {
	r := NewRegistry()
	r.Register(NewClearKeyProvider())
	return r
}

// Register adds or replaces a provider.
func (r *Registry) Register(p Provider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byName[p.KeySystem()] = p
}

// RegisterAlias lets alias resolve to the provider registered under canonical (for example the legacy
// "com.apple.fps.1_0" names).
func (r *Registry) RegisterAlias(alias, canonical string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.aliases[alias] = canonical
}

// Lookup returns the provider for a key system string, whether or not it is available.
func (r *Registry) Lookup(keySystem string) (Provider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if c, ok := r.aliases[keySystem]; ok {
		keySystem = c
	}
	p, ok := r.byName[keySystem]
	return p, ok
}

// KeySystems lists the key systems that are registered and available, sorted.
func (r *Registry) KeySystems() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []string
	for n, p := range r.byName {
		if p.Availability().Supported {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// Status is one line of a registry report (about:media-keys style diagnostics).
type Status struct {
	KeySystem    string
	Availability Availability
	Capabilities Capabilities
}

// Report describes every registered key system, available or not, sorted by name.
func (r *Registry) Report() []Status {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []Status
	for n, p := range r.byName {
		st := Status{KeySystem: n, Availability: p.Availability()}
		if st.Availability.Supported {
			st.Capabilities = p.Capabilities()
		}
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].KeySystem < out[j].KeySystem })
	return out
}

// MediaCapability is a MediaKeySystemMediaCapability of the EME spec.
type MediaCapability struct {
	ContentType      string // video/mp4; codecs="avc1.64001f"
	Robustness       string
	EncryptionScheme string // "" | "cenc" | "cbcs" | "cbcs-1-9"
}

// KeySystemConfig is a MediaKeySystemConfiguration of the EME spec.
type KeySystemConfig struct {
	Label                 string
	InitDataTypes         []string
	AudioCapabilities     []MediaCapability
	VideoCapabilities     []MediaCapability
	DistinctiveIdentifier Requirement
	PersistentState       Requirement
	SessionTypes          []SessionType
}

// Access is the outcome of a successful negotiation: a MediaKeySystemAccess.
type Access struct {
	Provider Provider
	// Config is the accepted configuration (getConfiguration()), with unsupported entries removed.
	Config KeySystemConfig
	// HardwareSecure is true when an accepted capability needs a hardware robustness level.
	HardwareSecure bool
}

// NewInstanceConfig turns the access object into the configuration of createMediaKeys.
func (a *Access) NewInstanceConfig(origin, storageDir string) InstanceConfig {
	return InstanceConfig{
		DistinctiveIdentifierAllowed: a.Config.DistinctiveIdentifier == Required,
		PersistentStateAllowed:       a.Config.PersistentState == Required,
		HardwareSecure:               a.HardwareSecure,
		Origin:                       origin,
		StorageDir:                   storageDir,
		SessionTypes:                 a.Config.SessionTypes,
	}
}

// Negotiate implements the "get supported configuration" steps of requestMediaKeySystemAccess for one key system:
// it returns the first candidate configuration the provider can satisfy, restricted to what it supports, or an
// error that EME turns into a NotSupportedError rejection.
func (r *Registry) Negotiate(keySystem string, candidates []KeySystemConfig) (*Access, error) {
	p, ok := r.Lookup(keySystem)
	if !ok {
		return nil, fmt.Errorf("cdm: key system %q is not registered", keySystem)
	}
	av := p.Availability()
	if !av.Supported {
		reason := av.Reason
		if reason == "" {
			reason = "unavailable"
		}
		return nil, fmt.Errorf("cdm: key system %q is not available: %s", keySystem, reason)
	}
	caps := p.Capabilities()
	if len(candidates) == 0 {
		return nil, fmt.Errorf("cdm: no configuration given")
	}
	for _, c := range candidates {
		if cfg, hw, ok := negotiateOne(caps, c); ok {
			return &Access{Provider: p, Config: cfg, HardwareSecure: hw}, nil
		}
	}
	return nil, fmt.Errorf("cdm: none of the %d configurations is supported by %q", len(candidates), keySystem)
}

func negotiateOne(caps Capabilities, c KeySystemConfig) (KeySystemConfig, bool, bool) {
	out := KeySystemConfig{Label: c.Label}
	// initDataTypes
	if len(c.InitDataTypes) > 0 {
		for _, t := range c.InitDataTypes {
			if caps.hasInitData(t) {
				out.InitDataTypes = append(out.InitDataTypes, t)
			}
		}
		if len(out.InitDataTypes) == 0 {
			return out, false, false
		}
	}
	// distinctiveIdentifier / persistentState
	var ok bool
	if out.DistinctiveIdentifier, ok = resolveRequirement(c.DistinctiveIdentifier, caps.DistinctiveIdentifier); !ok {
		return out, false, false
	}
	if out.PersistentState, ok = resolveRequirement(c.PersistentState, caps.PersistentState); !ok {
		return out, false, false
	}
	// sessionTypes
	types := c.SessionTypes
	if len(types) == 0 {
		types = []SessionType{SessionTemporary}
	}
	for _, t := range types {
		switch t {
		case SessionTemporary:
		case SessionPersistentLicense:
			if !caps.SupportsPersistentLicense() || out.PersistentState == NotAllowed {
				return out, false, false
			}
		default:
			return out, false, false
		}
		out.SessionTypes = append(out.SessionTypes, t)
	}
	// capabilities
	if len(c.AudioCapabilities) == 0 && len(c.VideoCapabilities) == 0 {
		return out, false, false
	}
	hw := false
	var good bool
	if out.AudioCapabilities, good = filterCaps(caps, c.AudioCapabilities, false, &hw); !good {
		return out, false, false
	}
	if out.VideoCapabilities, good = filterCaps(caps, c.VideoCapabilities, true, &hw); !good {
		return out, false, false
	}
	return out, hw, true
}

// resolveRequirement picks the effective requirement given what the page asks and what the CDM needs.
func resolveRequirement(asked, needs Requirement) (Requirement, bool) {
	if asked == "" {
		asked = Optional
	}
	if needs == "" {
		needs = NotAllowed
	}
	switch needs {
	case Required:
		if asked == NotAllowed {
			return "", false
		}
		return Required, true
	case Optional:
		if asked == Optional {
			return NotAllowed, true // not needed: do not ask the user for consent
		}
		return asked, true
	default: // the CDM cannot use it
		if asked == Required {
			return "", false
		}
		return NotAllowed, true
	}
}

func filterCaps(caps Capabilities, in []MediaCapability, video bool, hw *bool) ([]MediaCapability, bool) {
	if len(in) == 0 {
		return nil, true // an absent list is fine
	}
	var out []MediaCapability
	for _, mc := range in {
		ct, err := ParseContentType(mc.ContentType)
		if err != nil {
			continue
		}
		if !containsStr(caps.Containers, ct.MIME) {
			continue
		}
		if video != strings.HasPrefix(ct.MIME, "video/") {
			continue
		}
		ok := true
		for _, codec := range ct.Codecs {
			kind, fam := CodecFamily(codec)
			if kind == "" {
				ok = false
				break
			}
			list := caps.AudioCodecs
			if kind == "video" {
				list = caps.VideoCodecs
			}
			if !containsStr(list, fam) {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		lvl, found := caps.robustness(mc.Robustness)
		if !found {
			continue
		}
		if mc.EncryptionScheme != "" {
			s := strings.SplitN(mc.EncryptionScheme, "-", 2)[0]
			if !containsScheme(caps.Schemes, Scheme(s)) {
				continue
			}
		}
		if lvl.Hardware {
			*hw = true
		}
		out = append(out, mc)
	}
	return out, len(out) > 0
}

// ContentType is a parsed MIME type with its codecs parameter.
type ContentType struct {
	MIME   string // lower case, "video/mp4"
	Codecs []string
}

// ParseContentType parses `video/mp4; codecs="avc1.64001f, mp4a.40.2"`.
func ParseContentType(s string) (ContentType, error) {
	mt, params, err := mime.ParseMediaType(s)
	if err != nil {
		return ContentType{}, err
	}
	ct := ContentType{MIME: strings.ToLower(mt)}
	for _, c := range strings.Split(params["codecs"], ",") {
		if c = strings.TrimSpace(c); c != "" {
			ct.Codecs = append(ct.Codecs, c)
		}
	}
	return ct, nil
}

// CodecFamily classifies an RFC 6381 codec string into "video" or "audio" and a normalized family name. It
// returns an empty kind for codecs it does not know.
func CodecFamily(codec string) (kind, family string) {
	c := strings.ToLower(strings.TrimSpace(codec))
	head := c
	if i := strings.IndexByte(c, '.'); i >= 0 {
		head = c[:i]
	}
	switch head {
	case "avc1", "avc3":
		return "video", "avc1"
	case "hvc1", "hev1", "dvh1", "dvhe":
		return "video", "hevc"
	case "vp8", "vp08":
		return "video", "vp8"
	case "vp9", "vp09":
		return "video", "vp9"
	case "av01":
		return "video", "av01"
	case "mp4a":
		return "audio", "mp4a"
	case "opus":
		return "audio", "opus"
	case "vorbis":
		return "audio", "vorbis"
	case "flac":
		return "audio", "flac"
	case "ac-3":
		return "audio", "ac-3"
	case "ec-3":
		return "audio", "ec-3"
	}
	return "", ""
}

func containsStr(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func containsScheme(l []Scheme, s Scheme) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}
