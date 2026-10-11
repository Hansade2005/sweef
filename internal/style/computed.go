package style

// Shadow is one box-shadow or text-shadow.
type Shadow struct {
	DX, DY, Blur, Spread float64
	Color                Color
	Inset                bool
	cur                  bool
}

// Stop is a gradient color stop.
type Stop struct {
	Color  Color
	Cur    bool
	Pos    Length
	HasPos bool
	Hint   bool // a color hint (position only)
}

// Gradient is a parsed CSS gradient image.
type Gradient struct {
	Kind      string // linear, radial, conic
	Repeating bool
	// linear
	Angle    float64
	ToCorner string // "top right" etc. when the direction is a corner keyword
	// radial
	Shape   string // circle | ellipse
	Extent  string // closest-side farthest-side closest-corner farthest-corner (default farthest-corner)
	RadiusX Length // explicit radii (circle uses RadiusX)
	RadiusY Length
	HasSize bool
	Pos     [2]Length // center (radial/conic) as lengths relative to the box (pct or px)
	HasPos  bool
	From    float64 // conic start angle
	Stops   []Stop
}

// Image is a CSS <image>: url() or a gradient.
type Image struct {
	URL  string
	Grad *Gradient
}

// IsNone reports no image.
func (i Image) IsNone() bool { return i.URL == "" && i.Grad == nil }

// BgPos is one axis of background-position: Pct of (container-image) plus Px.
type BgPos struct{ Pct, Px float64 }

// BgLayer is one background layer.
type BgLayer struct {
	Image     Image
	PosX      Length // LPct/LPx/LCalc; resolved against container-image
	PosY      Length
	SizeKind  string // "", cover, contain
	SizeW     Length // auto allowed
	SizeH     Length
	RepeatX   string // repeat | no-repeat | space | round
	RepeatY   string
	Origin    string // border-box | padding-box | content-box
	Clip      string // border-box | padding-box | content-box | text
	Attach    string // scroll | fixed | local
	RepeatSet bool
	Blend     string
}

// TOp is one transform function.
type TOp struct {
	Fn string // matrix, translate, translateX, translateY, scale, scaleX, scaleY, rotate, skew, skewX, skewY, translate3d...
	A  [6]Length
	N  int
}

// FilterFn is one filter function.
type FilterFn struct {
	Name  string
	Args  []float64
	Color Color
	Shad  Shadow
}

// ContentItem is one piece of the content property.
type ContentItem struct {
	K     string // str, attr, counter, counters, open-quote, close-quote, no-open-quote, no-close-quote, url
	S     string
	Sep   string
	Style string
}

// CounterOp is one counter-reset / -increment / -set entry.
type CounterOp struct {
	Name string
	Val  int
}

// SizeKind of a grid track sizing function component.
type SizeKind uint8

// Track size kinds.
const (
	SzAuto SizeKind = iota
	SzLen
	SzFr
	SzMinContent
	SzMaxContent
	SzFitContent
)

// Sz is a track breadth.
type Sz struct {
	K  SizeKind
	L  Length
	Fr float64
}

// Track is a track sizing function (min, max).
type Track struct {
	Min, Max Sz
	Names    []string // line names before this track
}

// TrackList is a grid-template-columns/rows value.
type TrackList struct {
	Pre, Rep, Post []Track
	RepMode        string // "", fill, fit
	RepCount       int
	EndNames       []string // line names after the last track
	RepEndNames    []string // line names after the repeat group's last track
	None           bool
	Subgrid        bool
}

// GridLine is a grid-row-start style placement value.
type GridLine struct {
	Auto bool
	Span int // >0 for span N
	Num  int
	Name string
	Set  bool
}

