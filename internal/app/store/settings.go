package store

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"sync"
)

const (
	settingsFile    = "settings.json"
	SettingsVersion = 3
)

// Kind is a setting's value type.
type Kind string

const (
	KBool   Kind = "bool"
	KString Kind = "string"
	KInt    Kind = "int"
	KEnum   Kind = "enum"
	KObject Kind = "object" // a JSON object, normalised by Spec.Norm
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
	// Norm validates and canonicalises a KObject value.
	Norm func(any) (any, error) `json:"-"`
}

// Schema is the full list of known settings (also sent to the settings page).
// The keys are the ones the chrome UI reads and writes with `settings.set`
// (docs/gui-contract.md, "settings keys"), plus a few the app owns.
var Schema = []Spec{
	{Key: "theme", Kind: KEnum, Default: "system", Enum: []string{"system", "light", "dark"}, Group: "appearance"},
	{Key: "density", Kind: KEnum, Default: "comfortable", Enum: []string{"comfortable", "compact"}, Group: "appearance"},
	{Key: "bookmarks.bar", Kind: KBool, Default: false, Group: "appearance"},
	{Key: "sidebar.open", Kind: KBool, Default: false, Group: "agent"},
	{Key: "sidebar.width", Kind: KInt, Default: 380, Min: 320, Max: 560, Group: "agent"},
	{Key: "agent.mode", Kind: KEnum, Default: "ask", Enum: []string{"ask", "agent"}, Group: "agent"},
	{Key: "agent.latencyStrip", Kind: KBool, Default: true, Group: "agent"},
	{Key: "agent.sources.page", Kind: KBool, Default: true, Group: "agent"},
	{Key: "agent.sources.web", Kind: KBool, Default: false, Group: "agent"},
	{Key: "agent.confirmations", Kind: KBool, Default: true, Group: "agent"},
	{Key: "agent.visibilityDefault", Kind: KEnum, Default: "ask", Enum: []string{"allowed", "blocked", "ask"}, Group: "agent"},
	{Key: "privacy.trackers", Kind: KEnum, Default: "standard", Enum: []string{"standard", "strict", "off"}, Group: "privacy"},
	{Key: "privacy.gpc", Kind: KBool, Default: true, Group: "privacy"},
	{Key: "privacy.httpsUpgrade", Kind: KBool, Default: true, Group: "privacy"},
	{Key: "privacy.clearOnExit", Kind: KBool, Default: false, Group: "privacy"},
	{Key: "privacy.thirdPartyCookies", Kind: KBool, Default: true, Group: "privacy"}, // block third-party cookies
	{Key: "privacy.trimReferrer", Kind: KBool, Default: true, Group: "privacy"},
	{Key: "search.engine", Kind: KString, Default: "duckduckgo", Group: "search"},
	{Key: "search.suggest", Kind: KBool, Default: true, Group: "search"},
	{Key: "omni.askRow", Kind: KBool, Default: true, Group: "search"},
	{Key: "startup.mode", Kind: KEnum, Default: "newtab", Enum: []string{"newtab", "restore", "pages"}, Group: "general"},
	{Key: "startup.pages", Kind: KString, Default: "", Group: "general"},
	{Key: "downloads.dir", Kind: KString, Default: "", Group: "downloads"}, // "" = ~/Downloads
	{Key: "downloads.ask", Kind: KBool, Default: false, Group: "downloads"},
	{Key: "zoom.default", Kind: KInt, Default: 100, Min: 25, Max: 500, Group: "appearance"},
	{Key: "tabs.discardMinutes", Kind: KInt, Default: 30, Min: 0, Max: 1440, Group: "performance"},
	{Key: "onboarding.done", Kind: KBool, Default: false, Group: "general"},

	// Notifications, Web Push and background sync (docs/notifications.md, docs/push.md).
	{Key: "notifications.dnd", Kind: KBool, Default: false, Group: "notifications"},
	{Key: "notifications.default", Kind: KEnum, Default: "ask", Enum: []string{"ask", "blocked"}, Group: "notifications"},
	{Key: "push.service", Kind: KString, Default: "", Group: "notifications"}, // autopush service URL; empty: SWEEF_PUSH_SERVICE, else none

	// Data Saver (docs/data-saver.md). data.level "auto" means: standard when a
	// budget is set, else off. The resolved level is in state.data.level.
	{Key: "data.level", Kind: KEnum, Default: "auto", Enum: []string{"auto", "off", "standard", "strict"}, Group: "data"},
	{Key: "data.budget", Kind: KObject, Default: DefaultBudget().Map(), Group: "data", Norm: normBudget},
	{Key: "data.sites", Kind: KObject, Default: map[string]any{}, Group: "data", Norm: normDataSites},
	{Key: "data.agentLite", Kind: KBool, Default: true, Group: "data"},
	{Key: "data.showMeter", Kind: KBool, Default: true, Group: "data"},
}

func normBudget(v any) (any, error) {
	b, err := ParseBudget(v)
	if err != nil {
		return nil, err
	}
	return b.Map(), nil
}

// normDataSites keeps {origin: "full"|"lite"} entries with a usable origin.
func normDataSites(v any) (any, error) {
	in, ok := v.(map[string]any)
	if !ok {
		if m, ok2 := v.(map[string]string); ok2 {
			in = map[string]any{}
			for k, x := range m {
				in[k] = x
			}
		} else {
			return nil, fmt.Errorf("data.sites: want an object")
		}
	}
	out := map[string]any{}
	for k, x := range in {
		r, _ := x.(string)
		if o := OriginOf(k); o != "" && (r == "full" || r == "lite") {
			out[o] = r
		}
	}
	return out, nil
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
	if d.V < 3 { // v2 used camelCase keys; v3 uses the UI's dotted keys
		rename := map[string]string{
			"searchEngine": "search.engine", "httpsUpgrade": "privacy.httpsUpgrade",
			"blockThirdPartyCookies": "privacy.thirdPartyCookies", "sendGPC": "privacy.gpc",
			"trimReferrer": "privacy.trimReferrer", "agentDefaultVisibility": "agent.visibilityDefault",
			"sidebarOpen": "sidebar.open", "sidebarWidth": "sidebar.width", "showBookmarksBar": "bookmarks.bar",
			"askDownloadLocation": "downloads.ask", "downloadsDir": "downloads.dir", "defaultZoom": "zoom.default",
			"tabDiscardMinutes": "tabs.discardMinutes",
		}
		for old, nw := range rename {
			if v, ok := d.Values[old]; ok {
				d.Values[nw] = v
				delete(d.Values, old)
			}
		}
		if v, ok := d.Values["blockTrackers"]; ok { // bool -> standard|off
			if b, _ := v.(bool); b {
				d.Values["privacy.trackers"] = "standard"
			} else {
				d.Values["privacy.trackers"] = "off"
			}
			delete(d.Values, "blockTrackers")
		}
		if v, ok := d.Values["restoreSession"]; ok { // always|ask|never -> restore|newtab
			if v == "always" {
				d.Values["startup.mode"] = "restore"
			} else {
				d.Values["startup.mode"] = "newtab"
			}
			delete(d.Values, "restoreSession")
		}
		if w, ok := d.Values["sidebar.width"].(float64); ok && w < 320 {
			d.Values["sidebar.width"] = float64(320)
		}
		delete(d.Values, "homepage")
		d.V = 3
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
	case KObject:
		if sp.Norm != nil {
			return sp.Norm(v)
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
	if old, had := s.vals[key]; had && reflect.DeepEqual(old, cv) {
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
