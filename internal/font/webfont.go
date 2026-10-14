package font

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// webfont.go: the set of @font-face (and script created) fonts of one
// document, and how CSS font-family names reach them.
//
// A document that declares web fonts owns a WebSet. Style computation rewrites
// every family name of a font-family list that the set declares into a scoped
// name ("roboto\x00w7"), so everything downstream that resolves families by
// name (layout, rasterisation, canvas) finds the right set without a
// per-document parameter, and two tabs that define different "Roboto" fonts
// never see each other's. LookupAll understands scoped names; a family whose
// faces have not arrived yet resolves to nothing, so the next family of the
// list (and finally a system font) is used: fonts are always painted with a
// fallback first (font-display: swap), never invisible.
//
// Faces are fetched lazily. Whoever needs a character that no loaded face
// provides calls Demand; the set finds the face the CSS font matching
// algorithm selects for that character (honouring unicode-range) and hands
// it to the loader exactly once.

// URange is an inclusive code point range of a unicode-range descriptor.
type URange struct{ Lo, Hi rune }

// Source is one entry of an @font-face src descriptor.
type Source struct {
	URL    string   // absolute URL, "" for local()
	Local  string   // local() name
	Format string   // format() hint, lower case ("" none)
	Tech   []string // tech() hints, lower case
}

// FaceDesc is a parsed @font-face rule (or the arguments of new FontFace).
type FaceDesc struct {
	Family     string
	WMin, WMax int // font-weight range, 1..1000
	Style      uint8
	SMin, SMax float64 // font-stretch range in percent
	Ranges     []URange
	Src        []Source
	Display    string
	Base       string // URL of the stylesheet that declared it (informational)
	Features   string // font-feature-settings descriptor, raw
	Variations string // font-variation-settings descriptor, raw
	// SizeAdjust, AscentOverride, DescentOverride and LineGapOverride are the
	// metric descriptors (fractions: 1.0 = 100%); a size-adjust of 0 means 1.
	// Overrides is the set of overrides that were given (OvrAscent|OvrDescent|OvrLineGap).
	SizeAdjust                                       float64
	AscentOverride, DescentOverride, LineGapOverride float64
	Overrides                                        uint8
}

// Overrides bits.
const (
	OvrAscent = 1 << iota
	OvrDescent
	OvrLineGap
)

// Style values of FaceDesc.
const (
	StyleNormal  = 0
	StyleItalic  = 1
	StyleOblique = 2
)

// Normalize fills defaults.
func (d *FaceDesc) Normalize() {
	if d.WMin == 0 && d.WMax == 0 {
		d.WMin, d.WMax = 400, 400
	}
	if d.WMax < d.WMin {
		d.WMin, d.WMax = d.WMax, d.WMin
	}
	if d.SMin == 0 && d.SMax == 0 {
		d.SMin, d.SMax = 100, 100
	}
	if d.SizeAdjust == 0 {
		d.SizeAdjust = 1
	}
}

// Key identifies a declaration: two equal keys are the same font.
func (d FaceDesc) Key() string {
	var b strings.Builder
	b.WriteString(FamilyKey(d.Family))
	fmt.Fprintf(&b, "|%d-%d|%d|%g-%g|", d.WMin, d.WMax, d.Style, d.SMin, d.SMax)
	for _, r := range d.Ranges {
		fmt.Fprintf(&b, "%x-%x,", r.Lo, r.Hi)
	}
	b.WriteByte('|')
	for _, s := range d.Src {
		fmt.Fprintf(&b, "%s;%s;%s;%s,", s.URL, s.Local, s.Format, strings.Join(s.Tech, "+"))
	}
	fmt.Fprintf(&b, "|%s|%s|%s|%g,%g,%g,%g,%d", d.Display, d.Features, d.Variations, d.SizeAdjust, d.AscentOverride, d.DescentOverride, d.LineGapOverride, d.Overrides)
	return b.String()
}

