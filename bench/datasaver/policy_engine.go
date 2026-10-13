//go:build datasaver_engine

package main

import (
	"fmt"

	"github.com/sweefbrowser/sweef/internal/engine"
)

// Real adapter, compiled with -tags datasaver_engine once the engine API from docs/data-saver.md exists.

func policyAPI() bool { return true }

func applyPolicy(ctx *engine.BrowserContext, level string) error {
	var l engine.SaverLevel
	switch level {
	case "off":
		l = engine.SaverOff
	case "standard":
		l = engine.SaverStandard
	case "strict":
		l = engine.SaverStrict
	default:
		return fmt.Errorf("unknown level %q", level)
	}
	ctx.SetDataPolicy(engine.PolicyFor(l))
	return nil
}

func engineCounters(ctx *engine.BrowserContext) (EngineCounters, bool) {
	s := ctx.DataStats()
	c := EngineCounters{
		WireIn: s.WireIn, WireOut: s.WireOut,
		CacheHitBytes: s.CacheHitBytes, AbortedBytes: s.AbortedBytes,
		SkippedEstimate: s.SkippedEstimate, SkippedKnown: s.SkippedKnownBytes, Requests: s.Requests,
		Skipped: map[string]int{},
	}
	for k, n := range s.Skipped {
		c.Skipped[string(k)] = n
	}
	return c, true
}
