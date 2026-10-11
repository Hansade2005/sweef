package media

type psState struct {
	start bool
}

func newPS() *psState { return &psState{} }

func (p *psState) parse(r *aacBits, left int) error { return errSBRPS }

func (s *sbrElem) applyPS() {}