// Covers reports whether the unicode-range includes r.
func (d *FaceDesc) Covers(r rune) bool { return inRanges(d.Ranges, r) }

func inRanges(rs []URange, r rune) bool {
	if len(rs) == 0 {
		return true
	}
	for _, x := range rs {
		if r >= x.Lo && r <= x.Hi {
			return true
		}
	}
	return false
}

// LoadState of a web face.
type LoadState uint8

const (
	Unloaded LoadState = iota
	Loading
	Loaded
	Failed
)

func (s LoadState) String() string {
	return [...]string{"unloaded", "loading", "loaded", "error"}[s]
}

// WebFace is one declared font. The engine fetches its bytes and reports them
// with SetData or SetFailed; the set only decides when a load is needed.
type WebFace struct {
	Desc  FaceDesc
	Order int // declaration order within the set
	// Data is free for the loader (the engine keeps its request state here).
	Data any
	// Trigger is the first character that made the face necessary (set when
	// a load starts; zero for an explicit load).
	Trigger rune

	set   *WebSet
	state LoadState
	err   error
	sfnt  []byte
	base  *Face
	insts map[int]*Face
	var_  *variation
	gen   uint64 // set generation at which the state last changed
}

// NewWebFace makes a face that belongs to no set yet.
func NewWebFace(d FaceDesc) *WebFace {
	d.Normalize()
	return &WebFace{Desc: d}
}

// State reports the load state.
func (f *WebFace) State() LoadState {
	l := f.lockSet()
	defer l.unlock()
	return f.state
}

// Err is the failure reason of a Failed face.
func (f *WebFace) Err() error {
	l := f.lockSet()
	defer l.unlock()
	return f.err
}

// Set returns the set the face belongs to (nil before Add).
func (f *WebFace) Set() *WebSet {
	wmu.Lock()
	defer wmu.Unlock()
	return f.set
}

// wmu guards every WebSet and WebFace: loads and lookups are short and rare
// enough that one lock is simpler than per-set ones and cannot be raced by a
// face joining a set while another goroutine reports its data.
var wmu sync.Mutex

type lockHolder struct{}

func (lockHolder) unlock() { wmu.Unlock() }

func (f *WebFace) lockSet() lockHolder {
	wmu.Lock()
	return lockHolder{}
}

// BeginLoad moves an Unloaded (or Failed, for a retry) face to Loading and
// reports whether the caller should fetch it.
func (f *WebFace) BeginLoad() bool {
	l := f.lockSet()
	defer l.unlock()
	if f.state == Loading || f.state == Loaded {
		return false
	}
	f.state, f.err = Loading, nil
	return true
}

// SetData installs decoded font data (a validated sfnt, see Decode). On
// failure the face becomes Failed.
func (f *WebFace) SetData(sfnt []byte) error {
	base, err := newFace(strings.ToLower(f.Desc.Family), Style{Weight: clampW(400, f.Desc.WMin, f.Desc.WMax), Italic: f.Desc.Style != StyleNormal}, sfnt, false)
	var vr *variation
	if err == nil {
		vr = parseVariation(base)
	}
	l := f.lockSet()
	if err != nil {
		f.state, f.err = Failed, err
		l.unlock()
		f.touch(false)
		return err
	}
	f.sfnt, f.base, f.var_, f.insts = sfnt, base, vr, nil
	f.Desc.applyTo(base)
	f.state, f.err = Loaded, nil
	l.unlock()
	f.touch(true)
	return nil
}

// SetFailed marks the face as failed.
func (f *WebFace) SetFailed(err error) {
	l := f.lockSet()
	f.state, f.err = Failed, err
	l.unlock()
	f.touch(false)
}

func (f *WebFace) touch(changed bool) {
	wmu.Lock()
	s := f.set
	wmu.Unlock()
	if s != nil {
		s.faceChanged(f, changed)
	}
}

func clampW(w, lo, hi int) int {
	if w < lo {
		return lo
	}
	if w > hi {
		return hi
	}
	return w
}

