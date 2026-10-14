package engine

import "testing"

// MediaCapabilities answers for 'webrtc' follow what a peer connection negotiates: VP8 and Opus, nothing else,
// and 'record' is unsupported because MediaRecorder does not exist.
func TestMediaCapabilitiesWebRTC(t *testing.T) {
	tab := newMediaTab(t)
	type info struct {
		Supported, Smooth, PowerEfficient bool
	}
	get := func(call string) info {
		t.Helper()
		var r info
		evalJSON(t, tab, `navigator.mediaCapabilities.`+call+`.then(function(x){return {Supported:x.supported,Smooth:x.smooth,PowerEfficient:x.powerEfficient}})`, &r)
		return r
	}
	if r := get(`decodingInfo({type:'webrtc', video:{contentType:'video/VP8', width:640, height:360, framerate:30, bitrate:500000}, audio:{contentType:'audio/opus'}})`); !r.Supported || !r.Smooth || r.PowerEfficient {
		t.Errorf("VP8+Opus 360p30 decode: %+v", r)
	}
	if r := get(`decodingInfo({type:'webrtc', video:{contentType:'video/H264', width:640, height:360, framerate:30, bitrate:500000}})`); r.Supported {
		t.Errorf("H.264 is not negotiated: %+v", r)
	}
	if r := get(`decodingInfo({type:'webrtc', video:{contentType:'video/VP9', width:640, height:360, framerate:30, bitrate:500000}})`); r.Supported {
		t.Errorf("VP9 is not negotiated: %+v", r)
	}
	if r := get(`decodingInfo({type:'webrtc', video:{contentType:'video/VP8', width:1920, height:1080, framerate:60, bitrate:5000000}})`); !r.Supported || r.Smooth {
		t.Errorf("VP8 1080p60 decodes but not smoothly: %+v", r)
	}
	if r := get(`encodingInfo({type:'webrtc', video:{contentType:'video/VP8', width:640, height:360, framerate:30, bitrate:500000}, audio:{contentType:'audio/opus'}})`); !r.Supported || !r.Smooth || r.PowerEfficient {
		t.Errorf("VP8+Opus 360p30 encode: %+v", r)
	}
	if r := get(`encodingInfo({type:'webrtc', video:{contentType:'video/VP8', width:1920, height:1080, framerate:30, bitrate:4000000}})`); !r.Supported || r.Smooth {
		t.Errorf("VP8 1080p30 encode is supported but not smooth: %+v", r)
	}
	if r := get(`encodingInfo({type:'record', audio:{contentType:'audio/webm;codecs=opus'}})`); r.Supported {
		t.Errorf("record: %+v", r)
	}
	var name string
	evalJSON(t, tab, `navigator.mediaCapabilities.encodingInfo({type:'bogus', audio:{contentType:'audio/opus'}}).then(function(){return 'ok'},function(e){return e.name})`, &name)
	if name != "TypeError" {
		t.Errorf("bad encoding type: %q", name)
	}
}
