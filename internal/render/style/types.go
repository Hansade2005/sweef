// Package style computes CSS computed values for every element and
// pseudo-element: the full cascade (user-agent sheet, author sheets, inline
// style, !important, specificity, @media, @supports, @layer order), custom
// properties and var(), shorthand expansion, inheritance, and resolution of
// relative units (em, rem, ex, ch, vw, vh, %) to the extent CSS allows
// before layout. Percentages that depend on layout stay as Length.Pct.
//
// Layout and paint consume ComputedStyle and never look at declarations.
// This file is the contract between the stages; style.go implements
// Compute.
package style

import (
	"github.com/sweefbrowser/sweef/internal/render/gfx"
)

// LenKind distinguishes keyword lengths.
type LenKind uint8

const (
	LenFixed      LenKind = iota // Px + Pct% of the reference (calc() mixes both)
	LenAuto                      // auto
	LenNone                      // none (max-width/max-height)
	LenMinContent                // min-content
	LenMaxContent                // max-content
	LenFitContent                // fit-content (Px/Pct hold the fit-content() argument when given)
	LenNormal                    // normal (gaps, line-height handled separately)
)

// Length is a computed length-percentage: Px absolute pixels plus Pct
// percent of a reference only known at layout. calc(50% - 10px) is
// {Px: -10, Pct: 50}. min()/max()/clamp() with percentages are resolved at
// layout through Calc when set.
type Length struct {
	Kind LenKind
	Px   float64
	Pct  float64
	// Calc, when non-nil, evaluates the value for a reference size (used
	// for min()/max()/clamp() that cannot be reduced to Px + Pct).
	Calc func(ref float64) float64
}

// Px is a fixed pixel length.
func Px(v float64) Length { return Length{Px: v} }

// Pct is a percentage length.
func Pct(v float64) Length { return Length{Pct: v} }

// Auto is the auto keyword.
var Auto = Length{Kind: LenAuto}

// IsAuto reports Kind == LenAuto.
func (l Length) IsAuto() bool { return l.Kind == LenAuto }

// HasPct reports whether the value depends on a percentage reference.
func (l Length) HasPct() bool { return l.Kind == LenFixed && (l.Pct != 0 || l.Calc != nil) }

// Resolve returns the length for reference size ref (auto/none -> 0).
func (l Length) Resolve(ref float64) float64 {
	if l.Kind != LenFixed {
		return 0
	}
	if l.Calc != nil {
		return l.Calc(ref)
	}
	return l.Px + l.Pct*ref/100
}

// Display is the computed display value split into outer and inner types.
type Display struct {
	Outer DisplayOuter
	Inner DisplayInner
	// ListItem is set for display: list-item (and `block list-item` etc).
	ListItem bool
}

type DisplayOuter uint8

const (
	OuterBlock DisplayOuter = iota
	OuterInline
	OuterRunIn
	OuterNone     // display: none
	OuterContents // display: contents
)

type DisplayInner uint8

const (
	InnerFlow DisplayInner = iota
	InnerFlowRoot
	InnerTable
	InnerFlex
	InnerGrid
	InnerRuby
	// Internal table boxes.
	InnerTableRowGroup
	InnerTableHeaderGroup
	InnerTableFooterGroup
	InnerTableRow
	InnerTableCell
	InnerTableColumnGroup
	InnerTableColumn
	InnerTableCaption
)

// IsNone reports display: none.
func (d Display) IsNone() bool { return d.Outer == OuterNone }

// IsInlineLevel reports inline, inline-block, inline-flex, inline-grid,
// inline-table.
func (d Display) IsInlineLevel() bool { return d.Outer == OuterInline }

type Position uint8

const (
	PosStatic Position = iota
	PosRelative
	PosAbsolute
	PosFixed
	PosSticky
)

type Float uint8

const (
	FloatNone Float = iota
	FloatLeft
	FloatRight
)

type Clear uint8

const (
	ClearNone Clear = iota
	ClearLeft
	ClearRight
	ClearBoth
)

type BoxSizing uint8

const (
	ContentBox BoxSizing = iota
	BorderBox
)

type Overflow uint8

const (
	OverflowVisible Overflow = iota
	OverflowHidden
	OverflowClip
	OverflowScroll
	OverflowAuto
)

type Visibility uint8

const (
	Visible Visibility = iota
	Hidden
	Collapse
)

type WhiteSpace uint8

const (
	WSNormal WhiteSpace = iota
	WSNowrap
	WSPre
	WSPreWrap
	WSPreLine
	WSBreakSpaces
)

// CollapsesSpaces reports whether sequences of white space collapse.
func (w WhiteSpace) CollapsesSpaces() bool { return w == WSNormal || w == WSNowrap || w == WSPreLine }

