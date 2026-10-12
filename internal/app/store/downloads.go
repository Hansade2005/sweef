package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const downloadsFile = "downloads.jsonl"

// Download states.
const (
	DLProgress = "progress"
	DLPaused   = "paused"
	DLDone     = "done"
	DLCanceled = "canceled"
	DLError    = "error"
)

// DownloadItem is one download.
type DownloadItem struct {
	ID        string    `json:"id"`
	URL       string    `json:"url"`
	Name      string    `json:"name"`
	Path      string    `json:"path"`
	Mime      string    `json:"mime,omitempty"`
	Total     int64     `json:"total"` // -1 unknown
	Received  int64     `json:"received"`
	State     string    `json:"state"`
	Error     string    `json:"error,omitempty"`
	Started   time.Time `json:"started"`
	Incognito bool      `json:"-"`

	cancel context.CancelFunc
	done   chan struct{} // closed when the transfer goroutine exits
	cookie string
	ua     string
}

// Downloads tracks downloads and runs the streaming downloader. Progress,
// pause (resume with a Range request) and cancel are real: they act on the
// HTTP body being read, not on a fake counter.
type Downloads struct {
	b      Backend
	mu     sync.Mutex
	items  map[string]*DownloadItem
	order  []string
	next   int
	Dir    string
	Client *http.Client
	// OnUpdate is called (without locks) after every state change; progress
	// updates are throttled to ~10/s per item.
	OnUpdate func(DownloadItem)
}

func OpenDownloads(b Backend) *Downloads {
	d := &Downloads{b: b, items: map[string]*DownloadItem{}, Client: &http.Client{}}
	if data, err := b.ReadFile(downloadsFile); err == nil {
		forEachLine(data, func(l []byte) {
			var it DownloadItem
			if json.Unmarshal(l, &it) == nil && it.ID != "" {
				if it.State == "removed" {
					delete(d.items, it.ID)
					for i, o := range d.order {
						if o == it.ID {
							d.order = append(d.order[:i], d.order[i+1:]...)
							break
						}
					}
					return
				}
				if _, ok := d.items[it.ID]; !ok {
					d.order = append(d.order, it.ID)
				}
				if it.State == DLProgress { // interrupted by exit
					it.State = DLPaused
				}
				cp := it
				d.items[it.ID] = &cp
				var n int
				fmt.Sscanf(it.ID, "d%d", &n)
				if n > d.next {
					d.next = n
				}
			}
		})
	}
	return d
}

func (d *Downloads) persist(it *DownloadItem) {
	b, _ := json.Marshal(it)
	_ = d.b.Append(downloadsFile, b)
}

func (d *Downloads) notify(it DownloadItem) {
	if d.OnUpdate != nil {
		d.OnUpdate(it)
	}
}

// List returns downloads, newest first.
func (d *Downloads) List() []DownloadItem {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]DownloadItem, 0, len(d.order))
	for i := len(d.order) - 1; i >= 0; i-- {
		out = append(out, *d.items[d.order[i]])
	}
	return out
}

// Get returns one item.
func (d *Downloads) Get(id string) (DownloadItem, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if it := d.items[id]; it != nil {
		return *it, true
	}
	return DownloadItem{}, false
}

// Record adds an already-finished download (e.g. one the engine saved).
func (d *Downloads) Record(u, name, p string, size int64, mime string) DownloadItem {
	d.mu.Lock()
	d.next++
	it := &DownloadItem{ID: fmt.Sprintf("d%d", d.next), URL: u, Name: name, Path: p, Mime: mime, Total: size,
		Received: size, State: DLDone, Started: time.Now()}
	d.items[it.ID] = it
	d.order = append(d.order, it.ID)
	d.persist(it)
	cp := *it
	d.mu.Unlock()
	d.notify(cp)
	return cp
}

// Start begins a streaming download of rawurl into dir. cookie/ua are sent
// as the requesting tab would.
func (d *Downloads) Start(rawurl, name, dir, cookie, ua string) (DownloadItem, error) {
	u, err := url.Parse(rawurl)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return DownloadItem{}, errors.New("unsupported download url")
	}
	if dir == "" {
		dir = d.Dir
	}
	if dir == "" {
		dir = filepath.Join(os.TempDir(), "sweef-downloads")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return DownloadItem{}, err
	}
	if name == "" {
		name = path.Base(u.Path)
	}
	name = SafeFilename(name)
	p := uniquePath(filepath.Join(dir, name))
	d.mu.Lock()
	d.next++
	it := &DownloadItem{ID: fmt.Sprintf("d%d", d.next), URL: rawurl, Name: filepath.Base(p), Path: p, Total: -1,
		State: DLProgress, Started: time.Now(), cookie: cookie, ua: ua}
	d.items[it.ID] = it
	d.order = append(d.order, it.ID)
	d.mu.Unlock()
	d.run(it)
	return *it, nil
}

