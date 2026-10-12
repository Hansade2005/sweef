package app

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sweefbrowser/sweef/internal/app/state"
	"github.com/sweefbrowser/sweef/internal/app/store"
)

// faviconCache keeps data: URLs of site icons in memory and (for normal
// profiles) on disk, so the chrome page never has to fetch anything.
type faviconCache struct {
	mu   sync.Mutex
	mem  map[string]string // icon URL -> data URL
	dir  string
	cli  *http.Client
	fail map[string]time.Time
}

const maxFaviconBytes = 128 << 10

func newFaviconCache(p *store.Profile) *faviconCache {
	c := &faviconCache{mem: map[string]string{}, fail: map[string]time.Time{}, cli: &http.Client{Timeout: 6 * time.Second}}
	if !p.Incognito && p.Dir != "" {
		c.dir = filepath.Join(p.Dir, "favicons")
	}
	return c
}

func (c *faviconCache) get(iconURL string) (string, bool) {
	c.mu.Lock()
	if v, ok := c.mem[iconURL]; ok {
		c.mu.Unlock()
		return v, true
	}
	if t, ok := c.fail[iconURL]; ok && time.Since(t) < 10*time.Minute {
		c.mu.Unlock()
		return "", true // known failure: do not retry soon
	}
	dir := c.dir
	c.mu.Unlock()
	if dir != "" {
		sum := sha256.Sum256([]byte(iconURL))
		if b, err := os.ReadFile(filepath.Join(dir, hex.EncodeToString(sum[:8]))); err == nil && len(b) > 0 {
			c.put(iconURL, string(b), false)
			return string(b), true
		}
	}
	return "", false
}

func (c *faviconCache) put(iconURL, dataURL string, persist bool) {
	c.mu.Lock()
	if len(c.mem) > 256 {
		c.mem = map[string]string{}
	}
	c.mem[iconURL] = dataURL
	dir := c.dir
	c.mu.Unlock()
	if persist && dir != "" {
		_ = os.MkdirAll(dir, 0o700)
		sum := sha256.Sum256([]byte(iconURL))
		_ = os.WriteFile(filepath.Join(dir, hex.EncodeToString(sum[:8])), []byte(dataURL), 0o600)
	}
}

// fetchFavicon resolves and loads the icon for pageURL in the background.
func (a *App) fetchFavicon(e *etab, pageURL, href string) {
	base, err := url.Parse(pageURL)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") {
		return
	}
	icon := base.Scheme + "://" + base.Host + "/favicon.ico"
	if href != "" {
		if ref, err := url.Parse(href); err == nil {
			icon = base.ResolveReference(ref).String()
		}
	}
	id := e.id
	if v, ok := a.favicons.get(icon); ok {
		if v != "" {
			a.setFavicon(id, pageURL, v)
		}
		return
	}
	if a.priv.Check(pageURL, icon).Block {
		return
	}
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		data, err := a.favicons.download(icon, e.t.UserAgent())
		if err != nil {
			a.favicons.mu.Lock()
			a.favicons.fail[icon] = time.Now()
			a.favicons.mu.Unlock()
			return
		}
		a.favicons.put(icon, data, !e.incog)
		a.post(func() { a.setFavicon(id, pageURL, data) })
	}()
}

func (a *App) setFavicon(id, pageURL, data string) {
	st, ok := a.model.Tab(id)
	if !ok || (st.URL != pageURL && !strings.HasPrefix(st.URL, strings.TrimSuffix(pageURL, "/"))) {
		return
	}
	a.model.Update(id, func(t *state.Tab) { t.Favicon = data })
	a.markState()
}

func (c *faviconCache) download(iconURL, ua string) (string, error) {
	if strings.HasPrefix(iconURL, "data:") {
		return iconURL, nil
	}
	req, err := http.NewRequest("GET", iconURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", ua)
	resp, err := c.cli.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", io.ErrUnexpectedEOF
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxFaviconBytes))
	if err != nil || len(b) == 0 {
		return "", io.ErrUnexpectedEOF
	}
	mt := http.DetectContentType(b)
	if p, ok := icoToPNG(b); ok {
		b, mt = p, "image/png"
	} else if ct := resp.Header.Get("Content-Type"); strings.HasPrefix(ct, "image/svg") || strings.Contains(string(b[:min(len(b), 256)]), "<svg") {
		mt = "image/svg+xml"
	}
	if !strings.HasPrefix(mt, "image/") {
		return "", io.ErrUnexpectedEOF
	}
	return "data:" + mt + ";base64," + base64.StdEncoding.EncodeToString(b), nil
}

// icoToPNG extracts the largest PNG-encoded image from an .ico file.
func icoToPNG(b []byte) ([]byte, bool) {
	if len(b) < 22 || b[0] != 0 || b[1] != 0 || b[2] != 1 || b[3] != 0 {
		return nil, false
	}
	n := int(binary.LittleEndian.Uint16(b[4:]))
	var best []byte
	bestW := -1
	for i := 0; i < n && 6+16*(i+1) <= len(b); i++ {
		ent := b[6+16*i:]
		w := int(ent[0])
		if w == 0 {
			w = 256
		}
		size := int(binary.LittleEndian.Uint32(ent[8:]))
		off := int(binary.LittleEndian.Uint32(ent[12:]))
		if off < 0 || size <= 8 || off+size > len(b) {
			continue
		}
		data := b[off : off+size]
		if string(data[:8]) == "\x89PNG\r\n\x1a\n" && w > bestW {
			best, bestW = data, w
		}
	}
	return best, best != nil
}
