package agent

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/httptrace"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/http2"
)

// client.go - direct Anthropic Messages API client over net/http.
//
// Why not the SDK: the SDK buffers and re-marshals typed message structs on
// every turn and hides the SSE stream behind an accumulator. The agent needs
// (1) byte-exact control over the serialized prefix (prompt-cache hits depend
// on it), (2) tool_use blocks the instant content_block_stop arrives (early
// dispatch), (3) first-byte / first-token / first-tool timings, and (4) no
// additional dependency in a pure-Go, CGO-free browser binary.

const anthropicVersion = "2023-06-01"

// Usage is the token accounting of one request.
type Usage struct {
	InputTokens         int `json:"input_tokens"`
	OutputTokens        int `json:"output_tokens"`
	CacheCreationTokens int `json:"cache_creation_input_tokens"`
	CacheReadTokens     int `json:"cache_read_input_tokens"`
}

// PromptTokens is every input token the request consumed, cached or not.
func (u Usage) PromptTokens() int { return u.InputTokens + u.CacheCreationTokens + u.CacheReadTokens }

// Block is one completed content block of a streamed assistant message.
type Block struct {
	Index     int
	Type      string // text, tool_use, thinking, redacted_thinking, ...
	Text      string
	ID        string
	Name      string
	Input     json.RawMessage // tool_use: accumulated input (valid JSON object when InputOK)
	InputOK   bool            // false when the streamed JSON was empty-invalid/truncated
	Thinking  string
	Signature string
	Raw       json.RawMessage // canonical block JSON for the conversation history
}

// Message is a (possibly partial, if err != nil) assistant response.
type Message struct {
	ID           string
	Model        string
	StopReason   string
	StopCategory string
	Blocks       []Block // completed blocks, in index order
	Usage        Usage
	Complete     bool // message_stop was received
}

// Metrics are the timings and counters of one Stream call.
type Metrics struct {
	Attempts      int
	Status        int
	ReusedConn    bool
	Proto         string        // e.g. HTTP/2.0
	Headers       time.Duration // request start -> response headers
	FirstByte     time.Duration // request start -> first SSE byte
	FirstToken    time.Duration // request start -> first text delta (0 if none)
	FirstToolCall time.Duration // request start -> first tool_use block start (0 if none)
	Total         time.Duration
	RequestBytes  int
	ResponseBytes int
	Usage         Usage
	StopReason    string
	RequestID     string
}

// Handler receives streaming callbacks on the goroutine that runs Stream.
// Byte slices are only valid during the call.
type Handler struct {
	OnText       func(text []byte)
	OnThinking   func(text []byte)
	OnBlockStart func(index int, typ, id, name string)
	OnBlockStop  func(b *Block) // fires for every completed block, tool_use included
	OnRetry      func(attempt int, wait time.Duration, err error)
}

// APIError is an error returned by the API (HTTP status or SSE error event).
type APIError struct {
	Status     int
	Type       string
	Message    string
	RequestID  string
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	s := fmt.Sprintf("anthropic: %s", e.Type)
	if e.Status != 0 {
		s = fmt.Sprintf("anthropic: HTTP %d %s", e.Status, e.Type)
	}
	if e.Message != "" {
		s += ": " + e.Message
	}
	if e.RequestID != "" {
		s += " (request-id " + e.RequestID + ")"
	}
	return s
}

// Retryable reports whether the same request may succeed later.
func (e *APIError) Retryable() bool {
	switch e.Status {
	case 408, 409, 429, 500, 502, 503, 504, 529:
		return true
	}
	switch e.Type {
	case "overloaded_error", "rate_limit_error", "api_error", "timeout_error":
		return true
	}
	return false
}

// CacheStats aggregates usage across requests.
type CacheStats struct {
	Requests      int   `json:"requests"`
	InputTokens   int64 `json:"inputTokens"`
	CacheRead     int64 `json:"cacheReadTokens"`
	CacheCreation int64 `json:"cacheCreationTokens"`
	OutputTokens  int64 `json:"outputTokens"`
}

// HitRate = cache_read / (cache_read + cache_creation + input).
func (c CacheStats) HitRate() float64 {
	d := c.CacheRead + c.CacheCreation + c.InputTokens
	if d == 0 {
		return 0
	}
	return float64(c.CacheRead) / float64(d)
}

