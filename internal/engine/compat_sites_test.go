package engine

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"
)

// The feature checks below are the ones the start-up scripts of Google Meet, WhatsApp Web and the YouTube player make
// (existence of constructors and methods, and the answers of the capability queries). They run against a local page
// and prove nothing about the live sites; docs/compat.md says so. The list of APIs that are known to be absent is part of
// the test: when one is implemented the test fails and the documentation has to change with it.

var compatChecks = map[string][][2]string{
	"meet": {
		{"RTCPeerConnection", `typeof RTCPeerConnection === 'function'`},
		{"RTCRtpSender.getCapabilities", `typeof RTCRtpSender.getCapabilities === 'function' && RTCRtpSender.getCapabilities('video').codecs.length > 0`},
		{"setCodecPreferences", `typeof RTCRtpTransceiver.prototype.setCodecPreferences === 'function'`},
		{"RTCRtpScriptTransform", `typeof RTCRtpScriptTransform === 'function'`},
		{"createEncodedStreams", `typeof RTCRtpSender.prototype.createEncodedStreams === 'function'`},
		{"getStats", `typeof RTCPeerConnection.prototype.getStats === 'function'`},
		{"jitterBufferTarget", `'jitterBufferTarget' in RTCRtpReceiver.prototype`},
		{"RTCDataChannel", `typeof RTCDataChannel === 'function'`},
		{"simulcast sendEncodings", `(() => { const pc = new RTCPeerConnection(); const t = pc.addTransceiver('video', { sendEncodings: [{ rid: 'h' }, { rid: 'l', scaleResolutionDownBy: 2 }] }); const n = t.sender.getParameters().encodings.length; pc.close(); return n === 2; })()`},
		{"getUserMedia", `typeof navigator.mediaDevices.getUserMedia === 'function'`},
		{"getDisplayMedia", `typeof navigator.mediaDevices.getDisplayMedia === 'function'`},
		{"supported constraints", `(() => { const c = navigator.mediaDevices.getSupportedConstraints(); return !!(c.echoCancellation && c.noiseSuppression && c.autoGainControl && c.width && c.frameRate); })()`},
		{"MediaStreamTrackProcessor", `typeof MediaStreamTrackProcessor === 'function' && typeof MediaStreamTrackGenerator === 'function'`},
		{"VideoFrame", `typeof VideoFrame === 'function' && typeof AudioData === 'function'`},
		{"OffscreenCanvas", `typeof OffscreenCanvas === 'function'`},
		{"requestVideoFrameCallback", `typeof HTMLVideoElement.prototype.requestVideoFrameCallback === 'function'`},
		{"AudioContext", `typeof AudioContext === 'function' && typeof AudioWorkletNode === 'function' && typeof MediaStreamAudioSourceNode === 'function'`},
		{"setSinkId", `typeof HTMLMediaElement.prototype.setSinkId === 'function'`},
		{"fullscreen", `typeof document.documentElement.requestFullscreen === 'function' && 'fullscreenElement' in document`},
		{"Permissions camera", `typeof navigator.permissions.query === 'function'`},
		{"Web Locks", `typeof navigator.locks.request === 'function'`},
		{"BroadcastChannel", `typeof BroadcastChannel === 'function'`},
		{"Intl.DateTimeFormat", `new Intl.DateTimeFormat('en', { timeZone: 'UTC', hour: 'numeric', minute: '2-digit' }).format(0).length > 0`},
		{"Intl.RelativeTimeFormat", `new Intl.RelativeTimeFormat('en').format(-5, 'minute') === '5 minutes ago'`},
		{"SharedArrayBuffer is gated", `typeof SharedArrayBuffer === 'function' || typeof SharedArrayBuffer === 'undefined'`},
		{"WebAssembly", `typeof WebAssembly === 'object' && typeof WebAssembly.instantiate === 'function'`},
		{"WebGL2", `!!document.createElement('canvas').getContext('webgl2')`},
		{"requestIdleCallback", `typeof requestIdleCallback === 'function'`},
		{"ResizeObserver, IntersectionObserver", `typeof ResizeObserver === 'function' && typeof IntersectionObserver === 'function'`},
	},
	"whatsapp": {
		{"WebSocket binary", `(() => { const w = new WebSocket('ws://127.0.0.1:1/'); const ok = w.binaryType === 'blob' && (w.binaryType = 'arraybuffer', w.binaryType === 'arraybuffer'); try { w.close(); } catch (e) {} return ok; })()`},
		{"crypto.subtle", `['digest', 'importKey', 'deriveBits', 'encrypt', 'sign', 'generateKey'].every(k => typeof crypto.subtle[k] === 'function')`},
		{"crypto.subtle X25519/ECDH", `crypto.subtle.generateKey({ name: 'ECDH', namedCurve: 'P-256' }, true, ['deriveBits']).then(k => !!k.privateKey)`},
		{"CompressionStream", `typeof CompressionStream === 'function' && typeof DecompressionStream === 'function'`},
		{"IndexedDB", `typeof indexedDB.open === 'function' && typeof IDBObjectStore.prototype.getAll === 'function' && typeof IDBObjectStore.prototype.getAllKeys === 'function' && typeof IDBObjectStore.prototype.openKeyCursor === 'function'`},
		{"Notification", `typeof Notification === 'function' && typeof Notification.requestPermission === 'function'`},
		{"ServiceWorker + Push", `typeof navigator.serviceWorker.register === 'function' && typeof PushManager === 'function'`},
		{"Clipboard", `typeof navigator.clipboard.readText === 'function' && typeof navigator.clipboard.write === 'function' && typeof ClipboardItem === 'function'`},
		{"Web Locks", `typeof navigator.locks.request === 'function'`},
		{"BroadcastChannel", `typeof BroadcastChannel === 'function'`},
		{"Worker, SharedWorker", `typeof Worker === 'function' && typeof SharedWorker === 'function'`},
		{"structuredClone", `(() => { const m = new Map([[1, new Set([2])]]); const c = structuredClone({ m, d: new Date(0), r: /x/g, b: new Uint8Array([1, 2]), e: new Error('x') }); return c.m.get(1).has(2) && c.d.getTime() === 0 && c.r.global && c.b[1] === 2 && c.e.message === 'x'; })()`},
		{"Blob/File/FileReader", `typeof Blob === 'function' && typeof File === 'function' && typeof FileReader === 'function'`},
		{"atob/btoa", `atob(btoa('abc')) === 'abc'`},
		{"TextEncoder", `new TextDecoder().decode(new TextEncoder().encode('é')) === 'é'`},
		{"Intl.Segmenter", `[...new Intl.Segmenter('en', { granularity: 'grapheme' }).segment('a👍🏽b')].length === 3`},
		{"Intl.PluralRules", `new Intl.PluralRules('en').select(1) === 'one'`},
		{"Intl.ListFormat", `new Intl.ListFormat('en').format(['a', 'b', 'c']) === 'a, b, and c'`},
		{"Intl.Collator", `['b', 'a'].sort(new Intl.Collator('en').compare).join('') === 'ab'`},
		{"navigator.storage.estimate", `typeof navigator.storage.estimate === 'function' && typeof navigator.storage.persist === 'function'`},
		{"getUserMedia audio", `typeof navigator.mediaDevices.getUserMedia === 'function'`},
		{"AudioContext decodeAudioData", `typeof AudioContext.prototype.decodeAudioData === 'function'`},
		{"requestIdleCallback", `typeof requestIdleCallback === 'function'`},
		{"Page visibility", `typeof document.hidden === 'boolean' && typeof document.visibilityState === 'string'`},
		{"BroadcastChannel postMessage", `typeof new BroadcastChannel('x').postMessage === 'function'`},
		{"File System Access (picker)", `typeof showOpenFilePicker === 'function'`},
		{"OPFS", `typeof navigator.storage.getDirectory === 'function'`},
	},
	"youtube": {
		{"MediaSource", `typeof MediaSource === 'function'`},
		{"ManagedMediaSource", `typeof ManagedMediaSource === 'function'`},
		{"isTypeSupported opus", `MediaSource.isTypeSupported('audio/webm; codecs="opus"')`},
		{"isTypeSupported vp8", `MediaSource.isTypeSupported('video/webm; codecs="vp8"')`},
		{"isTypeSupported answers a bogus type", `MediaSource.isTypeSupported('video/x-bogus') === false`},
		{"canPlayType", `typeof document.createElement('video').canPlayType('video/mp4') === 'string'`},
		{"MediaCapabilities", `typeof navigator.mediaCapabilities.decodingInfo === 'function'`},
		{"decodingInfo answers", `navigator.mediaCapabilities.decodingInfo({ type: 'media-source', video: { contentType: 'video/webm; codecs="vp8"', width: 640, height: 360, bitrate: 1e6, framerate: 30 } }).then(r => typeof r.supported === 'boolean' && typeof r.smooth === 'boolean')`},
		{"getVideoPlaybackQuality", `(() => { const q = document.createElement('video').getVideoPlaybackQuality(); return typeof q.totalVideoFrames === 'number' && typeof q.droppedVideoFrames === 'number'; })()`},
		{"Media Session", `typeof navigator.mediaSession.setActionHandler === 'function' && typeof MediaMetadata === 'function'`},
		{"fullscreen", `typeof document.documentElement.requestFullscreen === 'function' && typeof document.exitFullscreen === 'function'`},
		{"Picture-in-Picture", `'pictureInPictureEnabled' in document`},
		{"EME", `typeof navigator.requestMediaKeySystemAccess === 'function'`},
		{"Clear Key is available", `navigator.requestMediaKeySystemAccess('org.w3.clearkey', [{ initDataTypes: ['cenc', 'webm', 'keyids'], videoCapabilities: [{ contentType: 'video/webm; codecs="vp8"' }] }]).then(a => !!a)`},
		{"Widevine is not offered", `navigator.requestMediaKeySystemAccess('com.widevine.alpha', [{ initDataTypes: ['cenc'], videoCapabilities: [{ contentType: 'video/mp4; codecs="avc1.42E01E"' }] }]).then(() => false, () => true)`},
		{"autoplay policy", `typeof navigator.getAutoplayPolicy === 'function' || typeof document.createElement('video').play === 'function'`},
		{"requestVideoFrameCallback", `typeof HTMLVideoElement.prototype.requestVideoFrameCallback === 'function'`},
		{"Intl.NumberFormat compact", `new Intl.NumberFormat('en', { notation: 'compact' }).format(1234567) === '1.2M'`},
		{"Intl.DateTimeFormat relative", `new Intl.RelativeTimeFormat('en', { numeric: 'auto' }).format(-1, 'day') === 'yesterday'`},
		{"Intl.DisplayNames", `new Intl.DisplayNames('en', { type: 'language' }).of('fr') === 'French'`},
		{"Web Locks, BroadcastChannel", `typeof navigator.locks.request === 'function' && typeof BroadcastChannel === 'function'`},
		{"ResizeObserver", `typeof ResizeObserver === 'function'`},
		{"CSS.supports", `typeof CSS.supports === 'function'`},
		{"IntersectionObserver", `typeof IntersectionObserver === 'function'`},
		{"WebAssembly", `typeof WebAssembly === 'object'`},
		{"userAgentData is truthful", `navigator.userAgentData === undefined || !navigator.userAgentData.brands.some(b => /Chrom(e|ium)/.test(b.brand))`},
	},
}

// known absent or stubbed: documented in docs/compat.md and docs/web-platform.md
var compatAbsent = map[string]string{}

func TestCompatSiteFeatureDetection(t *testing.T) {
	tab := platformTab(t)
	for site, checks := range compatChecks {
		site, checks := site, checks
		t.Run(site, func(t *testing.T) {
			var cases []jsCase
			for _, c := range checks {
				cases = append(cases, jsCase{c[0], "return !!(await (" + c[1] + "));", "true"})
			}
			runJSCases(t, tab, cases)
		})
	}
	t.Run("documented gaps", func(t *testing.T) {
		var names []string
		for n := range compatAbsent {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			r, err := tab.Evaluate("(" + compatAbsent[n] + ")")
			if err != nil {
				t.Fatalf("%s: %v", n, err)
			}
			var got string
			_ = json.Unmarshal(r, &got)
			if !strings.Contains(got, "NotSupportedError") {
				t.Errorf("%s is documented as absent but gave %q: update docs/compat.md and docs/web-platform.md", n, got)
			}
		}
	})
}
