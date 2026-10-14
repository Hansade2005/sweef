// Package playready is the PlayReady (com.microsoft.playready) key system on Windows, backed by the PlayReady CDM
// that ships with Windows through Media Foundation's content decryption module interfaces
// (IMFContentDecryptionModuleFactory / Access / IMFContentDecryptionModule / Session, mfmediaengine.h and
// mfcontentdecryptionmodule.h of the Windows SDK), called from Go through COM vtables with golang.org/x/sys/windows.
// The call sequence follows Chromium's media/cdm/win/media_foundation_cdm_util.cc and media_foundation_cdm_module.cc.
//
// UNVERIFIED. This code is compile-checked for windows/amd64 and windows/arm64 and has never run: there is no Windows
// machine, PlayReady runtime or license server in the environment it was written in. The interface IDs, the
// property keys and the vtable slot numbers were written from the public documentation and from memory of the SDK
// headers; they are marked where they are defined and must be compared with the SDK headers before anyone relies on
// them. To keep an unverified backend from changing behaviour on users' machines it is off unless
// SWEEF_PLAYREADY=1 is set.
//
// What it does when it works: it negotiates a PlayReady content decryption module, creates sessions, and carries
// license requests and responses (GenerateRequest, Update, Close, Remove, key statuses, expiration).
//
// What it does not do, by design of the platform: PlayReady in Media Foundation decrypts and decodes inside the
// operating system's protected media path; the keys never reach Sweef. The backend therefore reports
// cdm.ModePlatformRender, Decrypt returns cdm.ErrUnsupported, and playback needs a Media Foundation Media Engine
// (IMFMediaEngine with the content decryption module attached through IMFMediaEngineEME / IMFMediaEngineProtectedContent)
// to render the stream. That playback handoff is NOT implemented; see docs/drm.md.
package playready

import "github.com/sweefbrowser/sweef/internal/media/cdm"

// Key systems this package registers.
const (
	KeySystem               = "com.microsoft.playready"
	KeySystemRecommendation = "com.microsoft.playready.recommendation"
)

// EnableEnv must be "1" for the provider to report itself available: the backend is unverified.
const EnableEnv = "SWEEF_PLAYREADY"

// Options configures the providers.
type Options struct {
	// Enable overrides EnableEnv (settings).
	Enable bool
}

// NewProviders returns the PlayReady providers (the standard key system and its ".recommendation" variant, which
// asks for the best protection the machine offers).
func NewProviders(o Options) []cdm.Provider {
	return []cdm.Provider{newProvider(KeySystem, o), newProvider(KeySystemRecommendation, o)}
}
