package policy

import "testing"

const top = "https://app.example"

func TestHeader(t *testing.T) {
	cases := []struct {
		hdr     string
		feature string
		origin  string
		want    bool
	}{
		{"", Camera, top, true},
		{"camera=()", Camera, top, false},
		{"camera=()", Microphone, top, true},
		{"camera=(self)", Camera, top, true},
		{"camera=*", Camera, top, true},
		{`camera=("https://other.example")`, Camera, top, false},
		{`camera=(self "https://other.example")`, Camera, "https://other.example", true},
		{"camera=(), microphone=(self)", Microphone, top, true},
		{"camera 'none'; microphone 'self'", Camera, top, false},
		{"camera 'none'; microphone 'self'", Microphone, top, true},
		{"display-capture=()", DisplayCapture, top, false},
		{`geolocation=(self), camera=(self "https://a.example" "https://b.example")`, Camera, top, true},
	}
	for _, c := range cases {
		if got := ParseHeader(c.hdr).Allowed(c.feature, c.origin); got != c.want {
			t.Errorf("%q %s @ %s: got %v want %v", c.hdr, c.feature, c.origin, got, c.want)
		}
	}
}

func TestFrame(t *testing.T) {
	frame := "https://meet.example"
	none := ParseHeader("")
	cases := []struct {
		name   string
		parent string
		attr   string
		frame  string
		want   bool
	}{
		{"no allow attribute", "", "", "", false},
		{"bare name delegates to src", "", "camera", "", true},
		{"other feature only", "", "microphone", "", false},
		{"explicit origin", "", "camera https://meet.example", "", true},
		{"wrong origin", "", "camera https://evil.example", "", false},
		{"star", "", "camera *", "", true},
		{"none", "", "camera 'none'", "", false},
		{"parent header closes the feature", "camera=()", "camera", "", false},
		{"parent header opens it to the frame", `camera=(self "https://meet.example")`, "camera", "", true},
		{"parent header names someone else", `camera=(self "https://x.example")`, "camera", "", false},
		{"frame's own header closes it", "", "camera", "camera=()", false},
		{"semicolon list", "", "microphone; camera; display-capture", "", true},
	}
	for _, c := range cases {
		got := FrameAllowed(Camera, ParseHeader(c.parent), top, ParseAllowAttr(c.attr), frame, frame, ParseHeader(c.frame))
		if got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
	if !FrameAllowed(Camera, none, top, ParseAllowAttr(""), top, top, none) {
		t.Error("same-origin frame inherits")
	}
	if FrameAllowed(Camera, ParseHeader("camera=()"), top, ParseAllowAttr("camera"), top, top, none) {
		t.Error("same-origin frame under a closed parent")
	}
}
