//go:build optrace

package engine_test

import "github.com/sweefbrowser/sweef/internal/raster"

func init() { traceDump, traceReset = raster.OpStatDump, raster.OpStatReset }
