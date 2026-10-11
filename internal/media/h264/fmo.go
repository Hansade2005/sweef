package h264

// Slice groups (flexible macroblock ordering, 8.2.2).

func (d *Decoder) sliceGroupMap(h *sliceHeader) []uint8 { return nil }
func (d *Decoder) startFMO(first int) bool                { return false }
func (d *Decoder) advanceFMO() bool                       { return false }