// Face returns the font to draw with at the requested weight: a variable
// font is instantiated at the weight (clamped to the declared range), a static
// one is returned as is. nil until the face is Loaded.
func (f *WebFace) Face(weight int) *Face {
	l := f.lockSet()
	defer l.unlock()
	return f.faceLocked(weight, 0)
}

// FaceAt is Face with the optical size (CSS px) for a variable font's opsz axis.
func (f *WebFace) FaceAt(weight int, opsz float64) *Face {
	l := f.lockSet()
	defer l.unlock()
	return f.faceLocked(weight, opsz)
}

func (f *WebFace) faceLocked(weight int, opsz float64) *Face {
	if f.state != Loaded || f.base == nil {
		return nil
	}
	w := clampW(weight, f.Desc.WMin, f.Desc.WMax)
	if f.var_ == nil || !f.var_.hasWght() {
		return f.base
	}
	if !f.var_.hasAxis("opsz") {
		opsz = 0 // one instance per weight, whatever the size
	}
	// opsz is kept to a quarter pixel: sizes that close draw the same outlines
	key := w<<20 | int(opsz*4+0.5)
	if in := f.insts[key]; in != nil {
		return in
	}
	in := f.var_.instance(f, w, opsz)
	if f.insts == nil {
		f.insts = map[int]*Face{}
	}
	if len(f.insts) > 64 {
		f.insts = map[int]*Face{}
	}
	f.insts[key] = in
	return in
}

// ---- the set --------------------------------------------------------------------

// WebSet is the web fonts of one document.
type WebSet struct {
	id    int
	scope string

	sheet    []*WebFace // from stylesheets, in order
	script   []*WebFace // added with Add
	sheetKey string
	byFam    map[string][]*WebFace
	declGen  uint64
	gen      uint64
	closed   bool
	loader   func(*WebFace)
	onChange func()
	match    map[string]matchEnt
	order    int
}

type matchEnt struct {
	gen   uint64
	faces []*WebFace
}

var (
	regMu   sync.Mutex
	regSets = map[int]*WebSet{}
	regNext int

	webGen atomic.Uint64
)

// WebGen changes whenever the faces available to any set change. Caches of
// resolved faces key on it.
func WebGen() uint64 { return webGen.Load() }

// NewWebSet creates a set. loader is called (never with the set's lock held,
// and it must not block) once for every face that becomes necessary; the
// loader reports the outcome with SetData or SetFailed. onChange is called
// when the set of usable faces changed (a face loaded, the declarations
// changed) so that the document can be laid out again.
func NewWebSet(loader func(*WebFace), onChange func()) *WebSet {
	regMu.Lock()
	regNext++
	s := &WebSet{id: regNext, loader: loader, onChange: onChange, byFam: map[string][]*WebFace{}, match: map[string]matchEnt{}}
	s.scope = "\x00w" + strconv.Itoa(s.id)
	regSets[s.id] = s
	regMu.Unlock()
	return s
}

// Close drops the set: its scoped names stop resolving. Safe to call twice.
func (s *WebSet) Close() {
	regMu.Lock()
	delete(regSets, s.id)
	regMu.Unlock()
	wmu.Lock()
	s.closed = true
	s.sheet, s.script, s.byFam, s.match = nil, nil, map[string][]*WebFace{}, map[string]matchEnt{}
	wmu.Unlock()
	webGen.Add(1)
}

// DeclGen changes when the set of declared families or faces changes (not when
// a face merely loads). Style caches key on it.
func (s *WebSet) DeclGen() uint64 {
	if s == nil {
		return 0
	}
	wmu.Lock()
	defer wmu.Unlock()
	return s.declGen
}

// Gen changes whenever DeclGen does and whenever a face loads.
func (s *WebSet) Gen() uint64 {
	if s == nil {
		return 0
	}
	wmu.Lock()
	defer wmu.Unlock()
	return s.gen
}

// ID identifies the set in scoped names.
func (s *WebSet) ID() int { return s.id }

