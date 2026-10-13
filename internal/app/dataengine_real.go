package app

import "github.com/sweefbrowser/sweef/internal/engine"

// realDataEngine adapts the engine's Data Saver API (docs/data-saver.md,
// "Engine API") to DataEngine. Tests inject a fake through Config.DataEngine.
type realDataEngine struct{}

func newDefaultDataEngine(*engine.Engine) DataEngine { return realDataEngine{} }

func saverLevel(level string) engine.SaverLevel {
	switch level {
	case lvlStrict:
		return engine.SaverStrict
	case lvlStandard:
		return engine.SaverStandard
	}
	return engine.SaverOff
}

func (realDataEngine) Available() bool { return true }

func (realDataEngine) SetContextPolicy(c *engine.BrowserContext, level string) {
	c.SetDataPolicy(engine.PolicyFor(saverLevel(level)))
}

func (realDataEngine) SetTabPolicy(t *engine.Tab, level string) {
	t.SetAgentLoading(level == lvlAgent) // the engine's agent mode: strict without the script limits
	if level == "" || level == lvlAgent {
		t.SetDataPolicy(nil)
		return
	}
	p := engine.PolicyFor(saverLevel(level))
	t.SetDataPolicy(&p)
}

func (realDataEngine) SetBackground(t *engine.Tab, bg bool) { t.SetBackground(bg) }

func snapOf(s engine.DataStats) DataSnap {
	out := DataSnap{WireIn: s.WireIn, WireOut: s.WireOut, CacheHitBytes: s.CacheHitBytes, AbortedBytes: s.AbortedBytes, SkippedKnown: s.SkippedKnownBytes,
		SkippedEstimate: s.SkippedEstimate, ByKind: map[string]int64{}, ByOrigin: s.ByOrigin, Skipped: map[string]int{}}
	for k, v := range s.ByKind {
		out.ByKind[string(k)] = v
	}
	for k, v := range s.Skipped {
		out.Skipped[string(k)] = v
	}
	return out
}

func (realDataEngine) ContextStats(c *engine.BrowserContext) DataSnap { return snapOf(c.DataStats()) }
func (realDataEngine) TabStats(t *engine.Tab) DataSnap                { return snapOf(t.DataStats()) }
func (realDataEngine) LoadSkipped(t *engine.Tab, id int64) error      { return t.LoadSkipped(id) }
func (realDataEngine) LoadSkippedImages(t *engine.Tab) (int, error)   { return t.LoadSkippedImages() }
func (realDataEngine) LoadFully(t *engine.Tab) error                  { return t.LoadFully() }
func (realDataEngine) FullyLoaded(t *engine.Tab) bool                 { return t.FullyLoaded() }
func (realDataEngine) IsDataEvent(ev engine.Event) bool               { return ev.Kind == engine.EvData }
