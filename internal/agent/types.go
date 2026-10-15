// Package agent is the brain of Sweef's sidebar browser agent: a direct
// Anthropic Messages API client (net/http + own SSE parser), a tool loop with
// early dispatch of streamed tool calls, cache-aware context management and
// prompt-injection defences. It talks to the browser only through Backend.
//
// The agent never imports internal/app or internal/ui; the app implements
// Backend (or uses RPCBackend over an in-process engine) and forwards Event
// values to the chrome page as `agent.event`.
package agent

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// ---- browser vocabulary ----------------------------------------------------

// Step ops accepted by the `act` tool.
const (
	OpNavigate   = "navigate"
	OpClick      = "click"
	OpHover      = "hover"
	OpType       = "type"
	OpPress      = "press"
	OpScroll     = "scroll"
	OpWait       = "wait"
	OpSelect     = "select"
	OpBack       = "back"
	OpForward    = "forward"
	OpExtract    = "extract"
	OpScreenshot = "screenshot"
	OpExpect     = "expect"
)

// Step is one browser action. Which fields matter depends on Op:
//
//	navigate   URL
//	click      Ref
//	hover      Ref
//	type       Ref, Text, [Append], [Submit]
//	press      Key, [Ref]
//	scroll     [Dir up|down], [Amount px]
//	wait       Ms | Text | URL (glob)
//	select     Ref, Value
//	back/forward
//	extract    [Ref]  (text content; default: whole body)
//	screenshot (handled by the agent via Backend.Screenshot)
//	expect     Kind (text|url|title|visible|hidden|count|value), Value, [Ref]
type Step struct {
	Op       string `json:"op"`
	Tab      string `json:"tab,omitempty"` // filled in by the agent
	Ref      string `json:"ref,omitempty"`
	URL      string `json:"url,omitempty"`
	Text     string `json:"text,omitempty"`
	Key      string `json:"key,omitempty"`
	Dir      string `json:"dir,omitempty"`
	Amount   int    `json:"amount,omitempty"`
	Ms       int    `json:"ms,omitempty"`
	Value    string `json:"value,omitempty"`
	Kind     string `json:"kind,omitempty"`
	Append   bool   `json:"append,omitempty"`
	Submit   bool   `json:"submit,omitempty"`
	Optional bool   `json:"optional,omitempty"`
}

// StepResult is the outcome of one Step.
type StepResult struct {
	Index  int    `json:"index"` // 1-based within the Act call
	Op     string `json:"op"`
	OK     bool   `json:"ok"`
	Output string `json:"output,omitempty"` // e.g. extracted text
	Error  string `json:"error,omitempty"`
	Ms     int64  `json:"ms"`
}

// Result is what Backend.Act returns for a batch of steps.
type Result struct {
	OK         bool         `json:"ok"`
	Steps      []StepResult `json:"steps"`
	URL        string       `json:"url,omitempty"`
	Title      string       `json:"title,omitempty"`
	Screenshot []byte       `json:"-"`
	TotalMs    int64        `json:"totalMs"`
}

// TabInfo describes one browser tab.
type TabInfo struct {
	ID     string `json:"id"`
	URL    string `json:"url"`
	Title  string `json:"title"`
	Active bool   `json:"active"`
}

// ConfirmKind classifies why the agent needs the user's approval.
type ConfirmKind string

const (
	ConfirmPayment     ConfirmKind = "payment"
	ConfirmMessage     ConfirmKind = "message"
	ConfirmDelete      ConfirmKind = "delete"
	ConfirmCredentials ConfirmKind = "credentials"
	ConfirmCrossDomain ConfirmKind = "cross_domain"
	ConfirmOther       ConfirmKind = "other"
)

// ConfirmRequest asks the user to approve one risky action.
type ConfirmRequest struct {
	RunID   string      `json:"runId"`
	Kind    ConfirmKind `json:"kind"`
	Summary string      `json:"summary"` // human readable, e.g. "Click 'Place order'"
	URL     string      `json:"url,omitempty"`
	Detail  string      `json:"detail,omitempty"`
}

// ---- events ----------------------------------------------------------------

// EventKind mirrors the `kind` of the chrome page's agent.event message.
type EventKind string

const (
	EventText    EventKind = "text"    // assistant text delta
	EventThought EventKind = "thought" // thought summary delta (only if Config.ThoughtSummaries)
	EventAction  EventKind = "action"  // human readable label, e.g. "Clicking 'Add to cart'"
	EventStatus  EventKind = "status"  // progress / retry / pause notices
	EventConfirm EventKind = "confirm" // an approval is being requested (answered through Backend.Confirm)
	EventDone    EventKind = "done"
	EventError   EventKind = "error"
)

