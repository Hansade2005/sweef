package render

func (c *ctx) layoutFlex(b *lbox, in layoutIn) { c.layoutFlow(b, in) }
func (c *ctx) layoutGrid(b *lbox, in layoutIn) { c.layoutFlow(b, in) }
