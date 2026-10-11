package font

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/gobolditalic"
	"golang.org/x/image/font/gofont/goitalic"
	"golang.org/x/image/font/gofont/gomono"
	"golang.org/x/image/font/gofont/gomonobold"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/sfnt"
)

type fontCatalog struct {
	families      map[string][]*source // lower-cased family -> faces
	fallbackOrder []string
}

var (
	catOnce sync.Once
	cat     *fontCatalog
	// extraDirs are added by SWEEF_FONT_DIRS (colon separated).
	embedded = map[string][]byte{}
)

func catalog() *fontCatalog {
	catOnce.Do(func() { cat = scan() })
	return cat
}

// Families lists the family names available, for diagnostics.
func Families() []string {
	c := catalog()
	out := make([]string, 0, len(c.families))
	for _, fam := range c.families {
		out = append(out, fam[0].family)
	}
	sort.Strings(out)
	return out
}

func fontDirs() []string {
	dirs := []string{"/usr/share/fonts", "/usr/local/share/fonts", "/usr/X11R6/lib/X11/fonts",
		"/Library/Fonts", "/System/Library/Fonts", `C:\Windows\Fonts`}
	if h, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(h, ".fonts"), filepath.Join(h, ".local/share/fonts"), filepath.Join(h, "Library/Fonts"))
	}
	if v := os.Getenv("SWEEF_FONT_DIRS"); v != "" {
		dirs = append(filepath.SplitList(v), dirs...)
	}
	return dirs
}

