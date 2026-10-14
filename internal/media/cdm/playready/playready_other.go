//go:build !windows

package playready

import (
	"context"

	"github.com/sweefbrowser/sweef/internal/media/cdm"
)

type unsupported struct{ ks string }

func newProvider(ks string, o Options) cdm.Provider { return unsupported{ks} }

func (u unsupported) KeySystem() string { return u.ks }
func (unsupported) Availability() cdm.Availability {
	return cdm.Availability{Reason: "PlayReady is only available through Media Foundation on Windows"}
}
func (unsupported) Capabilities() cdm.Capabilities { return cdm.Capabilities{} }
func (unsupported) New(ctx context.Context, cfg cdm.InstanceConfig) (cdm.Backend, error) {
	return nil, cdm.ErrUnsupported
}