func (s *WebSet) reindexLocked() {
	s.byFam = map[string][]*WebFace{}
	s.match = map[string]matchEnt{}
	i := 0
	for _, list := range [][]*WebFace{s.sheet, s.script} {
		for _, f := range list {
			f.Order = i
			i++
			k := FamilyKey(f.Desc.Family)
			s.byFam[k] = append(s.byFam[k], f)
		}
	}
	s.declGen++
	s.gen++
}

// SetSheetFaces replaces the faces declared by stylesheets. Faces whose
// declaration is unchanged keep their state (a loaded font stays loaded when
// the page re-lays out). It reports whether anything changed.
func (s *WebSet) SetSheetFaces(descs []FaceDesc) bool {
	var key strings.Builder
	for i := range descs {
		descs[i].Normalize()
		key.WriteString(descs[i].Key())
		key.WriteByte('\n')
	}
	wmu.Lock()
	if s.closed || key.String() == s.sheetKey {
		wmu.Unlock()
		return false
	}
	s.sheetKey = key.String()
	old := map[string][]*WebFace{}
	for _, f := range s.sheet {
		k := f.Desc.Key()
		old[k] = append(old[k], f)
	}
	next := make([]*WebFace, 0, len(descs))
	for _, d := range descs {
		k := d.Key()
		if l := old[k]; len(l) > 0 {
			next = append(next, l[0])
			old[k] = l[1:]
			continue
		}
		f := &WebFace{Desc: d, set: s}
		next = append(next, f)
	}
	s.sheet = next
	s.reindexLocked()
	wmu.Unlock()
	webGen.Add(1)
	// no change notification: stylesheet faces are set by the layout pass that uses them
	return true
}

// Add puts a script created face (new FontFace(...)) into the set.
func (s *WebSet) Add(f *WebFace) {
	wmu.Lock()
	if s.closed {
		wmu.Unlock()
		return
	}
	for _, g := range s.script {
		if g == f {
			wmu.Unlock()
			return
		}
	}
	f.set = s
	s.script = append(s.script, f)
	s.reindexLocked()
	wmu.Unlock()
	webGen.Add(1)
	s.changed()
}

// Delete removes a script created face.
func (s *WebSet) Delete(f *WebFace) bool {
	wmu.Lock()
	found := false
	for i, g := range s.script {
		if g == f {
			s.script = append(s.script[:i:i], s.script[i+1:]...)
			found = true
			break
		}
	}
	if found {
		s.reindexLocked()
	}
	wmu.Unlock()
	if found {
		webGen.Add(1)
		s.changed()
	}
	return found
}

// Clear removes every script created face.
func (s *WebSet) Clear() {
	wmu.Lock()
	n := len(s.script)
	s.script = nil
	if n > 0 {
		s.reindexLocked()
	}
	wmu.Unlock()
	if n > 0 {
		webGen.Add(1)
		s.changed()
	}
}

// Faces lists every face, stylesheet faces first.
func (s *WebSet) Faces() []*WebFace {
	wmu.Lock()
	defer wmu.Unlock()
	return append(append([]*WebFace(nil), s.sheet...), s.script...)
}

// Pending counts faces whose load is in progress.
func (s *WebSet) Pending() int {
	if s == nil {
		return 0
	}
	wmu.Lock()
	defer wmu.Unlock()
	n := 0
	for _, f := range s.sheet {
		if f.state == Loading {
			n++
		}
	}
	for _, f := range s.script {
		if f.state == Loading {
			n++
		}
	}
	return n
}

func (s *WebSet) changed() {
	if s.onChange != nil {
		s.onChange()
	}
}

func (s *WebSet) faceChanged(f *WebFace, loaded bool) {
	wmu.Lock()
	if s.closed {
		wmu.Unlock()
		return
	}
	f.gen = s.gen
	if loaded {
		s.gen++
		s.match = map[string]matchEnt{}
	}
	wmu.Unlock()
	if loaded {
		webGen.Add(1)
	}
	s.changed()
}

