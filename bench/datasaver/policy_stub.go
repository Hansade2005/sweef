//go:build !datasaver_engine

package main

import (
	"errors"

	"github.com/sweefbrowser/sweef/internal/engine"
)

// This file is used until the engine's DataPolicy / DataStats API has landed (docs/data-saver.md, "Engine API").
// When it has, build with `-tags datasaver_engine` (scripts/bench/datasaver.sh does this automatically when
// `go doc ./internal/engine DataPolicy` succeeds). See policy_engine.go.

var errNoPolicyAPI = errors.New("engine.DataPolicy is not available in this build (engine API has not landed)")

func policyAPI() bool { return false }

// applyPolicy configures the context for mode "off", "standard" or "strict".
func applyPolicy(ctx *engine.BrowserContext, level string) error {
	if level == "off" {
		return nil // the engine today has no data policy: nothing is blocked, which is what "off" means
	}
	return errNoPolicyAPI
}

// engineCounters reports the engine's own wire counters (DataStats). ok=false when the API does not exist.
func engineCounters(ctx *engine.BrowserContext) (c EngineCounters, ok bool) { return c, false }
