package style

import (
	fnet "github.com/sweefbrowser/sweef/internal/net"
	"github.com/sweefbrowser/sweef/internal/render/gfx"
	"golang.org/x/net/html"
)

// Resolver computes styles for one document at one viewport size. Styles
// are cached per node; build a new Resolver after DOM or stylesheet
// changes (layout does this per layout pass).
type Resolver struct {
	Doc         *fnet.Doc
	ViewportW   float64
	ViewportH   float64
	ColorScheme string // "light" or "dark"
	impl        *resolverImpl
	fn          StyleFunc
	fnCache     map[fnKey]*ComputedStyle
}

// NewResolver prepares style resolution for d at a viewport size.
func NewResolver(d *fnet.Doc, vw, vh float64) *Resolver {
	r := &Resolver{Doc: d, ViewportW: vw, ViewportH: vh, ColorScheme: "light"}
	r.impl = newResolverImpl(r)
	return r
}

// Style returns the computed style of element n (never nil).
func (r *Resolver) Style(n *html.Node) *ComputedStyle {
	if r.fn != nil {
		return r.viaFn(n, "")
	}
	return r.impl.style(n)
}

func (r *Resolver) viaFn(n *html.Node, p string) *ComputedStyle {
	k := fnKey{n, p}
	if cs, ok := r.fnCache[k]; ok {
		return cs
	}
	cs := r.fn(n, p)
	if cs == nil && p == "" {
		cs = Initial()
	}
	r.fnCache[k] = cs
	return cs
}

// Pseudo returns the computed style of n's pseudo-element ("before",
// "after", "marker", "placeholder", "first-line", "first-letter",
// "selection"), or nil when it generates no box (no content, or display:
// none).
func (r *Resolver) Pseudo(n *html.Node, pseudo string) *ComputedStyle {
	if r.fn != nil {
		return r.viaFn(n, pseudo)
	}
	return r.impl.pseudo(n, pseudo)
}

// Initial returns a ComputedStyle holding every property's initial value
// (what the root inherits from).
func Initial() *ComputedStyle {
	return &ComputedStyle{
		Display: Display{Outer: OuterInline, Inner: InnerFlow},
		Top:     Auto, Right: Auto, Bottom: Auto, Left: Auto,
		Width: Auto, Height: Auto,
		MinWidth: Auto, MinHeight: Auto,
		MaxWidth: Length{Kind: LenNone}, MaxHeight: Length{Kind: LenNone},
		Opacity:           1,
		ZIndexAuto:        true,
		Color:             gfx.Black,
		FontFamily:        []string{"serif"},
		FontSize:          16,
		FontWeight:        400,
		LineHeight:        LineHeight{Normal: true},
		TabSize:           8,
		Direction:         "ltr",
		WritingMode:       "horizontal-tb",
		ListStyleType:     "disc",
		ListStylePosition: "outside",
		FlexDirection:     "row",
		FlexWrap:          "nowrap",
		FlexShrink:        1,
		FlexBasis:         Auto,
		JustifyContent:    "normal",
		AlignItems:        "normal",
		AlignSelf:         "auto",
		AlignContent:      "normal",
		JustifyItems:      "legacy",
		JustifySelf:       "auto",
		RowGap:            Length{Kind: LenNormal},
		ColumnGap:         Length{Kind: LenNormal},
		GridAutoFlow:      "row",
		GridAutoColumns:   []GridTrack{{Min: Auto, Max: Auto}},
		GridAutoRows:      []GridTrack{{Min: Auto, Max: Auto}},
		GridRowStart:      GridLine{Auto: true}, GridRowEnd: GridLine{Auto: true},
		GridColumnStart: GridLine{Auto: true}, GridColumnEnd: GridLine{Auto: true},
		CaptionSide: "top",
		ObjectFit:   "fill",
		ObjectPosX:  Pct(50), ObjectPosY: Pct(50),
		ImageRendering:  "auto",
		TransformOrigin: [2]Length{Pct(50), Pct(50)},
		PointerEvents:   true,
		Cursor:          "auto",
		Appearance:      "none",
		TextDecoration:  TextDecorationStyle{Style: "solid", Thickness: Auto, UnderlineOffset: Auto},
		ColorScheme:     "normal",
		ScrollBehavior:  "auto",
		ColumnWidth:     Auto,
		Quotes:          []string{"“", "”", "‘", "’"},
	}
}

// StyleFunc supplies styles directly (tests, and callers that compute
// styles another way). pseudo is "" for the element itself; returning nil
// for a pseudo means it generates no box.
type StyleFunc func(n *html.Node, pseudo string) *ComputedStyle

// NewResolverFunc builds a Resolver whose styles come from fn instead of
// the cascade. Layout and paint tests use it to run before, and
// independently of, the cascade.
func NewResolverFunc(d *fnet.Doc, vw, vh float64, fn StyleFunc) *Resolver {
	r := &Resolver{Doc: d, ViewportW: vw, ViewportH: vh, ColorScheme: "light"}
	r.fn = fn
	r.fnCache = map[fnKey]*ComputedStyle{}
	return r
}

type fnKey struct {
	n *html.Node
	p string
}
