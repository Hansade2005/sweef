package constraints

import (
	"encoding/json"
	"errors"
	"testing"
)

func parse(t *testing.T, js string) Constraints {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(js), &v); err != nil {
		t.Fatal(err)
	}
	c, err := Parse(v)
	if err != nil {
		t.Fatalf("parse %s: %v", js, err)
	}
	return c
}

var webcam = VideoDevice{ID: "cam-a", GroupID: "g1", Modes: []Mode{
	{160, 120, 1, 30}, {320, 240, 1, 30}, {640, 360, 1, 30}, {640, 480, 1, 30}, {1280, 720, 1, 30}, {1920, 1080, 1, 15},
}}
var cam2 = VideoDevice{ID: "cam-b", GroupID: "g2", Facing: "environment", Modes: []Mode{
	{640, 480, 5, 60}, {1280, 720, 5, 60}, {1920, 1080, 5, 30},
}}

func TestSolveVideoTable(t *testing.T) {
	cases := []struct {
		name         string
		js           string
		devs         []VideoDevice
		dev, w, h    int
		fps          float64
		overconstrai string // expected failing constraint, "" for success
	}{
		{"default is 640x480@30", `true`, []VideoDevice{webcam}, 0, 640, 480, 30, ""},
		{"ideal size", `{"width":1280,"height":720}`, []VideoDevice{webcam}, 0, 1280, 720, 30, ""},
		{"ideal larger than max picks the largest", `{"width":{"ideal":4000},"height":{"ideal":3000}}`, []VideoDevice{webcam}, 0, 1920, 1080, 15, ""},
		{"min width forces up", `{"width":{"min":1000}}`, []VideoDevice{webcam}, 0, 1280, 720, 30, ""},
		{"max height bounds", `{"height":{"max":300,"ideal":1000}}`, []VideoDevice{webcam}, 0, 320, 240, 30, ""},
		{"exact size", `{"width":{"exact":320},"height":{"exact":240}}`, []VideoDevice{webcam}, 0, 320, 240, 30, ""},
		{"exact missing width", `{"width":{"exact":1000}}`, []VideoDevice{webcam}, 0, 0, 0, 0, "width"},
		{"exact missing height", `{"width":640,"height":{"exact":999}}`, []VideoDevice{webcam}, 0, 0, 0, 0, "height"},
		{"fps ideal inside range", `{"frameRate":{"ideal":24}}`, []VideoDevice{webcam}, 0, 640, 480, 24, ""},
		{"fps ideal beyond range clamps", `{"frameRate":60}`, []VideoDevice{webcam}, 0, 640, 480, 30, ""},
		{"fps min above everything", `{"frameRate":{"min":120}}`, []VideoDevice{webcam}, 0, 0, 0, 0, "frameRate"},
		{"fps min forces a different mode", `{"width":1920,"frameRate":{"min":20}}`, []VideoDevice{webcam}, 0, 1280, 720, 30, ""},
		{"aspect ratio 16:9 (two modes fit, the one nearer the 640x480 default wins)", `{"aspectRatio":{"exact":1.7777777777777777}}`, []VideoDevice{webcam}, 0, 640, 360, 30, ""},
		{"aspect ratio 16:9 with a size ideal", `{"aspectRatio":{"exact":1.7777777777777777},"width":1280}`, []VideoDevice{webcam}, 0, 1280, 720, 30, ""},
		{"aspect ratio 4:3 ideal", `{"aspectRatio":1.3333333333}`, []VideoDevice{webcam}, 0, 640, 480, 30, ""},
		{"aspect ratio impossible", `{"aspectRatio":{"exact":3}}`, []VideoDevice{webcam}, 0, 0, 0, 0, "aspectRatio"},
		{"device id", `{"deviceId":{"exact":"cam-b"},"width":1280}`, []VideoDevice{webcam, cam2}, 1, 1280, 720, 30, ""},
		{"device id ideal that does not exist", `{"deviceId":"nope","width":1280}`, []VideoDevice{webcam, cam2}, 0, 1280, 720, 30, ""},
		{"device id exact that does not exist", `{"deviceId":{"exact":"nope"}}`, []VideoDevice{webcam}, 0, 0, 0, 0, "deviceId"},
		{"group id", `{"groupId":{"exact":"g2"}}`, []VideoDevice{webcam, cam2}, 1, 640, 480, 30, ""},
		{"facing environment picks the rear camera", `{"facingMode":"environment"}`, []VideoDevice{webcam, cam2}, 1, 640, 480, 30, ""},
		{"facing exact user picks the unknown-facing webcam", `{"facingMode":{"exact":"user"}}`, []VideoDevice{cam2, webcam}, 1, 640, 480, 30, ""},
		{"facing exact environment with only a webcam", `{"facingMode":{"exact":"environment"}}`, []VideoDevice{webcam}, 0, 0, 0, 0, "facingMode"},
		{"facing list", `{"facingMode":{"exact":["left","environment"]}}`, []VideoDevice{webcam, cam2}, 1, 640, 480, 30, ""},
		{"advanced set is applied when satisfiable", `{"advanced":[{"width":{"exact":1280}}]}`, []VideoDevice{webcam}, 0, 1280, 720, 30, ""},
		{"ideal inside an advanced set does not filter", `{"advanced":[{"width":1280}]}`, []VideoDevice{webcam}, 0, 640, 480, 30, ""},
		{"advanced set that cannot be met is ignored", `{"advanced":[{"width":{"exact":99}},{"height":{"exact":1080}}]}`, []VideoDevice{webcam}, 0, 1920, 1080, 15, ""},
		{"first advanced wins over the second", `{"advanced":[{"width":{"exact":320}},{"width":{"exact":1280}}]}`, []VideoDevice{webcam}, 0, 320, 240, 30, ""},
		{"basic hard constraint beats advanced", `{"width":{"min":1280},"advanced":[{"width":{"exact":320}}]}`, []VideoDevice{webcam}, 0, 1280, 720, 30, ""},
		{"both devices tie: first wins", `{"width":640,"height":480}`, []VideoDevice{webcam, cam2}, 0, 640, 480, 30, ""},
		{"better fit on the second device", `{"width":1920,"height":1080,"frameRate":30}`, []VideoDevice{webcam, cam2}, 1, 1920, 1080, 30, ""},
		{"no devices", `true`, nil, 0, 0, 0, 0, "deviceId"},
		{"resizeMode none is what we deliver", `{"resizeMode":{"exact":"none"}}`, []VideoDevice{webcam}, 0, 640, 480, 30, ""},
		{"resizeMode crop-and-scale is not offered", `{"resizeMode":{"exact":"crop-and-scale"}}`, []VideoDevice{webcam}, 0, 0, 0, 0, "resizeMode"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := parse(t, tc.js)
			r, err := SolveVideo(c, tc.devs)
			if tc.overconstrai != "" {
				var oc *Overconstrained
				if !errors.As(err, &oc) || oc.Constraint != tc.overconstrai {
					t.Fatalf("got %+v, %v; want overconstrained %q", r, err, tc.overconstrai)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if r.Device != tc.dev || r.W != tc.w || r.H != tc.h || r.FPS != tc.fps {
				t.Fatalf("got dev=%d %dx%d@%v, want dev=%d %dx%d@%v", r.Device, r.W, r.H, r.FPS, tc.dev, tc.w, tc.h, tc.fps)
			}
		})
	}
}

func TestParseErrors(t *testing.T) {
	for _, js := range []string{`"x"`, `{"width":{"min":"abc"}}`, `{"advanced":3}`, `{"advanced":[1]}`, `{"facingMode":{"exact":5}}`, `{"echoCancellation":"yes"}`, `{"width":[1]}`} {
		var v any
		_ = json.Unmarshal([]byte(js), &v)
		if _, err := Parse(v); err == nil {
			t.Errorf("%s: expected a TypeError", js)
		} else if _, ok := err.(*TypeError); !ok {
			t.Errorf("%s: %T", js, err)
		}
	}
	// unknown keys and numeric strings are fine
	if c := parse(t, `{"bogus":1,"width":"640"}`); c.Width.Ideal == nil || *c.Width.Ideal != 640 {
		t.Fatalf("numeric string: %+v", c.Width)
	}
}

var mic = AudioDevice{ID: "mic-a", GroupID: "g1", Rates: []int{44100}, MinChannels: 1, MaxChannels: 2, DefaultRate: 44100, DefaultChans: 1}
var mic2 = AudioDevice{ID: "mic-b", GroupID: "g2", Rates: []int{48000}, MinChannels: 1, MaxChannels: 1, DefaultRate: 48000, DefaultChans: 1}

func TestSolveAudioTable(t *testing.T) {
	cases := []struct {
		name  string
		js    string
		devs  []AudioDevice
		dev   int
		rate  int
		chans int
		oc    string
	}{
		{"default", `true`, []AudioDevice{mic}, 0, 44100, 1, ""},
		{"ideal rate prefers the matching device", `{"sampleRate":48000}`, []AudioDevice{mic, mic2}, 1, 48000, 1, ""},
		{"exact rate unavailable", `{"sampleRate":{"exact":16000}}`, []AudioDevice{mic}, 0, 0, 0, "sampleRate"},
		{"channel count", `{"channelCount":2}`, []AudioDevice{mic}, 0, 44100, 2, ""},
		{"channel count exact beyond the device", `{"channelCount":{"exact":6}}`, []AudioDevice{mic}, 0, 0, 0, "channelCount"},
		{"echo cancellation ideal is accepted", `{"echoCancellation":true,"noiseSuppression":true,"autoGainControl":true}`, []AudioDevice{mic}, 0, 44100, 1, ""},
		{"echo cancellation exact true is overconstrained", `{"echoCancellation":{"exact":true}}`, []AudioDevice{mic}, 0, 0, 0, "echoCancellation"},
		{"echo cancellation exact false", `{"echoCancellation":{"exact":false}}`, []AudioDevice{mic}, 0, 44100, 1, ""},
		{"device id", `{"deviceId":{"exact":"mic-b"}}`, []AudioDevice{mic, mic2}, 1, 48000, 1, ""},
		{"device id missing", `{"deviceId":{"exact":"zzz"}}`, []AudioDevice{mic}, 0, 0, 0, "deviceId"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, err := SolveAudio(parse(t, tc.js), tc.devs)
			if tc.oc != "" {
				var oc *Overconstrained
				if !errors.As(err, &oc) || oc.Constraint != tc.oc {
					t.Fatalf("got %+v %v, want overconstrained %q", r, err, tc.oc)
				}
				return
			}
			if err != nil || r.Device != tc.dev || r.Rate != tc.rate || r.Channels != tc.chans {
				t.Fatalf("got %+v %v, want dev=%d rate=%d ch=%d", r, err, tc.dev, tc.rate, tc.chans)
			}
		})
	}
}

func TestSolveDisplay(t *testing.T) {
	cases := []struct {
		js     string
		w, h   int
		ow, oh int
		fps    float64
	}{
		{`true`, 1280, 720, 1280, 720, 30},
		{`{"width":{"max":640},"height":{"max":640}}`, 1280, 720, 640, 360, 30},
		{`{"width":{"ideal":1920}}`, 1280, 720, 1280, 720, 30}, // never scaled up
		{`{"height":{"ideal":360},"frameRate":{"max":10}}`, 1280, 720, 640, 360, 10},
		{`{"frameRate":15}`, 801, 601, 800, 600, 15},
	}
	for _, tc := range cases {
		r, err := SolveDisplay(parse(t, tc.js), tc.w, tc.h)
		if err != nil || r.W != tc.ow || r.H != tc.oh || r.FPS != tc.fps {
			t.Errorf("%s on %dx%d: got %+v %v want %dx%d@%v", tc.js, tc.w, tc.h, r, err, tc.ow, tc.oh, tc.fps)
		}
	}
	if _, err := SolveDisplay(parse(t, `{"frameRate":{"min":200}}`), 100, 100); err == nil {
		t.Error("frame rate min 200 should be overconstrained")
	}
}
