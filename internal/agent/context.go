package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// context.go - conversation state, request serialization and compaction.
//
// # Cache design (prefix order: tools -> system -> messages)
//
// Four breakpoints at most:
//
//	1. last tool           (tools are static)
//	2. system prompt       (static)
//	3. the last stable block of the PREVIOUS request  (guarantees a cache read
//	   point even if the 20-block lookback of breakpoint 4 would miss)
//	4. the last stable block of THIS request          (writes the cache the
//	   next turn reads)
//
// "Stable" means history only. Everything volatile (the task-memory scratchpad,
// turn counter, the current tab) is rendered as one extra text block appended
// to the last user message at request time, AFTER breakpoint 4, and is not
// stored in history. The next turn's history therefore still begins with the
// bytes this turn's breakpoint covered.
//
// Blocks are stored pre-serialized; building a request is a sequence of
// memcpys. cache_control is injected at write time on the chosen blocks only,
// so moving the breakpoint never changes the stored bytes.
//
// # Compaction cost model
//
// A request's price is roughly  in*(fresh) + 0.1*in*(cache read) + 1.25*in*
// (cache write). With Haiku 5.5 at $0.10/MTok input a 40K-token history read
// from cache costs $0.0004 per turn, so compaction is not about the bill
// until the prompt nears the 100K-token price tier ($0.50/MTok beyond); it is
// about time-to-first-token (prefill grows with prompt length), context-window
// headroom and the quality of attention over a smaller prompt. But a
// compaction rewrites old tool results, which invalidates the cache from the
// first rewritten block to the end: that turn pays a one-off cache WRITE of
// the whole suffix (1.25x) instead of a read (0.1x), roughly 12x the usual
// per-token cost of one turn, after which the new, smaller prefix is warm
// again. Therefore compaction:
//   - runs at a FIXED boundary: only at turns that are multiples of Every, and
//     it only touches observations older than KeepRecentTurns behind the
//     boundary, so between compactions the history is strictly append-only;
//   - runs at most once per Every turns and only when the prompt is big
//     enough (MinPromptTokens) that the saving repays the rewrite;
//   - stage 1 only elides observations that a newer FULL snapshot of the same
//     tab supersedes (plus screenshots), keeping the delta chain intact;
//     stage 2 (prompt >= HardPromptTokens) elides everything old and forces
//     the next observation of those tabs to be a full snapshot;
//   - on the compaction turn the "previous request" breakpoint is dropped
//     because it would point into rewritten content.

// CompactConfig tunes context compaction.
type CompactConfig struct {
	Disabled         bool
	Every            int // boundary granularity in turns; default 8
	KeepRecentTurns  int // observations from the last N turns are never elided; default 2
	MinPromptTokens  int // below this prompt size compaction is not worth a cache rewrite; default 24000
	HardPromptTokens int // at/above this, elide every old observation; default 60000
}

func (c *CompactConfig) applyDefaults() {
	if c.Every <= 0 {
		c.Every = 8
	}
	if c.KeepRecentTurns <= 0 {
		c.KeepRecentTurns = 2
	}
	if c.MinPromptTokens <= 0 {
		c.MinPromptTokens = 24000
	}
	if c.HardPromptTokens <= 0 {
		c.HardPromptTokens = 60000
	}
}

type blockKind uint8

const (
	bText blockKind = iota
	bToolUse
	bThinking
	bToolResult
)

type block struct {
	raw  []byte // JSON object ending in '}'
	kind blockKind
	tr   *toolResult
}

type message struct {
	role   string
	blocks []*block
}

type partKind uint8

const (
	partText partKind = iota
	partObs
	partImage
	partExtract
)

// part is one element of a tool_result's content array.
type part struct {
	kind   partKind
	text   string // text parts (observations are already wrapped as untrusted)
	img    string // base64 PNG
	tab    string
	url    string
	step   int  // action counter when created (for stubs and ordering)
	turn   int  // assistant turn that produced it
	full   bool // observation: full snapshot (a baseline) rather than a delta
	tokens int
	elided bool
	stub   string
	owner  *block
}

type toolResult struct {
	id    string
	isErr bool
	parts []*part
}

// baseline is what the model currently holds of a tab.
type baseline struct {
	url   string
	text  string // the (truncated) snapshot text the model has, kept current by deltas
	valid bool
}

