//go:build linux

package x11

import (
	"unicode"

	"github.com/sweefbrowser/sweef/internal/shell"
	"github.com/sweefbrowser/sweef/internal/shell/keymap"
)

// kbdMap is the core keyboard mapping plus the modifier assignments, used to
// resolve (keycode, state) to a keysym the way XLookupKeysym does, with
// XKB's group bits (13-14 of the state) selecting the second layout.
type kbdMap struct {
	min     uint8
	kpk     int // keysyms per keycode
	syms    []uint32
	numLock uint16
	level3  uint16 // ISO_Level3_Shift (AltGr)
	modeSw  uint16 // Mode_switch
}

func (c *conn) loadKeyboard() (*kbdMap, error) {
	n := int(c.maxKeycode) - int(c.minKeycode) + 1
	b := request(opGetKbdMapping, 0, 8)
	b[4] = c.minKeycode
	b[5] = uint8(n)
	r, err := c.do(b)
	if err != nil {
		return nil, err
	}
	kpk := int(r[1])
	m := &kbdMap{min: c.minKeycode, kpk: kpk}
	cnt := int(le.Uint32(r[4:]))
	m.syms = make([]uint32, cnt)
	for i := range m.syms {
		m.syms[i] = le.Uint32(r[32+4*i:])
	}
	// modifier mapping
	r, err = c.do(request(opGetModMapping, 0, 4))
	if err != nil {
		return m, nil // mapping still usable without modifier info
	}
	kpm := int(r[1])
	for mod := 3; mod < 8 && kpm > 0; mod++ { // Mod1..Mod5
		for j := 0; j < kpm; j++ {
			kc := r[32+mod*kpm+j]
			if kc == 0 {
				continue
			}
			switch m.sym(kc, 0) {
			case 0xff7f:
				m.numLock = 1 << mod
			case 0xfe03:
				m.level3 = 1 << mod
			case 0xff7e:
				m.modeSw = 1 << mod
			}
		}
	}
	return m, nil
}

// sym returns the keysym at column col of keycode kc (0 if none).
func (m *kbdMap) sym(kc uint8, col int) uint32 {
	if m == nil || kc < m.min || col >= m.kpk {
		return 0
	}
	i := (int(kc)-int(m.min))*m.kpk + col
	if i < 0 || i >= len(m.syms) {
		return 0
	}
	return m.syms[i]
}

func isKeypadKeysym(s uint32) bool { return s >= 0xff80 && s <= 0xffbd }

// lookup resolves the keysym for a key press/release with the given state.
func (m *kbdMap) lookup(kc uint8, state uint16) uint32 {
	if m == nil {
		return 0
	}
	group := int(state>>13) & 3
	base := 0
	switch {
	case m.level3 != 0 && state&m.level3 != 0 && m.kpk >= 6 && (m.sym(kc, 4) != 0 || m.sym(kc, 5) != 0):
		base = 4
	case (m.modeSw != 0 && state&m.modeSw != 0 || m.level3 != 0 && state&m.level3 != 0) && m.kpk >= 4:
		base = 2
	case group == 1 && m.kpk >= 4:
		base = 2
	}
	s0 := m.sym(kc, base)
	s1 := m.sym(kc, base+1)
	if s0 == 0 && base != 0 {
		s0, s1 = m.sym(kc, 0), m.sym(kc, 1)
	}
	if s1 == 0 {
		s1 = s0
		if r, ok := keymap.KeysymToRune(s0); ok && unicode.IsLower(r) && s0 < 0x01000000 {
			if u := unicode.ToUpper(r); u != r {
				s1 = upperKeysym(s0, u)
			}
		}
	}
	shift := state&1 != 0
	lock := state&2 != 0
	if isKeypadKeysym(s0) || isKeypadKeysym(s1) {
		numlock := m.numLock != 0 && state&m.numLock != 0
		if numlock != shift {
			return s1
		}
		return s0
	}
	use := shift
	if lock {
		if r, ok := keymap.KeysymToRune(s0); ok && (unicode.IsLower(r) || unicode.IsUpper(r)) {
			use = !shift
		}
	}
	if use {
		return s1
	}
	return s0
}

func upperKeysym(orig uint32, upper rune) uint32 {
	if orig < 0x100 {
		return uint32(upper)
	}
	return 0x01000000 | uint32(upper)
}

func modsFromState(st uint16) shell.Mods {
	var m shell.Mods
	if st&1 != 0 {
		m |= shell.ModShift
	}
	if st&4 != 0 {
		m |= shell.ModCtrl
	}
	if st&8 != 0 {
		m |= shell.ModAlt
	}
	if st&64 != 0 {
		m |= shell.ModMeta
	}
	return m
}

// modBitFor returns the shell modifier flag a modifier key itself toggles.
func modBitFor(key string) shell.Mods {
	switch key {
	case "Shift":
		return shell.ModShift
	case "Control":
		return shell.ModCtrl
	case "Alt":
		return shell.ModAlt
	case "Meta":
		return shell.ModMeta
	}
	return 0
}

// keyTranslator turns key presses into DOM key/code/text, keeping dead-key
// state between events.
type keyTranslator struct {
	deadComb  rune
	deadSpace rune
}

// translate handles a key press. It returns the DOM key name and the text
// to deliver (possibly empty).
func (t *keyTranslator) translate(ks uint32, mods shell.Mods) (key, text string) {
	if comb, space, ok := keymap.DeadKeysym(ks); ok {
		if t.deadComb != 0 {
			// second dead key: emit the first accent, keep the new one
			text = string(t.deadSpace)
		}
		t.deadComb, t.deadSpace = comb, space
		return "Dead", text
	}
	if name, ok := keymap.KeysymKey(ks); ok {
		if !keymap.IsModifierKeysym(ks) && t.deadComb != 0 && name != "Shift" {
			// a non-text key cancels a pending dead key
			t.deadComb = 0
		}
		return name, ""
	}
	r, ok := keymap.KeysymToRune(ks)
	if !ok {
		return "Unidentified", ""
	}
	key = string(r)
	if mods&(shell.ModCtrl|shell.ModAlt|shell.ModMeta) != 0 || !keymap.Printable(r) {
		t.deadComb = 0
		return key, ""
	}
	if t.deadComb != 0 {
		comb, space := t.deadComb, t.deadSpace
		t.deadComb = 0
		if r == ' ' {
			return key, string(space)
		}
		if s := keymap.ComposeDead(comb, r); s != "" {
			return key, s
		}
		return key, string(space) + string(r)
	}
	return key, key
}

// setupInput loads the keyboard map and asks XKB for detectable auto-repeat
// (so held keys produce repeated KeyPress events without synthetic
// KeyRelease in between).
func (w *Window) setupInput() {
	if km, err := w.c.loadKeyboard(); err == nil {
		w.kb.Store(km)
	}
	ext, err := w.c.queryExtension("XKEYBOARD")
	if err != nil || !ext.present {
		return
	}
	b := request(ext.major, 0, 8) // UseExtension
	le.PutUint16(b[4:], 1)
	le.PutUint16(b[6:], 0)
	if r, err := w.c.do(b); err != nil || r[1] == 0 {
		return
	}
	f := request(ext.major, 21, 28) // PerClientFlags
	le.PutUint16(f[4:], 0x100)      // XkbUseCoreKbd
	le.PutUint32(f[8:], 1)          // change: DetectableAutoRepeat
	le.PutUint32(f[12:], 1)         // value
	w.c.do(f)
}
