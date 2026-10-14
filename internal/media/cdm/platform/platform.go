// Package platform assembles the key system registry for the running operating system: Clear Key everywhere,
// Widevine through the separate CDM host helper wherever a Widevine CDM is installed, PlayReady on Windows and
// FairPlay on macOS (both unverified and opt-in, see their packages).
//
// It imports only pure Go code on Linux, so the static Sweef binary can use it: dlopen lives in the helper binary.
package platform

import (
	"io"

	"github.com/sweefbrowser/sweef/internal/media/cdm"
	"github.com/sweefbrowser/sweef/internal/media/cdm/fairplay"
	"github.com/sweefbrowser/sweef/internal/media/cdm/playready"
)

// Options configures the registry.
type Options struct {
	// WidevinePath is an explicit Widevine CDM library or directory (settings; the environment variable
	// SWEEF_WIDEVINE_CDM is the fallback).
	WidevinePath string
	// HelperPath overrides the sweef-cdm helper location.
	HelperPath string
	// HelperStderr receives the helper's diagnostics (nil: discarded).
	HelperStderr io.Writer
	PlayReady    playready.Options
	FairPlay     fairplay.Options
}

// Register adds the platform key systems to r. A key system that cannot work on this machine is registered
// anyway: it reports why through Availability, which is what about:media style diagnostics show.
func Register(r *cdm.Registry, o Options) {
	r.Register(cdm.NewWidevineProvider(cdm.WidevineOptions{Path: o.WidevinePath, HelperPath: o.HelperPath, Stderr: o.HelperStderr}))
	for _, p := range playready.NewProviders(o.PlayReady) {
		r.Register(p)
	}
	r.Register(fairplay.NewProvider(o.FairPlay))
	for _, a := range fairplay.Aliases {
		r.RegisterAlias(a, fairplay.KeySystem)
	}
}

// NewRegistry returns a registry with Clear Key and every platform key system.
func NewRegistry(o Options) *cdm.Registry {
	r := cdm.NewDefaultRegistry()
	Register(r, o)
	return r
}