type breakpoint struct {
	msg, blk int
	ok       bool
}

type convo struct {
	cfg  CompactConfig
	msgs []*message
	// pending blocks (text) are merged into the next user message.
	pending []string

	turn int // assistant messages so far
	step int // browser actions so far (used in stubs)

	obs          []*part
	baselines    map[string]*baseline
	lastBP       breakpoint
	lastBoundary int
	compacted    bool // a compaction happened since the last request
	head         []byte
	buf          bytes.Buffer

	// stats
	Compactions   int
	ElidedTokens  int
	StubbedParts  int
	LastBodyBytes int
}

func newConvo(cfg CompactConfig, head []byte) *convo {
	cfg.applyDefaults()
	return &convo{cfg: cfg, head: head, baselines: map[string]*baseline{}}
}

// ---- JSON helpers -----------------------------------------------------------

// jsonString encodes s as a JSON string without HTML escaping.
func jsonString(s string) []byte {
	var b bytes.Buffer
	b.Grow(len(s) + 2)
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	out := b.Bytes()
	return out[:len(out)-1]
}

func textBlockRaw(s string) []byte {
	out := make([]byte, 0, len(s)+32)
	out = append(out, `{"type":"text","text":`...)
	out = append(out, jsonString(s)...)
	return append(out, '}')
}

func (b *block) rerender() {
	tr := b.tr
	var o bytes.Buffer
	o.WriteString(`{"type":"tool_result","tool_use_id":`)
	o.Write(jsonString(tr.id))
	if tr.isErr {
		o.WriteString(`,"is_error":true`)
	}
	o.WriteString(`,"content":[`)
	first := true
	for _, p := range tr.parts {
		if !first {
			o.WriteByte(',')
		}
		first = false
		switch {
		case p.elided:
			o.Write(textBlockRaw(p.stub))
		case p.kind == partImage:
			o.WriteString(`{"type":"image","source":{"type":"base64","media_type":"image/png","data":"`)
			o.WriteString(p.img)
			o.WriteString(`"}}`)
		default:
			o.Write(textBlockRaw(p.text))
		}
	}
	if first { // an empty content array is legal but be explicit
		o.Write(textBlockRaw("(no output)"))
	}
	o.WriteString(`]}`)
	b.raw = o.Bytes()
}

// ---- appending history --------------------------------------------------------

func (c *convo) lastMsg() *message {
	if len(c.msgs) == 0 {
		return nil
	}
	return c.msgs[len(c.msgs)-1]
}

// addPending queues text that is merged into the next user message (after
// any tool results, which must come first).
// hasAssistant reports whether the history holds a model turn yet.
func (c *convo) hasAssistant() bool {
	for _, m := range c.msgs {
		if m.role == "assistant" {
			return true
		}
	}
	return false
}

func (c *convo) addPending(text string) { c.pending = append(c.pending, text) }

func (c *convo) flushPending(m *message) {
	for _, t := range c.pending {
		m.blocks = append(m.blocks, &block{raw: textBlockRaw(t), kind: bText})
	}
	c.pending = c.pending[:0]
}

// addUserText appends the user's message. If the history already ends with a
// user message (a finished run whose tool results are the last thing), the
// text joins it: appending after the last block keeps every cached byte valid.
func (c *convo) addUserText(text string) {
	m := c.lastMsg()
	if m == nil || m.role != "user" {
		m = &message{role: "user"}
		c.msgs = append(c.msgs, m)
	}
	c.flushPending(m)
	m.blocks = append(m.blocks, &block{raw: textBlockRaw(text), kind: bText})
}

// addAssistant stores the assistant message built from completed blocks.
// Empty text blocks are rejected by the API, so they are dropped.
func (c *convo) addAssistant(blocks []Block) {
	m := &message{role: "assistant"}
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if strings.TrimSpace(b.Text) == "" {
				continue
			}
			m.blocks = append(m.blocks, &block{raw: b.Raw, kind: bText})
		case "tool_use":
			m.blocks = append(m.blocks, &block{raw: b.Raw, kind: bToolUse})
		case "thinking", "redacted_thinking":
			m.blocks = append(m.blocks, &block{raw: b.Raw, kind: bThinking})
		default:
			if len(b.Raw) > 0 {
				m.blocks = append(m.blocks, &block{raw: b.Raw, kind: bText})
			}
		}
	}
	if len(m.blocks) == 0 {
		m.blocks = append(m.blocks, &block{raw: textBlockRaw("(no output)"), kind: bText})
	}
	c.msgs = append(c.msgs, m)
	c.turn++
}