func (c *CacheStats) add(u Usage) {
	c.Requests++
	c.InputTokens += int64(u.InputTokens)
	c.CacheRead += int64(u.CacheReadTokens)
	c.CacheCreation += int64(u.CacheCreationTokens)
	c.OutputTokens += int64(u.OutputTokens)
}

// Sub returns c minus o (for per-run deltas).
func (c CacheStats) Sub(o CacheStats) CacheStats {
	return CacheStats{c.Requests - o.Requests, c.InputTokens - o.InputTokens, c.CacheRead - o.CacheRead,
		c.CacheCreation - o.CacheCreation, c.OutputTokens - o.OutputTokens}
}

// Cost estimates dollars for the given usage. Pricing (Haiku 5.5): $0.10 /
// $0.50 per MTok in/out up to 100K prompt tokens, $0.50 / $2.50 beyond;
// cache reads 0.1x and cache writes 1.25x of the input price (the standard
// 5-minute-TTL multipliers; verify against your invoice).
func CostUSD(u Usage) float64 {
	in, out := 0.10, 0.50
	if u.PromptTokens() > 100_000 {
		in, out = 0.50, 2.50
	}
	return (float64(u.InputTokens)*in + float64(u.CacheReadTokens)*in*0.1 +
		float64(u.CacheCreationTokens)*in*1.25 + float64(u.OutputTokens)*out) / 1e6
}

// Client is a Messages API client. It is safe for concurrent use.
type Client struct {
	cfg Config
	hc  *http.Client
	tr  *http.Transport
	url string

	mu    sync.Mutex
	stats CacheStats
}

// NewClient builds a Client with a tuned transport (HTTP/2, keep-alive, TLS
// session cache) unless cfg.HTTPClient is supplied.
func NewClient(cfg Config) (*Client, error) {
	cfg.applyDefaults()
	if cfg.APIKey == "" {
		return nil, errors.New("agent: no API key configured")
	}
	if cfg.MaxRetries < 0 {
		cfg.MaxRetries = 0
	}
	c := &Client{cfg: cfg, url: strings.TrimRight(cfg.BaseURL, "/") + "/v1/messages"}
	if cfg.HTTPClient != nil {
		c.hc = cfg.HTTPClient
	} else {
		c.tr = newTransport()
		c.hc = &http.Client{Transport: c.tr} // no Timeout: streams are long, ctx governs
	}
	return c, nil
}

func newTransport() *http.Transport {
	d := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           d.DialContext,
		ForceAttemptHTTP2:     true,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12, ClientSessionCache: tls.NewLRUClientSessionCache(16)},
		TLSHandshakeTimeout:   10 * time.Second,
		MaxIdleConns:          4,
		MaxIdleConnsPerHost:   2,
		IdleConnTimeout:       120 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
		ExpectContinueTimeout: 0,
		WriteBufferSize:       64 << 10,
		ReadBufferSize:        64 << 10,
	}
	// Health-check idle HTTP/2 connections so a half-dead socket is replaced
	// before the user's next message instead of stalling it.
	if t2, err := http2.ConfigureTransports(tr); err == nil {
		t2.ReadIdleTimeout = 30 * time.Second
		t2.PingTimeout = 10 * time.Second
	}
	return tr
}

// Close releases idle connections.
func (c *Client) Close() {
	if c.tr != nil {
		c.tr.CloseIdleConnections()
	}
}

