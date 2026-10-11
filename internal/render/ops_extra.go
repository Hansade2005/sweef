package render

// Additive display-list ops (see displaylist.go for the base set).

// Scrollbar paints a classic (non-overlay, 15px) Chromium/Linux scrollbar
// inside Rect: track, two arrow buttons and a thumb.
type Scrollbar struct {
	Rect     Rect
	Vertical bool
	// Thumb is the thumb's offset from the bar start and its length in px;
	// Len 0 means "no thumb" (nothing to scroll).
	ThumbOff, ThumbLen float64
	Dark               bool
}

// ScrollCorner fills the square where two scrollbars meet.
type ScrollCorner struct {
	Rect Rect
	Dark bool
}

func (Scrollbar) isOp()    {}
func (ScrollCorner) isOp() {}
