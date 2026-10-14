//go:build !darwin

package fairplay

import (
	"context"

	"github.com/sweefbrowser/sweef/internal/media/cdm"
)

type unsupported struct{}

func newProvider(o Options) cdm.Provider { return unsupported{} }

func (unsupported) KeySystem() string { return KeySystem }
func (unsupported) Availability() cdm.Availability {
	return cdm.Availability{Reason: "FairPlay is only available through AVFoundation on macOS"}
}
func (unsupported) Capabilities() cdm.Capabilities { return cdm.Capabilities{} }
func (unsupported) New(ctx context.Context, cfg cdm.InstanceConfig) (cdm.Backend, error) {
	return nil, cdm.ErrUnsupported
}
