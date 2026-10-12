package agent

import "context"

// Backend is everything the agent needs from the browser. internal/app
// implements it on top of the engine; RPCBackend is a ready implementation
// over an in-process *engine.Engine and the rpc Batch runner.
//
// Concurrency: the agent may call Snapshot/URL/Title/Tabs from several
// goroutines at once (parallel read-only tools, early dispatch), but never
// issues two Act calls for the same tab concurrently. Implementations must
// be safe for concurrent use.
//
// Cancellation: Act and Confirm receive the run context. When it is
// cancelled (user pressed stop) they should return promptly.
type Backend interface {
	// Snapshot returns the accessibility snapshot (snapshot v2 YAML, nodes
	// carry [ref=eN]) of a tab. An empty tabID means the active tab. With
	// delta=true a backend MAY return a compact diff against the previous
	// snapshot it returned (it must then start with DeltaPrefix); the agent
	// core itself always asks for full snapshots and diffs them against the
	// text the model has actually seen, so that find/read_page calls can never
	// desynchronise a backend-side baseline.
	Snapshot(tabID string, delta bool) (string, error)

	// Act runs an ordered batch of steps (each carries its Tab) and returns
	// per-step results. It stops at the first failing non-optional step.
	// OpScreenshot steps are never sent to Act; the agent calls Screenshot.
	Act(ctx context.Context, steps []Step) (Result, error)

	Tabs() []TabInfo
	OpenTab(url string) (TabInfo, error)
	SwitchTab(id string) error
	CloseTab(id string) error

	URL(tabID string) string
	Title(tabID string) string
	Screenshot(tabID string) ([]byte, error)

	// Confirm blocks until the user approves or rejects req (the agent has
	// already emitted an EventConfirm for the UI). A non-nil error counts as a
	// rejection.
	Confirm(ctx context.Context, req ConfirmRequest) (bool, error)
}

// UserAsker is an optional Backend extension: when implemented, the ask_user
// tool blocks for the user's free-form answer instead of ending the run (the
// default chat flow, where the question is shown and the next user message is
// the answer).
type UserAsker interface {
	Ask(ctx context.Context, question string, options []string) (string, error)
}

// DeltaPrefix starts every delta snapshot (see diff.go).
const DeltaPrefix = "# delta"