// Scope returns the scoped form of family when the set declares it.
func (s *WebSet) Scope(family string) (string, bool) {
	k := FamilyKey(strings.Trim(strings.TrimSpace(family), `"'`))
	wmu.Lock()
	_, ok := s.byFam[k]
	wmu.Unlock()
	if !ok {
		return family, false
	}
	return k + s.scope, true
}

// Declares reports whether any face is declared for family.
func (s *WebSet) Declares(family string) bool {
	_, ok := s.Scope(family)
	return ok
}

// splitScoped splits "family\x00w3" into the family and set id.
func splitScoped(name string) (family string, id int, ok bool) {
	i := strings.IndexByte(name, 0)
	if i < 0 || len(name) < i+3 || name[i+1] != 'w' {
		return name, 0, false
	}
	n, err := strconv.Atoi(name[i+2:])
	if err != nil {
		return name, 0, false
	}
	return name[:i], n, true
}

// BaseFamily strips the scope of a scoped family name.
func BaseFamily(name string) string {
	f, _, _ := splitScoped(name)
	return f
}

func setOf(id int) *WebSet {
	regMu.Lock()
	defer regMu.Unlock()
	return regSets[id]
}

// HasScoped reports whether any name of the list is scoped to a web set.
func HasScoped(fams []string) bool {
	for _, f := range fams {
		if strings.IndexByte(f, 0) >= 0 {
			return true
		}
	}
	return false
}

// ---- CSS font matching ------------------------------------------------------------

// groupKey identifies the faces that differ only in unicode-range: browsers
// treat them as one composite face.
type groupKey struct {
	wmin, wmax int
	style      uint8
	smin, smax float64
}

func keyOf(d *FaceDesc) groupKey { return groupKey{d.WMin, d.WMax, d.Style, d.SMin, d.SMax} }