func scan() *fontCatalog {
	c := &fontCatalog{families: map[string][]*source{}}
	add := func(s *source) {
		k := strings.ToLower(s.family)
		c.families[k] = append(c.families[k], s)
	}
	if os.Getenv("SWEEF_FONTS_EMBEDDED_ONLY") == "" {
		seen := map[string]bool{}
		for _, d := range fontDirs() {
			_ = filepath.WalkDir(d, func(p string, e os.DirEntry, err error) error {
				if err != nil || e.IsDir() {
					return nil
				}
				ext := strings.ToLower(filepath.Ext(p))
				if ext != ".ttf" && ext != ".otf" && ext != ".ttc" && ext != ".otc" {
					return nil
				}
				if rp, err := filepath.EvalSymlinks(p); err == nil {
					if seen[rp] {
						return nil
					}
					seen[rp] = true
				}
				for _, s := range describe(p) {
					add(s)
				}
				return nil
			})
		}
	}
	// Embedded Go fonts: always present, registered as family "go" (and
	// "go mono") so the catalog is never empty.
	for name, data := range map[string][]byte{
		"go/regular": goregular.TTF, "go/bold": gobold.TTF, "go/italic": goitalic.TTF,
		"go/bolditalic": gobolditalic.TTF, "gomono/regular": gomono.TTF, "gomono/bold": gomonobold.TTF,
	} {
		embedded[name] = data
		for _, s := range describeBytes("embedded:"+name, data) {
			add(s)
		}
	}
	if _, ok := c.families["go"]; !ok {
		c.families["go"] = c.families[strings.ToLower(firstFamily(c))]
	}
	// Fallback order: common Latin families first so punctuation and
	// symbols come from a face that looks like the text, then everything
	// else alphabetically, colour fonts last.
	pref := []string{"liberation sans", "dejavu sans", "liberation serif", "dejavu serif", "freesans", "freeserif",
		"noto sans", "noto sans cjk sc", "wenquanyi zen hei", "droid sans fallback", "dejavu sans mono", "freemono", "go"}
	added := map[string]bool{}
	for _, p := range pref {
		if _, ok := c.families[p]; ok && !added[p] {
			c.fallbackOrder = append(c.fallbackOrder, p)
			added[p] = true
		}
	}
	var rest, colour []string
	for k, fam := range c.families {
		if added[k] {
			continue
		}
		if fam[0].color {
			colour = append(colour, k)
		} else {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	sort.Strings(colour)
	c.fallbackOrder = append(append(c.fallbackOrder, rest...), colour...)
	return c
}

func firstFamily(c *fontCatalog) string {
	for _, f := range c.families {
		return f[0].family
	}
	return ""
}

func describe(path string) []*source {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return describeBytes(path, data)
}

func describeBytes(path string, data []byte) []*source {
	var fonts []*sfnt.Font
	if bytes.HasPrefix(data, []byte("ttcf")) {
		coll, err := sfnt.ParseCollection(data)
		if err != nil {
			return nil
		}
		for i := 0; i < coll.NumFonts(); i++ {
			f, err := coll.Font(i)
			if err != nil {
				return nil
			}
			fonts = append(fonts, f)
		}
	} else {
		f, err := sfnt.Parse(data)
		if err != nil {
			return nil
		}
		fonts = []*sfnt.Font{f}
	}
	var out []*source
	var b sfnt.Buffer
	for i, f := range fonts {
		fam, _ := f.Name(&b, sfnt.NameIDTypographicFamily)
		if fam == "" {
			fam, _ = f.Name(&b, sfnt.NameIDFamily)
		}
		sub, _ := f.Name(&b, sfnt.NameIDTypographicSubfamily)
		if sub == "" {
			sub, _ = f.Name(&b, sfnt.NameIDSubfamily)
		}
		if fam == "" {
			continue
		}
		os2 := readOS2(data, i)
		s := &source{family: fam, sub: sub, file: path, idx: i, weight: 400}
		ls := strings.ToLower(sub)
		if os2.ok && os2.weight >= 100 && os2.weight <= 1000 {
			s.weight = int(os2.weight)
		} else if strings.Contains(ls, "bold") {
			s.weight = 700
		}
		s.italic = (os2.ok && os2.fsSelection&1 != 0) || strings.Contains(ls, "italic") || strings.Contains(ls, "oblique")
		s.color = hasTable(data, i, "CBDT") || hasTable(data, i, "sbix")
		out = append(out, s)
		// A face also answers to its legacy (non-typographic) family name,
		// e.g. "Inter Black" or "DejaVu Sans Condensed".
		if legacy, _ := f.Name(&b, sfnt.NameIDFamily); legacy != "" && !strings.EqualFold(legacy, fam) {
			out = append(out, &source{family: legacy, sub: s.sub, file: s.file, idx: s.idx, weight: s.weight, italic: s.italic, color: s.color})
		}
	}
	return out
}

func openSource(s *source) (*sfnt.Font, []byte, error) {
	var data []byte
	if strings.HasPrefix(s.file, "embedded:") {
		data = embedded[strings.TrimPrefix(s.file, "embedded:")]
	} else {
		var err error
		if data, err = os.ReadFile(s.file); err != nil {
			return nil, nil, err
		}
	}
	if bytes.HasPrefix(data, []byte("ttcf")) {
		coll, err := sfnt.ParseCollection(data)
		if err != nil {
			return nil, nil, err
		}
		f, err := coll.Font(s.idx)
		return f, data, err
	}
	f, err := sfnt.Parse(data)
	return f, data, err
}

// ---- raw table access (sfnt does not expose OS/2 fields we need)

type os2Info struct {
	ok                         bool
	weight                     uint16
	fsSelection                uint16
	typoAsc, typoDesc, typoGap int16
	winAsc, winDesc            uint16
	useTypo                    bool
}

func tableDir(data []byte, idx int) (int, bool) {
	if bytes.HasPrefix(data, []byte("ttcf")) {
		if len(data) < 12+4*(idx+1) {
			return 0, false
		}
		return int(binary.BigEndian.Uint32(data[12+4*idx:])), true
	}
	return 0, true
}

func findTable(data []byte, idx int, tag string) ([]byte, error) {
	off, ok := tableDir(data, idx)
	if !ok || len(data) < off+12 {
		return nil, errors.New("bad font")
	}
	n := int(binary.BigEndian.Uint16(data[off+4:]))
	for i := 0; i < n; i++ {
		r := off + 12 + 16*i
		if len(data) < r+16 {
			break
		}
		if string(data[r:r+4]) == tag {
			o := int(binary.BigEndian.Uint32(data[r+8:]))
			l := int(binary.BigEndian.Uint32(data[r+12:]))
			if o+l > len(data) || o < 0 || l < 0 {
				return nil, errors.New("bad table")
			}
			return data[o : o+l], nil
		}
	}
	return nil, errors.New("no table")
}

func hasTable(data []byte, idx int, tag string) bool {
	_, err := findTable(data, idx, tag)
	return err == nil
}

func readOS2(data []byte, idx int) os2Info {
	t, err := findTable(data, idx, "OS/2")
	if err != nil || len(t) < 78 {
		return os2Info{}
	}
	be := binary.BigEndian
	o := os2Info{ok: true}
	o.weight = be.Uint16(t[4:])
	o.fsSelection = be.Uint16(t[62:])
	o.typoAsc = int16(be.Uint16(t[68:]))
	o.typoDesc = int16(be.Uint16(t[70:]))
	o.typoGap = int16(be.Uint16(t[72:]))
	o.winAsc = be.Uint16(t[74:])
	o.winDesc = be.Uint16(t[76:])
	o.useTypo = o.fsSelection&(1<<7) != 0
	return o
}