// addToolResults appends one user message holding all results (parallel tool
// calls must be answered in a single message).
func (c *convo) addToolResults(trs []*toolResult) {
	m := &message{role: "user"}
	for _, tr := range trs {
		b := &block{kind: bToolResult, tr: tr}
		for _, p := range tr.parts {
			p.owner = b
			p.turn = c.turn
			if p.kind == partObs || p.kind == partImage || p.kind == partExtract {
				c.obs = append(c.obs, p)
			}
		}
		b.rerender()
		m.blocks = append(m.blocks, b)
	}
	c.flushPending(m)
	c.msgs = append(c.msgs, m)
}

// dropLastAssistant removes a trailing assistant message (used when a response
// must not enter history, e.g. a refusal).
func (c *convo) dropLastAssistant() {
	if m := c.lastMsg(); m != nil && m.role == "assistant" {
		c.msgs = c.msgs[:len(c.msgs)-1]
		c.turn--
	}
}

// ---- request building -----------------------------------------------------------

// body serializes the request. tail is volatile text appended to the last user
// message after the final breakpoint.
func (c *convo) body(tail string) []byte {
	c.buf.Reset()
	c.buf.Write(c.head)
	last := len(c.msgs) - 1
	stable := breakpoint{msg: last, blk: len(c.msgs[last].blocks) - 1, ok: true}
	prev := c.lastBP
	if c.compacted || !prev.ok || prev == stable || prev.msg > last || prev.blk >= len(c.msgs[prev.msg].blocks) {
		prev.ok = false
	}
	for mi, m := range c.msgs {
		if mi > 0 {
			c.buf.WriteByte(',')
		}
		c.buf.WriteString(`{"role":"`)
		c.buf.WriteString(m.role)
		c.buf.WriteString(`","content":[`)
		for bi, b := range m.blocks {
			if bi > 0 {
				c.buf.WriteByte(',')
			}
			if (prev.ok && prev.msg == mi && prev.blk == bi) || (stable.msg == mi && stable.blk == bi) {
				c.buf.Write(b.raw[:len(b.raw)-1])
				c.buf.WriteString(cacheCtl)
			} else {
				c.buf.Write(b.raw)
			}
		}
		if mi == last && tail != "" {
			c.buf.WriteByte(',')
			c.buf.Write(textBlockRaw(tail))
		}
		c.buf.WriteString(`]}`)
	}
	c.buf.WriteString(`]}`)
	c.lastBP = stable
	c.compacted = false
	out := make([]byte, c.buf.Len())
	copy(out, c.buf.Bytes())
	c.LastBodyBytes = len(out)
	return out
}

// buildHead renders everything before "messages" content: model, limits,
// thinking, effort, tools (cached) and system (cached). It is computed once
// per agent, so it is byte-identical on every request by construction.
func buildHead(cfg *Config) []byte {
	var b bytes.Buffer
	b.WriteString(`{"model":`)
	b.Write(jsonString(cfg.Model))
	b.WriteString(`,"max_tokens":` + strconv.Itoa(cfg.MaxTokens))
	b.WriteString(`,"stream":true`)
	switch cfg.Thinking {
	case ThinkingDisabled:
		b.WriteString(`,"thinking":{"type":"disabled"}`)
	default:
		if cfg.ThoughtSummaries {
			b.WriteString(`,"thinking":{"type":"adaptive","display":"summarized"}`)
		} else {
			b.WriteString(`,"thinking":{"type":"adaptive"}`)
		}
	}
	if cfg.Effort != "" {
		b.WriteString(`,"output_config":{"effort":`)
		b.Write(jsonString(cfg.Effort))
		b.WriteByte('}')
	}
	b.WriteString(`,"tools":`)
	b.Write(toolsJSONCached)
	b.WriteString(`,"system":[{"type":"text","text":`)
	b.Write(jsonString(systemPrompt))
	b.WriteString(`,"cache_control":{"type":"ephemeral"}}],"messages":[`)
	return b.Bytes()
}

// ---- observations & baselines --------------------------------------------------------

