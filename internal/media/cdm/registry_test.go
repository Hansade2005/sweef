package cdm

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

type fakeProvider struct {
	ks    string
	av    Availability
	caps  Capabilities
	newFn func(InstanceConfig) (Backend, error)
}

func (f *fakeProvider) KeySystem() string          { return f.ks }
func (f *fakeProvider) Availability() Availability { return f.av }
func (f *fakeProvider) Capabilities() Capabilities { return f.caps }
func (f *fakeProvider) New(ctx context.Context, cfg InstanceConfig) (Backend, error) {
	return f.newFn(cfg)
}

func widevineLikeCaps() Capabilities {
	return Capabilities{
		InitDataTypes:         []string{"cenc", "keyids"},
		SessionTypes:          []SessionType{SessionTemporary, SessionPersistentLicense},
		VideoCodecs:           []string{"avc1", "vp9"},
		AudioCodecs:           []string{"mp4a", "opus"},
		Containers:            []string{"video/mp4", "audio/mp4", "video/webm", "audio/webm"},
		Schemes:               []Scheme{SchemeCenc, SchemeCbcs},
		Robustness:            []RobustnessLevel{{Name: ""}, {Name: "SW_SECURE_CRYPTO", MaxHeight: 720}, {Name: "HW_SECURE_ALL", Hardware: true}},
		DistinctiveIdentifier: NotAllowed,
		PersistentState:       Optional,
	}
}

func TestRegistryLookupReportAndAliases(t *testing.T) {
	r := NewDefaultRegistry()
	r.Register(&fakeProvider{ks: "com.example.drm", av: Availability{Supported: false, Reason: "no CDM"}})
	r.Register(&fakeProvider{ks: "com.apple.fps", av: Availability{Supported: true}, caps: widevineLikeCaps()})
	r.RegisterAlias("com.apple.fps.1_0", "com.apple.fps")

	if p, ok := r.Lookup("com.apple.fps.1_0"); !ok || p.KeySystem() != "com.apple.fps" {
		t.Fatal("alias did not resolve")
	}
	if _, ok := r.Lookup("nope"); ok {
		t.Fatal("unknown key system found")
	}
	if got, want := r.KeySystems(), []string{"com.apple.fps", "org.w3.clearkey"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("KeySystems %v, want %v (unavailable ones are hidden)", got, want)
	}
	rep := r.Report()
	if len(rep) != 3 || rep[0].KeySystem != "com.apple.fps" || rep[1].KeySystem != "com.example.drm" || rep[1].Availability.Supported {
		t.Fatalf("report %+v", rep)
	}
	if rep[1].Availability.Reason != "no CDM" {
		t.Fatal("reason lost")
	}
	_, err := r.Negotiate("com.example.drm", []KeySystemConfig{{}})
	if err == nil || !strings.Contains(err.Error(), "no CDM") {
		t.Fatalf("unavailable key system must say why: %v", err)
	}
	if _, err := r.Negotiate("nope", []KeySystemConfig{{}}); err == nil {
		t.Fatal("unregistered key system")
	}
}

func mc(ct, rob string) MediaCapability { return MediaCapability{ContentType: ct, Robustness: rob} }

