package media

import (
	"fmt"
	"math"
	"os"
)

// Transitions between the FD and LPD cores: state resets, hand-over of the
// overlap data, forward aliasing cancellation for FD frames following an LPD
// frame, and the bass postfilter of the first FD frame. After libxaac's
// ixheaacd_lpc.c / ixheaacd_acelp_bitparse.c / ixheaacd_imdct.c
// (Apache-2.0, see NOTICE).

// lpdResetFromFD prepares the LPD decoder when the previous frame was an FD
// frame (ixheaacd_tw_buff_update: reset_acelp_data_fix + fix2flt_data).
func (d *usacDec) lpdResetFromFD(c *usacCh) {
	st := c.lpd
	n := d.cfg.coreLen
	wasShort := c.ics.seq == usacEightShort
	st.reset(wasShort)
	var fl int
	if wasShort {
		fl = n / 16
	} else {
		fl = n / 8
	}
	win := lpdSine(2 * fl)
	for i := 0; i < 2*fl; i++ {
		c.olap[n/2-fl+i] *= win[2*fl-1-i]
	}
	for i := 0; i < n/2-fl; i++ {
		c.olap[n/2+fl+i] = 0
	}
	st.lpFltPrev = [34]float64{}
	st.xcitPrev = [lpdMaxPitch + lpdInterOrder + 1]float64{}
	st.synthPrev = [lpdMaxPitch + lpdSynthDelay]float64{}
	st.bpfPrev = [lpdFilterDly + lpdSubfr]float64{}
	st.gainThreshold = 0
	lenSub := n / 4
	for i := 0; i < lenSub/2-fl; i++ {
		st.excPrev[i] = 0
	}
	for i := 0; i < 2*fl+1; i++ {
		st.excPrev[lenSub/2-fl+i] = c.olap[i+n/2-fl-1] * 32768
	}
}

// lpdUpdate hands the LPD state to the overlap buffer before an FD frame
// that follows an LPD frame (ixheaacd_lpd_dec_update).
func (d *usacDec) lpdUpdate(c *usacCh) {
	st := c.lpd
	n := d.cfg.coreLen
	numSub := n * 4 / lpdSuper
	sbf := 4 * numSub / 2
	delay := sbf * lpdSubfr
	by2 := sbf - 1
	synthDelay := by2 * lpdSubfr
	fl := n / 4 / 2
	for i := 0; i < lpdSubfr+synthDelay; i++ {
		c.olap[i] = st.synthPrev[lpdMaxPitch-lpdSubfr+i] / 32768
	}
	base := lpdSubfr + synthDelay - fl
	for k := 0; k < 2*fl; k++ {
		c.olap[base+k] = st.excPrev[k+1] / 32768
	}
	for i := 0; i < n-delay-fl; i++ {
		c.olap[delay+fl+i] = 0
	}
	if st.modePrev == 0 {
		copy(c.lpcPrev[:], st.lpFltPrev[lpdOrder+1:2*(lpdOrder+1)])
		copy(c.acelpIn[:], st.excPrev[:1+2*lpdFacLength])
	}
	c.ics.seq, c.ics.seqEff = usacEightShort, usacEightShort
	c.ics.kb = 0
	c.tdPrev = true
}

// lpdTDFrame combines the LPD synthesis with the overlap of the previous FD
// frame (ixheaacd_td_frm_dec).
func (d *usacDec) lpdTDFrame(c *usacCh, mod0 int, out []float64) {
	n := d.cfg.coreLen
	nshort := n / 8
	lfac := 0
	if !c.tdPrev {
		if c.ics.seq == usacEightShort {
			lfac = nshort / 2
		} else {
			lfac = nshort
		}
	}
	if !c.tdPrev && mod0 == 0 {
		for i := 0; i < n/2-lfac-lpdSubfr; i++ {
			out[i] = 0
		}
		for i := n/2 - lfac - lpdSubfr; i < n/2; i++ {
			c.olap[i] = 0
		}
	} else if !c.tdPrev {
		for i := 0; i < n/2-lfac; i++ {
			out[i] = 0
		}
		for i := n/2 - lfac; i < n; i++ {
			c.olap[i] = 0
		}
	}
	for i := 0; i < n; i++ {
		out[i] = out[i]/32768 + c.olap[i]
		c.olap[i] = 0
	}
}

