package fairplay

import (
	"context"
	"errors"
	"runtime"
	"testing"

	"github.com/sweefbrowser/sweef/internal/media/cdm"
)

func TestKeyURI(t *testing.T) {
	for _, c := range []struct {
		typ, data, want string
		fail            bool
	}{
		{"skd", "skd://assetid-1", "skd://assetid-1", false},
		{"skd", "skd://x\x00\x00", "skd://x", false},
		{"skd", "", "", true},
		{"sinf", "\x00\x01junk skd://abc123 tail", "skd://abc123", false},
		{"cenc", "no uri here", "", true},
		{"webm", "skd://x", "", true},
	} {
		got, err := keyURI(c.typ, []byte(c.data))
		if (err != nil) != c.fail || got != c.want {
			t.Errorf("keyURI(%s, %q) = %q, %v", c.typ, c.data, got, err)
		}
	}
}

func TestProviderIsOffUnlessEnabled(t *testing.T) {
	t.Setenv(EnableEnv, "")
	p := NewProvider(Options{})
	av := p.Availability()
	if av.Supported || av.Reason == "" {
		t.Fatalf("%+v", av)
	}
	if _, err := p.New(context.Background(), cdm.InstanceConfig{}); !errors.Is(err, cdm.ErrUnsupported) {
		t.Fatalf("%v", err)
	}
	if p.KeySystem() != KeySystem {
		t.Fatal("key system")
	}
	if runtime.GOOS != "darwin" {
		// even enabled, there is no AVFoundation off macOS
		if av := NewProvider(Options{Enable: true}).Availability(); av.Supported {
			t.Fatal("supported off macOS")
		}
	}
	caps := NewProvider(Options{Enable: true}).Capabilities()
	if runtime.GOOS == "darwin" && (caps.Mode != cdm.ModePlatformRender || !caps.Unverified) {
		t.Fatalf("%+v", caps)
	}
}