// Event is what the app forwards to the chrome page.
type Event struct {
	RunID   string          `json:"runId"`
	Kind    EventKind       `json:"kind"`
	Text    string          `json:"text,omitempty"`
	Tool    string          `json:"tool,omitempty"`
	Confirm *ConfirmRequest `json:"confirm,omitempty"`
	Stats   *RunStats       `json:"stats,omitempty"`
}

// ---- configuration ---------------------------------------------------------

// ThinkingMode selects the request's thinking configuration.
type ThinkingMode string

const (
	ThinkingAdaptive ThinkingMode = "adaptive" // default (Haiku 5.5 thinks by default)
	ThinkingDisabled ThinkingMode = "disabled" // only valid at effort <= high
)

// Config configures a Client and an Agent. The zero value plus an APIKey is
// valid; defaults are applied by New / NewClient.
type Config struct {
	APIKey  string `json:"-"` // never logged; redacted from every error
	BaseURL string // default https://api.anthropic.com
	Model   string // default claude-haiku-5-5

	Effort           string       // output_config.effort; default "low"
	Thinking         ThinkingMode // default adaptive
	ThoughtSummaries bool         // request thinking.display = "summarized" and emit EventThought
	MaxTokens        int          // per response; default 8192 (thinking tokens count against it; always sent explicitly)

	// Run budgets.
	MaxTurns        int     // default 40
	MaxCostUSD      float64 // default 0.50 per run
	MaxOutputTokens int     // default 60000 per run
	MaxActions      int     // browser steps per run; default 200
	MaxNavigations  int     // navigations per run; default 40
	MaxStepsPerAct  int     // default 30

	// Context management.
	SnapshotMaxTokens int // cap per snapshot; default 6000
	ExtractMaxChars   int // cap per extract result; default 8000
	// NoInitialObservation: do not put an observation of the current tab next to the task. By default the
	// first user message of a run carries the snapshot (or the delta, on a page already read) of the page the
	// run starts on, which saves the read_page round trip almost every task began with.
	NoInitialObservation bool
	Compact              CompactConfig

	// Transport.
	HTTPClient   *http.Client                               // optional override
	MaxRetries   int                                        // default 4 (5 attempts)
	RetryBase    time.Duration                              // default 400ms
	RetryMaxWait time.Duration                              // largest retry-after honoured; default 30s
	Sleep        func(context.Context, time.Duration) error // test hook

	Logf func(format string, args ...any) // optional debug sink; never sees the key
}

// Format keeps the API key out of %v / %+v / %#v dumps.
func (c Config) Format(f fmt.State, verb rune) {
	key := c.APIKey
	if key != "" {
		c.APIKey = "[REDACTED]"
	}
	type plain Config
	if verb == 'v' && f.Flag('+') {
		fmt.Fprintf(f, "%+v", plain(c))
		return
	}
	fmt.Fprintf(f, "%v", plain(c))
}

// DefaultModel is the model the agent targets.
const DefaultModel = "claude-haiku-5-5"

// DefaultBaseURL is the Messages API origin.
const DefaultBaseURL = "https://api.anthropic.com"

func (c *Config) applyDefaults() {
	if c.BaseURL == "" {
		c.BaseURL = DefaultBaseURL
	}
	if c.Model == "" {
		c.Model = DefaultModel
	}
	if c.Effort == "" {
		c.Effort = "low"
	}
	if c.Thinking == "" {
		c.Thinking = ThinkingAdaptive
	}
	if c.MaxTokens <= 0 {
		c.MaxTokens = 8192
	}
	if c.MaxTurns <= 0 {
		c.MaxTurns = 40
	}
	if c.MaxCostUSD <= 0 {
		c.MaxCostUSD = 0.50
	}
	if c.MaxOutputTokens <= 0 {
		c.MaxOutputTokens = 60000
	}
	if c.MaxActions <= 0 {
		c.MaxActions = 200
	}
	if c.MaxNavigations <= 0 {
		c.MaxNavigations = 40
	}
	if c.MaxStepsPerAct <= 0 {
		c.MaxStepsPerAct = 30
	}
	if c.SnapshotMaxTokens <= 0 {
		c.SnapshotMaxTokens = 6000
	}
	if c.ExtractMaxChars <= 0 {
		c.ExtractMaxChars = 8000
	}
	c.Compact.applyDefaults()
	if c.MaxRetries == 0 {
		c.MaxRetries = 4
	}
	if c.RetryBase <= 0 {
		c.RetryBase = 400 * time.Millisecond
	}
	if c.RetryMaxWait <= 0 {
		c.RetryMaxWait = 30 * time.Second
	}
}

func (c *Config) logf(format string, args ...any) {
	if c.Logf != nil {
		c.Logf(format, args...)
	}
}