// Prewarm opens the TLS (HTTP/2) connection ahead of the first request, e.g.
// when the sidebar opens. It sends an unauthenticated HEAD to the API origin
// (no key leaves the process, no tokens are billed); the response status is
// irrelevant, the point is a pooled, handshaken connection and a cached TLS
// session. It returns the time it took.
func (c *Client) Prewarm(ctx context.Context) (time.Duration, error) {
	t0 := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, strings.TrimRight(c.cfg.BaseURL, "/")+"/", nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", c.userAgent())
	resp, err := c.hc.Do(req)
	if err != nil {
		return 0, c.redactErr(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return time.Since(t0), nil
}

func (c *Client) userAgent() string { return "sweef-agent/1" }

// CacheStats returns the cumulative usage seen by this client.
func (c *Client) CacheStats() CacheStats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stats
}

var keyRe = regexp.MustCompile(`sk-ant-[A-Za-z0-9_\-]{6,}`)

func (c *Client) redact(s string) string {
	if k := c.cfg.APIKey; k != "" {
		s = strings.ReplaceAll(s, k, "[REDACTED]")
	}
	return keyRe.ReplaceAllString(s, "[REDACTED]")
}

func (c *Client) redactErr(err error) error {
	if err == nil {
		return nil
	}
	if msg := err.Error(); c.redact(msg) != msg {
		return errors.New(c.redact(msg))
	}
	return err
}

func (c *Client) sleep(ctx context.Context, d time.Duration) error {
	if c.cfg.Sleep != nil {
		return c.cfg.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func parseRetryAfter(h string) time.Duration {
	h = strings.TrimSpace(h)
	if h == "" {
		return 0
	}
	if f, err := strconv.ParseFloat(h, 64); err == nil && f >= 0 {
		return time.Duration(f * float64(time.Second))
	}
	if t, err := http.ParseTime(h); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

// backoff returns the wait before retry number attempt (0-based): exponential
// with jitter, never less than the server's retry-after.
func (c *Client) backoff(attempt int, retryAfter time.Duration) time.Duration {
	d := c.cfg.RetryBase << uint(attempt)
	if d > 8*time.Second || d <= 0 {
		d = 8 * time.Second
	}
	d = d/2 + time.Duration(rand.Int64N(int64(d/2)+1))
	if retryAfter > d {
		d = retryAfter
	}
	return d
}

// Stream POSTs body (a complete Messages request with stream:true) and parses
// the SSE response, invoking h as blocks arrive. It retries 429/5xx/overloaded
// with backoff (honouring retry-after) as long as nothing has been delivered
// to h yet; after the first content block started a failure is returned with
// the partial Message so the caller can decide. Cancelling ctx aborts the
// stream immediately.
func (c *Client) Stream(ctx context.Context, body []byte, h *Handler) (*Message, *Metrics, error) {
	if h == nil {
		h = &Handler{}
	}
	start := time.Now()
	var m Metrics
	m.RequestBytes = len(body)
	for attempt := 0; ; attempt++ {
		m.Attempts = attempt + 1
		msg, committed, err := c.once(ctx, body, h, start, &m)
		if err == nil {
			m.Total = time.Since(start)
			m.Usage = msg.Usage
			m.StopReason = msg.StopReason
			c.mu.Lock()
			c.stats.add(msg.Usage)
			c.mu.Unlock()
			return msg, &m, nil
		}
		m.Total = time.Since(start)
		if ctx.Err() != nil {
			return msg, &m, ctx.Err()
		}
		var ae *APIError
		retry := false
		if errors.As(err, &ae) {
			retry = ae.Retryable()
		} else {
			retry = (errors.Is(err, io.ErrUnexpectedEOF) || isNetErr(err)) && attempt < 2
		}
		if committed || !retry || attempt >= c.cfg.MaxRetries {
			return msg, &m, err
		}
		var ra time.Duration
		if ae != nil {
			ra = ae.RetryAfter
		}
		if ra > c.cfg.RetryMaxWait {
			return msg, &m, err
		}
		wait := c.backoff(attempt, ra)
		if h.OnRetry != nil {
			h.OnRetry(attempt+1, wait, err)
		}
		c.cfg.logf("retrying in %v: %v", wait, err)
		if serr := c.sleep(ctx, wait); serr != nil {
			return msg, &m, serr
		}
	}
}

func isNetErr(err error) bool {
	var ne net.Error
	if errors.As(err, &ne) {
		return true
	}
	var oe *net.OpError
	return errors.As(err, &oe)
}

type blkState struct {
	typ, id, name string
	text          []byte
	input         []byte
	think         []byte
	sig           []byte
	startRaw      json.RawMessage
	done          bool
}

func (c *Client) once(ctx context.Context, body []byte, h *Handler, start time.Time, m *Metrics) (*Message, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return nil, false, c.redactErr(err)
	}
	req.Header.Set("x-api-key", c.cfg.APIKey)
	req.Header.Set("anthropic-version", anthropicVersion)
	req.Header.Set("content-type", "application/json")
	req.Header.Set("accept", "text/event-stream")
	req.Header.Set("accept-encoding", "identity") // never buffer behind gzip
	req.Header.Set("user-agent", c.userAgent())
	trace := &httptrace.ClientTrace{GotConn: func(i httptrace.GotConnInfo) { m.ReusedConn = i.Reused }}
	req = req.WithContext(httptrace.WithClientTrace(ctx, trace))

	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, false, c.redactErr(err)
	}
	defer resp.Body.Close()
	// Abort a blocked Read the instant ctx is cancelled.
	stop := context.AfterFunc(ctx, func() { resp.Body.Close() })
	defer stop()

	m.Status = resp.StatusCode
	m.Proto = resp.Proto
	m.Headers = time.Since(start)
	m.RequestID = resp.Header.Get("request-id")
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		ae := &APIError{Status: resp.StatusCode, Type: "http_error", RequestID: m.RequestID,
			RetryAfter: parseRetryAfter(resp.Header.Get("retry-after"))}
		var e struct {
			Error struct{ Type, Message string } `json:"error"`
		}
		if json.Unmarshal(b, &e) == nil && e.Error.Type != "" {
			ae.Type, ae.Message = e.Error.Type, c.redact(e.Error.Message)
		} else {
			ae.Message = c.redact(strings.TrimSpace(string(b[:min(len(b), 200)])))
		}
		return nil, false, ae
	}

	cr := &countReader{r: resp.Body, start: start}
	sr := newSSEReader(cr)
	msg := &Message{}
	var blocks []*blkState
	var scratch []byte
	committed := false
	var firstByte, firstToken, firstTool time.Duration

	finish := func(i int) {
		b := blocks[i]
		if b == nil || b.done {
			return
		}
		b.done = true
		blk := c.buildBlock(i, b)
		msg.Blocks = append(msg.Blocks, blk)
		if h.OnBlockStop != nil {
			h.OnBlockStop(&msg.Blocks[len(msg.Blocks)-1])
		}
	}
	defer func() {
		m.FirstByte, m.FirstToken, m.FirstToolCall = firstByte, firstToken, firstTool
		m.ResponseBytes = cr.n
	}()

	for {
		name, data, err := sr.next()
		firstByte = cr.first
		if err != nil {
			if ctx.Err() != nil {
				return msg, committed, ctx.Err()
			}
			if err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
			return msg, committed, c.redactErr(fmt.Errorf("stream: %w", err))
		}
		switch string(name) {
		case "content_block_delta":
			idx, kind, val, ok := parseDelta(data, &scratch)
			if !ok {
				var ev struct {
					Index int `json:"index"`
					Delta struct {
						Type        string `json:"type"`
						Text        string `json:"text"`
						PartialJSON string `json:"partial_json"`
						Thinking    string `json:"thinking"`
						Signature   string `json:"signature"`
					} `json:"delta"`
				}
				if json.Unmarshal(data, &ev) != nil {
					continue
				}
				idx = ev.Index
				switch {
				case ev.Delta.PartialJSON != "":
					kind, val = deltaInputJSON, []byte(ev.Delta.PartialJSON)
				case ev.Delta.Text != "":
					kind, val = deltaText, []byte(ev.Delta.Text)
				case ev.Delta.Thinking != "":
					kind, val = deltaThinking, []byte(ev.Delta.Thinking)
				case ev.Delta.Signature != "":
					kind, val = deltaSignature, []byte(ev.Delta.Signature)
				}
			}
			if idx < 0 || idx >= len(blocks) || blocks[idx] == nil {
				continue // unknown/late delta: tolerate
			}
			b := blocks[idx]
			switch kind {
			case deltaText:
				b.text = append(b.text, val...)
				if firstToken == 0 && len(val) > 0 {
					firstToken = time.Since(start)
				}
				if h.OnText != nil {
					h.OnText(val)
				}
			case deltaInputJSON:
				b.input = append(b.input, val...)
			case deltaThinking:
				b.think = append(b.think, val...)
				if h.OnThinking != nil {
					h.OnThinking(val)
				}
			case deltaSignature:
				b.sig = append(b.sig, val...)
			}
		case "content_block_start":
			var ev struct {
				Index int             `json:"index"`
				Block json.RawMessage `json:"content_block"`
			}
			if json.Unmarshal(data, &ev) != nil || ev.Index < 0 || ev.Index > 1024 {
				continue
			}
			var hd struct {
				Type, ID, Name string
			}
			_ = json.Unmarshal(ev.Block, &hd)
			for len(blocks) <= ev.Index {
				blocks = append(blocks, nil)
			}
			blocks[ev.Index] = &blkState{typ: hd.Type, id: hd.ID, name: hd.Name, startRaw: append(json.RawMessage(nil), ev.Block...)}
			committed = true
			if hd.Type == "tool_use" && firstTool == 0 {
				firstTool = time.Since(start)
			}
			if h.OnBlockStart != nil {
				h.OnBlockStart(ev.Index, hd.Type, hd.ID, hd.Name)
			}
		case "content_block_stop":
			var ev struct {
				Index int `json:"index"`
			}
			if json.Unmarshal(data, &ev) != nil || ev.Index < 0 || ev.Index >= len(blocks) {
				continue
			}
			finish(ev.Index)
		case "message_start":
			var ev struct {
				Message struct {
					ID    string `json:"id"`
					Model string `json:"model"`
					Usage Usage  `json:"usage"`
				} `json:"message"`
			}
			if json.Unmarshal(data, &ev) == nil {
				msg.ID, msg.Model, msg.Usage = ev.Message.ID, ev.Message.Model, ev.Message.Usage
			}
		case "message_delta":
			var ev struct {
				Delta struct {
					StopReason  string `json:"stop_reason"`
					StopDetails *struct {
						Category string `json:"category"`
					} `json:"stop_details"`
				} `json:"delta"`
				Usage map[string]json.RawMessage `json:"usage"`
			}
			if json.Unmarshal(data, &ev) == nil {
				if ev.Delta.StopReason != "" {
					msg.StopReason = ev.Delta.StopReason
				}
				if ev.Delta.StopDetails != nil {
					msg.StopCategory = ev.Delta.StopDetails.Category
				}
				// message_delta usage is cumulative; fields may be absent.
				setInt := func(k string, dst *int) {
					if raw, ok := ev.Usage[k]; ok {
						if n, err := strconv.Atoi(string(raw)); err == nil {
							*dst = n
						}
					}
				}
				setInt("output_tokens", &msg.Usage.OutputTokens)
				setInt("input_tokens", &msg.Usage.InputTokens)
				setInt("cache_creation_input_tokens", &msg.Usage.CacheCreationTokens)
				setInt("cache_read_input_tokens", &msg.Usage.CacheReadTokens)
			}
		case "message_stop":
			// Some blocks may not have seen an explicit stop (defensive).
			for i := range blocks {
				finish(i)
			}
			msg.Complete = true
			// Let the transport reuse the connection (HTTP/1.1 needs the body
			// read to EOF; for HTTP/2 this is a no-op).
			io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
			return msg, committed, nil
		case "error":
			var ev struct {
				Error struct{ Type, Message string } `json:"error"`
			}
			ae := &APIError{Type: "stream_error", RequestID: m.RequestID}
			if json.Unmarshal(data, &ev) == nil && ev.Error.Type != "" {
				ae.Type, ae.Message = ev.Error.Type, c.redact(ev.Error.Message)
			}
			return msg, committed, ae
		default: // ping and any future event type
		}
	}
}

// buildBlock turns accumulated state into a Block with canonical history JSON.
func (c *Client) buildBlock(i int, b *blkState) Block {
	blk := Block{Index: i, Type: b.typ, ID: b.id, Name: b.name}
	switch b.typ {
	case "text":
		blk.Text = string(b.text)
		blk.Raw, _ = json.Marshal(struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}{"text", blk.Text})
	case "tool_use":
		in := bytes.TrimSpace(b.input)
		if len(in) == 0 {
			in = []byte("{}")
		}
		if json.Valid(in) && in[0] == '{' {
			blk.InputOK = true
			blk.Input = append(json.RawMessage(nil), in...)
		} else { // truncated or garbage: keep history valid, flag for is_error
			blk.Input = json.RawMessage("{}")
		}
		blk.Raw, _ = json.Marshal(struct {
			Type  string          `json:"type"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		}{"tool_use", b.id, b.name, blk.Input})
	case "thinking":
		blk.Thinking, blk.Signature = string(b.think), string(b.sig)
		blk.Raw, _ = json.Marshal(struct {
			Type      string `json:"type"`
			Thinking  string `json:"thinking"`
			Signature string `json:"signature"`
		}{"thinking", blk.Thinking, blk.Signature})
	default:
		// redacted_thinking and anything new: replay the start block verbatim.
		blk.Raw = b.startRaw
	}
	return blk
}

type countReader struct {
	r     io.Reader
	n     int
	start time.Time
	first time.Duration // time of the first non-empty read, since start
}

func (c *countReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if n > 0 && c.n == 0 {
		c.first = time.Since(c.start)
	}
	c.n += n
	return n, err
}
