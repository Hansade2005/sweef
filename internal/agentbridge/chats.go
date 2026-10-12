package agentbridge

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sweefbrowser/sweef/internal/app"
)

const (
	chatsFile    = "agent-chats.json"
	maxChats     = 30
	maxChatMsgs  = 60
	maxChatBytes = 20000
)

type chatMsg struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

type chatRec struct {
	ID       string    `json:"id"`
	Title    string    `json:"title"`
	Updated  time.Time `json:"updated"`
	Messages []chatMsg `json:"messages"`
}

// chatStore keeps the transcripts shown in the sidebar's chat list. It stores
// what the user typed and what the assistant said, in the profile directory
// (0600), and nothing for incognito / in-memory profiles. Reopening a chat
// shows its transcript; the model's own context is not restored.
type chatStore struct {
	mu      sync.Mutex
	dir     string // "" = memory only
	chats   []*chatRec
	cur     *chatRec
	loaded  bool
	nextSeq int
}

func (c *chatStore) setDir(dir string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.dir = dir
	c.loaded = false
}

func (c *chatStore) load() {
	if c.loaded {
		return
	}
	c.loaded = true
	if c.dir == "" {
		return
	}
	b, err := os.ReadFile(filepath.Join(c.dir, chatsFile))
	if err != nil {
		return
	}
	var recs []*chatRec
	if json.Unmarshal(b, &recs) == nil {
		c.chats = append(c.chats, recs...)
	}
}

func (c *chatStore) save() {
	if c.dir == "" {
		return
	}
	b, err := json.Marshal(c.chats)
	if err != nil {
		return
	}
	if os.MkdirAll(c.dir, 0o700) != nil {
		return
	}
	tmp, err := os.CreateTemp(c.dir, ".chats.*")
	if err != nil {
		return
	}
	_ = tmp.Chmod(0o600)
	_, werr := tmp.Write(b)
	cerr := tmp.Close()
	if werr != nil || cerr != nil || os.Rename(tmp.Name(), filepath.Join(c.dir, chatsFile)) != nil {
		_ = os.Remove(tmp.Name())
	}
}

// newChat starts an empty conversation (it is listed once it has a message).
func (c *chatStore) newChat() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.load()
	c.cur = nil
}

func (c *chatStore) ensure(first string) *chatRec {
	if c.cur != nil {
		return c.cur
	}
	c.nextSeq++
	t := strings.Join(strings.Fields(first), " ")
	if r := []rune(t); len(r) > 48 {
		t = string(r[:47]) + "…"
	}
	rec := &chatRec{ID: fmt.Sprintf("c%d-%d", time.Now().Unix(), c.nextSeq), Title: t}
	c.cur = rec
	c.chats = append([]*chatRec{rec}, c.chats...)
	if len(c.chats) > maxChats {
		c.chats = c.chats[:maxChats]
	}
	return rec
}

func (c *chatStore) add(role, text, first string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.load()
	rec := c.ensure(first)
	if len(text) > maxChatBytes {
		text = text[:maxChatBytes]
	}
	rec.Messages = append(rec.Messages, chatMsg{role, text})
	if len(rec.Messages) > maxChatMsgs {
		rec.Messages = rec.Messages[len(rec.Messages)-maxChatMsgs:]
	}
	rec.Updated = time.Now()
	c.save()
}

func (c *chatStore) title() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cur != nil {
		return c.cur.Title
	}
	return ""
}

func (c *chatStore) list() []app.AgentChat {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.load()
	out := make([]app.AgentChat, 0, len(c.chats))
	for _, r := range c.chats {
		out = append(out, app.AgentChat{ID: r.ID, Title: r.Title, When: when(r.Updated)})
	}
	return out
}

func when(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d h ago", int(d.Hours()))
	}
	return t.Format("Jan 2")
}

// open makes id the current chat and returns its transcript.
func (c *chatStore) open(id string) (string, []map[string]any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.load()
	for _, r := range c.chats {
		if r.ID == id {
			c.cur = r
			msgs := make([]map[string]any, 0, len(r.Messages))
			for _, m := range r.Messages {
				msgs = append(msgs, map[string]any{"role": m.Role, "text": m.Text})
			}
			return r.Title, msgs, true
		}
	}
	return "", nil, false
}