// Inherited holds inherited properties.
type Inherited struct {
	Color                   Color
	FontFamily              []string
	FontSize                float64
	FontSizeKw              int8
	FontAbs                 bool
	FontWeight              int
	FontStyle               uint8 // 0 normal 1 italic 2 oblique
	SmallCaps               bool
	FontStretch             float64
	LineHeight              Length // LAuto = normal; LNumber multiplier; LPx absolute; LPct of font size (resolved to LPx)
	LetterSpacing           float64
	WordSpacing             float64
	TextAlign               string
	TextAlignLast           string
	TextIndent              Length
	TextIndentHang          bool
	TextTransform           string
	WhiteSpace              string
	WordBreak               string
	OverflowWrap            string
	LineBreak               string
	Hyphens                 string
	TextWrap                string // wrap | nowrap | balance | pretty
	Direction               string
	Visibility              string
	Cursor                  string
	PointerEvents           string
	ListStyleType           string
	ListStylePos            string
	ListStyleImage          Image
	Quotes                  [][2]string
	BorderCollapse          bool
	SpacingH                float64
	SpacingV                float64
	CaptionSide             string
	EmptyCellsHide          bool
	TabSize                 float64
	TabSizePx               bool
	TextShadow              []Shadow
	TextFill                Color
	TextFillSet             bool
	TextStrokeW             float64
	TextStrokeC             Color
	TextStrokeSet           bool
	FontKerning             string
	TextRendering           string
	FontVariantNum          string
	UserSelect              string
	WritingMode             string
	TextUnderlineOffset     Length
	TextDecorationThickness Length
	TextUnderlinePos        string
	ColorScheme             string
	AccentColor             Color
	AccentSet               bool
	Custom                  map[string]string
	ImageRendering          string
	LineClamp               int
	Orphans                 int
	CaretColor              Color
	CaretSet                bool
}

// Computed is the computed style of an element or pseudo-element.
type Computed struct {
	Inherited

	Display              string
	Position             string
	Float                string
	Clear                string
	BoxSizing            string
	OverflowX, OverflowY string
	Contain              string
	Isolation            string
	Appearance           string
	Resize               string
	Scrollbar            string // scrollbar-width: auto | thin | none
	ScrollbarGutter      string
	OverflowClipMargin   float64

	Width, Height, MinWidth, MinHeight, MaxWidth, MaxHeight Length
	Margin                                                  [4]Length // top right bottom left
	Padding                                                 [4]Length
	Inset                                                   [4]Length // top right bottom left
	AspectRatio                                             float64   // 0 = auto
	AspectAuto                                              bool      // "auto <ratio>"

	BorderWidth [4]float64
	BorderStyle [4]string
	BorderColor [4]Color
	bcSet       [4]bool
	Radius      [4][2]Length // tl tr br bl; [h v]
	BorderImage bool

	OutlineWidth  float64
	OutlineStyle  string
	OutlineColor  Color
	outlineSet    bool
	OutlineOffset float64

	ZIndex int
	ZAuto  bool

	BgColor   Color
	bgCur     bool
	BgLayers  []BgLayer
	BoxShadow []Shadow

	Opacity                  float64
	OpacitySet               bool
	Transform                []TOp
	TransformOrigin          [2]Length
	TOriginZ                 float64
	Translate, Rotate, Scale []TOp
	Perspective              float64
	Filter                   []FilterFn
	BackdropFilter           []FilterFn
	MixBlend                 string
	ClipPath                 bool
	WillChange               string
	MaskImage                bool

	VerticalAlign    string // baseline sub super top text-top middle bottom text-bottom length
	VerticalAlignLen Length
	TextDecorLine    uint8 // bit0 underline bit1 overline bit2 line-through
	TextDecorStyle   string
	TextDecorColor   Color
	DecorCur         bool
	TextOverflow     string
	ObjectFit        string
	ObjectPos        [2]Length
	ListMarkerIsNone bool

	// flex
	FlexDirection                                                                  string
	FlexWrap                                                                       string
	FlexGrow                                                                       float64
	FlexShrink                                                                     float64
	FlexBasis                                                                      Length
	Order                                                                          int
	JustifyContent, AlignItems, AlignSelf, AlignContent, JustifyItems, JustifySelf string
	RowGap, ColumnGap                                                              Length
	BoxOrient                                                                      string // -webkit-box-orient

	// grid
	GridCols, GridRows         TrackList
	GridAutoCols, GridAutoRows []Track
	GridAutoFlow               string
	GridAreas                  [][]string
	GridRowStart, GridRowEnd   GridLine
	GridColStart, GridColEnd   GridLine

	// table
	TableLayout string

	// multi-column
	ColumnCount     int
	ColumnWidth     Length
	ColumnRuleW     float64
	ColumnRuleStyle string
	ColumnRuleColor Color
	ColumnFill      string
	ColumnSpan      string

	// generated content
	Content                               []ContentItem
	ContentSet                            bool // content specified (not normal)
	ContentNone                           bool
	CounterReset, CounterIncr, CounterSet []CounterOp

	// internal helpers
	Zoom  float64
	Flags uint32
	OrigInline bool
	id    uint64
}

