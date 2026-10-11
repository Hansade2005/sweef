package render

import "github.com/sweefbrowser/sweef/internal/style"

type gridPlace struct{}

func (c *ctx) layoutFlex(b *lbox, in layoutIn) { c.layoutFlow(b, in) }
func (c *ctx) layoutGrid(b *lbox, in layoutIn) { c.layoutFlow(b, in) }
func (c *ctx) layoutControl(b *lbox, in layoutIn) {
	c.layoutReplaced(b, in)
}
func (c *ctx) controlSize(b *lbox) (float64, float64, float64) { return 100, 20, 15 }
func (c *ctx) gridIntrinsic(b *lbox) (float64, float64)        { return c.blockIntrinsic(b) }

type paintInfo struct{}

func (c *ctx) paintAll(rb *lbox)   {}
func (c *ctx) installHit(rb *lbox) {}

var _ = style.Px