// PreservesNewlines reports whether segment breaks are kept.
func (w WhiteSpace) PreservesNewlines() bool { return w == WSPre || w == WSPreWrap || w == WSPreLine || w == WSBreakSpaces }

// Wraps reports whether lines may wrap.
func (w WhiteSpace) Wraps() bool { return w != WSNowrap && w != WSPre }

type TextAlign uint8

const (
	AlignStart TextAlign = iota
	AlignEnd
	AlignLeft
	AlignRight
	AlignCenter
	AlignJustify
	AlignMatchParent
)

type TextTransform uint8

const (
	TransformNone TextTransform = iota
	TransformUppercase
	TransformLowercase
	TransformCapitalize
)

type WordBreak uint8

const (
	WordBreakNormal WordBreak = iota
	WordBreakBreakAll
	WordBreakKeepAll
	WordBreakBreakWord
)

type OverflowWrap uint8

const (
	OverflowWrapNormal OverflowWrap = iota
	OverflowWrapBreakWord
	OverflowWrapAnywhere
)

type TextOverflow uint8

const (
	TextOverflowClip TextOverflow = iota
	TextOverflowEllipsis
)

// VerticalAlign is a keyword or a length (Kind == VAlignLength).
type VerticalAlign struct {
	Kind   VAlignKind
	Length Length // for VAlignLength; percentages refer to line-height
}

type VAlignKind uint8

const (
	VAlignBaseline VAlignKind = iota
	VAlignSub
	VAlignSuper
	VAlignTextTop
	VAlignTextBottom
	VAlignMiddle
	VAlignTop
	VAlignBottom
	VAlignLength
)

// LineHeight is normal, a number (multiplier of font-size, inherited as a
// number), or a computed length.
type LineHeight struct {
	Normal bool
	Number float64 // >0 when specified as a unitless number
	Px     float64 // computed length otherwise
}

// Resolve returns the used line height for a font size and the font's
// normal line height.
func (l LineHeight) Resolve(fontSize, normal float64) float64 {
	switch {
	case l.Normal:
		return normal
	case l.Number > 0:
		return l.Number * fontSize
	}
	return l.Px
}

// Shadow is a box-shadow or text-shadow layer (lengths resolved to px).
type Shadow struct {
	OffsetX, OffsetY float64
	Blur, Spread     float64
	Color            gfx.Color
	Inset            bool
}

// ImageKind is the kind of a CSS <image>.
type ImageKind uint8

const (
	ImageNone ImageKind = iota
	ImageURL
	ImageLinearGradient
	ImageRadialGradient
	ImageConicGradient
)

// ColorStop is a gradient stop; Pos.Kind == LenAuto means "unpositioned"
// (distributed per CSS Images §3.5.1). Hint stops have Hint set.
type ColorStop struct {
	Color gfx.Color
	Pos   Length
	Hint  bool
}

// Image is a computed CSS <image>.
type Image struct {
	Kind ImageKind
	URL  string // absolute
	// Gradients.
	Repeating bool
	Angle     float64 // linear: radians, 0 = to top, clockwise; conic: from angle
	// ToCorner is set for `to top right` style linear gradients (the angle
	// is computed from the box at paint time): bit0 right, bit1 bottom,
	// bit2 left, bit3 top.
	ToCorner uint8
	// Radial: shape and size.
	Circle   bool
	SizeKind RadialSize
	SizeX    Length
	SizeY    Length
	// Centre for radial/conic.
	PosX, PosY Length
	Stops      []ColorStop
}

type RadialSize uint8

const (
	FarthestCorner RadialSize = iota
	ClosestSide
	ClosestCorner
	FarthestSide
	ExplicitSize
)

// BgRepeat per axis.
type BgRepeat uint8

const (
	BgRepeat_ BgRepeat = iota // repeat
	BgNoRepeat
	BgSpace
	BgRound
)

// BgBox is a background-origin/clip box.
type BgBox uint8

const (
	BoxBorder BgBox = iota
	BoxPadding
	BoxContent
	BoxText // background-clip: text
)

// BgSizeKind for background-size.
type BgSizeKind uint8

const (
	BgSizeExplicit BgSizeKind = iota // W/H (auto allowed per axis)
	BgSizeCover
	BgSizeContain
)

// BgLayer is one background layer. Layers are listed top-most first, as
// written in CSS; the background colour is painted below the last layer.
type BgLayer struct {
	Image      Image
	PosX, PosY Length // percentages refer to (area - image size)
	SizeKind   BgSizeKind
	SizeW      Length // auto allowed
	SizeH      Length
	RepeatX    BgRepeat
	RepeatY    BgRepeat
	Origin     BgBox
	Clip       BgBox
	Fixed      bool // background-attachment: fixed
}

