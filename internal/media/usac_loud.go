package media

import (
	"math"
	"sync/atomic"
)

// Loudness normalisation with the loudnessInfoSet of the stream
// (ISO/IEC 23003-4): the gain brings the programme loudness to a target
// loudness. It is off by default (like FFmpeg, which ignores the metadata);
// SetUSACTargetLoudness enables it.

var usacTargetLoud atomic.Uint64 // math.Float64bits of the target in LKFS; 0 bits = off

// SetUSACTargetLoudness sets the loudness (in LKFS, e.g. -16 or -24) that
// USAC / xHE-AAC streams carrying loudness metadata are normalised to. A
// value of 0 disables the normalisation (the default).
func SetUSACTargetLoudness(lkfs float64) {
	if lkfs >= 0 {
		usacTargetLoud.Store(0)
		return
	}
	usacTargetLoud.Store(math.Float64bits(lkfs))
}

// usacLoudnessGain returns the linear gain that moves the stream to the
// target loudness, or 1 when there is no usable loudness information.
func usacLoudnessGain(cfg *usacConfig, target float64) float64 {
	if target >= 0 || cfg == nil {
		return 1
	}
	// downmixId 0 and drcSetId 0 describe the decoded signal itself; prefer
	// the programme loudness (method 1), then the anchor loudness (2)
	best, bestRank := math.NaN(), 99
	for _, li := range cfg.loudness {
		if li.drcSetID != 0 || li.downmixID != 0 || li.eqSetID != 0 {
			continue
		}
		for i := 0; i < li.nMeas; i++ {
			rank := 50
			switch li.methodDef[i] {
			case 1:
				rank = 0
			case 2:
				rank = 1
			case 0:
				rank = 2
			default:
				continue
			}
			if rank < bestRank {
				best, bestRank = li.methodVal[i], rank
			}
		}
	}
	if math.IsNaN(best) {
		return 1
	}
	return math.Pow(10, (target-best)/20)
}