func (c *convo) baselineFor(tab string) *baseline {
	b := c.baselines[tab]
	if b == nil {
		b = &baseline{}
		c.baselines[tab] = b
	}
	return b
}

func (c *convo) invalidateAllBaselines() {
	for _, b := range c.baselines {
		b.valid = false
	}
}

// newObservation builds the part for a snapshot the model is about to see.
func (c *convo) newObservation(tab, pageURL, wrapped string, full bool) *part {
	c.step++
	return &part{kind: partObs, text: wrapped, tab: tab, url: pageURL, step: c.step, full: full,
		tokens: EstimateTokens(wrapped), stub: fmt.Sprintf("[page snapshot of %s at step %d elided; re-read with read_page]", clip(pageURL, 120), c.step)}
}

func (c *convo) newExtract(tab, pageURL, wrapped, ref string) *part {
	c.step++
	return &part{kind: partExtract, text: wrapped, tab: tab, url: pageURL, step: c.step, tokens: EstimateTokens(wrapped),
		stub: fmt.Sprintf("[text extracted from %s at step %d elided (%d tokens); extract again if needed]", clip(pageURL, 120), c.step, EstimateTokens(wrapped))}
}

func (c *convo) newImage(tab, pageURL, b64 string) *part {
	c.step++
	return &part{kind: partImage, img: b64, tab: tab, url: pageURL, step: c.step, tokens: len(b64) / 1000,
		stub: fmt.Sprintf("[screenshot of %s at step %d elided]", clip(pageURL, 120), c.step)}
}

// ---- compaction -----------------------------------------------------------------------

// maybeCompact applies the policy described at the top of the file. promptTokens
// is the prompt size reported by the last response. It returns true if history
// was rewritten.
func (c *convo) maybeCompact(promptTokens int) bool {
	if c.cfg.Disabled {
		return false
	}
	boundary := ((c.turn - c.cfg.KeepRecentTurns) / c.cfg.Every) * c.cfg.Every
	if boundary <= 0 || boundary <= c.lastBoundary || promptTokens < c.cfg.MinPromptTokens {
		return false
	}
	hard := promptTokens >= c.cfg.HardPromptTokens
	// newest live full snapshot per tab
	latestFull := map[string]int{}
	for _, p := range c.obs {
		if p.kind == partObs && p.full && !p.elided && p.step > latestFull[p.tab] {
			latestFull[p.tab] = p.step
		}
	}
	touched := map[*block]bool{}
	saved := 0
	elide := func(p *part) {
		p.elided = true
		touched[p.owner] = true
		saved += p.tokens - EstimateTokens(p.stub)
		c.StubbedParts++
	}
	for _, p := range c.obs {
		if p.elided || p.turn > boundary {
			continue
		}
		switch p.kind {
		case partImage:
			elide(p)
		case partObs:
			if p.step < latestFull[p.tab] { // superseded by a newer full snapshot
				elide(p)
			} else if hard {
				elide(p)
				if b := c.baselines[p.tab]; b != nil {
					b.valid = false // the model no longer holds this page
				}
			}
		case partExtract:
			if hard || p.tokens > 1500 {
				elide(p)
			}
		}
	}
	c.lastBoundary = boundary
	if len(touched) == 0 {
		return false
	}
	for b := range touched {
		b.rerender()
	}
	c.Compactions++
	c.ElidedTokens += saved
	c.compacted = true
	return true
}

// stripThinking removes thinking blocks from history. It is the recovery path
// when the API rejects replayed thinking after the history was edited.
func (c *convo) stripThinking() int {
	n := 0
	for _, m := range c.msgs {
		if m.role != "assistant" {
			continue
		}
		kept := m.blocks[:0]
		for _, b := range m.blocks {
			if b.kind == bThinking {
				n++
				continue
			}
			kept = append(kept, b)
		}
		m.blocks = kept
		if len(m.blocks) == 0 {
			m.blocks = append(m.blocks, &block{raw: textBlockRaw("(no output)"), kind: bText})
		}
	}
	if n > 0 {
		c.compacted = true
	}
	return n
}

// reset starts a fresh conversation (keeps the static head).
func (c *convo) reset() {
	c.msgs = nil
	c.pending = nil
	c.turn, c.step = 0, 0
	c.obs = nil
	c.baselines = map[string]*baseline{}
	c.lastBP = breakpoint{}
	c.lastBoundary = 0
	c.compacted = false
}
