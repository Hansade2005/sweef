package av1

import "sync/atomic"

// Coverage counters of coding tools (used by tests to check that fixtures
// exercise the tool they are meant to cover).
const (
	covPalette = iota
	covPaletteUV
	covIntrabc
	covCfl
	covFilterIntra
	covDirectional
	covEdgeUpsample
	covSmooth
	covPaeth
	covOBMC
	covLocalWarp
	covGlobalWarp
	covWedge
	covDiffwtd
	covDistance
	covInterintra
	covCompoundAvg
	covScaledRef
	covSkipMode
	covSegmentation
	covDeltaQ
	covDeltaLF
	covQmatrix
	covTx64
	covIdtx
	covLRWiener
	covLRSgr
	covSuperres
	covFilmGrain
	covCdef
	covLoopFilter
	covMfmv
	covTiles
	covInterpDual
	covLossless
	covIntraEdgeFilter
	covRefMvTemporal
	covMax
)

var covNames = [covMax]string{"palette", "paletteUV", "intrabc", "cfl", "filterIntra", "directional", "edgeUpsample",
	"smooth", "paeth", "obmc", "localWarp", "globalWarp", "wedge", "diffwtd", "distance", "interintra", "compoundAvg",
	"scaledRef", "skipMode", "segmentation", "deltaQ", "deltaLF", "qmatrix", "tx64", "idtx", "lrWiener", "lrSgr",
	"superres", "filmGrain", "cdef", "loopFilter", "mfmv", "tiles", "interpDual", "lossless", "intraEdgeFilter", "refMvTemporal"}

var coverage [covMax]int32

func cov(i int) { atomic.AddInt32(&coverage[i], 1) }

// resetCoverage zeroes the coverage counters (tests).
func resetCoverage() {
	for i := range coverage {
		atomic.StoreInt32(&coverage[i], 0)
	}
}
