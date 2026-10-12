package store

import (
	"encoding/json"
	"fmt"
	"sort"
	"sync"
)

const (
	settingsFile    = "settings.json"
	SettingsVersion = 2
)

// Kind is a setting's value type.
type Kind string

const (
	KBool   Kind = "bool"
	KString Kind = "string"
	KInt    Kind = "int"
	KEnum   Kind = "enum"
)

// Spec describes one typed setting.
type Spec struct {
	Key     string   `json:"key"`
	Kind    Kind     `json:"kind"`
	Default any      `json:"default"`
	Enum    []string `json:"enum,omitempty"`
	Min     int      `json:"min,omitempty"`
	Max     int      `json:"max,omitempty"`
	Group   string   `json:"group,omitempty"`
}

// Schema is the full list of known settings (also sent to the settings page).
var Schema = []Spec{
	{Key: "theme", Kind: KEnum, Default: "system", Enum: []string{"system", "light", "dark"}, Group: "appearance"},
	{Key: "searchEngine", Kind: KString, Default: "duckduckgo", Group: "search"},
	{Key: "homepage", Kind: KString, Default: "sweef://newtab", Group: "general"},
	{Key: "restoreSession", Kind: KEnum, Default: "ask", Enum: []string{"always", "ask", "never"}, Group: "general"},
	{Key: "blockTrackers", Kind: KBool, Default: true, Group: "privacy"},
	{Key: "httpsUpgrade", Kind: KBool, Default: true, Group: "privacy"},
	{Key: "blockThirdPartyCookies", Kind: KBool, Default: true, Group: "privacy"},
	{Key: "sendGPC", Kind: KBool, Default: true, Group: "privacy"},
	{Key: "trimReferrer", Kind: KBool, Default: true, Group: "privacy"},
	{Key: "agentDefaultVisibility", Kind: KEnum, Default: "ask", Enum: []string{"allowed", "blocked", "ask"}, Group: "agent"},
	{Key: "sidebarOpen", Kind: KBool, Default: false, Group: "agent"},
	{Key: "sidebarWidth", Kind: KInt, Default: 360, Min: 240, Max: 720, Group: "agent"},
	{Key: "showBookmarksBar", Kind: KBool, Default: true, Group: "appearance"},
	{Key: "askDownloadLocation", Kind: KBool, Default: false, Group: "downloads"},
	{Key: "downloadsDir", Kind: KString, Default: "", Group: "downloads"},
	{Key: "defaultZoom", Kind: KInt, Default: 100, Min: 25, Max: 500, Group: "appearance"},
	{Key: "tabDiscardMinutes", Kind: KInt, Default: 30, Min: 0, Max: 1440, Group: "performance"},
}

// Settings holds typed values with defaults and schema versioning.
type Settings struct {
	b      Backend
	mu     sync.RWMutex
	vals   map[string]any
	specs  map[string]Spec
	onSet  []func(key string, v any)
	loaded int // version found on disk (0 = fresh)
}

type settingsDoc struct {
	V      int            `json:"v"`
	Values map[string]any `json:"values"`
}

// OpenSettings loads and migrates the settings file. Unknown keys are kept
// (forward compatibility) but invalid values for known keys fall back to the
// default.
func OpenSettings(b Backend) (*Settings, error) {
	s := &Settings{b: b, vals: map[string]any{}, specs: map[string]Spec{}}
	for _, sp := range Schema {
		s.specs[sp.Key] = sp
	}
	data, err := b.ReadFile(settingsFile)
	if err != nil {
		return s, nil
	}
	var d settingsDoc
	if json.Unmarshal(data, &d) != nil {
		return s, nil // corrupt: start from defaults, do not fail start-up
	}
	s.loaded = d.V
	stale := d.V != SettingsVersion
	migrate(&d)
	for k, v := range d.Values {
		if sp, ok := s.specs[k]; ok {
			if cv, err := coerce(sp, v); err == nil {
				s.vals[k] = cv
			}
			continue
		}
		s.vals[k] = v
	}
	if stale {
		s.saveLocked()
	}
	return s, nil
}

