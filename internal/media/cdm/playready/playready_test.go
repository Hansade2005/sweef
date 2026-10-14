package playready

import (
	"context"
	"errors"
	"runtime"
	"testing"

	"github.com/sweefbrowser/sweef/internal/media/cdm"
)

func TestProvidersAreOffUnlessEnabled(t *testing.T) {
	t.Setenv(EnableEnv, "")
	ps := NewProviders(Options{})
	if len(ps) != 2 || ps[0].KeySystem() != KeySystem || ps[1].KeySystem() != KeySystemRecommendation {
		t.Fatalf("providers: %v", ps)
	}
	for _, p := range ps {
		av := p.Availability()
		if av.Supported || av.Reason == "" {
			t.Errorf("%s: %+v", p.KeySystem(), av)
		}
		if _, err := p.New(context.Background(), cdm.InstanceConfig{}); !errors.Is(err, cdm.ErrUnsupported) {
			t.Errorf("%s: %v", p.KeySystem(), err)
		}
	}
	if runtime.GOOS != "windows" {
		for _, p := range NewProviders(Options{Enable: true}) {
			if p.Availability().Supported {
				t.Errorf("%s supported off Windows", p.KeySystem())
			}
		}
	}
}