// SafeFilename strips path separators and control characters.
func SafeFilename(n string) string {
	n = strings.Map(func(r rune) rune {
		if r < 0x20 || strings.ContainsRune(`/\:*?"<>|`, r) {
			return '_'
		}
		return r
	}, n)
	n = strings.Trim(n, ". ")
	if n == "" {
		n = "download"
	}
	return n
}

func uniquePath(p string) string {
	if _, err := os.Stat(p); errors.Is(err, os.ErrNotExist) {
		return p
	}
	ext := filepath.Ext(p)
	base := strings.TrimSuffix(p, ext)
	for i := 1; ; i++ {
		q := fmt.Sprintf("%s (%d)%s", base, i, ext)
		if _, err := os.Stat(q); errors.Is(err, os.ErrNotExist) {
			return q
		}
	}
}

func (d *Downloads) run(it *DownloadItem) {
	ctx, cancel := context.WithCancel(context.Background())
	d.mu.Lock()
	prev := it.done
	d.mu.Unlock()
	if prev != nil {
		<-prev // the previous transfer must have stopped writing before we continue the file
	}
	d.mu.Lock()
	it.cancel = cancel
	it.State = DLProgress
	start := it.Received
	done := make(chan struct{})
	it.done = done
	d.mu.Unlock()
	d.notify(*it)
	go func() {
		defer close(done)
		err := d.fetch(ctx, it, start)
		d.mu.Lock()
		switch {
		case it.State == DLPaused || it.State == DLCanceled:
			// state set by Pause/Cancel
		case err != nil:
			it.State, it.Error = DLError, err.Error()
		default:
			it.State = DLDone
		}
		d.persist(it)
		cp := *it
		d.mu.Unlock()
		d.notify(cp)
	}()
}

func (d *Downloads) fetch(ctx context.Context, it *DownloadItem, from int64) error {
	req, err := http.NewRequestWithContext(ctx, "GET", it.URL, nil)
	if err != nil {
		return err
	}
	if it.ua != "" {
		req.Header.Set("User-Agent", it.ua)
	}
	if it.cookie != "" {
		req.Header.Set("Cookie", it.cookie)
	}
	if from > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", from))
	}
	resp, err := d.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	flags := os.O_CREATE | os.O_WRONLY
	switch {
	case resp.StatusCode == http.StatusPartialContent && from > 0:
		flags |= os.O_APPEND
	case resp.StatusCode == http.StatusOK:
		flags |= os.O_TRUNC
		from = 0
	default:
		return fmt.Errorf("http %d", resp.StatusCode)
	}
	f, err := os.OpenFile(it.Path, flags, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if flags&os.O_APPEND != 0 {
		if err := f.Truncate(from); err != nil { // drop anything past what we counted
			return err
		}
	}
	d.mu.Lock()
	it.Mime = resp.Header.Get("Content-Type")
	it.Received = from
	if resp.ContentLength >= 0 {
		it.Total = from + resp.ContentLength
	}
	d.mu.Unlock()
	buf := make([]byte, 32<<10)
	last := time.Now()
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				return werr
			}
			d.mu.Lock()
			it.Received += int64(n)
			var cp DownloadItem
			tick := time.Since(last) > 100*time.Millisecond
			if tick {
				cp, last = *it, time.Now()
			}
			d.mu.Unlock()
			if tick {
				d.notify(cp)
			}
		}
		if rerr == io.EOF {
			return nil
		}
		if rerr != nil {
			return rerr
		}
	}
}

// Pause stops the transfer, keeping the partial file for Resume.
func (d *Downloads) Pause(id string) bool {
	d.mu.Lock()
	it := d.items[id]
	if it == nil || it.State != DLProgress {
		d.mu.Unlock()
		return false
	}
	it.State = DLPaused
	cancel := it.cancel
	d.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return true
}

// Resume continues a paused download with a Range request.
func (d *Downloads) Resume(id string) bool {
	d.mu.Lock()
	it := d.items[id]
	if it == nil || (it.State != DLPaused && it.State != DLError) {
		d.mu.Unlock()
		return false
	}
	d.mu.Unlock()
	d.run(it)
	return true
}

// Cancel aborts and deletes the partial file.
func (d *Downloads) Cancel(id string) bool {
	d.mu.Lock()
	it := d.items[id]
	if it == nil || it.State == DLDone || it.State == DLCanceled {
		d.mu.Unlock()
		return false
	}
	it.State = DLCanceled
	cancel := it.cancel
	p := it.Path
	d.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	// let the transfer goroutine release the file first
	go func() {
		time.Sleep(50 * time.Millisecond)
		os.Remove(p)
	}()
	d.mu.Lock()
	d.persist(it)
	cp := *it
	d.mu.Unlock()
	d.notify(cp)
	return true
}

// Remove drops an entry from the list (not the file).
func (d *Downloads) Remove(id string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.items, id)
	for i, o := range d.order {
		if o == id {
			d.order = append(d.order[:i], d.order[i+1:]...)
			break
		}
	}
	it := DownloadItem{ID: id, State: "removed"}
	b, _ := json.Marshal(it)
	_ = d.b.Append(downloadsFile, b)
}
