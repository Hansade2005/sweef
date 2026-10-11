package h264

// Slice data partitioning (Extended profile, nal_unit_type 2, 3 and 4;
// 7.3.2.9, 7.4.2.9). Partition A carries the slice header, the slice_id and
// the category 2 syntax elements (macroblock types, prediction data,
// coded_block_pattern, mb_qp_delta); partition B the residual of intra
// macroblocks (category 3) and partition C the residual of inter macroblocks
// (category 4). The partitions of one slice follow each other in the stream;
// the slice is decoded once the next NAL unit that is not a B or C partition
// (or the end of the access unit) is seen.
//
// A slice whose partition B or C is missing is decoded with the macroblocks
// whose residual lived in the missing partition reconstructed from their
// prediction alone (a missing B turns intra macroblocks into pure
// predictions, a missing C inter macroblocks into pure motion
// compensation), which is the behaviour of the JM reference decoder's
// concealment when no neighbouring data is available. A slice without
// partition A is dropped, as its macroblock layer is unrecoverable.

type dpSlice struct {
	n      []byte
	h      *sliceHeader
	r      *bitReader
	refIdc int
	id     int
	b, c   *bitReader
}

func (d *Decoder) handlePartitionA(n []byte, refIdc int) error {
	r := newBitReader(unescape(n[1:]))
	h, err := d.parseSliceHeader(r, 2, refIdc)
	if err != nil {
		return err
	}
	id := int(r.ue())
	if r.overrun() {
		return errBitstream
	}
	d.dpA = &dpSlice{n: n, h: h, r: r, refIdc: refIdc, id: id}
	return nil
}

func (d *Decoder) handlePartitionBC(n []byte, typ int) error {
	a := d.dpA
	if a == nil {
		return nil // partition A lost: nothing to attach to
	}
	r := newBitReader(unescape(n[1:]))
	id := int(r.ue())
	if a.h.sps.SeparatePlanes {
		r.u(2)
	}
	if a.h.pps.RedundantPicCnt {
		r.ue()
	}
	if r.overrun() || id != a.id {
		return nil // belongs to a slice whose partition A was lost
	}
	if typ == 3 {
		a.b = r
	} else {
		a.c = r
	}
	return nil
}

// flushPartitions decodes the pending partitioned slice, if any.
func (d *Decoder) flushPartitions() error {
	a := d.dpA
	if a == nil {
		return nil
	}
	d.dpA = nil
	d.brB, d.brC = a.b, a.c
	err := d.startSlice(a.h, a.r, 2, a.refIdc)
	d.brB, d.brC = nil, nil
	return err
}

// dpConstrainedNC reports whether the nC rule of 9.2.1 for partitioned
// slices applies: with constrained_intra_pred_flag, the non-zero counts of
// inter neighbours of an intra macroblock are taken as 0.
func (d *Decoder) dpConstrainedNC(intra bool) bool {
	return d.dpOn && intra && d.hdr.pps.ConstrainedIntra
}

// dpSpan is the bit range of the residual (or PCM samples) of a macroblock;
// dpTrace, when set, receives one per macroblock (used by the partitioning
// test, which rewrites ordinary slices into partitioned ones).
type dpSpan struct {
	start, end int
	pre        int // PCM: position before the alignment bits
	intra, pcm bool
}

var dpTrace func(dpSpan)

// dpContaminated reports whether a neighbouring macroblock of the slice lost
// its residual in a missing partition, which makes the coeff_token context
// (nC) of this macroblock unknowable.
func (d *Decoder) dpContaminated(intra bool) bool {
	nb := &d.nb
	lost := func(t uint32, xy int) bool {
		if t == 0 || d.dpLost[xy] != d.curSlice {
			return false
		}
		return !d.dpConstrainedNC(intra) || isIntraT(t)
	}
	return lost(nb.topType, nb.topXY) || lost(nb.leftType[0], nb.leftXY[0]) || lost(nb.leftType[1], nb.leftXY[1])
}
