package hevc

// Multilayer support (Annex F): MV-HEVC style decoding of an enhancement
// layer with inter-layer reference pictures. The decoder state that is
// specific to a layer is swapped in when a slice of another layer arrives.

// SetOutputLayer selects the layer (nuh_layer_id) whose pictures are
// returned by Push, Flush and DecodeFrame. Layer 0, the default, decodes the
// base layer only and ignores all other NAL units. Any other layer needs the
// VPS with its extension; the layers it depends on are decoded as well.
func (d *Decoder) SetOutputLayer(id int) {
	if id < 0 || id >= maxLayers {
		id = 0
	}
	d.outLayer = id
}

// OutputLayer returns the layer selected with SetOutputLayer.
func (d *Decoder) OutputLayer() int { return d.outLayer }

func (d *Decoder) vpsByID(id int) *VPS {
	if id < 0 || id >= len(d.vps) {
		return nil
	}
	return d.vps[id]
}

// Layers returns the nuh_layer_id values declared by the VPS of the stream
// (always starting with 0), or just 0 when the stream is single layer or the
// layer selection is the base layer.
func (d *Decoder) Layers() []int {
	for _, v := range d.vps {
		if v != nil && v.hasExt {
			var out []int
			for i := 0; i < v.maxLayers; i++ {
				out = append(out, v.layerIDInNuh[i])
			}
			return out
		}
	}
	return []int{0}
}

// layerNeeded reports whether pictures of layer take part in decoding the
// selected output layer.
func (d *Decoder) layerNeeded(layer int) bool {
	if layer == d.outLayer {
		return true
	}
	for _, v := range d.vps {
		if v == nil || !v.hasExt || layer >= maxLayers || d.outLayer >= maxLayers {
			continue
		}
		li, oi := v.layerIdx[layer], v.layerIdx[d.outLayer]
		if li >= 0 && oi >= 0 {
			return v.depFlag[oi][li]
		}
	}
	return false
}

func (d *Decoder) saveLayer() {
	ls := d.layers[d.curLayer]
	if ls == nil {
		ls = &layerState{}
		d.layers[d.curLayer] = ls
	}
	*ls = layerState{
		dpb: d.dpb, asps: d.asps, apps: d.apps,
		stBefore: d.stBefore, stAfter: d.stAfter, ltCurr: d.ltCurr,
		prevTid0Poc: d.prevTid0Poc, firstPic: d.firstPic, afterEOS: d.afterEOS,
		noRaslOutput: d.noRaslOutput, skipPic: d.skipPic,
	}
}

func (d *Decoder) switchLayer(id int) {
	if id == d.curLayer {
		return
	}
	d.saveLayer()
	ls := d.layers[id]
	if ls == nil {
		ls = &layerState{firstPic: true}
		d.layers[id] = ls
	}
	d.dpb, d.asps, d.apps = ls.dpb, ls.asps, ls.apps
	d.stBefore, d.stAfter, d.ltCurr = ls.stBefore, ls.stAfter, ls.ltCurr
	d.prevTid0Poc, d.firstPic, d.afterEOS = ls.prevTid0Poc, ls.firstPic, ls.afterEOS
	d.noRaslOutput, d.skipPic = ls.noRaslOutput, ls.skipPic
	d.curLayer = id
	d.lastIndep = nil
}

// interLayerRefs returns the inter-layer reference pictures of the current
// picture for sh, split into the two sets of F.8.1.2 (RefPicSetInterLayer0
// and 1).
func (d *Decoder) interLayerRefs(sh *sliceHeader) (il0, il1 []*Picture, err error) {
	if sh.numActiveRefLay == 0 {
		return nil, nil, nil
	}
	sps := d.asps
	vps := d.vps[sps.vpsID]
	if vps == nil {
		return nil, nil, errBitstream
	}
	li := vps.layerIdx[sh.layer]
	cur := vps.viewID[li]
	zero := vps.viewID[0]
	for i := 0; i < sh.numActiveRefLay; i++ {
		rli := vps.directRefIdx[li][sh.refLayerIdc[i]]
		rl := vps.layerIDInNuh[rli]
		var pic *Picture
		if ls := d.layers[rl]; ls != nil {
			for _, p := range ls.dpb {
				if p.poc == d.cur.poc && p != d.cur {
					pic = p
				}
			}
		}
		if pic == nil {
			return nil, nil, errBitstream
		}
		ref := vps.viewID[rli]
		if (cur <= zero && cur <= ref) || (cur >= zero && cur >= ref) {
			il0 = append(il0, pic)
		} else {
			il1 = append(il1, pic)
		}
	}
	return il0, il1, nil
}