func TestNegotiateConfigurations(t *testing.T) {
	r := NewRegistry()
	r.Register(&fakeProvider{ks: "com.widevine.alpha", av: Availability{Supported: true}, caps: widevineLikeCaps()})

	// First candidate asks for HEVC (unsupported): the second one wins, and unsupported capabilities are filtered.
	acc, err := r.Negotiate("com.widevine.alpha", []KeySystemConfig{
		{VideoCapabilities: []MediaCapability{mc(`video/mp4; codecs="hvc1.1.6.L93.B0"`, "")}},
		{
			InitDataTypes: []string{"webm", "cenc"},
			AudioCapabilities: []MediaCapability{
				mc(`audio/mp4; codecs="mp4a.40.2"`, ""),
				mc(`audio/mp4; codecs="ec-3"`, ""), // unsupported codec: dropped
			},
			VideoCapabilities: []MediaCapability{
				mc(`video/mp4; codecs="avc1.64001f"`, "HW_SECURE_ALL"),
				mc(`video/mp4; codecs="avc1.64001f"`, "SW_SECURE_CRYPTO"),
				mc(`video/webm; codecs="vp9"`, "UNKNOWN_LEVEL"), // unknown robustness: dropped
			},
			PersistentState: Optional,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(acc.Config.InitDataTypes, []string{"cenc"}) {
		t.Fatalf("init data types %v", acc.Config.InitDataTypes)
	}
	if len(acc.Config.AudioCapabilities) != 1 || len(acc.Config.VideoCapabilities) != 2 {
		t.Fatalf("filtered capabilities: audio %d video %d", len(acc.Config.AudioCapabilities), len(acc.Config.VideoCapabilities))
	}
	if !acc.HardwareSecure {
		t.Fatal("an accepted HW_SECURE_ALL capability must set HardwareSecure")
	}
	if !reflect.DeepEqual(acc.Config.SessionTypes, []SessionType{SessionTemporary}) {
		t.Fatalf("session types default to temporary: %v", acc.Config.SessionTypes)
	}
	if acc.Config.DistinctiveIdentifier != NotAllowed {
		t.Fatalf("distinctive identifier %q", acc.Config.DistinctiveIdentifier)
	}
	ic := acc.NewInstanceConfig("https://example.com", "/tmp/x")
	if !ic.HardwareSecure || ic.Origin != "https://example.com" || ic.DistinctiveIdentifierAllowed {
		t.Fatalf("instance config %+v", ic)
	}

	reject := func(name string, c KeySystemConfig) {
		t.Helper()
		if _, err := r.Negotiate("com.widevine.alpha", []KeySystemConfig{c}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	vid := []MediaCapability{mc(`video/mp4; codecs="avc1.64001f"`, "")}
	reject("no capabilities at all", KeySystemConfig{})
	reject("only unsupported init data", KeySystemConfig{InitDataTypes: []string{"webm"}, VideoCapabilities: vid})
	reject("distinctive identifier required", KeySystemConfig{DistinctiveIdentifier: Required, VideoCapabilities: vid})
	reject("persistent session without persistent state", KeySystemConfig{PersistentState: NotAllowed, SessionTypes: []SessionType{SessionPersistentLicense}, VideoCapabilities: vid})
	reject("unknown session type", KeySystemConfig{SessionTypes: []SessionType{"persistent-usage-record"}, VideoCapabilities: vid})
	reject("scheme the CDM lacks", KeySystemConfig{VideoCapabilities: []MediaCapability{{ContentType: `video/mp4; codecs="avc1.64001f"`, EncryptionScheme: "cens"}}})
	reject("audio mime in video list", KeySystemConfig{VideoCapabilities: []MediaCapability{mc(`audio/mp4; codecs="mp4a.40.2"`, "")}})
	reject("garbage content type", KeySystemConfig{VideoCapabilities: []MediaCapability{mc(`;;`, "")}})

	if _, err := r.Negotiate("com.widevine.alpha", []KeySystemConfig{{PersistentState: Required, SessionTypes: []SessionType{SessionTemporary, SessionPersistentLicense}, VideoCapabilities: []MediaCapability{{ContentType: `video/mp4; codecs="avc1.64001f"`, EncryptionScheme: "cbcs-1-9"}}}}); err != nil {
		t.Fatalf("persistent license with cbcs-1-9: %v", err)
	}
}

func TestResolveRequirement(t *testing.T) {
	cases := []struct {
		asked, needs, want Requirement
		ok                 bool
	}{
		{"", "", NotAllowed, true},
		{Optional, NotAllowed, NotAllowed, true},
		{Required, NotAllowed, "", false},
		{NotAllowed, Required, "", false},
		{Optional, Required, Required, true},
		{Required, Required, Required, true},
		{Optional, Optional, NotAllowed, true}, // not needed: do not ask the user
		{Required, Optional, Required, true},
		{NotAllowed, Optional, NotAllowed, true},
	}
	for _, c := range cases {
		got, ok := resolveRequirement(c.asked, c.needs)
		if got != c.want || ok != c.ok {
			t.Errorf("resolve(%q,%q) = %q,%v want %q,%v", c.asked, c.needs, got, ok, c.want, c.ok)
		}
	}
}

func TestCodecFamilyAndContentType(t *testing.T) {
	for in, want := range map[string]string{
		"avc1.64001f": "video/avc1", "avc3.42E01E": "video/avc1", "hvc1.1.6.L93.B0": "video/hevc", "hev1.2.4.L120.90": "video/hevc",
		"vp09.00.10.08": "video/vp9", "vp9": "video/vp9", "vp8": "video/vp8", "av01.0.04M.08": "video/av01",
		"mp4a.40.2": "audio/mp4a", "opus": "audio/opus", "ec-3": "audio/ec-3", "AC-3": "audio/ac-3", "flac": "audio/flac", "weird": "/",
	} {
		k, f := CodecFamily(in)
		if k+"/"+f != want {
			t.Errorf("CodecFamily(%q) = %s/%s, want %s", in, k, f, want)
		}
	}
	ct, err := ParseContentType(`Video/MP4; codecs="avc1.64001f, mp4a.40.2"`)
	if err != nil || ct.MIME != "video/mp4" || !reflect.DeepEqual(ct.Codecs, []string{"avc1.64001f", "mp4a.40.2"}) {
		t.Fatalf("%+v %v", ct, err)
	}
	caps := widevineLikeCaps()
	if caps.MaxHeight("SW_SECURE_CRYPTO") != 720 || caps.MaxHeight("") != 0 || !caps.SupportsPersistentLicense() {
		t.Fatal("capability helpers")
	}
}

func TestClearKeyCapabilitiesNegotiate(t *testing.T) {
	r := NewDefaultRegistry()
	acc, err := r.Negotiate(ClearKeySystem, []KeySystemConfig{{
		InitDataTypes:     []string{"keyids", "cenc"},
		VideoCapabilities: []MediaCapability{mc(`video/mp4; codecs="avc1.4d401e"`, ""), mc(`video/webm; codecs="vp9"`, "")},
		AudioCapabilities: []MediaCapability{mc(`audio/mp4; codecs="mp4a.40.2"`, "")},
	}})
	if err != nil {
		t.Fatal(err)
	}
	b, err := acc.Provider.New(context.Background(), acc.NewInstanceConfig("https://a.example", ""))
	if err != nil || b.KeySystem() != ClearKeySystem {
		t.Fatalf("%v", err)
	}
	b.Close()
	if _, err := r.Negotiate(ClearKeySystem, []KeySystemConfig{{VideoCapabilities: []MediaCapability{mc(`video/mp4; codecs="avc1.4d401e"`, "HW_SECURE_ALL")}}}); err == nil {
		t.Fatal("Clear Key has no hardware robustness")
	}
	var ce *Error
	if !errors.As(Errorf(ExceptionNotSupportedError, "x %d", 1), &ce) || ce.Error() != "NotSupportedError: x 1" {
		t.Fatal("Error formatting")
	}
}
