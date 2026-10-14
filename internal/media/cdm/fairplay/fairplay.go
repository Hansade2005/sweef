// Package fairplay is the FairPlay Streaming key system (com.apple.fps) on macOS, backed by AVFoundation's
// AVContentKeySession, driven from Go through the Objective-C runtime with purego (no cgo).
//
// UNVERIFIED. This code is compile-checked for darwin/amd64 and darwin/arm64 and has never run: there is no Mac, no
// FairPlay application certificate and no FairPlay license server (KSM) in the environment it was written in. The
// Objective-C selectors follow Apple's AVContentKeySession documentation. To keep an unverified backend from changing
// behaviour on users' machines it is off unless SWEEF_FAIRPLAY=1 is set.
//
// What it does when it works: SetServerCertificate stores the application certificate FairPlay needs; GenerateRequest
// takes the "skd" key URI (or "sinf"/"cenc" init data, treated the same way), asks AVContentKeySession for a content
// key request and returns the Server Playback Context (SPC) as the license-request message; Update takes the Content
// Key Context (CKC) the key server answered with and completes the request.
//
// What it does not do, by design of the platform: FairPlay decrypts and decodes inside AVFoundation. The keys never
// reach Sweef, so the backend reports cdm.ModePlatformRender and Decrypt returns cdm.ErrUnsupported. To show the
// video, the protected stream has to be handed to an AVFoundation renderer that is a content key recipient: an
// AVSampleBufferDisplayLayer (video) and AVSampleBufferAudioRenderer (audio) added with
// AVContentKeySession.addContentKeyRecipient, fed with CMSampleBuffers that still carry the encrypted payload, or an
// AVPlayer item. That playback handoff (cdm.PlatformRenderer, StreamHandoff.NativeLayer) is NOT implemented; see
// docs/drm.md.
package fairplay

import (
	"strings"

	"github.com/sweefbrowser/sweef/internal/media/cdm"
)

// KeySystem is the key system string; the versioned legacy names are registered as aliases.
const KeySystem = "com.apple.fps"

// Aliases are the legacy key system names Safari also answers to.
var Aliases = []string{"com.apple.fps.1_0", "com.apple.fps.2_0", "com.apple.fps.3_0"}

// EnableEnv must be "1" for the provider to report itself available: the backend is unverified.
const EnableEnv = "SWEEF_FAIRPLAY"

// Options configures the provider.
type Options struct {
	// Enable overrides EnableEnv (settings).
	Enable bool
}

// NewProvider returns the FairPlay provider.
func NewProvider(o Options) cdm.Provider { return newProvider(o) }

// keyURI extracts the key identifier ("skd://...") from FairPlay init data.
func keyURI(initDataType string, data []byte) (string, error) {
	switch initDataType {
	case "skd":
		u := strings.TrimRight(string(data), "\x00")
		if u == "" {
			return "", cdm.Errorf(cdm.ExceptionTypeError, "empty skd init data")
		}
		return u, nil
	case "sinf", "cenc":
		// Safari hands the skd URI inside the init data; carry whatever printable URI is there.
		if i := strings.Index(string(data), "skd://"); i >= 0 {
			end := i
			for end < len(data) && data[end] >= 0x21 && data[end] < 0x7f {
				end++
			}
			return string(data[i:end]), nil
		}
	}
	return "", cdm.Errorf(cdm.ExceptionNotSupportedError, "cannot find a FairPlay key URI in %q init data", initDataType)
}
