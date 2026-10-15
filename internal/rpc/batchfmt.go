package rpc

import (
	"encoding/json"

	"github.com/sweefbrowser/sweef/internal/batchfmt"
)

// FormatBatch renders a Batch.run result as the step report the session CLI prints and the MCP browser_batch
// tool returns (batchfmt.Format, which the engine-free session client uses too).
func FormatBatch(raw json.RawMessage, maxResult int) (text string, ok bool) {
	return batchfmt.Format(raw, maxResult)
}