// lpdBpfFix applies the bass postfilter to the first samples of an FD frame
// that follows an LPD frame (ixheaacd_lpd_bpf_fix).
func (d *usacDec) lpdBpfFix(c *usacCh, short bool, out []float64) error {
	st := c.lpd
	n := d.cfg.coreLen
	numSub := n * 4 / lpdSuper
	sbf := 4 * numSub / 2
	by2 := sbf - 1
	synthDelay := by2 * lpdSubfr
	buf := make([]float64, lpdMaxPitch+lpdSynthDelay+lpdSuper+lpdSubfr)
	copy(buf, st.synthPrev[:lpdMaxPitch+synthDelay])
	// the reference reads past the frame here; only samples inside it reach
	// the filtered part
	cnt := synthDelay + n + lpdSubfr
	if cnt > len(out) {
		cnt = len(out)
	}
	for i := 0; i < cnt; i++ {
		buf[lpdMaxPitch-lpdSubfr+i] = out[i] * 32768
	}
	var pitch [24]int
	var gain [24]float64
	for i := 0; i < by2; i++ {
		pitch[i] = st.pitchPrev[i]
		gain[i] = st.gainPrev[i]
	}
	for i := by2; i < sbf+3; i++ {
		pitch[i] = 64
		gain[i] = 0
	}
	if st.modePrev == 0 {
		pitch[by2] = pitch[by2-1]
		gain[by2] = gain[by2-1]
		if !short {
			pitch[by2+1] = pitch[by2]
			gain[by2+1] = gain[by2]
		}
	}
	sy := lpdMaxPitch
	for i := 0; i < by2+2; i++ {
		tp := pitch[i]
		if i*lpdSubfr+lpdMaxPitch < tp {
			return errUSACUnsupported
		}
		if gain[i] > 0 {
			var corr float64
			en := 1e-6
			for k := 0; k < lpdSubfr; k++ {
				a := buf[sy+i*lpdSubfr+k]
				b := buf[sy+i*lpdSubfr-tp+k]
				corr += a * b
				en += b * b
			}
			gain[i] = corr / en
		}
	}
	sig := make([]float64, lpdSuper+lpdSubfr*8)
	if err := lpdBassPostfilter(buf, sy, pitch[:], gain[:], sig, (sbf+2)*lpdSubfr+lpdSubfr, n-(sbf+4)*lpdSubfr, st.bpfPrev[:]); err != nil {
		return err
	}
	for i := 0; i < (sbf+2)*lpdSubfr+lpdSubfr; i++ {
		out[i] = sig[i] / 32768
	}
	return nil
}

// calFac computes the FAC contribution (2*lfac samples) of an FD frame after
// an LPD frame (ixheaacd_cal_fac_data).
func (d *usacDec) lpdCalFac(c *usacCh, lfac int, fac []float64) {
	st := c.lpd
	gain := math.Pow(10, float64(c.facData[0])/28)
	var x [lpdFacLength]float64
	// natural order (the reference permutes for its FFT based DCT-IV)
	for k := 0; k < lfac; k++ {
		x[k] = float64(c.facData[1+k])
	}
	y := make([]float64, 2*lfac)
	st.dct4(x[:lfac], y[:lfac])
	if os.Getenv("MODS") != "" {
		println("MYY0", float32(y[0]), float32(y[1]), float32(y[2]), float32(y[3]))
	}
	var ap [lpdOrder + 1]float64
	lpdWeightLPC(c.lpcPrev[:], ap[:], 0.92)
	var zero [lpdOrder]float64
	lpdSynthFilter(ap[:], y, y, zero[:])
	win := lpdSine(2 * lfac)
	n := d.cfg.coreLen
	l2 := n / 4 / 2
	for i := 0; i < lfac; i++ {
		fw1 := win[lfac+i] * win[lfac+i]
		fw1 = 1 - fw1
		fw0 := win[lfac-1-i] * win[2*lfac-1-(lfac-1-i)]
		t1 := c.acelpIn[1+l2+i] * fw1
		t2 := c.acelpIn[1+l2-1-i] * fw0
		fac[i] = (gain*y[i]*lpdFacHalf + t1 + t2) / 32768
		if os.Getenv("MODS") != "" && i < 3 {
			println("MYT", i, float32(t1), float32(t2), float32(y[i]))
		}
		fac[lfac+i] = gain * y[lfac+i] * lpdFacHalf / 32768
	}
	if os.Getenv("MODS") != "" {
		str := "MYCALF"
		for i := 0; i < 2*lfac; i++ {
			str += fmt.Sprintf(" %f", fac[i]*32768)
		}
		println(str)
	}
	if os.Getenv("MODS") != "" {
		println("MYCAL lfac", lfac, "gaincode", c.facData[0], "gain", float32(gain), "x", c.facData[1], c.facData[2], c.facData[3], "acelp_in", float32(c.acelpIn[0]), float32(c.acelpIn[1]), float32(c.acelpIn[2]), float32(c.acelpIn[3]), "lpc", float32(c.lpcPrev[0]), float32(c.lpcPrev[1]), float32(c.lpcPrev[2]), "out", float32(fac[0]*32768), float32(fac[1]*32768), float32(fac[2]*32768), float32(fac[3]*32768))
	}
}

const lpdFacHalf = 1.0