// TransformFn is one transform function with lengths/angles resolved
// (translate percentages stay as Length for the box size).
type TransformFn struct {
	Kind string // matrix, translate, translateX, translateY, scale, scaleX, scaleY, rotate, skew, skewX, skewY, matrix3d (2D part), perspective (ignored)
	L    [2]Length
	N    [6]float64 // numbers / radians / matrix entries
}

// GridTrack is one track sizing function (minmax collapsed into Min/Max).
type GridTrack struct {
	Min, Max Length
	// Fr is the flexible factor when Max is a flex value (Max.Kind is
	// LenAuto in that case and Fr > 0).
	Fr float64
	// FitContent tracks keep the limit in Max.
	FitContent bool
}

// GridTemplate is a track list with optional repeat(auto-fill|auto-fit).
type GridTemplate struct {
	Tracks []GridTrack
	// LineNames[i] are the names of line i (len = len(Tracks)+1).
	LineNames [][]string
	// AutoRepeat, when set, is inserted at AutoRepeatAt and repeated to
	// fill the container (Fill) or fit it, collapsing empty ones (Fit).
	AutoRepeat   []GridTrack
	AutoRepeatAt int
	AutoFill     bool
	AutoFit      bool
	// Subgrid/masonry are not supported and compute to none.
	None bool
}

// GridLine is a grid placement: auto, a line number, a span, or a name.
type GridLine struct {
	Auto bool
	Line int // 1-based, negative from the end; 0 with Name means first such line
	Span int // >0 for `span N`
	Name string
}

// ListStyleType values are stored as their keyword (disc, circle, square,
// decimal, decimal-leading-zero, lower-alpha, upper-alpha, lower-roman,
// upper-roman, lower-greek, none, or a quoted string marker "\"→\"").
type ListStyleType string

// ContentItem is one piece of the `content` property for ::before/::after
// and ::marker.
type ContentItem struct {
	Kind    string // string, attr, counter, counters, open-quote, close-quote, url
	Text    string // string literal, attr name, counter name, URL
	Sep     string // counters() separator
	Style   ListStyleType
}

// CounterOp is a counter-reset/increment/set entry.
type CounterOp struct {
	Name  string
	Value int
}

