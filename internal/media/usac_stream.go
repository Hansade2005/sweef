package media

import (
	"errors"
	"math"
)

// decodeUSACStream decodes every access unit of a USAC (xHE-AAC) stream to
// planar PCM at the output sampling rate.
func decodeUSACStream(as *audioStream) (*PCM, error) {
	cfg, err := parseUsacASC(as.cfg)
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		return nil, errUSACUnsupported
	}
	d, err := newUSACDecoder(cfg)
	if err != nil {
		return nil, err
	}
	n := len(as.packets)
	for i := range d.pcm {
		d.pcm[i] = make([]float32, 0, n*d.outFrame)
	}
	for _, p := range as.packets {
		before := d.frames
		if err := d.decodeAU(p); err != nil {
			if errors.Is(err, ErrUnsupported) {
				return nil, err
			}
			if d.frames == 0 {
				return nil, err
			}
			// a damaged access unit becomes silence
			if d.frames == before {
				for c := range d.pcm {
					d.pcm[c] = append(d.pcm[c], make([]float32, d.outFrame)...)
				}
				d.frames++
			}
		}
	}
	if g := usacLoudnessGain(cfg, math.Float64frombits(usacTargetLoud.Load())); g != 1 {
		for _, ch := range d.pcm {
			for i, v := range ch {
				x := float32(float64(v) * g)
				if x > 1 {
					x = 1
				} else if x < -1 {
					x = -1
				}
				ch[i] = x
			}
		}
	}
	out := &PCM{SampleRate: cfg.rate, Data: d.pcm}
	lastUSACStats = d.stats
	skip := 0
	if as.skip > 0 {
		skip = as.skip
	}
	total := as.editDur
	if skip > 0 || total >= 0 {
		nf := out.Frames()
		if skip > nf {
			skip = nf
		}
		end := nf
		if total >= 0 && skip+total < nf {
			end = skip + total
		}
		for c := range out.Data {
			out.Data[c] = out.Data[c][skip:end]
		}
	}
	return out, nil
}
