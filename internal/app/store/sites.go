package store

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"strings"
	"sync"
)

const sitesFile = "sites.json"

// Visibility is the per-origin rule for what the agent may read.
type Visibility string

const (
	VisAllowed Visibility = "allowed"
	VisBlocked Visibility = "blocked"
	VisAsk     Visibility = "ask"
)

// SiteRule is everything remembered about one origin.
type SiteRule struct {
	Agent     Visibility `json:"agent,omitempty"`
	NoBlock   bool       `json:"noBlock,omitempty"`   // privacy exception: blocking off here
	NoUpgrade bool       `json:"noUpgrade,omitempty"` // https upgrade off here
	// Camera, Microphone and Screen are the stored capture decisions: "allow", "block" or "" (ask). Screen is never
	// "allow": sharing the screen asks every time; only a block is remembered.
	Camera     string `json:"camera,omitempty"`
	Microphone string `json:"microphone,omitempty"`
	Screen     string `json:"screen,omitempty"`
}

// Sites stores per-origin permissions with a default agent visibility.
type Sites struct {
	b       Backend
	mu      sync.RWMutex
	rules   map[string]SiteRule
	salt    string
	Default Visibility
}

type sitesDoc struct {
	V       int                 `json:"v"`
	Default Visibility          `json:"default"`
	Rules   map[string]SiteRule `json:"rules"`
	Salt    string              `json:"salt,omitempty"`
}

func OpenSites(b Backend) *Sites {
	s := &Sites{b: b, rules: map[string]SiteRule{}, Default: VisAsk}
	if data, err := b.ReadFile(sitesFile); err == nil {
		var d sitesDoc
		if json.Unmarshal(data, &d) == nil {
			if d.Rules != nil {
				s.rules = d.Rules
			}
			if d.Default != "" {
				s.Default = d.Default
			}
			s.salt = d.Salt
		}
	}
	return s
}

// OriginOf normalises a URL to scheme://host[:port]; "" for opaque URLs.
func OriginOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		if strings.HasPrefix(raw, "sweef://") {
			return "sweef://" + strings.SplitN(strings.TrimPrefix(raw, "sweef://"), "/", 2)[0]
		}
		return ""
	}
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host)
}

func (s *Sites) save() {
	b, _ := json.Marshal(sitesDoc{V: 1, Default: s.Default, Rules: s.rules, Salt: s.salt})
	_ = s.b.WriteAtomic(sitesFile, b)
}

// Agent returns the agent visibility for an origin or URL (default if none).
func (s *Sites) Agent(originOrURL string) Visibility {
	o := OriginOf(originOrURL)
	s.mu.RLock()
	defer s.mu.RUnlock()
	if r, ok := s.rules[o]; ok && r.Agent != "" {
		return r.Agent
	}
	return s.Default
}

// SetAgent sets the rule; "" resets to default.
func (s *Sites) SetAgent(originOrURL string, v Visibility) {
	o := OriginOf(originOrURL)
	if o == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.rules[o]
	r.Agent = v
	s.put(o, r)
	s.save()
}

// SetDefault sets the default visibility for origins without a rule.
func (s *Sites) SetDefault(v Visibility) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Default = v
	s.save()
}

func (s *Sites) put(o string, r SiteRule) {
	if r == (SiteRule{}) {
		delete(s.rules, o)
	} else {
		s.rules[o] = r
	}
}

// Rule returns the stored rule for the origin.
func (s *Sites) Rule(originOrURL string) SiteRule {
	o := OriginOf(originOrURL)
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.rules[o]
}

// SetPrivacyException toggles tracker blocking for an origin.
func (s *Sites) SetPrivacyException(originOrURL string, noBlock bool) {
	o := OriginOf(originOrURL)
	if o == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.rules[o]
	r.NoBlock = noBlock
	s.put(o, r)
	s.save()
}

// All returns a copy of every rule.
func (s *Sites) All() map[string]SiteRule {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]SiteRule, len(s.rules))
	for k, v := range s.rules {
		out[k] = v
	}
	return out
}

// Media returns the stored decision ("allow", "block" or "") for "camera", "microphone" or "screen".
func (s *Sites) Media(originOrURL, name string) string {
	o := OriginOf(originOrURL)
	s.mu.RLock()
	defer s.mu.RUnlock()
	r := s.rules[o]
	switch name {
	case "camera":
		return r.Camera
	case "microphone":
		return r.Microphone
	case "screen":
		return r.Screen
	}
	return ""
}

// SetMedia stores a decision; "" forgets it.
func (s *Sites) SetMedia(originOrURL, name, v string) {
	o := OriginOf(originOrURL)
	if o == "" || (v != "" && v != "allow" && v != "block") {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.rules[o]
	switch name {
	case "camera":
		r.Camera = v
	case "microphone":
		r.Microphone = v
	case "screen":
		if v == "allow" {
			return
		}
		r.Screen = v
	default:
		return
	}
	s.put(o, r)
	s.save()
}

// MediaOrigins lists the origins that have a stored capture decision.
func (s *Sites) MediaOrigins() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []string
	for o, r := range s.rules {
		if r.Camera != "" || r.Microphone != "" || r.Screen != "" {
			out = append(out, o)
		}
	}
	return out
}

// Salt is the per-profile secret behind the per-origin device ids pages see. It is created on first use and kept
// with the site rules, so a page that stores a deviceId finds it valid after a restart.
func (s *Sites) Salt() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.salt == "" {
		var b [16]byte
		_, _ = rand.Read(b[:])
		s.salt = hex.EncodeToString(b[:])
		s.save()
	}
	return s.salt
}