// ComputedStyle is every property layout and paint need, computed.
// Inherited properties are already inherited; lengths are absolute px
// except where a Length keeps a percentage.
type ComputedStyle struct {
	Display   Display
	Position  Position
	Float     Float
	Clear     Clear
	BoxSizing BoxSizing

	Top, Right, Bottom, Left Length
	Width, Height            Length
	MinWidth, MinHeight      Length // auto computes to Auto (0 except flex/grid items)
	MaxWidth, MaxHeight      Length // LenNone when none
	AspectRatio              float64 // width/height; 0 = auto
	AspectRatioAuto          bool    // `auto && <ratio>`: replaced elements prefer their own

	Margin  [4]Length  // top, right, bottom, left; auto allowed
	Padding [4]Length  // percentages refer to containing block width
	Border  [4]BorderSide
	Radii   [4][2]Length // tl, tr, br, bl; each {horizontal, vertical}

	Outline       BorderSide
	OutlineOffset float64

	OverflowX, OverflowY Overflow
	Visibility           Visibility
	Opacity              float64
	ZIndex               int
	ZIndexAuto           bool

	Color           gfx.Color
	BackgroundColor gfx.Color
	Backgrounds     []BgLayer
	BoxShadow       []Shadow

	// Fonts and text (inherited unless noted).
	FontFamily     []string
	FontSize       float64
	FontWeight     int
	FontItalic     bool
	FontSmallCaps  bool
	LineHeight     LineHeight
	LetterSpacing  float64
	WordSpacing    float64
	TextAlign      TextAlign
	TextAlignLast  TextAlign
	TextIndent     Length
	TextTransform  TextTransform
	WhiteSpace     WhiteSpace
	WordBreak      WordBreak
	OverflowWrap   OverflowWrap
	Hyphens        bool
	TabSize        float64 // in spaces
	Direction      string  // ltr, rtl
	WritingMode    string  // horizontal-tb only is laid out
	TextShadow     []Shadow
	TextOverflow   TextOverflow // not inherited
	VerticalAlign  VerticalAlign // not inherited
	TextDecoration TextDecorationStyle // not inherited (propagates per CSS rules in paint)
	TextFillColor  *gfx.Color          // -webkit-text-fill-color
	TextStroke     float64
	TextStrokeColor gfx.Color

	ListStyleType     ListStyleType
	ListStylePosition string // outside, inside
	ListStyleImage    Image

	Content         []ContentItem // nil = normal/none for elements
	ContentNone     bool
	CounterReset    []CounterOp
	CounterIncrement []CounterOp
	CounterSet      []CounterOp
	Quotes          []string

	// Flexbox.
	FlexDirection  string // row, row-reverse, column, column-reverse
	FlexWrap       string // nowrap, wrap, wrap-reverse
	FlexGrow       float64
	FlexShrink     float64
	FlexBasis      Length // auto = LenAuto; content = LenMaxContent
	Order          int
	JustifyContent string // flex-start, flex-end, center, space-between, space-around, space-evenly, start, end, left, right, stretch, normal
	AlignItems     string // stretch, flex-start, flex-end, center, baseline, start, end, normal, self-start, self-end
	AlignSelf      string // auto or as AlignItems
	AlignContent   string
	JustifyItems   string
	JustifySelf    string
	RowGap         Length // LenNormal = normal
	ColumnGap      Length

	// Grid.
	GridTemplateColumns GridTemplate
	GridTemplateRows    GridTemplate
	GridTemplateAreas   [][]string // rows of cell names ("." = empty)
	GridAutoColumns     []GridTrack
	GridAutoRows        []GridTrack
	GridAutoFlow        string // row, column, row dense, column dense
	GridRowStart        GridLine
	GridRowEnd          GridLine
	GridColumnStart     GridLine
	GridColumnEnd       GridLine

	// Tables.
	BorderCollapse  bool
	BorderSpacingH  float64
	BorderSpacingV  float64
	TableLayoutFixed bool
	CaptionSide     string // top, bottom
	EmptyCellsHide  bool

	// Replaced content.
	ObjectFit      string // fill, contain, cover, none, scale-down
	ObjectPosX     Length
	ObjectPosY     Length
	ImageRendering string // auto, pixelated, crisp-edges

	// Effects.
	Transform       []TransformFn
	TransformOrigin [2]Length // z ignored
	Filter          []gfx.FilterOp
	BackdropFilter  []gfx.FilterOp
	MixBlendMode    gfx.CompositeOp
	Isolation       bool
	ClipPath        string // raw value; paint parses basic shapes
	Clip            *gfx.Rect // legacy clip: rect(), for absolutely positioned
	Mask            string    // raw; unsupported values ignored
	WillChange      string
	Contain         string
	ContentVisibilityHidden bool

	// Interaction (hit-testing and form rendering).
	PointerEvents  bool // false for pointer-events: none
	Cursor         string
	Appearance     string // auto, none, ...
	AccentColor    *gfx.Color
	CaretColor     *gfx.Color
	ColorScheme    string
	ScrollBehavior string

	// Multi-column (laid out as a single column when unsupported).
	ColumnCount int
	ColumnWidth Length

	// Pseudo is "" for elements, or before, after, marker, placeholder,
	// first-line, first-letter, selection.
	Pseudo string
}

// BorderSide is one border (or the outline).
type BorderSide struct {
	Width float64 // computed: 0 when style is none/hidden
	Style gfx.BorderStyle
	Color gfx.Color
}

// TextDecorationStyle is the decoration an element introduces.
type TextDecorationStyle struct {
	Lines     gfx.TextDecoration
	Style     string // solid, double, dotted, dashed, wavy
	Color     gfx.Color
	ColorSet  bool // false = currentColor
	Thickness Length
	// UnderlineOffset is text-underline-offset (auto = Kind LenAuto).
	UnderlineOffset Length
}

// Establishes reports whether the style creates a stacking context.
func (s *ComputedStyle) EstablishesStackingContext() bool {
	if s.Position != PosStatic && !s.ZIndexAuto {
		return true
	}
	if s.Position == PosFixed || s.Position == PosSticky {
		return true
	}
	return s.Opacity < 1 || len(s.Transform) > 0 || len(s.Filter) > 0 || s.MixBlendMode != gfx.SrcOver ||
		s.Isolation || s.ClipPath != "" || len(s.BackdropFilter) > 0
}

// IsFloating reports float: left/right on a non-positioned box.
func (s *ComputedStyle) IsFloating() bool {
	return s.Float != FloatNone && s.Position != PosAbsolute && s.Position != PosFixed
}

// IsOutOfFlow reports absolutely/fixed positioned boxes.
func (s *ComputedStyle) IsOutOfFlow() bool { return s.Position == PosAbsolute || s.Position == PosFixed }

// FontStyle is the font request for this style.
func (s *ComputedStyle) FontStyle() (families []string, weight int, italic bool, size float64) {
	return s.FontFamily, s.FontWeight, s.FontItalic, s.FontSize
}
