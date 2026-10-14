package store

import (
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const bookmarksFile = "bookmarks.jsonl"

// Well-known folder ids.
const (
	FolderRoot  = "root"
	FolderBar   = "bar"
	FolderOther = "other"
)

// Bookmark is a bookmark or a folder (URL == "" and Folder == true).
type Bookmark struct {
	ID     string    `json:"id"`
	Parent string    `json:"parent"`
	Title  string    `json:"title"`
	URL    string    `json:"url,omitempty"`
	Folder bool      `json:"folder,omitempty"`
	Added  time.Time `json:"added"`
	Pos    int       `json:"pos"`
}

type bRec struct {
	Op string `json:"op"` // add | del | upd | mv
	Bookmark
}

// Bookmarks is a folder tree with O(1) "is this URL bookmarked".
type Bookmarks struct {
	b     Backend
	mu    sync.RWMutex
	nodes map[string]*Bookmark
	byURL map[string]int
	next  int
	lines int
}

func OpenBookmarks(b Backend) *Bookmarks {
	bm := &Bookmarks{b: b, nodes: map[string]*Bookmark{}, byURL: map[string]int{}}
	bm.nodes[FolderRoot] = &Bookmark{ID: FolderRoot, Title: "Root", Folder: true}
	bm.nodes[FolderBar] = &Bookmark{ID: FolderBar, Parent: FolderRoot, Title: "Bookmarks bar", Folder: true}
	bm.nodes[FolderOther] = &Bookmark{ID: FolderOther, Parent: FolderRoot, Title: "Other bookmarks", Folder: true, Pos: 1}
	if data, err := b.ReadFile(bookmarksFile); err == nil {
		forEachLine(data, func(l []byte) {
			var r bRec
			if json.Unmarshal(l, &r) == nil {
				bm.lines++
				bm.apply(r)
			}
		})
	}
	if bm.lines > 200 && bm.lines > 3*len(bm.nodes) {
		bm.compactLocked()
	}
	return bm
}

func (bm *Bookmarks) apply(r bRec) {
	switch r.Op {
	case "add":
		n := r.Bookmark
		bm.nodes[n.ID] = &n
		if n.URL != "" {
			bm.byURL[n.URL]++
		}
		if v, err := strconv.Atoi(strings.TrimPrefix(n.ID, "b")); err == nil && v > bm.next {
			bm.next = v
		}
	case "del":
		bm.removeTree(r.ID)
	case "upd", "mv":
		if n := bm.nodes[r.ID]; n != nil {
			if n.URL != "" {
				bm.byURL[n.URL]--
			}
			*n = r.Bookmark
			if n.URL != "" {
				bm.byURL[n.URL]++
			}
		}
	}
}

func (bm *Bookmarks) removeTree(id string) {
	if id == FolderRoot || id == FolderBar || id == FolderOther {
		return
	}
	n := bm.nodes[id]
	if n == nil {
		return
	}
	if n.Folder {
		for cid, c := range bm.nodes {
			if c.Parent == id {
				bm.removeTree(cid)
			}
		}
	}
	if n.URL != "" {
		if bm.byURL[n.URL]--; bm.byURL[n.URL] <= 0 {
			delete(bm.byURL, n.URL)
		}
	}
	delete(bm.nodes, id)
}

func (bm *Bookmarks) log(op string, n Bookmark) {
	b, _ := json.Marshal(bRec{Op: op, Bookmark: n})
	bm.lines++
	_ = bm.b.Append(bookmarksFile, b)
}

func (bm *Bookmarks) compactLocked() {
	var buf []byte
	ids := make([]string, 0, len(bm.nodes))
	for id := range bm.nodes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		b, _ := json.Marshal(bRec{Op: "add", Bookmark: *bm.nodes[id]})
		buf = append(append(buf, b...), '\n')
	}
	if bm.b.WriteAtomic(bookmarksFile, buf) == nil {
		bm.lines = len(bm.nodes)
	}
}

// IsBookmarked reports whether url has a bookmark.
func (bm *Bookmarks) IsBookmarked(url string) bool {
	bm.mu.RLock()
	defer bm.mu.RUnlock()
	return bm.byURL[url] > 0
}

func (bm *Bookmarks) nextID() string { bm.next++; return "b" + strconv.Itoa(bm.next) }

func (bm *Bookmarks) childCount(parent string) int {
	c := 0
	for _, n := range bm.nodes {
		if n.Parent == parent {
			c++
		}
	}
	return c
}