// matchGroup picks the best matching group of faces for weight w, normal
// stretch and the style, following CSS Fonts 4 section 5.2: stretch first,
// then style, then weight. The faces of the chosen group are returned in
// priority order: later declarations first.
func matchFaces(faces []*WebFace, st Style) []*WebFace {
	if len(faces) == 0 {
		return nil
	}
	var keys []groupKey
	seen := map[groupKey]bool{}
	for _, f := range faces {
		k := keyOf(&f.Desc)
		if !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	// stretch: normal (100%) wanted; nearest wider-than-100 is never preferred
	// over narrower only when 100 is outside the range (condensed first)
	best := keys[:0:0]
	bestD := 1e18
	for _, k := range keys {
		d := 0.0
		switch {
		case 100 < k.smin:
			d = k.smin - 100 + 1000 // wider than wanted: worse than narrower
		case 100 > k.smax:
			d = 100 - k.smax
		}
		if d < bestD-1e-9 {
			best, bestD = []groupKey{k}, d
		} else if d < bestD+1e-9 {
			best = append(best, k)
		}
	}
	keys = best
	// style
	want := uint8(StyleNormal)
	if st.Italic {
		want = StyleItalic
	}
	rank := func(have uint8) int {
		if have == want {
			return 0
		}
		if want == StyleItalic {
			if have == StyleOblique {
				return 1
			}
			return 2
		}
		if have == StyleOblique {
			return 1
		}
		return 2
	}
	bestR := 99
	best = keys[:0:0]
	for _, k := range keys {
		r := rank(k.style)
		if r < bestR {
			best, bestR = []groupKey{k}, r
		} else if r == bestR {
			best = append(best, k)
		}
	}
	keys = best
	// weight
	w := st.Weight
	if w == 0 {
		w = 400
	}
	type cand struct {
		k    groupKey
		tier int
		key  int
	}
	var cs []cand
	for _, k := range keys {
		eff := clampW(w, k.wmin, k.wmax)
		var tier, key int
		switch {
		case eff == w:
			tier, key = 0, 0
		case w >= 400 && w <= 500:
			switch {
			case eff > w && eff <= 500:
				tier, key = 1, eff
			case eff < w:
				tier, key = 2, -eff
			default:
				tier, key = 3, eff
			}
		case w < 400:
			if eff < w {
				tier, key = 1, -eff
			} else {
				tier, key = 2, eff
			}
		default:
			if eff > w {
				tier, key = 1, eff
			} else {
				tier, key = 2, -eff
			}
		}
		cs = append(cs, cand{k, tier, key})
	}
	sort.SliceStable(cs, func(i, j int) bool {
		if cs[i].tier != cs[j].tier {
			return cs[i].tier < cs[j].tier
		}
		return cs[i].key < cs[j].key
	})
	win := cs[0].k
	var out []*WebFace
	for i := len(faces) - 1; i >= 0; i-- {
		if keyOf(&faces[i].Desc) == win {
			out = append(out, faces[i])
		}
	}
	return out
}

// facesFor returns the matched group for a family (not necessarily loaded).
func (s *WebSet) facesFor(family string, st Style) []*WebFace {
	key := family + "|" + strconv.Itoa(st.Weight) + "|" + strconv.FormatBool(st.Italic)
	wmu.Lock()
	defer wmu.Unlock()
	if e, ok := s.match[key]; ok && e.gen == s.gen {
		return e.faces
	}
	fs := matchFaces(s.byFam[family], st)
	s.match[key] = matchEnt{gen: s.gen, faces: fs}
	return fs
}

// Match returns the faces CSS font matching selects for family at the style,
// in priority order (any load state). It is how document.fonts.load/check find
// what to wait for.
func (s *WebSet) Match(family string, st Style) []*WebFace {
	return s.facesFor(FamilyKey(strings.Trim(strings.TrimSpace(family), `"'`)), st)
}

// LookupAll resolves one family of a font-family list to the loaded faces of
// the matched group, in priority order. For a plain family name it is the
// single face Lookup returns. A scoped name whose faces are not loaded yet
// yields nil: the caller moves on to the next family.
func LookupAll(family string, st Style) []*Face {
	base, id, ok := splitScoped(family)
	if !ok {
		if f := Lookup(family, st); f != nil {
			return []*Face{f}
		}
		return nil
	}
	s := setOf(id)
	if s == nil {
		return nil
	}
	if st.Weight == 0 {
		st.Weight = 400
	}
	fs := s.facesFor(base, st)
	var out []*Face
	wmu.Lock()
	for _, f := range fs {
		if face := f.faceLocked(st.Weight, st.OpticalSize); face != nil {
			out = append(out, face)
		}
	}
	wmu.Unlock()
	return out
}

// Demand tells the fonts that the text needs a glyph for r. For the first
// scoped family of the list that has a face covering r (by unicode-range) that
// is not loaded yet, the load starts; later families are only considered when
// that face cannot provide the glyph (it failed, or it loaded without it).
func Demand(fams []string, st Style, r rune) {
	if st.Weight == 0 {
		st.Weight = 400
	}
	for _, fam := range fams {
		base, id, ok := splitScoped(fam)
		if !ok {
			// a system family that has the glyph ends the search too
			if f := Lookup(fam, st); f != nil && f.HasGlyph(r) {
				return
			}
			continue
		}
		s := setOf(id)
		if s == nil {
			continue
		}
		if s.demand(base, st, r) {
			return
		}
	}
}

// demand reports whether this family settles the glyph (loaded with it, or on
// its way).
func (s *WebSet) demand(base string, st Style, r rune) (settled bool) {
	fs := s.facesFor(base, st)
	var start []*WebFace
	wmu.Lock()
	for _, f := range fs {
		if !f.Desc.Covers(r) {
			continue
		}
		switch f.state {
		case Loaded:
			if face := f.faceLocked(st.Weight, st.OpticalSize); face != nil && face.HasGlyph(r) {
				wmu.Unlock()
				return true
			}
		case Loading:
			settled = true
		case Unloaded:
			start = append(start, f)
			f.state = Loading
			f.Trigger = r
			settled = true
		}
		if settled {
			break
		}
	}
	wmu.Unlock()
	for _, f := range start {
		if s.loader != nil {
			s.loader(f)
		} else {
			f.SetFailed(fmt.Errorf("no loader"))
		}
	}
	return settled
}

// DemandText demands every character of text for the family (document.fonts.load).
func (s *WebSet) DemandText(family string, st Style, text string) []*WebFace {
	base := FamilyKey(strings.Trim(strings.TrimSpace(family), `"'`))
	seen := map[*WebFace]bool{}
	var out []*WebFace
	if text == "" {
		text = " "
	}
	for _, r := range text {
		s.demand(base, st, r)
	}
	for _, f := range s.facesFor(base, st) {
		for _, r := range text {
			if f.Desc.Covers(r) && !seen[f] {
				seen[f] = true
				out = append(out, f)
			}
		}
	}
	return out
}

// DemandFamily is Demand for one family of a font-family list. It reports
// whether the family has a face for r that is loading (started now or before):
// the caller then shows r in a later family for the moment and does not ask
// the families after this one to load.
func DemandFamily(family string, st Style, r rune) (pending bool) {
	if st.Weight == 0 {
		st.Weight = 400
	}
	base, id, ok := splitScoped(family)
	if !ok {
		return false
	}
	s := setOf(id)
	if s == nil {
		return false
	}
	return s.demand(base, st, r)
}

// Redescribe replaces the descriptors of a face that has not started loading
// (FontFace attribute setters). It reports whether the change was applied.
func (f *WebFace) Redescribe(d FaceDesc) bool {
	d.Normalize()
	wmu.Lock()
	if f.state != Unloaded && f.state != Failed {
		wmu.Unlock()
		return false
	}
	f.Desc = d
	s := f.set
	if s != nil && !s.closed {
		s.reindexLocked()
	}
	wmu.Unlock()
	if s != nil {
		webGen.Add(1)
		s.changed()
	}
	return true
}

// featureOff reports whether a font-feature-settings list switches the tag off ("liga" 0, "liga" off).
func featureOff(list, tag string) bool {
	for _, item := range strings.Split(list, ",") {
		f := strings.Fields(strings.ToLower(strings.TrimSpace(item)))
		if len(f) == 2 && strings.Trim(f[0], `"'`) == tag && (f[1] == "0" || f[1] == "off") {
			return true
		}
	}
	return false
}

// applyTo copies the descriptors that act on the drawn face: unicode-range,
// size-adjust and the metric overrides.
func (d *FaceDesc) applyTo(f *Face) {
	if d.Ranges != nil {
		f.ur = d.Ranges
	}
	if d.SizeAdjust != 0 && d.SizeAdjust != 1 {
		f.adj = d.SizeAdjust
	}
	f.noLiga = featureOff(d.Features, "liga") || featureOff(d.Features, "clig")
	if d.Overrides != 0 {
		f.hasOvr = true
		f.ovrAsc, f.ovrDesc, f.ovrGap = -1, -1, -1
		if d.Overrides&OvrAscent != 0 {
			f.ovrAsc = d.AscentOverride
		}
		if d.Overrides&OvrDescent != 0 {
			f.ovrDesc = d.DescentOverride
		}
		if d.Overrides&OvrLineGap != 0 {
			f.ovrGap = d.LineGapOverride
		}
	}
}

// Retry puts a face whose load could not be attempted (the tab was in the
// background) back to Unloaded, so that the next layout asks for it again.
func (f *WebFace) Retry() {
	wmu.Lock()
	if f.state == Loading {
		f.state = Unloaded
	}
	wmu.Unlock()
}

// OpenSets is the number of web font sets that have not been closed (diagnostics and leak tests).
func OpenSets() int {
	regMu.Lock()
	defer regMu.Unlock()
	return len(regSets)
}