// migrate upgrades older documents in place.
func migrate(d *settingsDoc) {
	if d.Values == nil {
		d.Values = map[string]any{}
	}
	if d.V < 2 { // v1 used "doNotTrack" and "adblock"
		if v, ok := d.Values["adblock"]; ok {
			d.Values["blockTrackers"] = v
			delete(d.Values, "adblock")
		}
		if v, ok := d.Values["doNotTrack"]; ok {
			d.Values["sendGPC"] = v
			delete(d.Values, "doNotTrack")
		}
		d.V = 2
	}
}

func coerce(sp Spec, v any) (any, error) {
	switch sp.Kind {
	case KBool:
		if b, ok := v.(bool); ok {
			return b, nil
		}
	case KString:
		if s, ok := v.(string); ok {
			return s, nil
		}
	case KInt:
		var n int
		switch x := v.(type) {
		case float64:
			if x != float64(int(x)) {
				return nil, fmt.Errorf("%s: not an integer", sp.Key)
			}
			n = int(x)
		case int:
			n = x
		case int64:
			n = int(x)
		default:
			return nil, fmt.Errorf("%s: want integer", sp.Key)
		}
		if n < sp.Min || (sp.Max > 0 && n > sp.Max) {
			return nil, fmt.Errorf("%s: %d out of range [%d,%d]", sp.Key, n, sp.Min, sp.Max)
		}
		return n, nil
	case KEnum:
		if s, ok := v.(string); ok {
			for _, e := range sp.Enum {
				if e == s {
					return s, nil
				}
			}
			return nil, fmt.Errorf("%s: %q not one of %v", sp.Key, s, sp.Enum)
		}
	}
	return nil, fmt.Errorf("%s: want %s", sp.Key, sp.Kind)
}

// Set validates and stores a value.
func (s *Settings) Set(key string, v any) error {
	sp, ok := s.specs[key]
	if !ok {
		return fmt.Errorf("unknown setting %q", key)
	}
	cv, err := coerce(sp, v)
	if err != nil {
		return err
	}
	s.mu.Lock()
	if old, had := s.vals[key]; had && old == cv {
		s.mu.Unlock()
		return nil
	}
	s.vals[key] = cv
	s.saveLocked()
	hooks := append([]func(string, any){}, s.onSet...)
	s.mu.Unlock()
	for _, h := range hooks {
		h(key, cv)
	}
	return nil
}

// Get returns the value or its default.
func (s *Settings) Get(key string) any {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if v, ok := s.vals[key]; ok {
		return v
	}
	return s.specs[key].Default
}

func (s *Settings) Bool(key string) bool  { b, _ := s.Get(key).(bool); return b }
func (s *Settings) Str(key string) string { v, _ := s.Get(key).(string); return v }
func (s *Settings) Int(key string) int    { v, _ := s.Get(key).(int); return v }

// OnChange registers a callback run after Set.
func (s *Settings) OnChange(fn func(key string, v any)) {
	s.mu.Lock()
	s.onSet = append(s.onSet, fn)
	s.mu.Unlock()
}

// All returns every known setting with its effective value.
func (s *Settings) All() map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]any, len(s.specs))
	for k, sp := range s.specs {
		out[k] = sp.Default
	}
	for k, v := range s.vals {
		out[k] = v
	}
	return out
}

// Keys returns known keys sorted.
func (s *Settings) Keys() []string {
	ks := make([]string, 0, len(s.specs))
	for k := range s.specs {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func (s *Settings) saveLocked() {
	b, _ := json.MarshalIndent(settingsDoc{V: SettingsVersion, Values: s.vals}, "", " ")
	_ = s.b.WriteAtomic(settingsFile, b)
}