// Flags bits.
const (
	FlagSelfShadow = 1 << iota
)

// Underline etc. bits for TextDecorLine.
const (
	DecoUnderline   = 1
	DecoOverline    = 2
	DecoLineThrough = 4
)

// Initial returns the root's parent: the initial values of all properties.
func Initial() *Computed {
	c := &Computed{}
	c.Color = Color{0, 0, 0, 255}
	c.FontFamily = []string{"serif"}
	c.FontSizeKw = 4
	c.FontSize = 16
	c.FontSizeKw = 4
	c.FontWeight = 400
	c.FontStretch = 100
	c.TextAlign = "start"
	c.TextTransform = "none"
	c.WhiteSpace = "normal"
	c.WordBreak = "normal"
	c.OverflowWrap = "normal"
	c.Direction = "ltr"
	c.Visibility = "visible"
	c.Cursor = "auto"
	c.PointerEvents = "auto"
	c.ListStyleType = "disc"
	c.ListStylePos = "outside"
	c.CaptionSide = "top"
	c.TabSize = 8
	c.SpacingH, c.SpacingV = 0, 0
	c.TextWrap = "wrap"
	c.Hyphens = "manual"
	c.UserSelect = "auto"
	c.WritingMode = "horizontal-tb"
	c.Quotes = [][2]string{{"“", "”"}, {"‘", "’"}}
	c.ImageRendering = "auto"
	c.Display = "inline"
	c.Position = "static"
	c.Float = "none"
	c.Clear = "none"
	c.BoxSizing = "content-box"
	c.OverflowX, c.OverflowY = "visible", "visible"
	c.ZAuto = true
	c.Opacity = 1
	c.OpacitySet = false
	c.VerticalAlign = "baseline"
	c.TextDecorStyle = "solid"
	c.TextOverflow = "clip"
	c.ObjectFit = "fill"
	c.ObjectPos = [2]Length{Pct(50), Pct(50)}
	c.FlexDirection = "row"
	c.FlexWrap = "nowrap"
	c.FlexShrink = 1
	c.FlexBasis = Length{K: LAuto}
	c.JustifyContent, c.AlignItems, c.AlignSelf, c.AlignContent = "normal", "normal", "auto", "normal"
	c.JustifyItems, c.JustifySelf = "legacy", "auto"
	c.GridAutoFlow = "row"
	c.TableLayout = "auto"
	c.MixBlend = "normal"
	c.TransformOrigin = [2]Length{Pct(50), Pct(50)}
	c.OutlineStyle = "none"
	c.OutlineWidth = 3
	for i := range c.BorderStyle {
		c.BorderStyle[i] = "none"
		c.BorderWidth[i] = 0
	}
	c.ColumnFill = "balance"
	c.ColumnSpan = "none"
	c.ColumnRuleStyle = "none"
	c.ColumnRuleW = 3
	c.Isolation = "auto"
	c.Appearance = "none"
	c.Resize = "none"
	c.Scrollbar = "auto"
	c.Zoom = 1
	for i := 0; i < 4; i++ {
		c.Margin[i] = Px(0)
		c.Padding[i] = Px(0)
	}
	c.Orphans = 2
	return c
}
