//go:build !(amd64 || arm64)

package widevinehost

import (
	"errors"

	"github.com/sweefbrowser/sweef/internal/media/cdm"
)

// This package hosts 64-bit CDMs only (Widevine ships no 32-bit CDM for the platforms Sweef supports).

type Config struct {
	Library           string
	KeySystem         string
	InterfaceVersions []int
	Logf              func(format string, args ...any)
}

type Engine struct{ cdm.Engine }

// Open always fails on this architecture.
func Open(cfg Config) (*Engine, error) {
	return nil, errors.New("widevinehost: hosting a CDM needs a 64-bit amd64 or arm64 process")
}