// Add creates a bookmark in folder (default: the bookmarks bar).
func (bm *Bookmarks) Add(folder, title, url string) (Bookmark, error) {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	if folder == "" {
		folder = FolderBar
	}
	if p := bm.nodes[folder]; p == nil || !p.Folder {
		return Bookmark{}, errors.New("no such folder")
	}
	if url == "" {
		return Bookmark{}, errors.New("empty url")
	}
	n := Bookmark{ID: bm.nextID(), Parent: folder, Title: title, URL: url, Added: time.Now().Truncate(time.Second), Pos: bm.childCount(folder)}
	bm.nodes[n.ID] = &n
	bm.byURL[url]++
	bm.log("add", n)
	return n, nil
}

// AddFolder creates a folder.
func (bm *Bookmarks) AddFolder(parent, title string) (Bookmark, error) {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	if parent == "" {
		parent = FolderBar
	}
	if p := bm.nodes[parent]; p == nil || !p.Folder {
		return Bookmark{}, errors.New("no such folder")
	}
	n := Bookmark{ID: bm.nextID(), Parent: parent, Title: title, Folder: true, Added: time.Now().Truncate(time.Second), Pos: bm.childCount(parent)}
	bm.nodes[n.ID] = &n
	bm.log("add", n)
	return n, nil
}

// Toggle removes every bookmark of url if there is one, else adds it to the
// bar. It returns the new state.
func (bm *Bookmarks) Toggle(title, url string) (bool, error) {
	bm.mu.Lock()
	if bm.byURL[url] > 0 {
		var ids []string
		for id, n := range bm.nodes {
			if n.URL == url {
				ids = append(ids, id)
			}
		}
		for _, id := range ids {
			bm.removeTree(id)
			bm.log("del", Bookmark{ID: id})
		}
		bm.mu.Unlock()
		return false, nil
	}
	bm.mu.Unlock()
	_, err := bm.Add(FolderBar, title, url)
	return err == nil, err
}

// Remove deletes a bookmark or a folder subtree.
func (bm *Bookmarks) Remove(id string) bool {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	if bm.nodes[id] == nil || id == FolderRoot || id == FolderBar || id == FolderOther {
		return false
	}
	bm.removeTree(id)
	bm.log("del", Bookmark{ID: id})
	return true
}

// Move re-parents id into folder at position pos.
func (bm *Bookmarks) Move(id, folder string, pos int) error {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	n, p := bm.nodes[id], bm.nodes[folder]
	if n == nil || p == nil || !p.Folder || id == FolderRoot {
		return errors.New("bad move")
	}
	for a := folder; a != ""; a = bm.nodes[a].Parent { // no cycles
		if a == id {
			return errors.New("cannot move a folder into itself")
		}
		if bm.nodes[a] == nil {
			break
		}
	}
	n.Parent, n.Pos = folder, pos
	bm.log("mv", *n)
	return nil
}

// Rename changes the title.
func (bm *Bookmarks) Rename(id, title string) bool {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	n := bm.nodes[id]
	if n == nil {
		return false
	}
	n.Title = title
	bm.log("upd", *n)
	return true
}

// Edit changes the title and, for a bookmark (not a folder), the address.
func (bm *Bookmarks) Edit(id, title, rawurl string) bool {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	n := bm.nodes[id]
	if n == nil || id == FolderRoot || id == FolderBar || id == FolderOther {
		return false
	}
	if title != "" {
		n.Title = title
	}
	if !n.Folder && rawurl != "" && rawurl != n.URL {
		bm.byURL[n.URL]--
		n.URL = rawurl
		bm.byURL[n.URL]++
	}
	bm.log("upd", *n)
	return true
}

// Children lists a folder in position order.
func (bm *Bookmarks) Children(folder string) []Bookmark {
	bm.mu.RLock()
	defer bm.mu.RUnlock()
	var out []Bookmark
	for _, n := range bm.nodes {
		if n.Parent == folder && n.ID != FolderRoot {
			out = append(out, *n)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Pos != out[j].Pos {
			return out[i].Pos < out[j].Pos
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Search finds bookmarks whose title or URL contains every term.
func (bm *Bookmarks) Search(q string, limit int) []Bookmark {
	terms := strings.Fields(strings.ToLower(q))
	bm.mu.RLock()
	defer bm.mu.RUnlock()
	var out []Bookmark
	for _, n := range bm.nodes {
		if n.Folder {
			continue
		}
		hay := strings.ToLower(n.Title + " " + n.URL)
		ok := true
		for _, t := range terms {
			if !strings.Contains(hay, t) {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, *n)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Added.After(out[j].Added) || (out[i].Added.Equal(out[j].Added) && out[i].ID < out[j].ID)
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// All returns every non-folder bookmark.
func (bm *Bookmarks) All() []Bookmark { return bm.Search("", 0) }
